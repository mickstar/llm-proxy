package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Settings field focus indices.
const (
	sFocusProxy = iota
	sFocusControl
	sFocusPolicy
	sFocusBaseURL
	sFocusAPIKey
	sFocusCapacity
	sFocusCount
)

// Model policy values as the control API spells them. The TUI mirrors the API
// strings instead of importing the server's config package.
const (
	policyPassthrough = "passthrough"
	policyAllowlist   = "allowlist"
)

type settingsModel struct {
	client *Client

	editing bool
	loaded  bool
	cfg     Config
	policy  string

	proxy   textinput.Model
	control textinput.Model
	baseURL textinput.Model
	apiKey  textinput.Model
	cap     textinput.Model
	focus   int
	errText string
}

func newSettingsModel(client *Client) settingsModel {
	m := settingsModel{
		client:  client,
		proxy:   textinput.New(),
		control: textinput.New(),
		baseURL: textinput.New(),
		apiKey:  textinput.New(),
		cap:     textinput.New(),
	}
	m.proxy.Placeholder = "127.0.0.1:8787"
	m.control.Placeholder = "127.0.0.1:8788"
	m.baseURL.Placeholder = "https://openrouter.ai/api/v1"
	m.apiKey.Placeholder = "sk-or-..."
	m.cap.Placeholder = "500"
	return m
}

func loadConfigCmd(c *Client) tea.Cmd {
	return func() tea.Msg {
		cfg, err := c.GetConfig()
		return configMsg{cfg: cfg, err: err}
	}
}

func (m *settingsModel) handleConfig(msg configMsg) tea.Cmd {
	if msg.err != nil {
		return statusCmd("settings: "+msg.err.Error(), true)
	}
	m.cfg = msg.cfg
	m.loaded = true
	if !m.editing {
		m.populate()
	}
	return nil
}

func (m *settingsModel) populate() {
	m.proxy.SetValue(m.cfg.Server.ProxyAddr)
	m.control.SetValue(m.cfg.Server.ControlAddr)
	m.policy = m.cfg.Server.ModelPolicy
	m.baseURL.SetValue(m.cfg.OpenRouter.BaseURL)
	m.apiKey.SetValue(m.cfg.OpenRouter.APIKey)
	m.cap.SetValue(strconv.Itoa(m.cfg.Server.TrafficLogCapacity))
}

func (m *settingsModel) handleSaved(msg settingsSavedMsg) tea.Cmd {
	if msg.err != nil {
		return statusCmd("save settings: "+msg.err.Error(), true)
	}
	m.cfg = msg.cfg
	m.editing = false
	m.populate()
	return statusCmd("settings saved", false)
}

func (m *settingsModel) capturesInput() bool { return m.editing }

func (m *settingsModel) update(msg tea.Msg) tea.Cmd {
	k, isKey := msg.(tea.KeyMsg)
	if !isKey {
		if m.editing {
			return m.updateWidgets(msg)
		}
		return nil
	}
	if !m.editing {
		switch k.String() {
		case "e", "enter":
			m.editing = true
			m.errText = ""
			m.populate()
			m.setFocus(sFocusProxy)
			return nil
		case "r":
			return loadConfigCmd(m.client)
		}
		return nil
	}
	switch k.String() {
	case "esc":
		m.editing = false
		m.populate()
		return nil
	case "ctrl+s":
		return m.save()
	case "tab":
		m.setFocus((m.focus + 1) % sFocusCount)
		return nil
	case "shift+tab":
		m.setFocus((m.focus + sFocusCount - 1) % sFocusCount)
		return nil
	}
	if m.focus == sFocusPolicy {
		switch k.String() {
		case " ", "enter", "left", "right":
			m.togglePolicy()
			return nil
		}
	}
	return m.updateWidgets(msg)
}

// togglePolicy switches between the two model policies.
func (m *settingsModel) togglePolicy() {
	if m.policy == policyAllowlist {
		m.policy = policyPassthrough
		return
	}
	m.policy = policyAllowlist
}

func (m *settingsModel) updateWidgets(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch m.focus {
	case sFocusProxy:
		m.proxy, cmd = m.proxy.Update(msg)
	case sFocusControl:
		m.control, cmd = m.control.Update(msg)
	case sFocusBaseURL:
		m.baseURL, cmd = m.baseURL.Update(msg)
	case sFocusAPIKey:
		m.apiKey, cmd = m.apiKey.Update(msg)
	case sFocusCapacity:
		m.cap, cmd = m.cap.Update(msg)
	}
	return cmd
}

func (m *settingsModel) setFocus(idx int) {
	m.focus = idx
	m.proxy.Blur()
	m.control.Blur()
	m.baseURL.Blur()
	m.apiKey.Blur()
	m.cap.Blur()
	switch idx {
	case sFocusProxy:
		m.proxy.Focus()
	case sFocusControl:
		m.control.Focus()
	case sFocusBaseURL:
		m.baseURL.Focus()
	case sFocusAPIKey:
		m.apiKey.Focus()
	case sFocusCapacity:
		m.cap.Focus()
	}
}

func (m *settingsModel) save() tea.Cmd {
	capacity, err := strconv.Atoi(strings.TrimSpace(m.cap.Value()))
	if err != nil {
		m.errText = "traffic log capacity must be a number"
		return nil
	}
	m.errText = ""
	server := ServerConfig{
		ProxyAddr:          strings.TrimSpace(m.proxy.Value()),
		ControlAddr:        strings.TrimSpace(m.control.Value()),
		ModelPolicy:        m.policy,
		TrafficLogCapacity: capacity,
	}
	openrouter := OpenRouterConfig{
		BaseURL: strings.TrimSpace(m.baseURL.Value()),
		APIKey:  strings.TrimSpace(m.apiKey.Value()),
	}
	c := m.client
	return func() tea.Msg {
		cfg, err := c.PutSettings(server, openrouter)
		return settingsSavedMsg{cfg: cfg, err: err}
	}
}

func (m *settingsModel) resize(w, h int) {
	for _, in := range []*textinput.Model{&m.proxy, &m.control, &m.baseURL, &m.apiKey, &m.cap} {
		in.Width = max(20, w-20)
	}
}

func (m *settingsModel) view() string {
	if !m.loaded {
		return styleHelp.Render(" loading settings…")
	}
	if !m.editing {
		var b strings.Builder
		b.WriteString(styleTitle.Render(" settings") + "\n\n")
		b.WriteString(" Proxy address      " + m.cfg.Server.ProxyAddr + "\n")
		b.WriteString(" Control address    " + m.cfg.Server.ControlAddr + "\n")
		b.WriteString(" Model policy       " + m.cfg.Server.ModelPolicy + "\n")
		b.WriteString(" OpenRouter base    " + m.cfg.OpenRouter.BaseURL + "\n")
		b.WriteString(" OpenRouter key     " + maskKey(m.cfg.OpenRouter.APIKey) + "\n")
		b.WriteString(" Traffic capacity   " + strconv.Itoa(m.cfg.Server.TrafficLogCapacity) + "\n\n")
		b.WriteString(styleHelp.Render(" e edit   r reload   tab switch   q quit"))
		return b.String()
	}
	var b strings.Builder
	b.WriteString(styleTitle.Render(" edit settings") + "\n\n")
	b.WriteString(m.field("Proxy address   ", m.proxy.View(), sFocusProxy) + "\n")
	b.WriteString(m.field("Control address ", m.control.View(), sFocusControl) + "\n")
	b.WriteString(m.field("Model policy    ", m.policy, sFocusPolicy) + "\n")
	b.WriteString(m.field("Base URL        ", m.baseURL.View(), sFocusBaseURL) + "\n")
	b.WriteString(m.field("API key         ", m.apiKey.View(), sFocusAPIKey) + "\n")
	b.WriteString(m.field("Log capacity    ", m.cap.View(), sFocusCapacity) + "\n\n")
	if m.errText != "" {
		b.WriteString(styleError.Render(" "+m.errText) + "\n")
	}
	b.WriteString(styleHelp.Render(" tab next field   space toggles policy   ctrl+s save   esc cancel"))
	return b.String()
}

func (m *settingsModel) field(label, value string, idx int) string {
	style := lipgloss.NewStyle()
	if m.focus == idx {
		style = style.Foreground(lipgloss.Color("212"))
	}
	return style.Render(label+" ") + value
}

// maskKey hides all but the last four characters of a credential.
func maskKey(k string) string {
	if k == "" {
		return "(none)"
	}
	if len(k) <= 4 {
		return strings.Repeat("*", len(k))
	}
	return strings.Repeat("*", len(k)-4) + k[len(k)-4:]
}
