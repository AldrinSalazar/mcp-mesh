package paths

import (
	"os"
	"path/filepath"
)

// SocketPath returns the daemon's control socket location.
func SocketPath() (string, error) {
	if path := os.Getenv("MCPMESH_SOCKET"); path != "" {
		return checkSocketPath(path, 107)
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return checkSocketPath(filepath.Join(dir, "mcp-mesh", "node.sock"), 107)
	}
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return checkSocketPath(filepath.Join(dir, "node.sock"), 107)
}
