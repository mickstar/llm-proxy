// Package filter compiles the configured content-filter rules into an immutable
// snapshot and applies them to the string values of JSON request bodies. A rule's
// pattern is either inline RE2 or the verbatim contents of one or more files named
// by pattern_file (config.CompileFilter).
//
// Body values are walked untyped so every field survives, numbers decode as
// json.Number and re-marshal byte-identically, and object keys are never
// scanned. A body is re-marshaled only when a replacement actually altered a
// string, so callers forward untouched bodies verbatim.
package filter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/mickstar/llm-proxy/internal/config"
	"github.com/mickstar/llm-proxy/internal/redact"
)

// ErrNotJSON reports a body that cannot be content-filtered.
var ErrNotJSON = errors.New("filter: body is not JSON")

// FilterCount reports matches for one enabled filter, in configured order.
type FilterCount struct {
	Name   string `json:"name"`
	Action string `json:"action"`
	Count  int    `json:"count"`
}

// Outcome is the result of applying a Set to a JSON request body.
type Outcome struct {
	Blocked       bool          // a block filter matched
	BlockedFilter string        // name of the blocking filter (only when Blocked)
	Counts        []FilterCount // one entry per enabled filter, in configured order
	Matched       bool          // any enabled filter matched anything
	Changed       bool          // at least one replacement altered the body
	Body          []byte        // re-marshaled filtered JSON, non-nil ONLY when Changed
}

// compiledFilter is one enabled filter with its pattern compiled once.
type compiledFilter struct {
	name        string
	action      string
	replacement string
	re          *regexp.Regexp
}

// Set is an immutable compiled snapshot of the enabled filters.
type Set struct {
	filters []compiledFilter
	aliases *redact.Table
}

// Compile compiles the enabled filters from cfg, preserving configured order.
// Disabled filters are skipped. A replace filter whose replacement contains $R
// records its aliases in aliases (creating one when nil) so responses can be
// restored.
func Compile(cfg []config.Filter, aliases *redact.Table) (*Set, error) {
	if aliases == nil {
		aliases = redact.NewTable(0)
	}
	s := &Set{filters: make([]compiledFilter, 0, len(cfg)), aliases: aliases}
	for _, f := range cfg {
		if !f.Enabled {
			continue
		}
		re, err := config.CompileFilter(f)
		if err != nil {
			return nil, fmt.Errorf("filter %q: %w", f.Name, err)
		}
		s.filters = append(s.filters, compiledFilter{
			name:        f.Name,
			action:      f.Action,
			replacement: f.Replacement,
			re:          re,
		})
	}
	return s, nil
}

// Len returns the number of compiled (enabled) filters.
func (s *Set) Len() int { return len(s.filters) }

// applyState accumulates a single ApplyBody pass.
type applyState struct {
	counts        []int // total matches per compiled filter
	matched       bool  // some filter matched some string
	changed       bool  // some replacement altered a string
	blocked       bool  // a block filter matched
	blockedIndex  int   // index of the blocking filter
	blockedFilter string
}

// ApplyBody filters every string value in the JSON body. It returns ErrNotJSON
// (possibly wrapped) when body is not valid JSON.
func (s *Set) ApplyBody(body []byte) (*Outcome, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotJSON, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing data after JSON value", ErrNotJSON)
	}

	st := &applyState{counts: make([]int, len(s.filters))}
	v, _ = s.applyValue(v, st)

	if st.blocked {
		return &Outcome{
			Blocked:       true,
			BlockedFilter: st.blockedFilter,
			Counts:        s.filterCounts(st.counts[:st.blockedIndex+1]),
			Matched:       true,
			Changed:       false,
			Body:          nil,
		}, nil
	}

	out := &Outcome{
		Counts:  s.filterCounts(st.counts),
		Matched: st.matched,
		Changed: st.changed,
	}
	if !st.changed {
		return out, nil
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("filter: marshal filtered body: %w", err)
	}
	out.Body = bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	return out, nil
}

// applyValue walks v recursively. It returns the possibly-rewritten value and
// whether processing must stop because a block filter matched.
func (s *Set) applyValue(v any, st *applyState) (any, bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			nv, stop := s.applyValue(val, st)
			if stop {
				return v, true
			}
			x[k] = nv
		}
	case []any:
		for i, val := range x {
			nv, stop := s.applyValue(val, st)
			if stop {
				return v, true
			}
			x[i] = nv
		}
	case string:
		return s.applyString(x, st)
	}
	return v, false
}

// applyString applies the enabled filters to one string in configured order,
// each seeing the previous filter's output. It stops at the first block match.
func (s *Set) applyString(str string, st *applyState) (string, bool) {
	cur := str
	for i := range s.filters {
		f := &s.filters[i]
		n := len(f.re.FindAllStringIndex(cur, -1))
		if n == 0 {
			continue
		}
		st.counts[i] += n
		st.matched = true
		if f.action == config.ActionBlock {
			st.blocked = true
			st.blockedIndex = i
			st.blockedFilter = f.name
			return cur, true
		}
		next := f.re.ReplaceAllString(cur, f.replacement)
		if strings.Contains(f.replacement, "$R") {
			next = s.replaceTokens(cur, f)
		}
		if next != cur {
			st.changed = true
			cur = next
		}
	}
	return cur, false
}

// replaceTokens rewrites every match through the template, expanding $R to a
// per-match token and recording the alias so the response can be restored.
func (s *Set) replaceTokens(str string, f *compiledFilter) string {
	matches := f.re.FindAllStringSubmatchIndex(str, -1)
	if len(matches) == 0 {
		return str
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		if m[0] < last {
			continue
		}
		match := str[m[0]:m[1]]
		if s.aliases.IsAlias(match) {
			continue
		}
		alias := redact.Expand(f.replacement, str, m, s.aliases.Token(f.name, match))
		if alias == match {
			continue
		}
		s.aliases.Add(f.name, alias, match)
		b.WriteString(str[last:m[0]])
		b.WriteString(alias)
		last = m[1]
	}
	// Nothing changed: hand back the input so the caller sees no mutation.
	if last == 0 && len(matches) > 0 && b.Len() == 0 {
		return str
	}
	b.WriteString(str[last:])
	return b.String()
}

// filterCounts pairs the accumulator with the compiled filters' names/actions.
func (s *Set) filterCounts(counts []int) []FilterCount {
	out := make([]FilterCount, len(counts))
	for i, n := range counts {
		out[i] = FilterCount{Name: s.filters[i].name, Action: s.filters[i].action, Count: n}
	}
	return out
}

// Test compiles a single filter rule — an inline pattern or a pattern_file —
// and applies it to sample, without saving. An empty replacement leaves result
// == sample. matched holds each matching substring; result is the rewritten text.
// A $R replacement expands through aliases, so the preview matches what the proxy
// would send upstream; pass a stable table to keep the preview token steady.
func Test(f config.Filter, sample string, aliases *redact.Table) (matched []string, result string, err error) {
	re, err := config.CompileFilter(f)
	if err != nil {
		return nil, "", err
	}
	matched = re.FindAllString(sample, -1)
	if f.Replacement == "" {
		return matched, sample, nil
	}
	if !strings.Contains(f.Replacement, "$R") {
		return matched, re.ReplaceAllString(sample, f.Replacement), nil
	}
	if aliases == nil {
		aliases = redact.NewTable(0)
	}
	cf := compiledFilter{name: f.Name, action: f.Action, replacement: f.Replacement, re: re}
	set := &Set{filters: []compiledFilter{cf}, aliases: aliases}
	return matched, set.replaceTokens(sample, &set.filters[0]), nil
}
