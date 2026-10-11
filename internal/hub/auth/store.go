package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

const (
	// persistEvery bounds how often one key's last-used values are written,
	// so a busy client doesn't turn every request into a database write.
	persistEvery = time.Minute
	// flushTick is how often Watch looks for last-used values that are due.
	flushTick = 15 * time.Second
)

// Store holds the hub's keys in the hub database, which the running hub and
// the key CLI share. Reads go to the database every time so a revocation made
// by another process applies to the very next request.
type Store struct {
	db     *store.DB
	logger *slog.Logger

	// nudge wakes Watch when this Store changed the keys itself, so an
	// in-process revocation doesn't wait for the next poll.
	nudge chan struct{}

	mu sync.Mutex
	// touched holds every key this process authenticated a request with,
	// and its last-used values. Entries are dropped once their key is gone
	// (Watch's reload), which is also how Watch notices a key that was
	// created, used and revoked between two of its loads.
	touched map[string]*touch
	// loads counts Watch's loads; a touch remembers the count at its first
	// use, so a load knows whether its query started after that use.
	loads uint64
	// reloadErr is the last load's error (nil once one succeeds) and
	// failingSince when loads started failing, for health: while they fail,
	// revocations by other processes don't reach live connections.
	reloadErr    error
	failingSince time.Time
}

// ReloadError reports Watch's key reloads failing: since when, and the last
// error; a nil error while they succeed.
func (s *Store) ReloadError() (since time.Time, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failingSince, s.reloadErr
}

type touch struct {
	key       Key    // ID, kind and name, for onRevoke
	load      uint64 // Store.loads at the first touch
	at        time.Time
	ip        string
	seq       uint64 // counts touches, so a flush knows if a newer one arrived meanwhile
	saved     uint64
	persisted time.Time // when it was last written; zero if never
}

// Open returns the key store backed by db.
func Open(ctx context.Context, db *store.DB, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Store{db: db, logger: logger, nudge: make(chan struct{}, 1), touched: map[string]*touch{}}
	// Fail early on a database that lacks the table rather than on the first
	// request.
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM keys").Scan(&n); err != nil {
		return nil, fmt.Errorf("open keys: %w", err)
	}
	return s, nil
}

const keyColumns = "id, kind, name, hash, created_at, last_used_at, last_ip, oauth_client"

type scanner interface{ Scan(dest ...any) error }

func scanKey(r scanner) (Key, error) {
	var (
		k        Key
		kind     string
		created  int64
		lastUsed sql.NullInt64
		lastIP   sql.NullString
		oauth    sql.NullString
	)
	if err := r.Scan(&k.ID, &kind, &k.Name, &k.Hash, &created, &lastUsed, &lastIP, &oauth); err != nil {
		return Key{}, err
	}
	k.Kind = Kind(kind)
	k.Created = store.FromMillis(created)
	k.LastUsed = store.FromMillis(lastUsed.Int64)
	k.LastIP = lastIP.String
	k.OAuthClient = oauth.String
	return k, nil
}

// overlay applies last-used values not yet persisted.
func (s *Store) overlay(k Key) Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.touched[k.ID]; ok && !t.at.Before(k.LastUsed) {
		k.LastUsed, k.LastIP = t.at, t.ip
	}
	return k
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func queryKeys(ctx context.Context, q querier, where string, args ...any) ([]Key, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+keyColumns+" FROM keys "+where+" ORDER BY kind, name, created_at, id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []Key
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func validName(name string) error {
	switch {
	case name == "":
		return errors.New("name is empty")
	case len(name) > 64:
		return errors.New("name is longer than 64 bytes")
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("name %q contains whitespace or control characters", name)
		}
	}
	return nil
}

// Create makes a key and returns its secret, which is not stored and cannot
// be shown again.
func (s *Store) Create(kind Kind, name string) (string, Key, error) {
	return s.create(kind, name, "")
}

// CreateOAuth makes the client key behind an OAuth authorization of the
// given OAuth client. It has no usable secret (its hash is of a secret
// nobody keeps); the client authenticates with access tokens that map to
// it, so groups, revocation and the audit log treat it like any client key.
func (s *Store) CreateOAuth(name, oauthClient string) (Key, error) {
	if oauthClient == "" {
		return Key{}, errors.New("oauth client is empty")
	}
	_, key, err := s.create(KindClient, name, oauthClient)
	return key, err
}

func (s *Store) create(kind Kind, name, oauthClient string) (string, Key, error) {
	if !validKind(kind) {
		return "", Key{}, fmt.Errorf("unknown key kind %q (want node or client)", kind)
	}
	if err := validName(name); err != nil {
		return "", Key{}, err
	}
	secret, err := NewSecret(kind)
	if err != nil {
		return "", Key{}, err
	}
	var key Key
	err = s.db.Tx(context.Background(), func(tx *sql.Tx) error {
		for {
			var b [4]byte
			if _, err := rand.Read(b[:]); err != nil {
				return fmt.Errorf("random id: %w", err)
			}
			id := "k_" + hex.EncodeToString(b[:])
			var taken int
			if err := tx.QueryRow("SELECT count(*) FROM keys WHERE id = ?", id).Scan(&taken); err != nil {
				return err
			}
			if taken == 0 {
				key = Key{ID: id, Kind: kind, Name: name, Hash: Hash(secret), Created: time.Now().UTC().Truncate(time.Second), OAuthClient: oauthClient}
				break
			}
		}
		var oc any
		if oauthClient != "" {
			oc = oauthClient
		}
		_, err := tx.Exec("INSERT INTO keys (id, kind, name, hash, created_at, oauth_client) VALUES (?, ?, ?, ?, ?, ?)",
			key.ID, string(key.Kind), key.Name, key.Hash, store.Millis(key.Created), oc)
		return err
	})
	if err != nil {
		return "", Key{}, fmt.Errorf("create key: %w", err)
	}
	return secret, key, nil
}

// Revoke deletes a key by exact ID, else by name when exactly one key has it.
func (s *Store) Revoke(idOrName string) (Key, error) {
	ctx := context.Background()
	var key Key
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		matches, err := queryKeys(ctx, tx, "WHERE id = ?", idOrName)
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			if matches, err = queryKeys(ctx, tx, "WHERE name = ?", idOrName); err != nil {
				return err
			}
		}
		switch len(matches) {
		case 0:
			return fmt.Errorf("no key with id or name %q", idOrName)
		case 1:
			key = matches[0]
		default:
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = fmt.Sprintf("%s (%s)", m.ID, m.Kind)
			}
			return fmt.Errorf("%d keys are named %q, revoke one by id: %s", len(matches), idOrName, strings.Join(ids, ", "))
		}
		_, err = tx.Exec("DELETE FROM keys WHERE id = ?", key.ID)
		return err
	})
	if err != nil {
		return Key{}, err
	}
	select {
	case s.nudge <- struct{}{}:
	default:
	}
	return key, nil
}

// List returns all keys sorted by kind, name and creation time.
func (s *Store) List() ([]Key, error) {
	keys, err := queryKeys(context.Background(), s.db, "")
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}
	for i := range keys {
		keys[i] = s.overlay(keys[i])
	}
	return keys, nil
}

// Get returns the key with this ID. A database error counts as not found.
func (s *Store) Get(id string) (Key, bool) {
	return s.one("WHERE id = ?", id)
}

// Lookup finds the key for a presented secret by its hash. The index lookup
// is not constant-time, which doesn't matter here: the hash is of a 256-bit
// random secret, so timing can't help an attacker guess one.
func (s *Store) Lookup(secret string) (Key, bool) {
	return s.one("WHERE hash = ?", Hash(secret))
}

func (s *Store) one(where string, arg any) (Key, bool) {
	keys, err := queryKeys(context.Background(), s.db, where, arg)
	if err != nil {
		// Fail closed: a key we can't read is not a key.
		s.logger.Warn("key lookup failed", "error", err)
		return Key{}, false
	}
	if len(keys) == 0 {
		return Key{}, false
	}
	return s.overlay(keys[0]), true
}

// Touch records a use of the key, which must have just been looked up. The
// latest values are visible at once; Watch writes them to the database at
// most once a minute per key, and reports the key to onRevoke once it is
// gone.
func (s *Store) Touch(k Key, ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.touched[k.ID]
	if t == nil {
		t = &touch{key: Key{ID: k.ID, Kind: k.Kind, Name: k.Name}, load: s.loads}
		s.touched[k.ID] = t
	}
	t.at, t.ip, t.seq = time.Now().UTC().Truncate(time.Millisecond), ip, t.seq+1
}

// flush writes the last-used values that are due, or all of them with force.
func (s *Store) flush(ctx context.Context, force bool) {
	type pending struct {
		id string
		t  touch
	}
	var due []pending
	now := time.Now()
	s.mu.Lock()
	for id, t := range s.touched {
		if t.seq != t.saved && (force || now.Sub(t.persisted) >= persistEvery) {
			due = append(due, pending{id, *t})
		}
	}
	s.mu.Unlock()
	if len(due) == 0 {
		return
	}
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, p := range due {
			// A revoked key matches no row, which is fine.
			if _, err := tx.Exec("UPDATE keys SET last_used_at = ?, last_ip = ? WHERE id = ?", store.Millis(p.t.at), p.t.ip, p.id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.logger.Warn("saving key usage failed", "error", err)
		return
	}
	s.mu.Lock()
	for _, p := range due {
		if t := s.touched[p.id]; t != nil {
			t.persisted = now
			t.saved = p.t.seq
		}
	}
	s.mu.Unlock()
}

// Watch calls onRevoke once for each key that disappears, whether revoked
// through this Store (immediately) or by another process such as the key CLI
// (within interval). That covers every key it saw in a load and every key
// this Store authenticated a request with (Touch), so a key created, used
// and revoked between two loads is reported too. It also persists last-used
// values. It returns when ctx ends; the caller owns the goroutine.
func (s *Store) Watch(ctx context.Context, interval time.Duration, onRevoke func(Key)) {
	known := map[string]Key{}
	// failed makes the next tick reload again: a change seen while the
	// database was unreadable must not wait for another change.
	failed := false
	reload := func() {
		s.mu.Lock()
		s.loads++
		load := s.loads
		s.mu.Unlock()
		keys, err := queryKeys(ctx, s.db, "")
		failed = err != nil
		if ctx.Err() == nil {
			s.mu.Lock()
			if err != nil && s.reloadErr == nil {
				s.failingSince = time.Now()
			}
			s.reloadErr = err
			s.mu.Unlock()
		}
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("keys not reloaded, keeping the current set; retrying", "error", err)
			}
			return
		}
		cur := make(map[string]Key, len(keys))
		for _, k := range keys {
			cur[k.ID] = k
		}
		var gone []Key
		for id, k := range known {
			if _, ok := cur[id]; !ok {
				gone = append(gone, k)
			}
		}
		s.mu.Lock()
		for id, t := range s.touched {
			// A key first used after this load's query started may have been
			// created after it too, so it waits for the next load.
			if _, ok := cur[id]; ok || t.load >= load {
				continue
			}
			if _, ok := known[id]; !ok {
				gone = append(gone, t.key)
			}
			delete(s.touched, id)
		}
		s.mu.Unlock()
		known = cur
		if onRevoke != nil {
			for _, k := range gone {
				onRevoke(k)
			}
		}
	}
	// unseen reports a used key that no load has seen yet, which can only be
	// confirmed (or reported revoked) by another load.
	unseen := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		for id := range s.touched {
			if _, ok := known[id]; !ok {
				return true
			}
		}
		return false
	}
	reload()

	// db.Watch needs its own goroutine; it stops with ctx and is awaited so
	// nothing outlives this call.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.db.Watch(ctx, interval, func() error {
			select {
			case s.nudge <- struct{}{}:
			default:
			}
			return nil
		})
		if err != nil {
			s.logger.Warn("database watch stopped; other processes' key changes are no longer noticed", "error", err)
		}
	}()
	defer wg.Wait()

	tick := time.NewTicker(min(interval, flushTick))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			// ctx is done, so the final write needs its own.
			s.flush(context.WithoutCancel(ctx), true)
			return
		case <-s.nudge:
			reload()
		case <-tick.C:
			// Normally every used key is known. One that isn't was created
			// after the last load, and if its revocation was the last change
			// no nudge is coming to report it.
			if failed || unseen() {
				reload()
			}
			s.flush(ctx, false)
		}
	}
}
