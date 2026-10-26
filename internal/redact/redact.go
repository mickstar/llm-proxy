// Package redact implements reversible substitution of sensitive values.
//
// A rule matches a pattern in a request string value and replaces every match
// with a template-derived alias. The alias -> matched-text mapping is kept in a
// process-local Table and responses are scanned for aliases so the client sees
// the original values again. Nothing is written to disk: restarting the server
// forgets every mapping.
//
// Templates expand $R to a per-match token derived from the value, and $0..$9 to
// capture groups. A template without $R is a fixed substitution and is restored
// exactly as written.
package redact

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mickstar/llm-proxy/internal/config"
)

// ErrNotJSON reports a body that cannot be walked.
var ErrNotJSON = errors.New("redact: body is not JSON")

// DefaultLimit bounds how many distinct aliases one process remembers.
const DefaultLimit = 10000

// Instance is one recorded alias, as listed by the control API and the TUI.
type Instance struct {
	Alias     string    `json:"alias"`
	Original  string    `json:"original"`
	Rule      string    `json:"rule"`
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	// Conflict marks a fixed template that mapped two different originals to
	// the same alias; the first original is the one restored.
	Conflict bool `json:"conflict,omitempty"`
}

// Count reports how many replacements one rule performed in one body.
type Count struct {
	Rule     string `json:"rule"`
	Replaced int    `json:"replaced"`
}

// Rule is one compiled redaction.
type Rule struct {
	name     string
	re       *regexp.Regexp
	template string
}

// Set is an immutable compiled snapshot of the enabled redactions.
type Set struct {
	rules []Rule
}

// Compile compiles the enabled redactions, preserving configured order.
func Compile(cfg []config.Redaction) (*Set, error) {
	s := &Set{}
	for _, r := range cfg {
		if !r.Enabled {
			continue
		}
		re, err := config.CompilePattern(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("redaction %q: pattern: %w", r.Name, err)
		}
		s.rules = append(s.rules, Rule{name: r.Name, re: re, template: r.Template})
	}
	return s, nil
}

// Len returns the number of compiled (enabled) rules.
func (s *Set) Len() int { return len(s.rules) }

// Result is the outcome of one ApplyBody pass.
type Result struct {
	Changed bool    // at least one replacement altered the body
	Body    []byte  // re-marshaled body, non-nil ONLY when Changed
	Counts  []Count // one entry per rule that replaced something
	Total   int     // replacements performed
}

// Summary renders the counts as "rule x2, other x1".
func (r *Result) Summary() string {
	if r == nil || r.Total == 0 {
		return ""
	}
	parts := make([]string, 0, len(r.Counts))
	for _, c := range r.Counts {
		parts = append(parts, fmt.Sprintf("%s x%d", c.Rule, c.Replaced))
	}
	return strings.Join(parts, ", ")
}

type state struct {
	counts  []int
	changed bool
	total   int
}

// ApplyBody replaces every redaction match in the string values of the JSON
// body and records the aliases in t. Object keys are never scanned. It returns
// ErrNotJSON when body is not valid JSON.
func (s *Set) ApplyBody(body []byte, t *Table) (*Result, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotJSON, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing data after JSON value", ErrNotJSON)
	}

	st := &state{counts: make([]int, len(s.rules))}
	v = s.applyValue(v, t, st)

	out := &Result{Changed: st.changed, Total: st.total}
	for i, n := range st.counts {
		if n > 0 {
			out.Counts = append(out.Counts, Count{Rule: s.rules[i].name, Replaced: n})
		}
	}
	if !st.changed {
		return out, nil
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("redact: marshal body: %w", err)
	}
	out.Body = bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	return out, nil
}

// applyValue walks v recursively, returning the possibly-rewritten value.
func (s *Set) applyValue(v any, t *Table, st *state) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			x[k] = s.applyValue(val, t, st)
		}
		return x
	case []any:
		for i, val := range x {
			x[i] = s.applyValue(val, t, st)
		}
		return x
	case string:
		return s.applyString(x, t, st)
	}
	return v
}

// applyString runs every rule over one string, each seeing the previous rule's
// output. Text that is already a known alias is left alone so a client echoing
// an alias cannot trigger a second round of substitution.
func (s *Set) applyString(str string, t *Table, st *state) string {
	for i := range s.rules {
		r := &s.rules[i]
		matches := r.re.FindAllStringSubmatchIndex(str, -1)
		if len(matches) == 0 {
			continue
		}
		var b strings.Builder
		last := 0
		replaced := 0
		for _, m := range matches {
			if m[0] < last {
				continue
			}
			match := str[m[0]:m[1]]
			if t.IsAlias(match) {
				continue
			}
			alias := Expand(r.template, str, m, t.Token(r.name, match))
			t.Add(r.name, alias, match)
			b.WriteString(str[last:m[0]])
			b.WriteString(alias)
			last = m[1]
			replaced++
		}
		if replaced == 0 {
			continue
		}
		b.WriteString(str[last:])
		str = b.String()
		st.counts[i] += replaced
		st.total += replaced
		st.changed = true
	}
	return str
}

// Expand substitutes $R and $0..$9 in tmpl using the match indexes m over src.
func Expand(tmpl, src string, m []int, token string) string {
	var b strings.Builder
	b.Grow(len(tmpl) + 8)
	for i := 0; i < len(tmpl); i++ {
		c := tmpl[i]
		if c != '$' || i+1 >= len(tmpl) {
			b.WriteByte(c)
			continue
		}
		switch n := tmpl[i+1]; {
		case n == 'R':
			b.WriteString(token)
			i++
		case n >= '0' && n <= '9':
			g := int(n - '0')
			if 2*g+1 < len(m) && m[2*g] >= 0 {
				b.WriteString(src[m[2*g]:m[2*g+1]])
			}
			i++
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Table is the process-local alias registry. It is safe for concurrent use.
type Table struct {
	mu      sync.RWMutex
	byAlias map[string]*Instance
	order   []string
	limit   int
	version uint64
	salt    string
	evicted int
}

// NewTable returns a table that remembers at most limit aliases (zero uses
// DefaultLimit). The salt is per-process, so aliases differ between runs.
func NewTable(limit int) *Table {
	if limit <= 0 {
		limit = DefaultLimit
	}
	return &Table{
		byAlias: make(map[string]*Instance, 64),
		limit:   limit,
		salt:    rand.Text(),
	}
}

// Len reports how many aliases are remembered.
func (t *Table) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.byAlias)
}

// IsAlias reports whether s is a remembered alias.
func (t *Table) IsAlias(s string) bool {
	t.mu.RLock()
	_, ok := t.byAlias[s]
	t.mu.RUnlock()
	return ok
}

// List returns a copy of the recorded instances in insertion order.
func (t *Table) List() []Instance {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]Instance, 0, len(t.order))
	for _, a := range t.order {
		if inst, ok := t.byAlias[a]; ok {
			out = append(out, *inst)
		}
	}
	return out
}

// Version increments on every newly recorded alias, so a cached matcher knows
// when to rebuild.
func (t *Table) Version() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.version
}

// Token derives a stable six-character token for a matched value. The same
// value yields the same token for the process lifetime.
func (t *Table) Token(rule string, match string) string {
	mac := hmac.New(sha256.New, []byte(t.salt))
	mac.Write([]byte(rule))
	mac.Write([]byte{0})
	mac.Write([]byte(match))
	sum := mac.Sum(nil)
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:4])
	return strings.ToLower(enc[:6])
}

// Add records alias -> original. A repeated alias only bumps its count.
func (t *Table) Add(rule, alias, original string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if inst, ok := t.byAlias[alias]; ok {
		inst.Count++
		if inst.Original != original && !inst.Conflict {
			inst.Conflict = true
		}
		return
	}
	if len(t.byAlias) >= t.limit && len(t.order) > 0 {
		oldest := t.order[0]
		t.order = t.order[1:]
		delete(t.byAlias, oldest)
		t.evicted++
	}
	t.byAlias[alias] = &Instance{
		Alias:     alias,
		Original:  original,
		Rule:      rule,
		Count:     1,
		FirstSeen: time.Now(),
	}
	t.order = append(t.order, alias)
	t.version++
}
