package redact

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mickstar/llm-proxy/internal/config"
)

func mustSet(t *testing.T, rules ...config.Redaction) *Set {
	t.Helper()
	s, err := Compile(rules)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return s
}

// contentOf decodes {"content":"..."} from an ApplyBody result.
func contentOf(t *testing.T, b []byte) string {
	t.Helper()
	var v struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode result: %v (%s)", err, b)
	}
	return v.Content
}

var emailRule = config.Redaction{
	Name: "email", Pattern: `([\w.+-]+)@([\w.-]+)`, Template: "redacted$R@$2", Enabled: true,
}

func TestApplyBodyRewritesEmailDeterministically(t *testing.T) {
	tbl := NewTable(0)
	set := mustSet(t, emailRule)

	res, err := set.ApplyBody([]byte(`{"content":"mail john.smith@example.com now"}`), tbl)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Total != 1 {
		t.Fatalf("result = %+v", res)
	}
	got := contentOf(t, res.Body)

	re := regexp.MustCompile(`^mail redacted([a-z2-7]{6})@example\.com now$`)
	m := re.FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("aliased content = %q", got)
	}
	if strings.Contains(got, "john.smith") {
		t.Fatalf("original survived: %q", got)
	}

	// The same value in a later body gets the same alias.
	again, err := set.ApplyBody([]byte(`{"content":"john.smith@example.com"}`), tbl)
	if err != nil {
		t.Fatal(err)
	}
	if got := contentOf(t, again.Body); got != "redacted"+m[1]+"@example.com" {
		t.Errorf("alias not stable: %q", got)
	}
	if len(tbl.List()) != 1 {
		t.Errorf("instances = %d, want 1", len(tbl.List()))
	}
	inst := tbl.List()[0]
	if inst.Original != "john.smith@example.com" || inst.Rule != "email" || inst.Count != 2 {
		t.Errorf("instance = %+v", inst)
	}
}

func TestApplyBodyLeavesExistingAliasAlone(t *testing.T) {
	tbl := NewTable(0)
	set := mustSet(t, emailRule)
	first, _ := set.ApplyBody([]byte(`{"content":"a@b.com"}`), tbl)
	alias := contentOf(t, first.Body)

	res, err := set.ApplyBody([]byte(`{"content":"`+alias+`"}`), tbl)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Fatalf("alias was re-redacted: %s", res.Body)
	}
}

func TestApplyBodyFixedTemplatePath(t *testing.T) {
	tbl := NewTable(0)
	set := mustSet(t, config.Redaction{
		Name: "home", Pattern: `/Users/jane.doe/`, Template: `/Users/john.smith/`, Enabled: true,
	})
	res, err := set.ApplyBody([]byte(`{"content":"open /Users/jane.doe/notes/todo.md"}`), tbl)
	if err != nil {
		t.Fatal(err)
	}
	if got := contentOf(t, res.Body); got != "open /Users/john.smith/notes/todo.md" {
		t.Fatalf("content = %q", got)
	}
	if inst := tbl.List()[0]; inst.Original != "/Users/jane.doe/" {
		t.Errorf("instance = %+v", inst)
	}
}

func TestApplyBodyKeyPlaceholder(t *testing.T) {
	tbl := NewTable(0)
	set := mustSet(t, config.Redaction{
		Name: "key", Pattern: `sk-live-\w+`, Template: "[REDACTED-$R]", Enabled: true,
	})
	res, err := set.ApplyBody([]byte(`{"content":"use sk-live-abc123"}`), tbl)
	if err != nil {
		t.Fatal(err)
	}
	if got := contentOf(t, res.Body); !regexp.MustCompile(`^use \[REDACTED-[a-z2-7]{6}\]$`).MatchString(got) {
		t.Fatalf("content = %q", got)
	}
}

func TestApplyBodySkipsKeysAndNonStrings(t *testing.T) {
	tbl := NewTable(0)
	set := mustSet(t, config.Redaction{Name: "n", Pattern: `\d+`, Template: "x", Enabled: true})
	res, err := set.ApplyBody([]byte(`{"123":"123","n":123,"b":true}`), tbl)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(res.Body, &raw); err != nil {
		t.Fatal(err)
	}
	if got, ok := raw["123"]; !ok {
		t.Error("object key was rewritten")
	} else if got != "x" {
		t.Errorf("string value = %v, want x", got)
	}
	if got, ok := raw["n"].(float64); !ok || got != 123 {
		t.Errorf("number value = %v", raw["n"])
	}
	if got, ok := raw["b"].(bool); !ok || !got {
		t.Errorf("bool value = %v", raw["b"])
	}
}

func TestApplyBodyRejectsNonJSON(t *testing.T) {
	set := mustSet(t, emailRule)
	if _, err := set.ApplyBody([]byte(`{nope`), NewTable(0)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestDisabledRuleDoesNothing(t *testing.T) {
	set := mustSet(t, config.Redaction{Name: "e", Pattern: "x", Template: "y", Enabled: false})
	if set.Len() != 0 {
		t.Fatalf("len = %d", set.Len())
	}
}

// chunkReader returns its payload in fixed-size pieces to exercise the
// streaming split logic.
type chunkReader struct {
	data []byte
	n    int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := c.n
	if n > len(c.data) {
		n = len(c.data)
	}
	n = copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

func (c *chunkReader) Close() error { return nil }

func TestRestoreReaderWholeBuffer(t *testing.T) {
	tbl := NewTable(0)
	set := mustSet(t, emailRule)
	first, _ := set.ApplyBody([]byte(`{"content":"john.smith@example.com"}`), tbl)
	alias := contentOf(t, first.Body)

	in := `{"content":"` + alias + `"}`
	rc := NewRestoreReader(&chunkReader{data: []byte(in), n: len(in)}, tbl)
	out, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if got := contentOf(t, out); got != "john.smith@example.com" {
		t.Fatalf("restored = %q", got)
	}
}

func TestRestoreReaderSplitAcrossReads(t *testing.T) {
	tbl := NewTable(0)
	set := mustSet(t, emailRule)
	first, _ := set.ApplyBody([]byte(`{"content":"john.smith@example.com"}`), tbl)
	alias := contentOf(t, first.Body)

	in := `{"content":"` + alias + `"}`
	for _, chunk := range []int{1, 2, 3, 7} {
		rc := NewRestoreReader(&chunkReader{data: []byte(in), n: chunk}, tbl)
		out, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("chunk %d: %v", chunk, err)
		}
		if got := contentOf(t, out); got != "john.smith@example.com" {
			t.Errorf("chunk %d: restored = %q", chunk, got)
		}
	}
}

func TestRestoreReaderEscapesOriginal(t *testing.T) {
	tbl := NewTable(0)
	set := mustSet(t, config.Redaction{Name: "q", Pattern: `say .*`, Template: "[X$R]", Enabled: true})
	first, err := set.ApplyBody([]byte(`{"content":"say \"hi\" \\ there"}`), tbl)
	if err != nil {
		t.Fatal(err)
	}
	if got := contentOf(t, first.Body); strings.Contains(got, `"`) || strings.Contains(got, `\`) {
		t.Fatalf("alias leaked the original text: %q", got)
	}
	alias := contentOf(t, first.Body)

	rc := NewRestoreReader(io.NopCloser(strings.NewReader(`{"content":"`+alias+`"}`)), tbl)
	out, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if got := contentOf(t, out); got != `say "hi" \ there` {
		t.Fatalf("restored = %q", got)
	}
}

func TestRestoreReaderWithoutAliasesPassesThrough(t *testing.T) {
	body := `{"content":"nothing to do"}`
	rc := NewRestoreReader(io.NopCloser(strings.NewReader(body)), NewTable(0))
	out, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != body {
		t.Fatalf("out = %q", out)
	}
}

func TestTableEvictsOldest(t *testing.T) {
	tbl := NewTable(2)
	tbl.Add("r", "a1", "o1")
	tbl.Add("r", "a2", "o2")
	tbl.Add("r", "a3", "o3")
	if tbl.IsAlias("a1") {
		t.Error("oldest alias was not evicted")
	}
	if got := len(tbl.List()); got != 2 {
		t.Errorf("len = %d", got)
	}
}

func TestTableMarksConflicts(t *testing.T) {
	tbl := NewTable(0)
	tbl.Add("r", "same", "one")
	tbl.Add("r", "same", "two")
	inst := tbl.List()[0]
	if !inst.Conflict || inst.Original != "one" || inst.Count != 2 {
		t.Fatalf("instance = %+v", inst)
	}
}

func TestInstanceTimesAreSet(t *testing.T) {
	tbl := NewTable(0)
	tbl.Add("r", "a", "o")
	if time.Since(tbl.List()[0].FirstSeen) > time.Minute {
		t.Error("FirstSeen not populated")
	}
}

// sseStream renders delta events whose content values split an alias the way a
// provider tokenises a reply, with a comment line and a terminator.
func sseStream(parts []string) string {
	var b strings.Builder
	b.WriteString(": OPENROUTER PROCESSING\n\n")
	for _, p := range parts {
		ev, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]any{"content": p}}},
		})
		b.WriteString("data: " + string(ev) + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// deltas decodes every JSON data event and returns the concatenated delta
// content, failing the test if an event is not valid JSON.
func deltas(t *testing.T, body []byte) string {
	t.Helper()
	var b strings.Builder
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if strings.TrimSpace(payload) == "[DONE]" {
			continue
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatalf("event is not JSON: %v: %s", err, payload)
		}
		for _, c := range ev.Choices {
			b.WriteString(c.Delta.Content)
		}
	}
	return b.String()
}

func TestEventRestoreReaderJoinsDeltaFragments(t *testing.T) {
	tbl := NewTable(0)
	set := mustSet(t, emailRule)
	first, err := set.ApplyBody([]byte(`{"content":"john.smith@example.com"}`), tbl)
	if err != nil {
		t.Fatal(err)
	}
	alias := contentOf(t, first.Body)

	parts := []string{"mail "}
	for i := 0; i < len(alias); i += 3 {
		end := min(i+3, len(alias))
		parts = append(parts, alias[i:end])
	}
	stream := sseStream(append(parts, " now"))
	for _, chunk := range []int{1, 3, 17, len(stream)} {
		rc := NewEventRestoreReader(&chunkReader{data: []byte(stream), n: chunk}, tbl)
		out, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("chunk %d: %v", chunk, err)
		}
		if got, want := deltas(t, out), "mail john.smith@example.com now"; got != want {
			t.Errorf("chunk %d: restored = %q, want %q", chunk, got, want)
		}
		if strings.Contains(string(out), "reda") {
			t.Errorf("chunk %d: alias fragment leaked: %s", chunk, out)
		}
		if !strings.Contains(string(out), "data: [DONE]") || !strings.Contains(string(out), ": OPENROUTER PROCESSING") {
			t.Errorf("chunk %d: framing lost: %s", chunk, out)
		}
	}
}

func TestEventRestoreReaderKeepsUnresolvedPrefix(t *testing.T) {
	tbl := NewTable(0)
	set := mustSet(t, config.Redaction{Name: "e", Pattern: `[\w.+-]+@[\w.-]+`, Template: "redacted$R@gmail.com", Enabled: true})
	if _, err := set.ApplyBody([]byte(`{"content":"john.smith@example.com"}`), tbl); err != nil {
		t.Fatal(err)
	}

	// A stream that ends inside an alias prefix must still deliver the text.
	rc := NewEventRestoreReader(io.NopCloser(strings.NewReader(sseStream([]string{"reda"}))), tbl)
	out, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if got := deltas(t, out); got != "reda" {
		t.Fatalf("pending text lost: %q", got)
	}
}

func TestEventRestoreReaderLeavesOtherTextAlone(t *testing.T) {
	tbl := NewTable(0)
	tbl.Add("e", "redactedk7f2ab@gmail.com", "john.smith@example.com")

	stream := "data: {\"choices\":[{\"delta\":{\"reasoning\":\"plain words\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"no alias here\"},\"finish_reason\":\"stop\"}]}\n\n"
	rc := NewEventRestoreReader(io.NopCloser(strings.NewReader(stream)), tbl)
	out, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if got := deltas(t, out); got != "no alias here" {
		t.Fatalf("content = %q", got)
	}
	if !strings.Contains(string(out), `"reasoning":"plain words"`) {
		t.Errorf("reasoning delta lost: %s", out)
	}
}

// Records arrive on whatever field the provider chose; fragments must be joined
// per field so reasoning and tool-call arguments do not cross-contaminate.
func TestEventRestoreReaderJoinsReasoningAndArguments(t *testing.T) {
	tbl := NewTable(0)
	alias := "redactedk7f2ab@gmail.com"
	tbl.Add("e", alias, "john.smith@example.com")

	third := len(alias) / 3
	reasoning := []string{alias[:third], alias[third : 2*third], alias[2*third:]}
	args := []string{`{"path":"`, alias[:5], alias[5:], `"}`, `trunc`}
	var b strings.Builder
	for _, c := range reasoning {
		b.WriteString(`data: {"choices":[{"delta":{"reasoning":` + quote(c) + `}}]}` + "\n\n")
	}
	for _, c := range args[:4] {
		b.WriteString(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":` + quote(c) + `}}]}}]}` + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")

	out, err := io.ReadAll(NewEventRestoreReader(io.NopCloser(strings.NewReader(b.String())), tbl))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"reasoning":"john.smith@example.com"`) {
		t.Errorf("reasoning not restored: %s", out)
	}
	var arguments strings.Builder
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "data: ") || strings.Contains(line, "[DONE]") {
			continue
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Function struct {
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("event is not JSON: %v: %s", err, line)
		}
		for _, c := range ev.Choices {
			for _, tc := range c.Delta.ToolCalls {
				arguments.WriteString(tc.Function.Arguments)
			}
		}
	}
	if got, want := arguments.String(), `{"path":"john.smith@example.com"}`; got != want {
		t.Errorf("tool arguments = %q, want %q", got, want)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// CRLF framing and a final event with no trailing blank line are both valid SSE.
func TestEventRestoreReaderCRLFAndPartialTail(t *testing.T) {
	tbl := NewTable(0)
	alias := "redactedk7f2ab@gmail.com"
	tbl.Add("e", alias, "john.smith@example.com")

	stream := "data: {\"choices\":[{\"delta\":{\"content\":" + quote(alias[:9]) + "}}]}\r\n\r\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":" + quote(alias[9:]) + "}}]}"
	out, err := io.ReadAll(NewEventRestoreReader(io.NopCloser(strings.NewReader(stream)), tbl))
	if err != nil {
		t.Fatal(err)
	}
	if got := deltas(t, out); got != "john.smith@example.com" {
		t.Fatalf("restored = %q", got)
	}
}

// A data payload this proxy cannot decode still has contiguous aliases restored.
func TestEventRestoreReaderNonJSONFallsBack(t *testing.T) {
	tbl := NewTable(0)
	tbl.Add("e", "redactedk7f2ab@gmail.com", "john.smith@example.com")

	out, err := io.ReadAll(NewEventRestoreReader(
		io.NopCloser(strings.NewReader("data: see redactedk7f2ab@gmail.com now\n\n")), tbl))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "see john.smith@example.com now") {
		t.Fatalf("fallback did not restore: %s", out)
	}
}

// An alias recorded by a concurrent request part-way through a stream is picked
// up by the next read rather than being missed for the whole response.
func TestEventRestoreReaderPicksUpNewAliases(t *testing.T) {
	tbl := NewTable(0)
	tbl.Add("a", "redactedaaaaaa@gmail.com", "first@example.com")

	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"redactedbb\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"bbbb@gmail.com\"}}]}\n\n"
	rc := NewEventRestoreReader(&chunkReader{data: []byte(stream), n: len(stream) / 2}, tbl)
	tbl.Add("b", "redactedbbbbbb@gmail.com", "second@example.com")

	out, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if got := deltas(t, out); got != "second@example.com" {
		t.Fatalf("mid-stream alias missed: %q", got)
	}
}

func TestEventRestoreReaderWithoutAliasesPassesThrough(t *testing.T) {
	body := "data: {\"choices\":[]}\n\n"
	rc := NewEventRestoreReader(io.NopCloser(strings.NewReader(body)), NewTable(0))
	out, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != body {
		t.Fatalf("out = %q", out)
	}
}
