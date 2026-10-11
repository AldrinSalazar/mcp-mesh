package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var Keep = []string{"PATH", "NVM_DIR", "NVM_BIN", "FNM_DIR", "FNM_MULTISHELL_PATH",
	"VOLTA_HOME", "PNPM_HOME", "BUN_INSTALL", "GOPATH", "CARGO_HOME", "PYENV_ROOT",
	"LANG", "LC_ALL"}

// CaptureLoginEnv reads only the marked environment emitted by the login shell.
// It does not persist or apply the result; the install/refresh CLI owns those steps.
// If env -0 is unavailable, plain env cannot preserve multiline values.
func CaptureLoginEnv(ctx context.Context) (map[string]string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return captureLoginEnv(ctx, commandRunner{}, shell, 10*time.Second)
}

func captureLoginEnv(ctx context.Context, runner Runner, shell string, timeout time.Duration) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("create login environment markers: %w", err)
	}
	id := hex.EncodeToString(nonce[:])
	begin, end := "MCP_MESH_ENV_BEGIN_"+id, "MCP_MESH_ENV_END_"+id
	// Avoid POSIX-only conditionals: the user's shell may be fish or csh.
	command := "printf '%s\\n' '" + begin + "'; env -0 || env; printf '\\n%s\\n' '" + end + "'"
	out, err := runner.Run(ctx, shell, "-ilc", command)
	if err != nil {
		return nil, fmt.Errorf("capture login environment with %s: %w", shell, err)
	}
	start := strings.Index(string(out), begin+"\n")
	if start < 0 {
		return nil, errors.New("login shell did not emit the environment start marker")
	}
	body := string(out)[start+len(begin)+1:]
	stop := strings.Index(body, "\n"+end+"\n")
	// Plain env already ends with a newline; env -0 needs the added newline.
	if stop < 0 && strings.HasPrefix(body, end+"\n") {
		stop = 0
	}
	if stop < 0 {
		return nil, errors.New("login shell did not emit the environment end marker")
	}
	body = body[:stop]
	delimiter := "\n"
	if strings.ContainsRune(body, 0) {
		delimiter = "\x00"
	} else {
		// Plain env ends each record with a newline; remove only that separator.
		body = strings.TrimSuffix(body, "\n")
	}
	allowed := make(map[string]bool, len(Keep))
	for _, key := range Keep {
		allowed[key] = true
	}
	env := make(map[string]string)
	for _, entry := range strings.Split(body, delimiter) {
		key, value, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			env[key] = value
		}
	}
	return env, nil
}

const envHeader = "# mcp-mesh environment v1: KEY=Go-quoted-string (strconv.Quote/Unquote).\n" +
	"# One entry per line; escapes encode quotes, backslashes, newlines and controls.\n" +
	"# Read with ReadEnvFile, not by sourcing this file in a shell.\n"

// WriteEnvFile atomically replaces path with a sorted, private environment file.
func WriteEnvFile(path string, env map[string]string) error {
	if err := validateEnv(env); err != nil {
		return err
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var buf strings.Builder
	buf.WriteString(envHeader)
	for _, key := range keys {
		fmt.Fprintf(&buf, "%s=%s\n", key, strconv.Quote(env[key]))
	}
	return writeAtomic(path, []byte(buf.String()), 0600)
}

func ReadEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read environment file %s: %w", path, err)
	}
	env := make(map[string]string)
	for i, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("read environment file %s line %d: missing =", path, i+1)
		}
		decoded, err := strconv.Unquote(value)
		if err != nil || !strings.HasPrefix(value, `"`) {
			return nil, fmt.Errorf("read environment file %s line %d: invalid quoted value", path, i+1)
		}
		if _, exists := env[key]; exists {
			return nil, fmt.Errorf("read environment file %s line %d: duplicate key %s", path, i+1, key)
		}
		env[key] = decoded
	}
	if err := validateEnv(env); err != nil {
		return nil, fmt.Errorf("read environment file %s: %w", path, err)
	}
	return env, nil
}

// ApplyEnv validates all entries before changing the process environment.
func ApplyEnv(env map[string]string) error {
	if err := validateEnv(env); err != nil {
		return err
	}
	for key, value := range env {
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set environment variable %s: %w", key, err)
		}
	}
	return nil
}

func validateEnv(env map[string]string) error {
	for key, value := range env {
		if key == "" || strings.ContainsAny(key, "=\x00\r\n") || strings.ContainsRune(value, 0) {
			return errors.New("environment has an invalid key or NUL in a value")
		}
		for i, c := range key {
			if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_' || (i > 0 && c >= '0' && c <= '9')) {
				return fmt.Errorf("invalid environment key %q", key)
			}
		}
	}
	return nil
}

func makeLogDir(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create log directory for %s: %w", path, err)
	}
	return nil
}
