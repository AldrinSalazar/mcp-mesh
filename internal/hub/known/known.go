// Package known remembers every node that has connected to the hub, so the
// dashboard still lists a node while it is offline. The registry only knows
// connected nodes: it forgets one after its offline grace period, and all
// of them when the hub restarts.
package known

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/nodes"
)

// Node is what the hub last saw of a node.
type Node struct {
	Name        string
	NodeVersion string
	OS          string
	RemoteAddr  string
	KeyID       string
	FirstSeen   time.Time
	// LastSeen is when the node was last online.
	LastSeen time.Time
	MCPs     []MCP // sorted by name
}

// MCP is one of a node's MCPs as last listed.
type MCP struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Sharing   string `json:"sharing"`
	Start     string `json:"start"`
	Server    string `json:"server,omitempty"`
	Tools     int    `json:"tools"`
	Prompts   int    `json:"prompts"`
	Resources int    `json:"resources"`
}

// refresh is how often an online node's LastSeen is written, so a hub that
// stops without seeing it disconnect knows roughly when it was last there.
const refresh = time.Minute

// Store keeps known nodes in the hub database, and a copy in memory for
// listing.
type Store struct {
	db  *sql.DB
	log *slog.Logger

	mu     sync.Mutex
	nodes  map[string]Node
	online map[string]bool // as of the last Observe
}

// Open loads the known nodes.
func Open(ctx context.Context, db *sql.DB, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Store{db: db, log: logger, nodes: map[string]Node{}, online: map[string]bool{}}
	rows, err := db.QueryContext(ctx, `SELECT name, node_version, os, remote_addr, key_id, first_seen, last_seen, mcps FROM known_nodes`)
	if err != nil {
		return nil, fmt.Errorf("load known nodes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var n Node
		var first, last int64
		var mcps string
		if err := rows.Scan(&n.Name, &n.NodeVersion, &n.OS, &n.RemoteAddr, &n.KeyID, &first, &last, &mcps); err != nil {
			return nil, fmt.Errorf("load known nodes: %w", err)
		}
		n.FirstSeen, n.LastSeen = time.UnixMilli(first), time.UnixMilli(last)
		if err := json.Unmarshal([]byte(mcps), &n.MCPs); err != nil {
			logger.Warn("known node's MCPs unreadable; listed without them", "node", n.Name, "error", err)
		}
		s.nodes[n.Name] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load known nodes: %w", err)
	}
	return s, nil
}

// List returns the known nodes sorted by name.
func (s *Store) List() []Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Node, 0, len(s.nodes))
	for _, name := range slices.Sorted(maps.Keys(s.nodes)) {
		n := s.nodes[name]
		n.MCPs = slices.Clone(n.MCPs)
		out = append(out, n)
	}
	return out
}

// Observe records what the registry shows now: a node that is new, changed,
// went offline, or has been online for a while since it was last written.
func (s *Store) Observe(ctx context.Context, infos []nodes.NodeInfo, now time.Time) {
	for _, info := range infos {
		s.mu.Lock()
		old, seen := s.nodes[info.Name]
		wasOnline := s.online[info.Name]
		s.online[info.Name] = info.Online
		s.mu.Unlock()
		n := fromInfo(info, now)
		n.FirstSeen = now
		if seen {
			n.FirstSeen = old.FirstSeen
		}
		if !info.Online {
			// Offline (in the registry's grace period): last seen when it
			// disconnected, or as written before if that's later.
			n.LastSeen = info.DisconnectedAt
			if seen && old.LastSeen.After(n.LastSeen) {
				n.LastSeen = old.LastSeen
			}
			if n.LastSeen.IsZero() {
				n.LastSeen = now
			}
			if seen && len(n.MCPs) == 0 {
				n.MCPs = old.MCPs
			}
		}
		changed := !seen || wasOnline != info.Online || !sameNode(old, n) ||
			(info.Online && now.Sub(old.LastSeen) >= refresh)
		if !changed {
			continue
		}
		if err := s.write(ctx, n); err != nil {
			s.log.Warn("remember node", "node", n.Name, "error", err)
			continue
		}
		s.mu.Lock()
		s.nodes[n.Name] = n
		s.mu.Unlock()
	}
}

// fromInfo is a registry snapshot as a known node; LastSeen is now for an
// online node.
func fromInfo(info nodes.NodeInfo, now time.Time) Node {
	n := Node{Name: info.Name, NodeVersion: info.NodeVersion, OS: info.OS, RemoteAddr: info.RemoteAddr, KeyID: info.KeyID, LastSeen: now}
	for _, m := range info.MCPs {
		n.MCPs = append(n.MCPs, MCP{Name: m.Name, Transport: string(m.Transport), Sharing: string(m.Sharing), Start: string(m.Start),
			Server: m.Server, Tools: m.Tools, Prompts: m.Prompts, Resources: m.Resources})
	}
	slices.SortFunc(n.MCPs, func(a, b MCP) int { return strings.Compare(a.Name, b.Name) })
	return n
}

// sameNode compares what a node is, not when it was seen.
func sameNode(a, b Node) bool {
	a.FirstSeen, a.LastSeen, b.FirstSeen, b.LastSeen = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

func (s *Store) write(ctx context.Context, n Node) error {
	mcps, err := json.Marshal(n.MCPs)
	if err != nil {
		return err
	}
	if n.MCPs == nil {
		mcps = []byte("[]")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO known_nodes (name, node_version, os, remote_addr, key_id, first_seen, last_seen, mcps)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (name) DO UPDATE SET node_version = excluded.node_version, os = excluded.os, remote_addr = excluded.remote_addr,
	key_id = excluded.key_id, last_seen = excluded.last_seen, mcps = excluded.mcps`,
		n.Name, n.NodeVersion, n.OS, n.RemoteAddr, n.KeyID, n.FirstSeen.UnixMilli(), n.LastSeen.UnixMilli(), string(mcps))
	return err
}

// Forget removes a node, which the dashboard then lists only while it is
// connected. It reports whether the node was known.
func (s *Store) Forget(ctx context.Context, name string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM known_nodes WHERE name = ?`, name)
	if err != nil {
		return false, fmt.Errorf("forget node %s: %w", name, err)
	}
	s.mu.Lock()
	delete(s.nodes, name)
	delete(s.online, name)
	s.mu.Unlock()
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Run observes the registry every interval until ctx ends.
func (s *Store) Run(ctx context.Context, source func() []nodes.NodeInfo, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.Observe(ctx, source(), time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
