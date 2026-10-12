package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Clickable text is marked while the view is built and found again once
// it is laid out: the marks are APC sequences, which every ansi and lipgloss
// function treats as zero-width and carries through truncating, wrapping and
// placing. View records where each marked text landed and strips the marks.
const (
	zoneOpen  = "\x1b_mz:"
	zoneClose = "\x1b_mz/\x1b\\"
	apcEnd    = "\x1b\\"
)

// zone marks s as the clickable target id: "key:<key>" presses that key,
// "row:<i>" selects an MCP, "tab:<i>" a detail tab, "pick:<i>" a picker
// item, "field:<i>" a form field.
func zone(id, s string) string { return zoneOpen + id + apcEnd + s + zoneClose }

// hit is where a zone landed on screen; x1 is exclusive.
type hit struct {
	id        string
	y, x0, x1 int
}

// scanZones strips the marks from the screen's lines and returns where each
// zone landed. A zone wrapped onto the next line continues there.
func scanZones(lines []string) []hit {
	var hits []hit
	type open struct {
		id string
		x  int
	}
	var stack []open
	for y, line := range lines {
		var out strings.Builder
		x := 0
		for line != "" {
			i := strings.Index(line, "\x1b_mz")
			if i < 0 {
				out.WriteString(line)
				x += ansi.StringWidth(line)
				break
			}
			out.WriteString(line[:i])
			x += ansi.StringWidth(line[:i])
			line = line[i:]
			switch {
			case strings.HasPrefix(line, zoneClose):
				line = line[len(zoneClose):]
				if n := len(stack); n > 0 {
					hits = append(hits, hit{stack[n-1].id, y, stack[n-1].x, x})
					stack = stack[:n-1]
				}
			case strings.HasPrefix(line, zoneOpen):
				end := strings.Index(line, apcEnd)
				if end < 0 {
					line = ""
					break
				}
				stack = append(stack, open{line[len(zoneOpen):end], x})
				line = line[end+len(apcEnd):]
			default:
				out.WriteString(line[:1])
				line = line[1:]
			}
		}
		for i := range stack {
			hits = append(hits, hit{stack[i].id, y, stack[i].x, x})
			stack[i].x = 0
		}
		lines[y] = out.String()
	}
	return hits
}

// at is the innermost zone under (x, y).
func (m *model) at(x, y int) (string, bool) {
	best, found := hit{}, false
	for _, h := range m.zones {
		if h.y == y && x >= h.x0 && x < h.x1 && (!found || h.x1-h.x0 < best.x1-best.x0) {
			best, found = h, true
		}
	}
	return best.id, found
}

// hintKey is the key a clicked hint presses: the first of the keys it
// lists ("enter/ctrl+s save" saves with enter). Hints for keys that only
// move ("↑↓/jk select", "PgUp/PgDn scroll") press nothing: the mouse
// selects and scrolls by itself.
func hintKey(hint string) string {
	key, _, _ := strings.Cut(hint, " ")
	key, _, _ = strings.Cut(key, "/")
	switch key {
	case "↑↓", "↑", "PgUp":
		return ""
	}
	return strings.TrimSpace(key)
}

// keyPress is the key message a click stands for.
func keyPress(key string) tea.KeyPressMsg {
	codes := map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "space": ' ', "up": tea.KeyUp, "down": tea.KeyDown, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown}
	if c, ok := codes[key]; ok {
		return tea.KeyPressMsg{Code: c}
	}
	if rest, ok := strings.CutPrefix(key, "ctrl+"); ok && rest != "" {
		return tea.KeyPressMsg{Code: []rune(rest)[0], Mod: tea.ModCtrl}
	}
	r := []rune(key)
	if len(r) == 0 {
		return tea.KeyPressMsg{}
	}
	return tea.KeyPressMsg{Code: r[0], Text: string(r[0])}
}

// click does what pressing the clicked thing's key would; every click
// goes through the same handlers as the keyboard.
func (m *model) click(x, y int) tea.Cmd {
	id, ok := m.at(x, y)
	if !ok {
		// Outside everything: help closes, like a menu.
		if m.help {
			m.help = false
		}
		return nil
	}
	kind, value, _ := strings.Cut(id, ":")
	switch kind {
	case "key":
		if value == "" {
			return nil
		}
		_, cmd := m.Update(keyPress(value))
		return cmd
	}
	n, err := strconv.Atoi(value)
	if err != nil || m.busy {
		return nil
	}
	switch kind {
	case "row":
		if m.form != nil || m.confirm != "" || m.help || m.pairing || n >= len(m.rows) {
			return nil
		}
		if n != m.selected {
			m.selected = n
			return m.loadDetail()
		}
	case "tab":
		if _, ok := m.selectedMCP(); ok && n != m.tab {
			m.tab = n
			return m.loadDetail()
		}
	case "pick":
		f := m.form
		if f == nil {
			return nil
		}
		if f.picking {
			if n < len(f.hubs) && !compatible(f.hubs[n]) {
				return nil
			}
		} else if !f.choosing {
			return nil
		}
		// A second click on the chosen item takes it, like enter.
		if f.choice == n {
			_, cmd := m.Update(keyPress("enter"))
			return cmd
		}
		f.choice = n
	case "field":
		f := m.form
		if f == nil || f.picking || f.choosing || n >= len(f.inputs) {
			return nil
		}
		if f.add && n == 2 {
			// Start is a switch: clicking it flips it.
			if f.focus == 2 {
				f.lazy = !f.lazy
			}
		}
		return m.focusForm(n)
	}
	return nil
}

// wheel scrolls whatever is scrollable: a picker's choice, the help, or
// the detail box.
func (m *model) wheel(up bool) tea.Cmd {
	key := "down"
	if up {
		key = "up"
	}
	switch {
	case m.busy || m.confirm != "" || m.pairing:
		return nil
	case m.form != nil:
		if m.form.picking || m.form.choosing {
			_, cmd := m.Update(keyPress(key))
			return cmd
		}
		return nil
	case m.help:
		_, cmd := m.Update(keyPress(key))
		return cmd
	}
	if _, ok := m.selectedMCP(); !ok {
		return nil
	}
	if up {
		if m.tab == 0 {
			m.follow = false
		}
		m.viewport.ScrollUp(3)
	} else {
		m.viewport.ScrollDown(3)
	}
	return nil
}
