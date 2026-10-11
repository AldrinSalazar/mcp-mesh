//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/e2e/mesh"
)

// A node machine loses power during a long call. The client must learn
// within the heartbeat window, not at the call timeout: the hub pings every
// 20s and closes after two missed pongs (10s each), so up to about 50s.
// When the machine boots, the node's user service reconnects on its own and
// calls work again.
func TestNodePowerLossMidCall(t *testing.T) {
	e := newEnv(t, 15*time.Minute, "hub", "node-a")
	e.startHub(mesh.HubOptions{})
	m := e.machine("node-a")
	// Nobody logs in to this machine: the node starts at boot.
	e.joinNodeWith("node-a", "install", "--at-boot")
	e.waitMCPReady("node-a", "fake", 2*time.Minute)
	s := e.client("power")

	call := e.startCall(s, "node-a__fake__slow", map[string]any{"seconds": 90})
	e.waitInFlight("node-a", "fake", 1)
	cut := time.Now()
	if err := m.PowerOff(e.ctx); err != nil {
		t.Fatal(err)
	}
	r := <-call
	noticed := time.Since(cut)
	t.Logf("client learned after %s: err=%v isError=%v %q", noticed.Round(time.Second), r.err, r.isError, r.text)
	if r.err == nil && !r.isError {
		t.Fatalf("the call succeeded although its machine lost power")
	}
	if noticed > 65*time.Second {
		t.Fatalf("the client waited %s; the heartbeat should notice within about 50s", noticed.Round(time.Second))
	}

	if err := m.Boot(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.waitMCPReady("node-a", "fake", 3*time.Minute)
	e.call(s, "node-a__fake__render", map[string]any{"scene": "after power loss"})
}

// A machine nobody logs in to (a server): a plain install says the node
// will wait for a login after a reboot and how to avoid it; with
// install --at-boot, the node is back after a reboot without any login.
func TestNodeRebootHeadless(t *testing.T) {
	e := newEnv(t, 15*time.Minute, "hub", "node-a")
	e.startHub(mesh.HubOptions{})
	n := e.joinNodeWith("node-a", "install")
	e.waitMCPReady("node-a", "fake", 2*time.Minute)
	out, err := n.Run(e.ctx, "install")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "install --at-boot") {
		t.Fatalf("install doesn't say how to start at boot:\n%s", out)
	}
	if out, err = n.Run(e.ctx, "install", "--at-boot"); err != nil || !strings.Contains(out, "start when the machine boots") {
		t.Fatalf("install --at-boot: %v\n%s", err, out)
	}

	m := e.machine("node-a")
	if err := m.Shutdown(e.ctx); err != nil {
		t.Fatal(err)
	}
	// No SSH: a login would start the user's services and hide the answer.
	if err := m.BootWithoutLogin(e.ctx, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	err = mesh.Eventually(e.ctx, 2*time.Minute, func() error {
		ns, err := e.hub.Nodes(e.ctx)
		if err != nil {
			return err
		}
		for _, x := range ns {
			if x.Name == "node-a" && x.Online {
				return nil
			}
		}
		return errString("node-a is not back online")
	})
	if err != nil {
		t.Fatalf("after a reboot without a login: %v", err)
	}
}

// The hub restarts (an upgrade) and crashes (killed, restarted by systemd).
// Each time, the node reconnects by itself and a client can call again.
func TestHubRestartAndCrash(t *testing.T) {
	e := newEnv(t, 15*time.Minute, "hub", "node-a")
	h := e.startHub(mesh.HubOptions{})
	e.joinNode("node-a")
	e.waitMCPReady("node-a", "fake", 2*time.Minute)
	key, err := h.CreateKey(e.ctx, "client", "restarts")
	if err != nil {
		t.Fatal(err)
	}

	for _, how := range []struct{ name, cmd string }{
		{"restart", "sudo systemctl restart mcp-mesh-hub"},
		{"crash", "sudo systemctl kill -s KILL mcp-mesh-hub"},
	} {
		if out, err := h.M.Run(e.ctx, how.cmd); err != nil {
			t.Fatalf("%s: %v\n%s", how.name, err, out)
		}
		start := time.Now()
		if err := h.WaitReady(e.ctx, time.Minute); err != nil {
			t.Fatalf("after %s: %v", how.name, err)
		}
		e.waitMCPReady("node-a", "fake", 90*time.Second)
		t.Logf("%s: hub ready and node back after %s", how.name, time.Since(start).Round(time.Second))
		err := mesh.Eventually(e.ctx, 30*time.Second, func() error {
			s, err := h.Client(e.ctx, key)
			if err != nil {
				return err
			}
			defer s.Close()
			r := <-e.startCall(s, "node-a__fake__render", map[string]any{"scene": how.name})
			if r.err != nil || r.isError {
				return errString(r.text)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("calling after %s: %v", how.name, err)
		}
	}
}

// A slow home uplink (60ms ±10ms, 0.2% loss, 20 Mbit/s) between the node
// and the hub: an 8 MB result arrives in one piece, and a small call on the
// same node isn't stuck behind it. Chunking (protocol v2) interleaves the
// frames; the kernel's socket buffer still queues what was already written.
func TestSlowUplinkBigResult(t *testing.T) {
	e := newEnv(t, 15*time.Minute, "hub", "node-a")
	e.startHub(mesh.HubOptions{})
	m := e.machine("node-a")
	e.joinNode("node-a")
	e.waitMCPReady("node-a", "fake", 2*time.Minute)
	s := e.client("uplink")
	if out, err := m.Run(e.ctx, "sudo tc qdisc add dev eth0 root netem delay 60ms 10ms loss 0.2% rate 20mbit"); err != nil {
		t.Fatalf("netem: %v\n%s", err, out)
	}
	quiet := e.call(s, "node-a__fake__render", map[string]any{"scene": "idle link"})

	big := e.startCall(s, "node-a__fake__big", map[string]any{"mb": 8})
	time.Sleep(2 * time.Second)
	small := <-e.startCall(s, "node-a__fake__render", map[string]any{"scene": "beside a big result"})
	r := <-big
	t.Logf("idle small call %s; 8 MB took %s; small call during it %s (err=%v)",
		quiet.took.Round(10*time.Millisecond), r.took.Round(100*time.Millisecond), small.took.Round(10*time.Millisecond), small.err)
	if r.err != nil || r.isError {
		t.Fatalf("big call: err=%v %.200q", r.err, r.text)
	}
	if !strings.Contains(r.text, "8388608") && len(r.text) < 8<<20 {
		t.Fatalf("big result is %d bytes: %.200q", len(r.text), r.text)
	}
	if small.err != nil || small.isError {
		t.Fatalf("small call during the transfer: err=%v %q", small.err, small.text)
	}
	if small.took > 10*time.Second {
		t.Errorf("a small call took %s next to a big result; chunking should interleave them", small.took.Round(100*time.Millisecond))
	}
}

type errString string

func (e errString) Error() string { return string(e) }
