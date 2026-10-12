package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
	"github.com/charmbracelet/x/ansi"
)

func TestNodeCardStates(t *testing.T) {
	for _, tc := range []struct {
		name, state, detail string
		running             bool
		service             nodectl.ServiceState
		nodeErr             string
		warning, red        bool
	}{
		{name: "running at login", running: true, service: nodectl.ServiceState{Supported: true, Installed: true, Running: true, Detail: "active (running)"}, state: "● running · up 2h", detail: "starts at login · pid 1234"},
		{name: "running by hand", running: true, service: nodectl.ServiceState{Supported: true}, state: "● running · up 3m", detail: "until logout · i start at login"},
		{name: "stopped at login", service: nodectl.ServiceState{Supported: true, Installed: true}, state: "○ stopped", detail: "starts at login · s start now"},
		{name: "stopped inactive service", service: nodectl.ServiceState{Supported: true, Installed: true, Detail: "inactive (dead)"}, state: "○ stopped", detail: "starts at login · s start now"},
		{name: "stopped failed service", service: nodectl.ServiceState{Supported: true, Installed: true, Detail: "failed (exit-code)"}, state: "○ stopped", detail: "failed (exit-code)", warning: true},
		{name: "stopped by hand", service: nodectl.ServiceState{Supported: true, Detail: "inactive (dead)"}, state: "○ stopped", detail: "s start · i start at login"},
		{name: "unsupported stopped", state: "○ stopped", detail: "s start"},
		{name: "unsupported running", running: true, state: "● running · up 3m", detail: "until logout"},
		{name: "node error", service: nodectl.ServiceState{Supported: true}, nodeErr: "control socket error\nmore detail", state: "● error", detail: "control socket error more detail", red: true},
		{name: "service error", running: true, service: nodectl.ServiceState{Supported: true, Err: "cannot inspect service"}, state: "● error", detail: "cannot inspect service", red: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := fixture(t, tc.running)
			m.snapshot.Service, m.snapshot.Daemon.Err = tc.service, tc.nodeErr
			if tc.name == "running at login" {
				m.snapshot.Daemon.Status.StartedAt = m.now.Add(-2 * time.Hour)
			}
			cards := m.cards()
			if len(cards) != 3 || cards[0].title != "Node" || cards[1].title != "Hub" || cards[2].title != "Config" {
				t.Fatalf("cards: %#v", cards)
			}
			card := cards[0]
			if ansi.Strip(card.state) != tc.state || ansi.Strip(card.detail) != tc.detail {
				t.Fatalf("state %q, detail %q; want %q, %q", ansi.Strip(card.state), ansi.Strip(card.detail), tc.state, tc.detail)
			}
			if tc.red && card.state != m.red(tc.state) {
				t.Fatal("error state is not red")
			}
			if tc.warning && card.detail != lipgloss.NewStyle().Foreground(m.theme().warn).Render(tc.detail) {
				t.Fatal("failure detail is not warning colored")
			}
			m.width = cardBreakpoint - 1
			lines := m.dashboardStatus()
			if len(lines) != 4 {
				t.Fatalf("narrow status lines: %v", lines)
			}
			contains(t, lines[1], "Node    ", tc.state)
			if tc.red {
				contains(t, strings.Join(m.noticeLines(), " "), tc.detail)
			}
			if !tc.running {
				contains(t, cards[1].state, "○ node stopped")
			}
		})
	}
}

func TestStoppedGuidanceAndConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		supported, installed bool
	}{
		{"by hand", true, false}, {"at login", true, true}, {"unsupported", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := fixture(t, false)
			m.snapshot.Service = nodectl.ServiceState{Supported: tc.supported, Installed: tc.installed}
			want := "The node isn't running. Press s to start it"
			if tc.supported && !tc.installed {
				want += ", or i to start it now and at every login"
			}
			if got := strings.Join(m.guidance(), ""); got != want+"." {
				t.Fatalf("guidance: %q", got)
			}
			m.confirm = "stop"
			lines := strings.Join(m.confirmLines(), " ")
			contains(t, lines, "Stop the node? y/n", "Its MCPs stop and the hub loses them until you start it again (s).")
			if strings.Contains(lines, "It starts again at next login.") != tc.installed {
				t.Fatalf("login reminder: %s", lines)
			}
		})
	}
}

func TestCardWidthsAndPathTail(t *testing.T) {
	m, _ := fixture(t, true)
	m.snapshot.ConfigPath = "/" + strings.Repeat("long-directory/", 20) + "node.yaml"
	m.snapshot.Daemon.Status.Hub.URL = "ws://" + strings.Repeat("hub", 100) + "/node"
	for _, width := range []int{40, cardBreakpoint - 1, cardBreakpoint, 120} {
		m.width = width
		lines := m.dashboardStatus()
		for _, line := range lines {
			if lipgloss.Width(line) > width {
				t.Fatalf("status exceeds width %d: %q", width, line)
			}
		}
		if width >= cardBreakpoint {
			contains(t, strings.Join(lines, "\n"), "node.yaml")
			if strings.Count(ansi.Strip(lines[2]), "╭") != 3 {
				t.Fatal("expected three cards in one row")
			}
		}
	}
}

func TestFooterFitsSmallTerminals(t *testing.T) {
	for _, size := range [][2]int{{140, 40}, {100, 30}, {80, 24}, {60, 20}, {40, 15}, {30, 10}, {20, 4}} {
		for _, running := range []bool{true, false} {
			t.Run(fmt.Sprintf("%dx%d running=%v", size[0], size[1], running), func(t *testing.T) {
				m, _ := fixture(t, running)
				m.width, m.height = size[0], size[1]
				m.resizeDetail()
				view := strings.Split(ansi.Strip(m.View().Content), "\n")
				if len(view) > m.height {
					t.Fatalf("%d lines in a %d-line terminal", len(view), m.height)
				}
				for _, line := range view {
					if ansi.StringWidth(line) > m.width {
						t.Fatalf("line wider than %d: %q", m.width, line)
					}
				}
				footer := m.footerLines(m.width, m.footerBudget())
				if len(footer) > m.footerBudget() {
					t.Fatalf("footer takes %d lines, budget %d", len(footer), m.footerBudget())
				}
				// The footer is the bottom of the screen, not what gives way.
				if got, want := strings.Join(view[len(view)-len(footer):], "\n"), ansi.Strip(strings.Join(footer, "\n")); got != want {
					t.Fatalf("screen ends with\n%s\nwant footer\n%s", got, want)
				}
				text := ansi.Strip(strings.Join(footer, "\n"))
				contains(t, text, "? help", "q quit")
				if strings.Contains(text, "…") {
					t.Fatalf("a hint was cut:\n%s", text)
				}
				// Each hint is whole on one line. Compact, only keys show, and
				// a full footer may drop some: ? lists them all.
				compact := !strings.Contains(text, "View")
				if compact && len(footer) == m.footerBudget() {
					return
				}
				for _, g := range m.hintGroups() {
					for _, hint := range g.hints {
						key, _, _ := strings.Cut(hint, " ")
						if !strings.Contains(text, hint) && !(compact && strings.Contains(text, key)) {
							t.Errorf("hint %q missing from footer:\n%s", hint, text)
						}
					}
				}
			})
		}
	}
}

func TestModalFooterWrapsBetweenHints(t *testing.T) {
	m, _ := fixture(t, false)
	m.width, m.height = 40, 24
	m.Update(key("a"))
	m.Update(key("enter"))
	if m.form == nil || !m.form.add {
		t.Fatal("add form not open")
	}
	view := ansi.Strip(m.View().Content)
	for _, hint := range []string{"tab/shift+tab field", "space/←→ start", "enter/ctrl+s save", "esc back"} {
		found := false
		for line := range strings.SplitSeq(view, "\n") {
			found = found || strings.Contains(line, hint)
		}
		if !found {
			t.Errorf("hint %q split or missing:\n%s", hint, view)
		}
	}
}
