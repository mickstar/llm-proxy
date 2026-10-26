package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Editor field focus indices.
const (
	fFocusName = iota
	fFocusPattern
	fFocusPatternFile
	fFocusReplacement
	fFocusSample
	fFocusCount
)

type filtersModel struct {
	client  *Client
	table   table.Model
	filters []Filter

	editing  bool
	original string // name being edited ("" for a new filter)

	name        textinput.Model
	pattern     textinput.Model
	patternFile textinput.Model
	replacement textinput.Model
	sample      textarea.Model
	action      string
	enabled     bool
	focus       int

	testSeq  int
	test     TestResult
	testErr  string
	testOnce bool
}

func newFiltersModel(client *Client) filtersModel {
	t := table.New(
		table.WithColumns([]table.Column{
			{Title: "Name", Width: 18},
			{Title: "Pattern / File", Width: 30},
			{Title: "Action", Width: 9},
			{Title: "Replacement", Width: 16},
			{Title: "Enabled", Width: 8},
		}),
		table.WithFocused(true),
	)
	m := filtersModel{
		client:  client,
		table:   t,
		name:    textinput.New(),
		pattern: textinput.New(),
		sample:  textarea.New(),
	}
	m.name.Placeholder = "filter name"
	m.pattern.Placeholder = "regexp pattern"
	m.patternFile = textinput.New()
	m.patternFile.Placeholder = "file or glob (e.g. ~/.ssh/*.pem)"
	m.replacement = textinput.New()
	m.replacement.Placeholder = "replacement (replace only)"
	m.sample.Placeholder = "sample text to test against"
	m.sample.SetHeight(3)
	return m
}

func loadFiltersCmd(c *Client) tea.Cmd {
	return func() tea.Msg {
		fs, err := c.ListFilters()
		return filtersMsg{filters: fs, err: err}
	}
}

func (m *filtersModel) handleList(msg filtersMsg) tea.Cmd {
	if msg.err != nil {
		return statusCmd("filters: "+msg.err.Error(), true)
	}
	m.filters = msg.filters
	if m.filters == nil {
		m.filters = []Filter{}
	}
	m.rebuild()
	return nil
}

func (m *filtersModel) rebuild() {
	rows := make([]table.Row, 0, len(m.filters))
	for _, f := range m.filters {
		rows = append(rows, table.Row{
			f.Name,
			matchSource(f),
			f.Action,
			f.Replacement,
			boolLabel(f.Enabled),
		})
	}
	m.table.SetRows(rows)
	selectFirstRow(&m.table, len(rows))
}

// matchSource labels a filter's pattern column: the file spec for a
// file-backed filter, otherwise the inline pattern.
func matchSource(f Filter) string {
	if f.PatternFile != "" {
		return "file: " + f.PatternFile
	}
	return f.Pattern
}

func boolLabel(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func (m *filtersModel) handleSaved(msg filterSavedMsg) tea.Cmd {
	if msg.err != nil {
		return statusCmd("save filter: "+msg.err.Error(), true)
	}
	m.editing = false
	return tea.Batch(statusCmd("saved filter "+msg.f.Name, false), loadFiltersCmd(m.client))
}

func (m *filtersModel) handleDeleted(msg filterDeletedMsg) tea.Cmd {
	if msg.err != nil {
		return statusCmd("delete filter: "+msg.err.Error(), true)
	}
	return tea.Batch(statusCmd("deleted filter "+msg.name, false), loadFiltersCmd(m.client))
}

func (m *filtersModel) handleTest(msg testResultMsg) tea.Cmd {
	m.testOnce = true
	if msg.err != nil {
		m.testErr = msg.err.Error()
		m.test = TestResult{}
		return nil
	}
	m.testErr = ""
	m.test = msg.tr
	return nil
}

func (m *filtersModel) handleTick(msg testTickMsg) tea.Cmd {
	if msg.seq != m.testSeq {
		return nil
	}
	return testFilterCmd(m.client, m.pattern.Value(), m.patternFile.Value(), m.replacement.Value(), m.sample.Value())
}

func testFilterCmd(c *Client, pattern, patternFile, replacement, sample string) tea.Cmd {
	return func() tea.Msg {
		tr, err := c.TestFilter(pattern, patternFile, replacement, sample)
		return testResultMsg{tr: tr, err: err}
	}
}

func (m *filtersModel) capturesInput() bool { return m.editing }

func (m *filtersModel) update(msg tea.Msg) tea.Cmd {
	k, isKey := msg.(tea.KeyMsg)
	if !isKey {
		if m.editing {
			return m.updateEditorWidgets(msg)
		}
		return nil
	}
	if !m.editing {
		switch k.String() {
		case "n":
			return m.startEditor(-1)
		case "e", "enter":
			return m.startEditor(m.table.Cursor())
		case "d":
			return m.deleteSelected()
		case " ":
			return m.toggleSelected()
		case "r":
			return loadFiltersCmd(m.client)
		}
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		return cmd
	}

	switch k.String() {
	case "esc":
		m.editing = false
		return nil
	case "ctrl+s":
		return m.save()
	case "ctrl+a":
		m.action = toggleAction(m.action)
		m.maybeSuggest()
		return m.scheduleTest()
	case "ctrl+g":
		if m.action == "replace" {
			m.replacement.SetValue(suggestReplacement(strings.TrimSpace(m.pattern.Value())))
		}
		return m.scheduleTest()
	case "ctrl+e":
		m.enabled = !m.enabled
		return nil
	case "tab":
		m.setFocus((m.focus + 1) % fFocusCount)
		return nil
	case "shift+tab":
		m.setFocus((m.focus + fFocusCount - 1) % fFocusCount)
		return nil
	}
	cmd := m.updateEditorWidgets(msg)
	m.maybeSuggest()
	return tea.Batch(cmd, m.scheduleTest())
}

// updateEditorWidgets forwards a message to the focused input.
func (m *filtersModel) updateEditorWidgets(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch m.focus {
	case fFocusName:
		m.name, cmd = m.name.Update(msg)
	case fFocusPattern:
		m.pattern, cmd = m.pattern.Update(msg)
	case fFocusPatternFile:
		m.patternFile, cmd = m.patternFile.Update(msg)
	case fFocusReplacement:
		m.replacement, cmd = m.replacement.Update(msg)
	case fFocusSample:
		m.sample, cmd = m.sample.Update(msg)
	}
	return cmd
}

// suggestReplacement proposes a reversible alias template for a pattern. Values
// that look like an address keep the shape (always @gmail.com); everything else
// becomes a bracketed placeholder. Both use $R, which the proxy expands per
// match and records so responses can be restored.
func suggestReplacement(pattern string) string {
	if strings.Contains(pattern, "@") {
		return "redacted$R@gmail.com"
	}
	return "[REDACTED-$R]"
}

// maybeSuggest fills an empty replacement for a replace filter once the pattern
// says enough to classify it. A non-empty replacement is never touched.
func (m *filtersModel) maybeSuggest() {
	if m.action != "replace" {
		return
	}
	if strings.TrimSpace(m.replacement.Value()) != "" {
		return
	}
	pattern := m.pattern.Value()
	if pattern == "" && m.patternFile.Value() == "" {
		return
	}
	m.replacement.SetValue(suggestReplacement(pattern))
}

func toggleAction(a string) string {
	if a == "block" {
		return "replace"
	}
	return "block"
}

func (m *filtersModel) startEditor(idx int) tea.Cmd {
	m.editing = true
	m.testOnce = false
	m.testErr = ""
	m.test = TestResult{}
	if idx >= 0 && idx < len(m.filters) {
		f := m.filters[idx]
		m.original = f.Name
		m.name.SetValue(f.Name)
		m.pattern.SetValue(f.Pattern)
		m.patternFile.SetValue(f.PatternFile)
		m.replacement.SetValue(f.Replacement)
		m.action = f.Action
		if m.action == "" {
			m.action = "replace"
		}
		m.enabled = f.Enabled
	} else {
		m.original = ""
		m.name.SetValue("")
		m.pattern.SetValue("")
		m.patternFile.SetValue("")
		m.replacement.SetValue("")
		m.action = "replace"
		m.enabled = true
	}
	m.setFocus(fFocusName)
	return m.scheduleTest()
}

func (m *filtersModel) setFocus(idx int) {
	m.focus = idx
	m.name.Blur()
	m.pattern.Blur()
	m.patternFile.Blur()
	m.replacement.Blur()
	m.sample.Blur()
	switch idx {
	case fFocusName:
		m.name.Focus()
	case fFocusPattern:
		m.pattern.Focus()
	case fFocusPatternFile:
		m.patternFile.Focus()
	case fFocusReplacement:
		m.replacement.Focus()
	case fFocusSample:
		m.sample.Focus()
	}
}

func (m *filtersModel) scheduleTest() tea.Cmd {
	m.testSeq++
	seq := m.testSeq
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return testTickMsg{seq: seq} })
}

func (m *filtersModel) form() Filter {
	return Filter{
		Name:        strings.TrimSpace(m.name.Value()),
		Pattern:     m.pattern.Value(),
		PatternFile: strings.TrimSpace(m.patternFile.Value()),
		Action:      m.action,
		Replacement: m.replacement.Value(),
		Enabled:     m.enabled,
	}
}

func (m *filtersModel) save() tea.Cmd {
	m.maybeSuggest()
	f := m.form()
	if f.Name == "" {
		return statusCmd("filter name is required", true)
	}
	if f.Pattern == "" && f.PatternFile == "" {
		return statusCmd("filter pattern or pattern file is required", true)
	}
	if f.Pattern != "" && f.PatternFile != "" {
		return statusCmd("filter pattern and pattern file are mutually exclusive", true)
	}
	c := m.client
	if m.editing && m.original != "" {
		orig := m.original
		return func() tea.Msg {
			out, err := c.UpdateFilter(orig, f)
			return filterSavedMsg{f: out, err: err}
		}
	}
	return func() tea.Msg {
		out, err := c.CreateFilter(f)
		return filterSavedMsg{f: out, err: err}
	}
}

func (m *filtersModel) deleteSelected() tea.Cmd {
	i := m.table.Cursor()
	if i < 0 || i >= len(m.filters) {
		return nil
	}
	name := m.filters[i].Name
	c := m.client
	return func() tea.Msg {
		return filterDeletedMsg{name: name, err: c.DeleteFilter(name)}
	}
}

func (m *filtersModel) toggleSelected() tea.Cmd {
	i := m.table.Cursor()
	if i < 0 || i >= len(m.filters) {
		return nil
	}
	f := m.filters[i]
	f.Enabled = !f.Enabled
	c := m.client
	return func() tea.Msg {
		out, err := c.UpdateFilter(f.Name, f)
		return filterSavedMsg{f: out, err: err}
	}
}

func (m *filtersModel) resize(w, h int) {
	m.table.SetWidth(w)
	tableHeight := h - 1
	if tableHeight < 1 {
		tableHeight = 1
	}
	m.table.SetHeight(tableHeight)
	m.sample.SetWidth(max(20, w-4))
}

func (m *filtersModel) view() string {
	if !m.editing {
		help := styleHelp.Render(" n new   e/enter edit   d delete   space toggle   r reload   tab switch   q quit")
		return m.table.View() + "\n" + help
	}
	var b strings.Builder
	b.WriteString(styleTitle.Render(" edit filter") + "\n\n")
	b.WriteString(m.field("Name       ", m.name.View(), fFocusName) + "\n")
	b.WriteString(m.field("Pattern    ", m.pattern.View(), fFocusPattern) + "\n")
	b.WriteString(m.field("PatternFile", m.patternFile.View(), fFocusPatternFile) + "\n")
	b.WriteString(m.plain("Action     ", m.action+"  (ctrl+a toggles)") + "\n")
	b.WriteString(m.field("Replacement", m.replacement.View(), fFocusReplacement) + "\n")
	b.WriteString(m.plain("Enabled    ", boolLabel(m.enabled)+"  (ctrl+e toggles)") + "\n\n")
	b.WriteString(styleTitle.Render(" sample") + "\n")
	b.WriteString(m.sample.View() + "\n")
	b.WriteString(m.testView() + "\n")
	b.WriteString(styleHelp.Render(" tab next field   ctrl+a action   ctrl+g suggest   ctrl+s save   esc cancel"))
	return b.String()
}

// field renders a labelled input, highlighting the focused row.
func (m *filtersModel) field(label, value string, idx int) string {
	style := lipgloss.NewStyle()
	if m.focus == idx {
		style = style.Foreground(lipgloss.Color("212"))
	}
	return style.Render(label+" ") + value
}

func (m *filtersModel) plain(label, value string) string {
	return label + " " + value
}

func (m *filtersModel) testView() string {
	if !m.testOnce {
		return styleHelp.Render(" test: (waiting for pattern and sample)")
	}
	if m.testErr != "" {
		return styleError.Render(" test error: " + m.testErr)
	}
	var b strings.Builder
	fmt.Fprintf(&b, " test: %d match(es)", m.test.Matches)
	if len(m.test.Matched) > 0 {
		shown := m.test.Matched
		if len(shown) > 5 {
			shown = shown[:5]
		}
		b.WriteString("  " + styleHelp.Render(strings.Join(shown, ", ")))
	}
	b.WriteString("\n")
	b.WriteString(styleTitle.Render(" result") + "\n")
	if m.test.Result == "" {
		b.WriteString(styleHelp.Render("(empty)"))
	} else {
		b.WriteString(m.test.Result)
	}
	return b.String()
}
