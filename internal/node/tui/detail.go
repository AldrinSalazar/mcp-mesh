package tui

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func keys(m map[string]string) string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, singleLine(name))
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ", ")
}

func (m *model) detailText() string {
	r, ok := m.selectedMCP()
	if !ok {
		return "No MCP selected"
	}
	if m.tab == 2 {
		e := r.entry
		command := "Command: " + singleLine(strings.Join(append([]string{e.Command}, e.Args...), " "))
		if e.URL != "" {
			command = "URL: " + singleLine(e.URL)
		}
		if !r.configured {
			command = "Not in the config file · reload to apply file changes"
		}
		start, idle := e.Start, e.IdleTimeout
		if start == "" {
			start = "eager"
		}
		if idle == "" {
			if start == "lazy" {
				idle = "10m (default)"
			} else {
				idle = "-"
			}
		}
		allow := "all"
		if len(e.AllowTools) > 0 {
			allow = singleLine(strings.Join(e.AllowTools, ", "))
		}
		retry := "-"
		if !r.live.NextRetry.IsZero() {
			retry = r.live.NextRetry.Format(time.RFC3339)
		}
		commandKey, commandValue, paired := strings.Cut(command, ": ")
		kind, protocol := "launch", "stdio"
		if e.URL != "" || r.live.Transport == "http" || r.live.Transport == "sse" {
			kind, protocol = "connect", "Streamable HTTP"
			if e.Transport == "sse" || (e.Transport == "" && r.live.Transport == "sse") {
				protocol = "HTTP+SSE"
			}
		}
		lines := []string{m.dim(cell("Kind", 14)) + kind, command, m.dim(cell("Protocol", 14)) + protocol}
		if paired {
			lines[1] = m.dim(cell(commandKey, 14)) + commandValue
		}
		var sharing, clients string
		if r.live.Sharing == "per-client" {
			sharing = "per-client (max " + strconv.Itoa(r.live.MaxClients) + ")"
			for _, c := range r.live.Clients {
				name := c.Client
				if name == "" {
					name = "(listing)"
				}
				clients += "\n" + strings.Repeat(" ", 14) + singleLine(name) + " · " + string(c.State) + " · pid " + strconv.Itoa(c.PID)
			}
		}
		for _, field := range [][2]string{{"Sharing", sharing}, {"Dir", singleLine(e.Dir)}, {"Env", keys(e.Env)}, {"Headers", keys(e.Headers)}, {"Start", singleLine(start)}, {"Idle timeout", singleLine(idle)}, {"Allowed tools", allow}, {"Disabled", map[bool]string{true: "yes"}[e.Disabled]}, {"Last error", singleLine(r.live.LastError)}, {"Next retry", retry}} {
			if field[1] != "" && field[1] != "-" {
				lines = append(lines, m.dim(cell(field[0], 14))+field[1])
			}
		}
		if clients != "" {
			lines = append(lines, m.dim(cell("Processes", 14))+strings.TrimLeft(clients, "\n "))
		}
		return strings.Join(lines, "\n")
	}
	if !m.snapshot.Daemon.Running {
		if m.tab == 1 {
			return "start the node to see tools"
		}
		return "start the node to see logs"
	}
	if m.detailError != "" {
		return singleLine(m.detailError)
	}
	if m.detailPending {
		return "Loading…"
	}
	if m.tab == 0 {
		return logText(m.logs)
	}
	if len(m.tools) == 0 {
		return "No tools reported yet"
	}
	lines := make([]string, len(m.tools))
	for i, t := range m.tools {
		description := strings.Split(logText(t.Description), "\n")[0]
		// One row per tool; descriptions are often whole paragraphs.
		lines[i] = ansi.Truncate(m.bold(singleLine(t.Name))+m.dim(" · "+description), max(1, m.width-4), "…")
	}
	return strings.Join(lines, "\n")
}

func (m *model) detailTitle() string {
	tabs := []string{"logs", "tools", "info"}
	for i, tab := range tabs {
		if i == m.tab {
			tabs[i] = m.accent(tab)
		} else {
			tabs[i] = m.dim(tab)
		}
		tabs[i] = zone("tab:"+strconv.Itoa(i), tabs[i])
	}
	left := strings.Join(tabs, " ─ ")
	r, _ := m.selectedMCP()
	right := m.dim(singleLine(r.entry.Name))
	if m.tab == 0 {
		if m.follow {
			right += m.dim(" ─ follow on")
		} else {
			right += m.dim(" ─ follow off")
		}
	}
	space := max(1, m.width-8-lipgloss.Width(left)-lipgloss.Width(right))
	return left + " " + strings.Repeat("─", space) + " " + right
}

func (m *model) resizeDetail() {
	width := max(1, m.width-4)
	if m.viewport.Width() != width {
		m.viewport.SetWidth(width)
		m.setDetail()
	}
	m.viewport.SetHeight(max(0, m.height-len(m.topLines())-3-len(m.noticeLines())-len(m.footerLines(m.width, m.footerBudget()))))
	if m.follow && m.tab == 0 {
		m.viewport.GotoBottom()
	}
	if m.form != nil {
		for i := range m.form.inputs {
			m.form.inputs[i].SetWidth(max(1, min(80, m.width)-10))
		}
	}
}
