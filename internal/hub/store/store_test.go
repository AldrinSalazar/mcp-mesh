package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.Context(), filepath.Join(t.TempDir(), "sub", FileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenCreatesPrivateFileAndSchema(t *testing.T) {
	db := openTemp(t)
	info, err := os.Stat(db.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
	dir, err := os.Stat(filepath.Dir(db.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir mode = %o, want 700", perm)
	}
	all, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(all) {
		t.Errorf("user_version = %d, want %d", version, len(all))
	}
	for _, table := range []string{"keys", "audit"} {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v; want wal", mode, err)
	}
}

func TestReopenKeepsDataAndSkipsMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	db, err := Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO keys (id, kind, name, hash, created_at) VALUES ('k_1', 'node', 'pc', 'h', 1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM keys WHERE id = 'k_1'`).Scan(&name); err != nil || name != "pc" {
		t.Fatalf("name = %q, %v", name, err)
	}
}

func TestNewerSchemaRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	db, err := Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(t.Context(), path, nil); err == nil {
		t.Fatal("opened a database with a newer schema")
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	db := openTemp(t)
	boom := errors.New("boom")
	err := db.Tx(t.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO keys (id, kind, name, hash, created_at) VALUES ('k_1', 'node', 'pc', 'h', 1)`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	var n int
	db.QueryRow("SELECT count(*) FROM keys").Scan(&n)
	if n != 0 {
		t.Fatalf("%d keys after rollback", n)
	}
}

func TestConcurrentWritersDoNotFail(t *testing.T) {
	db := openTemp(t)
	// A second handle stands in for the key CLI in another process.
	other, err := Open(t.Context(), db.Path(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := range 40 {
		h := db
		if i%2 == 1 {
			h = other
		}
		wg.Go(func() {
			errs <- h.Tx(t.Context(), func(tx *sql.Tx) error {
				var n int
				if err := tx.QueryRow("SELECT count(*) FROM keys").Scan(&n); err != nil {
					return err
				}
				_, err := tx.Exec(`INSERT INTO keys (id, kind, name, hash, created_at) VALUES (?, 'client', 'c', ?, 1)`,
					fmt.Sprintf("k_%d", i), fmt.Sprintf("h%d", i))
				return err
			})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}

func TestWatchSeesOtherConnectionsCommits(t *testing.T) {
	db := openTemp(t)
	other, err := Open(t.Context(), db.Path(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	ctx, cancel := context.WithCancel(t.Context())
	changed := make(chan struct{}, 10)
	done := make(chan error)
	go func() { done <- db.Watch(ctx, 10*time.Millisecond, func() error { changed <- struct{}{}; return nil }) }()

	// The first call comes once the starting point is taken, so callers can
	// catch up with commits made before Watch began.
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("no initial changed call")
	}

	// Keep writing until one is noticed.
	n := 0
	for _, h := range []*DB{other, db} { // another process, then this one's pool
		deadline := time.After(5 * time.Second)
		tick := time.NewTicker(20 * time.Millisecond)
	wait:
		for {
			n++
			if _, err := h.Exec(`INSERT INTO keys (id, kind, name, hash, created_at) VALUES (?, 'client', 'c', ?, 1)`,
				fmt.Sprintf("k_%d", n), fmt.Sprintf("h%d", n)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-changed:
				break wait
			case <-tick.C:
			case <-deadline:
				t.Fatalf("writes through %s not noticed", h.Path())
			}
		}
		tick.Stop()
		// Drain notifications for the extra writes before the next handle.
		for len(changed) > 0 {
			<-changed
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMillisRoundTrip(t *testing.T) {
	if Millis(time.Time{}) != 0 || !FromMillis(0).IsZero() {
		t.Fatal("zero time does not map to 0")
	}
	now := time.Now().Truncate(time.Millisecond).UTC()
	if got := FromMillis(Millis(now)); !got.Equal(now) {
		t.Fatalf("round trip %v -> %v", now, got)
	}
}

// A callback whose reload fails is called again on the next ticks, without
// another commit, until it succeeds; a change must not be lost to one failed
// read.
func TestWatchRetriesFailedCallback(t *testing.T) {
	db := openTemp(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := make(chan int, 10)
	n := 0
	done := make(chan error, 1)
	go func() {
		done <- db.Watch(ctx, 5*time.Millisecond, func() error {
			n++
			calls <- n
			if n < 3 {
				return errors.New("reload failed")
			}
			return nil
		})
	}()
	for want := 1; want <= 3; want++ {
		select {
		case got := <-calls:
			if got != want {
				t.Fatalf("call %d, want %d", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("call %d never came", want)
		}
	}
	// Succeeded: no more calls without a commit.
	select {
	case got := <-calls:
		t.Fatalf("call %d after a successful one, with no commit", got)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Another process can migrate the database between this one reading the
// schema version and applying: each migration checks again under the write
// lock instead of failing on tables that already exist.
func TestMigrateAfterAnotherProcessMigrated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	var other *DB
	afterVersionRead = func() {
		afterVersionRead = nil // the other Open runs the hook too
		var err error
		if other, err = Open(t.Context(), path, nil); err != nil {
			t.Errorf("other process: %v", err)
		}
	}
	t.Cleanup(func() { afterVersionRead = nil })
	db, err := Open(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("open after another process migrated: %v", err)
	}
	defer db.Close()
	if other != nil {
		defer other.Close()
	}
	all, _ := migrations()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != len(all) {
		t.Fatalf("user_version = %d, %v", v, err)
	}
}

// A watcher whose reloads keep failing is reported until it recovers or
// stops; one that ended normally is not.
func TestFailingWatchersAreReported(t *testing.T) {
	db := openTemp(t)
	if err := db.CheckWritable(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	var fail atomic.Bool
	fail.Store(true)
	done := make(chan error, 1)
	go func() {
		done <- db.Watch(ctx, 5*time.Millisecond, func() error {
			if fail.Load() {
				return errors.New("grants table unreadable")
			}
			return nil
		})
	}()
	waitUntil := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitUntil("failing watcher not reported", func() bool {
		n, err := db.FailingWatchers()
		return n == 1 && err != nil && strings.Contains(err.Error(), "grants table unreadable")
	})
	fail.Store(false)
	waitUntil("recovered watcher still reported", func() bool { n, _ := db.FailingWatchers(); return n == 0 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n, _ := db.FailingWatchers(); n != 0 {
		t.Errorf("stopped watcher reported: %d", n)
	}
}
