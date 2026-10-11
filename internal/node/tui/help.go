package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func (m *model) helpLine() string {
	if m.busy {
		return m.hints("q quit")
	}
	if m.confirm != "" {
		return m.hints("y confirm  n cancel  q quit")
	}
	if m.form != nil {
		if m.form.choosing {
			return m.hints("↑/↓ choose  enter select  esc cancel")
		}
		if m.form.key {
			return m.hints("enter save  esc cancel  ctrl+c quit")
		}
		if m.form.add {
			return m.hints("tab/shift+tab field  space/←→ start  enter/ctrl+s save  esc back")
		}
		if m.form.picking {
			return m.hints("↑/↓ choose  enter select  ctrl+r search again  esc cancel")
		}
		return m.hints("enter create  esc cancel  ctrl+c quit")
	}
	if m.help {
		return m.hints("PgUp/PgDn scroll  ? close help  q quit")
	}
	var node, mcps, view []string
	if m.inspected && !m.snapshot.Config.Exists {
		node = append(node, "n create config")
	}
	if m.validConfig() && !m.snapshot.Daemon.Running {
		node = append(node, "s start")
	}
	if m.snapshot.Daemon.Running {
		node = append(node, "S stop", "R apply config")
	}
	if m.validConfig() && m.snapshot.Service.Supported && !m.snapshot.Service.Installed {
		node = append(node, "i start at login")
	}
	if m.snapshot.Service.Installed {
		node = append(node, "U don't start at login")
	}
	if m.snapshot.Config.Exists {
		node = append(node, "e edit config")
	}
	if m.validConfig() {
		node = append(node, "K hub key")
	}
	if m.validConfig() {
		mcps = append(mcps, "a add")
	}
	if r, ok := m.selectedMCP(); ok {
		if m.snapshot.Daemon.Running && !r.entry.Disabled {
			mcps = append(mcps, "r restart")
		}
		mcps = append(mcps, "d enable/disable", "x remove", "↑↓/jk select")
		view = append(view, "tab detail")
		if m.tab == 0 && m.snapshot.Daemon.Running {
			view = append(view, "f follow")
		}
		view = append(view, "PgUp/PgDn scroll")
	}
	view = append(view, "? help", "q quit")
	var groups []string
	for i, group := range [][]string{node, mcps, view} {
		if len(group) > 0 {
			groups = append(groups, m.bold([]string{"Node", "MCPs", "View"}[i])+"  "+m.hints(strings.Join(group, "  ")))
		}
	}
	return ansi.Truncate(strings.Join(groups, " · "), m.width, "…")
}

func (m *model) hints(s string) string {
	pairs := strings.Split(s, "  ")
	for i, pair := range pairs {
		key, desc, _ := strings.Cut(pair, " ")
		pairs[i] = m.accent(key) + " " + m.dim(desc)
	}
	return ansi.Truncate(strings.Join(pairs, "  "), m.width, "…")
}

func (m *model) helpLines() []string {
	node := []string{m.bold("Node"), "s  Start node (valid config, stopped)", "S  Stop node (confirm)", "R  Apply config changes (restarts MCPs)", "i  Start at login (valid config, supported)", "U  Don't start at login (confirm; node stops)", "e  Edit config; R applies it", "K  Set the hub key (created on the hub for this node)", "n  Create config"}
	mcps := []string{m.bold("MCPs"), "a  Add MCP", "Connect shares an app’s localhost MCP through the hub.", "d  Enable/disable selected MCP", "x  Remove selected MCP (confirm)", "r  Restart enabled MCP (node running)", "↑/↓ or j/k  Select MCP"}
	view := []string{m.bold("View"), "tab  Detail: logs → tools → info", "f  Toggle log follow", "PgUp/PgDn  Scroll detail", "? / esc  Close help", "q / ctrl+c  Quit", "the node keeps running"}
	for _, group := range [][]string{node, mcps, view} {
		for i := 1; i < len(group); i++ {
			key, description, ok := strings.Cut(group[i], "  ")
			if ok {
				group[i] = m.accent(key) + m.dim("  "+description)
			} else {
				group[i] = m.dim(group[i])
			}
		}
	}
	lines := []string{m.bold("Help"), "", "The node runs your MCPs and connects them to the hub. s runs it until you log out; i makes it start at every login.", ""}
	width := max(1, min(96, m.width)-6)
	right := append(mcps, "")
	right = append(right, view...)
	for _, pair := range [][2][]string{{node, right}} {
		if width >= 70 {
			half := (width - 2) / 2
			for i := 0; i < max(len(pair[0]), len(pair[1])); i++ {
				left, right := "", ""
				if i < len(pair[0]) {
					left = pair[0][i]
				}
				if i < len(pair[1]) {
					right = pair[1][i]
				}
				leftLines := strings.Split(ansi.Wrap(left, half, ""), "\n")
				rightLines := strings.Split(ansi.Wrap(right, half, ""), "\n")
				for n := 0; n < max(len(leftLines), len(rightLines)); n++ {
					l, r := "", ""
					if n < len(leftLines) {
						l = leftLines[n]
					}
					if n < len(rightLines) {
						r = rightLines[n]
					}
					lines = append(lines, cell(l, half)+"  "+cell(r, half))
				}
			}
		} else {
			lines = append(lines, pair[0]...)
			lines = append(lines, "")
			lines = append(lines, pair[1]...)
		}
		lines = append(lines, "")
	}
	lines = append(lines, "Start at login uses a user service (systemd/launchd).", "It captures your login shell's PATH so MCP commands are found.", "Forms: Tab/Shift+Tab fields, space/←/→ start mode", "Enter on last field / Ctrl+S save, Esc cancel", "Node logs: "+singleLine(m.logsHint))
	var wrapped []string
	for _, line := range lines {
		wrapped = append(wrapped, strings.Split(ansi.Wrap(line, width, ""), "\n")...)
	}
	return wrapped
}
