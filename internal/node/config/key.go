package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// KeyPrefix starts every node key the hub creates.
const KeyPrefix = "mm_node_"

// DefaultKeyFile is the file name `key set` uses next to node.yaml.
const DefaultKeyFile = "node.key"

// ValidateKey checks the shape of a node key without contacting the hub, so
// typos and pasted-in noise are caught before they are written to disk.
func ValidateKey(key string) error {
	switch {
	case !strings.HasPrefix(key, KeyPrefix):
		return fmt.Errorf("a node key starts with %s; create one on the hub with `mcp-mesh-hub key create node <node-name>`", KeyPrefix)
	case len(key) < len(KeyPrefix)+16:
		return errors.New("that node key is too short")
	case len(key) > 512:
		return errors.New("that node key is too long")
	}
	for _, r := range key {
		if r <= ' ' || r > '~' {
			return errors.New("a node key has no spaces or control characters; paste only the key")
		}
	}
	return nil
}

// KeySource says where node.yaml gets the hub key from, without reading it.
type KeySource struct {
	// Env is the key_env variable name, File the key_file path (with ~ and
	// ${env:} expanded when possible). Both are empty when no key is set.
	Env, File string
}

func (k KeySource) Set() bool { return k.Env != "" || k.File != "" }

// HubKey reads which credential source node.yaml names. It does not
// validate the file, so it works for configs that are otherwise invalid.
func HubKey(path string) (KeySource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return KeySource{}, fmt.Errorf("read %s: %w", path, err)
	}
	doc, err := readDocument(data, path)
	if err != nil {
		return KeySource{}, err
	}
	var raw struct {
		Hub struct {
			KeyEnv  string `yaml:"key_env"`
			KeyFile string `yaml:"key_file"`
		} `yaml:"hub"`
	}
	if err := doc.Decode(&raw); err != nil {
		return KeySource{}, decodeError(path, err)
	}
	src := source{path: path, doc: doc}
	result := KeySource{Env: raw.Hub.KeyEnv, File: raw.Hub.KeyFile}
	if file, err := src.expand(raw.Hub.KeyFile, "hub.key_file", true); err == nil {
		result.File = file
	}
	return result, nil
}

// WriteKeyFile replaces path with the key, mode 0600, through a temporary
// file so a crash never leaves a half-written credential.
func WriteKeyFile(path, key string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	// Follow a symlinked key file (e.g. into a secrets directory).
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".node.key-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if _, err := file.WriteString(key + "\n"); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", path, err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("publish %s: %w", path, err)
	}
	return nil
}

// DefaultKeyPath is where StoreKey keeps the key: next to node.yaml.
func DefaultKeyPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), DefaultKeyFile)
}

// StoreKey saves key in DefaultKeyPath (mode 0600) and points hub.key_file
// at it, replacing any key_env. If node.yaml rejects the change (say, a
// ws:// hub on the internet) the previous key file is put back. It returns
// the key file.
func StoreKey(configPath, key string) (string, error) {
	file := DefaultKeyPath(configPath)
	previous, readErr := os.ReadFile(file)
	if err := WriteKeyFile(file, key); err != nil {
		return "", err
	}
	if err := SetKeyFile(configPath, file); err != nil {
		if readErr == nil {
			_ = WriteKeyFile(file, strings.TrimSpace(string(previous)))
		} else {
			_ = os.Remove(file)
		}
		return "", err
	}
	return file, nil
}

// SetKeyFile points hub.key_file at keyFile and drops hub.key_env, keeping
// comments. The file must already exist: the edited config is validated.
func SetKeyFile(path, keyFile string) error {
	err := editConfig(path, func(root *yaml.Node) error {
		hub, err := hubMapping(root)
		if err != nil {
			return err
		}
		file, env := mappingIndex(hub, "key_file"), mappingIndex(hub, "key_env")
		switch {
		case file >= 0:
			hub.Content[file+1] = stringNode(keyFile)
			if env >= 0 {
				removePair(hub, env)
			}
		case env >= 0:
			// Take the old entry's place so the file stays readable.
			hub.Content[env].Value = "key_file"
			hub.Content[env+1] = stringNode(keyFile)
		default:
			// The example config ends hub with commented-out credential
			// options, which YAML keeps as the last pair's foot comment. Move
			// them above the new key so they read as its documentation rather
			// than floating after a blank line.
			name := stringNode("key_file")
			if n := len(hub.Content); n >= 2 {
				name.HeadComment = joinComments(hub.Content[n-2].FootComment, hub.Content[n-1].FootComment)
				hub.Content[n-2].FootComment, hub.Content[n-1].FootComment = "", ""
			}
			hub.Content = append(hub.Content, name, stringNode(keyFile))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("set hub.key_file in %s: %w", path, err)
	}
	return nil
}

// ClearKey removes hub.key_file and hub.key_env.
func ClearKey(path string) error {
	err := editConfig(path, func(root *yaml.Node) error {
		hub, err := hubMapping(root)
		if err != nil {
			return err
		}
		for _, key := range []string{"key_file", "key_env"} {
			if i := mappingIndex(hub, key); i >= 0 {
				// Option comments above the key (the example's) outlive it.
				hub.Content[i].FootComment = joinComments(hub.Content[i].HeadComment, hub.Content[i].FootComment)
				hub.Content[i].HeadComment = ""
				removePair(hub, i)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("clear hub key in %s: %w", path, err)
	}
	return nil
}

// SetHubURL sets hub.url, keeping comments.
func SetHubURL(path, hubURL string) error {
	err := editConfig(path, func(root *yaml.Node) error {
		hub, err := hubMapping(root)
		if err != nil {
			return err
		}
		if i := mappingIndex(hub, "url"); i >= 0 {
			hub.Content[i+1] = stringNode(hubURL)
		} else {
			hub.Content = append([]*yaml.Node{stringNode("url"), stringNode(hubURL)}, hub.Content...)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("set hub.url in %s: %w", path, err)
	}
	return nil
}

func hubMapping(root *yaml.Node) (*yaml.Node, error) {
	i := mappingIndex(root, "hub")
	if i < 0 || root.Content[i+1].Kind != yaml.MappingNode {
		return nil, errors.New("hub must be a mapping with a url")
	}
	return root.Content[i+1], nil
}

// localHost reports whether sending a key in clear text to host is
// acceptable: a machine or network the user controls. It never resolves DNS;
// names are judged by their shape only.
func localHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i] // IPv6 zone
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		cgnat := netip.MustParsePrefix("100.64.0.0/10")
		return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || cgnat.Contains(addr)
	}
	if !strings.Contains(host, ".") && !strings.Contains(host, ":") {
		return true // single-label names resolve on the local network only
	}
	for _, suffix := range []string{".local", ".lan", ".home.arpa", ".ts.net"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
