package tui

import (
	"strings"
	"testing"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/upstream"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/charmbracelet/x/ansi"
)

func TestKindChooser(t *testing.T) {
	m, _ := fixture(t, false)
	m.Update(key("a"))
	if m.form == nil || !m.form.choosing {
		t.Fatal("missing chooser")
	}
	contains(t, m.View().Content, "Launch a command", "Connect to a running server", "port stays private")
	m.Update(key("down"))
	m.Update(key("enter"))
	if !m.form.connect || !m.form.lazy || m.form.inputs[0].Value() != "http://127.0.0.1:" {
		t.Fatal("connect defaults")
	}
	contains(t, m.View().Content, "URL", "[lazy]")
	m.Update(key("esc"))
	if !m.form.choosing {
		t.Fatal("form did not return to chooser")
	}
	m.Update(key("up"))
	m.Update(key("enter"))
	if m.form.connect || m.form.lazy || m.form.inputs[0].Value() != "" {
		t.Fatal("launch defaults")
	}
	contains(t, m.View().Content, "Command", "[eager]")
	m.Update(key("esc"))
	m.Update(key("esc"))
	if m.form != nil {
		t.Fatal("chooser did not close")
	}
}

func TestConnectDetection(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "detected", true: "closed app"}[fallback], func(t *testing.T) {
			m, f := fixture(t, false)
			f.detected = proto.TransportSSE
			if fallback {
				f.detectionErr = upstream.ErrUnreachable
			}
			m.Update(key("a"))
			m.Update(key("down"))
			m.Update(key("enter"))
			m.form.inputs[0].SetValue("http://127.0.0.1:9876/sse")
			m.form.inputs[1].SetValue("blender")
			_, cmd := m.Update(key("ctrl+s"))
			if !m.busy || f.count("detect:http://127.0.0.1:9876/sse") != 0 {
				t.Fatal("detection ran on Update")
			}
			contains(t, m.View().Content, "Checking the server…")
			result := work(t, cmd)
			m.Update(result)
			if f.added.Transport != "sse" || f.added.Start != "lazy" || f.count("detect:http://127.0.0.1:9876/sse") != 1 || f.count("add:blender") != 1 {
				t.Fatalf("entry: %+v", f.added)
			}
			if m.noticeError || m.form != nil {
				t.Fatal("connect was not saved")
			}
			if fallback {
				contains(t, m.notice, "Couldn't reach", "saved as sse because the path ends in /sse")
			}
		})
	}
}

func TestWaitingForApp(t *testing.T) {
	m, f := fixture(t, true)
	f.snapshot.Config.Entries[1].Transport = "sse"
	f.snapshot.Daemon.Status.MCPs[1].Unreachable = true
	f.snapshot.Daemon.Status.MCPs[1].Transport = "sse"
	f.snapshot.Daemon.Status.MCPs[1].State = control.MCPBackoff
	inspect(t, m)
	m.Update(key("j"))
	contains(t, ansi.Strip(strings.Join(m.tableLines(), "\n")), "connect (sse)", "◌ waiting for app")
	m.tab = 2
	contains(t, ansi.Strip(m.detailText()), "Kind", "connect", "URL", "Protocol", "HTTP+SSE")
}
