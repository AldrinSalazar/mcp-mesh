// Package adminlog records who changed what on the hub: sign-ins (and failed
// attempts), keys, admin users, groups, trust and approvals, OAuth grants.
// Tool calls have their own log (activity.Audit); this one is for admins.
package adminlog

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

// Actions. Target and Detail say what they apply to.
const (
	SignIn         = "sign_in"         // target: username
	SignInFailed   = "sign_in_failed"  // target: username tried; actor ""
	SignOut        = "sign_out"        //
	KeyCreate      = "key.create"      // target: key ID; detail: "node workstation"
	KeyRevoke      = "key.revoke"      // target: key ID; detail: kind and name
	KeyGroups      = "key.groups"      // target: key ID; detail: group names
	AdminCreate    = "admin.create"    // target: username
	AdminDelete    = "admin.delete"    // target: username
	AdminPassword  = "admin.password"  // target: username
	GroupCreate    = "group.create"    // target: group ID; detail: name
	GroupUpdate    = "group.update"    //
	GroupDelete    = "group.delete"    //
	TrustLevel     = "trust.level"     // target: node ("" for the hub default); detail: level
	NodeApprove    = "node.approve"    // target: node
	NodeForget     = "node.forget"     // target: node
	ToolApprove    = "tool.approve"    // target: node/mcp/tool (or a prefix); detail: count
	ToolOverride   = "tool.override"   // target: node/mcp/tool; detail: alias ("" if none or cleared)
	OAuthAuthorize = "oauth.authorize" // target: key ID; detail: client name
	OAuthDenied    = "oauth.denied"    // target: client name
	PairingApprove = "pairing.approve" // target: key ID; detail: "node workstation from 192.168.1.20"
	PairingDeny    = "pairing.deny"    // target: node name asked for; detail: its address
)

// Entry is one logged action.
type Entry struct {
	ID     int64     `json:"id"`
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail"`
	IP     string    `json:"ip"`
}

// Recorder is what other packages need to log.
type Recorder interface {
	Record(ctx context.Context, e Entry)
}

// Log writes and queries the admin_log table.
type Log struct {
	db        *store.DB
	logger    *slog.Logger
	retention time.Duration
}

// New returns a Log. Entries older than retention are pruned on writes;
// 0 keeps a year.
func New(db *store.DB, logger *slog.Logger, retention time.Duration) *Log {
	if logger == nil {
		logger = slog.Default()
	}
	if retention <= 0 {
		retention = 365 * 24 * time.Hour
	}
	return &Log{db: db, logger: logger, retention: retention}
}

// Record writes e (Time defaults to now). Admin actions are rare, so the
// write is synchronous; a failure is logged, never returned, since the
// action itself already happened.
func (l *Log) Record(ctx context.Context, e Entry) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	// The caller's request may be ending; the record should still land.
	ctx = context.WithoutCancel(ctx)
	_, err := l.db.ExecContext(ctx,
		"INSERT INTO admin_log (time, actor, action, target, detail, ip) VALUES (?, ?, ?, ?, ?, ?)",
		store.Millis(e.Time), e.Actor, e.Action, e.Target, e.Detail, e.IP)
	if err == nil {
		_, err = l.db.ExecContext(ctx, "DELETE FROM admin_log WHERE time < ?", store.Millis(time.Now().Add(-l.retention)))
	}
	if err != nil {
		l.logger.Error("admin log write failed", "action", e.Action, "error", err)
	}
}

// Query returns entries newest first: those with id < beforeID (0 = from the
// newest), at most limit (1-1000; 0 = 100).
func (l *Log) Query(ctx context.Context, beforeID int64, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 1000)
	where, args := "", []any{}
	if beforeID > 0 {
		where, args = "WHERE id < ?", append(args, beforeID)
	}
	rows, err := l.db.QueryContext(ctx,
		"SELECT id, time, actor, action, target, detail, ip FROM admin_log "+where+" ORDER BY id DESC LIMIT ?",
		append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("query admin log: %w", err)
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var ms int64
		if err := rows.Scan(&e.ID, &ms, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.IP); err != nil {
			return nil, fmt.Errorf("read admin log: %w", err)
		}
		e.Time = store.FromMillis(ms)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read admin log: %w", err)
	}
	return out, nil
}

// Nop discards entries (auth off, tests).
type Nop struct{}

func (Nop) Record(context.Context, Entry) {}
