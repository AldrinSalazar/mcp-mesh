//go:build linux || darwin

package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

func xdgDir(variable string, fallback ...string) (string, error) {
	if dir := os.Getenv(variable); dir != "" {
		return filepath.Join(dir, "mcp-mesh"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", variable, err)
	}
	parts := append([]string{home}, fallback...)
	return filepath.Join(append(parts, "mcp-mesh")...), nil
}

func configDir() (string, error) {
	return xdgDir("XDG_CONFIG_HOME", ".config")
}

// StateDir returns the node's runtime state directory.
func StateDir() (string, error) {
	return xdgDir("XDG_STATE_HOME", ".local", "state")
}
