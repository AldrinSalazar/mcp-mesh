package tui

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/AldrinSalazar/mcp-mesh/internal/discovery"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
)

type form struct {
	add                bool
	key                bool // one masked field: the hub key
	choosing, connect  bool
	inputs             []textinput.Model
	focus              int
	lazy, nameEdited   bool
	err                string
	picking, searching bool
	hubs               []discovery.Hub
	choice             int
	discoveryError     string
}

func (m *model) openForm(add bool) tea.Cmd {
	f := &form{add: add, choosing: add}
	count := 1
	if add {
		count = 4
	}
	f.inputs = make([]textinput.Model, count)
	for i := range f.inputs {
		f.inputs[i] = textinput.New()
		f.inputs[i].SetStyles(textinput.DefaultStyles(m.dark))
		f.inputs[i].CharLimit = 4096
		f.inputs[i].SetWidth(max(1, m.width-4))
	}
	if add {
		f.inputs[0].Placeholder = "npx -y @modelcontextprotocol/server-filesystem ~/code"
		f.inputs[1].SetValue(nodectl.SuggestName(nil, ""))
		f.inputs[3].Placeholder = "all tools"
	} else {
		f.inputs[0].SetValue("ws://")
	}
	f.setDark(m.dark)
	m.form = f
	m.notice = ""
	if !add {
		f.picking = true
		return m.discover()
	}
	return nil
}

// openKeyForm asks for the node key. The field is masked: the key is a
// credential and the terminal may be shared or recorded.
func (m *model) openKeyForm() tea.Cmd {
	f := &form{key: true, inputs: make([]textinput.Model, 1)}
	f.inputs[0] = textinput.New()
	f.inputs[0].EchoMode = textinput.EchoPassword
	f.inputs[0].Placeholder = config.KeyPrefix + "…"
	f.inputs[0].CharLimit = 512
	f.inputs[0].SetWidth(max(1, m.width-4))
	f.setDark(m.dark)
	m.form = f
	m.notice = ""
	return m.focusForm(0)
}

func isURL(s string) bool { return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") }
func validateURL(s string, schemes ...string) error {
	u, err := url.Parse(s)
	if err == nil && u.Hostname() != "" {
		for _, scheme := range schemes {
			if u.Scheme == scheme {
				return nil
			}
		}
	}
	return errors.New("enter an absolute " + strings.Join(schemes, " or ") + " URL with a host")
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (f *form) entry() (config.Entry, error) {
	e := config.Entry{Name: strings.TrimSpace(f.inputs[1].Value()), Start: "eager"}
	if !namePattern.MatchString(e.Name) {
		return e, errors.New("Name must use letters, numbers, _ or -")
	}
	line := strings.TrimSpace(f.inputs[0].Value())
	if line == "" {
		if f.connect {
			return e, errors.New("URL is required")
		}
		return e, errors.New("Command is required")
	}
	if f.connect {
		if err := validateURL(line, "http", "https"); err != nil {
			return e, err
		}
		e.URL = line
	} else {
		words, err := nodectl.SplitCommand(line)
		if err != nil {
			return e, err
		}
		if len(words) == 0 || words[0] == "" {
			return e, errors.New("Command is required")
		}
		e.Command, e.Args = words[0], words[1:]
	}
	if f.lazy {
		e.Start = "lazy"
	}
	if tools := strings.TrimSpace(f.inputs[3].Value()); tools != "" {
		for _, tool := range strings.Split(tools, ",") {
			tool = strings.TrimSpace(tool)
			if tool == "" {
				return e, errors.New("Allowed tools cannot contain an empty name")
			}
			e.AllowTools = append(e.AllowTools, tool)
		}
	}
	return e, nil
}

func (m *model) saveForm() tea.Cmd {
	f, node := m.form, m.node
	f.err = ""
	if f.key {
		key := strings.TrimSpace(f.inputs[0].Value())
		if err := config.ValidateKey(key); err != nil {
			f.err = err.Error()
			return nil
		}
		return m.begin("Saving the key…", func(ctx context.Context) actionMsg {
			result, err := node.SetKey(ctx, key)
			// A key is applied at once (the node reconnects with it).
			label := "hub key saved · press s to start the node"
			if result.Reloaded {
				label = "hub key saved · node reloaded"
			}
			return actionMsg{label: label, failure: "save key", err: err, form: true}
		})
	}
	if !f.add {
		hub := strings.TrimSpace(f.inputs[0].Value())
		if err := validateURL(hub, "ws", "wss"); err != nil {
			f.err = err.Error()
			return nil
		}
		return m.begin("Creating config…", func(context.Context) actionMsg {
			return actionMsg{label: "config created · press a to add an MCP or e to edit", failure: "create config", err: node.InitConfig(hub, false), form: true}
		})
	}
	e, err := f.entry()
	if err != nil {
		f.err = err.Error()
		return nil
	}
	label := "Adding " + e.Name + "…"
	if f.connect {
		label = "Checking the server…"
	}
	return m.begin(label, func(ctx context.Context) actionMsg {
		notice := ""
		if e.URL != "" {
			transport, err := node.DetectTransport(ctx, e.URL, e.Headers)
			if err != nil {
				if ctx.Err() != nil {
					return actionMsg{failure: "add " + e.Name, err: ctx.Err(), form: true}
				}
				var reason string
				transport, reason = nodectl.TransportFallback(e.URL)
				notice = " · Couldn't reach " + e.URL + " (is the app running?); saved as " + reason + ". Set transport: in node.yaml to override."
			}
			e.Transport = string(transport)
		}
		pending, err := node.AddMCP(ctx, e)
		return actionMsg{label: editResult("added "+e.Name, pending) + notice, failure: "add " + e.Name, err: err, form: true, selectName: e.Name}
	})
}

func (m *model) updateForm(msg tea.Msg) tea.Cmd {
	f := m.form
	if f.picking {
		return m.updatePicker(msg)
	}
	if f.choosing {
		return m.updateKindPicker(msg)
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			m.cancelDiscovery()
			if f.add {
				f.choosing = true
				f.inputs[f.focus].Blur()
				f.err = ""
			} else {
				m.form = nil
			}
			return nil
		case "ctrl+s":
			return m.saveForm()
		case "enter":
			if !f.add || f.focus == 3 {
				return m.saveForm()
			}
			return m.focusForm((f.focus + 1) % len(f.inputs))
		case "tab":
			return m.focusForm((f.focus + 1) % len(f.inputs))
		case "shift+tab":
			return m.focusForm((f.focus + len(f.inputs) - 1) % len(f.inputs))
		case "space", "left", "right":
			if f.add && f.focus == 2 {
				f.lazy = !f.lazy
				return nil
			}
		}
	}
	if f.add && f.focus == 2 {
		return nil
	}
	before := f.inputs[f.focus].Value()
	var cmd tea.Cmd
	f.inputs[f.focus], cmd = f.inputs[f.focus].Update(msg)
	if f.add && f.focus == 1 && before != f.inputs[1].Value() {
		f.nameEdited = true
	}
	if f.add && f.focus == 0 && !f.nameEdited && before != f.inputs[0].Value() {
		line := strings.TrimSpace(f.inputs[0].Value())
		if isURL(line) {
			f.inputs[1].SetValue(nodectl.SuggestName(nil, line))
		} else if words, err := nodectl.SplitCommand(line); err == nil {
			f.inputs[1].SetValue(nodectl.SuggestName(words, ""))
		}
	}
	return cmd
}
func (m *model) focusForm(index int) tea.Cmd {
	f := m.form
	f.inputs[f.focus].Blur()
	f.focus = index
	if f.add && index == 2 {
		return nil
	}
	// Land at the end so typing appends and ctrl+u clears the whole value.
	f.inputs[index].CursorEnd()
	return f.inputs[index].Focus()
}
func (m *model) formLines() []string {
	f := m.form
	title := "Create config"
	labels := []string{"Hub URL"}
	if f.key {
		title, labels = "Set hub key", []string{"Node key"}
	}
	if f.add {
		title = "Add MCP"
		labels = []string{"Command", "Name", "Start", "Allowed tools (comma-separated; empty = all)"}
		if f.connect {
			title = "Connect to a running server"
			labels[0] = "URL"
		} else {
			title = "Launch a command"
		}
	}
	lines := []string{m.bold(title), m.dim("Share an MCP through your hub.")}
	if f.key {
		lines[1] = m.dim("Paste the key created on the hub for this node:")
		lines = append(lines, m.dim("mcp-mesh-hub key create node "+singleLine(m.snapshot.Config.Node)))
	} else if !f.add {
		lines[1] = m.dim("Enter your hub’s node endpoint URL.")
	}
	for i, label := range labels {
		marker := "  "
		if i == f.focus {
			marker = m.accent("▌ ")
		}
		lines = append(lines, marker+m.dim(label))
		if f.add && i == 2 {
			start := "[eager]  lazy"
			if f.lazy {
				start = " eager  [lazy]"
			}
			lines = append(lines, "  "+start)
		} else {
			lines = append(lines, "  "+f.inputs[i].View())
		}
	}
	if f.err != "" {
		lines = append(lines, m.red(singleLine(f.err)))
	}
	return lines
}

func (f *form) setDark(dark bool) {
	for i := range f.inputs {
		styles := textinput.DefaultStyles(dark)
		styles.Focused.Prompt = lipgloss.NewStyle().Foreground(newTheme(dark).accent).Bold(true)
		f.inputs[i].SetStyles(styles)
	}
}

func (m *model) updateKindPicker(msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	f := m.form
	switch key.String() {
	case "esc":
		m.form = nil
	case "up", "k":
		f.choice = 0
	case "down", "j":
		f.choice = 1
	case "enter":
		f.choosing = false
		f.connect = f.choice == 1
		f.lazy, f.nameEdited, f.focus, f.err = f.connect, false, 0, ""
		for i := range f.inputs {
			f.inputs[i].SetValue("")
		}
		f.inputs[1].SetValue(nodectl.SuggestName(nil, ""))
		if f.connect {
			f.inputs[0].SetValue("http://127.0.0.1:")
			f.inputs[0].Placeholder = "http://127.0.0.1:9876/mcp"
		} else {
			f.inputs[0].Placeholder = "npx -y @modelcontextprotocol/server-filesystem ~/code"
		}
		return m.focusForm(0)
	}
	return nil
}

func (m *model) kindPickerLines() []string {
	width := max(1, min(80, m.width)-6)
	lines := []string{m.bold("Add MCP"), m.dim("Choose how this MCP runs"), ""}
	for i, item := range [][2]string{
		{"Launch a command", "The node starts it: npx, uvx, a binary…"},
		{"Connect to a running server", "An app on this machine already serves MCP at a URL (Blender, Fusion 360, a dev server…). The node connects to it and shares it with the hub, so other machines on your network can use it. The app's port stays private."},
	} {
		marker := "  "
		if m.form.choice == i {
			marker = m.accent("▌ ")
		}
		lines = append(lines, marker+item[0])
		for _, line := range m.dimWrap(item[1], width) {
			lines = append(lines, "  "+line)
		}
		lines = append(lines, "")
	}
	return lines
}
