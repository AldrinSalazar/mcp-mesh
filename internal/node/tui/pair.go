package tui

import (
	"errors"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// pairPhase is where the pairing screen is, read from the node's status.
type pairPhase int

const (
	pairAsking  pairPhase = iota // the request is on its way, or the hub is out of reach
	pairWaiting                  // the hub gave a code; an admin has to approve it
	pairDone                     // connected with the key the hub sent
	pairFailed                   // declined, refused, or the hub can't pair
	pairStopped                  // the node isn't running
)

func (m *model) pairPhase() pairPhase {
	s := m.snapshot
	h := s.Daemon.Status.Hub
	switch {
	case !m.pairFresh:
		// The snapshot from before the request may still show the old
		// connection.
		return pairAsking
	case !s.Daemon.Running:
		return pairStopped
	case h.Connected:
		return pairDone
	case h.PairCode != "":
		return pairWaiting
	case h.KeyRejected:
		return pairFailed
	}
	return pairAsking
}

func (m *model) updatePairing(key string) tea.Cmd {
	phase := m.pairPhase()
	switch key {
	case "esc", "enter":
		m.pairing = false
	case "k", "K":
		if m.validConfig() && phase != pairDone {
			m.pairing = false
			return m.openKeyForm()
		}
	case "r":
		if phase == pairFailed || phase == pairStopped {
			m.pairFresh = false
			return m.pair(m.snapshot.Config.Hub, m.snapshot.Config.Node)
		}
	}
	return nil
}

func (m *model) pairHints() string {
	switch m.pairPhase() {
	case pairDone:
		return "enter close  q quit"
	case pairFailed, pairStopped:
		return "r ask again  k use a key instead  esc close"
	}
	return "k use a key instead  esc close"
}

func (m *model) pairLines() []string {
	s := m.snapshot
	h := s.Daemon.Status.Hub
	width := max(1, min(76, m.width)-4)
	lines := []string{m.bold("Pair with a hub"), m.dim("Hub   ") + singleLine(s.Config.Hub), m.dim("Name  ") + singleLine(s.Config.Node), ""}
	warn := lipgloss.NewStyle().Foreground(m.theme().warn).Render
	switch m.pairPhase() {
	case pairWaiting:
		lines = append(lines, warn("● Waiting for approval"), "")
		lines = append(lines, m.codeBox(h.PairCode)...)
		lines = append(lines, "")
		lines = append(lines, m.dimWrap("In the hub's dashboard, open Approvals and approve this machine. Check that the request shows this code: anyone who can reach the hub can ask to join, under any name.", width)...)
	case pairDone:
		lines = append(lines, m.green("✓ Paired as "+singleLine(s.Config.Node)+" · connected to the hub"), "")
		lines = append(lines, m.dimWrap("The hub sent this machine's key, and the node saved it next to node.yaml. Add MCPs with a; the hub lists their tools.", width)...)
	case pairFailed:
		lines = append(lines, m.red("✗ "+pairFailure(h.KeyReason)), "")
		lines = append(lines, m.dimWrap(singleLine(h.LastError), width)...)
	case pairStopped:
		lines = append(lines, m.red("✗ The node isn't running"), "")
		lines = append(lines, m.dimWrap("It has to run to ask the hub. r starts it and asks again.", width)...)
	default:
		lines = append(lines, m.accent(m.indicator.View())+" Asking the hub…")
		if h.LastError != "" && m.pairFresh {
			lines = append(lines, "")
			lines = append(lines, m.dimWrap("Hub: "+singleLine(h.LastError)+" · keeps retrying", width)...)
		}
	}
	if m.busy {
		lines = append(lines, "", m.noticeLine())
	}
	return lines
}

// validNodeName is the hub's rule for the name a node key is bound to
// (auth.ValidName), checked here so a typo doesn't wait for the hub.
func validNodeName(name string) error {
	switch {
	case name == "":
		return errors.New("Name is required")
	case len(name) > 64:
		return errors.New("Name must be at most 64 bytes")
	case strings.ContainsFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }):
		return errors.New("Name can't contain spaces")
	}
	return nil
}

// pairFailure is the headline for a pairing that ended without a key.
func pairFailure(reason string) string {
	switch reason {
	case "pairing declined":
		return "An admin declined this request"
	case "", "no key set":
		return "Pairing failed"
	}
	return "Pairing failed: " + reason
}

// codeBox shows the pairing code large enough to compare at a glance with
// the one in the hub's dashboard.
func (m *model) codeBox(code string) []string {
	spaced := strings.Join(strings.Split(singleLine(code), ""), " ")
	return m.box([]string{"  " + m.accent(m.bold(spaced))}, "", lipgloss.Width(spaced)+8)
}
