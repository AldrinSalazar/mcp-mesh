package known

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/nodes"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

func openStore(t *testing.T) (*store.DB, *Store) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := Open(t.Context(), db.DB, nil)
	if err != nil {
		t.Fatal(err)
	}
	return db, s
}

func studio(online bool, at time.Time) nodes.NodeInfo {
	info := nodes.NodeInfo{Name: "studio", NodeVersion: "v1.1.0", OS: "darwin", RemoteAddr: "192.168.1.20:5000", KeyID: "k_1", Online: online,
		MCPs: []nodes.MCPInfo{{Name: "fusion", Transport: proto.TransportHTTP, Start: proto.StartLazy, Tools: 4, Server: "Fusion 2.0"}}}
	if !online {
		info.DisconnectedAt = at
	}
	return info
}

func written(t *testing.T, db *store.DB) (lastSeen int64) {
	t.Helper()
	if err := db.QueryRow(`SELECT last_seen FROM known_nodes WHERE name = 'studio'`).Scan(&lastSeen); err != nil {
		t.Fatal(err)
	}
	return lastSeen
}

func TestKnownNodesOutliveTheRegistry(t *testing.T) {
	db, s := openStore(t)
	t0 := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

	s.Observe(t.Context(), []nodes.NodeInfo{studio(true, time.Time{})}, t0)
	if got := written(t, db); got != t0.UnixMilli() {
		t.Fatalf("last_seen = %d, want %d", got, t0.UnixMilli())
	}
	// Seen again unchanged within a minute: nothing is written.
	s.Observe(t.Context(), []nodes.NodeInfo{studio(true, time.Time{})}, t0.Add(30*time.Second))
	if got := written(t, db); got != t0.UnixMilli() {
		t.Fatal("an unchanged node was written again")
	}
	// Still online a minute later: last seen moves on.
	t1 := t0.Add(time.Minute)
	s.Observe(t.Context(), []nodes.NodeInfo{studio(true, time.Time{})}, t1)
	if got := written(t, db); got != t1.UnixMilli() {
		t.Fatalf("last_seen = %d, want %d", got, t1.UnixMilli())
	}
	// It disconnects: last seen is when it went, and the registry then forgets it.
	gone := t1.Add(10 * time.Second)
	s.Observe(t.Context(), []nodes.NodeInfo{studio(false, gone)}, gone.Add(time.Second))
	s.Observe(t.Context(), nil, gone.Add(time.Hour))

	// A restarted hub still knows it, with its MCPs.
	again, err := Open(t.Context(), db.DB, nil)
	if err != nil {
		t.Fatal(err)
	}
	list := again.List()
	if len(list) != 1 {
		t.Fatalf("known = %+v", list)
	}
	n := list[0]
	if n.Name != "studio" || !n.LastSeen.Equal(gone) || !n.FirstSeen.Equal(t0) || n.OS != "darwin" || n.KeyID != "k_1" {
		t.Fatalf("node = %+v", n)
	}
	if len(n.MCPs) != 1 || n.MCPs[0] != (MCP{Name: "fusion", Transport: "http", Start: "lazy", Server: "Fusion 2.0", Tools: 4}) {
		t.Fatalf("mcps = %+v", n.MCPs)
	}

	forgot, err := again.Forget(t.Context(), "studio")
	if err != nil || !forgot || len(again.List()) != 0 {
		t.Fatalf("Forget = %v, %v; list %v", forgot, err, again.List())
	}
	if forgot, _ := again.Forget(t.Context(), "studio"); forgot {
		t.Fatal("forgot a node twice")
	}
}

func TestKnownNodeKeepsMCPsWhileOffline(t *testing.T) {
	_, s := openStore(t)
	t0 := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	s.Observe(t.Context(), []nodes.NodeInfo{studio(true, time.Time{})}, t0)
	offline := studio(false, t0.Add(time.Minute))
	offline.MCPs = nil
	s.Observe(t.Context(), []nodes.NodeInfo{offline}, t0.Add(2*time.Minute))
	if n := s.List()[0]; len(n.MCPs) != 1 || !n.LastSeen.Equal(t0.Add(time.Minute)) {
		t.Fatalf("node = %+v", n)
	}
}

func TestUpgradeKnowsNodesByTheirKeys(t *testing.T) {
	db, _ := openStore(t)
	// What migration 2 does for a hub that had keys before it.
	if _, err := db.Exec(`INSERT INTO keys (id, kind, name, hash, created_at, last_used_at) VALUES
		('k_a', 'node', 'desk', 'h1', 1000, 5000), ('k_b', 'node', 'unused', 'h2', 1000, NULL), ('k_c', 'client', 'claude', 'h3', 1000, 6000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(backfill(t)); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), db.DB, nil)
	if err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if len(list) != 1 || list[0].Name != "desk" || list[0].KeyID != "k_a" || list[0].LastSeen.UnixMilli() != 5000 {
		t.Fatalf("known = %+v", list)
	}
}

// backfill is the INSERT of migration 2, run again on a database that
// already has keys.
func backfill(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../store/migrations/0002_known_nodes.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, insert, ok := strings.Cut(string(data), "INSERT INTO known_nodes")
	if !ok {
		t.Fatal("migration 2 has no backfill")
	}
	return "INSERT INTO known_nodes" + insert
}
