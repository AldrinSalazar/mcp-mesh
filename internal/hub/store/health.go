package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// watchers tracks each running Watch, for Health.
type watchers struct {
	mu  sync.Mutex
	all map[*watchState]struct{}
}

type watchState struct {
	err error // why it is failing (polling, or its callback); nil = fine
}

func (w *watchers) add() *watchState {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.all == nil {
		w.all = map[*watchState]struct{}{}
	}
	s := &watchState{}
	w.all[s] = struct{}{}
	return s
}

func (w *watchers) set(s *watchState, err error) {
	w.mu.Lock()
	s.err = err
	w.mu.Unlock()
}

func (w *watchers) remove(s *watchState) {
	w.mu.Lock()
	delete(w.all, s)
	w.mu.Unlock()
}

// FailingWatchers counts the Watch loops that can't poll the database, whose
// last callback failed (a reload keeping stale state), or that stopped with
// an error (they stay counted: changes from other processes go unnoticed).
// It returns the first such error too.
func (db *DB) FailingWatchers() (int, error) {
	db.watch.mu.Lock()
	defer db.watch.mu.Unlock()
	n := 0
	var first error
	for s := range db.watch.all {
		if s.err != nil {
			n++
			if first == nil {
				first = s.err
			}
		}
	}
	return n, first
}

// CheckWritable takes and releases the database's write lock (BEGIN
// IMMEDIATE), so a read-only file, a full disk on the journal or a lock held
// too long by another process shows up.
func (db *DB) CheckWritable(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := db.Tx(ctx, func(*sql.Tx) error { return nil }); err != nil {
		return fmt.Errorf("database %s is not writable: %w", db.path, err)
	}
	return nil
}
