package filter

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/mickstar/llm-proxy/internal/config"
	"github.com/mickstar/llm-proxy/internal/redact"
)

// enabled builds an enabled filter rule.
func enabled(name, pattern, action, replacement string) config.Filter {
	return config.Filter{
		Name:        name,
		Pattern:     pattern,
		Action:      action,
		Replacement: replacement,
		Enabled:     true,
	}
}

func mustCompile(t *testing.T, fs ...config.Filter) *Set {
	t.Helper()
	s, err := Compile(fs, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return s
}

// decodeJSON decodes b preserving numeric literals as json.Number.
func decodeJSON(t *testing.T, b []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %q: %v", b, err)
	}
	return v
}

// at walks a decoded JSON value by map keys (string) and array indexes (int).
func at(t *testing.T, v any, path ...any) any {
	t.Helper()
	for _, p := range path {
		switch key := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("at %v: not an object (%T)", path, v)
			}
			v, ok = m[key]
			if !ok {
				t.Fatalf("at %v: missing key %q", path, key)
			}
		case int:
			a, ok := v.([]any)
			if !ok {
				t.Fatalf("at %v: not an array (%T)", path, v)
			}
			if key < 0 || key >= len(a) {
				t.Fatalf("at %v: index %d out of range (len %d)", path, key, len(a))
			}
			v = a[key]
		default:
			t.Fatalf("at %v: bad path element %T", path, p)
		}
	}
	return v
}

func str(t *testing.T, v any) string {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("value %v (%T) is not a string", v, v)
	}
	return s
}

func TestApplyBodyLiteralReplacement(t *testing.T) {
	s := mustCompile(t, enabled("name", "James Brown", config.ActionReplace, "John Smith"))
	body := []byte(`{"messages":[{"role":"user","content":"Ask James Brown"}]}`)

	out, err := s.ApplyBody(body)
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if out.Blocked || !out.Matched || !out.Changed {
		t.Fatalf("outcome = %+v, want matched+changed, not blocked", out)
	}
	if out.Body == nil {
		t.Fatal("Body = nil, want filtered JSON")
	}
	if got := str(t, at(t, decodeJSON(t, out.Body), "messages", 0, "content")); got != "Ask John Smith" {
		t.Errorf("content = %q, want %q", got, "Ask John Smith")
	}
	want := []FilterCount{{Name: "name", Action: "replace", Count: 1}}
	if !reflect.DeepEqual(out.Counts, want) {
		t.Errorf("Counts = %+v, want %+v", out.Counts, want)
	}
}

func TestApplyBodyCaptureGroupMasking(t *testing.T) {
	s := mustCompile(t, enabled("mask", `(sk_\w{2}).*(\w{2})`, config.ActionReplace, `${1}**********${2}`))
	body := []byte(`{"content":"sk_xy1234abcd7890ab"}`)

	out, err := s.ApplyBody(body)
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if !out.Changed || out.Body == nil {
		t.Fatalf("outcome = %+v, want changed with body", out)
	}
	if got := str(t, at(t, decodeJSON(t, out.Body), "content")); got != "sk_xy**********ab" {
		t.Errorf("content = %q, want %q", got, "sk_xy**********ab")
	}
}

func TestApplyBodyCaseInsensitivePathRewrite(t *testing.T) {
	s := mustCompile(t, enabled("path", `(?i)/Users/james/`, config.ActionReplace, `/users/john.smith/`))
	body := []byte(`{"content":"/Users/james/xyz"}`)

	out, err := s.ApplyBody(body)
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if !out.Changed || out.Body == nil {
		t.Fatalf("outcome = %+v, want changed with body", out)
	}
	if got := str(t, at(t, decodeJSON(t, out.Body), "content")); got != "/users/john.smith/xyz" {
		t.Errorf("content = %q, want %q", got, "/users/john.smith/xyz")
	}
}

func TestApplyBodyChainedFilters(t *testing.T) {
	s := mustCompile(t,
		enabled("a", "James", config.ActionReplace, "John"),
		enabled("b", "Brown", config.ActionReplace, "Smith"),
	)
	body := []byte(`{"content":"James Brown"}`)

	out, err := s.ApplyBody(body)
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if !out.Changed || out.Body == nil {
		t.Fatalf("outcome = %+v, want changed with body", out)
	}
	if got := str(t, at(t, decodeJSON(t, out.Body), "content")); got != "John Smith" {
		t.Errorf("content = %q, want %q", got, "John Smith")
	}
	want := []FilterCount{
		{Name: "a", Action: "replace", Count: 1},
		{Name: "b", Action: "replace", Count: 1},
	}
	if !reflect.DeepEqual(out.Counts, want) {
		t.Errorf("Counts = %+v, want %+v", out.Counts, want)
	}
}

func TestApplyBodyNestedScanning(t *testing.T) {
	body := []byte(`{
		"messages": [{"role": "user", "content": "use sk-live-abc123"}],
		"tools": [{
			"type": "function",
			"function": {
				"name": "lookup",
				"description": "calls with sk-live-abc123",
				"parameters": {
					"type": "object",
					"properties": {"q": {"type": "string", "description": "key sk-live-abc123"}}
				}
			}
		}]
	}`)
	s := mustCompile(t, enabled("secret", `sk-live-abc123`, config.ActionReplace, "[REDACTED]"))

	out, err := s.ApplyBody(body)
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if !out.Changed || out.Body == nil {
		t.Fatalf("outcome = %+v, want changed with body", out)
	}
	if bytes.Contains(out.Body, []byte("sk-live-abc123")) {
		t.Errorf("filtered body still contains secret: %s", out.Body)
	}
	v := decodeJSON(t, out.Body)
	want := map[string]string{
		"message":   "use [REDACTED]",
		"tooldesc":  "calls with [REDACTED]",
		"paramdesc": "key [REDACTED]",
	}
	got := map[string]string{
		"message":   str(t, at(t, v, "messages", 0, "content")),
		"tooldesc":  str(t, at(t, v, "tools", 0, "function", "description")),
		"paramdesc": str(t, at(t, v, "tools", 0, "function", "parameters", "properties", "q", "description")),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nested strings = %+v, want %+v", got, want)
	}
	if out.Counts[0].Count != 3 {
		t.Errorf("Count = %d, want 3", out.Counts[0].Count)
	}
}

func TestApplyBodyStructureAndNumbersPreserved(t *testing.T) {
	body := []byte(`{
		"model": "openai/gpt-4o",
		"temperature": 0.7,
		"truncated": 123456789012345678901234567890,
		"precise": 3.141592653589793238462643383279,
		"provider": {"order": ["openai"], "allow_fallbacks": false},
		"unrecognized": {"x": [1, 2, 3], "y": null},
		"messages": [{"role": "user", "content": "hello James"}]
	}`)
	s := mustCompile(t, enabled("name", "James", config.ActionReplace, "Bob"))

	out, err := s.ApplyBody(body)
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if !out.Changed || out.Body == nil {
		t.Fatalf("outcome = %+v, want changed with body", out)
	}

	// Large/high-precision literals must survive byte-identically.
	for _, literal := range []string{"123456789012345678901234567890", "3.141592653589793238462643383279"} {
		if !bytes.Contains(out.Body, []byte(literal)) {
			t.Errorf("filtered body lost literal %s: %s", literal, out.Body)
		}
	}

	orig := decodeJSON(t, body).(map[string]any)
	filt := decodeJSON(t, out.Body).(map[string]any)
	if len(orig) != len(filt) {
		t.Errorf("top-level field count = %d, want %d", len(filt), len(orig))
	}
	for k := range orig {
		if _, ok := filt[k]; !ok {
			t.Errorf("missing field %q", k)
		}
	}

	if n, ok := filt["temperature"].(json.Number); !ok || n.String() != "0.7" {
		t.Errorf("temperature = %v (%T), want json.Number 0.7", filt["temperature"], filt["temperature"])
	}
	if got := str(t, at(t, filt, "provider", "order", 0)); got != "openai" {
		t.Errorf("provider.order[0] = %q, want openai", got)
	}
	if b, ok := at(t, filt, "provider", "allow_fallbacks").(bool); !ok || b {
		t.Errorf("provider.allow_fallbacks = %v, want false", at(t, filt, "provider", "allow_fallbacks"))
	}
	if a := at(t, filt, "unrecognized", "x").([]any); len(a) != 3 {
		t.Errorf("unrecognized.x len = %d, want 3", len(a))
	}
	if at(t, filt, "unrecognized", "y") != nil {
		t.Errorf("unrecognized.y = %v, want null", at(t, filt, "unrecognized", "y"))
	}
	if got := str(t, at(t, filt, "messages", 0, "content")); got != "hello Bob" {
		t.Errorf("content = %q, want %q", got, "hello Bob")
	}
}

func TestApplyBodyBlockShortCircuit(t *testing.T) {
	s := mustCompile(t,
		enabled("name", "James", config.ActionReplace, "John"),
		enabled("secret", "sk-secret", config.ActionBlock, ""),
	)
	body := []byte(`{"content":"James has sk-secret"}`)

	out, err := s.ApplyBody(body)
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if !out.Blocked {
		t.Fatal("Blocked = false, want true")
	}
	if out.BlockedFilter != "secret" {
		t.Errorf("BlockedFilter = %q, want %q", out.BlockedFilter, "secret")
	}
	if out.Changed {
		t.Error("Changed = true, want false")
	}
	if out.Body != nil {
		t.Errorf("Body = %s, want nil", out.Body)
	}
	if !out.Matched {
		t.Error("Matched = false, want true")
	}
	want := []FilterCount{
		{Name: "name", Action: "replace", Count: 1},
		{Name: "secret", Action: "block", Count: 1},
	}
	if !reflect.DeepEqual(out.Counts, want) {
		t.Errorf("Counts = %+v, want %+v", out.Counts, want)
	}

	// The encoded outcome must not leak the matched value.
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if bytes.Contains(encoded, []byte("sk-secret")) {
		t.Errorf("encoded outcome leaks secret: %s", encoded)
	}
}

func TestApplyBodyBlockOmitsLaterFilters(t *testing.T) {
	s := mustCompile(t,
		enabled("secret", "sk-secret", config.ActionBlock, ""),
		enabled("later", "James", config.ActionReplace, "John"),
	)
	body := []byte(`{"content":"sk-secret and James"}`)

	out, err := s.ApplyBody(body)
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if !out.Blocked {
		t.Fatal("Blocked = false, want true")
	}
	want := []FilterCount{{Name: "secret", Action: "block", Count: 1}}
	if !reflect.DeepEqual(out.Counts, want) {
		t.Errorf("Counts = %+v, want only the blocking filter %+v", out.Counts, want)
	}
}

func TestApplyBodyDisabledFiltersSkipped(t *testing.T) {
	disabled := enabled("b", "Brown", config.ActionReplace, "Smith")
	disabled.Enabled = false
	s := mustCompile(t, enabled("a", "James", config.ActionReplace, "John"), disabled)

	if s.Len() != 1 {
		t.Fatalf("Len = %d, want 1", s.Len())
	}
	out, err := s.ApplyBody([]byte(`{"content":"James Brown"}`))
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if !out.Changed || out.Body == nil {
		t.Fatalf("outcome = %+v, want changed with body", out)
	}
	if got := str(t, at(t, decodeJSON(t, out.Body), "content")); got != "John Brown" {
		t.Errorf("content = %q, want %q", got, "John Brown")
	}
	if len(out.Counts) != 1 {
		t.Errorf("Counts = %+v, want one entry", out.Counts)
	}
}

func TestApplyBodyNoEnabledFilters(t *testing.T) {
	disabled := enabled("a", "James", config.ActionReplace, "John")
	disabled.Enabled = false
	s := mustCompile(t, disabled)

	if s.Len() != 0 {
		t.Fatalf("Len = %d, want 0", s.Len())
	}
	out, err := s.ApplyBody([]byte(`{"content":"James Brown"}`))
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if out.Matched || out.Changed || out.Blocked || out.Body != nil {
		t.Errorf("outcome = %+v, want untouched", out)
	}
	if len(out.Counts) != 0 {
		t.Errorf("Counts = %+v, want empty", out.Counts)
	}
}

func TestApplyBodyKeysNotScanned(t *testing.T) {
	s := mustCompile(t, enabled("name", "James Brown", config.ActionReplace, "John Smith"))
	body := []byte(`{"James Brown":"value","nested":{"James Brown":1}}`)

	out, err := s.ApplyBody(body)
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if out.Matched || out.Changed {
		t.Errorf("outcome = %+v, want no match (keys are not scanned)", out)
	}
	if out.Body != nil {
		t.Errorf("Body = %s, want nil", out.Body)
	}
}

func TestApplyBodyUnchangedMatchLeavesBodyNil(t *testing.T) {
	s := mustCompile(t, enabled("noop", "abc", config.ActionReplace, "abc"))
	out, err := s.ApplyBody([]byte(`{"content":"abc"}`))
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if !out.Matched {
		t.Error("Matched = false, want true")
	}
	if out.Changed {
		t.Error("Changed = true, want false (replacement identical)")
	}
	if out.Body != nil {
		t.Errorf("Body = %s, want nil", out.Body)
	}
	if out.Counts[0].Count != 1 {
		t.Errorf("Count = %d, want 1", out.Counts[0].Count)
	}
}

func TestApplyBodyDoesNotEscapeHTML(t *testing.T) {
	s := mustCompile(t, enabled("tags", `<tag>`, config.ActionReplace, `x<y&z>`))
	out, err := s.ApplyBody([]byte(`{"content":"a <tag> b"}`))
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if !out.Changed || out.Body == nil {
		t.Fatalf("outcome = %+v, want changed with body", out)
	}
	if !bytes.Contains(out.Body, []byte(`x<y&z>`)) {
		t.Errorf("body escaped HTML: %s", out.Body)
	}
	if got := str(t, at(t, decodeJSON(t, out.Body), "content")); got != "a x<y&z> b" {
		t.Errorf("content = %q, want %q", got, "a x<y&z> b")
	}
}

func TestApplyBodyCountsAcrossStrings(t *testing.T) {
	s := mustCompile(t, enabled("a", "x", config.ActionReplace, "y"))
	body := []byte(`{"a":"x x","b":["x",{"c":"xx"}]}`)

	out, err := s.ApplyBody(body)
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if !out.Changed {
		t.Fatal("Changed = false, want true")
	}
	if out.Counts[0].Count != 5 {
		t.Errorf("Count = %d, want 5", out.Counts[0].Count)
	}
	if bytes.Contains(out.Body, []byte(`"x`)) {
		t.Errorf("filtered body still contains matches: %s", out.Body)
	}
}

func TestApplyBodyNotJSON(t *testing.T) {
	multipart := []byte("--X\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\nContent-Type: audio/wav\r\n\r\n\x00\x01binary\xff\r\n--X--\r\n")
	cases := map[string][]byte{
		"multipart":  multipart,
		"empty":      nil,
		"truncated":  []byte(`{"a":`),
		"trailing":   []byte(`{"a":1} extra`),
		"plain text": []byte("hello world"),
	}
	s := mustCompile(t, enabled("a", "James", config.ActionReplace, "John"))
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := s.ApplyBody(body)
			if !errors.Is(err, ErrNotJSON) {
				t.Fatalf("err = %v, want ErrNotJSON", err)
			}
			if out != nil {
				t.Errorf("outcome = %+v, want nil", out)
			}
		})
	}
}

func TestCompileInvalidPattern(t *testing.T) {
	if _, err := Compile([]config.Filter{enabled("bad", "(", config.ActionReplace, "x")}, nil); err == nil {
		t.Fatal("Compile error = nil, want pattern error")
	}
}

func TestCompileOrderAndLen(t *testing.T) {
	skip := enabled("skip", "z", config.ActionReplace, "z")
	skip.Enabled = false
	s := mustCompile(t,
		enabled("first", "a", config.ActionReplace, "A"),
		skip,
		enabled("second", "b", config.ActionReplace, "B"),
	)
	if s.Len() != 2 {
		t.Fatalf("Len = %d, want 2", s.Len())
	}
	out, err := s.ApplyBody([]byte(`{"c":"ab"}`))
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	want := []FilterCount{
		{Name: "first", Action: "replace", Count: 1},
		{Name: "second", Action: "replace", Count: 1},
	}
	if !reflect.DeepEqual(out.Counts, want) {
		t.Errorf("Counts = %+v, want %+v (configured order)", out.Counts, want)
	}
}

func TestTest(t *testing.T) {
	rule := func(pattern, replacement string) config.Filter {
		return config.Filter{Name: "t", Pattern: pattern, Replacement: replacement}
	}

	matched, result, err := Test(rule(`sk-\w+`, ""), "text sk-live-abc123 end", nil)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if len(matched) != 1 {
		t.Errorf("matched = %v, want 1 match", matched)
	}
	if result != "text sk-live-abc123 end" {
		t.Errorf("result = %q, want unchanged sample", result)
	}

	// RE2's \w is [0-9A-Za-z_]: it excludes '-', so sk-\w+ matches only
	// "sk-live", not the full hyphenated token.
	matched, result, err = Test(rule(`sk-\w+`, "[X]"), "text sk-live-abc123 end", nil)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if len(matched) != 1 {
		t.Errorf("matched = %v, want 1 match", matched)
	}
	if result != "text [X]-abc123 end" {
		t.Errorf("result = %q, want %q", result, "text [X]-abc123 end")
	}

	// A hyphen-aware class replaces the whole token.
	matched, result, err = Test(rule(`sk-[\w-]+`, "[X]"), "text sk-live-abc123 end", nil)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if len(matched) != 1 {
		t.Errorf("matched = %v, want 1 match", matched)
	}
	if result != "text [X] end" {
		t.Errorf("result = %q, want %q", result, "text [X] end")
	}

	matched, _, err = Test(rule(`a`, "b"), "a a a", nil)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if len(matched) != 3 {
		t.Errorf("matched = %v, want 3 matches", matched)
	}

	if _, _, err := Test(rule(`(`, ""), "x", nil); err == nil {
		t.Error("Test error = nil, want compile error")
	}
}

func TestTestPatternFile(t *testing.T) {
	key := filepath.Join(t.TempDir(), "id_rsa")
	if err := os.WriteFile(key, []byte("-----BEGIN KEY-----\nabc\n-----END KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	matched, result, err := Test(config.Filter{Name: "key", PatternFile: key}, "prefix -----BEGIN KEY-----\nabc\n-----END KEY----- suffix", nil)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if len(matched) != 1 {
		t.Fatalf("matched = %v, want the key", matched)
	}
	if result == "" || !strings.Contains(result, "BEGIN KEY") {
		t.Errorf("result = %q, want unchanged sample", result)
	}

	if _, _, err := Test(config.Filter{Name: "key", PatternFile: filepath.Join(t.TempDir(), "missing")}, "x", nil); err == nil {
		t.Error("Test error = nil, want file error")
	}
}

func TestApplyBodyBlocksPatternFile(t *testing.T) {
	key := filepath.Join(t.TempDir(), "id_ed25519")
	content := "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk\n-----END OPENSSH PRIVATE KEY-----\n"
	if err := os.WriteFile(key, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	s := mustCompile(t, config.Filter{Name: "ssh-key", PatternFile: key, Action: config.ActionBlock, Enabled: true})

	// JSON string escapes decode to real newlines before filters run. The file
	// uses LF and ends with a newline; a request that re-encodes the same key
	// with CRLF, without a trailing newline, or both must still be blocked.
	body := strings.TrimRight(content, "\n")
	for _, tc := range []struct {
		name string
		text string
	}{
		{"LF", body},
		{"LF with trailing newline", content},
		{"CRLF", strings.ReplaceAll(body, "\n", `\r\n`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			escaped := strings.ReplaceAll(tc.text, "\n", `\n`)
			out, err := s.ApplyBody([]byte(`{"messages":[{"role":"user","content":"` + escaped + `"}]}`))
			if err != nil {
				t.Fatalf("ApplyBody: %v", err)
			}
			if !out.Blocked || out.BlockedFilter != "ssh-key" {
				t.Fatalf("outcome = %+v, want blocked by ssh-key", out)
			}
			if out.Body != nil {
				t.Errorf("Body = %s, want nil for a blocked request", out.Body)
			}
		})
	}

	out, err := s.ApplyBody([]byte(`{"messages":[{"role":"user","content":"nothing secret"}]}`))
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if out.Blocked || out.Matched {
		t.Errorf("outcome = %+v, want no match", out)
	}
}

func TestCaseInsensitiveByDefault(t *testing.T) {
	s := mustCompile(t, enabled("pii", "james brown", config.ActionReplace, "John Smith"))
	out, err := s.ApplyBody([]byte(`{"content":"Ask JAMES BROWN about James Brown"}`))
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if !out.Changed || out.Body == nil {
		t.Fatalf("outcome = %+v, want changed with body", out)
	}
	if got := str(t, at(t, decodeJSON(t, out.Body), "content")); got != "Ask John Smith about John Smith" {
		t.Errorf("content = %q, want both cases rewritten", got)
	}
	if out.Counts[0].Count != 2 {
		t.Errorf("Count = %d, want 2", out.Counts[0].Count)
	}
}

func TestCaseSensitiveOptOut(t *testing.T) {
	s := mustCompile(t, enabled("pii", `(?-i)james brown`, config.ActionReplace, "John Smith"))
	out, err := s.ApplyBody([]byte(`{"content":"Ask James Brown"}`))
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if out.Matched || out.Changed {
		t.Errorf("outcome = %+v, want no match with (?-i) opt-out", out)
	}

	// The same pattern without the opt-out matches case-insensitively.
	s = mustCompile(t, enabled("pii", "james brown", config.ActionReplace, "John Smith"))
	out, err = s.ApplyBody([]byte(`{"content":"Ask James Brown"}`))
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if !out.Changed {
		t.Errorf("outcome = %+v, want match", out)
	}
}

func TestBlockIsCaseInsensitive(t *testing.T) {
	s := mustCompile(t, enabled("secret", `sk-live-\w+`, config.ActionBlock, ""))
	out, err := s.ApplyBody([]byte(`{"content":"use SK-LIVE-ABC123 now"}`))
	if err != nil {
		t.Fatalf("ApplyBody: %v", err)
	}
	if !out.Blocked || out.BlockedFilter != "secret" {
		t.Errorf("outcome = %+v, want blocked by secret", out)
	}
}

// A $R replacement registers the alias so the response can be restored.
func TestReplaceWithTokenRegistersAlias(t *testing.T) {
	tbl := redact.NewTable(0)
	s, err := Compile([]config.Filter{
		enabled("email", `[\w.+-]+@[\w.-]+`, config.ActionReplace, "redacted$R@gmail.com"),
	}, tbl)
	if err != nil {
		t.Fatal(err)
	}

	out, err := s.ApplyBody([]byte(`{"content":"write to john@gmail.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	content := contentOf(t, out.Body)
	re := regexp.MustCompile(`^write to redacted([a-z2-7]{6})@gmail\.com$`)
	m := re.FindStringSubmatch(content)
	if m == nil {
		t.Fatalf("content = %q", content)
	}

	inst := tbl.List()
	if len(inst) != 1 || inst[0].Original != "john@gmail.com" || inst[0].Rule != "email" {
		t.Fatalf("aliases = %+v", inst)
	}

	// The same value produces the same alias and is not re-replaced.
	again, err := s.ApplyBody([]byte(`{"content":"john@gmail.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := contentOf(t, again.Body); got != "redacted"+m[1]+"@gmail.com" {
		t.Errorf("alias = %q, want stable token", got)
	}
	if third, _ := s.ApplyBody([]byte(`{"content":"` + contentOf(t, again.Body) + `"}`)); third == nil || third.Changed {
		t.Errorf("alias was re-replaced: %+v", third)
	}
}

func contentOf(t *testing.T, b []byte) string {
	t.Helper()
	var v struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode: %v (%s)", err, b)
	}
	return v.Content
}
