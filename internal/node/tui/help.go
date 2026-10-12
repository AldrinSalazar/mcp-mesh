package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// stateHints are the shortcuts of a modal state (busy, confirming, a form,
// help), or "" on the main screen, whose shortcuts come in groups.
func (m *model) stateHints() string {
	if m.busy {
		return "q quit"
	}
	if m.confirm != "" {
		return "y confirm  n cancel  q quit"
	}
	if m.form != nil {
		if m.form.choosing {
			return "↑/↓ choose  enter select  esc cancel"
		}
		if m.form.key {
			return "enter save  esc cancel  ctrl+c quit"
		}
		if m.form.add {
			return "tab/shift+tab field  space/←→ start  enter/ctrl+s save  esc back"
		}
		if m.form.picking {
			return "↑/↓ choose  enter select  ctrl+r search again  esc cancel"
		}
		return "enter create  esc cancel  ctrl+c quit"
	}
	if m.help {
		return "PgUp/PgDn scroll  ? close help  q quit"
	}
	return ""
}

// hintGroup is one titled group of the main screen's shortcuts; each hint
// is "key description".
type hintGroup struct {
	title string
	hints []string
}

func (m *model) hintGroups() []hintGroup {
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
	var groups []hintGroup
	for i, hints := range [][]string{node, mcps, view} {
		if len(hints) > 0 {
			groups = append(groups, hintGroup{[]string{"Node", "MCPs", "View"}[i], hints})
		}
	}
	return groups
}

// footerBudget is how many lines the main screen's shortcuts may take: the
// detail box gives them room, but not most of a short terminal.
func (m *model) footerBudget() int {
	return max(1, min(4, m.height/6))
}

// footerLines lays the shortcuts out in width, in at most maxLines lines,
// never cutting a hint in two. The main screen tries, in order: one line;
// a line per group, wrapped between hints; then only the keys, with ? help
// and q quit first, so the part that doesn't fit is what ? lists anyway.
func (m *model) footerLines(width, maxLines int) []string {
	width, maxLines = max(1, width), max(1, maxLines)
	if s := m.stateHints(); s != "" {
		return packHints(m.styledHints(strings.Split(s, "  ")), width, maxLines)
	}
	groups := m.hintGroups()
	var parts []string
	for _, g := range groups {
		parts = append(parts, m.bold(g.title)+"  "+strings.Join(m.styledHints(g.hints), "  "))
	}
	if line := strings.Join(parts, " · "); ansi.StringWidth(line) <= width {
		return []string{line}
	}
	var lines []string
	for _, g := range groups {
		// Titles are four letters; continuation lines line up under the hints.
		group := packHints(m.styledHints(g.hints), width-6, 0)
		for i, line := range group {
			prefix := strings.Repeat(" ", 6)
			if i == 0 {
				prefix = m.bold(g.title) + "  "
			}
			lines = append(lines, prefix+line)
		}
	}
	if len(lines) <= maxLines && width > 6 {
		return lines
	}
	var keys []string
	for _, g := range groups {
		for _, hint := range g.hints {
			if hint != "? help" && hint != "q quit" {
				key, _, _ := strings.Cut(hint, " ")
				keys = append(keys, m.accent(key))
			}
		}
	}
	return packHints(append(m.styledHints([]string{"? help", "q quit"}), keys...), width, maxLines)
}

// styledHints colors each "key description" hint.
func (m *model) styledHints(hints []string) []string {
	styled := make([]string, len(hints))
	for i, hint := range hints {
		key, desc, _ := strings.Cut(hint, " ")
		styled[i] = m.accent(key) + " " + m.dim(desc)
	}
	return styled
}

// packHints fills lines of width with hints two spaces apart, breaking only
// between hints; a hint wider than a line on its own is truncated. maxLines
// > 0 drops the hints that don't fit in that many lines.
func packHints(hints []string, width, maxLines int) []string {
	var lines []string
	line := ""
	for _, hint := range hints {
		switch {
		case line == "":
			line = hint
		case ansi.StringWidth(line)+2+ansi.StringWidth(hint) <= width:
			line += "  " + hint
		default:
			lines = append(lines, line)
			line = hint
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "…")
	}
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return lines
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
