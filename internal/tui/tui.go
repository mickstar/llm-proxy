// Package tui implements the Bubble Tea terminal interface: a live traffic
// table, filter manager with a live test pane, model manager, and settings
// form. It is a pure HTTP client of the control API and never imports server
// internals.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mickstar/llm-proxy/internal/traffic"
)

// Run starts the terminal UI connected to the control API at controlURL
// (e.g. "http://127.0.0.1:8788") and blocks until the user quits.
func Run(controlURL string) error {
	client := NewClient(controlURL)
	p := tea.NewProgram(newRootModel(client), tea.WithAltScreen())

	ctx, cancel := context.WithCancel(context.Background())
	go streamTraffic(ctx, client, p)
	_, err := p.Run()
	cancel()
	return err
}

// streamTraffic forwards SSE entries into the program, reconnecting after a
// short delay if the stream drops. It exits when ctx is cancelled.
func streamTraffic(ctx context.Context, client *Client, p *tea.Program) {
	for {
		err := client.StreamTraffic(ctx, func(e traffic.Entry) {
			select {
			case <-ctx.Done():
			default:
				p.Send(e)
			}
		})
		if ctx.Err() != nil {
			return
		}
		msg := "traffic stream disconnected"
		if err != nil {
			msg += ": " + err.Error()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		select {
		case <-ctx.Done():
			return
		default:
			p.Send(statusMsg{text: msg, isErr: true})
		}
	}
}

// --- tabs ---

const (
	tabTraffic = iota
	tabRedactions
	tabFilters
	tabModels
	tabSettings
)

var tabNames = []string{"Traffic", "Redactions", "Filters", "Models", "Settings"}

// --- messages ---

// statusMsg updates the banner line. isErr selects the error styling.
type statusMsg struct {
	text  string
	isErr bool
}

type healthMsg struct {
	h   Health
	err error
}

type healthTickMsg struct{}

type resumeMsg struct {
	st  HaltStatus
	err error
}

type entriesMsg struct {
	entries []traffic.Entry
	err     error
}

type entryDetailMsg struct {
	e   traffic.Entry
	err error
}

type redactionsMsg struct {
	items []RedactionInstance
	err   error
}

type filtersMsg struct {
	filters []Filter
	err     error
}

type filterSavedMsg struct {
	f   Filter
	err error
}

type filterDeletedMsg struct {
	name string
	err  error
}

type testResultMsg struct {
	tr  TestResult
	err error
}

type testTickMsg struct{ seq int }

type modelsMsg struct {
	models []Model
	err    error
}

type modelSavedMsg struct {
	m   Model
	err error
}

type modelFetchedMsg struct {
	m   Model
	err error
}

type modelDeletedMsg struct {
	id  string
	err error
}

type configMsg struct {
	cfg Config
	err error
}

type settingsSavedMsg struct {
	cfg Config
	err error
}

// statusCmd returns a command that sets the banner.
func statusCmd(text string, isErr bool) tea.Cmd {
	return func() tea.Msg { return statusMsg{text: text, isErr: isErr} }
}

// selectFirstRow gives a populated table a valid cursor. bubbles leaves the
// cursor at -1 until the user presses a navigation key, which would make the
// highlighted row inert on a freshly populated list.
func selectFirstRow(t *table.Model, rows int) {
	if rows > 0 && t.Cursor() < 0 {
		t.SetCursor(0)
	}
}

// --- styles ---

var (
	styleTabActive   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")).Underline(true)
	styleTabInactive = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleStatus      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleError       = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleHelp        = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	styleTitle       = lipgloss.NewStyle().Bold(true)
)

// --- root model ---

type rootModel struct {
	client *Client

	tab    int
	width  int
	height int

	status    string
	statusErr bool
	configErr string
	halt      HaltStatus

	traffic    trafficModel
	redactions redactionModel
	filters    filtersModel
	models     modelsModel
	settings   settingsModel
}

func newRootModel(client *Client) rootModel {
	m := rootModel{
		client:     client,
		traffic:    newTrafficModel(client),
		redactions: newRedactionsModel(client),
		filters:    newFiltersModel(client),
		models:     newModelsModel(client),
		settings:   newSettingsModel(client),
		width:      80,
		height:     24,
	}
	m.resize()
	return m
}

func (m rootModel) Init() tea.Cmd {
	return tea.Batch(
		healthCmd(m.client),
		healthTickCmd(),
		loadTrafficCmd(m.client),
		loadRedactionsCmd(m.client),
		loadFiltersCmd(m.client),
		loadModelsCmd(m.client),
		loadConfigCmd(m.client),
	)
}

func (m rootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil
	case tea.KeyMsg:
		if cmd, handled := m.globalKey(msg); handled {
			return m, cmd
		}
	case healthMsg:
		m.applyHealth(msg)
		return m, nil
	case healthTickMsg:
		return m, tea.Batch(healthCmd(m.client), healthTickCmd())
	case statusMsg:
		m.status = msg.text
		m.statusErr = msg.isErr
		return m, nil
	case resumeMsg:
		if msg.err != nil {
			return m, statusCmd("resume: "+msg.err.Error(), true)
		}
		m.halt = msg.st
		return m, statusCmd("proxy resumed", false)
	}
	return m, m.route(msg)
}

// globalKey handles keys that apply across tabs. When the active view is in a
// form, only ctrl+c is global so typed characters reach the form.
func (m *rootModel) globalKey(k tea.KeyMsg) (tea.Cmd, bool) {
	if k.String() == "ctrl+c" {
		return tea.Quit, true
	}
	if m.capturesInput() {
		return nil, false
	}
	switch k.String() {
	case "q":
		return tea.Quit, true
	case "R":
		if m.halt.Halted {
			return resumeCmd(m.client), true
		}
		return nil, false
	case "tab":
		return m.switchTab((m.tab + 1) % len(tabNames)), true
	case "shift+tab":
		return m.switchTab((m.tab + len(tabNames) - 1) % len(tabNames)), true
	case "1", "2", "3", "4", "5":
		if n := int(k.String()[0] - '1'); n < len(tabNames) {
			return m.switchTab(n), true
		}
	}
	return nil, false
}

// switchTab changes the active tab and loads the view's data on entry.
func (m *rootModel) switchTab(n int) tea.Cmd {
	if n == m.tab {
		return nil
	}
	m.tab = n
	if n == tabRedactions {
		return loadRedactionsCmd(m.client)
	}
	return nil
}

// route delivers a message to the view that owns it, falling back to the
// active view for input events. Traffic entries always reach the traffic view
// so the list stays live even while another tab is focused.
func (m *rootModel) route(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case traffic.Entry:
		cmd := m.traffic.update(msg)
		if m.tab == tabRedactions {
			return tea.Batch(cmd, loadRedactionsCmd(m.client))
		}
		return cmd
	case redactionsMsg:
		return m.redactions.handleList(msg)
	case entriesMsg:
		return m.traffic.handleEntries(msg)
	case entryDetailMsg:
		return m.traffic.handleDetail(msg)
	case filtersMsg:
		return m.filters.handleList(msg)
	case filterSavedMsg:
		return m.filters.handleSaved(msg)
	case filterDeletedMsg:
		return m.filters.handleDeleted(msg)
	case testResultMsg:
		return m.filters.handleTest(msg)
	case testTickMsg:
		return m.filters.handleTick(msg)
	case modelsMsg:
		return m.models.handleList(msg)
	case modelSavedMsg:
		return m.models.handleSaved(msg)
	case modelFetchedMsg:
		return m.models.handleFetched(msg)
	case modelDeletedMsg:
		return m.models.handleDeleted(msg)
	case configMsg:
		return m.settings.handleConfig(msg)
	case settingsSavedMsg:
		return m.settings.handleSaved(msg)
	}
	return m.activeUpdate(msg)
}

func (m *rootModel) activeUpdate(msg tea.Msg) tea.Cmd {
	switch m.tab {
	case tabTraffic:
		return m.traffic.update(msg)
	case tabRedactions:
		return m.redactions.update(msg)
	case tabFilters:
		return m.filters.update(msg)
	case tabModels:
		return m.models.update(msg)
	case tabSettings:
		return m.settings.update(msg)
	}
	return nil
}

// capturesInput reports whether the active view is a form consuming keystrokes.
func (m rootModel) capturesInput() bool {
	switch m.tab {
	case tabFilters:
		return m.filters.capturesInput()
	case tabModels:
		return m.models.capturesInput()
	case tabSettings:
		return m.settings.capturesInput()
	}
	return false
}

func (m *rootModel) resize() {
	content := m.height - 2 // tabs + banner
	if content < 1 {
		content = 1
	}
	m.traffic.resize(m.width, content)
	m.redactions.resize(m.width, content)
	m.filters.resize(m.width, content)
	m.models.resize(m.width, content)
	m.settings.resize(m.width, content)
}

func (m rootModel) View() string {
	var b strings.Builder
	b.WriteString(m.tabsView())
	b.WriteString("\n")
	b.WriteString(m.bannerView())
	b.WriteString("\n")
	b.WriteString(m.activeView())
	return b.String()
}

func (m rootModel) activeView() string {
	switch m.tab {
	case tabTraffic:
		return m.traffic.view()
	case tabRedactions:
		return m.redactions.view()
	case tabFilters:
		return m.filters.view()
	case tabModels:
		return m.models.view()
	case tabSettings:
		return m.settings.view()
	}
	return ""
}

func (m rootModel) tabsView() string {
	parts := make([]string, 0, len(tabNames))
	for i, name := range tabNames {
		label := fmt.Sprintf("%d:%s", i+1, name)
		if i == m.tab {
			parts = append(parts, styleTabActive.Render(label))
		} else {
			parts = append(parts, styleTabInactive.Render(label))
		}
	}
	return " " + strings.Join(parts, "  ")
}

func (m rootModel) bannerView() string {
	if m.halt.Halted {
		msg := fmt.Sprintf("! HALTED after filter %q — %d request(s) refused — press R or run `llm-proxy resume`",
			m.halt.Filter, m.halt.Refused)
		return " " + styleError.Render(msg)
	}
	switch {
	case m.configErr != "":
		return " " + styleError.Render("! "+m.configErr)
	case m.status != "":
		if m.statusErr {
			return " " + styleError.Render("! "+m.status)
		}
		return " " + styleStatus.Render(m.status)
	default:
		if m.client == nil {
			return " "
		}
		return " " + styleHelp.Render("connected to "+m.client.base)
	}
}

func (m *rootModel) applyHealth(msg healthMsg) {
	if msg.err == nil {
		m.halt = msg.h.Halt
	}
	if msg.err != nil {
		m.configErr = "control API unreachable: " + msg.err.Error()
		return
	}
	if msg.h.ConfigError != "" {
		m.configErr = "config: " + msg.h.ConfigError
		return
	}
	m.configErr = ""
}

// --- shared commands ---

func healthCmd(c *Client) tea.Cmd {
	return func() tea.Msg {
		h, err := c.Health()
		return healthMsg{h: h, err: err}
	}
}

func resumeCmd(c *Client) tea.Cmd {
	return func() tea.Msg {
		st, err := c.Resume()
		return resumeMsg{st: st, err: err}
	}
}

func healthTickCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return healthTickMsg{} })
}
