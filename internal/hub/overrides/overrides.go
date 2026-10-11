// Package overrides stores the aliases and description overrides admins give
// tools. Rows may name tools that aren't connected; the catalog applies them
// when the tool appears. Every change is pushed into the catalog, which
// recomputes exposed names and tells the gateway.
package overrides

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

// MaxDescription is the longest description override, in characters.
const MaxDescription = 2000

var (
	// ErrAliasTaken wraps the message naming what already uses the alias.
	ErrAliasTaken = errors.New("alias is taken")
	// ErrInvalid wraps validation failures.
	ErrInvalid = errors.New("invalid override")
)

// Entry is one stored override.
type Entry struct {
	Node        string    `json:"node"`
	MCP         string    `json:"mcp"`
	Tool        string    `json:"tool"` // original tool name
	Alias       string    `json:"alias"`
	Description string    `json:"description"`
	Updated     time.Time `json:"updated"`
	UpdatedBy   string    `json:"updated_by"`
}

// Options configure a Store.
type Options struct {
	Catalog *catalog.Catalog // required
	Logger  *slog.Logger     // nil = slog.Default()
	// OnChange is called after the set changes, from a write here or one
	// noticed by Watch.
	OnChange func()
}

// Store is the tool_overrides table, mirrored into the catalog.
type Store struct {
	db     *store.DB
	opts   Options
	logger *slog.Logger

	// mu serializes reloads so the catalog receives sets in the order they
	// were read.
	mu   sync.Mutex
	last map[catalog.ToolKey]catalog.Override
}

// Open pushes the stored set into the catalog.
func Open(ctx context.Context, db *store.DB, opts Options) (*Store, error) {
	if opts.Catalog == nil {
		return nil, errors.New("open overrides: no catalog")
	}
	s := &Store{db: db, opts: opts, logger: opts.Logger}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	if _, err := s.reload(ctx, true); err != nil {
		return nil, fmt.Errorf("open overrides: %w", err)
	}
	return s, nil
}

// reload pushes the table into the catalog when it differs from what was
// pushed last (or when force is set) and reports whether it did.
func (s *Store) reload(ctx context.Context, force bool) (bool, error) {
	// Read and publish under one lock: a reload that read before a write
	// but published after the write's own reload would put the older set
	// back.
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.List(ctx)
	if err != nil {
		return false, err
	}
	next := make(map[catalog.ToolKey]catalog.Override, len(entries))
	for _, e := range entries {
		next[catalog.ToolKey{Node: e.Node, MCP: e.MCP, Tool: e.Tool}] = catalog.Override{Name: e.Alias, Description: e.Description}
	}
	if !force && s.last != nil && maps.Equal(s.last, next) {
		return false, nil
	}
	s.last = next
	s.opts.Catalog.SetOverrides(maps.Clone(next))
	return true, nil
}

// List returns every override, sorted by node, MCP and tool.
func (s *Store) List(ctx context.Context) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT node, mcp, tool, alias, description, updated_at, updated_by
		FROM tool_overrides ORDER BY node, mcp, tool`)
	if err != nil {
		return nil, fmt.Errorf("list overrides: %w", err)
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var at int64
		if err := rows.Scan(&e.Node, &e.MCP, &e.Tool, &e.Alias, &e.Description, &at, &e.UpdatedBy); err != nil {
			return nil, fmt.Errorf("list overrides: %w", err)
		}
		e.Updated = store.FromMillis(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

func validKeyPart(s string) bool {
	return s != "" && len(s) <= 128 && !strings.ContainsFunc(s, unicode.IsControl)
}

func validDescription(d string) error {
	if utf8.RuneCountInString(d) > MaxDescription {
		return fmt.Errorf("%w: description is longer than %d characters", ErrInvalid, MaxDescription)
	}
	for _, r := range d {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return fmt.Errorf("%w: description has a control character", ErrInvalid)
		}
	}
	return nil
}

// Set stores the override for a tool; an empty alias and description delete
// it. The returned Entry is zero when it deleted.
func (s *Store) Set(ctx context.Context, key catalog.ToolKey, alias, description, by string) (Entry, error) {
	if !validKeyPart(key.Node) || !validKeyPart(key.MCP) || !validKeyPart(key.Tool) {
		return Entry{}, fmt.Errorf("%w: bad node, MCP or tool", ErrInvalid)
	}
	if alias == "" && description == "" {
		return Entry{}, s.Delete(ctx, key)
	}
	if alias != "" && !catalog.ValidAlias(alias) {
		return Entry{}, fmt.Errorf("%w: alias must be 1 to %d letters, digits, _ or -", ErrInvalid, catalog.MaxNameLen)
	}
	if err := validDescription(description); err != nil {
		return Entry{}, err
	}
	if alias != "" {
		// Another tool already exposed under this name (an alias or a
		// generated name). The unique index covers rows not in the catalog.
		if t, ok := s.opts.Catalog.Lookup(alias); ok && t.Upstream != nil &&
			(catalog.ToolKey{Node: t.Node, MCP: t.MCP, Tool: t.Upstream.Name}) != key {
			return Entry{}, fmt.Errorf("%w: %q is already the name of %s/%s/%s", ErrAliasTaken, alias, t.Node, t.MCP, t.Upstream.Name)
		}
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO tool_overrides (node, mcp, tool, alias, description, updated_at, updated_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (node, mcp, tool) DO UPDATE SET alias = excluded.alias, description = excluded.description,
			updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		key.Node, key.MCP, key.Tool, alias, description, store.Millis(now), by)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: tool_overrides.alias") {
			return Entry{}, fmt.Errorf("%w: %q is another tool's alias", ErrAliasTaken, alias)
		}
		return Entry{}, fmt.Errorf("set override: %w", err)
	}
	s.refresh(ctx)
	return Entry{Node: key.Node, MCP: key.MCP, Tool: key.Tool, Alias: alias, Description: description,
		Updated: store.FromMillis(store.Millis(now)), UpdatedBy: by}, nil
}

// Delete removes a tool's override; a missing one is not an error.
func (s *Store) Delete(ctx context.Context, key catalog.ToolKey) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM tool_overrides WHERE node = ? AND mcp = ? AND tool = ?",
		key.Node, key.MCP, key.Tool); err != nil {
		return fmt.Errorf("delete override: %w", err)
	}
	s.refresh(ctx)
	return nil
}

// refresh pushes after a write of ours. The write is committed, so a failed
// reload must not fail it, and the request's context may be done.
func (s *Store) refresh(ctx context.Context) {
	changed, err := s.reload(context.WithoutCancel(ctx), false)
	if err != nil {
		s.logger.Warn("overrides not reloaded after a change", "error", err)
		return
	}
	if changed && s.opts.OnChange != nil {
		s.opts.OnChange()
	}
}

// Watch reloads when another process changes the database, until ctx ends.
// The caller owns the goroutine.
func (s *Store) Watch(ctx context.Context, interval time.Duration) {
	err := s.db.Watch(ctx, interval, func() error {
		changed, err := s.reload(ctx, false)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("overrides not reloaded, keeping the current set; retrying", "error", err)
			}
			return err
		}
		if changed && s.opts.OnChange != nil {
			s.opts.OnChange()
		}
		return nil
	})
	if err != nil {
		s.logger.Warn("database watch stopped; other processes' override changes are no longer noticed", "error", err)
	}
}
