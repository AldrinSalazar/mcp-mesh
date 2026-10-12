package tui

import (
	"context"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/AldrinSalazar/mcp-mesh/internal/discovery"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/charmbracelet/x/ansi"
)

const discoveryTimeout = 3 * time.Second

type discoveryMsg struct {
	id   int
	hubs []discovery.Hub
	err  error
}
type discoverySpinMsg struct{ id int }

func compatible(h discovery.Hub) bool {
	return h.Version >= proto.MinVersion && h.Version <= proto.Version
}

func (m *model) cancelDiscovery() {
	if m.discoveryCancel != nil {
		m.discoveryCancel()
		m.discoveryCancel = nil
	}
}

func (m *model) discover() tea.Cmd {
	m.cancelDiscovery()
	m.discoveryID++
	f := m.form
	f.searching, f.hubs, f.choice, f.discoveryError = true, nil, 0, ""
	ctx, cancel := context.WithTimeout(m.ctx, discoveryTimeout)
	m.discoveryCancel = cancel
	node, id := m.node, m.discoveryID
	return tea.Batch(func() tea.Msg {
		defer cancel()
		hubs, err := node.Discover(ctx, discoveryTimeout)
		return discoveryMsg{id: id, hubs: hubs, err: err}
	}, m.discoverySpinner())
}

func (m *model) discoverySpinner() tea.Cmd {
	id := m.discoveryID
	return after(m.ctx, m.indicator.Spinner.FPS, func(time.Time) tea.Msg { return discoverySpinMsg{id} })
}

func (m *model) updatePicker(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	f := m.form
	switch k.String() {
	case "esc":
		m.cancelDiscovery()
		m.form = nil
	case "ctrl+r":
		return m.discover()
	case "up", "k", "down", "j":
		step := 1
		if k.String() == "up" || k.String() == "k" {
			step = -1
		}
		for i := f.choice + step; i >= 0 && i <= len(f.hubs); i += step {
			if i == len(f.hubs) || compatible(f.hubs[i]) {
				f.choice = i
				break
			}
		}
	case "enter":
		if f.choice < len(f.hubs) && !compatible(f.hubs[f.choice]) {
			return nil
		}
		m.cancelDiscovery()
		f.picking, f.searching = false, false
		if f.choice == len(f.hubs) {
			return m.focusForm(0)
		}
		f.inputs[0].SetValue(f.hubs[f.choice].URL)
		if f.pair {
			// Then the name, which the hub shows and binds the key to.
			return m.focusForm(1)
		}
		return m.saveForm()
	}
	return nil
}

func (m *model) pickerLines() []string {
	// Matches the create-config modal's inner width in View.
	width := max(1, min(80, m.width)-4)
	f := m.form
	title := "Create config"
	if f.pair {
		title = "Pair with a hub"
	}
	lines := []string{m.bold(title), m.dim("Choose your hub"), ""}
	if f.searching {
		lines = append(lines, m.accent(m.indicator.View())+" Searching the local network…")
	}
	// Keep the selected choice visible even when a network advertises many hubs.
	count := max(1, min(len(f.hubs)+1, m.height-12))
	first := min(max(0, f.choice-count+1), max(0, len(f.hubs)+1-count))
	for i := first; i < first+count; i++ {
		text := "Enter the URL manually"
		if i < len(f.hubs) {
			h := f.hubs[i]
			width := max(1, min(80, m.width)-6)
			nameWidth := min(18, max(1, width/3))
			suffix := ""
			if !compatible(h) {
				suffix = " · incompatible"
			}
			prefix := cell(singleLine(h.Name), nameWidth) + "  "
			text = prefix + ansi.Truncate(singleLine(h.URL), max(1, width-nameWidth-2-len(suffix)), "…") + suffix
			if !compatible(h) {
				text = m.dim(text)
			}
		}
		marker := "  "
		if i == f.choice {
			marker = m.accent("▌ ")
		}
		lines = append(lines, zone("pick:"+strconv.Itoa(i), marker+text))
	}
	if !f.searching && len(f.hubs) == 0 && f.discoveryError == "" {
		lines = append(lines, m.dimWrap("No hubs found on this network (Docker bridge networks and VPNs like Tailscale don't carry discovery).", width)...)
	}
	if f.discoveryError != "" {
		lines = append(lines, m.dimWrap(singleLine(f.discoveryError), width)...)
	}
	lines = append(lines, "")
	lines = append(lines, m.dimWrap("Found on the local network; only connect to hubs you trust — a hub can call this machine's MCP tools.", width)...)
	return lines
}
