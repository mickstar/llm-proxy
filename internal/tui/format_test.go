package tui

import (
	"strings"
	"testing"

	"github.com/mickstar/llm-proxy/internal/traffic"
)

func TestPrettyBodyJSON(t *testing.T) {
	in := `{"model":"m","messages":[{"role":"user","content":"hi"}],"temperature":0.7,"n":123456789012345678901234567890}`
	out := prettyBody(in)
	if out == in {
		t.Fatal("expected re-indented output")
	}
	if !strings.Contains(out, "\n  \"model\": \"m\"") {
		t.Errorf("not indented:\n%s", out)
	}
	if !strings.Contains(out, "\n  \"temperature\": 0.7") {
		t.Errorf("non-string value missing:\n%s", out)
	}
	if !strings.Contains(out, "123456789012345678901234567890") {
		t.Errorf("large number literal not preserved:\n%s", out)
	}
}

func TestPrettyBodySSE(t *testing.T) {
	in := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	out := prettyBody(in)
	if !strings.Contains(out, "data: {") || !strings.Contains(out, "\"content\": \"hi\"") {
		t.Errorf("SSE payload not formatted:\n%s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("done marker lost:\n%s", out)
	}
}

func TestPrettyBodyPassthrough(t *testing.T) {
	cases := []string{
		"",
		"hello world",
		"--X\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\nraw\r\n--X--\r\n",
	}
	for _, in := range cases {
		if got := prettyBody(in); got != in {
			t.Errorf("prettyBody(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestEntryDetailFormatsBodies(t *testing.T) {
	e := traffic.Entry{
		Method:       "POST",
		Path:         "/v1/chat/completions",
		Status:       200,
		RequestBody:  `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		ResponseBody: `{"id":"x","choices":[]}`,
	}
	got := entryDetail(e)
	if !strings.Contains(got, "\n  \"model\": \"m\"") {
		t.Errorf("request body not formatted:\n%s", got)
	}
	if !strings.Contains(got, "\n  \"id\": \"x\"") {
		t.Errorf("response body not formatted:\n%s", got)
	}
}
