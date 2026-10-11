package server

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

func bootstrapFixture(t *testing.T) (*auth.Users, *bytes.Buffer, *slog.Logger) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), store.FileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	users, err := auth.OpenUsers(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	return users, &buf, slog.New(slog.NewTextHandler(&buf, nil))
}

func TestBootstrapAdminGeneratesPassword(t *testing.T) {
	users, logs, log := bootstrapFixture(t)
	if err := bootstrapAdmin(t.Context(), users, Config{}, "http://hub:7000/", log); err != nil {
		t.Fatal(err)
	}
	list, _ := users.List(t.Context())
	if len(list) != 1 || list[0].Username != "admin" {
		t.Fatalf("users = %+v", list)
	}
	out := logs.String()
	const marker = "with password: "
	i := strings.Index(out, marker)
	if i < 0 || !strings.Contains(out, "level=WARN") || !strings.Contains(out, "mcp-mesh-hub admin passwd admin") || !strings.Contains(out, "http://hub:7000/") {
		t.Fatalf("log = %s", out)
	}
	password, _, _ := strings.Cut(out[i+len(marker):], ";")
	if len(password) != 24 {
		t.Fatalf("password %q", password)
	}
	if _, err := users.Verify(t.Context(), "admin", password); err != nil {
		t.Errorf("logged password does not work: %v", err)
	}

	// A second start leaves things alone and logs nothing.
	logs.Reset()
	if err := bootstrapAdmin(t.Context(), users, Config{}, "http://hub:7000/", log); err != nil {
		t.Fatal(err)
	}
	if n, _ := users.Count(t.Context()); n != 1 || logs.Len() != 0 {
		t.Errorf("second start: %d users, log %q", n, logs.String())
	}
}

func TestBootstrapAdminConfigured(t *testing.T) {
	users, logs, log := bootstrapFixture(t)
	cfg := Config{AdminUser: "boss", AdminPassword: "a configured password"}
	if err := bootstrapAdmin(t.Context(), users, cfg, "http://hub:7000/", log); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Verify(t.Context(), "boss", cfg.AdminPassword); err != nil {
		t.Errorf("configured password: %v", err)
	}
	if strings.Contains(logs.String(), cfg.AdminPassword) {
		t.Error("configured password was logged")
	}
	// Later starts with another configuration don't add or change users.
	cfg.AdminUser = "other"
	if err := bootstrapAdmin(t.Context(), users, cfg, "http://hub:7000/", log); err != nil {
		t.Fatal(err)
	}
	if n, _ := users.Count(t.Context()); n != 1 {
		t.Errorf("%d users", n)
	}
}

func TestBootstrapAdminRejectsWeakPassword(t *testing.T) {
	users, _, log := bootstrapFixture(t)
	err := bootstrapAdmin(t.Context(), users, Config{AdminPassword: "short"}, "http://hub:7000/", log)
	if err == nil || !strings.Contains(err.Error(), "at least") {
		t.Errorf("err = %v", err)
	}
}
