package adminlog

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

func TestRecordQuery(t *testing.T) {
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), store.FileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	l := New(db, nil, 0)
	old := Entry{Time: time.Now().Add(-400 * 24 * time.Hour), Actor: "x", Action: SignIn}
	l.Record(t.Context(), old) // pruned by the next write
	for i, a := range []string{SignIn, KeyCreate, KeyRevoke} {
		l.Record(t.Context(), Entry{Actor: "alice", Action: a, Target: string(rune('a' + i)), IP: "10.0.0.1"})
	}
	got, err := l.Query(t.Context(), 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Action != KeyRevoke || got[1].Action != KeyCreate || got[0].IP != "10.0.0.1" {
		t.Fatalf("page 1 = %+v", got)
	}
	rest, err := l.Query(t.Context(), got[1].ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0].Action != SignIn || rest[0].Actor != "alice" {
		t.Fatalf("page 2 = %+v (the old entry should be pruned)", rest)
	}
}
