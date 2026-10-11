package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

// maxConcurrentHashes bounds argon2's memory: each hash needs 32 MiB.
const maxConcurrentHashes = 4

// Users are the people who can sign in to the dashboard. They live in the hub
// database, shared with the CLI, and every read goes to it.
type Users struct {
	db     *store.DB
	logger *slog.Logger
	now    func() time.Time // replaced in tests
	hashes chan struct{}
}

// OpenUsers returns the users stored in db.
func OpenUsers(ctx context.Context, db *store.DB, logger *slog.Logger) (*Users, error) {
	if logger == nil {
		logger = slog.Default()
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM admin_users").Scan(&n); err != nil {
		return nil, fmt.Errorf("open users: %w", err)
	}
	return &Users{db: db, logger: logger, now: time.Now, hashes: make(chan struct{}, maxConcurrentHashes)}, nil
}

// withHashSlot runs fn once one of the hashing slots is free.
func (u *Users) withHashSlot(ctx context.Context, fn func()) error {
	select {
	case u.hashes <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-u.hashes }()
	fn()
	return nil
}

func validUsername(name string) error {
	if name == "" {
		return errors.New("username is empty")
	}
	if len(name) > 64 {
		return errors.New("username is longer than 64 characters")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-', r == '@':
		default:
			return fmt.Errorf("username %q may only contain letters, digits and . _ - @", name)
		}
	}
	return nil
}

func (u *Users) hash(ctx context.Context, password string) (string, error) {
	var (
		h   string
		err error
	)
	if werr := u.withHashSlot(ctx, func() { h, err = hashPassword(password) }); werr != nil {
		return "", werr
	}
	return h, err
}

// Create adds an admin user.
func (u *Users) Create(ctx context.Context, username, password string) (User, error) {
	if err := validUsername(username); err != nil {
		return User{}, err
	}
	if err := checkPassword(password); err != nil {
		return User{}, err
	}
	hash, err := u.hash(ctx, password)
	if err != nil {
		return User{}, fmt.Errorf("hash password: %w", err)
	}
	now := u.now().UTC().Truncate(time.Millisecond)
	user := User{Username: username, Created: now, PasswordChanged: now}
	err = u.db.Tx(ctx, func(tx *sql.Tx) error {
		var taken int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM admin_users WHERE username = ?", username).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 {
			return ErrUserExists
		}
		for {
			var b [4]byte
			if _, err := rand.Read(b[:]); err != nil {
				return fmt.Errorf("random id: %w", err)
			}
			user.ID = "u_" + hex.EncodeToString(b[:])
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM admin_users WHERE id = ?", user.ID).Scan(&taken); err != nil {
				return err
			}
			if taken == 0 {
				break
			}
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO admin_users (id, username, password_hash, created_at, password_changed_at) VALUES (?, ?, ?, ?, ?)",
			user.ID, username, hash, store.Millis(now), store.Millis(now))
		return err
	})
	if errors.Is(err, ErrUserExists) {
		return User{}, ErrUserExists
	}
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return user, nil
}

// SetPassword changes a user's password and signs them out everywhere.
func (u *Users) SetPassword(ctx context.Context, username, password string) error {
	if err := checkPassword(password); err != nil {
		return err
	}
	hash, err := u.hash(ctx, password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	err = u.db.Tx(ctx, func(tx *sql.Tx) error {
		var id string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM admin_users WHERE username = ?", username).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoUser
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE admin_users SET password_hash = ?, password_changed_at = ? WHERE id = ?",
			hash, store.Millis(u.now()), id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", id)
		return err
	})
	if errors.Is(err, ErrNoUser) {
		return ErrNoUser
	}
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	return nil
}

// Delete removes a user and their sessions, unless they are the last admin.
func (u *Users) Delete(ctx context.Context, username string) error {
	err := u.db.Tx(ctx, func(tx *sql.Tx) error {
		var id string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM admin_users WHERE username = ?", username).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoUser
			}
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM admin_users").Scan(&n); err != nil {
			return err
		}
		if n <= 1 {
			return ErrLastAdmin
		}
		// Explicit, so it doesn't depend on foreign_keys being on.
		if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM admin_users WHERE id = ?", id)
		return err
	})
	if errors.Is(err, ErrNoUser) {
		return ErrNoUser
	}
	if errors.Is(err, ErrLastAdmin) {
		return ErrLastAdmin
	}
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}

// List returns all users sorted by username.
func (u *Users) List(ctx context.Context) ([]User, error) {
	rows, err := u.db.QueryContext(ctx, "SELECT id, username, created_at, password_changed_at, last_login_at FROM admin_users ORDER BY username COLLATE NOCASE, id")
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var (
			usr              User
			created, changed int64
			lastLogin        sql.NullInt64
		)
		if err := rows.Scan(&usr.ID, &usr.Username, &created, &changed, &lastLogin); err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}
		usr.Created, usr.PasswordChanged, usr.LastLogin = store.FromMillis(created), store.FromMillis(changed), store.FromMillis(lastLogin.Int64)
		users = append(users, usr)
	}
	return users, rows.Err()
}

// Count returns how many users exist.
func (u *Users) Count(ctx context.Context) (int, error) {
	var n int
	if err := u.db.QueryRowContext(ctx, "SELECT count(*) FROM admin_users").Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// Verify checks a username and password. An unknown user and a wrong password
// look the same, in the error and in the time taken.
func (u *Users) Verify(ctx context.Context, username, password string) (User, error) {
	usr, _, err := u.verify(ctx, username, password)
	return usr, err
}

// verify is Verify that also returns the stored hash it checked against, so a
// caller can tell later whether the password changed since.
func (u *Users) verify(ctx context.Context, username, password string) (User, string, error) {
	var (
		usr              User
		hash             string
		created, changed int64
		lastLogin        sql.NullInt64
	)
	err := u.db.QueryRowContext(ctx, "SELECT id, username, password_hash, created_at, password_changed_at, last_login_at FROM admin_users WHERE username = ?", username).
		Scan(&usr.ID, &usr.Username, &hash, &created, &changed, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		if len(password) <= maxPasswordBytes {
			if werr := u.withHashSlot(ctx, func() { spendDummy(password) }); werr != nil {
				return User{}, "", werr
			}
		}
		return User{}, "", ErrBadCredentials
	}
	if err != nil {
		return User{}, "", fmt.Errorf("verify: %w", err)
	}
	if len(password) > maxPasswordBytes {
		return User{}, "", ErrBadCredentials
	}
	var (
		ok   bool
		verr error
	)
	if werr := u.withHashSlot(ctx, func() { ok, verr = verifyPassword(password, hash) }); werr != nil {
		return User{}, "", werr
	}
	if verr != nil {
		u.logger.Error("stored password hash is unusable", "user", usr.Username, "error", verr)
		return User{}, "", ErrBadCredentials
	}
	if !ok {
		return User{}, "", ErrBadCredentials
	}
	usr.Created, usr.PasswordChanged, usr.LastLogin = store.FromMillis(created), store.FromMillis(changed), store.FromMillis(lastLogin.Int64)
	return usr, hash, nil
}
