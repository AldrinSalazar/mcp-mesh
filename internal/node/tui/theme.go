package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/charmbracelet/x/ansi"
)

type theme struct{ accent, ok, warn, error, muted, border, highlight, badgeText color.Color }

func newTheme(dark bool) theme {
	adaptive := lipgloss.LightDark(dark)
	c := func(light, dark string) color.Color { return adaptive(lipgloss.Color(light), lipgloss.Color(dark)) }
	return theme{
		accent: c("#5A3FD1", "#7D56F4"), ok: c("#16733C", "#73D49A"),
		warn: c("#936100", "#EDCE6A"), error: c("#B52232", "#FF8890"),
		muted: c("#626262", "#A0A0A0"), border: c("#C8C8D0", "#454550"),
		highlight: c("#EEEBFA", "#262237"), badgeText: c("#FFFFFF", "#FFFFFF"),
	}
}
func (m *model) theme() theme { return newTheme(m.dark) }
func (m *model) accent(s string) string {
	return lipgloss.NewStyle().Foreground(m.theme().accent).Bold(true).Render(s)
}
func (m *model) bold(s string) string  { return lipgloss.NewStyle().Bold(true).Render(s) }
func (m *model) green(s string) string { return lipgloss.NewStyle().Foreground(m.theme().ok).Render(s) }
func (m *model) red(s string) string {
	return lipgloss.NewStyle().Foreground(m.theme().error).Render(s)
}
func (m *model) warn(s string) string {
	return lipgloss.NewStyle().Foreground(m.theme().warn).Render(s)
}
func (m *model) dim(s string) string {
	return lipgloss.NewStyle().Foreground(m.theme().muted).Faint(true).Render(s)
}
func (m *model) stateColor(state control.MCPState) color.Color {
	t := m.theme()
	switch state {
	case control.MCPRunning:
		return t.ok
	case control.MCPStarting, control.MCPBackoff:
		return t.warn
	case control.MCPFailed:
		return t.error
	default:
		return t.muted
	}
}
func (m *model) badge() string {
	return lipgloss.NewStyle().Background(m.theme().accent).Foreground(m.theme().badgeText).Bold(true).Render(" mcp-mesh ")
}

// dimWrap wraps before styling: wrapping styled text loses the style on
// continuation lines.
func (m *model) dimWrap(s string, width int) []string {
	var out []string
	for _, line := range strings.Split(ansi.Wrap(s, width, ""), "\n") {
		out = append(out, m.dim(line))
	}
	return out
}
