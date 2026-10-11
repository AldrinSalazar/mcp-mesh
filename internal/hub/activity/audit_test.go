package activity

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), store.FileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// runAudit starts Run and returns a stop func that cancels and waits.
func runAudit(t *testing.T, a *Audit) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); a.Run(ctx) }()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	return stop
}

// waitFor polls Query until at least n entries match q.
func waitFor(t *testing.T, a *Audit, q AuditQuery, n int) []Entry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := a.Query(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out: have %d entries, want %d", len(got), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

var (
	base        = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	keepForever = 100 * 365 * 24 * time.Hour
)

func TestAuditRoundTrip(t *testing.T) {
	a := NewAudit(openDB(t), AuditOptions{Retention: keepForever})
	runAudit(t, a)
	want := Call{
		Time: base, ClientID: "k_1234abcd", Client: "laptop", Kind: KindTool, Tool: "fs_read", Node: "pc1",
		MCP: "fs", Duration: 1234 * time.Millisecond, Outcome: "tool_error", Error: "boom",
	}
	a.Add(want)
	got := waitFor(t, a, AuditQuery{}, 1)
	if len(got) != 1 || got[0].Call != want || got[0].ID == 0 {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestAuditQueryFilters(t *testing.T) {
	a := NewAudit(openDB(t), AuditOptions{Retention: keepForever})
	calls := []Call{
		{Time: base, ClientID: "c1", Client: "a", Kind: KindTool, Tool: "t1", Node: "n1", MCP: "m1", Outcome: "ok"},
		{Time: base.Add(time.Hour), ClientID: "c2", Client: "b", Kind: KindTool, Tool: "t2", Node: "n1", MCP: "m2", Outcome: "error"},
		{Time: base.Add(2 * time.Hour), ClientID: "c1", Client: "a", Kind: KindTool, Tool: "t2", Node: "n2", MCP: "m2", Outcome: "tool_error"},
		{Time: base.Add(3 * time.Hour), ClientID: "c2", Client: "b", Kind: KindTool, Tool: "t1", Node: "n2", MCP: "m1", Outcome: "ok"},
	}
	runAudit(t, a)
	for _, c := range calls {
		a.Add(c)
	}
	waitFor(t, a, AuditQuery{}, len(calls))

	tests := []struct {
		name string
		q    AuditQuery
		want string // indexes into calls, newest first
	}{
		{"all", AuditQuery{}, "3210"},
		{"client", AuditQuery{ClientID: "c1"}, "20"},
		{"node", AuditQuery{Node: "n2"}, "32"},
		{"mcp", AuditQuery{MCP: "m1"}, "30"},
		{"tool", AuditQuery{Tool: "t2"}, "21"},
		{"outcome", AuditQuery{Outcome: "ok"}, "30"},
		{"since inclusive", AuditQuery{Since: base.Add(time.Hour)}, "321"},
		{"until exclusive", AuditQuery{Until: base.Add(2 * time.Hour)}, "10"},
		{"range", AuditQuery{Since: base.Add(time.Hour), Until: base.Add(3 * time.Hour)}, "21"},
		{"combined", AuditQuery{ClientID: "c2", Tool: "t1", Outcome: "ok", Node: "n2", MCP: "m1"}, "3"},
		{"combined empty", AuditQuery{ClientID: "c1", Outcome: "error"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := a.Query(t.Context(), tt.q)
			if err != nil {
				t.Fatal(err)
			}
			var idx strings.Builder
			for _, e := range got {
				for i, c := range calls {
					if e.Call == c {
						idx.WriteByte(byte('0' + i))
					}
				}
			}
			if idx.String() != tt.want {
				t.Errorf("got %q, want %q", idx.String(), tt.want)
			}
		})
	}
}

func TestAuditCount(t *testing.T) {
	a := NewAudit(openDB(t), AuditOptions{Retention: keepForever})
	runAudit(t, a)
	for i, outcome := range []string{"ok", "error", "ok", "tool_error", "ok"} {
		a.Add(Call{Time: base.Add(time.Duration(i) * time.Minute), Tool: "t", Outcome: outcome})
	}
	waitFor(t, a, AuditQuery{}, 5)

	tests := []struct {
		since time.Time
		want  Counts
	}{
		{base, Counts{Calls: 5, Errors: 2}},
		{base.Add(2 * time.Minute), Counts{Calls: 3, Errors: 1}},
		{base.Add(time.Hour), Counts{}},
	}
	for _, tt := range tests {
		got, err := a.Count(t.Context(), tt.since)
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Errorf("Count(%v) = %+v, want %+v", tt.since, got, tt.want)
		}
	}
}

func TestAuditPaging(t *testing.T) {
	a := NewAudit(openDB(t), AuditOptions{Retention: keepForever})
	runAudit(t, a)
	for i := range 5 {
		a.Add(Call{Time: base.Add(time.Duration(i) * time.Second), Tool: "t", Outcome: "ok"})
	}
	waitFor(t, a, AuditQuery{}, 5)
	var seen []time.Time
	var before int64
	for {
		page, err := a.Query(t.Context(), AuditQuery{Limit: 2, BeforeID: before})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 2 {
			t.Fatalf("page of %d, limit 2", len(page))
		}
		for _, e := range page {
			seen = append(seen, e.Time)
		}
		before = page[len(page)-1].ID
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d entries, want 5", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if !seen[i].Before(seen[i-1]) {
			t.Errorf("not newest first: %v", seen)
		}
	}
}

func TestAuditPrunesOldRows(t *testing.T) {
	db := openDB(t)
	old := store.Millis(time.Now().Add(-48 * time.Hour))
	fresh := store.Millis(time.Now())
	for _, ts := range []int64{old, fresh} {
		if _, err := db.Exec(`INSERT INTO audit (time, client_id, client, tool, node, mcp, duration_ms, outcome)
			VALUES (?, 'c', 'c', 't', 'n', 'm', 0, 'ok')`, ts); err != nil {
			t.Fatal(err)
		}
	}
	a := NewAudit(db, AuditOptions{Retention: 24 * time.Hour})
	runAudit(t, a)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := a.Query(t.Context(), AuditQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 1 {
			if store.Millis(got[0].Time) != fresh {
				t.Fatalf("kept the wrong row: %+v", got[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("old row not pruned: %d rows", len(got))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func TestAuditFullBufferDrops(t *testing.T) {
	var logs lockedBuf
	a := NewAudit(openDB(t), AuditOptions{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	done := make(chan struct{})
	go func() { // no Run: the buffer fills
		defer close(done)
		for range auditBuffer + 100 {
			a.Add(Call{Time: base, Tool: "t", Outcome: "ok"})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Add blocked on a full buffer")
	}
	if len(a.ch) != auditBuffer {
		t.Errorf("queued %d, want %d", len(a.ch), auditBuffer)
	}
	if n := strings.Count(logs.String(), "buffer full"); n != 1 {
		t.Errorf("warned %d times, want 1 (rate limited): %s", n, logs.String())
	}
	if lost, at := a.Lost(); lost != 100 || at.IsZero() {
		t.Errorf("Lost = %d, %v; want 100 and a time", lost, at)
	}
}

func TestAuditDrainsOnCancel(t *testing.T) {
	a := NewAudit(openDB(t), AuditOptions{Retention: keepForever})
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // Run starts already cancelled: everything must come from the drain
	const n = 600
	for range n {
		a.Add(Call{Time: base, Tool: "t", Outcome: "ok"})
	}
	a.Run(ctx)
	got, err := a.Query(t.Context(), AuditQuery{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("drained %d, want %d", len(got), n)
	}
}

func TestAuditAddAfterRun(t *testing.T) {
	a := NewAudit(openDB(t), AuditOptions{})
	stop := runAudit(t, a)
	stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range auditBuffer * 2 {
			a.Add(Call{Time: base})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Add blocked after Run ended")
	}
}

type countRec struct{ n int }

func (c *countRec) Add(Call) { c.n++ }

func TestMulti(t *testing.T) {
	a, b := &countRec{}, &countRec{}
	m := Multi(a, nil, b)
	m.Add(Call{})
	m.Add(Call{})
	if a.n != 2 || b.n != 2 {
		t.Errorf("a=%d b=%d, want 2 each", a.n, b.n)
	}
	Multi().Add(Call{}) // no recorders: no-op
}
