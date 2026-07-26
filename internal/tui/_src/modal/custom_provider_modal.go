package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// customProviderSavedMsg is delivered after a successful Upsert (+ optional key).
type customProviderSavedMsg struct {
	name        string
	err         error
	selectAfter bool
	returnTo    modal // settings list, if any; nil closes
}

// customProviderFormModal collects name / base URL / wire API / key / models
// for a new or edited custom provider. Esc abandons without writes.
type customProviderFormModal struct {
	providers   host.Providers
	auth        host.Auth
	ops         chan<- protocol.Op
	th          theme.Theme
	editing     bool // true when name is locked (edit path)
	selectAfter bool
	returnTo    modal // settings list, if opened from /settings

	step      int // 0 name, 1 url, 2 api, 3 key, 4 models
	name      textinput.Model
	url       textinput.Model
	key       textinput.Model
	models    textinput.Model
	apiCursor int
	err       string
}

var wireAPIChoices = []string{"openai", "anthropic"}

func newCustomProviderFormModal(
	services host.Services,
	ops chan<- protocol.Op,
	th theme.Theme,
	existing *host.CustomProvider,
	selectAfter bool,
	returnTo modal,
) *customProviderFormModal {
	m := &customProviderFormModal{
		providers:   services.Providers,
		auth:        services.Auth,
		ops:         ops,
		th:          th,
		selectAfter: selectAfter,
		returnTo:    returnTo,
		name:        newTextInput(th, "kimi"),
		url:         newTextInput(th, "https://api.example.com/v1"),
		key:         newTextInput(th, "optional api key"),
		models:      newTextInput(th, "model-a, model-b"),
	}
	m.key.EchoMode = textinput.EchoPassword
	if existing != nil {
		m.editing = true
		m.name.SetValue(existing.Name)
		m.url.SetValue(existing.BaseURL)
		m.models.SetValue(strings.Join(existing.Models, ", "))
		for i, api := range wireAPIChoices {
			if api == existing.API {
				m.apiCursor = i
			}
		}
		m.step = 1 // skip name when editing
		m.url.Focus()
	} else {
		m.name.Focus()
	}
	return m
}

func (m *customProviderFormModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	if isEscape(msg) {
		return m.returnTo, nil
	}
	switch msg.String() {
	case "enter":
		return m.advance()
	case "shift+tab":
		if m.step > 0 && !(m.editing && m.step == 1) {
			m.step--
			m.focusStep()
			m.err = ""
		}
		return m, nil
	case "up", "k":
		if m.step == 2 {
			m.apiCursor = (m.apiCursor + len(wireAPIChoices) - 1) % len(wireAPIChoices)
			return m, nil
		}
	case "down", "j":
		if m.step == 2 {
			m.apiCursor = (m.apiCursor + 1) % len(wireAPIChoices)
			return m, nil
		}
	}
	var cmd tea.Cmd
	switch m.step {
	case 0:
		m.name, cmd = m.name.Update(msg)
	case 1:
		m.url, cmd = m.url.Update(msg)
	case 3:
		m.key, cmd = m.key.Update(msg)
	case 4:
		m.models, cmd = m.models.Update(msg)
	}
	return m, cmd
}

func (m *customProviderFormModal) advance() (modal, tea.Cmd) {
	m.err = ""
	switch m.step {
	case 0:
		name := strings.ToLower(strings.TrimSpace(m.name.Value()))
		if name == "" {
			m.err = "name is required"
			return m, nil
		}
		m.name.SetValue(name)
		m.step = 1
		m.focusStep()
		return m, nil
	case 1:
		if strings.TrimSpace(m.url.Value()) == "" {
			m.err = "base URL is required"
			return m, nil
		}
		m.step = 2
		m.focusStep()
		return m, nil
	case 2:
		m.step = 3
		m.focusStep()
		return m, nil
	case 3:
		m.step = 4
		m.focusStep()
		return m, nil
	case 4:
		return m.save()
	}
	return m, nil
}

func (m *customProviderFormModal) focusStep() {
	m.name.Blur()
	m.url.Blur()
	m.key.Blur()
	m.models.Blur()
	switch m.step {
	case 0:
		m.name.Focus()
	case 1:
		m.url.Focus()
	case 3:
		m.key.Focus()
	case 4:
		m.models.Focus()
	}
}

func (m *customProviderFormModal) save() (modal, tea.Cmd) {
	if m.providers == nil {
		m.err = "custom providers are unavailable"
		return m, nil
	}
	p := host.CustomProvider{
		Name:    strings.TrimSpace(m.name.Value()),
		BaseURL: strings.TrimSpace(m.url.Value()),
		API:     wireAPIChoices[m.apiCursor],
		Models:  splitModelIDs(m.models.Value()),
	}
	key := strings.TrimSpace(m.key.Value())
	// Env-only key refs ({env:NAME}, $NAME) go into apiKeyEnv — never the auth store.
	if envName, ok := parseSoleEnvRef(key); ok {
		p.APIKeyEnv = envName
		key = ""
	}
	providers, authsvc, selectAfter, ops, name := m.providers, m.auth, m.selectAfter, m.ops, p.Name
	returnTo := m.returnTo
	// Stay on the form until the cmd reports success so validation errors
	// remain editable; the app closes/returns on customProviderSavedMsg.
	return m, func() tea.Msg {
		if err := providers.Upsert(p); err != nil {
			return customProviderSavedMsg{name: name, err: err}
		}
		if key != "" && authsvc != nil {
			if err := authsvc.SetAPIKey(name, key); err != nil {
				return customProviderSavedMsg{name: name, err: err}
			}
		}
		if selectAfter && ops != nil {
			ops <- protocol.SelectModel{Provider: name}
		}
		return customProviderSavedMsg{name: name, selectAfter: selectAfter, returnTo: returnTo}
	}
}

// parseSoleEnvRef detects a key field that is only an env reference.
func parseSoleEnvRef(s string) (name string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if strings.HasPrefix(s, "{env:") && strings.HasSuffix(s, "}") {
		inner := s[len("{env:") : len(s)-1]
		if inner != "" && !strings.ContainsAny(inner, " \t\n{}=") {
			return inner, true
		}
	}
	if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
		inner := s[2 : len(s)-1]
		if inner != "" && !strings.ContainsAny(inner, " \t\n{}=") {
			return inner, true
		}
	}
	if strings.HasPrefix(s, "$") && !strings.HasPrefix(s, "${") {
		inner := s[1:]
		if inner != "" && !strings.ContainsAny(inner, " \t\n{}=$/") {
			return inner, true
		}
	}
	return "", false
}

func splitModelIDs(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	})
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (m *customProviderFormModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))
	title := "Add custom provider"
	if m.editing {
		title = "Edit " + m.name.Value()
	}
	steps := []string{"name", "base URL", "wire API", "API key", "models"}
	var body strings.Builder
	body.WriteString(st.Muted.Render("step " + steps[m.step] + " (" + strconv.Itoa(m.step+1) + "/" + strconv.Itoa(len(steps)) + ")"))
	body.WriteString("\n")
	switch m.step {
	case 0:
		sizeInput(&m.name, inner)
		body.WriteString(st.Text.Render("Provider name (slug)"))
		body.WriteString("\n")
		body.WriteString(m.name.View())
	case 1:
		sizeInput(&m.url, inner)
		body.WriteString(st.Text.Render("Base URL (or {env:VAR} / $VAR)"))
		body.WriteString("\n")
		body.WriteString(m.url.View())
		body.WriteString("\n")
		body.WriteString(st.Muted.Render(detailJoin(th, "openai: "+th.Icons.Ellipsis+"/v1", "anthropic: origin only")))
	case 2:
		items := make([]ui.ListItem, len(wireAPIChoices))
		for i, api := range wireAPIChoices {
			detail := "chat-completions"
			if api == "anthropic" {
				detail = "messages"
			}
			items[i] = ui.ListItem{Label: api, Detail: detail}
		}
		body.WriteString(st.Text.Render("Wire API (protocol, not brand)"))
		body.WriteString("\n")
		body.WriteString(ui.List(th, ui.ListOpts{
			Items: items, Cursor: m.apiCursor, Width: inner, Visible: len(items),
		}))
	case 3:
		sizeInput(&m.key, inner)
		body.WriteString(st.Text.Render("API key (optional — or {env:VAR} / $VAR)"))
		body.WriteString("\n")
		body.WriteString(m.key.View())
	case 4:
		sizeInput(&m.models, inner)
		body.WriteString(st.Text.Render("Model ids (optional, comma-separated)"))
		body.WriteString("\n")
		body.WriteString(m.models.View())
	}
	if m.err != "" {
		body.WriteString("\n")
		body.WriteString(st.Error.Render(m.err))
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: title,
		Hint:  dotJoin(th, "enter next/save", "shift+tab back", "esc cancel"),
		Width: width,
	}, body.String())
}

func sizeInput(in *textinput.Model, inner int) {
	cursorWidth := max(1, ansi.StringWidth(in.Cursor.View()))
	in.Width = max(1, inner-ansi.StringWidth(in.Prompt)-cursorWidth)
	in.SetValue(in.Value())
}
