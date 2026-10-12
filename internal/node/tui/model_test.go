package tui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/AldrinSalazar/mcp-mesh/internal/discovery"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/service"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/charmbracelet/x/ansi"
)

type fakeNode struct {
	detected         proto.Transport
	detectionErr     error
	hubs             []discovery.Hub
	discoveryErr     error
	discoveryContext context.Context
	discoveryTimeout time.Duration
	mu               sync.Mutex
	snapshot         nodectl.Snapshot
	calls            []string
	added            config.Entry
	hub              string
	key              string
	pairHub          string
	pairName         string
	force            bool
	disabled         bool
	err              error
	reloaded         bool
	logs             map[string]string
	tools            []control.Tool
	events           chan control.Event
	eventContext     context.Context
}

var _ Node = (*fakeNode)(nil)

func (f *fakeNode) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return f.err
}
func (f *fakeNode) Inspect(context.Context) nodectl.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "inspect")
	return f.snapshot
}
func (f *fakeNode) Discover(ctx context.Context, timeout time.Duration) ([]discovery.Hub, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "discover")
	f.discoveryContext, f.discoveryTimeout = ctx, timeout
	return f.hubs, f.discoveryErr
}
func (f *fakeNode) DetectTransport(_ context.Context, url string, _ map[string]string) (proto.Transport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "detect:"+url)
	transport := f.detected
	if transport == "" {
		transport = proto.TransportHTTP
	}
	return transport, f.detectionErr
}
func (f *fakeNode) InitConfig(hub string, force bool) error {
	f.mu.Lock()
	f.hub, f.force = hub, force
	f.mu.Unlock()
	return f.record("init")
}
func (f *fakeNode) Start(context.Context) error { return f.record("start") }
func (f *fakeNode) Stop(context.Context) error  { return f.record("stop") }
func (f *fakeNode) Install(context.Context) (service.State, error) {
	return service.State{}, f.record("install")
}
func (f *fakeNode) Uninstall(context.Context) error { return f.record("uninstall") }
func (f *fakeNode) LogsHint() string                { return "follow the node log" }
func (f *fakeNode) AddMCP(_ context.Context, e config.Entry) (bool, error) {
	f.mu.Lock()
	f.added = e
	f.mu.Unlock()
	return f.reloaded, f.record("add:" + e.Name)
}
func (f *fakeNode) RemoveMCP(_ context.Context, name string) (bool, error) {
	return f.reloaded, f.record("remove:" + name)
}
func (f *fakeNode) SetDisabled(_ context.Context, name string, disabled bool) (bool, error) {
	f.mu.Lock()
	f.disabled = disabled
	f.mu.Unlock()
	return f.reloaded, f.record("disable:" + name)
}
func (f *fakeNode) SetKey(_ context.Context, key string) (nodectl.KeyResult, error) {
	f.mu.Lock()
	f.key = key
	f.mu.Unlock()
	return nodectl.KeyResult{Reloaded: f.reloaded}, f.record("setkey")
}
func (f *fakeNode) Pair(_ context.Context, hub, name string) (nodectl.PairResult, error) {
	f.mu.Lock()
	f.pairHub, f.pairName = hub, name
	f.mu.Unlock()
	return nodectl.PairResult{}, f.record("pair")
}
func (f *fakeNode) Reload(context.Context) error                 { return f.record("reload") }
func (f *fakeNode) Restart(_ context.Context, name string) error { return f.record("restart:" + name) }
func (f *fakeNode) Logs(_ context.Context, name string, n int) ([]byte, error) {
	if n != logBytes {
		panic("incorrect log limit")
	}
	err := f.record("logs:" + name)
	return []byte(f.logs[name]), err
}
func (f *fakeNode) Tools(_ context.Context, name string) ([]control.Tool, error) {
	return f.tools, f.record("tools:" + name)
}
func (f *fakeNode) Events(ctx context.Context) (<-chan control.Event, error) {
	f.mu.Lock()
	f.eventContext = ctx
	f.mu.Unlock()
	return f.events, f.record("events")
}
func (f *fakeNode) EditorCommand() (*exec.Cmd, error) {
	return exec.Command("unused-editor"), f.record("editor")
}
func (f *fakeNode) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

func fixture(t *testing.T, running bool) (*model, *fakeNode) {
	t.Helper()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	f := &fakeNode{events: make(chan control.Event, 10), logs: map[string]string{"fs": "fs stderr\n", "web": "web stderr\n"}, tools: []control.Tool{{Name: "read_file", Description: "Read files\nSecond line"}}}
	f.snapshot = nodectl.Snapshot{Version: "v1", ConfigPath: "/tmp/node.yaml", Config: nodectl.ConfigState{Exists: true, Hub: "ws://hub/node", Node: "work-pc", Entries: []config.Entry{{Name: "fs", Command: "fs-server", Start: "eager"}, {Name: "web", URL: "https://mcp.example.com", Start: "lazy"}}}, Service: nodectl.ServiceState{Supported: true}, Daemon: nodectl.DaemonState{Running: running, Status: control.Status{PID: 1234, StartedAt: now.Add(-3 * time.Minute), Hub: control.HubStatus{URL: "ws://hub/node", Connected: true, Since: now.Add(-5 * time.Minute)}, MCPs: []control.MCPStatus{{Name: "fs", State: control.MCPRunning, Start: "eager", PID: 5678, Tools: 2, StartedAt: now.Add(-time.Minute)}, {Name: "web", State: control.MCPStopped, Start: "lazy"}}}}}
	m := newModel(t.Context(), f)
	m.now = now
	m.width, m.height = 140, 40
	t.Cleanup(m.closeConnection)
	t.Cleanup(m.cancelDiscovery)
	inspect(t, m)
	return m, f
}
func key(s string) tea.KeyPressMsg {
	codes := map[string]rune{"up": tea.KeyUp, "down": tea.KeyDown, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "tab": tea.KeyTab, "enter": tea.KeyEnter, "esc": tea.KeyEscape, "left": tea.KeyLeft, "right": tea.KeyRight, "space": ' '}
	if s == "ctrl+r" {
		return tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
	}
	if s == "ctrl+s" {
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	}
	if s == "ctrl+c" {
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	if s == "shift+tab" {
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	}
	if c, ok := codes[s]; ok {
		return tea.KeyPressMsg{Code: c}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}
func contains(t *testing.T, view string, wants ...string) {
	t.Helper()
	view = ansi.Strip(view)
	for _, want := range wants {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}
func absent(t *testing.T, view string, wants ...string) {
	t.Helper()
	view = ansi.Strip(view)
	for _, want := range wants {
		if strings.Contains(view, want) {
			t.Errorf("view contains %q:\n%s", want, view)
		}
	}
}

// Action batches also contain timer commands. Execute only the work branch;
// tests deliver timer and event messages explicitly without waiting on clocks.
func work(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing command")
	}
	msg := cmd()
	for {
		batch, ok := msg.(tea.BatchMsg)
		if !ok {
			return msg
		}
		if len(batch) == 0 {
			t.Fatal("empty batch")
		}
		msg = batch[0]()
	}
}
func action(t *testing.T, m *model, keyName string) tea.Cmd {
	t.Helper()
	_, cmd := m.Update(key(keyName))
	_, refresh := m.Update(work(t, cmd))
	return refresh
}
func inspect(t *testing.T, m *model) {
	t.Helper()
	_, cmd := m.Update(m.inspect(true)())
	var apply func(tea.Cmd)
	apply = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, child := range batch {
				apply(child)
			}
			return
		}
		m.Update(msg)
	}
	apply(cmd)
}
func connectIfNeeded(m *model) {
	if cmd := m.connect(); cmd != nil {
		m.Update(cmd())
	}
}
func typeText(m *model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestWelcomeAndInit(t *testing.T) {
	m, f := fixture(t, false)
	f.snapshot.Config = nodectl.ConfigState{}
	inspect(t, m)
	contains(t, m.View().Content, "Welcome!", "/tmp/node.yaml", "p  pair with a hub", "n  create the config only", "not created yet")
	m.Update(key("n"))
	contains(t, m.View().Content, "Create config", "Searching", "Enter the URL manually")
	m.Update(key("enter"))
	typeText(m, "hub.example/node")
	refresh := action(t, m, "enter")
	if f.count("init") != 1 || f.hub != "ws://hub.example/node" || f.force {
		t.Fatalf("bad init: %q, %v", f.hub, f.force)
	}
	if m.form != nil {
		t.Fatal("successful form stayed open")
	}
	contains(t, m.View().Content, "config created", "press a to add an MCP or e to edit")
	if refresh == nil {
		t.Fatal("no inspect after action")
	}
}
func TestInvalidConfig(t *testing.T) {
	m, f := fixture(t, false)
	f.snapshot.Config.Err = "node.yaml:4: bad hub URL"
	inspect(t, m)
	contains(t, m.View().Content, "Invalid config", "node.yaml:4", "Press e to fix it")
	_, cmd := m.Update(key("s"))
	if cmd != nil || f.count("start") != 0 {
		t.Fatal("started invalid config")
	}
	contains(t, m.View().Content, "Fix the config with e")
	m.Update(key("i"))
	if m.confirm != "" {
		t.Fatal("install enabled for invalid config")
	}
	m.Update(tickMsg(m.now.Add(20 * time.Second)))
	if !m.noticeError || m.notice == "" {
		t.Fatal("error expired")
	}
}
func TestStartAndInspect(t *testing.T) {
	m, f := fixture(t, false)
	contains(t, m.View().Content, "○ stopped", "Press s to start", "fs", "web")
	refresh := action(t, m, "s")
	if f.count("start") != 1 {
		t.Fatal("Start not called")
	}
	if m.snapshot.Daemon.Running {
		t.Fatal("start guessed node state")
	}
	f.snapshot.Daemon.Running = true
	m.Update(refresh())
	contains(t, m.View().Content, "● running", "until logout", "up 3m", "● connected · 5m", "ws://hub/node")
	f.snapshot.Service.Running = true
	inspect(t, m)
	contains(t, m.View().Content, "● running", "starts at login")
}
func TestConfirmations(t *testing.T) {
	for _, tc := range []struct {
		key, call, prompt string
		running           bool
	}{{"S", "stop", "Stop the node? y/n", true}, {"i", "install", "MCP commands are", false}, {"U", "uninstall", "stops the node", true}, {"x", "remove:fs", "Remove fs? y/n", false}} {
		t.Run(tc.key, func(t *testing.T) {
			m, f := fixture(t, tc.running)
			if tc.key == "U" {
				f.snapshot.Service.Installed = true
				inspect(t, m)
			}
			m.Update(key(tc.key))
			contains(t, m.View().Content, tc.prompt)
			if f.count(tc.call) != 0 {
				t.Fatal("ran before confirmation")
			}
			m.Update(key("n"))
			if f.count(tc.call) != 0 || m.confirm != "" {
				t.Fatal("n confirmed")
			}
			m.Update(key(tc.key))
			action(t, m, "y")
			if f.count(tc.call) != 1 {
				t.Fatal("y did not confirm")
			}
		})
	}
}
func TestUnsupportedServiceAndEmptyConfig(t *testing.T) {
	m, f := fixture(t, false)
	f.snapshot.Service.Supported = false
	f.snapshot.Config.Entries = nil
	inspect(t, m)
	contains(t, m.View().Content, "s start", "No MCPs yet. Press a to add one.")
	absent(t, strings.Join(m.footerLines(m.width, m.footerBudget()), "\n"), "i start at login")
	absent(t, strings.Join(m.guidance(), "\n"), "or i")
	_, cmd := m.Update(key("i"))
	if cmd != nil || m.confirm != "" {
		t.Fatal("unsupported installation offered")
	}
}
func TestAddForm(t *testing.T) {
	m, _ := fixture(t, false)
	m.Update(key("a"))
	m.Update(key("enter"))
	typeText(m, "npx -y @modelcontextprotocol/server-filesystem ~/code")
	if m.form.inputs[1].Value() != "filesystem" {
		t.Fatalf("suggested name: %q", m.form.inputs[1].Value())
	}
	m.Update(key("tab"))
	typeText(m, "-custom")
	name := m.form.inputs[1].Value()
	m.Update(key("shift+tab"))
	typeText(m, " extra")
	if m.form.inputs[1].Value() != name {
		t.Fatal("manual name overwritten")
	}
	m.form.inputs[1].SetValue("fs")
	m.form.focus = 2
	m.Update(key("space"))
	m.form.inputs[3].SetValue("read_file, list_directory")
	entry, err := m.form.entry()
	if err != nil {
		t.Fatal(err)
	}
	if entry.Command != "npx" || !reflect.DeepEqual(entry.Args, []string{"-y", "@modelcontextprotocol/server-filesystem", "~/code", "extra"}) || entry.Start != "lazy" || len(entry.AllowTools) != 2 {
		t.Fatalf("entry: %#v", entry)
	}
	m.form.inputs[1].SetValue("bad name")
	m.form.focus = 3
	_, cmd := m.Update(key("enter"))
	if cmd != nil || m.form == nil || m.form.err == "" {
		t.Fatal("validation closed form")
	}
	contains(t, m.View().Content, "Name must use")
	m.Update(key("esc"))
	if m.form == nil || !m.form.choosing {
		t.Fatal("esc did not return to chooser")
	}
	m.Update(key("esc"))
	if m.form != nil {
		t.Fatal("esc did not cancel")
	}
}
func TestAddURLAndResults(t *testing.T) {
	for _, reload := range []bool{false, true} {
		t.Run(fmt.Sprint(reload), func(t *testing.T) {
			m, f := fixture(t, reload)
			f.reloaded = reload
			m.Update(key("a"))
			m.Update(key("down"))
			m.Update(key("enter"))
			m.form.inputs[0].SetValue("")
			typeText(m, "http://127.0.0.1:8931/mcp")
			if m.form.inputs[1].Value() != "127-0-0-1-8931" {
				t.Fatalf("URL name: %q", m.form.inputs[1].Value())
			}
			m.form.inputs[1].SetValue("fs")
			refresh := action(t, m, "ctrl+s")
			if f.added.URL != "http://127.0.0.1:8931/mcp" || f.added.Command != "" || f.count("add:fs") != 1 {
				t.Fatal("URL not added")
			}
			want := "added fs · press s to start the node"
			if reload {
				want = "added fs · press R to apply"
			}
			contains(t, m.View().Content, want)
			if refresh == nil || m.form != nil {
				t.Fatal("missing refresh or form left open")
			}
		})
	}
}
func TestAddFailureKeepsForm(t *testing.T) {
	m, f := fixture(t, false)
	f.err = errors.New("MCP already configured")
	m.Update(key("a"))
	m.Update(key("down"))
	m.Update(key("enter"))
	m.form.inputs[0].SetValue("")
	typeText(m, "https://mcp.example.com")
	action(t, m, "ctrl+s")
	if m.form == nil {
		t.Fatal("failed add closed form")
	}
	contains(t, m.View().Content, "MCP already configured")
}
func TestSelectedDisableAndRemove(t *testing.T) {
	m, f := fixture(t, false)
	m.Update(key("j"))
	action(t, m, "d")
	if f.count("disable:web") != 1 || !f.disabled {
		t.Fatal("disabled wrong selection")
	}
	f.snapshot.Config.Entries[1].Disabled = true
	inspect(t, m)
	contains(t, m.View().Content, "disabled")
	action(t, m, "d")
	if f.disabled {
		t.Fatal("did not enable")
	}
	m.Update(key("x"))
	contains(t, m.View().Content, "Remove web?")
	action(t, m, "y")
	if f.count("remove:web") != 1 {
		t.Fatal("removed wrong selection")
	}
}
func TestEditorReturn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		running   bool
		configErr string
		editorErr error
		reload    int
		notice    string
	}{{"running", true, "", nil, 0, "config saved · press R to apply"}, {"stopped", false, "", nil, 0, "config saved"}, {"invalid", true, "bad YAML", nil, 0, "the node keeps the previous config"}, {"editor failed", true, "", errors.New("exit status 1"), 0, "editor failed"}} {
		t.Run(tc.name, func(t *testing.T) {
			m, f := fixture(t, tc.running)
			f.snapshot.Config.Err = tc.configErr
			_, open := m.Update(key("e"))
			ready := work(t, open)
			if f.count("editor") != 1 {
				t.Fatal("missing EditorCommand")
			}
			_, execCmd := m.Update(ready)
			if execCmd == nil {
				t.Fatal("editor did not suspend via ExecProcess")
			}
			_, check := m.Update(editorDoneMsg{id: m.actionID, err: tc.editorErr})
			_, refresh := m.Update(work(t, check))
			if f.count("reload") != tc.reload {
				t.Fatal("incorrect reload")
			}
			contains(t, m.View().Content, tc.notice)
			if refresh == nil {
				t.Fatal("editor did not re-inspect")
			}
		})
	}
}
func TestTabsToolsAndSecrets(t *testing.T) {
	m, f := fixture(t, true)
	_, cmd := m.Update(key("tab"))
	m.Update(cmd())
	contains(t, m.View().Content, "tools", "read_file · Read files")
	absent(t, m.View().Content, "Second line")
	if f.count("tools:fs") != 1 {
		t.Fatal("did not request selected tools")
	}
	f.snapshot.Config.Entries[0].Env = map[string]string{"API_TOKEN": "private-env-value"}
	f.snapshot.Config.Entries[0].Headers = map[string]string{"Authorization": "private-header-value"}
	inspect(t, m)
	m.Update(key("tab"))
	contains(t, m.View().Content, "info", "Command       fs-server", "Env           API_TOKEN", "Headers       Authorization")
	absent(t, m.View().Content, "private-env-value", "private-header-value")
	f.snapshot.Daemon.Running = false
	inspect(t, m)
	m.Update(key("tab"))
	m.Update(key("tab"))
	contains(t, m.View().Content, "start the node to see tools")
}
func TestBusyIgnoresKeysAndQuit(t *testing.T) {
	m, _ := fixture(t, true)
	_, cmd := m.Update(key("r"))
	if cmd == nil || !m.busy {
		t.Fatal("restart not busy")
	}
	selected, tab := m.selected, m.tab
	for _, k := range []string{"j", "tab", "a", "e", "d", "x", "S", "R", "?", "f"} {
		_, next := m.Update(key(k))
		if next != nil {
			t.Fatalf("busy accepted %s", k)
		}
	}
	if m.selected != selected || m.tab != tab || m.form != nil || m.help {
		t.Fatal("busy state changed")
	}
	contains(t, m.View().Content, "Restarting fs…")
	frame := m.indicator.View()
	m.Update(spinMsg{m.actionID})
	if m.indicator.View() == frame {
		t.Fatal("spinner did not advance")
	}
	_, quit := m.Update(key("q"))
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatal("q did not quit")
	}
}
func TestNoticeExpiry(t *testing.T) {
	m, f := fixture(t, true)
	action(t, m, "R")
	m.Update(tickMsg(m.now.Add(7 * time.Second)))
	if m.notice != "" {
		t.Fatal("success did not expire")
	}
	f.err = errors.New("rejected")
	action(t, m, "R")
	m.Update(tickMsg(m.now.Add(time.Minute)))
	if m.notice == "" || !m.noticeError {
		t.Fatal("error expired")
	}
}
func TestMergedRowsAndSelection(t *testing.T) {
	m, f := fixture(t, true)
	f.snapshot.Config.Entries = append(f.snapshot.Config.Entries, config.Entry{Name: "disabled", Disabled: true})
	f.snapshot.Daemon.Status.MCPs = append(f.snapshot.Daemon.Status.MCPs, control.MCPStatus{Name: "removed-from-file", State: control.MCPRunning})
	inspect(t, m)
	contains(t, m.View().Content, "disabled", "removed-from-file")
	if len(m.rows) != 4 {
		t.Fatal("did not merge")
	}
	m.selected = 3
	f.snapshot.Config.Entries = f.snapshot.Config.Entries[1:]
	inspect(t, m)
	selected, _ := m.selectedMCP()
	if selected.entry.Name != "web" {
		t.Fatal("selection not preserved by name")
	}
	f.snapshot.Daemon.Running = false
	inspect(t, m)
	if len(m.rows) != 2 {
		t.Fatal("stale node rows after stop")
	}
}
func TestInspectionEpochsAndDebounce(t *testing.T) {
	m, f := fixture(t, true)
	connectIfNeeded(m)
	event := eventMsg{epoch: m.epoch, event: control.Event{Kind: control.EventStatus}}
	calls := f.count("inspect")
	for i := 0; i < 20; i++ {
		m.Update(event)
	}
	if !m.refreshScheduled || f.count("inspect") != calls {
		t.Fatal("events not debounced")
	}
	_, cmd := m.Update(refreshMsg{m.epoch})
	for i := 0; i < 20; i++ {
		m.Update(event)
	}
	if !m.inspectDirty {
		t.Fatal("in-flight events lost")
	}
	m.Update(cmd())
	if !m.refreshScheduled || f.count("inspect") != calls+1 {
		t.Fatal("no coalesced follow-up")
	}
	old := m.inspect(true)
	newer := m.inspect(true)
	m.Update(newer())
	epoch := m.inspectEpoch
	stale := old().(inspectMsg)
	stale.snapshot.Config.Node = "stale"
	m.Update(stale)
	if m.inspectEpoch != epoch || m.snapshot.Config.Node == "stale" || m.inspectPending {
		t.Fatal("stale inspect applied")
	}
	// An action must invalidate an inspection started before its completion.
	old = m.inspect(true)
	action(t, m, "R")
	m.Update(old())
	if !m.inspectPending {
		t.Fatal("old inspection completed current request")
	}
}
func TestLogsEventsFollowAndStaleReplies(t *testing.T) {
	m, _ := fixture(t, true)
	connectIfNeeded(m)
	old := m.loadDetail()
	_, current := m.Update(key("j"))
	m.Update(old())
	if !m.logsPending || m.logs != "" {
		t.Fatal("stale log reply applied")
	}
	m.Update(eventMsg{epoch: m.epoch, event: control.Event{Kind: control.EventLog, MCP: "web", Data: "live\n"}})
	m.Update(current())
	if m.logs != "web stderr\nlive\n" {
		t.Fatalf("lost pending event: %q", m.logs)
	}
	m.setLogs(strings.Repeat("line\n", 100))
	if !m.viewport.AtBottom() {
		t.Fatal("follow not at bottom")
	}
	m.Update(key("pgup"))
	offset := m.viewport.YOffset()
	if m.follow || m.viewport.AtBottom() {
		t.Fatal("page up did not stop follow and scroll")
	}
	m.Update(eventMsg{epoch: m.epoch, event: control.Event{Kind: control.EventLog, MCP: "web", Data: "new\n"}})
	if m.viewport.YOffset() != offset {
		t.Fatal("append moved non-following viewport")
	}
	m.Update(key("f"))
	if !m.viewport.AtBottom() {
		t.Fatal("follow did not jump to bottom")
	}
	for _, s := range []string{strings.Repeat("界", logBytes), strings.Repeat("\xffa", logBytes)} {
		m.setLogs(s)
		if len(m.logs) > logBytes || !utf8.ValidString(m.logs) {
			t.Fatal("invalid or unbounded tail")
		}
	}
	_, tools := m.Update(key("tab"))
	_, info := m.Update(key("tab"))
	if info != nil {
		t.Fatal("info called node")
	}
	m.Update(tools())
	if m.tab != 2 || len(m.tools) != 0 {
		t.Fatal("stale tools applied after tab switch")
	}
}
func TestEventStreamLifecycle(t *testing.T) {
	m, f := fixture(t, true)
	connectIfNeeded(m)
	ctx := f.eventContext
	if f.count("events") != 1 {
		t.Fatal("no stream")
	}
	wait := m.waitEvent()
	oldEpoch := m.epoch
	f.snapshot.Daemon.Running = false
	inspect(t, m)
	if ctx.Err() == nil || m.events != nil {
		t.Fatal("stopped node retained stream")
	}
	if !wait().(eventMsg).closed {
		t.Fatal("event wait did not cancel")
	}
	m.Update(eventMsg{epoch: oldEpoch, event: control.Event{Kind: control.EventLog, MCP: "fs", Data: "stale"}})
	if m.logs != "" {
		t.Fatal("old event applied")
	}
	f.snapshot.Daemon.Running = true
	inspect(t, m)
	connectIfNeeded(m)
	f.events <- control.Event{Kind: control.EventLog, MCP: "fs", Data: "live"}
	m.Update(m.waitEvent()())
	close(f.events)
	_, refresh := m.Update(m.waitEvent()())
	if refresh == nil || m.events != nil {
		t.Fatal("stream closure did not poll")
	}
	// An unavailable stream leaves inspection usable, and the next snapshot retries.
	f.err = errors.New("socket gone")
	connectIfNeeded(m)
	if m.events != nil || m.connecting {
		t.Fatal("failed handshake stuck")
	}
	f.err = nil
	f.events = make(chan control.Event)
	inspect(t, m)
	connectIfNeeded(m)
	_, quit := m.Update(key("ctrl+c"))
	if _, ok := quit().(tea.QuitMsg); !ok || f.eventContext.Err() == nil {
		t.Fatal("quit leaked subscription")
	}
	if after(f.eventContext, time.Hour, func(time.Time) tea.Msg { return "tick" })() != nil {
		t.Fatal("timer ignored cancellation")
	}
}
func TestNarrowAndShortTerminals(t *testing.T) {
	for _, size := range []struct{ w, h int }{{140, 40}, {80, 24}, {50, 18}, {30, 12}, {14, 8}, {1, 1}, {0, 0}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			m, _ := fixture(t, true)
			for _, mode := range []string{"dashboard", "help", "add", "launch", "connect", "init"} {
				m.help = false
				m.form = nil
				switch mode {
				case "help":
					m.help = true
				case "add":
					m.openForm(true)
				case "launch", "connect":
					m.openForm(true)
					if mode == "connect" {
						m.Update(key("down"))
					}
					m.Update(key("enter"))
				case "init":
					m.openForm(false)
				}
				m.setLogs(strings.Repeat("wide 界 stderr\n", 20))
				m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
				view := m.View().Content
				if lipgloss.Height(view) > max(1, size.h) {
					t.Fatalf("%s view too tall", mode)
				}
				for _, line := range strings.Split(view, "\n") {
					if lipgloss.Width(line) > max(1, size.w) {
						t.Fatalf("%s line too wide: %q", mode, line)
					}
				}
			}
		})
	}
}
func TestTerminalTextPalettePathsAndHelp(t *testing.T) {
	m, f := fixture(t, true)
	m.setLogs("\x1b[31mred\x1b[0m\r\nline\tend\x07\x1b]0;bad title\x07")
	contains(t, m.View().Content, "red", "line    end")
	if strings.ContainsAny(m.viewport.GetContent(), "\x1b\x07") {
		t.Fatal("unsafe terminal controls")
	}
	if singleLine("bad\nname") != "bad name" {
		t.Fatal("multiline name")
	}
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	light := m.stateColor(control.MCPRunning)
	m.Update(tea.BackgroundColorMsg{Color: color.Black})
	if reflect.DeepEqual(light, m.stateColor(control.MCPRunning)) {
		t.Fatal("palette unchanged")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	f.snapshot.ConfigPath = filepath.Join(home, ".config", "mcp-mesh", "node.yaml")
	inspect(t, m)
	contains(t, m.View().Content, "~"+string(filepath.Separator)+".config")
	m.Update(key("?"))
	contains(t, m.View().Content, "Help", "the node keeps running", "MCP commands are")
	m.Update(key("?"))
	if m.help {
		t.Fatal("help did not close")
	}
	f.snapshot.Daemon.Status.Hub.Connected = false
	f.snapshot.Daemon.Status.Hub.LastError = "connection refused"
	inspect(t, m)
	contains(t, m.View().Content, "disconnected: connection refused", "keeps retrying")
}

func TestFormValidation(t *testing.T) {
	for _, tc := range []struct{ line, name, tools, want string }{
		{"", "fs", "", "Command is required"},
		{"http://", "fs", "", "absolute http or https URL"},
		{"server 'unterminated", "fs", "", "quote"},
		{"server", "bad name", "", "Name must use"},
		{"server", "fs", "read,,write", "empty name"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			m, f := fixture(t, false)
			m.Update(key("a"))
			if isURL(tc.line) {
				m.Update(key("down"))
			}
			m.Update(key("enter"))
			m.form.inputs[0].SetValue(tc.line)
			m.form.inputs[1].SetValue(tc.name)
			m.form.inputs[3].SetValue(tc.tools)
			_, cmd := m.Update(key("ctrl+s"))
			if cmd != nil || m.form == nil || f.count("add:fs") != 0 {
				t.Fatal("invalid form submitted")
			}
			contains(t, m.View().Content, tc.want)
		})
	}
	m, f := fixture(t, false)
	f.snapshot.Config.Exists = false
	inspect(t, m)
	m.Update(key("n"))
	m.Update(key("enter"))
	_, cmd := m.Update(key("enter"))
	if cmd != nil || m.form == nil || f.count("init") != 0 {
		t.Fatal("invalid hub submitted")
	}
	contains(t, m.View().Content, "absolute ws or wss URL")
}

func TestShortHelpAndConfirmation(t *testing.T) {
	m, _ := fixture(t, false)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m.Update(key("?"))
	for i := 0; i < 10; i++ {
		m.Update(key("pgdown"))
	}
	contains(t, m.View().Content, "Node logs:")
	for i := 0; i < 10; i++ {
		m.Update(key("pgup"))
	}
	contains(t, m.View().Content, "The node runs your MCPs", "Node")
	m.Update(key("pgdown"))
	contains(t, m.View().Content, "Start node")
	m.Update(key("?"))
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(key("i"))
	contains(t, m.View().Content, "MCP commands are", "If the node is running now", "Start the node at login? y/n")
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 12})
	contains(t, m.View().Content, "y/n")
}

func TestInitialInspectAndPeriodicPolling(t *testing.T) {
	f := &fakeNode{}
	m := newModel(t.Context(), f)
	t.Cleanup(m.closeConnection)
	t.Cleanup(m.cancelDiscovery)
	init := m.Init()
	if f.count("inspect") != 0 {
		t.Fatal("Inspect called outside Cmd")
	}
	m.Update(work(t, init))
	if f.count("inspect") != 1 {
		t.Fatal("missing initial inspection")
	}
	_, cmd := m.Update(tickMsg(m.now.Add(2 * time.Second)))
	m.Update(work(t, cmd))
	if f.count("inspect") != 2 || f.count("events") != 0 {
		t.Fatal("stopped dashboard did not poll independently of events")
	}
	contains(t, m.View().Content, "n  create the config only")
}

func TestUpdateNotices(t *testing.T) {
	m, f := fixture(t, true)
	absent(t, m.View().Content, "available", "Upgrade with", "still runs")
	f.snapshot.Update = nodectl.UpdateState{Latest: "v2", URL: "https://example/v2", Upgrade: "brew upgrade mcp-mesh-node", Running: "v0.9"}
	inspect(t, m)
	contains(t, m.View().Content, "↑ v2 available · v1", "Update available: mcp-mesh-node v2. Upgrade with brew upgrade mcp-mesh-node, then restart the node (S, then s).",
		"The node still runs v0.9. Restart it to use this v1 (S, then s).")
	f.snapshot.Update = nodectl.UpdateState{Hub: "v3", Upgrade: "brew upgrade mcp-mesh-node"}
	inspect(t, m)
	contains(t, m.View().Content, "The hub runs v3, newer than this node. Upgrade with brew upgrade mcp-mesh-node")
	absent(t, m.View().Content, "available")
}
