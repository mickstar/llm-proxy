package usage

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestExtractJSONResponse(t *testing.T) {
	body := []byte(`{"id":"cmpl-1","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":12,"completion_tokens":34,"total_tokens":46,"cost":0.000123}}`)
	got, ok := Extract(body)
	if !ok {
		t.Fatal("Extract found no usage")
	}
	want := Usage{PromptTokens: 12, CompletionTokens: 34, TotalTokens: 46, CostUSD: 0.000123, CostReported: true}
	if got != want {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
}

func TestExtractSSEUsesFinalChunk(t *testing.T) {
	body := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":7,\"total_tokens\":12,\"cost\":0.000456}}\n\n" +
		"data: [DONE]\n\n")
	got, ok := Extract(body)
	if !ok {
		t.Fatal("Extract found no usage")
	}
	if got.TotalTokens != 12 || got.PromptTokens != 5 || got.CompletionTokens != 7 || got.CostUSD != 0.000456 {
		t.Fatalf("usage = %+v", got)
	}
}

func TestExtractIgnoresUsageQuotedInContent(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"say \"usage\": {\"prompt_tokens\": 999, \"completion_tokens\": 999}"}}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	got, ok := Extract(body)
	if !ok {
		t.Fatal("Extract found no usage")
	}
	if got.TotalTokens != 7 {
		t.Fatalf("picked the quoted usage: %+v", got)
	}
}

func TestExtractResponsesAPINames(t *testing.T) {
	body := []byte(`{"output":[],"usage":{"input_tokens":11,"output_tokens":22,"total_tokens":33}}`)
	got, ok := Extract(body)
	if !ok {
		t.Fatal("Extract found no usage")
	}
	want := Usage{PromptTokens: 11, CompletionTokens: 22, TotalTokens: 33}
	if got != want {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
}

func TestExtractCachedPromptTokens(t *testing.T) {
	body := []byte(`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,` +
		`"prompt_tokens_details":{"cached_tokens":64}}}`)
	got, ok := Extract(body)
	if !ok {
		t.Fatal("Extract found no usage")
	}
	if got.CachedPromptTokens != 64 {
		t.Fatalf("cached = %d, want 64", got.CachedPromptTokens)
	}
	if got.CostReported {
		t.Fatal("absent cost must not be reported")
	}

	// Responses API shape, and a cached count larger than the prompt count.
	body = []byte(`{"usage":{"input_tokens":10,"output_tokens":1,"input_tokens_details":{"cached_tokens":40}}}`)
	got, ok = Extract(body)
	if !ok || got.CachedPromptTokens != 10 {
		t.Fatalf("clamped cached = %d, ok = %v; want 10, true", got.CachedPromptTokens, ok)
	}
}

func TestExtractMissingOrIncomplete(t *testing.T) {
	for _, body := range []string{
		``,
		`{"choices":[]}`,
		`{"usage":{"prompt_tokens":1`,
		`{"usage":null}`,
		`{"usage":{}}`,
		`{"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
	} {
		if u, ok := Extract([]byte(body)); ok {
			t.Errorf("Extract(%q) = %+v, true; want false", body, u)
		}
	}
}

func TestReaderCapturesAcrossChunks(t *testing.T) {
	body := `{"usage":{"prompt_tokens":8,"completion_tokens":9,"total_tokens":17}}`
	for _, tc := range []struct {
		name string
		r    io.Reader
	}{
		{"one-byte", iotest.OneByteReader(strings.NewReader(body))},
		{"half", iotest.HalfReader(strings.NewReader(body))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c Capture
			r := NewReader(io.NopCloser(tc.r), &c)
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != body {
				t.Fatalf("body changed: %q", got)
			}
			u, ok := c.Get()
			if !ok || u.TotalTokens != 17 {
				t.Fatalf("usage = %+v, %v", u, ok)
			}
		})
	}
}

func TestReaderLongBodyKeepsTailUsage(t *testing.T) {
	prefix := bytes.Repeat([]byte("x"), tailLimit*2)
	body := append(prefix, []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)...)
	var c Capture
	r := NewReader(io.NopCloser(bytes.NewReader(body)), &c)
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
	u, ok := c.Get()
	if !ok || u.TotalTokens != 3 {
		t.Fatalf("usage = %+v, %v", u, ok)
	}
}

func TestReaderCloseFinishes(t *testing.T) {
	const body = `{"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4}}`
	var c Capture
	r := NewReader(io.NopCloser(strings.NewReader(body)), &c)
	if _, err := io.ReadFull(r, make([]byte, len(body))); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(); ok {
		t.Fatal("usage captured before the body ended")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if u, ok := c.Get(); !ok || u.TotalTokens != 4 {
		t.Fatalf("usage after close = %+v, %v", u, ok)
	}
}
