package tui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mickstar/llm-proxy/internal/traffic"
)

var errFake = errors.New("boom")

func sampleEntry() traffic.Entry {
	return traffic.Entry{
		ID:            7,
		Time:          time.Unix(1700000000, 0),
		Method:        "POST",
		Path:          "/v1/chat/completions",
		Model:         "deepseek/deepseek-v4.1-flash",
		Status:        200,
		DurationMS:    42,
		RequestSize:   128,
		ResponseSize:  256,
		FilterSummary: "redact-keys:1",
	}
}

// TestTrafficModelStreamedEntry proves a traffic.Entry delivered as a message
// (as the SSE goroutine does) lands in the traffic view model and its table,
// and that the first row stays selectable after an empty backfill (bubbles
// leaves the cursor at -1 once a table is emptied).
func TestTrafficModelStreamedEntry(t *testing.T) {
	m := newTrafficModel(nil)
	m.resize(100, 30)

	// The initial GET /api/traffic may come back empty before any SSE entry.
	m.handleEntries(entriesMsg{entries: nil})
	if got := len(m.table.Rows()); got != 0 {
		t.Fatalf("initial rows = %d, want 0", got)
	}

	e := sampleEntry()
	if cmd := m.update(e); cmd != nil {
		t.Fatalf("streamed entry returned a command, want nil")
	}

	if len(m.entries) != 1 || m.entries[0].ID != e.ID {
		t.Fatalf("entries = %+v, want a single entry with id %d", m.entries, e.ID)
	}
	rows := m.table.Rows()
	if len(rows) != 1 {
		t.Fatalf("table rows = %d, want 1", len(rows))
	}
	if rows[0][1] != e.Model {
		t.Errorf("row model = %q, want %q", rows[0][1], e.Model)
	}
	if rows[0][5] != e.FilterSummary {
		t.Errorf("row filter outcome = %q, want %q", rows[0][5], e.FilterSummary)
	}
	if got := m.table.Cursor(); got != 0 {
		t.Fatalf("cursor = %d, want 0 (first row selectable without navigation)", got)
	}
}

// TestRootRoutesStreamedEntryToTraffic proves entries arriving while another
// tab is focused still update the traffic view.
func TestRootRoutesStreamedEntryToTraffic(t *testing.T) {
	m := newRootModel(nil)
	m.tab = tabFilters

	next, cmd := m.Update(sampleEntry())
	if cmd != nil {
		t.Fatalf("root Update returned a command, want nil")
	}
	rm := next.(rootModel)
	if got := len(rm.traffic.table.Rows()); got != 1 {
		t.Fatalf("traffic rows = %d, want 1", got)
	}
}

// TestTrafficBackfillNewestFirst proves the backfill response populates the
// table in the order the server sent it.
func TestTrafficBackfillNewestFirst(t *testing.T) {
	m := newTrafficModel(nil)
	m.resize(100, 30)

	m.handleEntries(entriesMsg{entries: []traffic.Entry{
		{ID: 2, Time: time.Unix(2, 0), Model: "new"},
		{ID: 1, Time: time.Unix(1, 0), Model: "old"},
	}})

	rows := m.table.Rows()
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0][1] != "new" || rows[1][1] != "old" {
		t.Fatalf("rows = %v, want newest-first", rows)
	}
}

// TestRootViewRendersEveryTab proves the whole view renders on each tab
// without panicking (the program must show the Traffic tab on start).
func TestRootViewRendersEveryTab(t *testing.T) {
	for _, tab := range []int{tabTraffic, tabRedactions, tabFilters, tabModels, tabSettings} {
		m := newRootModel(nil)
		m.tab = tab
		next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		out := next.(rootModel).View()
		if !strings.Contains(out, tabNames[tab]) {
			t.Errorf("tab %d: view missing tab label %q", tab, tabNames[tab])
		}
	}
}

// TestGlobalTabKeys covers tab/shift+tab cycling and the 1-4 shortcuts.
func TestGlobalTabKeys(t *testing.T) {
	m := newRootModel(nil)
	m.resize()

	press := func(m rootModel, key string) rootModel {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		return next.(rootModel)
	}
	if m.tab != tabTraffic {
		t.Fatalf("start tab = %d, want Traffic", m.tab)
	}
	if got := press(m, "3"); got.tab != tabFilters {
		t.Errorf("after '3' tab = %d, want Filters", got.tab)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := next.(rootModel).tab; got != tabRedactions {
		t.Errorf("after tab = %d, want Redactions", got)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if got := next.(rootModel).tab; got != tabSettings {
		t.Errorf("after shift+tab = %d, want Settings", got)
	}
}

// TestQuitKeys checks the documented quit bindings produce tea.Quit.
func TestQuitKeys(t *testing.T) {
	m := newRootModel(nil)
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("q")},
		{Type: tea.KeyCtrlC},
	} {
		cmd, handled := m.globalKey(key)
		if !handled || cmd == nil {
			t.Errorf("key %q: handled=%v cmd=%v, want quit", key.String(), handled, cmd)
		}
	}
}

// TestTrafficOpenSelectedFetchesDetail covers the whole inspect flow without a
// terminal: a streamed entry, Enter key, the detail HTTP fetch, and the view.
func TestTrafficOpenSelectedFetchesDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/traffic/7" {
			t.Errorf("path = %q, want /api/traffic/7", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(traffic.Entry{
			ID:           7,
			Method:       "POST",
			Path:         "/v1/chat/completions",
			Status:       200,
			RequestBody:  `{"model":"x"}`,
			ResponseBody: `{"ok":true}`,
		})
	}))
	defer srv.Close()

	m := newTrafficModel(NewClient(srv.URL))
	m.resize(100, 30)
	m.update(sampleEntry())

	cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a selected row produced no fetch command")
	}
	msg := cmd()
	detail, ok := msg.(entryDetailMsg)
	if !ok {
		t.Fatalf("command produced %T, want entryDetailMsg", msg)
	}
	if detail.err != nil {
		t.Fatalf("fetch detail: %v", detail.err)
	}
	m.handleDetail(detail)
	if !m.detail {
		t.Fatal("detail view not opened")
	}
	out := m.view()
	for _, want := range []string{"request body", "response body", `"model"`, `"x"`, `"ok"`} {
		if !strings.Contains(out, want) {
			t.Errorf("detail view missing %q", want)
		}
	}
}

func TestModelPathKeepsSlashLiteral(t *testing.T) {
	got := modelPath("deepseek/deepseek-v4.1-flash:free")
	want := "/api/models/deepseek/deepseek-v4.1-flash:free"
	if got != want {
		t.Errorf("modelPath = %q, want %q", got, want)
	}
	if got := modelPath("a b/c"); got != "/api/models/a%20b/c" {
		t.Errorf("modelPath with space = %q", got)
	}
}

// TestClientEndpoints pins the exact method and path each client call uses,
// including the '/'-preserving model id path, and that API errors surface.
func TestClientEndpoints(t *testing.T) {
	type call struct{ method, path, body string }
	var got call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = call{r.Method, r.URL.EscapedPath(), string(b)}
		if r.URL.Query().Get("fail") == "1" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/filters/test"):
			_, _ = w.Write([]byte(`{"matches":2,"matched":["a","a"],"result":"b"}`))
		case r.URL.Path == "/api/filters" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"name":"f","pattern":"p","action":"replace","enabled":true}]`))
		case strings.Contains(r.URL.Path, "/api/filters"):
			_, _ = w.Write([]byte(`{"name":"f","pattern":"p","action":"replace","replacement":"x","enabled":true}`))
		case r.URL.Path == "/api/models" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"id":"a/b","name":"n","context_length":10,"pricing":{"prompt":"1","completion":"2"},"supported_parameters":["temperature"],"enabled":true}]`))
		case strings.Contains(r.URL.Path, "/api/models"):
			_, _ = w.Write([]byte(`{"id":"a/b","name":"n","context_length":10,"pricing":{"prompt":"1","completion":"2"},"supported_parameters":["temperature"],"enabled":true}`))
		case r.URL.Path == "/api/traffic/7":
			_, _ = w.Write([]byte(`{"id":7,"method":"POST","path":"/v1/x","status":200,"request_body":"{}"}`))
		case r.URL.Path == "/api/traffic":
			_, _ = w.Write([]byte(`[{"id":1,"path":"/v1/x","status":200}]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL)

	check := func(name, method, path, body string) {
		t.Helper()
		if got.method != method || got.path != path {
			t.Errorf("%s: got %s %s, want %s %s", name, got.method, got.path, method, path)
		}
		if body != "" && got.body != body {
			t.Errorf("%s: body = %q, want %q", name, got.body, body)
		}
	}

	if _, err := c.Health(); err != nil {
		t.Fatal(err)
	}
	check("health", "GET", "/api/health", "")

	if _, err := c.GetConfig(); err != nil {
		t.Fatal(err)
	}
	check("config", "GET", "/api/config", "")

	if _, err := c.PutSettings(ServerConfig{ProxyAddr: "a", TrafficLogCapacity: 3}, OpenRouterConfig{BaseURL: "u"}); err != nil {
		t.Fatal(err)
	}
	check("settings", "PUT", "/api/settings", `{"openrouter":{"base_url":"u","api_key":""},"server":{"proxy_addr":"a","control_addr":"","model_policy":"","traffic_log_capacity":3}}`)

	if _, err := c.ListFilters(); err != nil {
		t.Fatal(err)
	}
	check("list filters", "GET", "/api/filters", "")

	if _, err := c.CreateFilter(Filter{Name: "f"}); err != nil {
		t.Fatal(err)
	}
	check("create filter", "POST", "/api/filters", `{"name":"f","pattern":"","action":"","enabled":false}`)

	if _, err := c.UpdateFilter("f", Filter{Name: "f"}); err != nil {
		t.Fatal(err)
	}
	check("update filter", "PUT", "/api/filters/f", `{"name":"f","pattern":"","action":"","enabled":false}`)

	if err := c.DeleteFilter("f"); err != nil {
		t.Fatal(err)
	}
	check("delete filter", "DELETE", "/api/filters/f", "")

	tr, err := c.TestFilter("p", "", "x", "sample")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Matches != 2 || tr.Result != "b" {
		t.Errorf("test result = %+v", tr)
	}
	check("test filter", "POST", "/api/filters/test", `{"pattern":"p","pattern_file":"","replacement":"x","sample":"sample"}`)

	if _, err := c.ListModels(); err != nil {
		t.Fatal(err)
	}
	check("list models", "GET", "/api/models", "")

	if _, err := c.UpdateModel("author/cool:free", Model{ID: "author/cool:free"}); err != nil {
		t.Fatal(err)
	}
	check("update model", "PUT", "/api/models/author/cool:free", "")

	if err := c.DeleteModel("author/cool:free"); err != nil {
		t.Fatal(err)
	}
	check("delete model", "DELETE", "/api/models/author/cool:free", "")

	if _, err := c.FetchModel("author/slug"); err != nil {
		t.Fatal(err)
	}
	check("fetch model", "POST", "/api/models/fetch", `{"id":"author/slug"}`)

	if _, err := c.ListTraffic(200); err != nil {
		t.Fatal(err)
	}
	check("list traffic", "GET", "/api/traffic", "")

	if _, err := c.GetTraffic(7); err != nil {
		t.Fatal(err)
	}
	check("get traffic", "GET", "/api/traffic/7", "")

	// API errors surface the {"error":...} message.
	if err := c.do("GET", "/api/health?fail=1", nil, nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v, want containing %q", err, "boom")
	}
}

// TestRootEnterOpensDetail drives the whole program path: root Update routing
// the SSE entry, the Enter key, the detail fetch command, and the resulting
// message updating the traffic view.
func TestRootEnterOpensDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(traffic.Entry{
			ID: 7, Method: "POST", Path: "/v1/chat/completions", Status: 200,
			RequestBody: `{"model":"x"}`, ResponseBody: `{"ok":true}`,
		})
	}))
	defer srv.Close()

	m := newRootModel(NewClient(srv.URL))
	m.resize()

	// Empty backfill first, as on a fresh server before any SSE entry.
	next0, _ := m.Update(entriesMsg{entries: nil})
	m = next0.(rootModel)

	if next, _ := m.Update(sampleEntry()); len(next.(rootModel).traffic.table.Rows()) != 1 {
		t.Fatal("streamed entry did not reach the traffic table")
	} else {
		m = next.(rootModel)
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("root Update on enter returned no command")
	}
	m = next.(rootModel)

	next2, _ := m.Update(cmd())
	rm := next2.(rootModel)
	if !rm.traffic.detail {
		t.Fatal("root routing did not open the detail view")
	}
	if out := rm.traffic.view(); !strings.Contains(out, "request body") || !strings.Contains(out, `"model"`) {
		t.Fatalf("detail view not rendered:\n%s", out)
	}
}

// TestFiltersToggleSelected covers the list-mode space key: it must PUT the
// selected filter with its enabled flag flipped, on the first row, without any
// prior navigation.
func TestFiltersToggleSelected(t *testing.T) {
	var calls []struct{ method, path, body string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, struct{ method, path, body string }{r.Method, r.URL.EscapedPath(), string(b)})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"f","pattern":"p","action":"replace","replacement":"[X]","enabled":false}`))
	}))
	defer srv.Close()

	m := newFiltersModel(NewClient(srv.URL))
	m.handleList(filtersMsg{filters: []Filter{{Name: "f", Pattern: "p", Action: "replace", Replacement: "[X]", Enabled: true}}})

	if got := m.table.Cursor(); got != 0 {
		t.Fatalf("cursor = %d, want 0", got)
	}
	cmd := m.update(tea.KeyMsg{Type: tea.KeySpace})
	if cmd == nil {
		t.Fatal("space on the selected filter produced no command")
	}
	msg, ok := cmd().(filterSavedMsg)
	if !ok || msg.err != nil {
		t.Fatalf("toggle message = %#v", msg)
	}
	if len(calls) != 1 || calls[0].method != "PUT" || calls[0].path != "/api/filters/f" {
		t.Fatalf("calls = %+v, want one PUT /api/filters/f", calls)
	}
	if !strings.Contains(calls[0].body, `"enabled":false`) {
		t.Errorf("toggle body = %s, want enabled false", calls[0].body)
	}
}

// TestFiltersLiveTestDebounce checks that only the newest scheduled test runs
// and that results and errors land in the test pane state.
func TestFiltersLiveTestDebounce(t *testing.T) {
	m := newFiltersModel(nil)
	m.editing = true
	m.pattern.SetValue("a")

	m.scheduleTest()
	stale := m.testSeq
	m.scheduleTest()

	if cmd := m.handleTick(testTickMsg{seq: stale}); cmd != nil {
		t.Error("stale tick triggered a test")
	}
	if cmd := m.handleTick(testTickMsg{seq: m.testSeq}); cmd == nil {
		t.Error("current tick did not trigger a test")
	}

	m.handleTest(testResultMsg{tr: TestResult{Matches: 2, Result: "b"}})
	if !m.testOnce || m.test.Matches != 2 || m.test.Result != "b" || m.testErr != "" {
		t.Fatalf("test pane = %+v err=%q", m.test, m.testErr)
	}
	m.handleTest(testResultMsg{err: errFake})
	if m.testErr != "boom" || m.test.Matches != 0 {
		t.Fatalf("error test pane = %+v err=%q", m.test, m.testErr)
	}
}

// TestModelsDeleteUsesSelection checks 'd' deletes the first row without any
// prior navigation key.
func TestModelsDeleteUsesSelection(t *testing.T) {
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	m := newModelsModel(NewClient(srv.URL))
	m.handleList(modelsMsg{models: []Model{{ID: "author/slug", Enabled: true}}})
	if got := m.table.Cursor(); got != 0 {
		t.Fatalf("cursor = %d, want 0", got)
	}
	cmd := m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if cmd == nil {
		t.Fatal("d produced no command")
	}
	msg, ok := cmd().(modelDeletedMsg)
	if !ok || msg.err != nil {
		t.Fatalf("delete message = %#v", msg)
	}
	if method != "DELETE" || path != "/api/models/author/slug" {
		t.Fatalf("request = %s %s", method, path)
	}
}

// TestSettingsSave posts the edited values.
func TestSettingsSave(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server":{"proxy_addr":"p","control_addr":"c","traffic_log_capacity":9},"openrouter":{"base_url":"u","api_key":"k"}}`))
	}))
	defer srv.Close()

	m := newSettingsModel(NewClient(srv.URL))
	m.handleConfig(configMsg{cfg: Config{Server: ServerConfig{ProxyAddr: "old", TrafficLogCapacity: 1}, OpenRouter: OpenRouterConfig{BaseURL: "old"}}})
	if !m.loaded || m.editing {
		t.Fatalf("loaded=%v editing=%v", m.loaded, m.editing)
	}
	m.editing = true
	m.proxy.SetValue("p")
	m.control.SetValue("c")
	m.baseURL.SetValue("u")
	m.apiKey.SetValue("k")
	m.cap.SetValue("9")

	cmd := m.save()
	if cmd == nil {
		t.Fatal("save produced no command")
	}
	msg, ok := cmd().(settingsSavedMsg)
	if !ok || msg.err != nil {
		t.Fatalf("save message = %#v", msg)
	}
	for _, want := range []string{`"proxy_addr":"p"`, `"traffic_log_capacity":9`, `"api_key":"k"`} {
		if !strings.Contains(body, want) {
			t.Errorf("settings body %s missing %s", body, want)
		}
	}
	m.handleSaved(msg)
	if m.editing {
		t.Error("save did not leave edit mode")
	}
	m.cap.SetValue("nope")
	if cmd := m.save(); cmd != nil || m.errText == "" {
		t.Errorf("bad capacity: cmd=%v errText=%q", cmd, m.errText)
	}
}

func TestFilterOutcome(t *testing.T) {
	cases := []struct {
		name string
		e    traffic.Entry
		want string
	}{
		{"blocked", traffic.Entry{Blocked: true}, "blocked"},
		{"not filtered", traffic.Entry{NotFiltered: true}, "not filtered"},
		{"summary", traffic.Entry{FilterSummary: "redact:2"}, "redact:2"},
		{"none", traffic.Entry{}, "-"},
	}
	for _, tc := range cases {
		if got := filterOutcome(tc.e); got != tc.want {
			t.Errorf("%s: filterOutcome = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestEntryDetailShowsBodies(t *testing.T) {
	e := sampleEntry()
	e.RequestBody = `{"model":"x"}`
	e.ResponseBody = "plain text response"
	e.PromptTokens = 5
	e.CompletionTokens = 7
	e.TotalTokens = 12
	e.CostUSD = 0.000789
	e.CachedPromptTokens = 3
	out := entryDetail(e)
	// JSON bodies are re-indented for display, so assert content and headers,
	// not the original byte layout; non-JSON bodies are shown verbatim.
	for _, want := range []string{"request body", "response body", `"model"`, `"x"`, "plain text response", "tokens:     12 (prompt 5, completion 7)", "cached:     3 prompt tokens", "cost:       $0.000789"} {
		if !strings.Contains(out, want) {
			t.Errorf("entryDetail missing %q", want)
		}
	}
}

// TestModelsFormBuildsEntry checks the model editor builds a Model from its
// inputs without hitting the network.
func TestModelsFormBuildsEntry(t *testing.T) {
	m := newModelsModel(nil)
	m.mode = modelsEdit
	m.editID = "deepseek/deepseek-v4.1-flash"
	m.name.SetValue("DeepSeek")
	m.context.SetValue("163840")
	m.prompt.SetValue("0.000001")
	m.cache.SetValue("0.00000002")
	m.complet.SetValue("0.000002")
	m.params.SetValue("temperature, tools,")
	m.enabled = true

	got, err := m.form()
	if err != nil {
		t.Fatalf("form: %v", err)
	}
	if got.ID != m.editID || got.Name != "DeepSeek" || got.ContextLength != 163840 {
		t.Fatalf("form = %+v", got)
	}
	if got.Pricing.Prompt != "0.000001" || got.Pricing.Cache != "0.00000002" || got.Pricing.Completion != "0.000002" {
		t.Fatalf("pricing = %+v", got.Pricing)
	}
	if len(got.SupportedParameters) != 2 {
		t.Fatalf("params = %v, want 2", got.SupportedParameters)
	}
	if !got.Enabled {
		t.Error("enabled = false, want true")
	}

	m.context.SetValue("not-a-number")
	if _, err := m.form(); err == nil {
		t.Error("form with bad context length: want error")
	}
}

// TestFiltersTableShowsMatchSource proves the list distinguishes file-backed
// filters from inline patterns.
func TestFiltersTableShowsMatchSource(t *testing.T) {
	m := newFiltersModel(nil)
	m.handleList(filtersMsg{filters: []Filter{
		{Name: "inline", Pattern: "james"},
		{Name: "keys", PatternFile: "~/.ssh/id_rsa"},
	}})

	rows := m.table.Rows()
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0][1] != "james" {
		t.Errorf("inline row = %q, want the pattern", rows[0][1])
	}
	if rows[1][1] != "file: ~/.ssh/id_rsa" {
		t.Errorf("pattern_file row = %q, want the file spec", rows[1][1])
	}
}

// TestFiltersEditorPatternFile proves the editor loads and saves a pattern_file
// and rejects ambiguous match sources before any request.
func TestFiltersEditorPatternFile(t *testing.T) {
	m := newFiltersModel(nil)
	m.handleList(filtersMsg{filters: []Filter{
		{Name: "keys", PatternFile: "~/.ssh/*.pem", Action: "block", Enabled: true},
	}})
	m.startEditor(0)
	if got := m.patternFile.Value(); got != "~/.ssh/*.pem" {
		t.Fatalf("patternFile = %q, want the stored spec", got)
	}
	if cmd := m.save(); cmd == nil {
		t.Fatal("save returned nil command for a valid file-backed filter")
	}

	m.pattern.SetValue("x")
	if msg := m.save()(); msg.(statusMsg).text != "filter pattern and pattern file are mutually exclusive" {
		t.Errorf("both sources message = %v", msg)
	}

	m.pattern.SetValue("")
	m.patternFile.SetValue("")
	if msg := m.save()(); msg.(statusMsg).text != "filter pattern or pattern file is required" {
		t.Errorf("no source message = %v", msg)
	}
}

// TestSettingsPolicyToggle flips the policy from the settings form and saves it.
func TestSettingsPolicyToggle(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server":{"model_policy":"allowlist"},"openrouter":{}}`))
	}))
	defer srv.Close()

	m := newSettingsModel(NewClient(srv.URL))
	m.handleConfig(configMsg{cfg: Config{Server: ServerConfig{ModelPolicy: policyPassthrough, TrafficLogCapacity: 1}}})
	if m.policy != policyPassthrough {
		t.Fatalf("populated policy = %q", m.policy)
	}

	m.editing = true
	m.setFocus(sFocusPolicy)
	m.update(tea.KeyMsg{Type: tea.KeySpace})
	if m.policy != policyAllowlist {
		t.Fatalf("after toggle policy = %q, want %q", m.policy, policyAllowlist)
	}
	m.update(tea.KeyMsg{Type: tea.KeySpace})
	if m.policy != policyPassthrough {
		t.Fatalf("after second toggle policy = %q", m.policy)
	}

	// The rendered form shows the current policy and survives a full focus cycle.
	if view := m.view(); !strings.Contains(view, "Model policy") || !strings.Contains(view, "passthrough") {
		t.Errorf("settings view missing policy row: %s", view)
	}
	for i := 0; i < sFocusCount; i++ {
		m.update(tea.KeyMsg{Type: tea.KeyTab})
	}
	if m.focus != sFocusPolicy {
		t.Fatalf("focus after a full cycle = %d, want %d", m.focus, sFocusPolicy)
	}

	m.cap.SetValue("1")
	cmd := m.save()
	if cmd == nil {
		t.Fatal("save produced no command")
	}
	cmd()
	if !strings.Contains(body, `"model_policy":"passthrough"`) {
		t.Errorf("settings body %s missing toggled policy", body)
	}
}

// TestRedactionsTabListsInstances renders the alias table from the API.
func TestRedactionsTabListsInstances(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/redactions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"alias":"redacted473919@gmail.com","original":"john.smith@example.com","rule":"email","count":2,"first_seen":"2026-10-24T10:00:00Z"}]`))
	}))
	defer srv.Close()

	m := newRedactionsModel(NewClient(srv.URL))
	msg := loadRedactionsCmd(m.client)()
	if cmd := m.handleList(msg.(redactionsMsg)); cmd != nil {
		t.Fatal("unexpected command")
	}
	if len(m.items) != 1 {
		t.Fatalf("items = %+v", m.items)
	}
	view := m.view()
	for _, want := range []string{"redacted473919@gmail.com", "john.smith@example.com", "email", "1 alias"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestRedactionsTabEmptyState(t *testing.T) {
	m := newRedactionsModel(NewClient("http://example.test"))
	m.handleList(redactionsMsg{items: nil})
	if view := m.view(); !strings.Contains(view, "nothing redacted yet") {
		t.Errorf("view = %s", view)
	}
}

func TestHaltedBanner(t *testing.T) {
	m := newRootModel(nil)
	m.halt = HaltStatus{Halted: true, Filter: "secret", Refused: 3}
	view := m.bannerView()
	for _, want := range []string{"HALTED", "secret", "3 request", "press R"} {
		if !strings.Contains(view, want) {
			t.Errorf("banner missing %q: %s", want, view)
		}
	}
}

func TestResumeKeyOnlyWhenHalted(t *testing.T) {
	m := newRootModel(NewClient("http://example.test"))
	if cmd, handled := m.globalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")}); handled || cmd != nil {
		t.Fatal("R handled while the proxy is running")
	}
	m.halt = HaltStatus{Halted: true, Filter: "secret"}
	if cmd, handled := m.globalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")}); !handled || cmd == nil {
		t.Fatal("R did not trigger a resume")
	}
}

func TestSuggestReplacement(t *testing.T) {
	cases := map[string]string{
		"john@gmail.com":   "redacted$R@gmail.com",
		`[\w.+-]+@[\w.-]+`: "redacted$R@gmail.com",
		`sk-live-\w+`:      "[REDACTED-$R]",
		"James Brown":      "[REDACTED-$R]",
		"/Users/jane.doe/":  "[REDACTED-$R]",
	}
	for pattern, want := range cases {
		if got := suggestReplacement(pattern); got != want {
			t.Errorf("suggestReplacement(%q) = %q, want %q", pattern, got, want)
		}
	}
}

func TestFilterEditorPrefillsReplacement(t *testing.T) {
	m := newFiltersModel(NewClient("http://example.test"))
	m.startEditor(-1)
	m.action = "block"
	m.pattern.SetValue("john@gmail.com")
	m.replacement.SetValue("")

	m.update(tea.KeyMsg{Type: tea.KeyCtrlA})
	if m.action != "replace" {
		t.Fatalf("action = %q", m.action)
	}
	if got := m.replacement.Value(); got != "redacted$R@gmail.com" {
		t.Errorf("prefill = %q", got)
	}

	// ctrl+g regenerates from the current pattern.
	m.pattern.SetValue("sk-live-abc")
	m.update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if got := m.replacement.Value(); got != "[REDACTED-$R]" {
		t.Errorf("regenerated = %q", got)
	}

	// Typing a pattern into an empty replacement fills it without touching input.
	m2 := newFiltersModel(NewClient("http://example.test"))
	m2.startEditor(-1)
	m2.setFocus(fFocusPattern)
	m2.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a@b.co")})
	if got := m2.replacement.Value(); got != "redacted$R@gmail.com" {
		t.Errorf("typed prefill = %q", got)
	}
}

func TestFilterEditorKeepsManualReplacement(t *testing.T) {
	m := newFiltersModel(NewClient("http://example.test"))
	m.startEditor(-1)
	m.replacement.SetValue("***")
	m.setFocus(fFocusPattern)
	m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a@b.co")})
	if got := m.replacement.Value(); got != "***" {
		t.Errorf("manual replacement overwritten: %q", got)
	}
}
