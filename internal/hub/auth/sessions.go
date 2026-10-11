package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

// CookieName is the dashboard session cookie.
const CookieName = "mcpmesh_session"

const (
	defaultSessionTTL = 7 * 24 * time.Hour
	seenEvery         = time.Minute // how often last_seen_at/ip are rewritten
	purgeEvery        = time.Hour   // how often expired rows are deleted
)

// SessionOptions configures Sessions.
type SessionOptions struct {
	TTL    time.Duration // 0 = 7 days
	Logger *slog.Logger
}

// Sessions signs admin users in to the dashboard and keeps them signed in
// with a cookie. Sessions live in the hub database, so they survive a hub
// restart, and every request is checked against it, so changing a password or
// deleting a user ends them at once. It implements admin.Auth.
type Sessions struct {
	db     *store.DB
	users  *Users
	ttl    time.Duration
	logger *slog.Logger
	now    func() time.Time // replaced in tests
	limit  *limiter

	mu        sync.Mutex
	lastPurge time.Time

	// afterVerify, if set (tests), runs between checking the password and
	// creating the session.
	afterVerify func()
}

// NewSessions returns sessions for users stored in db.
func NewSessions(db *store.DB, users *Users, opts SessionOptions) *Sessions {
	if opts.TTL <= 0 {
		opts.TTL = defaultSessionTTL
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Sessions{db: db, users: users, ttl: opts.TTL, logger: opts.Logger, now: time.Now}
	s.limit = newLimiter(func() time.Time { return s.now() })
	return s
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Login checks the credentials and starts a session, setting its cookie.
// While the address or username is locked out after repeated failures it
// returns a *RateLimitError without looking at the password.
func (s *Sessions) Login(w http.ResponseWriter, r *http.Request, username, password string) (Session, error) {
	ctx := r.Context()
	ip := clientIP(r)
	if wait := s.limit.check(ip, username); wait > 0 {
		return Session{}, &RateLimitError{RetryAfter: wait}
	}
	user, verified, err := s.users.verify(ctx, username, password)
	if err != nil {
		if errors.Is(err, ErrBadCredentials) {
			s.limit.fail(ip, username)
		}
		return Session{}, err
	}
	s.limit.success(ip, username)
	if s.afterVerify != nil {
		s.afterVerify()
	}

	token, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	csrf, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	sess := Session{UserID: user.ID, Username: user.Username, CSRF: csrf, Expires: now.Add(s.ttl).UTC().Truncate(time.Millisecond)}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		// The password was checked outside this transaction (hashing is
		// slow). A reset since then signed the user out everywhere; this
		// session must not outlive it with the old password.
		var current string
		err := tx.QueryRowContext(ctx, "SELECT password_hash FROM admin_users WHERE id = ?", user.ID).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && current != verified) {
			return ErrBadCredentials
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO sessions (hash, user_id, csrf, created_at, expires_at, last_seen_at, ip) VALUES (?, ?, ?, ?, ?, ?, ?)",
			hashToken(token), user.ID, csrf, store.Millis(now), store.Millis(sess.Expires), store.Millis(now), ip); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE admin_users SET last_login_at = ? WHERE id = ?", store.Millis(now), user.ID)
		return err
	})
	if errors.Is(err, ErrBadCredentials) {
		return Session{}, err
	}
	if err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	s.purgeExpired(ctx, now)
	http.SetCookie(w, s.cookie(r, token, int(s.ttl/time.Second)))
	return sess, nil
}

// Current returns the request's session if it is valid and unexpired.
func (s *Sessions) Current(r *http.Request) (Session, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return Session{}, false
	}
	ctx := r.Context()
	hash := hashToken(c.Value)
	var (
		sess              Session
		expires, lastSeen int64
	)
	err = s.db.QueryRowContext(ctx, `SELECT s.user_id, u.username, s.csrf, s.expires_at, s.last_seen_at
		FROM sessions s JOIN admin_users u ON u.id = s.user_id WHERE s.hash = ?`, hash).
		Scan(&sess.UserID, &sess.Username, &sess.CSRF, &expires, &lastSeen)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			// Fail closed: a session we can't read is not a session.
			s.logger.Warn("session lookup failed", "error", err)
		}
		return Session{}, false
	}
	now := s.now()
	sess.Expires = store.FromMillis(expires)
	if !now.Before(sess.Expires) {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE hash = ?", hash); err != nil {
			s.logger.Warn("deleting expired session failed", "error", err)
		}
		return Session{}, false
	}
	if now.Sub(store.FromMillis(lastSeen)) >= seenEvery {
		if _, err := s.db.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ?, ip = ? WHERE hash = ?", store.Millis(now), clientIP(r), hash); err != nil {
			s.logger.Warn("updating session failed", "error", err)
		}
	}
	s.purgeExpired(ctx, now)
	return sess, true
}

// Logout ends the request's session and clears the cookie.
func (s *Sessions) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		if _, err := s.db.ExecContext(r.Context(), "DELETE FROM sessions WHERE hash = ?", hashToken(c.Value)); err != nil {
			s.logger.Warn("deleting session failed", "error", err)
		}
	}
	http.SetCookie(w, s.cookie(r, "", -1))
}

// CheckCSRF reports whether the request carries the session's CSRF token, in
// the X-CSRF-Token header or else the "csrf" form field.
func (s *Sessions) CheckCSRF(r *http.Request, sess Session) bool {
	got := r.Header.Get("X-CSRF-Token")
	if got == "" {
		got = r.PostFormValue("csrf")
	}
	if got == "" || sess.CSRF == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRF)) == 1
}

// purgeExpired deletes expired rows at most once an hour, from whichever
// request comes along, so no goroutine is needed.
func (s *Sessions) purgeExpired(ctx context.Context, now time.Time) {
	s.mu.Lock()
	if now.Sub(s.lastPurge) < purgeEvery {
		s.mu.Unlock()
		return
	}
	s.lastPurge = now
	s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ?", store.Millis(now)); err != nil {
		s.logger.Warn("purging expired sessions failed", "error", err)
	}
}

func (s *Sessions) cookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: CookieName, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: IsHTTPS(r),
	}
}
