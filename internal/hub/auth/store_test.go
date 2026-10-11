package auth

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

// openDB opens a store over the database file at path.
func openDB(t *testing.T, path string) *Store {
	t.Helper()
	db, err := store.Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := Open(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// openTemp returns a store on a fresh database and the database's path.
func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", store.FileName)
	return openDB(t, path), path
}

func mustList(t *testing.T, s *Store) []Key {
	t.Helper()
	keys, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSecret(t *testing.T) {
	for _, kind := range []Kind{KindNode, KindClient} {
		secret, err := NewSecret(kind)
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^mm_` + string(kind) + `_[0-9A-Za-z]{43}$`).MatchString(secret) {
			t.Errorf("bad secret %q", secret)
		}
		if got, ok := KindOf(secret); !ok || got != kind {
			t.Errorf("KindOf(%q) = %q, %v", secret, got, ok)
		}
		other, _ := NewSecret(kind)
		if other == secret {
			t.Error("secrets repeat")
		}
	}
	if _, err := NewSecret("bogus"); err == nil {
		t.Error("bogus kind accepted")
	}
	for _, bad := range []string{"", "mm_node_short", "xx_node_" + strings.Repeat("a", 43), "mm_other_" + strings.Repeat("a", 43)} {
		if _, ok := KindOf(bad); ok {
			t.Errorf("KindOf(%q) ok", bad)
		}
	}
}

func TestHash(t *testing.T) {
	// SHA-256("abc")
	if got := Hash("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("Hash = %s", got)
	}
}

func TestCreateValidation(t *testing.T) {
	s, _ := openTemp(t)
	for _, tt := range []struct {
		name string
		kind Kind
		ok   bool
	}{
		{"laptop", KindNode, true},
		{"", KindNode, false},
		{"has space", KindClient, false},
		{"tab\tname", KindClient, false},
		{"ctl\x01", KindClient, false},
		{strings.Repeat("a", 64), KindClient, true},
		{strings.Repeat("a", 65), KindClient, false},
		{"x", "bogus", false},
	} {
		_, _, err := s.Create(tt.kind, tt.name)
		if (err == nil) != tt.ok {
			t.Errorf("Create(%q, %q): err = %v", tt.kind, tt.name, err)
		}
	}
}

func TestStoreLifecycle(t *testing.T) {
	s, path := openTemp(t)
	secret, key, err := s.Create(KindClient, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^k_[0-9a-f]{8}$`).MatchString(key.ID) || key.Hash != Hash(secret) {
		t.Errorf("key = %+v", key)
	}
	if !key.Created.Equal(key.Created.Truncate(time.Second)) {
		t.Errorf("created %v not truncated to the second", key.Created)
	}
	if got, ok := s.Lookup(secret); !ok || got != key {
		t.Errorf("Lookup = %+v, %v; want %+v", got, ok, key)
	}
	if _, ok := s.Lookup(secret + "x"); ok {
		t.Error("Lookup of a wrong secret succeeded")
	}
	if got, ok := s.Get(key.ID); !ok || got.Name != "laptop" {
		t.Errorf("Get = %+v, %v", got, ok)
	}
	if _, ok := s.Get("k_nothere"); ok {
		t.Error("Get of an unknown id succeeded")
	}
	for _, k := range []struct {
		kind Kind
		name string
	}{{KindNode, "zeta"}, {KindClient, "alpha"}, {KindClient, "beta"}} {
		if _, _, err := s.Create(k.kind, k.name); err != nil {
			t.Fatal(err)
		}
	}
	var order []string
	for _, k := range mustList(t, s) {
		order = append(order, string(k.Kind)+"/"+k.Name)
	}
	if got := strings.Join(order, " "); got != "client/alpha client/beta client/laptop node/zeta" {
		t.Errorf("order = %s", got)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v", info.Mode().Perm())
	}

	// A second Store on the same file sees the same keys (a restart).
	again := openDB(t, path)
	if got := mustList(t, again); len(got) != 4 {
		t.Errorf("reopened %d keys", len(got))
	}
	if got, ok := again.Lookup(secret); !ok || got.ID != key.ID {
		t.Errorf("Lookup after reopen = %+v, %v", got, ok)
	}
}

func TestRevoke(t *testing.T) {
	s, _ := openTemp(t)
	_, byID, _ := s.Create(KindClient, "one")
	_, byName, _ := s.Create(KindNode, "two")
	_, d1, _ := s.Create(KindClient, "dup")
	_, d2, _ := s.Create(KindNode, "dup")

	for _, tt := range []struct {
		arg     string
		want    string // revoked ID; empty when an error is expected
		errPart string
	}{
		{byID.ID, byID.ID, ""},
		{"two", byName.ID, ""},
		{"two", "", "no key with id or name"},
		{"missing", "", "no key with id or name"},
		{"dup", "", `2 keys are named "dup", revoke one by id: ` + d1.ID + " (client), " + d2.ID + " (node)"},
		{d2.ID, d2.ID, ""},
		{"dup", d1.ID, ""},
	} {
		got, err := s.Revoke(tt.arg)
		switch {
		case tt.want != "" && (err != nil || got.ID != tt.want):
			t.Errorf("Revoke(%q) = %+v, %v; want %s", tt.arg, got, err, tt.want)
		case tt.want == "" && (err == nil || !strings.Contains(err.Error(), tt.errPart)):
			t.Errorf("Revoke(%q) err = %v; want containing %q", tt.arg, err, tt.errPart)
		}
	}
	if got := mustList(t, s); len(got) != 0 {
		t.Errorf("keys left: %+v", got)
	}
}

func TestRevokeVisibleToOtherStore(t *testing.T) {
	hub, path := openTemp(t)
	secret, key, _ := hub.Create(KindClient, "c")
	if _, ok := hub.Lookup(secret); !ok {
		t.Fatal("lookup failed before revoke")
	}
	cli := openDB(t, path)
	if _, err := cli.Revoke(key.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := hub.Lookup(secret); ok {
		t.Error("Lookup succeeds right after another store revoked the key")
	}
	if _, ok := hub.Get(key.ID); ok {
		t.Error("Get succeeds right after another store revoked the key")
	}
}

// collector gathers onRevoke calls.
type collector struct {
	mu  sync.Mutex
	ids []string
}

func newCollector() *collector { return &collector{} }

func (c *collector) revoked(k Key) {
	c.mu.Lock()
	c.ids = append(c.ids, k.ID)
	c.mu.Unlock()
}

func (c *collector) got() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ids...)
}

func (c *collector) wait(t *testing.T, id string) {
	t.Helper()
	eventually(t, "revocation of "+id, func() bool {
		for _, g := range c.got() {
			if g == id {
				return true
			}
		}
		return false
	})
}

// watch runs s.Watch until the test ends or stop is called.
func watch(t *testing.T, s *Store, interval time.Duration, c *collector) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Watch(ctx, interval, c.revoked)
	}()
	stop = func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return stop
}

// awaitWatch returns once a running Watch has loaded the keys that exist now.
// A nudge is only taken from the channel by Watch's loop, which starts after
// the initial load; the second nudge is taken after the reload the first one
// caused has finished.
func awaitWatch(t *testing.T, s *Store, _ *collector) {
	t.Helper()
	for range 2 {
		select {
		case s.nudge <- struct{}{}:
		default:
		}
		eventually(t, "watch to take a nudge", func() bool { return len(s.nudge) == 0 })
	}
}

func assertOnce(t *testing.T, c *collector) {
	t.Helper()
	seen := map[string]int{}
	for _, id := range c.got() {
		seen[id]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("key %s reported %d times", id, n)
		}
	}
}

func TestWatchOtherStore(t *testing.T) {
	hub, path := openTemp(t)
	_, a, _ := hub.Create(KindNode, "a")
	_, b, _ := hub.Create(KindClient, "b")
	c := newCollector()
	watch(t, hub, 10*time.Millisecond, c)
	awaitWatch(t, hub, c)

	// Revoked from another connection, as the key CLI does.
	cli := openDB(t, path)
	if _, err := cli.Revoke(a.ID); err != nil {
		t.Fatal(err)
	}
	c.wait(t, a.ID)
	if _, err := cli.Revoke(b.ID); err != nil {
		t.Fatal(err)
	}
	c.wait(t, b.ID)
	// Later reloads don't report them again.
	awaitWatch(t, hub, c)
	assertOnce(t, c)
}

func TestWatchOwnRevokeIsImmediate(t *testing.T) {
	s, _ := openTemp(t)
	_, key, _ := s.Create(KindNode, "n")
	_, other, _ := s.Create(KindNode, "o")
	c := newCollector()
	// An hour-long interval: only the nudge can deliver this in time.
	watch(t, s, time.Hour, c)
	awaitWatch(t, s, c)

	if _, err := s.Revoke(key.ID); err != nil {
		t.Fatal(err)
	}
	c.wait(t, key.ID)
	if _, err := s.Revoke(other.ID); err != nil {
		t.Fatal(err)
	}
	c.wait(t, other.ID)
	assertOnce(t, c)
}

func TestTouchPersisted(t *testing.T) {
	s, path := openTemp(t)
	secret, key, _ := s.Create(KindClient, "c")
	before := time.Now().Add(-time.Second)
	s.Touch(key, "10.1.2.3")
	if got, ok := s.Get(key.ID); !ok || got.LastIP != "10.1.2.3" || got.LastUsed.Before(before) {
		t.Errorf("Get after Touch = %+v, %v", got, ok)
	}
	if got, ok := s.Lookup(secret); !ok || got.LastIP != "10.1.2.3" {
		t.Errorf("Lookup after Touch = %+v, %v", got, ok)
	}
	if got := mustList(t, s); got[0].LastIP != "10.1.2.3" || got[0].LastUsed.IsZero() {
		t.Errorf("List after Touch = %+v", got)
	}
	// Not written yet: Watch's loop does that.
	if got, _ := openDB(t, path).Get(key.ID); !got.LastUsed.IsZero() {
		t.Errorf("persisted before a flush: %+v", got)
	}

	// Stopping Watch flushes whatever is pending.
	stop := watch(t, s, time.Hour, newCollector())
	s.Touch(key, "10.9.9.9")
	stop()
	got, ok := openDB(t, path).Get(key.ID)
	if !ok || got.LastIP != "10.9.9.9" || got.LastUsed.Before(before) {
		t.Errorf("after restart: %+v, %v", got, ok)
	}
}

func TestTouchPersistsAtMostOncePerMinute(t *testing.T) {
	s, path := openTemp(t)
	_, key, _ := s.Create(KindClient, "c")
	other := openDB(t, path)
	s.Touch(key, "1.1.1.1")
	s.flush(t.Context(), false)
	if got, _ := other.Get(key.ID); got.LastIP != "1.1.1.1" {
		t.Fatalf("first touch not persisted: %+v", got)
	}
	s.Touch(key, "2.2.2.2")
	s.flush(t.Context(), false)
	if got, _ := other.Get(key.ID); got.LastIP != "1.1.1.1" {
		t.Errorf("second touch persisted within a minute: %+v", got)
	}
	if got, _ := s.Get(key.ID); got.LastIP != "2.2.2.2" {
		t.Errorf("in-memory value lost: %+v", got)
	}
	s.flush(t.Context(), true)
	if got, _ := other.Get(key.ID); got.LastIP != "2.2.2.2" {
		t.Errorf("forced flush did not persist: %+v", got)
	}
}

// A key another process creates and revokes between two loads was never in
// a load, but this hub authenticated with it, so its connections must close.
func TestWatchReportsUsedKeyRevokedBetweenLoads(t *testing.T) {
	hub, path := openTemp(t)
	c := newCollector()
	// An hour-long interval: loads happen only when the test nudges.
	watch(t, hub, time.Hour, c)
	awaitWatch(t, hub, c)

	cli := openDB(t, path)
	secret, used, _ := cli.Create(KindClient, "used")
	_, idle, _ := cli.Create(KindClient, "idle")
	_, kept, _ := cli.Create(KindClient, "kept")
	k, ok := hub.Lookup(secret)
	if !ok {
		t.Fatal("lookup failed")
	}
	hub.Touch(k, "10.0.0.1")
	k, _ = hub.Get(kept.ID)
	hub.Touch(k, "10.0.0.2")
	for _, id := range []string{used.ID, idle.ID} {
		if _, err := cli.Revoke(id); err != nil {
			t.Fatal(err)
		}
	}
	awaitWatch(t, hub, c)
	if got := c.got(); len(got) != 1 || got[0] != used.ID {
		t.Fatalf("reported %v, want only %s (%s was never used here, %s still exists)", got, used.ID, idle.ID, kept.ID)
	}
	if _, err := cli.Revoke(kept.ID); err != nil {
		t.Fatal(err)
	}
	// In a load by now, and touched: reported once.
	awaitWatch(t, hub, c)
	c.wait(t, kept.ID)
	assertOnce(t, c)
}

// A failing key reload is reported (health), and clears once a reload works.
func TestWatchReportsReloadFailure(t *testing.T) {
	s, _ := openTemp(t)
	watch(t, s, time.Hour, newCollector())
	awaitWatch(t, s, nil)
	if _, err := s.ReloadError(); err != nil {
		t.Fatalf("before: %v", err)
	}
	rename := func(from, to string) {
		t.Helper()
		if err := s.db.Tx(t.Context(), func(tx *sql.Tx) error {
			_, err := tx.Exec("ALTER TABLE " + from + " RENAME TO " + to)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	rename("keys", "keys_away")
	awaitWatch(t, s, nil)
	since, err := s.ReloadError()
	if err == nil || since.IsZero() {
		t.Fatalf("while the table is gone: %v, %v", since, err)
	}
	rename("keys_away", "keys")
	awaitWatch(t, s, nil)
	if _, err := s.ReloadError(); err != nil {
		t.Fatalf("after: %v", err)
	}
}
