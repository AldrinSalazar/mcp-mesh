package paths

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

func appDir(variable string) (string, error) {
	dir := os.Getenv(variable)
	if dir == "" {
		return "", fmt.Errorf("%s is not set", variable)
	}
	return filepath.Join(dir, "mcp-mesh"), nil
}

func configDir() (string, error) {
	return appDir("AppData")
}

// StateDir returns the node's runtime state directory.
func StateDir() (string, error) {
	return appDir("LocalAppData")
}

// SocketPath returns a per-user named pipe for the daemon's control API.
func SocketPath() (string, error) {
	if path := os.Getenv("MCPMESH_SOCKET"); path != "" {
		return path, nil
	}
	current, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolve named-pipe user: %w", err)
	}
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, current.Username)
	return `\\.\pipe\mcp-mesh-node-` + name, nil
}
