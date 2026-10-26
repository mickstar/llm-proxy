package tui

import (
	"errors"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Model editor modes.
const (
	modelsList = iota
	modelsAdd
	modelsEdit
)

// errInvalidContext reports a non-numeric context length in the model editor.
var errInvalidContext = errors.New("context length must be a number")

// Model editor field focus indices.
const (
	mFocusName = iota
	mFocusContext
	mFocusPrompt
	mFocusCache
	mFocusCompletion
	mFocusParams
	mFocusCount
)

type modelsModel struct {
	client *Client
	table  table.Model
	items  []Model

	mode      int
	addID     textinput.Model
	editID    string
	name      textinput.Model
	context   textinput.Model
	prompt    textinput.Model
	cache     textinput.Model
	complet   textinput.Model
	params    textinput.Model
	enabled   bool
	focus     int
	formError string
}

func newModelsModel(client *Client) modelsModel {
	t := table.New(
		table.WithColumns([]table.Column{
			{Title: "ID", Width: 30},
			{Title: "Context", Width: 10},
			{Title: "Prompt", Width: 12},
			{Title: "Cache", Width: 12},
			{Title: "Completion", Width: 12},
			{Title: "Enabled", Width: 8},
		}),
		table.WithFocused(true),
	)
	m := modelsModel{
		client: client,
		table:  t,
		addID:  textinput.New(),
	}
	m.addID.Placeholder = "author/slug[:variant]"
	m.name = textinput.New()
	m.name.Placeholder = "display name"
	m.context = textinput.New()
	m.context.Placeholder = "context length"
	m.prompt = textinput.New()
	m.prompt.Placeholder = "prompt price"
	m.cache = textinput.New()
	m.cache.Placeholder = "cached prompt price"
	m.complet = textinput.New()
	m.complet.Placeholder = "completion price"
	m.params = textinput.New()
	m.params.Placeholder = "comma-separated supported parameters"
	return m
}

func loadModelsCmd(c *Client) tea.Cmd {
	return func() tea.Msg {
		ms, err := c.ListModels()
		return modelsMsg{models: ms, err: err}
	}
}

func (m *modelsModel) handleList(msg modelsMsg) tea.Cmd {
	if msg.err != nil {
		return statusCmd("models: "+msg.err.Error(), true)
	}
	m.items = msg.models
	if m.items == nil {
		m.items = []Model{}
	}
	m.rebuild()
	return nil
}

func (m *modelsModel) rebuild() {
	rows := make([]table.Row, 0, len(m.items))
	for _, mo := range m.items {
		rows = append(rows, table.Row{
			mo.ID,
			strconv.Itoa(mo.ContextLength),
			mo.Pricing.Prompt,
			mo.Pricing.Cache,
			mo.Pricing.Completion,
			boolLabel(mo.Enabled),
		})
	}
	m.table.SetRows(rows)
	selectFirstRow(&m.table, len(rows))
}

func (m *modelsModel) handleSaved(msg modelSavedMsg) tea.Cmd {
	if msg.err != nil {
		return statusCmd("save model: "+msg.err.Error(), true)
	}
	m.mode = modelsList
	return tea.Batch(statusCmd("saved model "+msg.m.ID, false), loadModelsCmd(m.client))
}

func (m *modelsModel) handleFetched(msg modelFetchedMsg) tea.Cmd {
	if msg.err != nil {
		return statusCmd("fetch model: "+msg.err.Error(), true)
	}
	m.mode = modelsList
	return tea.Batch(statusCmd("fetched model "+msg.m.ID, false), loadModelsCmd(m.client))
}

func (m *modelsModel) handleDeleted(msg modelDeletedMsg) tea.Cmd {
	if msg.err != nil {
		return statusCmd("delete model: "+msg.err.Error(), true)
	}
	return tea.Batch(statusCmd("deleted model "+msg.id, false), loadModelsCmd(m.client))
}

func (m *modelsModel) capturesInput() bool { return m.mode != modelsList }

func (m *modelsModel) update(msg tea.Msg) tea.Cmd {
	k, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return m.updateWidgets(msg)
	}
	switch m.mode {
	case modelsList:
		switch k.String() {
		case "a":
			return m.startAdd()
		case "e", "enter":
			return m.startEdit()
		case "d":
			return m.deleteSelected()
		case " ":
			return m.toggleSelected()
		case "r":
			return loadModelsCmd(m.client)
		}
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		return cmd
	case modelsAdd:
		switch k.String() {
		case "esc":
			m.mode = modelsList
			return nil
		case "enter":
			return m.fetch()
		}
		var cmd tea.Cmd
		m.addID, cmd = m.addID.Update(msg)
		return cmd
	case modelsEdit:
		switch k.String() {
		case "esc":
			m.mode = modelsList
			return nil
		case "ctrl+s":
			return m.save()
		case "ctrl+e":
			m.enabled = !m.enabled
			return nil
		case "tab":
			m.setFocus((m.focus + 1) % mFocusCount)
			return nil
		case "shift+tab":
			m.setFocus((m.focus + mFocusCount - 1) % mFocusCount)
			return nil
		}
		return m.updateWidgets(msg)
	}
	return nil
}

func (m *modelsModel) updateWidgets(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch m.mode {
	case modelsAdd:
		m.addID, cmd = m.addID.Update(msg)
	case modelsEdit:
		switch m.focus {
		case mFocusName:
			m.name, cmd = m.name.Update(msg)
		case mFocusContext:
			m.context, cmd = m.context.Update(msg)
		case mFocusPrompt:
			m.prompt, cmd = m.prompt.Update(msg)
		case mFocusCache:
			m.cache, cmd = m.cache.Update(msg)
		case mFocusCompletion:
			m.complet, cmd = m.complet.Update(msg)
		case mFocusParams:
			m.params, cmd = m.params.Update(msg)
		}
	}
	return cmd
}

func (m *modelsModel) setFocus(idx int) {
	m.focus = idx
	m.name.Blur()
	m.context.Blur()
	m.prompt.Blur()
	m.cache.Blur()
	m.complet.Blur()
	m.params.Blur()
	switch idx {
	case mFocusName:
		m.name.Focus()
	case mFocusContext:
		m.context.Focus()
	case mFocusPrompt:
		m.prompt.Focus()
	case mFocusCache:
		m.cache.Focus()
	case mFocusCompletion:
		m.complet.Focus()
	case mFocusParams:
		m.params.Focus()
	}
}

func (m *modelsModel) startAdd() tea.Cmd {
	m.mode = modelsAdd
	m.formError = ""
	m.addID.SetValue("")
	m.addID.Focus()
	return nil
}

func (m *modelsModel) startEdit() tea.Cmd {
	i := m.table.Cursor()
	if i < 0 || i >= len(m.items) {
		return nil
	}
	mo := m.items[i]
	m.mode = modelsEdit
	m.formError = ""
	m.editID = mo.ID
	m.name.SetValue(mo.Name)
	m.context.SetValue(strconv.Itoa(mo.ContextLength))
	m.prompt.SetValue(mo.Pricing.Prompt)
	m.cache.SetValue(mo.Pricing.Cache)
	m.complet.SetValue(mo.Pricing.Completion)
	m.params.SetValue(strings.Join(mo.SupportedParameters, ", "))
	m.enabled = mo.Enabled
	m.setFocus(mFocusName)
	return nil
}

func (m *modelsModel) fetch() tea.Cmd {
	id := strings.TrimSpace(m.addID.Value())
	if id == "" {
		m.formError = "model id is required"
		return nil
	}
	c := m.client
	return func() tea.Msg {
		mo, err := c.FetchModel(id)
		return modelFetchedMsg{m: mo, err: err}
	}
}

func (m *modelsModel) save() tea.Cmd {
	mo, err := m.form()
	if err != nil {
		m.formError = err.Error()
		return nil
	}
	m.formError = ""
	c := m.client
	id := mo.ID
	return func() tea.Msg {
		out, err := c.UpdateModel(id, mo)
		return modelSavedMsg{m: out, err: err}
	}
}

// form builds a Model from the editor inputs.
func (m *modelsModel) form() (Model, error) {
	ctx, err := strconv.Atoi(strings.TrimSpace(m.context.Value()))
	if err != nil {
		return Model{}, errInvalidContext
	}
	return Model{
		ID:            m.editID,
		Name:          m.name.Value(),
		ContextLength: ctx,
		Pricing: Pricing{
			Prompt:     m.prompt.Value(),
			Cache:      m.cache.Value(),
			Completion: m.complet.Value(),
		},
		SupportedParameters: splitList(m.params.Value()),
		Enabled:             m.enabled,
	}, nil
}

// splitList parses a comma-separated list, dropping blanks.
func splitList(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (m *modelsModel) deleteSelected() tea.Cmd {
	i := m.table.Cursor()
	if i < 0 || i >= len(m.items) {
		return nil
	}
	id := m.items[i].ID
	c := m.client
	return func() tea.Msg {
		return modelDeletedMsg{id: id, err: c.DeleteModel(id)}
	}
}

func (m *modelsModel) toggleSelected() tea.Cmd {
	i := m.table.Cursor()
	if i < 0 || i >= len(m.items) {
		return nil
	}
	mo := m.items[i]
	mo.Enabled = !mo.Enabled
	c := m.client
	return func() tea.Msg {
		out, err := c.UpdateModel(mo.ID, mo)
		return modelSavedMsg{m: out, err: err}
	}
}

func (m *modelsModel) resize(w, h int) {
	m.table.SetWidth(w)
	tableHeight := h - 1
	if tableHeight < 1 {
		tableHeight = 1
	}
	m.table.SetHeight(tableHeight)
	m.addID.Width = max(20, w-8)
	for _, in := range []*textinput.Model{&m.name, &m.context, &m.prompt, &m.cache, &m.complet, &m.params} {
		in.Width = max(20, w-16)
	}
}

func (m *modelsModel) view() string {
	switch m.mode {
	case modelsAdd:
		var b strings.Builder
		b.WriteString(styleTitle.Render(" add model by id") + "\n\n")
		b.WriteString(" ID  " + m.addID.View() + "\n\n")
		if m.formError != "" {
			b.WriteString(styleError.Render(" "+m.formError) + "\n")
		}
		b.WriteString(styleHelp.Render(" enter fetch from OpenRouter   esc cancel"))
		return b.String()
	case modelsEdit:
		var b strings.Builder
		b.WriteString(styleTitle.Render(" edit model "+m.editID) + "\n\n")
		b.WriteString(m.field("Name      ", m.name.View(), mFocusName) + "\n")
		b.WriteString(m.field("Context   ", m.context.View(), mFocusContext) + "\n")
		b.WriteString(m.field("Prompt$   ", m.prompt.View(), mFocusPrompt) + "\n")
		b.WriteString(m.field("Cache$    ", m.cache.View(), mFocusCache) + "\n")
		b.WriteString(m.field("Complete$ ", m.complet.View(), mFocusCompletion) + "\n")
		b.WriteString(m.field("Params    ", m.params.View(), mFocusParams) + "\n")
		b.WriteString("Enabled    " + boolLabel(m.enabled) + "  (ctrl+e toggles)\n\n")
		if m.formError != "" {
			b.WriteString(styleError.Render(" "+m.formError) + "\n")
		}
		b.WriteString(styleHelp.Render(" tab next field   ctrl+s save   esc cancel"))
		return b.String()
	default:
		help := styleHelp.Render(" a add by id   e/enter edit   d delete   space toggle   r reload   tab switch   q quit")
		return m.table.View() + "\n" + help
	}
}

func (m *modelsModel) field(label, value string, idx int) string {
	style := lipgloss.NewStyle()
	if m.focus == idx {
		style = style.Foreground(lipgloss.Color("212"))
	}
	return style.Render(label+" ") + value
}
