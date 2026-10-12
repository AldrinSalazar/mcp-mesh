package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func logText(s string) string {
	s = ansi.Strip(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\t", "    ")
	return strings.Map(func(r rune) rune {
		if r != '\n' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func singleLine(s string) string {
	return strings.ReplaceAll(logText(s), "\n", " ")
}

func elapsed(now, since time.Time) string {
	if since.IsZero() {
		return "-"
	}
	seconds := int64(max(0, now.Sub(since).Seconds()))
	switch {
	case seconds >= 86400:
		return fmt.Sprintf("%dd%dh", seconds/86400, seconds%86400/3600)
	case seconds >= 3600:
		if seconds%3600 < 60 {
			return fmt.Sprintf("%dh", seconds/3600)
		}
		return fmt.Sprintf("%dh%dm", seconds/3600, seconds%3600/60)
	case seconds >= 60:
		return fmt.Sprintf("%dm", seconds/60)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

func homePath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(filepath.Separator)) {
			return "~" + path[len(home):]
		}
	}
	return path
}

func cell(s string, width int) string {
	s = ansi.Truncate(s, width, "…")
	return s + strings.Repeat(" ", max(0, width-lipgloss.Width(s)))
}

// Borders are assembled explicitly so titles can contain independently styled tabs.
func (m *model) box(lines []string, title string, width int) []string {
	if width < 4 {
		return lines
	}
	border := lipgloss.NewStyle().Foreground(m.theme().border).Render
	inner := width - 2
	title = ansi.Truncate(title, max(0, inner-3), "…")
	top := border("╭")
	if title != "" {
		top += border("─ ") + title + " " + border(strings.Repeat("─", max(0, inner-3-lipgloss.Width(title))))
	} else {
		top += border(strings.Repeat("─", inner))
	}
	out := []string{top + border("╮")}
	for _, line := range lines {
		out = append(out, border("│")+" "+cell(line, max(0, inner-2))+" "+border("│"))
	}
	return append(out, border("╰"+strings.Repeat("─", inner)+"╯"))
}

func (m *model) modal(lines []string, width int) string {
	inner := max(1, width-4)
	var wrapped []string
	for _, line := range lines {
		wrapped = append(wrapped, strings.Split(ansi.Wrap(line, inner, ""), "\n")...)
	}
	footerLines := m.footerLines(inner, 3)
	limit := max(1, m.height-3-len(footerLines))
	wrapped = wrapped[:min(len(wrapped), limit)]
	wrapped = append(wrapped, "")
	wrapped = append(wrapped, footerLines...)
	box := strings.Join(m.box(wrapped, "", width), "\n")
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}
