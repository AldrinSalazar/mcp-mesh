package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m *model) View() tea.View {
	var content string
	switch {
	case m.help:
		lines := m.helpLines()
		page := max(1, m.height-6)
		first := min(m.helpOffset, max(0, len(lines)-page))
		content = m.modal(lines[first:min(len(lines), first+page)], m.helpLine(), min(96, m.width))
	case m.form != nil:
		lines := m.formLines()
		if m.form.choosing {
			lines = m.kindPickerLines()
		}
		if m.form.picking {
			lines = m.pickerLines()
		}
		if m.busy {
			lines = append(lines, m.noticeLine())
		}
		content = m.modal(lines, m.helpLine(), min(80, m.width))
	case m.confirm != "":
		content = m.modal(m.confirmLines(), m.hints("y confirm  n cancel  q quit"), min(76, m.width))
	case m.inspected && !m.snapshot.Config.Exists:
		lines := []string{m.badge(), "", "Welcome! This node shares your MCP servers with agents through one hub.", m.dim(singleLine(homePath(m.snapshot.ConfigPath))), m.dim("Config not created yet"), "", m.accent("n  create config")}
		if m.notice != "" {
			lines = append(lines, m.noticeLine())
		}
		content = m.modal(lines, m.helpLine(), min(76, m.width))
	default:
		lines := m.topLines()
		if _, ok := m.selectedMCP(); ok && m.snapshot.Config.Exists && m.viewport.Height() > 0 {
			lines = append(lines, m.box(strings.Split(m.viewport.View(), "\n"), m.detailTitle(), m.width)...)
		}
		notice := m.noticeLines()
		lines = lines[:min(len(lines), max(0, m.height-1-len(notice)))]
		if m.height > 1 {
			lines = append(lines, notice[:min(len(notice), m.height-1)]...)
		}
		lines = append(lines, m.helpLine())
		content = strings.Join(lines, "\n")
	}
	lines := strings.Split(content, "\n")
	lines = lines[:min(len(lines), m.height)]
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.width, "…")
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}
