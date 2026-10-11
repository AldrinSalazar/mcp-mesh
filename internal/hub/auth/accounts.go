package auth

import (
	"errors"
	"fmt"
	"time"
)

// MinPasswordLen is the shortest admin password accepted.
const MinPasswordLen = 12

// User is a person who signs in to the dashboard. The password hash never
// leaves the package.
type User struct {
	ID              string // "u_" + 8 hex chars
	Username        string
	Created         time.Time
	PasswordChanged time.Time
	LastLogin       time.Time // zero if never
}

// Session is a signed-in dashboard session.
type Session struct {
	UserID   string
	Username string
	// CSRF must accompany every state-changing request of this session
	// (header X-CSRF-Token, or the csrf form field on server-rendered pages).
	CSRF    string
	Expires time.Time
}

var (
	// ErrBadCredentials is the only answer to a wrong username or password,
	// so sign-in doesn't reveal which usernames exist.
	ErrBadCredentials = errors.New("wrong username or password")
	ErrUserExists     = errors.New("an admin with that username already exists")
	ErrNoUser         = errors.New("no admin with that username")
	ErrLastAdmin      = errors.New("the last admin can't be deleted")
	ErrWeakPassword   = fmt.Errorf("password must be at least %d characters", MinPasswordLen)
)

// RateLimitError is returned by sign-in while an address or username is
// locked out after repeated failures.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("too many failed sign-ins; try again in %s", e.RetryAfter.Round(time.Second))
}
