//go:build linux || darwin

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func fakeShell(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-shell")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCaptureLoginEnvFakeShell(t *testing.T) {
	for _, plain := range []bool{false, true} {
		name, envFunction := "nul", `env() {
  printf 'PATH=/tmp/bin space=equals\nnext\000NVM_BIN=/tmp/nvm\000SECRET=hidden\000'
}`
		want := map[string]string{"PATH": "/tmp/bin space=equals\nnext", "NVM_BIN": "/tmp/nvm"}
		if plain {
			name = "plain"
			envFunction = `env() {
  if [ "$1" = '-0' ]; then printf 'unsupported\n' >&2; return 1; fi
  printf 'PATH=/tmp/bin space=equals\nNVM_BIN=/tmp/nvm\nSECRET=hidden\n'
}`
			want["PATH"] = "/tmp/bin space=equals"
		}
		t.Run(name, func(t *testing.T) {
			shell := fakeShell(t, `
[ "$1" = '-ilc' ] || exit 3
# stdin must be EOF; receiving input makes this test shell fail.
if IFS= read -r input; then exit 4; fi
printf 'PATH=outside-before\nLogin shell banner\n'
printf 'profile warning\n' >&2
`+envFunction+`
eval "$2"
printf 'PATH=outside-after\nGoodbye\n'
`)
			t.Setenv("SHELL", shell)
			got, err := CaptureLoginEnv(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("env = %q, want %q", got, want)
			}
		})
	}
}

func TestCaptureFakeShellTimeout(t *testing.T) {
	shell := fakeShell(t, "while :; do :; done\n")
	t.Setenv("SHELL", shell)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := CaptureLoginEnv(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
}
