package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/AldrinSalazar/mcp-mesh/internal/discovery"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/charmbracelet/x/ansi"
)

func TestScanZones(t *testing.T) {
	lines := []string{
		"ab " + zone("key:s", "\x1b[1ms\x1b[0m start") + "  " + zone("key:q", "q quit"),
		"│ " + zone("row:1", "web "+zone("tab:0", "logs")) + " │",
		zone("pick:0", "wrapped"),
	}
	// A zone the layout wrapped continues on the next line.
	lines[2] = zoneOpen + "pick:0" + apcEnd + "first"
	lines = append(lines, "second"+zoneClose+" after")
	hits := scanZones(lines)
	want := []hit{
		{"key:s", 0, 3, 10}, {"key:q", 0, 12, 18},
		{"tab:0", 1, 6, 10}, {"row:1", 1, 2, 10},
		{"pick:0", 2, 0, 5}, {"pick:0", 3, 0, 6},
	}
	if len(hits) != len(want) {
		t.Fatalf("hits = %v", hits)
	}
	for i := range want {
		if hits[i] != want[i] {
			t.Errorf("hit %d = %v, want %v", i, hits[i], want[i])
		}
	}
	for _, line := range lines {
		if strings.Contains(line, "\x1b_") {
			t.Errorf("mark left on screen: %q", line)
		}
	}
	if got := ansi.Strip(lines[1]); got != "│ web logs │" {
		t.Errorf("line = %q", got)
	}
}

// clickOn renders the screen and clicks the zone id, as the terminal
// would, then runs what the click started.
func clickOn(t *testing.T, m *model, id string) {
	t.Helper()
	m.View()
	for _, h := range m.zones {
		if h.id == id {
			_, cmd := m.Update(tea.MouseClickMsg{X: h.x0, Y: h.y, Button: tea.MouseLeft})
			if cmd != nil {
				m.Update(work(t, cmd))
			}
			return
		}
	}
	var ids []string
	for _, h := range m.zones {
		ids = append(ids, h.id)
	}
	t.Fatalf("no zone %q on screen; zones: %v", id, ids)
}

func TestClickFooterHintsPressTheirKeys(t *testing.T) {
	m, f := fixture(t, true)
	if v := m.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Fatal("mouse not reported")
	}
	// Every hint on the main screen is a button.
	m.View()
	for _, g := range m.hintGroups() {
		for _, hint := range g.hints {
			if key := hintKey(hint); key != "" {
				found := false
				for _, h := range m.zones {
					found = found || h.id == "key:"+key
				}
				if !found {
					t.Errorf("hint %q is not clickable", hint)
				}
			}
		}
	}
	clickOn(t, m, "key:a")
	if m.form == nil || !m.form.add {
		t.Fatal("clicking a add did not open the add form")
	}
	clickOn(t, m, "key:esc")
	if m.form != nil {
		t.Fatal("clicking esc cancel left the form open")
	}
	clickOn(t, m, "key:S")
	if m.confirm != "stop" {
		t.Fatal("clicking S stop did not ask to confirm")
	}
	clickOn(t, m, "key:n")
	if m.confirm != "" || f.count("stop") != 0 {
		t.Fatal("clicking n cancel stopped the node")
	}
}

func TestClickRowsAndTabs(t *testing.T) {
	m, f := fixture(t, true)
	clickOn(t, m, "row:1")
	if r, _ := m.selectedMCP(); r.entry.Name != "web" {
		t.Fatalf("selected %q", r.entry.Name)
	}
	clickOn(t, m, "tab:1")
	if m.tab != 1 || f.count("tools:web") == 0 {
		t.Fatalf("tab %d, calls %v", m.tab, f.calls)
	}
	// Clicks on rows do nothing while a dialog is open.
	m.Update(key("?"))
	m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	if m.help {
		t.Fatal("a click outside the help did not close it")
	}
}

func TestWheelScrollsDetail(t *testing.T) {
	m, f := fixture(t, true)
	f.logs["fs"] = strings.Repeat("line\n", 200)
	m.Update(work(t, m.loadDetail()))
	bottom := m.viewport.YOffset()
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.viewport.YOffset() >= bottom || m.follow {
		t.Fatalf("wheel up: offset %d (was %d), follow %v", m.viewport.YOffset(), bottom, m.follow)
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.viewport.YOffset() != bottom {
		t.Fatalf("wheel down: offset %d, want %d", m.viewport.YOffset(), bottom)
	}
}

func TestClickPickerAndFields(t *testing.T) {
	m, f := pickerFixture(t)
	f.hubs = []discovery.Hub{{Name: "home-lab", URL: "ws://192.168.1.10:7001/node", Version: proto.Version}, {Name: "old", URL: "ws://192.168.1.30:7001/node", Version: proto.MinVersion - 1}}
	_, search := m.Update(key("p"))
	m.Update(work(t, search))
	clickOn(t, m, "pick:1")
	if m.form.choice != 0 {
		t.Fatal("an incompatible hub was chosen")
	}
	clickOn(t, m, "pick:2")
	if m.form.choice != 2 || !m.form.picking {
		t.Fatal("one click on manual entry should only choose it")
	}
	clickOn(t, m, "pick:0")
	clickOn(t, m, "pick:0")
	if m.form.picking || m.form.inputs[0].Value() != f.hubs[0].URL || m.form.focus != 1 {
		t.Fatal("a second click did not take the hub")
	}
	clickOn(t, m, "field:0")
	if m.form.focus != 0 {
		t.Fatal("clicking the hub field did not focus it")
	}

	// The add form's Start switch flips when clicked once focused.
	m, _ = fixture(t, false)
	m.Update(key("a"))
	clickOn(t, m, "pick:1")
	clickOn(t, m, "pick:1")
	if m.form.choosing || !m.form.connect {
		t.Fatal("clicking connect twice did not pick it")
	}
	lazy := m.form.lazy
	clickOn(t, m, "field:2")
	clickOn(t, m, "field:2")
	if m.form.focus != 2 || m.form.lazy == lazy {
		t.Fatalf("start switch: focus %d, lazy %v", m.form.focus, m.form.lazy)
	}
}

func TestMouseToggle(t *testing.T) {
	m, _ := fixture(t, true)
	m.Update(key("m"))
	if v := m.View(); v.MouseMode != tea.MouseModeNone {
		t.Fatal("m did not turn the mouse off")
	}
	contains(t, m.View().Content, "mouse off")
	m.Update(key("m"))
	if v := m.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Fatal("m did not turn the mouse back on")
	}
}
