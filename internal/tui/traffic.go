package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mickstar/llm-proxy/internal/traffic"
)

// maxTrafficRows bounds the rows kept client-side; the server ring buffer is
// the real retention limit.
const maxTrafficRows = 500

type trafficModel struct {
	client  *Client
	table   table.Model
	entries []traffic.Entry

	detail bool
	entry  traffic.Entry
	vp     viewport.Model
}

func newTrafficModel(client *Client) trafficModel {
	t := table.New(
		table.WithColumns([]table.Column{
			{Title: "Time", Width: 9},
			{Title: "Model", Width: 28},
			{Title: "Endpoint", Width: 24},
			{Title: "Status", Width: 7},
			{Title: "Dur", Width: 8},
			{Title: "Filter", Width: 20},
		}),
		table.WithFocused(true),
	)
	return trafficModel{
		client: client,
		table:  t,
		vp:     viewport.New(80, 10),
	}
}

func loadTrafficCmd(c *Client) tea.Cmd {
	return func() tea.Msg {
		es, err := c.ListTraffic(200)
		return entriesMsg{entries: es, err: err}
	}
}

// handleEntries applies a backfill response.
func (m *trafficModel) handleEntries(msg entriesMsg) tea.Cmd {
	if msg.err != nil {
		return statusCmd("traffic: "+msg.err.Error(), true)
	}
	entries := msg.entries
	if entries == nil {
		entries = []traffic.Entry{}
	}
	if len(entries) > maxTrafficRows {
		entries = entries[:maxTrafficRows]
	}
	m.entries = entries
	m.rebuild()
	return nil
}

// addEntry prepends a live entry (from SSE), keeping the selection stable when
// the user is not parked on the newest row.
func (m *trafficModel) addEntry(e traffic.Entry) {
	m.entries = append([]traffic.Entry{e}, m.entries...)
	if len(m.entries) > maxTrafficRows {
		m.entries = m.entries[:maxTrafficRows]
	}
	cursor := m.table.Cursor()
	m.rebuild()
	if cursor > 0 && cursor < len(m.entries) {
		m.table.SetCursor(cursor + 1)
	}
}

func (m *trafficModel) rebuild() {
	rows := make([]table.Row, 0, len(m.entries))
	for _, e := range m.entries {
		rows = append(rows, entryRow(e))
	}
	m.table.SetRows(rows)
	selectFirstRow(&m.table, len(rows))
}

func entryRow(e traffic.Entry) table.Row {
	return table.Row{
		e.Time.Format("15:04:05"),
		e.Model,
		e.Path,
		fmt.Sprintf("%d", e.Status),
		fmt.Sprintf("%dms", e.DurationMS),
		filterOutcome(e),
	}
}

func filterOutcome(e traffic.Entry) string {
	switch {
	case e.Blocked:
		return "blocked"
	case e.NotFiltered:
		return "not filtered"
	case e.FilterSummary != "":
		return e.FilterSummary
	default:
		return "-"
	}
}

// handleDetail opens the body inspector for a fetched entry.
func (m *trafficModel) handleDetail(msg entryDetailMsg) tea.Cmd {
	if msg.err != nil {
		return statusCmd("entry: "+msg.err.Error(), true)
	}
	m.entry = msg.e
	m.detail = true
	m.vp.SetContent(entryDetail(msg.e))
	m.vp.GotoTop()
	return nil
}

// update handles input for the traffic tab. A streamed traffic.Entry is also a
// message: it is prepended to the list.
func (m *trafficModel) update(msg tea.Msg) tea.Cmd {
	if e, ok := msg.(traffic.Entry); ok {
		m.addEntry(e)
		return nil
	}
	if k, ok := msg.(tea.KeyMsg); ok {
		switch {
		case m.detail && (k.String() == "esc" || k.String() == "backspace"):
			m.detail = false
			return nil
		case m.detail:
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return cmd
		case k.String() == "enter":
			return m.openSelected()
		case k.String() == "r":
			return loadTrafficCmd(m.client)
		}
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return cmd
}

func (m *trafficModel) openSelected() tea.Cmd {
	i := m.table.Cursor()
	if i < 0 || i >= len(m.entries) {
		return nil
	}
	id := m.entries[i].ID
	c := m.client
	return func() tea.Msg {
		e, err := c.GetTraffic(id)
		return entryDetailMsg{e: e, err: err}
	}
}

func (m *trafficModel) resize(w, h int) {
	m.table.SetWidth(w)
	tableHeight := h - 1
	if tableHeight < 1 {
		tableHeight = 1
	}
	m.table.SetHeight(tableHeight)
	dh := h - 3
	if dh < 3 {
		dh = 3
	}
	m.vp.Width = w - 2
	m.vp.Height = dh
}

func (m *trafficModel) view() string {
	if m.detail {
		header := styleTitle.Render(fmt.Sprintf(" entry #%d  %s %s  -> %d  (%dms)",
			m.entry.ID, m.entry.Method, m.entry.Path, m.entry.Status, m.entry.DurationMS))
		help := styleHelp.Render(" esc/q close   ↑/↓ pgup/pgdn scroll")
		return header + "\n" + m.vp.View() + "\n" + help
	}
	help := styleHelp.Render(" ↑/↓ move   enter inspect   r refresh   tab switch   q quit")
	return m.table.View() + "\n" + help
}

func entryDetail(e traffic.Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "model:      %s\n", e.Model)
	fmt.Fprintf(&b, "filter:     %s\n", filterOutcome(e))
	if e.FilterSummary != "" && e.Blocked {
		fmt.Fprintf(&b, "summary:    %s\n", e.FilterSummary)
	}
	fmt.Fprintf(&b, "request:    %d bytes\n", e.RequestSize)
	fmt.Fprintf(&b, "response:   %d bytes\n", e.ResponseSize)
	if e.TotalTokens > 0 {
		fmt.Fprintf(&b, "tokens:     %d (prompt %d, completion %d)\n", e.TotalTokens, e.PromptTokens, e.CompletionTokens)
		if e.CachedPromptTokens > 0 {
			fmt.Fprintf(&b, "cached:     %d prompt tokens\n", e.CachedPromptTokens)
		}
	}
	if e.CostUSD > 0 {
		fmt.Fprintf(&b, "cost:       $%.6f\n", e.CostUSD)
	}

	b.WriteString("\n")
	b.WriteString(styleTitle.Render("── request body (filtered)"))
	b.WriteString("\n")
	if e.RequestBody == "" {
		b.WriteString(styleHelp.Render("(none captured)"))
	} else {
		b.WriteString(prettyBody(e.RequestBody))
	}
	if e.RequestTruncated {
		b.WriteString("\n" + styleHelp.Render("[truncated]"))
	}

	b.WriteString("\n\n")
	b.WriteString(styleTitle.Render("── response body"))
	b.WriteString("\n")
	if e.ResponseBody == "" {
		b.WriteString(styleHelp.Render("(none captured)"))
	} else {
		b.WriteString(prettyBody(e.ResponseBody))
	}
	if e.ResponseTruncated {
		b.WriteString("\n" + styleHelp.Render("[truncated]"))
	}
	return b.String()
}

// prettyBody renders a captured body for display: valid JSON is re-indented,
// SSE streams have each `data:` payload indented, and anything else (plain
// text, multipart, undecodable bytes) is shown unchanged. Re-indentation uses
// json.Indent, so field order and number literals stay byte-exact.
func prettyBody(s string) string {
	if s == "" {
		return s
	}
	if out, ok := prettyJSON(s); ok {
		return out
	}
	if out, ok := prettySSE(s); ok {
		return out
	}
	return s
}

func prettyJSON(s string) (string, bool) {
	src := []byte(s)
	if !json.Valid(src) {
		return "", false
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, src, "", "  "); err != nil {
		return "", false
	}
	return strings.TrimRight(buf.String(), "\n"), true
}

// prettySSE indents the JSON payload of each `data:` line of a server-sent
// event stream, leaving event/comment lines and non-JSON payloads untouched.
func prettySSE(s string) (string, bool) {
	lines := strings.Split(s, "\n")
	formatted := false
	for i, line := range lines {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" || payload == "[DONE]" {
			continue
		}
		out, ok := prettyJSON(payload)
		if !ok {
			continue
		}
		lines[i] = "data: " + out
		formatted = true
	}
	if !formatted {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}
