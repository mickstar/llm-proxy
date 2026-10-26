package openrouter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const fullBody = `{
	"id": "deepseek/deepseek-v4.1-flash",
	"name": "DeepSeek V4.1 Flash",
	"context_length": 163840,
	"pricing": {"prompt": "0.0000001", "completion": "0.0000002", "image": "0"},
	"supported_parameters": ["temperature", "top_p", "max_tokens"],
	"unknown_field": {"nested": [1, 2, 3]},
	"architecture": "ignored"
}`

func TestFetchModelTopLevel(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/model/deepseek/deepseek-v4.1-flash" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fullBody))
	}))
	defer srv.Close()

	c := New(srv.URL, "test-key")
	m, err := c.FetchModel(context.Background(), "deepseek/deepseek-v4.1-flash")
	if err != nil {
		t.Fatalf("FetchModel: %v", err)
	}
	if m.ID != "deepseek/deepseek-v4.1-flash" {
		t.Errorf("ID = %q", m.ID)
	}
	if m.Name != "DeepSeek V4.1 Flash" {
		t.Errorf("Name = %q", m.Name)
	}
	if m.ContextLength != 163840 {
		t.Errorf("ContextLength = %d", m.ContextLength)
	}
	if m.Pricing.Prompt != "0.0000001" || m.Pricing.Completion != "0.0000002" {
		t.Errorf("Pricing = %+v", m.Pricing)
	}
	if !reflect.DeepEqual(m.SupportedParameters, []string{"temperature", "top_p", "max_tokens"}) {
		t.Errorf("SupportedParameters = %v", m.SupportedParameters)
	}
	if !m.Enabled {
		t.Error("Enabled = false, want true")
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func TestFetchModelEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":` + fullBody + `}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	m, err := c.FetchModel(context.Background(), "deepseek/deepseek-v4.1-flash")
	if err != nil {
		t.Fatalf("FetchModel: %v", err)
	}
	if m.ID != "deepseek/deepseek-v4.1-flash" || m.Name != "DeepSeek V4.1 Flash" || m.ContextLength != 163840 {
		t.Errorf("model = %+v", m)
	}
	if m.Pricing.Prompt != "0.0000001" || m.Pricing.Completion != "0.0000002" {
		t.Errorf("Pricing = %+v", m.Pricing)
	}
	if !reflect.DeepEqual(m.SupportedParameters, []string{"temperature", "top_p", "max_tokens"}) {
		t.Errorf("SupportedParameters = %v", m.SupportedParameters)
	}
}

func TestFetchModelMissingFieldsTolerated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"a/b"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	m, err := c.FetchModel(context.Background(), "a/b")
	if err != nil {
		t.Fatalf("FetchModel: %v", err)
	}
	if m.Name != "" || m.ContextLength != 0 || m.Pricing.Prompt != "" || m.Pricing.Completion != "" {
		t.Errorf("model = %+v, want zero values", m)
	}
	if m.SupportedParameters != nil {
		t.Errorf("SupportedParameters = %v, want nil", m.SupportedParameters)
	}
	if !m.Enabled {
		t.Error("Enabled = false, want true")
	}
}

func TestFetchModelNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	_, err := c.FetchModel(context.Background(), "deepseek/nope")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error %q missing status", err)
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Errorf("error %q missing body excerpt", err)
	}
}

func TestFetchModelInvalidIDMakesNoRequest(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-key")
	for _, id := range []string{"gpt-4", "", "deepseek/", "/slug"} {
		if _, err := c.FetchModel(context.Background(), id); err == nil {
			t.Errorf("FetchModel(%q) = nil error, want error", id)
		}
	}
	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
}

func TestFetchModelOmitsAuthWithoutKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty", got)
		}
		_, _ = w.Write([]byte(`{"id":"a/b","name":"AB"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	if _, err := c.FetchModel(context.Background(), "a/b"); err != nil {
		t.Fatalf("FetchModel: %v", err)
	}
}

func TestNewTrimsTrailingSlash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model/openai/gpt-4:free" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"openai/gpt-4:free"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/", "")
	if _, err := c.FetchModel(context.Background(), "openai/gpt-4:free"); err != nil {
		t.Fatalf("FetchModel: %v", err)
	}
}
