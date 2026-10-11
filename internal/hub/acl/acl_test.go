package acl

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
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

func openStore(t *testing.T, db *store.DB, opts Options) *Store {
	t.Helper()
	s, err := Open(t.Context(), db, opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func addKey(t *testing.T, db *store.DB, id string) {
	t.Helper()
	_, err := db.Exec("INSERT INTO keys (id, kind, name, hash, created_at) VALUES (?, 'client', ?, ?, 1)", id, id, "hash-"+id)
	if err != nil {
		t.Fatal(err)
	}
}

func mustCreate(t *testing.T, s *Store, name string, grants ...Grant) Group {
	t.Helper()
	g, err := s.Create(t.Context(), GroupInput{Name: name, Grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestEverythingAfterOpen(t *testing.T) {
	db := openDB(t)
	s := openStore(t, db, Options{})
	g, err := s.Get(t.Context(), Everything)
	if err != nil {
		t.Fatal(err)
	}
	if g.Name != "Everything" || !slices.Equal(g.Grants, []Grant{{"*", "*", "*"}}) {
		t.Fatalf("got %+v", g)
	}
}

func TestGrantValidation(t *testing.T) {
	long := strings.Repeat("x", 129)
	tests := []struct {
		name  string
		grant Grant
		ok    bool
	}{
		{"exact", Grant{"n", "m", "t"}, true},
		{"tool star", Grant{"n", "m", "*"}, true},
		{"mcp star", Grant{"n", "*", "*"}, true},
		{"all star", Grant{"*", "*", "*"}, true},
		{"max length", Grant{strings.Repeat("x", 128), "m", "t"}, true},
		{"empty node", Grant{"", "m", "t"}, false},
		{"empty mcp", Grant{"n", "", "t"}, false},
		{"empty tool", Grant{"n", "m", ""}, false},
		{"too long", Grant{"n", long, "t"}, false},
		{"control", Grant{"n", "m", "a\x00b"}, false},
		{"newline", Grant{"n", "m", "a\nb"}, false},
		{"node star needs mcp star", Grant{"*", "m", "*"}, false},
		{"node star needs tool star", Grant{"*", "*", "t"}, false},
		{"mcp star needs tool star", Grant{"n", "*", "t"}, false},
	}
	s := openStore(t, openDB(t), Options{})
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Create(t.Context(), GroupInput{Name: "g" + string(rune('a'+i)), Grants: []Grant{tc.grant}})
			if tc.ok && err != nil {
				t.Fatal(err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestNameValidation(t *testing.T) {
	tests := []struct {
		name, desc string
		ok         bool
	}{
		{"ok", "", true},
		{"  padded  ", "", true},
		{"", "", false},
		{"   ", "", false},
		{strings.Repeat("é", 64), "", true},
		{strings.Repeat("é", 65), "", false},
		{"bad\tname", "", false},
		{"fine", strings.Repeat("d", 500), true},
		{"fine2", strings.Repeat("d", 501), false},
	}
	s := openStore(t, openDB(t), Options{})
	for _, tc := range tests {
		_, err := s.Create(t.Context(), GroupInput{Name: tc.name, Description: tc.desc})
		if tc.ok && err != nil {
			t.Errorf("%q: %v", tc.name, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: err = %v, want ErrInvalid", tc.name, err)
		}
	}
	g, err := s.Create(t.Context(), GroupInput{Name: "  trimmed "})
	if err != nil || g.Name != "trimmed" {
		t.Fatalf("got %+v, %v", g, err)
	}
}

func TestCreateUpdateDelete(t *testing.T) {
	ctx := t.Context()
	s := openStore(t, openDB(t), Options{})

	g, err := s.Create(ctx, GroupInput{Name: "Dev", Description: "d", Grants: []Grant{
		{"b", "m", "t"}, {"a", "*", "*"}, {"b", "m", "t"}, {"a", "m", "x"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(g.ID, "g_") || len(g.ID) != 10 {
		t.Errorf("id = %q", g.ID)
	}
	want := []Grant{{"a", "*", "*"}, {"a", "m", "x"}, {"b", "m", "t"}}
	if !slices.Equal(g.Grants, want) {
		t.Errorf("grants = %v, want %v", g.Grants, want)
	}

	// Case-insensitive conflicts.
	if _, err := s.Create(ctx, GroupInput{Name: "dEV"}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("create dup: %v", err)
	}
	other := mustCreate(t, s, "Ops")
	if _, err := s.Update(ctx, other.ID, GroupInput{Name: "DEV"}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("update dup: %v", err)
	}
	// Renaming to its own name in another case is fine.
	if _, err := s.Update(ctx, g.ID, GroupInput{Name: "dev"}); err != nil {
		t.Errorf("self rename: %v", err)
	}

	up, err := s.Update(ctx, g.ID, GroupInput{Name: "Dev2", Description: "new", Grants: nil})
	if err != nil {
		t.Fatal(err)
	}
	if up.Name != "Dev2" || up.Description != "new" || len(up.Grants) != 0 || !up.Created.Equal(g.Created) {
		t.Errorf("updated = %+v", up)
	}
	if _, err := s.Update(ctx, g.ID, GroupInput{Name: "x", Grants: []Grant{{"", "", ""}}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid update: %v", err)
	}

	// List is sorted by name ignoring case.
	mustCreate(t, s, "alpha")
	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, x := range list {
		names = append(names, x.Name)
	}
	if want := []string{"alpha", "Dev2", "Everything", "Ops"}; !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}

	if err := s.Delete(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, g.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted: %v", err)
	}
	if err := s.Delete(ctx, g.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete again: %v", err)
	}
	if _, err := s.Update(ctx, "g_nope", GroupInput{Name: "q"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update unknown: %v", err)
	}
	if _, err := s.Get(ctx, "g_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get unknown: %v", err)
	}
}

func TestAllows(t *testing.T) {
	db := openDB(t)
	s := openStore(t, db, Options{})
	for _, k := range []string{"k_exact", "k_mcp", "k_node", "k_none", "k_two"} {
		addKey(t, db, k)
	}
	exact := mustCreate(t, s, "exact", Grant{"n1", "m1", "t1"})
	mcp := mustCreate(t, s, "mcp", Grant{"n1", "m1", "*"})
	node := mustCreate(t, s, "node", Grant{"*", "*", "*"})
	other := mustCreate(t, s, "other", Grant{"n2", "m2", "t2"})
	ctx := t.Context()
	for key, groups := range map[string][]string{
		"k_exact": {exact.ID},
		"k_mcp":   {mcp.ID},
		"k_node":  {node.ID},
		"k_two":   {exact.ID, other.ID},
	} {
		if err := s.SetKeyGroups(ctx, key, groups); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		key, node, mcp, tool string
		want                 bool
	}{
		{"k_exact", "n1", "m1", "t1", true},
		{"k_exact", "n1", "m1", "t2", false},
		{"k_exact", "n2", "m1", "t1", false},
		{"k_mcp", "n1", "m1", "anything", true},
		{"k_mcp", "n1", "m2", "t1", false},
		{"k_mcp", "n2", "m1", "t1", false},
		{"k_node", "zz", "yy", "xx", true},
		{"k_none", "n1", "m1", "t1", false},
		{"unknown", "n1", "m1", "t1", false},
		{"k_two", "n1", "m1", "t1", true},
		{"k_two", "n2", "m2", "t2", true},
		{"k_two", "n2", "m2", "t1", false},
	}
	for _, tc := range tests {
		if got := s.Allows(tc.key, tc.node, tc.mcp, tc.tool); got != tc.want {
			t.Errorf("Allows(%s, %s/%s/%s) = %v, want %v", tc.key, tc.node, tc.mcp, tc.tool, got, tc.want)
		}
	}

	// Editing a group applies at once; deleting it removes the access.
	if _, err := s.Update(ctx, exact.ID, GroupInput{Name: "exact", Grants: []Grant{{"n1", "m1", "t9"}}}); err != nil {
		t.Fatal(err)
	}
	if s.Allows("k_exact", "n1", "m1", "t1") || !s.Allows("k_exact", "n1", "m1", "t9") {
		t.Error("update not reflected")
	}
	if err := s.Delete(ctx, exact.ID); err != nil {
		t.Fatal(err)
	}
	if s.Allows("k_exact", "n1", "m1", "t9") {
		t.Error("delete not reflected")
	}
	if err := s.SetKeyGroups(ctx, "k_node", nil); err != nil {
		t.Fatal(err)
	}
	if s.Allows("k_node", "a", "b", "c") {
		t.Error("empty group list should deny")
	}
}

func TestMemberships(t *testing.T) {
	db := openDB(t)
	s := openStore(t, db, Options{})
	addKey(t, db, "k_1")
	addKey(t, db, "k_2")
	a, b := mustCreate(t, s, "a"), mustCreate(t, s, "b")
	ctx := t.Context()
	if err := s.SetKeyGroups(ctx, "k_1", []string{b.ID, a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKeyGroups(ctx, "k_2", []string{a.ID}); err != nil {
		t.Fatal(err)
	}
	m, err := s.Memberships(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{a.ID, b.ID}
	slices.Sort(want)
	if !slices.Equal(m["k_1"], want) || !slices.Equal(m["k_2"], []string{a.ID}) || len(m) != 2 {
		t.Errorf("memberships = %v", m)
	}
	ga, _ := s.Get(ctx, a.ID)
	if !slices.Equal(ga.Keys, []string{"k_1", "k_2"}) {
		t.Errorf("keys = %v", ga.Keys)
	}
}

func TestSetKeyGroupsAtomic(t *testing.T) {
	db := openDB(t)
	s := openStore(t, db, Options{})
	addKey(t, db, "k_1")
	g := mustCreate(t, s, "g", Grant{"n", "m", "t"})
	ctx := t.Context()
	if err := s.SetKeyGroups(ctx, "k_1", []string{g.ID}); err != nil {
		t.Fatal(err)
	}

	err := s.SetKeyGroups(ctx, "k_1", []string{Everything, "g_unknown"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	m, _ := s.Memberships(ctx)
	if !slices.Equal(m["k_1"], []string{g.ID}) {
		t.Errorf("half applied: %v", m)
	}
	if !s.Allows("k_1", "n", "m", "t") || s.Allows("k_1", "x", "y", "z") {
		t.Error("snapshot changed by failed call")
	}

	if err := s.SetKeyGroups(ctx, "k_missing", []string{g.ID}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown key: %v", err)
	}
}

func TestKeyDeletionCascades(t *testing.T) {
	db := openDB(t)
	s := openStore(t, db, Options{})
	addKey(t, db, "k_1")
	if err := s.SetKeyGroups(t.Context(), "k_1", []string{Everything}); err != nil {
		t.Fatal(err)
	}
	if !s.Allows("k_1", "a", "b", "c") {
		t.Fatal("should allow")
	}
	if _, err := db.Exec("DELETE FROM keys WHERE id = 'k_1'"); err != nil {
		t.Fatal(err)
	}
	m, err := s.Memberships(t.Context())
	if err != nil || len(m) != 0 {
		t.Fatalf("memberships = %v, %v", m, err)
	}
	// A reload (Open on the same DB) drops the access.
	s2 := openStore(t, db, Options{})
	if s2.Allows("k_1", "a", "b", "c") {
		t.Error("deleted key still allowed after reload")
	}
}

// waitUntil polls cond until it holds or the deadline passes.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWatchSeesOtherProcess(t *testing.T) {
	db := openDB(t)
	var changes atomic.Int32
	s := openStore(t, db, Options{OnChange: func() { changes.Add(1) }})
	addKey(t, db, "k_1")

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.Watch(ctx, 5*time.Millisecond) }()
	defer func() { cancel(); <-done }()

	// A second Open of the same file stands in for the hub CLI process.
	other, err := store.Open(t.Context(), db.Path(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	cli := openStore(t, other, Options{})
	if err := cli.SetKeyGroups(t.Context(), "k_1", []string{Everything}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "Allows after external change", func() bool { return s.Allows("k_1", "a", "b", "c") })
	waitUntil(t, "OnChange", func() bool { return changes.Load() >= 1 })
	if got := changes.Load(); got != 1 {
		t.Errorf("OnChange called %d times, want 1", got)
	}

	// External change that doesn't affect grants: no further OnChange. Wait
	// for Watch to see the commit by checking the group list is visible, then
	// give it a few more polls through a second harmless write.
	if _, err := other.Exec("INSERT INTO settings (key, value) VALUES ('x', '1')"); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec("INSERT INTO settings (key, value) VALUES ('y', '1')"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // negative check: nothing to wait on
	if got := changes.Load(); got != 1 {
		t.Errorf("OnChange called %d times after unrelated write, want 1", got)
	}
}

// Sync sees another process's write at once, without Watch running, and
// reports it through OnChange like Watch would.
func TestSyncSeesOtherProcessAtOnce(t *testing.T) {
	db := openDB(t)
	var changes atomic.Int32
	s := openStore(t, db, Options{OnChange: func() { changes.Add(1) }})
	addKey(t, db, "k_1")
	other, err := store.Open(t.Context(), db.Path(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := openStore(t, other, Options{}).SetKeyGroups(t.Context(), "k_1", []string{Everything}); err != nil {
		t.Fatal(err)
	}
	if s.Allows("k_1", "a", "b", "c") {
		t.Fatal("the snapshot saw the other process's write before Sync")
	}
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !s.Allows("k_1", "a", "b", "c") {
		t.Fatal("Allows after Sync = false")
	}
	if got := changes.Load(); got != 1 {
		t.Fatalf("OnChange called %d times, want 1", got)
	}
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := changes.Load(); got != 1 {
		t.Fatalf("OnChange called %d times after a Sync with nothing new, want 1", got)
	}
}

func TestOnChangeOnlyWhenChanged(t *testing.T) {
	db := openDB(t)
	var changes atomic.Int32
	s := openStore(t, db, Options{OnChange: func() { changes.Add(1) }})
	addKey(t, db, "k_1")
	ctx := t.Context()

	if got := changes.Load(); got != 0 {
		t.Fatalf("OnChange called %d times by Open", got)
	}
	// A group nobody is in changes nothing.
	g := mustCreate(t, s, "empty", Grant{"n", "m", "t"})
	if _, err := s.Update(ctx, g.ID, GroupInput{Name: "empty2", Grants: []Grant{{"n", "m", "t"}}}); err != nil {
		t.Fatal(err)
	}
	if got := changes.Load(); got != 0 {
		t.Fatalf("OnChange called %d times, want 0", got)
	}
	if err := s.SetKeyGroups(ctx, "k_1", []string{g.ID}); err != nil {
		t.Fatal(err)
	}
	if got := changes.Load(); got != 1 {
		t.Fatalf("after membership: %d, want 1", got)
	}
	// Same membership again: nothing changed.
	if err := s.SetKeyGroups(ctx, "k_1", []string{g.ID}); err != nil {
		t.Fatal(err)
	}
	if got := changes.Load(); got != 1 {
		t.Fatalf("after repeat: %d, want 1", got)
	}
	if err := s.Delete(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if got := changes.Load(); got != 2 {
		t.Fatalf("after delete: %d, want 2", got)
	}
}
