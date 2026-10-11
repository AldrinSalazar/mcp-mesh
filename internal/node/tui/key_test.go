package tui

import (
	"errors"
	"strings"
	"testing"
)

const testKey = "mm_node_0123456789abcdefghijklmnop"

func TestKeyRejectedCard(t *testing.T) {
	m, f := fixture(t, true)
	hub := &f.snapshot.Daemon.Status.Hub
	hub.Connected = false
	hub.KeyRejected, hub.KeyReason = true, "key revoked"
	hub.LastError = `hub rejected the node key: key revoked; set a key created on the hub for node "work-pc" with mcp-mesh-node key set`
	inspect(t, m)
	view := m.View().Content
	contains(t, view,
		"● key rejected", "key revoked",
		"The hub rejected this node's key: key revoked.",
		"mcp-mesh-hub key create node work-pc", "mcp-mesh-node key set <key>",
		"K hub key")
	absent(t, view, "● retrying", "keeps retrying")

	// An ordinary connection failure is still shown as retrying.
	hub.KeyRejected, hub.KeyReason, hub.LastError = false, "", "connection refused"
	inspect(t, m)
	contains(t, m.View().Content, "● retrying", "keeps retrying")
	absent(t, m.View().Content, "key rejected")

	// The narrow layout carries the state too.
	hub.KeyRejected, hub.KeyReason = true, "no key set"
	m.width = 80
	inspect(t, m)
	contains(t, m.View().Content, "key rejected")
}

func TestKeyForm(t *testing.T) {
	m, f := fixture(t, true)
	f.reloaded = true
	m.Update(key("K"))
	if m.form == nil || !m.form.key {
		t.Fatal("K did not open the key form")
	}
	contains(t, m.View().Content, "Set hub key", "mcp-mesh-hub key create node work-pc", "enter save")

	// Typed characters are masked on screen.
	typeText(m, testKey)
	view := m.View().Content
	absent(t, view, testKey, "mm_node_")
	contains(t, view, "*")

	// A key of the wrong shape stays in the form with the reason and does not reach the node.
	m.form.inputs[0].SetValue("nope")
	m.Update(key("enter"))
	if m.form == nil || f.count("setkey") != 0 {
		t.Fatalf("invalid key submitted: form=%v calls=%v", m.form, f.calls)
	}
	contains(t, m.View().Content, "starts with mm_node_")

	m.form.inputs[0].SetValue("  " + testKey + "  ")
	action(t, m, "enter")
	f.mu.Lock()
	got := f.key
	f.mu.Unlock()
	if got != testKey || f.count("setkey") != 1 {
		t.Fatalf("SetKey called with %q (%d calls)", got, f.count("setkey"))
	}
	if m.form != nil {
		t.Fatal("form stayed open after saving")
	}
	contains(t, m.View().Content, "hub key saved", "node reloaded")
	absent(t, m.View().Content, testKey)
}

func TestKeyFormFailureAndCancel(t *testing.T) {
	m, f := fixture(t, false)
	f.err = errors.New("hub.url: ws:// would send the node key unencrypted to hub.example.com")
	m.Update(key("K"))
	m.form.inputs[0].SetValue(testKey)
	action(t, m, "enter")
	if m.form == nil || !strings.Contains(m.form.err, "unencrypted") {
		t.Fatalf("form after failure: %+v", m.form)
	}
	contains(t, m.View().Content, "unencrypted")
	m.Update(key("esc"))
	if m.form != nil {
		t.Fatal("esc did not close the form")
	}

	// Without a valid config there is nothing to put the key in.
	f.snapshot.Config.Exists = false
	inspect(t, m)
	m.Update(key("K"))
	if m.form != nil {
		t.Fatal("key form opened without a config")
	}
}
