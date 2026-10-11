package auth

import (
	"strings"
	"sync"
	"time"
)

const (
	limitFailures = 5                // failures within limitWindow that trigger a lockout
	limitWindow   = 15 * time.Minute // failures further apart than this don't add up
	lockoutBase   = 30 * time.Second // first lockout; doubles per extra failure
	lockoutMax    = 15 * time.Minute
	limitMaxKeys  = 10000 // per map; stale records are dropped when exceeded
)

// limiter counts failed sign-ins per client address and per username, in
// memory: a restart forgives everyone, which is fine for a brake on guessing.
type limiter struct {
	mu    sync.Mutex
	now   func() time.Time
	ips   map[string]*failures
	users map[string]*failures
}

type failures struct {
	count  int
	last   time.Time
	locked time.Time // refuse attempts until then
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{now: now, ips: map[string]*failures{}, users: map[string]*failures{}}
}

// check returns how long the caller must wait, or 0 if it may try.
func (l *limiter) check(ip, username string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var wait time.Duration
	for _, f := range []*failures{l.ips[ip], l.users[strings.ToLower(username)]} {
		if f != nil && f.locked.After(now) {
			wait = max(wait, f.locked.Sub(now))
		}
	}
	return wait
}

func (l *limiter) fail(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.record(l.ips, ip, now)
	l.record(l.users, strings.ToLower(username), now)
}

func (l *limiter) record(m map[string]*failures, key string, now time.Time) {
	f := m[key]
	if f == nil {
		if len(m) >= limitMaxKeys {
			for k, v := range m {
				if now.Sub(v.last) > limitWindow && !v.locked.After(now) {
					delete(m, k)
				}
			}
			if len(m) >= limitMaxKeys {
				return // full of live records: don't track more
			}
		}
		f = &failures{}
		m[key] = f
	}
	if now.Sub(f.last) > limitWindow {
		f.count = 0
	}
	f.count++
	f.last = now
	if f.count >= limitFailures {
		shift := min(f.count-limitFailures, 10)
		f.locked = now.Add(min(lockoutBase<<shift, lockoutMax))
	}
}

// success forgets the address's and the username's failures.
func (l *limiter) success(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.ips, ip)
	delete(l.users, strings.ToLower(username))
}
