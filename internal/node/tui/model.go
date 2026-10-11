package tui

import (
	"context"
	"errors"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
	"github.com/charmbracelet/x/ansi"
)

const (
	logBytes        = 64 << 10
	refreshInterval = 2 * time.Second
	requestTimeout  = 5 * time.Second
	actionTimeout   = time.Minute
	statusDebounce  = 150 * time.Millisecond
)

type inspectMsg struct {
	epoch    int
	snapshot nodectl.Snapshot
	hint     string
}
type connectedMsg struct {
	epoch  int
	events <-chan control.Event
	err    error
}
type logsMsg struct {
	selection int
	data      []byte
	err       error
}
type toolsMsg struct {
	selection int
	tools     []control.Tool
	err       error
}
type eventMsg struct {
	epoch  int
	event  control.Event
	closed bool
}
type actionMsg struct {
	id      int
	label   string
	failure string
	err     error
	form    bool
	// selectName is selected once the next snapshot lists it (a new MCP).
	selectName string
}
type editorReadyMsg struct {
	id      int
	command *exec.Cmd
	err     error
}
type editorDoneMsg struct {
	id  int
	err error
}
type refreshMsg struct{ epoch int }
type tickMsg time.Time
type spinMsg struct{ id int }

type row struct {
	entry      config.Entry
	configured bool
	live       control.MCPStatus
}

type model struct {
	ctx                                            context.Context
	node                                           Node
	snapshot                                       nodectl.Snapshot
	inspected                                      bool
	logsHint                                       string
	rows                                           []row
	connectionCtx                                  context.Context
	connectionCancel                               context.CancelFunc
	epoch, inspectEpoch                            int
	events                                         <-chan control.Event
	connecting                                     bool
	inspectPending, inspectDirty, refreshScheduled bool
	selected, selection, tab                       int
	selectName                                     string
	logsPending                                    bool
	logs, pendingLogs                              string
	tools                                          []control.Tool
	detailError                                    string
	detailPending                                  bool
	viewport                                       viewport.Model
	follow, dark                                   bool
	width, height                                  int
	now                                            time.Time
	busy                                           bool
	actionID                                       int
	notice                                         string
	noticeError                                    bool
	noticeUntil                                    time.Time
	confirm, confirmName                           string
	help                                           bool
	helpOffset                                     int
	form                                           *form
	indicator                                      spinner.Model
	discoveryID                                    int
	discoveryCancel                                context.CancelFunc
}

func newModel(ctx context.Context, node Node) *model {
	return &model{ctx: ctx, node: node, viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(10)),
		indicator: spinner.New(spinner.WithSpinner(spinner.MiniDot)), follow: true, dark: true, width: 80, height: 24, now: time.Now()}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.inspect(false), tick(m.ctx), tea.RequestBackgroundColor)
}

func after(ctx context.Context, delay time.Duration, msg func(time.Time) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case now := <-timer.C:
			return msg(now)
		case <-ctx.Done():
			return nil
		}
	}
}
func tick(ctx context.Context) tea.Cmd {
	return after(ctx, refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) inspect(force bool) tea.Cmd {
	if m.inspectPending && !force {
		m.inspectDirty = true
		return nil
	}
	m.inspectEpoch++
	m.inspectPending, m.inspectDirty = true, false
	ctx, node, epoch := m.ctx, m.node, m.inspectEpoch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		return inspectMsg{epoch: epoch, snapshot: node.Inspect(ctx), hint: node.LogsHint()}
	}
}

func (m *model) closeConnection() {
	if m.connectionCancel != nil {
		m.connectionCancel()
		m.connectionCancel = nil
	}
	m.epoch++
	m.events, m.connecting, m.refreshScheduled = nil, false, false
}

func (m *model) connect() tea.Cmd {
	if !m.snapshot.Daemon.Running || m.connecting || m.events != nil || m.ctx.Err() != nil {
		return nil
	}
	m.closeConnection()
	m.connectionCtx, m.connectionCancel = context.WithCancel(m.ctx)
	m.connecting = true
	ctx, cancel, node, epoch := m.connectionCtx, m.connectionCancel, m.node, m.epoch
	return func() tea.Msg {
		// Only the handshake has a deadline; the subscription lives until the
		// daemon disappears or the dashboard closes.
		deadline := time.AfterFunc(requestTimeout, cancel)
		events, err := node.Events(ctx)
		deadline.Stop()
		if err == nil {
			err = ctx.Err()
		}
		if err == nil && events == nil {
			err = errors.New("node returned no event stream")
		}
		return connectedMsg{epoch: epoch, events: events, err: err}
	}
}
func (m *model) waitEvent() tea.Cmd {
	ctx, events, epoch := m.connectionCtx, m.events, m.epoch
	return func() tea.Msg {
		select {
		case event, ok := <-events:
			return eventMsg{epoch: epoch, event: event, closed: !ok}
		case <-ctx.Done():
			return eventMsg{epoch: epoch, closed: true}
		}
	}
}
func (m *model) scheduleInspect() tea.Cmd {
	if m.inspectPending {
		m.inspectDirty = true
		return nil
	}
	if m.refreshScheduled {
		return nil
	}
	m.refreshScheduled = true
	epoch := m.epoch
	return after(m.ctx, statusDebounce, func(time.Time) tea.Msg { return refreshMsg{epoch} })
}

func (m *model) selectedMCP() (row, bool) {
	if m.selected < 0 || m.selected >= len(m.rows) {
		return row{}, false
	}
	return m.rows[m.selected], true
}
func (m *model) mergeRows() {
	byName := make(map[string]row)
	for _, e := range m.snapshot.Config.Entries {
		byName[e.Name] = row{entry: e, configured: true}
	}
	if m.snapshot.Daemon.Running {
		for _, s := range m.snapshot.Daemon.Status.MCPs {
			r := byName[s.Name]
			if !r.configured {
				r.entry = config.Entry{Name: s.Name, Start: s.Start, AllowTools: s.AllowTools}
			}
			r.live = s
			byName[s.Name] = r
		}
	}
	m.rows = nil
	for _, r := range byName {
		m.rows = append(m.rows, r)
	}
	sort.Slice(m.rows, func(i, j int) bool { return m.rows[i].entry.Name < m.rows[j].entry.Name })
}

func (m *model) loadDetail() tea.Cmd {
	m.selection++
	m.logs, m.pendingLogs, m.detailError = "", "", ""
	m.logsPending, m.detailPending, m.tools = false, false, nil
	m.viewport.GotoTop()
	r, ok := m.selectedMCP()
	if !ok || !m.snapshot.Daemon.Running || m.tab == 2 {
		m.setDetail()
		return nil
	}
	m.detailPending = true
	m.logsPending = m.tab == 0
	ctx, node, selection, name, tab := m.ctx, m.node, m.selection, r.entry.Name, m.tab
	m.setDetail()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		if tab == 1 {
			tools, err := node.Tools(ctx, name)
			return toolsMsg{selection: selection, tools: tools, err: err}
		}
		data, err := node.Logs(ctx, name, logBytes)
		return logsMsg{selection: selection, data: data, err: err}
	}
}
func tail(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) > logBytes {
		s = s[len(s)-logBytes:]
		for len(s) > 0 && !utf8.RuneStart(s[0]) {
			s = s[1:]
		}
	}
	return s
}
func (m *model) setLogs(s string) { m.logs = tail(s); m.setDetail() }
func (m *model) setDetail() {
	// Pre-wrap once to avoid repeated viewport scans of long stderr lines.
	m.viewport.SetContent(ansi.Hardwrap(m.detailText(), max(1, m.width-4), true))
	if m.follow && m.tab == 0 {
		m.viewport.GotoBottom()
	}
}
func (m *model) setNotice(s string, err bool) {
	m.notice, m.noticeError, m.noticeUntil = s, err, time.Time{}
	if !err {
		m.noticeUntil = m.now.Add(6 * time.Second)
	}
}

func (m *model) begin(label string, work func(context.Context) actionMsg) tea.Cmd {
	m.busy = true
	m.actionID++
	m.setNotice(label, false)
	id, ctx := m.actionID, m.ctx
	return tea.Batch(func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, actionTimeout)
		defer cancel()
		result := work(ctx)
		result.id = id
		return result
	}, m.spinner())
}
func (m *model) spinner() tea.Cmd {
	id := m.actionID
	return after(m.ctx, 120*time.Millisecond, func(time.Time) tea.Msg { return spinMsg{id} })
}

func (m *model) handleInspect(msg inspectMsg) tea.Cmd {
	var cmd tea.Cmd
	if msg.epoch != m.inspectEpoch {
		return nil
	}
	m.inspectPending = false
	old, _ := m.selectedMCP()
	previous := m.snapshot.Daemon
	m.snapshot, m.logsHint, m.inspected = msg.snapshot, msg.hint, true
	changedDaemon := previous.Running != msg.snapshot.Daemon.Running || previous.Status.PID != msg.snapshot.Daemon.Status.PID
	if changedDaemon {
		m.closeConnection()
	}
	m.mergeRows()
	m.selected = min(m.selected, max(0, len(m.rows)-1))
	for i, r := range m.rows {
		if r.entry.Name == old.entry.Name {
			m.selected = i
			break
		}
	}
	for i, r := range m.rows {
		if m.selectName != "" && r.entry.Name == m.selectName {
			m.selected, m.selectName = i, ""
			break
		}
	}
	current, _ := m.selectedMCP()
	stream := m.connect()
	if changedDaemon || old.entry.Name != current.entry.Name {
		cmd = m.loadDetail()
	} else {
		m.setDetail()
	}
	cmd = tea.Batch(cmd, stream)
	if m.inspectDirty {
		m.inspectDirty = false
		cmd = tea.Batch(cmd, m.scheduleInspect())
	}
	return cmd
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		if m.form != nil {
			m.form.setDark(m.dark)
		}
		m.setDetail()
	case tickMsg:
		m.now = time.Time(msg)
		if !m.busy && !m.noticeUntil.IsZero() && !m.now.Before(m.noticeUntil) {
			m.notice = ""
		}
		cmd = tea.Batch(m.inspect(false), tick(m.ctx))
	case spinMsg:
		if m.busy && msg.id == m.actionID {
			m.indicator, _ = m.indicator.Update(spinner.TickMsg{})
			cmd = m.spinner()
		}
	case discoveryMsg:
		if msg.id == m.discoveryID && m.form != nil && m.form.picking {
			f := m.form
			f.searching, f.hubs, f.discoveryError = false, msg.hubs, ""
			if msg.err != nil {
				f.discoveryError = msg.err.Error()
			}
			f.choice = len(f.hubs)
			for i, h := range f.hubs {
				if compatible(h) {
					f.choice = i
					break
				}
			}
			m.cancelDiscovery()
		}
	case discoverySpinMsg:
		if msg.id == m.discoveryID && m.form != nil && m.form.searching {
			m.indicator, _ = m.indicator.Update(spinner.TickMsg{})
			cmd = m.discoverySpinner()
		}
	case inspectMsg:
		cmd = m.handleInspect(msg)
	case connectedMsg:
		if msg.epoch != m.epoch {
			break
		}
		m.connecting = false
		if msg.err != nil {
			m.closeConnection()
			break
		}
		m.events = msg.events
		cmd = m.waitEvent()
	case refreshMsg:
		if msg.epoch == m.epoch {
			m.refreshScheduled = false
			cmd = m.inspect(false)
		}
	case logsMsg:
		if msg.selection != m.selection {
			break
		}
		m.logsPending, m.detailPending = false, false
		if msg.err != nil {
			m.detailError = msg.err.Error()
			m.setDetail()
			cmd = m.scheduleInspect()
			break
		}
		m.setLogs(string(msg.data) + m.pendingLogs)
		m.pendingLogs = ""
	case toolsMsg:
		if msg.selection != m.selection {
			break
		}
		m.detailPending, m.tools = false, msg.tools
		if msg.err != nil {
			m.detailError = msg.err.Error()
			cmd = m.scheduleInspect()
		}
		m.setDetail()
	case eventMsg:
		if msg.epoch != m.epoch {
			break
		}
		if msg.closed {
			m.closeConnection()
			cmd = m.inspect(true)
			break
		}
		if msg.event.Kind == control.EventStatus {
			cmd = m.scheduleInspect()
		}
		if r, ok := m.selectedMCP(); ok && m.tab == 0 && msg.event.Kind == control.EventLog && msg.event.MCP == r.entry.Name {
			if m.logsPending {
				m.pendingLogs = tail(m.pendingLogs + msg.event.Data)
			} else {
				m.setLogs(m.logs + msg.event.Data)
			}
		}
		cmd = tea.Batch(cmd, m.waitEvent())
	case actionMsg:
		if msg.id != m.actionID {
			break
		}
		m.busy = false
		if msg.err != nil {
			failure := msg.failure
			if failure == "" {
				failure = msg.label
			}
			m.setNotice(failure+": "+msg.err.Error(), true)
			if msg.form && m.form != nil {
				m.form.err = msg.err.Error()
			}
		} else {
			m.setNotice(msg.label, false)
			if msg.form {
				m.form = nil
			}
			m.selectName = msg.selectName
		}
		cmd = m.inspect(true)
	case editorReadyMsg:
		if msg.id != m.actionID {
			break
		}
		if msg.err != nil || msg.command == nil {
			if msg.err == nil {
				msg.err = errors.New("editor command is empty")
			}
			return m.Update(actionMsg{id: msg.id, label: "open editor", err: msg.err})
		}
		id := msg.id
		cmd = tea.ExecProcess(msg.command, func(err error) tea.Msg { return editorDoneMsg{id: id, err: err} })
	case editorDoneMsg:
		if msg.id != m.actionID {
			break
		}
		node := m.node
		cmd = m.begin("Checking config…", func(ctx context.Context) actionMsg {
			if msg.err != nil {
				return actionMsg{label: "editor failed", err: msg.err}
			}
			s := node.Inspect(ctx)
			if s.Config.Err != "" {
				return actionMsg{label: "config invalid (the node keeps the previous config)", err: errors.New(s.Config.Err)}
			}
			if !s.Config.Exists {
				return actionMsg{label: "config was removed", err: errors.New("create it with n")}
			}
			if s.Daemon.Running {
				return actionMsg{label: "config saved · press R to apply"}
			}
			return actionMsg{label: "config saved · press s to start the node"}
		})
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" || (msg.String() == "q" && m.form == nil) {
			m.closeConnection()
			m.cancelDiscovery()
			return m, tea.Quit
		}
		if m.busy {
			if msg.String() == "q" {
				m.closeConnection()
				m.cancelDiscovery()
				return m, tea.Quit
			}
			break
		}
		if m.form != nil {
			cmd = m.updateForm(msg)
			break
		}
		if m.confirm != "" {
			action, name := m.confirm, m.confirmName
			m.confirm, m.confirmName = "", ""
			if msg.String() == "y" {
				cmd = m.confirmed(action, name)
			} else {
				m.notice = ""
			}
			break
		}
		if msg.String() == "?" {
			m.help = !m.help
			m.helpOffset = 0
			break
		}
		if m.help {
			switch msg.String() {
			case "esc":
				m.help = false
			case "pgup", "up", "k":
				m.helpOffset = max(0, m.helpOffset-max(1, m.height-6))
			case "pgdown", "down", "j":
				m.helpOffset = min(max(0, len(m.helpLines())-max(1, m.height-6)), m.helpOffset+max(1, m.height-6))
			}
			break
		}
		cmd = m.handleKey(msg.String())
	default:
		if m.form != nil && !m.busy {
			cmd = m.updateForm(msg)
		}
	}
	m.resizeDetail()
	return m, cmd
}
