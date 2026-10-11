// Package paths locates the node's configuration and runtime files without
// creating directories.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ConfigDir returns the directory containing node.yaml.
func ConfigDir() (string, error) {
	if path := os.Getenv("MCPMESH_CONFIG"); path != "" {
		return filepath.Dir(path), nil
	}
	return configDir()
}

// ConfigFile returns the configured node.yaml location.
func ConfigFile() (string, error) {
	if path := os.Getenv("MCPMESH_CONFIG"); path != "" {
		return path, nil
	}
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "node.yaml"), nil
}

// EnvFile returns the captured login environment location.
func EnvFile() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "login.env"), nil
}

// ToolCacheDir returns the directory for cached tool lists.
func ToolCacheDir() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tools"), nil
}

func checkSocketPath(path string, limit int) (string, error) {
	// sockaddr_un needs one byte for the terminating NUL on both supported OSes.
	if len(path) > limit {
		return "", fmt.Errorf("socket path exceeds limit of %d bytes (%d bytes): %s", limit, len(path), path)
	}
	if strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("socket path contains a NUL byte")
	}
	return path, nil
}
