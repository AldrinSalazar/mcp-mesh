// Package config loads and validates the node daemon's YAML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"go.yaml.in/yaml/v3"
)

type Config struct {
	Path     string
	Hub      Hub
	NodeName string
	LogLevel slog.Level
	MCPs     []MCP
}

type Hub struct {
	URL    string
	Header http.Header
	// AllowInsecure permits sending the key over ws:// to a non-local host.
	AllowInsecure bool
}

type MCP struct {
	Name        string
	Transport   proto.Transport
	Command     string
	Args        []string
	Env         []string
	Dir         string
	URL         string
	Headers     map[string]string
	Sharing     proto.Sharing
	Start       proto.Start
	IdleTimeout time.Duration
	AllowTools  []string
	// CallTimeout bounds one call on the hub; zero means the hub's default.
	CallTimeout time.Duration
	// MaxClients caps per-client channels open at once (per-client only).
	MaxClients int
}

// DefaultMaxClients is the per-client channel cap when max_clients is unset.
const DefaultMaxClients = 8

type rawConfig struct {
	Hub struct {
		URL     string  `yaml:"url"`
		KeyEnv  *string `yaml:"key_env"`
		KeyFile *string `yaml:"key_file"`
		// AllowInsecure opts out of the ws:// guard for a hub on a network the
		// user trusts but this check cannot recognize.
		AllowInsecure bool `yaml:"allow_insecure"`
	} `yaml:"hub"`
	Node struct {
		Name string `yaml:"name"`
	} `yaml:"node"`
	LogLevel string            `yaml:"log_level"`
	MCPs     map[string]rawMCP `yaml:"mcps"`
}

type rawMCP struct {
	Transport   *string           `yaml:"transport"`
	Command     string            `yaml:"command"`
	Args        []string          `yaml:"args"`
	Env         map[string]string `yaml:"env"`
	Dir         string            `yaml:"dir"`
	URL         string            `yaml:"url"`
	Headers     map[string]string `yaml:"headers"`
	Sharing     string            `yaml:"sharing"`
	Start       string            `yaml:"start"`
	IdleTimeout *string           `yaml:"idle_timeout"`
	CallTimeout *string           `yaml:"call_timeout"`
	MaxClients  *int              `yaml:"max_clients"`
	AllowTools  []string          `yaml:"allow_tools"`
	Disabled    bool              `yaml:"disabled"`
}

var (
	mcpNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	envPattern     = regexp.MustCompile(`\$\{env:([^{}]+)\}`)
	yamlLine       = regexp.MustCompile(`^(?:yaml: )?line ([0-9]+):\s*`)
	unknownField   = regexp.MustCompile(`field (\S+) not found in type .*$`)
)

// Load reads the named file and applies the same rules as Parse.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return Parse(data, path)
}

// Parse resolves environment references and credentials at load time.
func Parse(data []byte, path string) (*Config, error) {
	var raw rawConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		return nil, decodeError(path, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, decodeError(path, err)
		}
		return nil, fmt.Errorf("%s:%d: only one YAML document is supported", path, extra.Line)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, decodeError(path, err)
	}
	source := source{path: path, doc: &doc}
	cfg := &Config{Path: path, Hub: Hub{Header: make(http.Header)}}
	var err error
	cfg.Hub.URL, err = source.expand(raw.Hub.URL, "hub.url", false)
	if err != nil {
		return nil, err
	}
	if err := validateURL(cfg.Hub.URL, "ws", "wss"); err != nil {
		return nil, source.error("hub.url", err)
	}
	if raw.Hub.KeyEnv != nil && raw.Hub.KeyFile != nil {
		return nil, source.error("hub.key_file", errors.New("key_env and key_file are mutually exclusive"))
	}
	var key string
	if raw.Hub.KeyEnv != nil {
		name, err := source.expand(*raw.Hub.KeyEnv, "hub.key_env", false)
		if err != nil {
			return nil, err
		}
		value, ok := os.LookupEnv(name)
		if !ok {
			return nil, source.error("hub.key_env", fmt.Errorf("environment variable %q is not set", name))
		}
		key = strings.TrimSpace(value)
		if key == "" {
			return nil, source.error("hub.key_env", fmt.Errorf("environment variable %q contains an empty key", name))
		}
	}
	if raw.Hub.KeyFile != nil {
		file, err := source.expand(*raw.Hub.KeyFile, "hub.key_file", true)
		if err != nil {
			return nil, err
		}
		key, err = readKeyFile(file)
		if err != nil {
			return nil, source.error("hub.key_file", err)
		}
	}
	cfg.Hub.AllowInsecure = raw.Hub.AllowInsecure
	if key != "" {
		if u, err := url.Parse(cfg.Hub.URL); err == nil && u.Scheme == "ws" && !cfg.Hub.AllowInsecure && !localHost(u.Hostname()) {
			return nil, source.error("hub.url", fmt.Errorf("ws:// would send the node key unencrypted to %s; use wss:// or set hub.allow_insecure: true", u.Hostname()))
		}
		cfg.Hub.Header.Set("Authorization", "Bearer "+key)
	}
	cfg.NodeName, err = source.expand(raw.Node.Name, "node.name", false)
	if err != nil {
		return nil, err
	}
	if cfg.NodeName == "" {
		cfg.NodeName, err = os.Hostname()
		if err != nil {
			return nil, source.error("node.name", fmt.Errorf("hostname: %w", err))
		}
	}
	level, err := source.expand(raw.LogLevel, "log_level", false)
	if err != nil {
		return nil, err
	}
	switch level {
	case "", "info":
		cfg.LogLevel = slog.LevelInfo
	case "debug":
		cfg.LogLevel = slog.LevelDebug
	case "warn":
		cfg.LogLevel = slog.LevelWarn
	case "error":
		cfg.LogLevel = slog.LevelError
	default:
		return nil, source.error("log_level", errors.New("must be debug, info, warn or error"))
	}
	for _, name := range sortedKeys(raw.MCPs) {
		if !mcpNamePattern.MatchString(name) {
			return nil, source.error("mcps."+name, errors.New("MCP name must match [A-Za-z0-9_-]+"))
		}
		entry := raw.MCPs[name]
		// Disabled entries may refer to commands or credentials unavailable here.
		if entry.Disabled {
			continue
		}
		mcp, err := source.mcp(name, entry)
		if err != nil {
			return nil, err
		}
		cfg.MCPs = append(cfg.MCPs, mcp)
	}
	return cfg, nil
}

func (s source) mcp(name string, raw rawMCP) (MCP, error) {
	mcp := MCP{Name: name, Sharing: proto.SharingShared, Start: proto.StartEager}
	prefix := "mcps." + name + "."
	// Keep the field path alongside each value so expansion errors identify it.
	for _, field := range []struct {
		name string
		from string
		to   *string
		home bool
	}{
		{"command", raw.Command, &mcp.Command, true},
		{"dir", raw.Dir, &mcp.Dir, true},
		{"url", raw.URL, &mcp.URL, false},
	} {
		value, err := s.expand(field.from, prefix+field.name, field.home)
		if err != nil {
			return MCP{}, err
		}
		*field.to = value
	}
	if mcp.Command != "" && mcp.URL != "" {
		return MCP{}, s.error(prefix+"url", errors.New("command and url together are not supported yet"))
	}
	if mcp.Command == "" && mcp.URL == "" {
		return MCP{}, s.error("mcps."+name, errors.New("exactly one of command or url is required"))
	}
	transportValue := ""
	if raw.Transport != nil {
		transportValue = *raw.Transport
	}
	transport, err := s.expand(transportValue, prefix+"transport", false)
	if err != nil {
		return MCP{}, err
	}
	if transport != "" && transport != "http" && transport != "sse" {
		return MCP{}, s.error(prefix+"transport", errors.New("transport must be http or sse"))
	}
	if mcp.Command != "" && raw.Transport != nil {
		return MCP{}, s.error(prefix+"transport", errors.New("transport is only allowed with url"))
	}
	if mcp.Command != "" {
		mcp.Transport = proto.TransportStdio
	} else {
		mcp.Transport = proto.TransportHTTP
		if transport == "sse" {
			mcp.Transport = proto.TransportSSE
		}
		if err := validateURL(mcp.URL, "http", "https"); err != nil {
			return MCP{}, s.error(prefix+"url", err)
		}
	}
	mcp.Args, err = s.expandList(raw.Args, prefix+"args", true)
	if err != nil {
		return MCP{}, err
	}
	mcp.AllowTools, err = s.expandList(raw.AllowTools, prefix+"allow_tools", false)
	if err != nil {
		return MCP{}, err
	}
	for _, key := range sortedKeys(raw.Env) {
		value, err := s.expand(raw.Env[key], prefix+"env."+key, false)
		if err != nil {
			return MCP{}, err
		}
		mcp.Env = append(mcp.Env, key+"="+value)
	}
	if raw.Headers != nil {
		mcp.Headers = make(map[string]string, len(raw.Headers))
	}
	for _, key := range sortedKeys(raw.Headers) {
		value, err := s.expand(raw.Headers[key], prefix+"headers."+key, false)
		if err != nil {
			return MCP{}, err
		}
		mcp.Headers[key] = value
	}
	sharing, err := s.expand(raw.Sharing, prefix+"sharing", false)
	if err != nil {
		return MCP{}, err
	}
	switch sharing {
	case "", string(proto.SharingShared):
		mcp.Sharing = proto.SharingShared
	case string(proto.SharingPerClient):
		mcp.Sharing = proto.SharingPerClient
	default:
		return MCP{}, s.error(prefix+"sharing", errors.New("must be shared or per-client"))
	}
	start, err := s.expand(raw.Start, prefix+"start", false)
	if err != nil {
		return MCP{}, err
	}
	switch start {
	case "", string(proto.StartEager):
	case string(proto.StartLazy):
		mcp.Start = proto.StartLazy
		mcp.IdleTimeout = 10 * time.Minute
	default:
		return MCP{}, s.error(prefix+"start", errors.New("must be eager or lazy"))
	}
	if mcp.Sharing == proto.SharingPerClient {
		// Each client's process starts on its first call and idles out.
		if start == string(proto.StartEager) {
			return MCP{}, s.error(prefix+"start", errors.New("per-client MCPs start for each client on its first call; use start: lazy or leave it out"))
		}
		if mcp.Start != proto.StartLazy {
			mcp.Start, mcp.IdleTimeout = proto.StartLazy, 10*time.Minute
		}
		mcp.MaxClients = DefaultMaxClients
		if raw.MaxClients != nil {
			if *raw.MaxClients < 1 || *raw.MaxClients > 256 {
				return MCP{}, s.error(prefix+"max_clients", errors.New("must be between 1 and 256"))
			}
			mcp.MaxClients = *raw.MaxClients
		}
	} else if raw.MaxClients != nil {
		return MCP{}, s.error(prefix+"max_clients", errors.New("max_clients is only allowed with sharing: per-client"))
	}
	if raw.CallTimeout != nil {
		value, err := s.expand(*raw.CallTimeout, prefix+"call_timeout", false)
		if err != nil {
			return MCP{}, err
		}
		mcp.CallTimeout, err = time.ParseDuration(value)
		if err != nil {
			return MCP{}, s.error(prefix+"call_timeout", fmt.Errorf("parse duration: %w", err))
		}
		if mcp.CallTimeout <= 0 || mcp.CallTimeout > 24*time.Hour {
			return MCP{}, s.error(prefix+"call_timeout", errors.New("must be a positive duration up to 24h"))
		}
	}
	if raw.IdleTimeout != nil {
		if mcp.Start != proto.StartLazy {
			return MCP{}, s.error(prefix+"idle_timeout", errors.New("idle_timeout is only allowed with start: lazy"))
		}
		value, err := s.expand(*raw.IdleTimeout, prefix+"idle_timeout", false)
		if err != nil {
			return MCP{}, err
		}
		mcp.IdleTimeout, err = time.ParseDuration(value)
		if err != nil {
			return MCP{}, s.error(prefix+"idle_timeout", fmt.Errorf("parse duration: %w", err))
		}
		if mcp.IdleTimeout <= 0 {
			return MCP{}, s.error(prefix+"idle_timeout", errors.New("must be a positive duration"))
		}
	}
	return mcp, nil
}

func validateURL(value string, schemes ...string) error {
	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	for _, scheme := range schemes {
		if u.Scheme == scheme && u.Hostname() != "" {
			return nil
		}
	}
	return fmt.Errorf("must be an absolute %s URL with a host", strings.Join(schemes, " or "))
}

func readKeyFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if err := checkKeyPermissions(path, info); err != nil {
		return "", err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("%s contains an empty key", path)
	}
	return key, nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type source struct {
	path string
	doc  *yaml.Node
}

func (s source) error(field string, err error) error {
	return fmt.Errorf("%s:%d: %s: %w", s.path, s.line(field), field, err)
}

func (s source) line(field string) int {
	node := s.doc
	if len(node.Content) == 0 {
		return 1
	}
	if node.Kind == yaml.DocumentNode {
		node = node.Content[0]
	}
	parts := strings.Split(field, ".")
	for len(parts) > 0 {
		if node.Kind == yaml.AliasNode {
			node = node.Alias
		}
		var next *yaml.Node
		consumed := 1
		switch node.Kind {
		case yaml.MappingNode:
			for i := 0; i < len(node.Content); i += 2 {
				keyParts := strings.Split(node.Content[i].Value, ".")
				if len(keyParts) <= len(parts) && strings.Join(parts[:len(keyParts)], ".") == node.Content[i].Value && (next == nil || len(keyParts) > consumed) {
					next = node.Content[i+1]
					consumed = len(keyParts)
				}
			}
		case yaml.SequenceNode:
			i, err := strconv.Atoi(parts[0])
			if err == nil && i >= 0 && i < len(node.Content) {
				next = node.Content[i]
			}
		}
		if next == nil {
			break
		}
		node = next
		parts = parts[consumed:]
	}
	if node.Line == 0 {
		return 1
	}
	return node.Line
}

func (s source) expand(value, field string, home bool) (string, error) {
	var result strings.Builder
	last := 0
	for _, match := range envPattern.FindAllStringSubmatchIndex(value, -1) {
		name := value[match[2]:match[3]]
		replacement, ok := os.LookupEnv(name)
		if !ok {
			return "", s.error(field, fmt.Errorf("environment variable %q is not set", name))
		}
		result.WriteString(value[last:match[0]])
		result.WriteString(replacement)
		last = match[1]
	}
	result.WriteString(value[last:])
	value = result.String()
	if home && strings.HasPrefix(value, "~/") {
		dir, err := os.UserHomeDir()
		if err != nil {
			return "", s.error(field, fmt.Errorf("resolve home directory: %w", err))
		}
		value = filepath.Join(dir, value[2:])
	}
	return value, nil
}

func (s source) expandList(values []string, field string, home bool) ([]string, error) {
	if values == nil {
		return nil, nil
	}
	result := make([]string, len(values))
	for i, value := range values {
		var err error
		result[i], err = s.expand(value, field+"."+strconv.Itoa(i), home)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func decodeError(path string, err error) error {
	var typed *yaml.TypeError
	if errors.As(err, &typed) {
		messages := make([]string, len(typed.Errors))
		for i, message := range typed.Errors {
			messages[i] = locatedYAMLError(path, message)
		}
		return &yamlError{message: strings.Join(messages, "\n"), cause: err}
	}
	return &yamlError{message: locatedYAMLError(path, err.Error()), cause: err}
}

type yamlError struct {
	message string
	cause   error
}

func (e *yamlError) Error() string { return e.message }
func (e *yamlError) Unwrap() error { return e.cause }

func locatedYAMLError(path, message string) string {
	// The decoder names our internal Go types; users only need the key.
	message = unknownField.ReplaceAllString(message, `unknown key "$1"`)
	if match := yamlLine.FindStringSubmatch(message); match != nil {
		return path + ":" + match[1] + ": " + message[len(match[0]):]
	}
	return path + ": " + message
}
