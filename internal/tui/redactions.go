package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
)

// redactionModel lists every alias recorded since the server started. The list
// is refetched whenever a traffic entry arrives while the tab is visible, so it
// stays live without a second event stream.
type redactionModel struct {
	client *Client
	table  table.Model
	items  []RedactionInstance
	loaded bool
}

func newRedactionsModel(client *Client) redactionModel {
	t := table.New(
		table.WithColumns([]table.Column{
			{Title: "Alias", Width: 34},
			{Title: "Original", Width: 36},
			{Title: "Rule", Width: 14},
			{Title: "Hits", Width: 5},
		}),
		table.WithFocused(true),
	)
	return redactionModel{client: client, table: t}
}

func loadRedactionsCmd(c *Client) tea.Cmd {
	return func() tea.Msg {
		items, err := c.ListRedactions()
		return redactionsMsg{items: items, err: err}
	}
}

func (m *redactionModel) handleList(msg redactionsMsg) tea.Cmd {
	if msg.err != nil {
		return statusCmd("redactions: "+msg.err.Error(), true)
	}
	m.items = msg.items
	m.loaded = true
	m.rebuild()
	return nil
}

// rebuild renders newest-first so the latest replacement is on top.
func (m *redactionModel) rebuild() {
	rows := make([]table.Row, 0, len(m.items))
	for i := len(m.items) - 1; i >= 0; i-- {
		it := m.items[i]
		rule := it.Rule
		if it.Conflict {
			rule += " !"
		}
		rows = append(rows, table.Row{it.Alias, it.Original, rule, fmt.Sprintf("%d", it.Count)})
	}
	m.table.SetRows(rows)
	selectFirstRow(&m.table, len(rows))
}

func (m *redactionModel) update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "r" {
		return loadRedactionsCmd(m.client)
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return cmd
}

func (m *redactionModel) resize(w, h int) {
	m.table.SetWidth(w)
	m.table.SetHeight(max(1, h-2))
}

func (m *redactionModel) view() string {
	if !m.loaded {
		return styleHelp.Render(" loading redactions…")
	}
	var b strings.Builder
	b.WriteString(styleTitle.Render(" redactions") + "  ")
	b.WriteString(styleHelp.Render(fmt.Sprintf("%d alias(es), process memory only", len(m.items))) + "\n\n")
	if len(m.items) == 0 {
		b.WriteString(styleHelp.Render(" nothing redacted yet — add rules under redactions[] in the config") + "\n\n")
	} else {
		b.WriteString(m.table.View() + "\n\n")
	}
	b.WriteString(styleHelp.Render(" ↑/↓ move   r refresh   tab switch   q quit"))
	return b.String()
}
