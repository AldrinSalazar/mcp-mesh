// Package acl decides which tools each client may see and call: client keys
// belong to groups, and a group holds grants (node / MCP / tool, "*" at any
// level). Default deny: a key in no group sees nothing.
//
// The gateway asks Allows for every tool of every client (through
// gateway.Options.Visible), so Allows reads an in-memory snapshot; writes go
// to the database and replace the snapshot. Watch reloads it when another
// process (the hub CLI) changes the database.
package acl

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

// Everything is the group the migration creates with a single "*" grant.
// New client keys join it unless the creator picks other groups.
const Everything = "g_everything"

// Any matches every node, MCP or tool at its level of a Grant.
const Any = "*"

// Grant allows one node / MCP / tool. Node "*" means every node (and then
// MCP and Tool must be "*" too); MCP "*" means every MCP of Node (Tool "*");
// Tool is the MCP's original tool name, or "*".
type Grant struct {
	Node string `json:"node"`
	MCP  string `json:"mcp"`
	Tool string `json:"tool"`
}

// Group is a named set of grants and the keys in it.
type Group struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Created     time.Time `json:"created"`
	Grants      []Grant   `json:"grants"` // sorted by node, mcp, tool
	Keys        []string  `json:"keys"`   // key IDs, sorted
}

// GroupInput creates or replaces a group's editable fields.
type GroupInput struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Grants      []Grant `json:"grants"`
}

var (
	ErrNotFound  = errors.New("no such group")
	ErrNameTaken = errors.New("a group with that name exists")
	// ErrInvalid wraps validation failures (bad name, bad grant).
	ErrInvalid = errors.New("invalid group")
)

// Options configure a Store.
type Options struct {
	Logger *slog.Logger // nil = slog.Default()
	// OnChange is called (without locks held) after the snapshot changes,
	// from a write here or one noticed by Watch. The hub uses it to resync
	// every client's tool list.
	OnChange func()
}

// Store is the groups table plus an in-memory snapshot for Allows. It is
// safe for concurrent use.
type Store struct {
	db     *store.DB
	opts   Options
	logger *slog.Logger

	snap atomic.Pointer[snapshot]
	// mu serializes reloads so snapshots are swapped in the order they were
	// read; Allows never takes it.
	mu sync.Mutex
}

// snapshot maps a key ID to the union of its groups' grants (sorted, no
// duplicates). It is never modified after it is published.
type snapshot struct {
	grants map[string][]Grant
}

func (a *snapshot) equal(b *snapshot) bool {
	return maps.EqualFunc(a.grants, b.grants, slices.Equal[[]Grant])
}

func compareGrants(a, b Grant) int {
	return cmp.Or(cmp.Compare(a.Node, b.Node), cmp.Compare(a.MCP, b.MCP), cmp.Compare(a.Tool, b.Tool))
}

// Open loads the snapshot.
func Open(ctx context.Context, db *store.DB, opts Options) (*Store, error) {
	s := &Store{db: db, opts: opts, logger: opts.Logger}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	if _, err := s.reload(ctx); err != nil {
		return nil, fmt.Errorf("open groups: %w", err)
	}
	return s, nil
}

// reload rebuilds the snapshot from the database and reports whether it
// differs from the previous one.
func (s *Store) reload(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT kg.key_id, g.node, g.mcp, g.tool
		FROM key_groups kg JOIN group_grants g ON g.group_id = kg.group_id`)
	if err != nil {
		return false, fmt.Errorf("load grants: %w", err)
	}
	defer rows.Close()
	next := &snapshot{grants: map[string][]Grant{}}
	for rows.Next() {
		var id string
		var g Grant
		if err := rows.Scan(&id, &g.Node, &g.MCP, &g.Tool); err != nil {
			return false, fmt.Errorf("load grants: %w", err)
		}
		next.grants[id] = append(next.grants[id], g)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("load grants: %w", err)
	}
	for id, gs := range next.grants {
		slices.SortFunc(gs, compareGrants)
		next.grants[id] = slices.Compact(gs)
	}
	old := s.snap.Swap(next)
	return old != nil && !old.equal(next), nil
}

// refresh reloads after a write of ours and tells the hub if it mattered.
func (s *Store) refresh(ctx context.Context) {
	// The write is committed; a failed reload must not fail it, and the
	// request's context may already be done, so don't depend on it.
	changed, err := s.reload(context.WithoutCancel(ctx))
	if err != nil {
		s.logger.Warn("groups not reloaded after a change", "error", err)
		return
	}
	if changed && s.opts.OnChange != nil {
		s.opts.OnChange()
	}
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// loadGroups reads the group with this ID, or every group when id is "".
func loadGroups(ctx context.Context, q querier, id string) ([]Group, error) {
	where, args := "", []any(nil)
	if id != "" {
		where, args = " WHERE id = ?", []any{id}
	}
	rows, err := q.QueryContext(ctx, "SELECT id, name, description, created_at FROM groups"+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	byID := map[string]int{}
	for rows.Next() {
		var g Group
		var created int64
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &created); err != nil {
			return nil, err
		}
		g.Created = store.FromMillis(created)
		g.Grants, g.Keys = []Grant{}, []string{}
		byID[g.ID] = len(out)
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	grows, err := q.QueryContext(ctx, "SELECT group_id, node, mcp, tool FROM group_grants ORDER BY node, mcp, tool")
	if err != nil {
		return nil, err
	}
	defer grows.Close()
	for grows.Next() {
		var gid string
		var g Grant
		if err := grows.Scan(&gid, &g.Node, &g.MCP, &g.Tool); err != nil {
			return nil, err
		}
		if i, ok := byID[gid]; ok {
			out[i].Grants = append(out[i].Grants, g)
		}
	}
	if err := grows.Err(); err != nil {
		return nil, err
	}
	grows.Close()

	krows, err := q.QueryContext(ctx, "SELECT group_id, key_id FROM key_groups ORDER BY key_id")
	if err != nil {
		return nil, err
	}
	defer krows.Close()
	for krows.Next() {
		var gid, kid string
		if err := krows.Scan(&gid, &kid); err != nil {
			return nil, err
		}
		if i, ok := byID[gid]; ok {
			out[i].Keys = append(out[i].Keys, kid)
		}
	}
	return out, krows.Err()
}

// List returns every group, sorted by name.
func (s *Store) List(ctx context.Context) ([]Group, error) {
	groups, err := loadGroups(ctx, s.db, "")
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	slices.SortFunc(groups, func(a, b Group) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
	})
	return groups, nil
}

// Get returns one group.
func (s *Store) Get(ctx context.Context, id string) (Group, error) {
	groups, err := loadGroups(ctx, s.db, id)
	if err != nil {
		return Group{}, fmt.Errorf("get group %s: %w", id, err)
	}
	if len(groups) == 0 {
		return Group{}, fmt.Errorf("group %q: %w", id, ErrNotFound)
	}
	return groups[0], nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

func validateGrant(g Grant) error {
	for _, l := range []struct{ name, v string }{{"node", g.Node}, {"mcp", g.MCP}, {"tool", g.Tool}} {
		switch {
		case l.v == "":
			return invalid("grant %s is empty", l.name)
		case len(l.v) > 128:
			return invalid("grant %s is longer than 128 bytes", l.name)
		case hasControl(l.v):
			return invalid("grant %s %q contains control characters", l.name, l.v)
		}
	}
	if g.Node == Any && (g.MCP != Any || g.Tool != Any) {
		return invalid(`grant for node "*" must have mcp "*" and tool "*"`)
	}
	if g.MCP == Any && g.Tool != Any {
		return invalid(`grant for mcp "*" must have tool "*"`)
	}
	return nil
}

// validate checks the input and returns it normalized: trimmed name, grants
// sorted and deduplicated.
func validate(in GroupInput) (GroupInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	switch n := utf8.RuneCountInString(in.Name); {
	case n == 0:
		return in, invalid("name is empty")
	case n > 64:
		return in, invalid("name is longer than 64 characters")
	case hasControl(in.Name):
		return in, invalid("name contains control characters")
	}
	if utf8.RuneCountInString(in.Description) > 500 {
		return in, invalid("description is longer than 500 characters")
	}
	for _, g := range in.Grants {
		if err := validateGrant(g); err != nil {
			return in, err
		}
	}
	in.Grants = slices.Compact(slices.SortedFunc(slices.Values(in.Grants), compareGrants))
	return in, nil
}

func newID(tx *sql.Tx) (string, error) {
	for {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("random id: %w", err)
		}
		id := "g_" + hex.EncodeToString(b[:])
		var taken int
		if err := tx.QueryRow("SELECT count(*) FROM groups WHERE id = ?", id).Scan(&taken); err != nil {
			return "", err
		}
		if taken == 0 {
			return id, nil
		}
	}
}

// checkName fails with ErrNameTaken when another group has the name. The
// column is NOCASE only for ASCII, so compare with Unicode folding in Go.
func checkName(ctx context.Context, tx *sql.Tx, name, exceptID string) error {
	rows, err := tx.QueryContext(ctx, "SELECT id, name FROM groups")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, n string
		if err := rows.Scan(&id, &n); err != nil {
			return err
		}
		if id != exceptID && strings.EqualFold(n, name) {
			return fmt.Errorf("%w: %q", ErrNameTaken, name)
		}
	}
	return rows.Err()
}

func writeGrants(ctx context.Context, tx *sql.Tx, id string, grants []Grant) error {
	for _, g := range grants {
		if _, err := tx.ExecContext(ctx, "INSERT INTO group_grants (group_id, node, mcp, tool) VALUES (?, ?, ?, ?)",
			id, g.Node, g.MCP, g.Tool); err != nil {
			return err
		}
	}
	return nil
}

func mustExist(ctx context.Context, tx *sql.Tx, id string) error {
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM groups WHERE id = ?", id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("group %q: %w", id, ErrNotFound)
	}
	return nil
}

// Create adds a group. Names are 1-64 characters, unique ignoring case.
// Grants are validated (see Grant) and deduplicated.
func (s *Store) Create(ctx context.Context, in GroupInput) (Group, error) {
	in, err := validate(in)
	if err != nil {
		return Group{}, err
	}
	var out Group
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := checkName(ctx, tx, in.Name, ""); err != nil {
			return err
		}
		id, err := newID(tx)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO groups (id, name, description, created_at) VALUES (?, ?, ?, ?)",
			id, in.Name, in.Description, store.Millis(time.Now().UTC())); err != nil {
			return err
		}
		if err := writeGrants(ctx, tx, id, in.Grants); err != nil {
			return err
		}
		gs, err := loadGroups(ctx, tx, id)
		if err != nil {
			return err
		}
		out = gs[0]
		return nil
	})
	if err != nil {
		return Group{}, fmt.Errorf("create group: %w", err)
	}
	s.refresh(ctx)
	return out, nil
}

// Update replaces a group's name, description and grants.
func (s *Store) Update(ctx context.Context, id string, in GroupInput) (Group, error) {
	in, err := validate(in)
	if err != nil {
		return Group{}, err
	}
	var out Group
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := mustExist(ctx, tx, id); err != nil {
			return err
		}
		if err := checkName(ctx, tx, in.Name, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE groups SET name = ?, description = ? WHERE id = ?", in.Name, in.Description, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM group_grants WHERE group_id = ?", id); err != nil {
			return err
		}
		if err := writeGrants(ctx, tx, id, in.Grants); err != nil {
			return err
		}
		gs, err := loadGroups(ctx, tx, id)
		if err != nil {
			return err
		}
		out = gs[0]
		return nil
	})
	if err != nil {
		return Group{}, fmt.Errorf("update group %s: %w", id, err)
	}
	s.refresh(ctx)
	return out, nil
}

// Delete removes a group; its keys lose what it granted.
func (s *Store) Delete(ctx context.Context, id string) error {
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := mustExist(ctx, tx, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM groups WHERE id = ?", id)
		return err
	})
	if err != nil {
		return fmt.Errorf("delete group %s: %w", id, err)
	}
	s.refresh(ctx)
	return nil
}

// Memberships maps every key ID that is in at least one group to its group
// IDs (sorted).
func (s *Store) Memberships(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key_id, group_id FROM key_groups ORDER BY key_id, group_id")
	if err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var k, g string
		if err := rows.Scan(&k, &g); err != nil {
			return nil, fmt.Errorf("list memberships: %w", err)
		}
		out[k] = append(out[k], g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	return out, nil
}

// SetKeyGroups replaces the groups of a key. Unknown group IDs are
// ErrNotFound; an empty list leaves the key in no group (it sees nothing).
func (s *Store) SetKeyGroups(ctx context.Context, keyID string, groupIDs []string) error {
	ids := slices.Compact(slices.Sorted(slices.Values(groupIDs)))
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM keys WHERE id = ?", keyID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("key %q: %w", keyID, ErrNotFound)
		}
		for _, g := range ids {
			if err := mustExist(ctx, tx, g); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM key_groups WHERE key_id = ?", keyID); err != nil {
			return err
		}
		for _, g := range ids {
			if _, err := tx.ExecContext(ctx, "INSERT INTO key_groups (key_id, group_id) VALUES (?, ?)", keyID, g); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("set groups of key %s: %w", keyID, err)
	}
	s.refresh(ctx)
	return nil
}

// Allows reports whether the key may see and call the tool. It reads the
// snapshot only.
func (s *Store) Allows(keyID, node, mcp, tool string) bool {
	snap := s.snap.Load()
	if snap == nil {
		return false
	}
	for _, g := range snap.grants[keyID] {
		if (g.Node == Any || g.Node == node) && (g.MCP == Any || g.MCP == mcp) && (g.Tool == Any || g.Tool == tool) {
			return true
		}
	}
	return false
}

// Sync reloads the snapshot now, for a caller that must see a write another
// process just made rather than wait for Watch: the hub calls it when a
// client key is first used, since the CLI makes a key and its Everything
// membership a moment before the key's first request. It calls OnChange
// when the snapshot changed, so other clients are resynced too.
func (s *Store) Sync(ctx context.Context) error {
	changed, err := s.reload(ctx)
	if err != nil {
		return err
	}
	if changed && s.opts.OnChange != nil {
		s.opts.OnChange()
	}
	return nil
}

// Watch reloads the snapshot when the database changes (store.DB.Watch)
// until ctx ends, calling OnChange when it did change. The caller owns the
// goroutine.
func (s *Store) Watch(ctx context.Context, interval time.Duration) {
	err := s.db.Watch(ctx, interval, func() error {
		changed, err := s.reload(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("groups not reloaded, keeping the current set; retrying", "error", err)
			}
			return err
		}
		if changed && s.opts.OnChange != nil {
			s.opts.OnChange()
		}
		return nil
	})
	if err != nil {
		s.logger.Warn("database watch stopped; other processes' group changes are no longer noticed", "error", err)
	}
}
