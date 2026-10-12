package config

// Example returns a starter configuration with optional credentials commented
// out so init does not require secrets or an existing key file.
func Example() []byte {
	return []byte(`# Connect this node to your hub's node endpoint.
hub:
  url: wss://mesh.example.com/node
  # Choose one credential source; key files must be chmod 600 on Unix.
  # key_env: MCPMESH_NODE_KEY
  # key_file: ~/.config/mcp-mesh/node.key
  # allow_insecure: true # only to send the key over ws:// to a non-local host

# Omit the name to use this machine's hostname.
# node:
#   name: work-pc
log_level: info # debug | info | warn | error
# update_check: false # don't ask GitHub whether a newer release is out

mcps:
  # Add MCPs with "mcp-mesh-node add", the dashboard ("mcp-mesh-node"), or
  # here. Each one launches a command (stdio) or connects to a URL.
  #
  # filesystem:
  #   command: npx
  #   args: ["-y", "@modelcontextprotocol/server-filesystem", "~/code"]
  #   env: { DEBUG: "1" }
  #   dir: ~/code
  #   start: lazy # eager (default) | lazy: start on first call, stop when idle
  #   idle_timeout: 15m # lazy only; defaults to 10m
  #   allow_tools: [read_text_file, list_directory] # omit to allow all tools
  #   disabled: true # keep the entry but don't run it
  #
  # browser:
  #   url: http://127.0.0.1:8931/mcp
  #   headers: { Authorization: "Bearer ${env:LOCAL_TOKEN}" }
  #
  # Connect to an app's localhost MCP and share it over the LAN through the hub.
  # blender:
  #   url: http://127.0.0.1:9876/sse
  #   transport: sse # http (Streamable HTTP, default) | sse (HTTP+SSE, older apps)
  #   start: lazy
`)
}
