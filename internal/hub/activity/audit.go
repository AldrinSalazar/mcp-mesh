package activity

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

const (
	auditBuffer      = 4096
	auditBatch       = 256
	auditPruneEvery  = time.Hour
	auditDrainBudget = 5 * time.Second
	auditWarnEvery   = time.Minute
	defaultRetention = 30 * 24 * time.Hour
	defaultLimit     = 100
	maxLimit         = 1000
)

// AuditOptions configure an Audit.
type AuditOptions struct {
	Retention time.Duration // entries older than this are deleted; 0 = 30 days
	Logger    *slog.Logger  // nil = slog.Default()
}

// Audit is the persistent log of tool calls, kept in the hub database. It is
// a [Recorder]: Add queues and never blocks, [Audit.Run] does the writing.
type Audit struct {
	db        *store.DB
	retention time.Duration
	logger    *slog.Logger
	ch        chan Call

	dropped  atomic.Int64 // drops since the last warning
	warnMu   sync.Mutex
	lastWarn time.Time

	lost     atomic.Int64 // calls never written (buffer full or failed batch), since start
	lastLoss atomic.Int64 // when the last was lost, Unix ms; 0 = never
}

// Lost reports how many calls were never written since the hub started (the
// buffer was full, or their batch failed to write) and when the last one
// was lost (zero if never).
func (a *Audit) Lost() (int64, time.Time) {
	var at time.Time
	if ms := a.lastLoss.Load(); ms != 0 {
		at = time.UnixMilli(ms)
	}
	return a.lost.Load(), at
}

func (a *Audit) lose(n int) {
	a.lost.Add(int64(n))
	a.lastLoss.Store(time.Now().UnixMilli())
}

// NewAudit returns an audit log that writes calls to db's audit table.
func NewAudit(db *store.DB, opts AuditOptions) *Audit {
	if opts.Retention <= 0 {
		opts.Retention = defaultRetention
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Audit{db: db, retention: opts.Retention, logger: opts.Logger, ch: make(chan Call, auditBuffer)}
}

// Add queues a call for writing. It never blocks the tool call path: when
// the buffer is full the call is dropped and counted. The channel is never
// closed, so Add after Run has returned is safe (the call just stays queued).
func (a *Audit) Add(c Call) {
	select {
	case a.ch <- c:
		return
	default:
	}
	a.lose(1)
	n := a.dropped.Add(1)
	a.warnMu.Lock()
	defer a.warnMu.Unlock()
	if now := time.Now(); now.Sub(a.lastWarn) >= auditWarnEvery {
		a.lastWarn = now
		a.dropped.Add(-n)
		a.logger.Warn("audit log buffer full, calls dropped", "dropped", n)
	}
}

// Run writes queued calls and prunes old ones until ctx ends, then writes
// what is still queued. The caller owns the goroutine.
func (a *Audit) Run(ctx context.Context) {
	a.prune(ctx)
	t := time.NewTicker(auditPruneEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// The caller's ctx is done; finish the queue on our own, bounded
			// budget so shutdown doesn't lose the last calls.
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditDrainBudget)
			defer cancel()
			for dctx.Err() == nil && a.writeBatch(dctx, nil) {
			}
			return
		case <-t.C:
			a.prune(ctx)
		case c := <-a.ch:
			// A batch taken off the queue is written even if ctx ends
			// meanwhile; the drain above covers the rest.
			a.writeBatch(context.WithoutCancel(ctx), &c)
		}
	}
}

// writeBatch writes first (if any) plus whatever else is queued, up to
// auditBatch calls. It reports whether it found anything queued.
func (a *Audit) writeBatch(ctx context.Context, first *Call) bool {
	batch := make([]Call, 0, auditBatch)
	if first != nil {
		batch = append(batch, *first)
	}
fill:
	for len(batch) < auditBatch {
		select {
		case c := <-a.ch:
			batch = append(batch, c)
		default:
			break fill
		}
	}
	if len(batch) == 0 {
		return false
	}
	err := a.db.Tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO audit
			(time, client_id, client, kind, tool, node, mcp, duration_ms, outcome, error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, c := range batch {
			if _, err := stmt.ExecContext(ctx, store.Millis(c.Time), c.ClientID, c.Client, c.KindOrTool(), c.Tool,
				c.Node, c.MCP, c.Duration.Milliseconds(), c.Outcome, c.Error); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// Dropping the batch avoids a retry storm against a broken database.
		a.lose(len(batch))
		a.logger.Error("write audit log failed, batch dropped", "calls", len(batch), "error", err)
	}
	return true
}

func (a *Audit) prune(ctx context.Context) {
	cutoff := store.Millis(time.Now().Add(-a.retention))
	err := a.db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM audit WHERE time < ?", cutoff)
		return err
	})
	if err != nil && ctx.Err() == nil {
		a.logger.Error("prune audit log failed", "error", err)
	}
}

// AuditQuery selects entries. Filters combine with AND.
type AuditQuery struct {
	Since, Until time.Time // zero = unbounded; Since inclusive, Until exclusive
	ClientID     string    // exact match; empty = any
	Node         string
	MCP          string
	Tool         string
	Outcome      string
	BeforeID     int64 // paging: only entries with id < BeforeID; 0 = from newest
	Limit        int   // 0 = 100, capped at 1000
}

// Entry is a stored call.
type Entry struct {
	ID int64
	Call
}

// Query returns entries newest first.
func (a *Audit) Query(ctx context.Context, q AuditQuery) ([]Entry, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		where = append(where, cond)
		args = append(args, v)
	}
	if !q.Since.IsZero() {
		add("time >= ?", store.Millis(q.Since))
	}
	if !q.Until.IsZero() {
		add("time < ?", store.Millis(q.Until))
	}
	for _, f := range []struct{ col, val string }{
		{"client_id", q.ClientID}, {"node", q.Node}, {"mcp", q.MCP}, {"tool", q.Tool}, {"outcome", q.Outcome},
	} {
		if f.val != "" {
			add(f.col+" = ?", f.val)
		}
	}
	if q.BeforeID > 0 {
		add("id < ?", q.BeforeID)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)

	stmt := "SELECT id, time, client_id, client, kind, tool, node, mcp, duration_ms, outcome, error FROM audit"
	if len(where) > 0 {
		stmt += " WHERE " + strings.Join(where, " AND ")
	}
	stmt += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := a.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("query audit log: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var ts, ms int64
		if err := rows.Scan(&e.ID, &ts, &e.ClientID, &e.Client, &e.Kind, &e.Tool, &e.Node, &e.MCP, &ms, &e.Outcome, &e.Error); err != nil {
			return nil, fmt.Errorf("read audit log: %w", err)
		}
		e.Time = store.FromMillis(ts)
		e.Duration = time.Duration(ms) * time.Millisecond
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	return out, nil
}

// Counts summarizes the calls in a time range.
type Counts struct {
	Calls  int
	Errors int // outcome "tool_error" or "error"
}

// Count summarizes the tool calls at or after since (prompt fetches and
// resource reads are not calls).
func (a *Audit) Count(ctx context.Context, since time.Time) (Counts, error) {
	var c Counts
	err := a.db.QueryRowContext(ctx,
		"SELECT COUNT(*), COUNT(*) FILTER (WHERE outcome <> 'ok') FROM audit WHERE kind = 'tool' AND time >= ?",
		store.Millis(since)).Scan(&c.Calls, &c.Errors)
	if err != nil {
		return Counts{}, fmt.Errorf("count audit log: %w", err)
	}
	return c, nil
}
