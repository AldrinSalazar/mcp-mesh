package auth

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

const testPassword = "correct horse battery"

func init() {
	// Tests hash a lot; cheap parameters keep them fast. The parameters
	// really used are checked in TestPasswordHash.
	currentParams = argonParams{memory: 8, time: 1, threads: 1}
}

func openUsersDB(t *testing.T) (*store.DB, *Users) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), store.FileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	users, err := OpenUsers(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	return db, users
}

func TestPasswordHash(t *testing.T) {
	if currentParams == (argonParams{memory: argonMemory, time: argonTime, threads: argonThreads}) {
		t.Fatal("test parameters not applied")
	}
	// The production parameters, in the PHC format.
	h, err := hashWith(testPassword, argonParams{memory: argonMemory, time: argonTime, threads: argonThreads})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\$argon2id\$v=19\$m=32768,t=3,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`).MatchString(h) {
		t.Errorf("hash = %q", h)
	}
	if ok, err := verifyPassword(testPassword, h); err != nil || !ok {
		t.Errorf("round trip = %v, %v", ok, err)
	}
	if ok, err := verifyPassword(testPassword+"x", h); err != nil || ok {
		t.Errorf("wrong password = %v, %v", ok, err)
	}
	h2, _ := hashWith(testPassword, argonParams{memory: argonMemory, time: argonTime, threads: argonThreads})
	if h == h2 {
		t.Error("salt repeats")
	}
}

func TestPasswordParamsComeFromTheHash(t *testing.T) {
	odd := argonParams{memory: 16, time: 2, threads: 2}
	h, err := hashWith(testPassword, odd)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h, "$m=16,t=2,p=2$") {
		t.Errorf("hash = %q", h)
	}
	p, salt, key, err := parseHash(h)
	if err != nil || p != odd || len(salt) != argonSaltLen || len(key) != argonKeyLen {
		t.Errorf("parseHash = %+v, %d, %d, %v", p, len(salt), len(key), err)
	}
	// Verified with its own parameters, not the current ones.
	if ok, _ := verifyPassword(testPassword, h); !ok {
		t.Error("hash with other parameters not verified")
	}
}

func TestParseHashRejects(t *testing.T) {
	for _, bad := range []string{
		"",
		"plain",
		"$argon2i$v=19$m=8,t=1,p=1$c2FsdHNhbHQ$a2V5",
		"$argon2id$v=18$m=8,t=1,p=1$c2FsdHNhbHQ$a2V5",
		"$argon2id$v=19$m=8,t=1$c2FsdHNhbHQ$a2V5",
		"$argon2id$v=19$m=0,t=1,p=1$c2FsdHNhbHQ$a2V5",
		"$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHQ$a2V5",
		"$argon2id$v=19$m=8,t=1,p=1$!!$a2V5",
		"$argon2id$v=19$m=8,t=1,p=1$c2FsdHNhbHQ",
	} {
		if _, _, _, err := parseHash(bad); err == nil {
			t.Errorf("parseHash(%q) succeeded", bad)
		}
		if _, err := verifyPassword("x", bad); err == nil {
			t.Errorf("verifyPassword with %q succeeded", bad)
		}
	}
}

func TestCheckPassword(t *testing.T) {
	for _, tt := range []struct {
		name, password string
		ok             bool
	}{
		{"empty", "", false},
		{"short", "elevenchars", false},
		{"minimum", "twelve chars", true},
		{"runes not bytes", strings.Repeat("é", 12), true},
		{"too few runes", strings.Repeat("é", 11), false},
		{"max bytes", strings.Repeat("a", 1024), true},
		{"over max", strings.Repeat("a", 1025), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := checkPassword(tt.password)
			if (err == nil) != tt.ok {
				t.Errorf("err = %v", err)
			}
			if err != nil && !errors.Is(err, ErrWeakPassword) {
				t.Errorf("err = %v, want ErrWeakPassword", err)
			}
		})
	}
}

func TestUsersCreate(t *testing.T) {
	_, users := openUsersDB(t)
	ctx := t.Context()

	for _, name := range []string{"", strings.Repeat("a", 65), "has space", "semi;colon", "ünï", "a/b"} {
		if _, err := users.Create(ctx, name, testPassword); err == nil || errors.Is(err, ErrWeakPassword) {
			t.Errorf("Create(%q) = %v", name, err)
		}
	}
	if _, err := users.Create(ctx, "alice", "short"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("weak password: %v", err)
	}
	if n, _ := users.Count(ctx); n != 0 {
		t.Fatalf("%d users after failures", n)
	}

	u, err := users.Create(ctx, "Alice.Smith@x-y_z", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^u_[0-9a-f]{8}$`).MatchString(u.ID) || u.Username != "Alice.Smith@x-y_z" || u.Created.IsZero() || !u.LastLogin.IsZero() {
		t.Errorf("user = %+v", u)
	}
	if _, err := users.Create(ctx, strings.Repeat("a", 64), testPassword); err != nil {
		t.Errorf("64-character name: %v", err)
	}
	if _, err := users.Create(ctx, "alice.smith@X-Y_Z", testPassword); !errors.Is(err, ErrUserExists) {
		t.Errorf("duplicate with other case: %v", err)
	}

	list, err := users.List(ctx)
	if err != nil || len(list) != 2 || list[1].Username != "Alice.Smith@x-y_z" {
		t.Errorf("List = %+v, %v", list, err)
	}
	// Case-insensitive lookup, name as stored.
	got, err := users.Verify(ctx, "ALICE.SMITH@X-Y_Z", testPassword)
	if err != nil || got.ID != u.ID || got.Username != "Alice.Smith@x-y_z" {
		t.Errorf("Verify = %+v, %v", got, err)
	}
}

func TestUsersListSorted(t *testing.T) {
	_, users := openUsersDB(t)
	for _, n := range []string{"carol", "Bob", "alice"} {
		if _, err := users.Create(t.Context(), n, testPassword); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := users.List(t.Context())
	var names []string
	for _, u := range list {
		names = append(names, u.Username)
	}
	if got := strings.Join(names, " "); got != "alice Bob carol" {
		t.Errorf("order = %s", got)
	}
}

func TestVerify(t *testing.T) {
	_, users := openUsersDB(t)
	ctx := t.Context()
	if _, err := users.Create(ctx, "alice", testPassword); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, user, password string }{
		{"wrong password", "alice", "wrong password!"},
		{"unknown user", "nobody", testPassword},
		{"empty", "", ""},
		{"huge password", "alice", strings.Repeat("a", 5000)},
		{"huge password unknown user", "nobody", strings.Repeat("a", 5000)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := users.Verify(ctx, tt.user, tt.password); err != ErrBadCredentials {
				t.Errorf("err = %v, want ErrBadCredentials", err)
			}
		})
	}
	if _, err := users.Verify(ctx, "alice", testPassword); err != nil {
		t.Errorf("right password: %v", err)
	}
}

func TestSetPasswordAndDelete(t *testing.T) {
	db, users := openUsersDB(t)
	ctx := t.Context()
	s := NewSessions(db, users, SessionOptions{})
	for _, n := range []string{"alice", "bob"} {
		if _, err := users.Create(ctx, n, testPassword); err != nil {
			t.Fatal(err)
		}
	}
	signIn := func(user, password string) *http.Cookie {
		t.Helper()
		c, err := loginAs(s, user, password, "")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cAlice, cBob := signIn("alice", testPassword), signIn("bob", testPassword)

	if err := users.SetPassword(ctx, "nobody", testPassword); err != ErrNoUser {
		t.Errorf("SetPassword unknown: %v", err)
	}
	if err := users.SetPassword(ctx, "alice", "short"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("SetPassword weak: %v", err)
	}
	if _, ok := s.Current(reqWith(cAlice)); !ok {
		t.Fatal("failed SetPassword ended the session")
	}
	if err := users.SetPassword(ctx, "ALICE", "another long password"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Current(reqWith(cAlice)); ok {
		t.Error("session survives a password change")
	}
	if _, ok := s.Current(reqWith(cBob)); !ok {
		t.Error("another user's session ended")
	}
	if _, err := users.Verify(ctx, "alice", testPassword); err != ErrBadCredentials {
		t.Errorf("old password: %v", err)
	}
	if _, err := users.Verify(ctx, "alice", "another long password"); err != nil {
		t.Errorf("new password: %v", err)
	}

	cAlice = signIn("alice", "another long password")
	if err := users.Delete(ctx, "nobody"); err != ErrNoUser {
		t.Errorf("Delete unknown: %v", err)
	}
	if err := users.Delete(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Current(reqWith(cAlice)); ok {
		t.Error("session survives deleting its user")
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sessions WHERE user_id NOT IN (SELECT id FROM admin_users)").Scan(&n); err != nil || n != 0 {
		t.Errorf("orphan sessions = %d, %v", n, err)
	}
	if err := users.Delete(ctx, "bob"); err != ErrLastAdmin {
		t.Errorf("Delete last admin: %v", err)
	}
	if c, _ := users.Count(ctx); c != 1 {
		t.Errorf("Count = %d", c)
	}
}

// loginAs signs in and returns the cookie set, if any.
func loginAs(s *Sessions, user, password, remote string) (*http.Cookie, error) {
	req := httptest.NewRequest("POST", "/login", nil)
	if remote != "" {
		req.RemoteAddr = remote
	}
	rec := httptest.NewRecorder()
	_, err := s.Login(rec, req, user, password)
	for _, c := range rec.Result().Cookies() {
		return c, err
	}
	return nil, err
}

func reqWith(c *http.Cookie) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	if c != nil {
		r.AddCookie(c)
	}
	return r
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newTestSessions(t *testing.T, ttl time.Duration) (*Sessions, *Users, *store.DB, *clock) {
	t.Helper()
	db, users := openUsersDB(t)
	if _, err := users.Create(t.Context(), "root", testPassword); err != nil {
		t.Fatal(err)
	}
	s := NewSessions(db, users, SessionOptions{TTL: ttl})
	clk := &clock{now: time.Now().Truncate(time.Millisecond)}
	s.now = clk.Now
	return s, users, db, clk
}

func TestSessionLifecycle(t *testing.T) {
	s, users, db, clk := newTestSessions(t, time.Hour)

	if _, err := loginAs(s, "root", "wrong password!", ""); err != ErrBadCredentials {
		t.Errorf("wrong password: %v", err)
	}
	c, err := loginAs(s, "ROOT", testPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "mcpmesh_session" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure || c.MaxAge != 3600 {
		t.Errorf("cookie = %+v", c)
	}
	if len(c.Value) != 43 { // 32 bytes, base64url without padding
		t.Errorf("cookie value length %d", len(c.Value))
	}
	// Only the hash of the cookie is stored.
	var stored string
	if err := db.QueryRow("SELECT hash FROM sessions").Scan(&stored); err != nil || stored != hashToken(c.Value) || stored == c.Value || len(stored) != 64 {
		t.Errorf("stored = %q, %v", stored, err)
	}

	req := httptest.NewRequest("POST", "/login", nil)
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	if _, err := s.Login(rec, req, "root", testPassword); err != nil {
		t.Fatal(err)
	}
	if got := rec.Result().Cookies(); len(got) != 1 || !got[0].Secure {
		t.Errorf("cookie over TLS = %+v", got)
	}

	sess, ok := s.Current(reqWith(c))
	if !ok || sess.Username != "root" || sess.UserID == "" || len(sess.CSRF) != 43 || !sess.Expires.Equal(clk.now.Add(time.Hour).UTC()) {
		t.Errorf("Current = %+v, %v", sess, ok)
	}
	if _, ok := s.Current(reqWith(nil)); ok {
		t.Error("Current without cookie")
	}
	if _, ok := s.Current(reqWith(&http.Cookie{Name: c.Name, Value: "forged"})); ok {
		t.Error("Current with forged cookie")
	}

	list, _ := users.List(t.Context())
	if len(list) != 1 || !list[0].LastLogin.Equal(clk.now.UTC()) {
		t.Errorf("last login = %+v", list)
	}

	// Survives a restart: a new Sessions on the same database.
	restarted := NewSessions(db, users, SessionOptions{TTL: time.Hour})
	restarted.now = clk.Now
	if got, ok := restarted.Current(reqWith(c)); !ok || got.CSRF != sess.CSRF {
		t.Errorf("after restart: %+v, %v", got, ok)
	}

	// Last-seen is rewritten at most once a minute.
	seen := func() int64 {
		var ms int64
		if err := db.QueryRow("SELECT last_seen_at FROM sessions WHERE hash = ?", hashToken(c.Value)).Scan(&ms); err != nil {
			t.Fatal(err)
		}
		return ms
	}
	first := seen()
	clk.now = clk.now.Add(30 * time.Second)
	s.Current(reqWith(c))
	if seen() != first {
		t.Error("last_seen_at rewritten within a minute")
	}
	clk.now = clk.now.Add(31 * time.Second)
	s.Current(reqWith(c))
	if seen() == first {
		t.Error("last_seen_at not rewritten after a minute")
	}

	// Logout ends it and clears the cookie.
	out, _ := loginAs(s, "root", testPassword, "")
	rec = httptest.NewRecorder()
	s.Logout(rec, reqWith(out))
	if cleared := rec.Result().Cookies(); len(cleared) != 1 || cleared[0].MaxAge >= 0 || cleared[0].Name != CookieName {
		t.Errorf("logout cookie = %+v", cleared)
	}
	if _, ok := s.Current(reqWith(out)); ok {
		t.Error("session survives logout")
	}

	// Absolute expiry; the expired row goes away.
	clk.now = clk.now.Add(time.Hour)
	if _, ok := s.Current(reqWith(c)); ok {
		t.Error("session survives its TTL")
	}
	var n int
	db.QueryRow("SELECT count(*) FROM sessions WHERE hash = ?", hashToken(c.Value)).Scan(&n)
	if n != 0 {
		t.Error("expired session row not deleted")
	}
}

func TestSessionDefaultTTLAndPurge(t *testing.T) {
	s, _, db, clk := newTestSessions(t, 0)
	c, err := loginAs(s, "root", testPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxAge != 7*24*3600 {
		t.Errorf("MaxAge = %d", c.MaxAge)
	}
	// An abandoned expired session is deleted by a later request, at most hourly.
	clk.now = clk.now.Add(8 * 24 * time.Hour)
	if _, err := loginAs(s, "root", testPassword, ""); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow("SELECT count(*) FROM sessions").Scan(&n)
	if n != 1 {
		t.Errorf("%d sessions after purge, want 1", n)
	}
}

func TestCheckCSRF(t *testing.T) {
	s, _, _, _ := newTestSessions(t, time.Hour)
	sess := Session{CSRF: "token-value"}
	form := func(v string) *http.Request {
		r := httptest.NewRequest("POST", "/", strings.NewReader(url.Values{"csrf": {v}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r
	}
	header := func(v string) *http.Request {
		r := httptest.NewRequest("POST", "/", nil)
		if v != "" {
			r.Header.Set("X-CSRF-Token", v)
		}
		return r
	}
	for _, tt := range []struct {
		name string
		r    *http.Request
		sess Session
		want bool
	}{
		{"header", header("token-value"), sess, true},
		{"form", form("token-value"), sess, true},
		{"wrong header", header("nope"), sess, false},
		{"wrong form", form("nope"), sess, false},
		{"missing", header(""), sess, false},
		{"empty form", form(""), sess, false},
		{"session without token", header(""), Session{}, false},
	} {
		if got := s.CheckCSRF(tt.r, tt.sess); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestLoginRateLimitPerIP(t *testing.T) {
	s, _, _, clk := newTestSessions(t, time.Hour)
	// Different usernames from one address: the address gets locked.
	for i := range limitFailures {
		if _, err := loginAs(s, "guess"+string(rune('a'+i)), "wrong password!", "10.0.0.1:1000"); err != ErrBadCredentials {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	var rl *RateLimitError
	// The right password is refused without being checked.
	if _, err := loginAs(s, "root", testPassword, "10.0.0.1:2000"); !errors.As(err, &rl) || rl.RetryAfter != lockoutBase {
		t.Fatalf("locked attempt: %v", err)
	}
	if !strings.Contains(rl.Error(), "30s") {
		t.Errorf("message = %q", rl.Error())
	}
	// Another address is unaffected.
	if _, err := loginAs(s, "root", testPassword, "10.0.0.2:1000"); err != nil {
		t.Errorf("other address: %v", err)
	}
	clk.now = clk.now.Add(lockoutBase)
	if _, err := loginAs(s, "root", testPassword, "10.0.0.1:3000"); err != nil {
		t.Errorf("after the lockout: %v", err)
	}
}

func TestLoginRateLimitPerUsername(t *testing.T) {
	s, _, _, _ := newTestSessions(t, time.Hour)
	for i := range limitFailures {
		remote := "10.0.0." + string(rune('1'+i)) + ":1"
		if _, err := loginAs(s, "RooT", "wrong password!", remote); err != ErrBadCredentials {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	var rl *RateLimitError
	if _, err := loginAs(s, "root", testPassword, "10.9.9.9:1"); !errors.As(err, &rl) {
		t.Errorf("fresh address, locked username: %v", err)
	}
}

func TestLoginSuccessClearsFailures(t *testing.T) {
	s, _, _, _ := newTestSessions(t, time.Hour)
	for range limitFailures - 1 {
		loginAs(s, "root", "wrong password!", "10.0.0.1:1")
	}
	if _, err := loginAs(s, "root", testPassword, "10.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	// Four more failures would lock if the earlier ones still counted.
	for i := range limitFailures - 1 {
		if _, err := loginAs(s, "root", "wrong password!", "10.0.0.1:1"); err != ErrBadCredentials {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
}

func TestLimiterBackoff(t *testing.T) {
	clk := &clock{now: time.Now()}
	l := newLimiter(clk.Now)
	const ip, user = "1.2.3.4", "Bob"
	for range limitFailures - 1 {
		l.fail(ip, user)
		if w := l.check(ip, user); w != 0 {
			t.Fatalf("locked early: %v", w)
		}
	}
	want := lockoutBase
	for i := range 10 {
		l.fail(ip, user)
		if got := l.check(ip, "other"); got != min(want, lockoutMax) {
			t.Fatalf("failure %d: lockout %v, want %v", limitFailures+i, got, min(want, lockoutMax))
		}
		clk.now = clk.now.Add(min(want, lockoutMax))
		if w := l.check(ip, user); w != 0 {
			t.Fatalf("still locked after waiting: %v", w)
		}
		want *= 2
	}
	// Capped at 15 minutes.
	l.fail(ip, user)
	if got := l.check(ip, user); got != lockoutMax {
		t.Errorf("capped lockout = %v", got)
	}

	// Failures spread wider than the window don't add up.
	l2 := newLimiter(clk.Now)
	for range limitFailures + 3 {
		l2.fail("9.9.9.9", "x")
		if w := l2.check("9.9.9.9", "x"); w != 0 {
			t.Fatalf("slow failures locked: %v", w)
		}
		clk.now = clk.now.Add(limitWindow + time.Second)
	}
}

func TestLimiterBoundedMemory(t *testing.T) {
	clk := &clock{now: time.Now()}
	l := newLimiter(clk.Now)
	for i := range limitMaxKeys {
		l.fail(fmt.Sprintf("ip%d", i), fmt.Sprintf("u%d", i))
	}
	if len(l.ips) != limitMaxKeys {
		t.Fatalf("%d records", len(l.ips))
	}
	// Full of live records: no new ones are tracked.
	l.fail("new", "new")
	if len(l.ips) != limitMaxKeys || l.ips["new"] != nil {
		t.Errorf("grew to %d", len(l.ips))
	}
	// Once they are stale, they are dropped to make room.
	clk.now = clk.now.Add(limitWindow + time.Minute)
	l.fail("new", "new")
	if len(l.ips) != 1 || len(l.users) != 1 {
		t.Errorf("records after prune: %d ips, %d users", len(l.ips), len(l.users))
	}
}

// compile-time: Sessions is used through the admin UI's interface.
var _ interface {
	Current(*http.Request) (Session, bool)
	Login(http.ResponseWriter, *http.Request, string, string) (Session, error)
	Logout(http.ResponseWriter, *http.Request)
	CheckCSRF(*http.Request, Session) bool
} = (*Sessions)(nil)

// A password reset between the password check and the session insert signs
// the user out everywhere; the login in progress must not slip through with
// the old password.
func TestLoginLosesToPasswordReset(t *testing.T) {
	s, users, db, _ := newTestSessions(t, time.Hour)
	s.afterVerify = func() {
		if err := users.SetPassword(t.Context(), "root", testPassword+"-new"); err != nil {
			t.Error(err)
		}
	}
	if c, err := loginAs(s, "root", testPassword, ""); !errors.Is(err, ErrBadCredentials) || c != nil {
		t.Fatalf("login racing a reset = %v, cookie %v", err, c)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sessions").Scan(&n); err != nil || n != 0 {
		t.Fatalf("sessions = %d, %v", n, err)
	}
	s.afterVerify = nil
	if _, err := loginAs(s, "root", testPassword+"-new", ""); err != nil {
		t.Fatalf("login with the new password: %v", err)
	}
}
