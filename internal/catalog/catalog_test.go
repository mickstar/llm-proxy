package catalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mickstar/llm-proxy/internal/config"
)

func testModels() []config.Model {
	return []config.Model{
		{
			ID:                  "deepseek/deepseek-v4.1-flash",
			Name:                "DeepSeek V4.1 Flash",
			ContextLength:       163840,
			Pricing:             config.Pricing{Prompt: "0.0000001", Completion: "0.0000002"},
			SupportedParameters: []string{"temperature", "top_p"},
			Enabled:             true,
		},
		{
			ID:            "openai/gpt-4:free",
			Name:          "GPT-4 Free",
			ContextLength: 8192,
			Enabled:       true,
		},
		{
			ID:      "disabled/model",
			Name:    "Disabled",
			Enabled: false,
		},
	}
}

func TestBuildFiltersDisabled(t *testing.T) {
	c := Build(testModels())

	if c.Has("disabled/model") {
		t.Fatal("disabled model must not be present")
	}
	if _, ok := c.Get("disabled/model"); ok {
		t.Fatal("Get returned disabled model")
	}
	if _, ok := c.Object("disabled/model"); ok {
		t.Fatal("Object returned disabled model")
	}
	list := c.List()
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	if list[0].ID != "deepseek/deepseek-v4.1-flash" || list[1].ID != "openai/gpt-4:free" {
		t.Fatalf("List order = %q, %q", list[0].ID, list[1].ID)
	}
	if !c.Has("openai/gpt-4:free") {
		t.Fatal("enabled model missing")
	}
	m, ok := c.Get("deepseek/deepseek-v4.1-flash")
	if !ok {
		t.Fatal("Get failed for enabled model")
	}
	if m.ContextLength != 163840 || m.Pricing.Prompt != "0.0000001" {
		t.Fatalf("Get returned %+v", m)
	}
}

func TestBuildSnapshotIsImmutable(t *testing.T) {
	models := testModels()
	c := Build(models)
	models[0].ID = "mutated"
	if c.Has("mutated") || !c.Has("deepseek/deepseek-v4.1-flash") {
		t.Fatal("snapshot changed after source mutation")
	}
}

func TestListObjects(t *testing.T) {
	c := Build(testModels())
	objs := c.ListObjects()
	if len(objs) != 2 {
		t.Fatalf("ListObjects len = %d, want 2", len(objs))
	}
	o := objs[0]
	if o.ID != "deepseek/deepseek-v4.1-flash" || o.Object != "model" || o.OwnedBy != "openrouter" || o.Created != 0 {
		t.Fatalf("object = %+v", o)
	}
	if o.Name != "DeepSeek V4.1 Flash" || o.ContextLength != 163840 {
		t.Fatalf("object metadata = %+v", o)
	}
}

func TestObjectUnknown(t *testing.T) {
	c := Build(testModels())
	if _, ok := c.Object("nope/nope"); ok {
		t.Fatal("Object returned unknown model")
	}
}

func TestServeHTTPList(t *testing.T) {
	c := Build(testModels())
	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	var body struct {
		Object string        `json:"object"`
		Data   []ModelObject `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Object != "list" {
		t.Fatalf("object = %q, want list", body.Object)
	}
	if len(body.Data) != 2 {
		t.Fatalf("data len = %d, want 2", len(body.Data))
	}
	if body.Data[0].ID != "deepseek/deepseek-v4.1-flash" || body.Data[1].ID != "openai/gpt-4:free" {
		t.Fatalf("data ids = %q, %q", body.Data[0].ID, body.Data[1].ID)
	}
	for _, m := range body.Data {
		if m.ID == "disabled/model" {
			t.Fatal("disabled model leaked into listing")
		}
	}
}

func TestServeHTTPListTrailingSlash(t *testing.T) {
	c := Build(testModels())
	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestServeHTTPDetail(t *testing.T) {
	c := Build(testModels())
	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/deepseek/deepseek-v4.1-flash", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var obj ModelObject
	if err := json.Unmarshal(rec.Body.Bytes(), &obj); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if obj.ID != "deepseek/deepseek-v4.1-flash" || obj.Object != "model" || obj.OwnedBy != "openrouter" || obj.Created != 0 {
		t.Fatalf("object = %+v", obj)
	}
	if obj.Name != "DeepSeek V4.1 Flash" || obj.ContextLength != 163840 {
		t.Fatalf("object metadata = %+v", obj)
	}
}

func TestServeHTTPDetailEncodedID(t *testing.T) {
	c := Build(testModels())
	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/deepseek%2Fdeepseek-v4.1-flash", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestServeHTTPUnknown(t *testing.T) {
	c := Build(testModels())
	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/some/unknown", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var env struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Code != "model_not_found" || env.Error.Type != "invalid_request_error" {
		t.Fatalf("error envelope = %+v", env)
	}
}

func TestServeHTTPDisabledIs404(t *testing.T) {
	c := Build(testModels())
	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/disabled/model", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestServeHTTPMethodNotAllowed(t *testing.T) {
	c := Build(testModels())
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		c.ServeHTTP(rec, httptest.NewRequest(method, "/v1/models", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want 405", method, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != http.MethodGet {
			t.Fatalf("%s Allow = %q, want GET", method, got)
		}
	}
}

func TestParseModelID(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		author  string
		slug    string
		wantErr bool
	}{
		{name: "invalid no slash", in: "gpt-4", wantErr: true},
		{name: "plain", in: "deepseek/deepseek-v4.1-flash", author: "deepseek", slug: "deepseek-v4.1-flash"},
		{name: "variant suffix", in: "openai/gpt-4:free", author: "openai", slug: "gpt-4:free"},
		{name: "empty", in: "", wantErr: true},
		{name: "trailing slash", in: "deepseek/", wantErr: true},
		{name: "leading slash", in: "/slug", wantErr: true},
		{name: "only slash", in: "/", wantErr: true},
		{name: "extra slashes kept in slug", in: "a/b/c", author: "a", slug: "b/c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			author, slug, err := ParseModelID(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseModelID(%q) = (%q,%q), want error", tt.in, author, slug)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseModelID(%q) error: %v", tt.in, err)
			}
			if author != tt.author || slug != tt.slug {
				t.Fatalf("ParseModelID(%q) = (%q,%q), want (%q,%q)", tt.in, author, slug, tt.author, tt.slug)
			}
		})
	}
}

func TestUpsert(t *testing.T) {
	base := []config.Model{
		{ID: "a/one", Name: "One", Enabled: true},
		{ID: "b/two", Name: "Two", Enabled: true},
	}

	out, added := Upsert(base, config.Model{ID: "c/three", Name: "Three", Enabled: true})
	if !added {
		t.Fatal("new model reported as not added")
	}
	if len(out) != 3 || out[2].ID != "c/three" {
		t.Fatalf("append result = %+v", out)
	}
	if len(base) != 2 {
		t.Fatal("Upsert mutated the input slice")
	}

	out, added = Upsert(base, config.Model{ID: "a/one", Name: "One Updated", Enabled: true})
	if added {
		t.Fatal("existing model reported as added")
	}
	if len(out) != 2 {
		t.Fatalf("replace changed length to %d", len(out))
	}
	if out[0].ID != "a/one" || out[0].Name != "One Updated" || out[1].ID != "b/two" {
		t.Fatalf("replace result = %+v", out)
	}
	if base[0].Name != "One" || base[0].ID != "a/one" {
		t.Fatal("input slice was mutated")
	}
}
