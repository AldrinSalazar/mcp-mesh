package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

type envRunner struct {
	t       *testing.T
	body    string
	missing string
	seen    string
}

func (r *envRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.t.Helper()
	if name != "/fake/shell" || len(args) != 2 || args[0] != "-ilc" {
		r.t.Fatalf("unexpected shell command: %s %q", name, args)
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 10*time.Second {
		r.t.Fatal("capture did not set a timeout")
	}
	if !strings.Contains(args[1], "env -0") || !strings.Contains(args[1], "|| env;") {
		r.t.Fatal("missing NUL capture or plain env fallback")
	}
	markers := regexp.MustCompile(`MCP_MESH_ENV_(?:BEGIN|END)_[a-f0-9]{32}`).FindAllString(args[1], -1)
	if len(markers) != 2 {
		r.t.Fatalf("invalid markers in %s", args[1])
	}
	if r.seen == markers[0] {
		r.t.Fatal("marker reused between captures")
	}
	r.seen = markers[0]
	begin, end := markers[0]+"\n", "\n"+markers[1]+"\n"
	if r.missing == "start" {
		begin = ""
	}
	if r.missing == "end" {
		end = ""
	}
	return []byte("PATH=outside-before\n" + begin + r.body + end + "PATH=outside-after\n"), nil
}

func TestCaptureLoginEnvFraming(t *testing.T) {
	for _, tc := range []struct {
		name, body, missing string
		want                map[string]string
	}{
		{name: "nul", body: "PATH=space and =equals\nnewline\x00NVM_BIN=/bin/nvm\x00SECRET=hidden\x00LANG=\x00", want: map[string]string{"PATH": "space and =equals\nnewline", "NVM_BIN": "/bin/nvm", "LANG": ""}},
		{name: "plain", body: "PATH=space and =equals\nVOLTA_HOME=/tmp/volta\nSECRET=hidden", want: map[string]string{"PATH": "space and =equals", "VOLTA_HOME": "/tmp/volta"}},
		{name: "empty", want: map[string]string{}},
		{name: "no start", missing: "start"},
		{name: "no end", missing: "end"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &envRunner{t: t, body: tc.body, missing: tc.missing}
			got, err := captureLoginEnv(t.Context(), runner, "/fake/shell", 10*time.Second)
			if tc.missing != "" {
				if err == nil {
					t.Fatal("missing marker accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("env = %q, want %q", got, tc.want)
			}
			if _, err := captureLoginEnv(t.Context(), runner, "/fake/shell", 10*time.Second); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type canceledRunner struct{}

func (canceledRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCaptureTimeoutAndCancellation(t *testing.T) {
	_, err := captureLoginEnv(t.Context(), canceledRunner{}, "/fake/shell", time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = captureLoginEnv(ctx, canceledRunner{}, "/fake/shell", 10*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestEnvFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "login.env")
	env, err := ReadEnvFile(path)
	if err != nil || len(env) != 0 || env == nil {
		t.Fatalf("missing file = %v, %v", env, err)
	}
	want := map[string]string{
		"PATH": "spaces = equals \"quotes\" 'single' \\slash\nnew\r\tline\n",
		"LANG": "日本語", "EMPTY": "", "LONG": strings.Repeat("x", 128*1024),
	}
	if err := WriteEnvFile(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("environment did not round-trip")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("file mode = %o", info.Mode().Perm())
	}
	if !strings.HasPrefix(readFile(t, path), envHeader+"EMPTY=\"\"\nLANG=") {
		t.Fatal("file lacks documented format or deterministic key order")
	}
	if err := WriteEnvFile(path, map[string]string{"PATH": "replacement"}); err != nil {
		t.Fatal(err)
	}
	got, err = ReadEnvFile(path)
	if err != nil || !reflect.DeepEqual(got, map[string]string{"PATH": "replacement"}) {
		t.Fatalf("replacement = %v, %v", got, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v, %v", entries, err)
	}
	// Atomic replacement must not follow the destination's old permissions.
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteEnvFile(path, want); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("replacement permissions: %v, %v", info, err)
	}
}

func TestEnvFileRejectsInvalidData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "login.env")
	for _, text := range []string{"PATH=unquoted\n", "missing_equals\n", "PATH=\"a\"\nPATH=\"b\"\n", "BAD KEY=\"value\"\n", "PATH=\"\\x00\"\n", "PATH='x'\n"} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadEnvFile(path); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	if err := WriteEnvFile(path, map[string]string{"PATH": "original"}); err != nil {
		t.Fatal(err)
	}
	for _, env := range []map[string]string{{"": "empty key"}, {"BAD=KEY": "value"}, {"BAD\nKEY": "value"}, {"PATH": "NUL\x00value"}} {
		if err := WriteEnvFile(path, env); err == nil {
			t.Fatalf("accepted invalid env %v", env)
		}
		got, err := ReadEnvFile(path)
		if err != nil || got["PATH"] != "original" {
			t.Fatalf("invalid write changed destination: %v, %v", got, err)
		}
	}
	if _, err := ReadEnvFile(filepath.Dir(path)); err == nil {
		t.Fatal("directory accepted as env file")
	}
}

func TestApplyEnv(t *testing.T) {
	t.Setenv("MCP_MESH_TEST_ENV", "original")
	if err := ApplyEnv(map[string]string{"MCP_MESH_TEST_ENV": "spaces = \"quotes\"\nnewline"}); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("MCP_MESH_TEST_ENV"); got != "spaces = \"quotes\"\nnewline" {
		t.Fatalf("applied value = %q", got)
	}
	if err := ApplyEnv(map[string]string{"MCP_MESH_TEST_ENV": "changed", "BAD=KEY": "invalid"}); err == nil {
		t.Fatal("invalid environment accepted")
	}
	if os.Getenv("MCP_MESH_TEST_ENV") == "changed" {
		t.Fatal("validation failure partially applied environment")
	}
}
