package tui

import (
	"strings"
	"testing"

	"github.com/AldrinSalazar/mcp-mesh/internal/discovery"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

// waitingNode is a running node with no key that asked the hub to pair.
func waitingNode(f *fakeNode, code string) {
	f.snapshot.Config.Key = config.KeySource{}
	f.snapshot.Daemon.Running = true
	f.snapshot.Daemon.Status.Hub = control.HubStatus{URL: f.snapshot.Config.Hub, PairCode: code}
}

func TestPairFromWelcome(t *testing.T) {
	m, f := pickerFixture(t)
	f.hubs = []discovery.Hub{{Name: "home-lab", URL: "ws://192.168.1.10:7001/node", Version: proto.Version}}
	_, search := m.Update(key("p"))
	contains(t, m.View().Content, "Pair with a hub", "Searching the local network…")
	m.Update(work(t, search))
	m.Update(key("enter"))
	if m.form == nil || m.form.picking || m.form.focus != 1 {
		t.Fatal("choosing a hub did not move to the name")
	}
	contains(t, m.View().Content, "This machine's name in the mesh", "No key to copy")
	m.form.inputs[1].SetValue("")
	typeText(m, "studio-pc")
	action(t, m, "enter")
	if f.count("pair") != 1 || f.pairHub != "ws://192.168.1.10:7001/node" || f.pairName != "studio-pc" {
		t.Fatalf("Pair(%q, %q), %d calls", f.pairHub, f.pairName, f.count("pair"))
	}
	if m.form != nil || !m.pairing {
		t.Fatal("pairing screen not shown")
	}
	contains(t, m.View().Content, "Asking the hub…")

	f.snapshot.Config = nodectl.ConfigState{Exists: true, Hub: f.pairHub, Node: f.pairName}
	waitingNode(f, "K7Q-4MX")
	inspect(t, m)
	contains(t, m.View().Content, "Waiting for approval", "K 7 Q - 4 M X", "open Approvals", "k use a key instead")

	f.snapshot.Config.Key = config.KeySource{File: "/tmp/node.key"}
	f.snapshot.Daemon.Status.Hub = control.HubStatus{URL: f.pairHub, Connected: true}
	inspect(t, m)
	contains(t, m.View().Content, "Paired as studio-pc", "enter close")
	m.Update(key("enter"))
	if m.pairing {
		t.Fatal("enter did not close the pairing screen")
	}
}

func TestPairRejectsBadInput(t *testing.T) {
	for _, tc := range []struct{ hub, name, want string }{
		{"http://hub/node", "pc", "ws or wss"},
		{"ws://hub/node", "my pc", "can't contain spaces"},
		{"ws://hub/node", "", "required"},
	} {
		m, f := pickerFixture(t)
		m.Update(key("p"))
		m.Update(key("esc"))
		m.Update(key("p"))
		m.form.picking = false
		m.form.inputs[0].SetValue(tc.hub)
		m.form.inputs[1].SetValue(tc.name)
		m.focusForm(1)
		m.Update(key("enter"))
		if f.count("pair") != 0 || !strings.Contains(m.form.err, tc.want) {
			t.Fatalf("%s %q: err %q", tc.hub, tc.name, m.form.err)
		}
	}
}

func TestPairAgainConfirms(t *testing.T) {
	m, f := fixture(t, true)
	f.snapshot.Config.Key = config.KeySource{File: "/tmp/node.key"}
	inspect(t, m)
	m.Update(key("p"))
	contains(t, m.View().Content, "Pair this machine again? y/n", "removes it")
	m.Update(key("n"))
	if m.form != nil || m.confirm != "" {
		t.Fatal("n went on pairing")
	}
	m.Update(key("p"))
	m.Update(key("y"))
	if m.form == nil || !m.form.pair || m.form.inputs[0].Value() != "ws://hub/node" || m.form.inputs[1].Value() != "work-pc" {
		t.Fatal("pair form not prefilled with the config's hub and name")
	}
}

func TestPairShowsWaitingCode(t *testing.T) {
	m, f := fixture(t, true)
	waitingNode(f, "ABC-123")
	inspect(t, m)
	contains(t, m.View().Content, "● waiting for approval", "code ABC-123")
	absent(t, m.View().Content, "keeps retrying")
	m.Update(key("p"))
	if !m.pairing || f.count("pair") != 0 {
		t.Fatal("a node already waiting asked again")
	}
	contains(t, m.View().Content, "A B C - 1 2 3")
	m.Update(key("esc"))
	if m.pairing {
		t.Fatal("esc did not close the pairing screen")
	}
}

func TestPairDeclinedAsksAgain(t *testing.T) {
	m, f := fixture(t, true)
	waitingNode(f, "")
	f.snapshot.Daemon.Status.Hub.KeyRejected, f.snapshot.Daemon.Status.Hub.KeyReason = true, "pairing declined"
	f.snapshot.Daemon.Status.Hub.LastError = "an admin declined this machine's pairing request; restart the node to ask again"
	inspect(t, m)
	contains(t, m.View().Content, "● not joined", "pairing declined · p pair", "Press p to pair")
	m.pairing, m.pairFresh = true, true
	contains(t, m.View().Content, "An admin declined this request", "r ask again")
	action(t, m, "r")
	if f.count("pair") != 1 || f.pairHub != "ws://hub/node" || f.pairName != "work-pc" {
		t.Fatalf("r: Pair(%q, %q), %d calls", f.pairHub, f.pairName, f.count("pair"))
	}
	m.pairing, m.pairFresh = true, true
	m.Update(key("k"))
	if m.pairing || m.form == nil || !m.form.key {
		t.Fatal("k did not open the key form")
	}
}

func TestPairInFooterAndHelp(t *testing.T) {
	m, f := fixture(t, true)
	footer := func() string { return strings.Join(m.footerLines(m.width, m.footerBudget()), "\n") }
	absent(t, footer(), "p pair", "K hub key")
	f.snapshot.Daemon.Status.Hub.Connected = false
	inspect(t, m)
	contains(t, footer(), "p pair")
	absent(t, footer(), "K ")
	m.Update(key("?"))
	contains(t, m.View().Content, "Pair with a hub", "Other ways to join", "Set a key made on the hub")
}
