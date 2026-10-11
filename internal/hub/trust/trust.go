// Package trust decides which tools are approved: each node has a trust
// level, and approved tools are pinned by a hash of their whole definition (every field
// the MCP sent: name, description, schemas, annotations, _meta, ...), so a
// tool that changes ("rug pull") is hidden again until someone approves the
// change. Approvals name the hash the admin reviewed, so a definition that
// changed since is refused (StaleError) instead of silently approved.
//
// The catalog keeps every tool; Visible is what hides unapproved ones (it is
// part of gateway.Options.Visible). Visible fails closed: it hashes the
// catalog entry it is given, so a changed tool is hidden from the moment it
// enters the catalog, before Run has seen the change.
package trust

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Level is how much a node is trusted.
type Level string

const (
	// Trust shows every tool; nothing waits for approval. The default.
	Trust Level = "trust"
	// Review shows new tools at once (pinning them) but hides a pinned tool
	// whose definition changed until the change is approved.
	Review Level = "review"
	// Approve hides the whole node until it is approved; after that every new
	// or changed tool waits for approval.
	Approve Level = "approve"
)

// Valid reports whether l is one of the levels.
func (l Level) Valid() bool { return l == Trust || l == Review || l == Approve }

// State is a tool's approval state.
type State string

const (
	Approved State = "approved"
	New      State = "new"          // never approved (Approve level)
	Changed  State = "changed"      // differs from its pin
	NodeWait State = "node_pending" // the node itself isn't approved yet
)

// Hash identifies a tool definition: hex SHA-256 of its canonical JSON (see
// Definition). Every field counts, because the gateway forwards the whole
// upstream tool to clients, not just the name, description and input schema.
func Hash(t *mcp.Tool) string {
	h, _ := hashDefinition(t)
	return h
}

// Definition returns the tool as plain JSON values (marshalled, then
// unmarshalled into an any), so equal definitions encode identically whatever
// Go types they were built from: encoding/json sorts map keys.
func Definition(t *mcp.Tool) any {
	return normalize(t)
}

// normalize returns v as plain JSON values.
func normalize(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// normalizeSchema returns the schema as plain JSON values so equal schemas
// encode identically.
func normalizeSchema(schema any) any { return normalize(schema) }

// hashDefinition returns the Hash and the canonical JSON text it covers.
func hashDefinition(t *mcp.Tool) (hash, defJSON string) {
	b, _ := json.Marshal(Definition(t))
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), string(b)
}

// Pending is a tool waiting for approval.
type Pending struct {
	Node  string `json:"node"`
	MCP   string `json:"mcp"`
	Tool  string `json:"tool"` // original name
	Name  string `json:"name"` // exposed name
	State State  `json:"state"`
	// Hash is the current definition's Hash: what an approval sends back.
	Hash        string `json:"hash"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
	// Definition is the full current tool as the MCP sent it.
	Definition any `json:"definition"`
	// Previous is the approved definition, for Changed tools.
	Previous *Approval `json:"previous,omitempty"`
	Since    time.Time `json:"since"` // when this definition was first seen
}

// Approval is an approved tool text.
type Approval struct {
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
	// Definition is the full approved tool.
	Definition any       `json:"definition"`
	ApprovedAt time.Time `json:"approved_at"`
	ApprovedBy string    `json:"approved_by"`
}

// NodePolicy is a node's level and approval.
type NodePolicy struct {
	Node     string `json:"node"`
	Level    Level  `json:"level"`
	Explicit bool   `json:"explicit"` // false: the hub default applies
	// Approved is meaningful for the Approve level: the node is visible.
	Approved   bool       `json:"approved"`
	ApprovedAt *time.Time `json:"approved_at,omitempty"`
	ApprovedBy string     `json:"approved_by,omitempty"`
}

var ErrInvalid = errors.New("invalid trust level")

// ErrBadRequest marks an approval request that is malformed (empty, or
// naming a tool of another node).
var ErrBadRequest = errors.New("trust: invalid approval request")

// ErrStale is returned (as a *StaleError) when a definition changed since the
// admin reviewed it.
var ErrStale = errors.New("trust: definition changed since it was reviewed")

// Expected names one tool and the definition hash the admin reviewed.
type Expected struct {
	Node string `json:"node"`
	MCP  string `json:"mcp"`
	Tool string `json:"tool"` // original name
	Hash string `json:"hash"`
}

// StaleError lists the tools whose current hash differs from the expected
// one (Hash is the current hash, "" if the tool is gone).
type StaleError struct{ Tools []Expected }

func (e *StaleError) Error() string {
	return fmt.Sprintf("%v (%d tools)", ErrStale, len(e.Tools))
}

func (e *StaleError) Is(target error) bool { return target == ErrStale }

// Options configure a Store.
type Options struct {
	Catalog *catalog.Catalog
	Logger  *slog.Logger // nil = slog.Default()
	// OnChange is called (without locks held) after approvals, levels or
	// pins change what Visible answers. The hub resyncs client tool lists.
	OnChange func()
}

type toolKey struct{ node, mcp, tool string }

type pin struct {
	hash, description, schema, definition string
	approvedAt                            time.Time
	approvedBy                            string
}

type nodeRow struct {
	level      Level
	approvedAt time.Time
	approvedBy string
}

// snapshot is an immutable copy of the tables; Visible reads it lock-free.
type snapshot struct {
	def   Level
	nodes map[string]nodeRow
	pins  map[toolKey]pin
}

func (sn *snapshot) level(node string) Level {
	if r, ok := sn.nodes[node]; ok {
		return r.level
	}
	return sn.def
}

type seenKey struct {
	toolKey
	hash string
}

type hashed struct{ hash, schema, definition string }

// Store holds levels and pins (tables nodes, tool_pins, settings
// "trust.default"). It is safe for concurrent use.
type Store struct {
	db   *store.DB
	opts Options
	log  *slog.Logger

	snap atomic.Pointer[snapshot]
	// hashes caches hashed per catalog entry (a changed tool is a new
	// entry). Run clears it on every catalog change, which bounds its size.
	hashes sync.Map // *catalog.Tool -> hashed

	// mu serializes every read-modify-write of the tables (approvals, levels,
	// Run's auto-pinning) and snapshot rebuilds.
	mu sync.Mutex

	seenMu sync.Mutex
	seen   map[seenKey]time.Time
}

// Open loads levels and pins.
func Open(ctx context.Context, db *store.DB, opts Options) (*Store, error) {
	if opts.Catalog == nil {
		return nil, errors.New("trust: catalog is required")
	}
	s := &Store{db: db, opts: opts, log: opts.Logger, seen: map[seenKey]time.Time{}}
	if s.log == nil {
		s.log = slog.Default()
	}
	sn, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	s.snap.Store(sn)
	return s, nil
}

func (s *Store) load(ctx context.Context) (*snapshot, error) {
	sn := &snapshot{def: Trust, nodes: map[string]nodeRow{}, pins: map[toolKey]pin{}}
	var def string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'trust.default'`).Scan(&def)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("read trust default: %w", err)
	case Level(def).Valid():
		sn.def = Level(def)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name, trust, approved_at, approved_by FROM nodes`)
	if err != nil {
		return nil, fmt.Errorf("read node trust: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, level, by string
		var at sql.NullInt64
		if err := rows.Scan(&name, &level, &at, &by); err != nil {
			return nil, fmt.Errorf("read node trust: %w", err)
		}
		r := nodeRow{level: Level(level), approvedBy: by}
		if at.Valid {
			r.approvedAt = store.FromMillis(at.Int64)
		}
		sn.nodes[name] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read node trust: %w", err)
	}
	prows, err := s.db.QueryContext(ctx, `SELECT node, mcp, tool, hash, description, input_schema, definition, approved_at, approved_by FROM tool_pins`)
	if err != nil {
		return nil, fmt.Errorf("read tool pins: %w", err)
	}
	defer prows.Close()
	for prows.Next() {
		var k toolKey
		var p pin
		var at int64
		if err := prows.Scan(&k.node, &k.mcp, &k.tool, &p.hash, &p.description, &p.schema, &p.definition, &at, &p.approvedBy); err != nil {
			return nil, fmt.Errorf("read tool pins: %w", err)
		}
		p.approvedAt = store.FromMillis(at)
		sn.pins[k] = p
	}
	if err := prows.Err(); err != nil {
		return nil, fmt.Errorf("read tool pins: %w", err)
	}
	return sn, nil
}

// reloadLocked rebuilds the snapshot from the database and reports whether
// it differs. Callers hold s.mu.
func (s *Store) reloadLocked(ctx context.Context) (bool, error) {
	sn, err := s.load(ctx)
	if err != nil {
		return false, err
	}
	old := s.snap.Load()
	s.snap.Store(sn)
	return !reflect.DeepEqual(old, sn), nil
}

func (s *Store) changed() {
	if s.opts.OnChange != nil {
		s.opts.OnChange()
	}
}

// refresh reloads after a commit made through another connection.
func (s *Store) refresh(ctx context.Context) error {
	s.mu.Lock()
	ch, err := s.reloadLocked(ctx)
	s.mu.Unlock()
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("reload trust settings failed", "error", err)
		}
		return err
	}
	if ch {
		s.changed()
	}
	return nil
}

// Run follows catalog changes until ctx ends: under Trust it pins every tool
// (approved_by "auto") and re-pins changed ones; under Review it pins tools
// it has never seen. Pins are what Approve-level approvals and Review-level
// diffs compare against. It also reloads levels and pins when another
// process changes the database, calling OnChange.
func (s *Store) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := s.db.Watch(ctx, time.Second, func() error { return s.refresh(ctx) }); err != nil {
			s.log.Warn("trust: database watch stopped", "error", err)
		}
	})
	defer wg.Wait()

	ch, cancel := s.opts.Catalog.Changes()
	defer cancel()
	s.process(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			s.process(ctx)
		}
	}
}

// process pins what the levels call for and records when each definition was
// first seen.
func (s *Store) process(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hashes.Clear()
	tools := s.opts.Catalog.List()
	sn := s.snap.Load()
	now := time.Now()

	seen := make(map[seenKey]time.Time, len(tools))
	var toPin []*catalog.Tool
	var overwrite []bool
	s.seenMu.Lock()
	for _, t := range tools {
		if t.Upstream == nil {
			continue
		}
		h := s.hashOf(t).hash
		k := toolKey{t.Node, t.MCP, t.Upstream.Name}
		sk := seenKey{k, h}
		if at, ok := s.seen[sk]; ok {
			seen[sk] = at
		} else {
			seen[sk] = now
		}
		p, pinned := sn.pins[k]
		switch sn.level(t.Node) {
		case Trust:
			if !pinned || p.hash != h {
				toPin = append(toPin, t)
				overwrite = append(overwrite, true)
			}
		case Review:
			if !pinned {
				toPin = append(toPin, t)
				overwrite = append(overwrite, false)
			}
		}
	}
	s.seen = seen
	s.seenMu.Unlock()

	if len(toPin) == 0 {
		return
	}
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		for i, t := range toPin {
			if err := s.pinTool(ctx, tx, t, "auto", now, overwrite[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		// Auto-pins don't change what Visible answers, so no OnChange.
		_, err = s.reloadLocked(ctx)
	}
	if err != nil && ctx.Err() == nil {
		s.log.Warn("pin tools failed", "error", err)
	}
}

func (s *Store) hashOf(t *catalog.Tool) hashed {
	if v, ok := s.hashes.Load(t); ok {
		return v.(hashed)
	}
	h, def := hashDefinition(t.Upstream)
	sb, _ := json.Marshal(normalizeSchema(t.Upstream.InputSchema))
	v := hashed{h, string(sb), def}
	s.hashes.Store(t, v)
	return v
}

// pinTool writes t's current definition as its pin. Without overwrite an
// existing pin stays.
func (s *Store) pinTool(ctx context.Context, tx *sql.Tx, t *catalog.Tool, by string, now time.Time, overwrite bool) error {
	h := s.hashOf(t)
	conflict := "DO NOTHING"
	if overwrite {
		conflict = `DO UPDATE SET hash = excluded.hash, description = excluded.description,
			input_schema = excluded.input_schema, definition = excluded.definition, approved_at = excluded.approved_at, approved_by = excluded.approved_by`
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO tool_pins (node, mcp, tool, hash, description, input_schema, definition, approved_at, approved_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (node, mcp, tool) `+conflict,
		t.Node, t.MCP, t.Upstream.Name, h.hash, t.Upstream.Description, h.schema, h.definition, store.Millis(now), by)
	if err != nil {
		return fmt.Errorf("pin %s/%s/%s: %w", t.Node, t.MCP, t.Upstream.Name, err)
	}
	return nil
}

// Visible reports whether the tool may be shown to clients.
func (s *Store) Visible(t *catalog.Tool) bool { return s.State(t) == Approved }

// State is the tool's approval state (Approved when visible). A tool without
// an upstream definition cannot be verified and is New.
func (s *Store) State(t *catalog.Tool) State {
	if t == nil || t.Upstream == nil {
		return New
	}
	return stateOf(s.snap.Load(), t, s.hashOf(t).hash)
}

func stateOf(sn *snapshot, t *catalog.Tool, hash string) State {
	level := sn.level(t.Node)
	if level == Trust {
		return Approved
	}
	if level == Approve && sn.nodes[t.Node].approvedAt.IsZero() {
		return NodeWait
	}
	p, ok := sn.pins[toolKey{t.Node, t.MCP, t.Upstream.Name}]
	switch {
	case !ok && level == Review:
		return Approved // Run pins it
	case !ok:
		return New
	case p.hash != hash:
		return Changed
	}
	return Approved
}

// Default is the level for nodes without their own.
func (s *Store) Default() Level { return s.snap.Load().def }

// mutate runs fn in a transaction under the write lock, reloads the snapshot
// and calls OnChange if it changed.
func (s *Store) mutate(ctx context.Context, fn func(tx *sql.Tx, sn *snapshot) error) error {
	s.mu.Lock()
	err := s.db.Tx(ctx, func(tx *sql.Tx) error { return fn(tx, s.snap.Load()) })
	var ch bool
	if err == nil {
		ch, err = s.reloadLocked(ctx)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if ch {
		s.changed()
	}
	return nil
}

// pinWhere pins (overwriting) the catalog tools selected by pick.
func (s *Store) pinWhere(ctx context.Context, tx *sql.Tx, by string, pick func(*catalog.Tool) bool) error {
	now := time.Now()
	for _, t := range s.opts.Catalog.List() {
		if t.Upstream != nil && pick(t) {
			if err := s.pinTool(ctx, tx, t, by, now, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetDefault changes the hub default. When it becomes Trust, the current
// tools of nodes that follow it are pinned.
func (s *Store) SetDefault(ctx context.Context, l Level) error {
	if !l.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalid, l)
	}
	return s.mutate(ctx, func(tx *sql.Tx, sn *snapshot) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('trust.default', ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, string(l))
		if err != nil {
			return fmt.Errorf("save trust default: %w", err)
		}
		if l != Trust {
			return nil
		}
		return s.pinWhere(ctx, tx, "auto", func(t *catalog.Tool) bool { _, own := sn.nodes[t.Node]; return !own })
	})
}

// Policy returns a node's policy (connected or not). Approved is true only
// when the node has been approved (ApproveNode or Approve).
func (s *Store) Policy(node string) NodePolicy {
	sn := s.snap.Load()
	r, ok := sn.nodes[node]
	if !ok {
		return NodePolicy{Node: node, Level: sn.def}
	}
	p := NodePolicy{Node: node, Level: r.level, Explicit: true}
	if !r.approvedAt.IsZero() {
		at := r.approvedAt
		p.Approved, p.ApprovedAt, p.ApprovedBy = true, &at, r.approvedBy
	}
	return p
}

// NodeVisible reports whether a node's prompts and resources may be shown:
// always, except for an Approve-level node that isn't approved yet. Unlike
// tools they aren't pinned one by one (clients pick them; the model doesn't
// read them on its own).
func (s *Store) NodeVisible(node string) bool {
	sn := s.snap.Load()
	return sn.level(node) != Approve || !sn.nodes[node].approvedAt.IsZero()
}

// SetLevel gives a node its own level. Raising a node to Approve doesn't
// unapprove it if it was approved before; lowering to Trust pins its
// current tools.
func (s *Store) SetLevel(ctx context.Context, node string, l Level) error {
	if !l.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalid, l)
	}
	return s.mutate(ctx, func(tx *sql.Tx, _ *snapshot) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO nodes (name, trust) VALUES (?, ?)
			ON CONFLICT (name) DO UPDATE SET trust = excluded.trust`, node, string(l))
		if err != nil {
			return fmt.Errorf("save trust of %s: %w", node, err)
		}
		if l != Trust {
			return nil
		}
		return s.pinWhere(ctx, tx, "auto", func(t *catalog.Tool) bool { return t.Node == node })
	})
}

// ClearLevel makes the node follow the hub default again. It deletes the
// node's row, so a previous node approval is forgotten too.
func (s *Store) ClearLevel(ctx context.Context, node string) error {
	return s.mutate(ctx, func(tx *sql.Tx, _ *snapshot) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE name = ?`, node); err != nil {
			return fmt.Errorf("clear trust of %s: %w", node, err)
		}
		return nil
	})
}

// Pending lists tools waiting for approval among the tools in the catalog,
// sorted by node, MCP, tool. Tools of an unapproved Approve-level node are
// listed with State NodeWait.
func (s *Store) Pending() []Pending {
	sn := s.snap.Load()
	now := time.Now()
	var out []Pending
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	for _, t := range s.opts.Catalog.List() {
		if t.Upstream == nil {
			continue
		}
		h := s.hashOf(t)
		st := stateOf(sn, t, h.hash)
		if st == Approved {
			continue
		}
		k := toolKey{t.Node, t.MCP, t.Upstream.Name}
		sk := seenKey{k, h.hash}
		since, ok := s.seen[sk]
		if !ok {
			since = now
			s.seen[sk] = now
		}
		p := Pending{
			Node: t.Node, MCP: t.MCP, Tool: t.Upstream.Name, Name: t.Name, State: st,
			Hash: h.hash, Definition: Definition(t.Upstream), Description: t.Upstream.Description, InputSchema: normalizeSchema(t.Upstream.InputSchema),
			Since: since,
		}
		if pn, ok := sn.pins[k]; ok && st == Changed {
			var schema any
			_ = json.Unmarshal([]byte(pn.schema), &schema)
			var def any
			_ = json.Unmarshal([]byte(pn.definition), &def)
			p.Previous = &Approval{Description: pn.description, InputSchema: schema, Definition: def, ApprovedAt: pn.approvedAt, ApprovedBy: pn.approvedBy}
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Pending) int {
		return strings.Compare(a.Node+"\x00"+a.MCP+"\x00"+a.Tool, b.Node+"\x00"+b.MCP+"\x00"+b.Tool)
	})
	return out
}

// approveNodeRow marks the node approved, creating its row if it follows the
// default (the row then fixes the level it currently has).
func approveNodeRow(ctx context.Context, tx *sql.Tx, sn *snapshot, node, by string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO nodes (name, trust, approved_at, approved_by) VALUES (?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET approved_at = excluded.approved_at, approved_by = excluded.approved_by`,
		node, string(sn.level(node)), store.Millis(now), by)
	if err != nil {
		return fmt.Errorf("approve node %s: %w", node, err)
	}
	return nil
}

// checkExpected resolves the expected tools against the catalog. Tools that
// are gone or whose current hash differs make a *StaleError.
func (s *Store) checkExpected(tools []Expected) ([]*catalog.Tool, error) {
	var stale []Expected
	found := make([]*catalog.Tool, 0, len(tools))
	for _, e := range tools {
		t, ok := s.opts.Catalog.ToolByKey(catalog.ToolKey{Node: e.Node, MCP: e.MCP, Tool: e.Tool})
		if !ok || t.Upstream == nil {
			e.Hash = ""
			stale = append(stale, e)
			continue
		}
		if h := s.hashOf(t).hash; h != e.Hash {
			e.Hash = h
			stale = append(stale, e)
			continue
		}
		found = append(found, t)
	}
	if len(stale) > 0 {
		return nil, &StaleError{Tools: stale}
	}
	return found, nil
}

// ApproveNode approves an Approve-level node and pins exactly the listed
// tools (all of this node); its other tools stay unapproved. A node that
// follows the hub default gets its own row (keeping its current level) to
// hold the approval. A stale hash changes nothing (*StaleError).
func (s *Store) ApproveNode(ctx context.Context, node, by string, tools []Expected) error {
	for _, e := range tools {
		if e.Node != node {
			return fmt.Errorf("%w: tool %s/%s belongs to node %q, not %q", ErrBadRequest, e.MCP, e.Tool, e.Node, node)
		}
	}
	return s.mutate(ctx, func(tx *sql.Tx, sn *snapshot) error {
		found, err := s.checkExpected(tools)
		if err != nil {
			return err
		}
		now := time.Now()
		if err := approveNodeRow(ctx, tx, sn, node, by, now); err != nil {
			return err
		}
		for _, t := range found {
			if err := s.pinTool(ctx, tx, t, by, now, true); err != nil {
				return err
			}
		}
		return nil
	})
}

// Approve pins the listed tools' definitions, provided each still has the
// hash the admin reviewed; otherwise nothing is written and the error is a
// *StaleError. Tools that are already approved are skipped (a double click
// is not an error). It returns how many were pinned. If a pinned tool's node
// waited for approval, the node is approved too.
func (s *Store) Approve(ctx context.Context, by string, tools []Expected) (int, error) {
	if len(tools) == 0 {
		return 0, fmt.Errorf("%w: no tools", ErrBadRequest)
	}
	n := 0
	err := s.mutate(ctx, func(tx *sql.Tx, sn *snapshot) error {
		n = 0
		found, err := s.checkExpected(tools)
		if err != nil {
			return err
		}
		now := time.Now()
		waiting := map[string]bool{}
		for _, t := range found {
			st := stateOf(sn, t, s.hashOf(t).hash)
			if st == Approved {
				continue
			}
			if st == NodeWait {
				waiting[t.Node] = true
			}
			n++
			if err := s.pinTool(ctx, tx, t, by, now, true); err != nil {
				return err
			}
		}
		for node := range waiting {
			if err := approveNodeRow(ctx, tx, sn, node, by, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
