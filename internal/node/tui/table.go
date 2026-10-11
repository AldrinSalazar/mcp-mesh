package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/charmbracelet/x/ansi"
)

type column struct {
	heading string
	width   int
	value   func(row) string
}

func (m *model) columns() []column {
	nameWidth := 12
	for _, r := range m.rows {
		nameWidth = max(nameWidth, min(24, lipgloss.Width(singleLine(r.entry.Name))))
	}
	live := func(r row, value string) string {
		if !m.snapshot.Daemon.Running || r.live.Name == "" {
			return "-"
		}
		return value
	}
	pidWidth := 6
	for _, r := range m.rows {
		if r.live.Sharing == "per-client" {
			pidWidth = 22
		}
	}
	cols := []column{
		{"MCP", nameWidth, func(r row) string { return singleLine(r.entry.Name) }},
		{"STATE", 19, func(r row) string {
			if r.entry.Disabled {
				return "⊘ disabled"
			}
			if !m.snapshot.Daemon.Running || r.live.Name == "" {
				return "○ stopped"
			}
			if r.live.Unreachable {
				return "◌ waiting for app"
			}
			dot := "● "
			if r.live.State == control.MCPStopped {
				dot = "○ "
			}
			return dot + singleLine(string(r.live.State))
		}},
		{"KIND", 14, func(r row) string {
			if r.entry.URL != "" || r.live.Transport == "http" || r.live.Transport == "sse" {
				transport := r.entry.Transport
				if transport == "" {
					transport = r.live.Transport
				}
				if transport == "" {
					transport = "http"
				}
				return "connect (" + transport + ")"
			}
			return "launch"
		}},
		{"START", 5, func(r row) string {
			if r.entry.Start == "" {
				return "eager"
			}
			return singleLine(r.entry.Start)
		}},
		{"TOOLS", 5, func(r row) string { return live(r, strconv.Itoa(r.live.Tools)) }},
		{"PID", pidWidth, func(r row) string {
			if r.live.Sharing == "per-client" {
				return live(r, fmt.Sprintf("per-client · %d running", runningClients(r.live)))
			}
			if r.live.PID == 0 {
				return "-"
			}
			return live(r, strconv.Itoa(r.live.PID))
		}},
		{"UP", 8, func(r row) string {
			if r.live.State != control.MCPRunning && r.live.State != control.MCPStarting {
				return "-"
			}
			return live(r, elapsed(m.now, r.live.StartedAt))
		}},
		{"RESTARTS", 8, func(r row) string { return live(r, strconv.Itoa(r.live.Restarts)) }},
		{"ACTIVITY", 17, func(r row) string {
			if r.live.State == control.MCPBackoff && !r.live.NextRetry.IsZero() {
				return fmt.Sprintf("retry in %ds", max(0, int64(r.live.NextRetry.Sub(m.now).Seconds()+0.999)))
			}
			if r.live.LastActivity.IsZero() {
				return "-"
			}
			return elapsed(m.now, r.live.LastActivity) + " ago"
		}},
	}
	total := func() int {
		n := 2 + (len(cols)-1)*2
		for _, c := range cols {
			n += c.width
		}
		return n
	}
	for len(cols) > 2 && total() > m.width {
		cols = cols[:len(cols)-1]
	}
	if total() > m.width {
		cols[0].width = max(1, m.width-cols[1].width-4)
	}
	return cols
}

func (m *model) tableLines() []string {
	if !m.snapshot.Config.Exists {
		return nil
	}
	if len(m.rows) == 0 {
		if !m.validConfig() {
			return nil
		}
		text := m.dim("No MCPs yet. Press ") + m.accent("a") + m.dim(" to add one.")
		return []string{"", lipgloss.PlaceHorizontal(m.width, lipgloss.Center, text), ""}
	}
	cols := m.columns()
	cells := make([]string, len(cols))
	for i, c := range cols {
		cells[i] = m.dim(m.bold(cell(c.heading, c.width)))
	}
	lines := []string{"  " + strings.Join(cells, "  ")}
	count := min(len(m.rows), max(1, (m.height-13)/2))
	first := min(max(0, m.selected-count+1), max(0, len(m.rows)-count))
	t := m.theme()
	for i := first; i < first+count; i++ {
		r := m.rows[i]
		// Every segment carries the row background: a styled cell ends with a
		// reset, which would otherwise clear the highlight for the rest of the row.
		base := lipgloss.NewStyle()
		if i == m.selected {
			base = base.Background(t.highlight)
		}
		if r.entry.Disabled {
			base = base.Foreground(t.muted).Faint(true)
		}
		marker := base.Render("  ")
		if i == m.selected {
			marker = base.Foreground(t.accent).Bold(true).Render("▌ ")
		}
		for j, c := range cols {
			text := cell(c.value(r), c.width)
			if c.heading == "TOOLS" || c.heading == "PID" || c.heading == "UP" || c.heading == "RESTARTS" {
				value := ansi.Truncate(c.value(r), c.width, "…")
				text = strings.Repeat(" ", max(0, c.width-lipgloss.Width(value))) + value
			}
			style := base
			if c.heading == "STATE" {
				if r.entry.Disabled || !m.snapshot.Daemon.Running {
					style = style.Foreground(t.muted).Faint(true)
				} else {
					if r.live.Unreachable {
						style = style.Foreground(t.warn)
					} else {
						style = style.Foreground(m.stateColor(r.live.State))
					}
				}
			}
			cells[j] = style.Render(text)
		}
		line := marker + strings.Join(cells, base.Render("  "))
		if i == m.selected {
			line += base.Render(strings.Repeat(" ", max(0, m.width-lipgloss.Width(line))))
		}
		lines = append(lines, line)
	}
	return lines
}

func runningClients(s control.MCPStatus) int {
	n := 0
	for _, c := range s.Clients {
		if c.State == control.MCPRunning || c.State == control.MCPStarting {
			n++
		}
	}
	return n
}
