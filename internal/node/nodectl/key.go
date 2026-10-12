package nodectl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
)

// KeyResult describes a key change for the caller to report.
type KeyResult struct {
	// File is the key file now in use (SetKey) or the one that was removed
	// from the config (ClearKey).
	File string
	// Node is the node name the hub must have bound the key to.
	Node string
	// Reloaded is true when a running daemon picked the change up.
	Reloaded bool
	// Cleared is false when ClearKey found no key to remove.
	Cleared bool
	// Deleted is true when ClearKey also deleted the key file.
	Deleted bool
}

// DefaultKeyFile is where SetKey stores the key: next to node.yaml.
func (n *Node) DefaultKeyFile() string {
	return config.DefaultKeyPath(n.opts.ConfigPath)
}

// SetKey stores the hub key in the default key file (mode 0600) and points
// hub.key_file at it, replacing any key_env, then reloads a running daemon.
// If node.yaml rejects the change (say, a ws:// hub on the internet) the
// previous key file is put back.
func (n *Node) SetKey(ctx context.Context, key string) (KeyResult, error) {
	key = strings.TrimSpace(key)
	if err := config.ValidateKey(key); err != nil {
		return KeyResult{}, err
	}
	if _, err := os.Stat(n.opts.ConfigPath); errors.Is(err, os.ErrNotExist) {
		return KeyResult{}, fmt.Errorf("%s does not exist; create it first with `mcp-mesh-node init`", n.opts.ConfigPath)
	}
	var file string
	reloaded, err := n.edit(ctx, func() (err error) {
		file, err = config.StoreKey(n.opts.ConfigPath, key)
		return err
	})
	if err != nil {
		return KeyResult{}, err
	}
	return KeyResult{File: file, Node: n.configState().Node, Reloaded: reloaded}, nil
}

// ClearKey removes hub.key_file and hub.key_env from node.yaml and reloads a
// running daemon. It deletes the key file only when it is the default one
// that SetKey writes; a file the user pointed to is theirs.
func (n *Node) ClearKey(ctx context.Context) (KeyResult, error) {
	source, err := config.HubKey(n.opts.ConfigPath)
	if err != nil {
		return KeyResult{}, err
	}
	if !source.Set() {
		return KeyResult{}, nil
	}
	reloaded, err := n.edit(ctx, func() error { return config.ClearKey(n.opts.ConfigPath) })
	if err != nil {
		return KeyResult{}, err
	}
	result := KeyResult{File: source.File, Cleared: true, Reloaded: reloaded}
	if source.File != "" && filepath.Clean(source.File) == n.DefaultKeyFile() {
		if err := os.Remove(source.File); err == nil {
			result.Deleted = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, fmt.Errorf("the key was removed from the config but deleting %s failed: %w", source.File, err)
		}
	}
	return result, nil
}
