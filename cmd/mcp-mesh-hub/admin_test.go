package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

const cliPassword = "a long enough password"

// runAdmin runs "admin <args>" with stdin as given.
func runAdmin(t *testing.T, dataDir, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := rootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append([]string{"--data-dir", dataDir, "--public-host", "hub.example", "admin"}, args...))
	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}

func verify(t *testing.T, dataDir, user, password string) error {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(dataDir, store.FileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	users, err := auth.OpenUsers(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = users.Verify(t.Context(), user, password)
	return err
}

func TestAdminCommands(t *testing.T) {
	dir := t.TempDir()

	out, err := runAdmin(t, dir, cliPassword+"\n", "create", "alice", "--password-stdin")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if !strings.Contains(out, `Created admin user "alice"`) || !strings.Contains(out, "http://hub.example:7000/") || strings.Contains(out, cliPassword) {
		t.Errorf("create output:\n%s", out)
	}
	if err := verify(t, dir, "alice", cliPassword); err != nil {
		t.Errorf("new password: %v", err)
	}

	for name, tc := range map[string]struct {
		stdin string
		args  []string
		want  string
	}{
		"duplicate":        {cliPassword, []string{"create", "ALICE", "--password-stdin"}, "already exists"},
		"weak":             {"short\n", []string{"create", "bob", "--password-stdin"}, "at least"},
		"bad name":         {cliPassword, []string{"create", "b o b", "--password-stdin"}, "username"},
		"no password flag": {"", []string{"create", "bob"}, "--password-stdin"},
		"empty stdin":      {"", []string{"create", "bob", "--password-stdin"}, "read password"},
		"passwd no flag":   {"", []string{"passwd", "alice"}, "--password-stdin"},
		"passwd unknown":   {cliPassword, []string{"passwd", "nobody", "--password-stdin"}, "no admin"},
		"delete unknown":   {"", []string{"delete", "nobody"}, "no admin"},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := runAdmin(t, dir, tc.stdin, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q\n%s", err, tc.want, out)
			}
		})
	}

	// Without a trailing newline, and with CRLF.
	if _, err := runAdmin(t, dir, cliPassword, "create", "bob", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	if _, err := runAdmin(t, dir, "new long password\r\n", "passwd", "bob", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	if err := verify(t, dir, "bob", "new long password"); err != nil {
		t.Errorf("changed password: %v", err)
	}
	if err := verify(t, dir, "bob", cliPassword); err != auth.ErrBadCredentials {
		t.Errorf("old password: %v", err)
	}

	out, err = runAdmin(t, dir, "", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"USERNAME", "CREATED", "LAST SIGN-IN", "alice", "bob", "never"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "$argon2") || strings.Contains(out, "password") {
		t.Errorf("list leaks hashes:\n%s", out)
	}
	out, err = runAdmin(t, dir, "", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var listed []userJSON
	if err := json.Unmarshal([]byte(out), &listed); err != nil || len(listed) != 2 || listed[0].Username != "alice" || listed[0].LastLogin != nil {
		t.Fatalf("json list: %v\n%s", err, out)
	}
	if strings.Contains(out, "$argon2") {
		t.Errorf("json leaks hashes:\n%s", out)
	}

	if out, err := runAdmin(t, dir, "", "delete", "bob"); err != nil || !strings.Contains(out, `Deleted admin user "bob"`) {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if _, err := runAdmin(t, dir, "", "delete", "alice"); err == nil || !strings.Contains(err.Error(), "last admin") {
		t.Errorf("deleting the last admin: %v", err)
	}
	if info, err := os.Stat(filepath.Join(dir, "hub.db")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("hub.db: %v %v", info, err)
	}
}

func TestResolveAdminPassword(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(file, []byte("from the file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("unset leaves the flag alone", func(t *testing.T) {
		t.Setenv(adminPasswordFileEnv, "")
		cfg := config{}
		cfg.AdminPassword = "from flag"
		if err := resolveAdminPassword(&cfg); err != nil || cfg.AdminPassword != "from flag" {
			t.Errorf("%q, %v", cfg.AdminPassword, err)
		}
	})
	t.Run("file", func(t *testing.T) {
		t.Setenv(adminPasswordFileEnv, file)
		cfg := config{}
		if err := resolveAdminPassword(&cfg); err != nil || cfg.AdminPassword != "from the file" {
			t.Errorf("%q, %v", cfg.AdminPassword, err)
		}
	})
	t.Run("both", func(t *testing.T) {
		t.Setenv(adminPasswordFileEnv, file)
		cfg := config{}
		cfg.AdminPassword = "from env"
		if err := resolveAdminPassword(&cfg); err == nil || !strings.Contains(err.Error(), "use one") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		t.Setenv(adminPasswordFileEnv, filepath.Join(t.TempDir(), "nope"))
		if err := resolveAdminPassword(&config{}); err == nil {
			t.Error("missing file accepted")
		}
	})
}

func TestAdminFlags(t *testing.T) {
	t.Setenv("MCPMESH_ADMIN_USER", "boss")
	t.Setenv("MCPMESH_ADMIN_PASSWORD", "env password value")
	cmd := rootCmd()
	for name, want := range map[string]string{"admin-user": "boss", "admin-password": "env password value"} {
		f := cmd.Flags().Lookup(name)
		if f == nil || f.DefValue != want {
			t.Errorf("--%s = %+v, want default %q", name, f, want)
		}
	}
}
