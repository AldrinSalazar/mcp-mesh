package nodectl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
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
	result.Deleted, err = n.deleteKeyFile(source)
	return result, err
}

// deleteKeyFile deletes the key file once node.yaml no longer names it, if
// it is the default one SetKey writes; a file the user pointed to is theirs.
func (n *Node) deleteKeyFile(source config.KeySource) (bool, error) {
	if source.File == "" || filepath.Clean(source.File) != n.DefaultKeyFile() {
		return false, nil
	}
	if err := os.Remove(source.File); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("the key was removed from the config but deleting %s failed: %w", source.File, err)
	}
	return true, nil
}

// PairResult says how Pair got the node asking the hub.
type PairResult struct {
	Created   bool // node.yaml was written
	Started   bool // the node wasn't running
	Restarted bool // a running node whose config didn't change was restarted
}

// Pair sets this machine up to join hubURL as name by pairing, and gets the
// node asking: it creates node.yaml if needed, points it at the hub, drops
// the current key (a node without one asks the hub for a key) and starts the
// node, or applies the change to a running one. A running node whose config
// didn't change, say after an admin declined it, is restarted: it asks only
// once per run.
func (n *Node) Pair(ctx context.Context, hubURL, name string) (PairResult, error) {
	var result PairResult
	path := n.opts.ConfigPath
	if err := acquire(ctx, n.actions); err != nil {
		return result, err
	}
	before, _ := os.ReadFile(path)
	err := func() error {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			if err := config.Create(path, hubURL, false); err != nil {
				return err
			}
			result.Created = true
		} else if err != nil {
			return err
		} else if err := config.SetHubURL(path, hubURL); err != nil {
			return err
		}
		if err := config.SetNodeName(path, name); err != nil {
			return err
		}
		source, err := config.HubKey(path)
		if err != nil || !source.Set() {
			return err
		}
		if err := config.ClearKey(path); err != nil {
			return err
		}
		_, err = n.deleteKeyFile(source)
		return err
	}()
	after, _ := os.ReadFile(path)
	<-n.actions
	if err != nil {
		return result, err
	}
	switch _, err := n.daemonStatus(ctx); {
	case errors.Is(err, control.ErrNotRunning):
		result.Started = true
		return result, n.Start(ctx)
	case err != nil:
		return result, fmt.Errorf("config file was saved, but the node's state is unknown: %w", err)
	case !bytes.Equal(before, after):
		return result, n.client.Reload(ctx)
	}
	result.Restarted = true
	if err := n.Stop(ctx); err != nil {
		return result, err
	}
	return result, n.Start(ctx)
}
