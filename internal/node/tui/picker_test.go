package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/AldrinSalazar/mcp-mesh/internal/discovery"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/charmbracelet/x/ansi"
)

func pickerFixture(t *testing.T) (*model, *fakeNode) {
	t.Helper()
	m, f := fixture(t, false)
	f.snapshot.Config = nodectl.ConfigState{}
	inspect(t, m)
	return m, f
}

func TestPickerSelectHub(t *testing.T) {
	m, f := pickerFixture(t)
	f.hubs = []discovery.Hub{{Name: "home-lab", URL: "ws://192.168.1.10:7001/node", Version: proto.Version}, {Name: "lab-server", URL: "ws://192.168.1.20:7001/node", Version: proto.MinVersion}}
	_, search := m.Update(key("n"))
	if f.count("discover") != 0 {
		t.Fatal("discovery ran inside Update")
	}
	contains(t, m.View().Content, "Searching the local network…", "Enter the URL manually")
	m.Update(work(t, search))
	if f.discoveryTimeout != 3*time.Second {
		t.Fatalf("timeout: %v", f.discoveryTimeout)
	}
	contains(t, m.View().Content, "home-lab", "lab-server", "ws://192.168.1.10:7001/node", "only connect to hubs you trust", "MCP tools")
	m.Update(key("down"))
	action(t, m, "enter")
	if f.count("init") != 1 || f.hub != f.hubs[1].URL || f.force {
		t.Fatalf("InitConfig: %q, force %v", f.hub, f.force)
	}
	if m.form != nil {
		t.Fatal("created config left form open")
	}
}

func TestPickerManualDuringSearch(t *testing.T) {
	m, f := pickerFixture(t)
	_, search := m.Update(key("n"))
	id := m.discoveryID
	m.Update(key("enter"))
	if m.form == nil || m.form.picking || m.form.inputs[0].Value() != "ws://" {
		t.Fatal("manual entry not opened")
	}
	if m.discoveryCancel != nil {
		t.Fatal("manual entry retained discovery")
	}
	contains(t, m.View().Content, "Hub URL", "ws://")
	m.Update(work(t, search))
	m.Update(discoveryMsg{id: id, hubs: []discovery.Hub{{Name: "late", Version: proto.Version}}})
	if m.form.picking || len(m.form.hubs) != 0 {
		t.Fatal("late discovery replaced manual entry")
	}
	typeText(m, "manual.example/node")
	action(t, m, "enter")
	if f.hub != "ws://manual.example/node" {
		t.Fatalf("manual hub: %q", f.hub)
	}
}

func TestPickerIncompatible(t *testing.T) {
	m, f := pickerFixture(t)
	f.hubs = []discovery.Hub{
		{Name: "too-old", URL: "ws://old/node", Version: proto.MinVersion - 1},
		{Name: "compatible", URL: "ws://ok/node", Version: proto.Version},
		{Name: "too-new", URL: "ws://new/node", Version: proto.Version + 1},
	}
	_, search := m.Update(key("n"))
	m.Update(work(t, search))
	contains(t, m.View().Content, "too-old", "too-new", "incompatible")
	if m.form.choice != 1 {
		t.Fatal("initial selection is incompatible")
	}
	m.Update(key("up"))
	if m.form.choice != 1 {
		t.Fatal("up selected incompatible hub")
	}
	m.Update(key("down"))
	if m.form.choice != 3 {
		t.Fatal("down did not skip incompatible hub")
	}
	m.Update(key("up"))
	if m.form.choice != 1 {
		t.Fatal("up did not skip incompatible hub")
	}
	for _, i := range []int{0, 2} {
		m.form.choice = i
		_, cmd := m.Update(key("enter"))
		if cmd != nil || f.count("init") != 0 || !m.form.picking {
			t.Fatal("incompatible hub submitted")
		}
	}
}

func TestPickerNoneAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"none", nil}, {"error", errors.New("browse unavailable\x1b]0;bad\a")}} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.err
			m, f := pickerFixture(t)
			f.discoveryErr = err
			_, search := m.Update(key("n"))
			m.Update(work(t, search))
			contains(t, m.View().Content, "Enter the URL manually")
			if err == nil {
				contains(t, m.View().Content, "No hubs found on this network", "Docker bridge networks", "Tailscale don't carry discovery")
			} else {
				contains(t, m.View().Content, "browse unavailable")
				absent(t, m.View().Content, "bad")
			}
			if m.form.choice != 0 || m.form.searching {
				t.Fatal("manual option not selected after search")
			}
			m.Update(key("enter"))
			if m.form.picking {
				t.Fatal("manual entry unavailable")
			}
		})
	}
}

func TestPickerResearchDropsStale(t *testing.T) {
	m, f := pickerFixture(t)
	_, first := m.Update(key("n"))
	oldID := m.discoveryID
	oldResult := work(t, first).(discoveryMsg)
	oldResult.hubs = []discovery.Hub{{Name: "stale", URL: "ws://stale/node", Version: proto.Version}}
	f.hubs = []discovery.Hub{{Name: "current", URL: "ws://current/node", Version: proto.Version}}
	_, second := m.Update(key("ctrl+r"))
	if m.discoveryID == oldID {
		t.Fatal("re-search did not change epoch")
	}
	m.Update(oldResult)
	if !m.form.searching || len(m.form.hubs) != 0 {
		t.Fatal("old result ended current search")
	}
	m.Update(work(t, second))
	m.Update(oldResult)
	contains(t, m.View().Content, "current")
	absent(t, m.View().Content, "stale")
	if f.count("discover") != 2 {
		t.Fatal("re-search did not call Discover")
	}
	m.Update(key("esc"))
	m.Update(discoveryMsg{id: m.discoveryID, hubs: f.hubs})
	if m.form != nil {
		t.Fatal("result reopened canceled picker")
	}
	_, next := m.Update(key("n"))
	if next == nil {
		t.Fatal("could not reopen picker")
	}
	m.Update(discoveryMsg{id: oldID, hubs: oldResult.hubs})
	if len(m.form.hubs) != 0 || !m.form.searching {
		t.Fatal("stale result applied to new form")
	}
	m.Update(key("ctrl+c"))
	if m.discoveryCancel != nil {
		t.Fatal("quit retained discovery")
	}
}

func TestDashboardCardBreakpointsAndMarker(t *testing.T) {
	for _, width := range []int{120, 100, 99, 90, 89, 80, 70, 60, 40} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m, _ := fixture(t, true)
			m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
			view := m.View().Content
			contains(t, view, "▌ fs")
			cards := strings.Join(m.dashboardStatus(), "\n")
			contains(t, cards, "Node", "Hub", "Config")
			absent(t, cards, "Daemon", "Service")
			if width >= cardBreakpoint {
				contains(t, cards, "╭", "╰")
				want := 6
				if lipgloss.Height(cards) != want {
					t.Fatalf("card rows: %d, want %d", lipgloss.Height(cards), want)
				}
			} else {
				absent(t, cards, "╭", "╰")
				contains(t, cards, "Node    ")
			}
			for _, line := range strings.Split(view, "\n") {
				if lipgloss.Width(line) > width {
					t.Fatalf("line exceeds %d: %q", width, line)
				}
			}
		})
	}
}

func TestHelpModalGroupsAndKeys(t *testing.T) {
	m, _ := fixture(t, true)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(key("?"))
	contains(t, m.View().Content, "╭", "Node", "MCPs", "View", "Start node", "Start at login", "Add MCP", "Detail: logs", "MCP commands are found", "the node keeps running", "Node logs:")
	for _, line := range strings.Split(m.View().Content, "\n") {
		if lipgloss.Width(line) > 120 {
			t.Fatal("help too wide")
		}
	}
}

func TestCardErrorsRemainInNotice(t *testing.T) {
	m, f := fixture(t, true)
	f.snapshot.Daemon.Err = "cannot reach node: " + strings.Repeat("error details ", 8)
	inspect(t, m)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	contains(t, strings.Join(m.noticeLines(), "\n"), "cannot reach node")
	full := strings.Join(strings.Fields(ansi.Strip(strings.Join(m.noticeLines(), " "))), " ")
	if !strings.Contains(full, strings.TrimSpace(singleLine(f.snapshot.Daemon.Err))) {
		t.Fatalf("error truncated: %s", full)
	}
	contains(t, m.View().Content, "✗ Node")
}
