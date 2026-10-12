package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m *model) titleBar() string {
	s := m.snapshot
	name, version := s.Config.Node, s.Version
	if name == "" {
		name = s.Daemon.Status.Node
	}
	if version == "" {
		version = s.Daemon.Status.Version
	}
	left := m.badge() + "  " + m.bold(singleLine(name))
	right := m.dim(singleLine(version))
	available := max(0, m.width-lipgloss.Width(right)-2)
	left = ansi.Truncate(left, available, "…")
	return left + strings.Repeat(" ", max(0, m.width-lipgloss.Width(left)-lipgloss.Width(right))) + right
}

type statusCard struct {
	title, state, detail string
	// tailTruncate keeps the end of the detail (a path's file name).
	tailTruncate bool
}

func (m *model) cards() []statusCard {
	s := m.snapshot
	warnDetail := false
	node := statusCard{title: "Node", state: m.dim("○ stopped"), detail: "s start"}
	if s.Service.Supported {
		node.detail += " · i start at login"
	}
	if s.Service.Installed {
		node.detail = "starts at login · s start now"
		if strings.Contains(strings.ToLower(s.Service.Detail), "fail") && !s.Service.Running {
			node.detail = singleLine(s.Service.Detail)
			warnDetail = true
		}
	}
	if s.Daemon.Running {
		warnDetail = false
		node.state = m.green("● running · up " + elapsed(m.now, s.Daemon.Status.StartedAt))
		node.detail = "until logout"
		if s.Service.Running {
			node.detail = fmt.Sprintf("starts at login · pid %d", s.Daemon.Status.PID)
		} else if s.Service.Supported && !s.Service.Installed {
			node.detail += " · i start at login"
		}
	}
	if s.Service.Err != "" {
		node.state, node.detail = m.red("● error"), singleLine(s.Service.Err)
		warnDetail = false
	}
	if s.Daemon.Err != "" {
		node.state, node.detail = m.red("● error"), singleLine(s.Daemon.Err)
		warnDetail = false
	}
	hub := statusCard{title: "Hub", state: m.dim("○ not configured"), detail: singleLine(s.Config.Hub)}
	if s.Config.Hub != "" {
		hub.state = m.dim("○ node stopped")
	}
	if s.Daemon.Running {
		h := s.Daemon.Status.Hub
		hub.detail = singleLine(h.URL)
		switch {
		case h.Connected:
			hub.state = m.green("● connected · " + elapsed(m.now, h.Since))
		case h.PairCode != "":
			hub.state = lipgloss.NewStyle().Foreground(m.theme().warn).Render("● waiting for approval")
			hub.detail = "code " + singleLine(h.PairCode) + " · approve it on the hub"
		default:
			hub.state = lipgloss.NewStyle().Foreground(m.theme().warn).Render("● retrying")
			hub.detail = singleLine(h.LastError)
			if h.KeyRejected {
				hub.state, hub.detail = m.red("● key rejected"), singleLine(h.KeyReason)
				if !s.Config.Key.Set() {
					hub.state, hub.detail = m.red("● not joined"), singleLine(h.KeyReason)+" · p pair"
				}
			}
		}
	}
	cfg := statusCard{title: "Config", state: m.green("✓ valid"), detail: singleLine(homePath(s.ConfigPath))}
	cfg.tailTruncate = true
	if !s.Config.Exists {
		cfg.state = m.dim("○ not created yet")
	}
	if s.Config.Err != "" {
		cfg.state = m.red("● invalid")
		cfg.detail = singleLine(s.Config.Err)
	} else if s.Daemon.Running && s.Daemon.Status.ConfigChanged {
		cfg.state = lipgloss.NewStyle().Foreground(m.theme().warn).Render("● changed · R applies")
	}
	if warnDetail {
		node.detail = lipgloss.NewStyle().Foreground(m.theme().warn).Render(node.detail)
	} else {
		node.detail = m.dim(node.detail)
	}
	hub.detail, cfg.detail = m.dim(hub.detail), m.dim(cfg.detail)
	return []statusCard{node, hub, cfg}
}

// Below this width the state and login hints need more room than three boxes provide.
const cardBreakpoint = 90

func (m *model) dashboardStatus() []string {
	if m.width < cardBreakpoint {
		lines := []string{m.titleBar()}
		for _, card := range m.cards() {
			line := card.title + strings.Repeat(" ", 8-len(card.title)) + card.state
			if ansi.Strip(card.detail) != "" {
				line += " · " + card.detail
			}
			lines = append(lines, ansi.Truncate(line, m.width, "…"))
		}
		return lines
	}
	cards := m.cards()
	boxes := make([][]string, len(cards))
	// Hub and Config hold URLs and paths; give them more room.
	weights := []int{5, 6, 6}
	used := 0
	for j, card := range cards {
		width := m.width * weights[j] / 17
		if j == len(cards)-1 {
			width = m.width - used
		}
		used += width
		detail := card.detail
		if inner := width - 4; card.tailTruncate && lipgloss.Width(detail) > inner && inner > 1 {
			detail = "…" + ansi.TruncateLeft(detail, lipgloss.Width(detail)-inner+1, "")
		}
		boxes[j] = m.box([]string{card.state, detail}, m.bold(card.title), width)
	}
	lines := []string{m.titleBar(), ""}
	for row := 0; row < 4; row++ {
		line := ""
		for _, box := range boxes {
			line += box[row]
		}
		lines = append(lines, line)
	}
	return lines
}

func (m *model) guidance() []string {
	s := m.snapshot
	if !m.inspected {
		return []string{"Inspecting this node…"}
	}
	if !s.Config.Exists {
		// View shows the welcome modal instead of the dashboard.
		return nil
	}
	if s.Config.Err != "" {
		return []string{m.red("Invalid config: " + singleLine(s.Config.Err)), "Press e to fix it"}
	}
	var lines []string
	if !s.Daemon.Running {
		hint := "The node isn't running. Press s to start it"
		if s.Service.Supported && !s.Service.Installed {
			hint += ", or i to start it now and at every login"
		}
		lines = append(lines, hint+".")
	} else if h := s.Daemon.Status.Hub; h.KeyRejected && !s.Config.Key.Set() {
		lines = append(lines,
			m.red("This machine hasn't joined the hub: "+singleLine(h.KeyReason)+"."),
			"Press p to pair: an admin approves it in the hub's dashboard. Or K to set a key made on the hub.")
	} else if h.KeyRejected {
		lines = append(lines,
			m.red("The hub rejected this node's key: "+singleLine(h.KeyReason)+"."),
			"Create one on the hub (mcp-mesh-hub key create node "+singleLine(s.Config.Node)+"), then press K or run: mcp-mesh-node key set <key>")
	} else if !h.Connected && h.LastError != "" {
		// A node waiting for approval has no error: the notice says what to do.
		lines = append(lines, "Hub: "+singleLine(h.LastError)+" · keeps retrying")
	}
	return lines
}

func (m *model) topLines() []string {
	lines := m.dashboardStatus()
	lines = append(lines, "")
	guidance := m.guidance()
	lines = append(lines, guidance...)
	table := m.tableLines()
	// Keep guidance apart from the table header; the empty table has its own spacing.
	if len(guidance) > 0 && len(m.rows) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, table...)
	return lines
}
