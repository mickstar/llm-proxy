package redact

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
)

// matcher indexes aliases for a single left-to-right scan. Candidates are
// bucketed by their first byte and sorted longest-first so the longest alias
// wins at any position.
type matcher struct {
	byFirst [256][]string
	lookup  map[string]original // alias -> replacement in both renderings
	version uint64
}

// original carries one alias's replacement text: escaped for splicing into a
// JSON string literal, plain for a value that will be re-encoded.
type original struct {
	plain   string
	escaped string
}

func newMatcher(insts []Instance, version uint64) *matcher {
	m := &matcher{lookup: make(map[string]original, len(insts)), version: version}
	for _, inst := range insts {
		if inst.Alias == "" {
			continue
		}
		m.lookup[inst.Alias] = original{plain: inst.Original, escaped: jsonEscape(inst.Original)}
		m.byFirst[inst.Alias[0]] = append(m.byFirst[inst.Alias[0]], inst.Alias)
	}
	for b := range m.byFirst {
		cands := m.byFirst[b]
		sort.Slice(cands, func(i, j int) bool { return len(cands[i]) > len(cands[j]) })
		m.byFirst[b] = cands
	}
	return m
}

// jsonEscape renders s for insertion inside a JSON string literal.
func jsonEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) < 2 {
		return s
	}
	return string(b[1 : len(b)-1])
}

// replace rewrites aliases in buf. When final is false the longest suffix that
// could still grow into an alias is returned unwritten, so a match split across
// two reads is never missed or half-emitted.
func (m *matcher) replace(buf []byte, final bool) (out, rest []byte) {
	i := 0
	for i < len(buf) {
		cands := m.byFirst[buf[i]]
		matched := ""
		for _, a := range cands {
			if len(a) <= len(buf)-i && string(buf[i:i+len(a)]) == a {
				matched = a
				break
			}
		}
		if matched != "" {
			out = append(out, m.lookup[matched].escaped...)
			i += len(matched)
			continue
		}
		if !final && prefixOfAny(cands, buf[i:]) {
			return out, buf[i:]
		}
		out = append(out, buf[i])
		i++
	}
	return out, nil
}

func prefixOfAny(cands []string, rem []byte) bool {
	for _, a := range cands {
		if len(rem) < len(a) && strings.HasPrefix(a, string(rem)) {
			return true
		}
	}
	return false
}

// RestoreReader streams a response body, replacing known aliases with their
// original values. Aliases split across reads are held back until resolved, so
// it is safe for SSE deltas that break a token in two.
type RestoreReader struct {
	rc      io.ReadCloser
	t       *Table
	m       *matcher
	buf     []byte
	out     []byte
	pending []byte
	done    bool
	err     error
}

// NewRestoreReader wraps rc. It returns rc untouched when no alias is known yet.
func NewRestoreReader(rc io.ReadCloser, t *Table) io.ReadCloser {
	if t == nil || t.Len() == 0 {
		return rc
	}
	return &RestoreReader{
		rc:  rc,
		t:   t,
		m:   newMatcher(t.List(), t.Version()),
		buf: make([]byte, 32*1024),
	}
}

// Read returns restored bytes.
func (r *RestoreReader) Read(p []byte) (int, error) {
	for len(r.out) == 0 {
		if r.done {
			if r.err != nil {
				err := r.err
				r.err = nil
				return 0, err
			}
			return 0, io.EOF
		}

		n, err := r.rc.Read(r.buf)
		if n > 0 {
			r.refresh()
			r.pending = append(r.pending, r.buf[:n]...)
			var rest []byte
			r.out, rest = r.m.replace(r.pending, false)
			r.pending = rest
		}
		if err != nil {
			if err == io.EOF {
				tail, _ := r.m.replace(r.pending, true)
				r.out = append(r.out, tail...)
				r.pending = nil
				r.done = true
			} else {
				r.done = true
				r.err = err
				if len(r.out) == 0 {
					r.err = nil
					return 0, err
				}
			}
		}
	}
	n := copy(p, r.out)
	r.out = r.out[n:]
	return n, nil
}

// refresh rebuilds the matcher when new aliases were recorded mid-stream.
func (r *RestoreReader) refresh() {
	if v := r.t.Version(); v != r.m.version {
		r.m = newMatcher(r.t.List(), v)
	}
}

// Close closes the underlying body.
func (r *RestoreReader) Close() error { return r.rc.Close() }

// EventRestoreReader restores aliases in a server-sent event stream. Providers
// stream text one token at a time, so an alias normally arrives split across the
// JSON deltas of successive events, never contiguously in the raw bytes. This
// reader decodes each event, joins fragments per JSON path, and re-encodes the
// event so every chunk the client receives is still valid JSON.
type EventRestoreReader struct {
	rc      io.ReadCloser
	t       *Table
	m       *matcher
	buf     []byte
	raw     []byte // bytes of an event that has not ended yet
	out     []byte
	pending []pendingFragment
	done    bool
	err     error
}

// pendingFragment is an alias prefix withheld from one JSON path until the next
// delta on that path completes it or rules it out.
type pendingFragment struct {
	path string
	text string
}

// NewEventRestoreReader wraps an event-stream body. It returns rc untouched
// when no alias is known yet.
func NewEventRestoreReader(rc io.ReadCloser, t *Table) io.ReadCloser {
	if t == nil || t.Len() == 0 {
		return rc
	}
	return &EventRestoreReader{
		rc:  rc,
		t:   t,
		m:   newMatcher(t.List(), t.Version()),
		buf: make([]byte, 32*1024),
	}
}

// Read returns restored bytes.
func (r *EventRestoreReader) Read(p []byte) (int, error) {
	for len(r.out) == 0 {
		if r.done {
			if r.err != nil {
				err := r.err
				r.err = nil
				return 0, err
			}
			return 0, io.EOF
		}

		n, err := r.rc.Read(r.buf)
		if n > 0 {
			r.refresh()
			r.raw = append(r.raw, r.buf[:n]...)
			r.drain(false)
		}
		if err != nil {
			r.drain(true)
			r.flushPending()
			r.done = true
			if err != io.EOF {
				r.err = err
				if len(r.out) == 0 {
					r.err = nil
					return 0, err
				}
			}
		}
	}
	n := copy(p, r.out)
	r.out = r.out[n:]
	return n, nil
}

// drain processes every terminated event in r.raw, plus a trailing unterminated
// one when final is set.
func (r *EventRestoreReader) drain(final bool) {
	for {
		block, rest, ok := splitEvent(r.raw)
		if !ok {
			if !final || len(r.raw) == 0 {
				return
			}
			block, rest = r.raw, nil
		}
		r.raw = rest
		r.event(block)
		if !ok {
			return
		}
	}
}

// splitEvent cuts one event off buf at its blank-line terminator.
func splitEvent(buf []byte) (block, rest []byte, ok bool) {
	for i := range len(buf) {
		if buf[i] != '\n' {
			continue
		}
		if i+1 < len(buf) && buf[i+1] == '\n' {
			return buf[:i], buf[i+2:], true
		}
		if i >= 1 && buf[i-1] == '\r' && i+2 < len(buf) && buf[i+1] == '\r' && buf[i+2] == '\n' {
			return buf[:i-1], buf[i+3:], true
		}
	}
	return nil, buf, false
}

// event rewrites one event. Data lines are decoded as JSON and walked; every
// other line (a comment, or a field this proxy does not understand) is kept.
func (r *EventRestoreReader) event(block []byte) {
	var data []string
	var other []string
	for _, line := range strings.Split(string(block), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case strings.TrimSpace(line) != "":
			other = append(other, line)
		}
	}
	if len(data) == 0 {
		r.emitRaw(block)
		return
	}

	payload := strings.Join(data, "\n")
	if strings.TrimSpace(payload) == "[DONE]" {
		r.flushPending()
		r.emitRaw(block)
		return
	}

	var v any
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		// Not a shape this proxy knows: restore whatever is contiguous in the
		// raw event rather than dropping the bytes.
		out, _ := r.m.replace(block, true)
		r.out = append(r.out, out...)
		r.out = append(r.out, '\n', '\n')
		return
	}

	enc, err := encodeJSON(r.walk(v, ""))
	if err != nil {
		r.emitRaw(block)
		return
	}
	for _, line := range other {
		r.out = append(r.out, line...)
		r.out = append(r.out, '\n')
	}
	r.out = append(r.out, "data: "...)
	r.out = append(r.out, enc...)
	r.out = append(r.out, '\n', '\n')
}

// walk rewrites every string value in v, keyed by its JSON path so a token split
// between two deltas of the same field is reassembled.
func (r *EventRestoreReader) walk(v any, path string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			x[k] = r.walk(val, path+"/"+k)
		}
		return x
	case []any:
		for i, val := range x {
			x[i] = r.walk(val, path+"/"+strconv.Itoa(i))
		}
		return x
	case string:
		return r.restoreField(x, path)
	}
	return v
}

// restoreField restores aliases in one string value. A trailing alias prefix is
// withheld into r.pending and prepended to the next value on the same path.
func (r *EventRestoreReader) restoreField(s, path string) string {
	for i, p := range r.pending {
		if p.path == path {
			s = p.text + s
			r.pending = append(r.pending[:i], r.pending[i+1:]...)
			break
		}
	}
	out, rest := r.m.replace([]byte(s), false)
	if len(rest) > 0 {
		r.pending = append(r.pending, pendingFragment{path: path, text: string(rest)})
	}
	return string(out)
}

// flushPending emits withheld fragments that never resolved as one delta, so no
// part of the client's text is lost when a stream ends mid-alias.
func (r *EventRestoreReader) flushPending() {
	if len(r.pending) == 0 {
		return
	}
	var b strings.Builder
	for _, p := range r.pending {
		b.WriteString(p.text)
	}
	r.pending = nil
	enc, err := encodeJSON(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": b.String()}}},
	})
	if err != nil {
		return
	}
	r.out = append(r.out, "data: "...)
	r.out = append(r.out, enc...)
	r.out = append(r.out, '\n', '\n')
}

func (r *EventRestoreReader) emitRaw(block []byte) {
	r.out = append(r.out, block...)
	r.out = append(r.out, '\n', '\n')
}

// refresh rebuilds the matcher when new aliases were recorded mid-stream.
func (r *EventRestoreReader) refresh() {
	if v := r.t.Version(); v != r.m.version {
		r.m = newMatcher(r.t.List(), v)
	}
}

// Close closes the underlying body.
func (r *EventRestoreReader) Close() error { return r.rc.Close() }

// encodeJSON marshals v without escaping HTML, so streamed text keeps its bytes.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
