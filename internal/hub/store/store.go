// Package store owns the hub's SQLite database: opening it, its schema
// (embedded, numbered migrations) and noticing commits made elsewhere. The
// running hub and its CLI share the file, so the packages that own the data
// (auth, activity, later acl) run their own queries on [DB] and the hub
// learns about CLI changes through [DB.Watch].
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// FileName is the database's name inside the hub's data directory.
const FileName = "hub.db"

//go:embed migrations/*.sql
var migrationFiles embed.FS

// DB is the hub database. Its embedded *sql.DB is safe for concurrent use;
// write transactions take the write lock up front (BEGIN IMMEDIATE), so two
// writers wait for each other through busy_timeout instead of failing.
type DB struct {
	*sql.DB
	path   string
	logger *slog.Logger
	watch  watchers
}

// Open opens or creates the database at path and brings its schema up to
// date. The directory is created 0700 and the file 0600: it holds key
// hashes and, later, password hashes.
func Open(ctx context.Context, path string, logger *slog.Logger) (*DB, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	// SQLite creates the file with the process umask; create it first so it
	// is never readable by others, even briefly.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	f.Close()

	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	sqlDB, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath()+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	db := &DB{DB: sqlDB, path: path, logger: logger}
	if err := db.migrate(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// Path is the database file.
func (db *DB) Path() string { return db.path }

type migration struct {
	version int
	name    string
	sql     string
}

func migrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %s: name must start with a positive number and _", e.Name())
		}
		data, err := migrationFiles.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(data)})
	}
	slices.SortFunc(out, func(a, b migration) int { return a.version - b.version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration %s: expected version %d", m.name, i+1)
		}
	}
	return out, nil
}

// afterVersionRead, if set (tests), runs after migrate reads the schema
// version and before it applies anything.
var afterVersionRead func()

// migrate applies the migrations newer than the database's user_version,
// each in its own transaction. A database from a newer hub is refused rather
// than used with a schema this binary doesn't know. Two processes may start
// on a new database at once (the hub and the key CLI), so each transaction
// (BEGIN IMMEDIATE, so the write lock is held) checks the version again and
// skips a migration the other one already applied.
func (db *DB) migrate(ctx context.Context) error {
	all, err := migrations()
	if err != nil {
		return err
	}
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema version of %s: %w", db.path, err)
	}
	if current > len(all) {
		return fmt.Errorf("%s has schema version %d, newer than this hub knows (%d): upgrade mcp-mesh-hub", db.path, current, len(all))
	}
	if afterVersionRead != nil {
		afterVersionRead()
	}
	for _, m := range all[current:] {
		applied := false
		err := db.Tx(ctx, func(tx *sql.Tx) error {
			var now int
			if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&now); err != nil {
				return err
			}
			if now >= m.version {
				return nil
			}
			applied = true
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return err
			}
			// PRAGMA takes no bound parameters; the version is an int.
			_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version))
			return err
		})
		if err != nil {
			return fmt.Errorf("apply migration %s to %s: %w", m.name, db.path, err)
		}
		if applied {
			db.logger.Debug("applied database migration", "migration", m.name)
		}
	}
	return nil
}

// Tx runs fn in a write transaction, committing if it returns nil.
func (db *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, rollback(tx))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func rollback(tx *sql.Tx) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("rollback: %w", err)
	}
	return nil
}

// Watch calls changed after any commit made through another connection: the
// key CLI in another process, or another connection of this process's pool.
// It polls SQLite's data_version on a connection it holds for itself, every
// interval, until ctx ends. The caller owns the goroutine. changed runs on
// Watch's goroutine; one call may cover several commits. When changed
// returns an error (its reload failed), it is called again on every tick
// until it succeeds, even without another commit.
//
// changed is also called once right after Watch takes its starting point,
// so a caller that loaded its state before calling Watch catches up with
// commits made in between (otherwise a key revoked by the CLI in that
// moment would never be noticed).
func (db *DB) Watch(ctx context.Context, interval time.Duration, changed func() error) (err error) {
	state := db.watch.add()
	defer func() {
		// A watch that stopped with an error stays reported: nothing notices
		// other processes' changes any more.
		if err != nil {
			db.watch.set(state, fmt.Errorf("watch stopped: %w", err))
		} else {
			db.watch.remove(state)
		}
	}()
	call := func() bool {
		cerr := changed()
		if cerr != nil {
			db.watch.set(state, fmt.Errorf("reload failed: %w", cerr))
		} else {
			db.watch.set(state, nil)
		}
		return cerr != nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("watch %s: %w", db.path, err)
	}
	defer conn.Close()
	var last int64
	if err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&last); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("watch %s: %w", db.path, err)
	}
	pending := call()
	t := time.NewTicker(interval)
	defer t.Stop()
	warned := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		var v int64
		if err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&v); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if !warned {
				db.logger.Warn("database change check failed", "path", db.path, "error", err)
				warned = true
			}
			db.watch.set(state, fmt.Errorf("change check failed: %w", err))
			continue
		}
		warned = false
		if v != last || pending {
			last = v
			pending = call()
		} else {
			db.watch.set(state, nil)
		}
	}
}

// Millis and FromMillis convert between time.Time and the schema's Unix
// milliseconds. The zero time is stored as 0.
func Millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func FromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
