package main

import "github.com/spf13/cobra"

func commandHelp(root *cobra.Command) {
	help := map[string]struct{ group, long, example string }{
		"init":      {"setup", "Write a starter node.yaml with a hub endpoint and example MCP entries. Supply --hub for scripts; at a terminal the command searches the local network and asks you to pick a hub or enter its URL. --name sets this machine's name in the mesh and --key saves the node key the hub created for that name, so one command sets up a new machine. Without --key, the node asks the hub to pair when it starts: an admin approves it in the hub's dashboard, and the key the hub sends back is saved for you. Existing files are kept unless --force is given.", "init --hub ws://hub:7001/node --name studio-pc --key -"},
		"discover":  {"setup", "List hubs announcing themselves with mDNS on the local network. Discovery only lists hubs and never connects to them. The hub must have mDNS enabled (the default) and share this network; Docker bridge networks and VPNs such as Tailscale do not carry discovery. Use the hub's URL in those setups. Use --timeout to set the search duration or --json for a structured list.", "discover --timeout 3s"},
		"key":       {"setup", "Manage the node key the hub requires. Create it on the hub with `mcp-mesh-hub key create node <node-name>` (the key is bound to that node name), then run `key set`. The key is stored in a file next to node.yaml readable only by you and referenced by hub.key_file; the node is reloaded if it is running. Prefer `key set -` (stdin) or the hidden prompt over an argument, which ends up in shell history. `key clear` stops using the key. A node started without a key asks the hub to pair instead, and saves the key the hub sends once an admin approves it.", "key set -"},
		"edit":      {"setup", "Open node.yaml in VISUAL or EDITOR, then validate the saved file. A running node keeps its previous config until you run reload.", "edit"},
		"config":    {"setup", "Inspect and validate node.yaml before starting the node. The check command reports the hub, node name and MCP policies without printing credentials.", "config check"},
		"install":   {"setup", "Start the node now and at every login. Installs a user service (systemd/launchd) that restarts the node if it crashes and captures your login shell's PATH so MCP commands are found. A running node moves under the service.", "install"},
		"uninstall": {"setup", "Stop starting the node at login. Removes the user service and stops the node while keeping node.yaml and state files. Use start to run the node by hand until logout.", "uninstall"},
		"env":       {"setup", "Manage the captured login environment used by the node. Refresh it after changing your shell PATH or other login settings.", "env refresh"},
		"start":     {"node", "Start the node and wait for its control socket to answer. An installed user service owns the node; otherwise it runs in the background until logout and writes node.log. Starting a running node does nothing. A node without a key asks the hub to pair; start prints the code to look for in the hub's dashboard (Approvals).", "start"},
		"stop":      {"node", "Stop the node through its user service or control socket and wait until it stops answering. Its MCPs stop and the hub loses them until you use start again. If start at login is on, the node starts again at next login.", "stop"},
		"run":       {"node", "Run the node daemon in the foreground with every enabled MCP in node.yaml. The process stays attached until interrupted; use start for a background node.", "run --config ./node.yaml"},
		"status":    {"node", "Show the node, start at login setting, hub, configuration and MCP states. This works while the node is stopped; --json provides a structured snapshot without environment or header values.", "status --json"},
		"reload":    {"node", "Apply the saved node.yaml to the running node. MCP changes (add, remove, enable, disable, edit) are saved without being applied; reload applies them, restarting the node's MCPs once calls in progress finish (at most a minute). Validation errors leave it using the previous configuration.", "reload"},
		"tui":       {"node", "Open the node dashboard to manage the node, its start at login setting, configuration and MCPs. The dashboard also works before the node has started.", "tui"},
		"add":       {"mcps", "Launch a command, or connect to an MCP an app already runs (Blender, Fusion 360, a dev server) and share it through the hub. The app listens on localhost; the node connects out to the hub so other machines can use it without opening the app’s port. URL entries default to lazy and detect Streamable HTTP or HTTP+SSE (older apps); use --start or --transport to override. Saves to node.yaml; a running node picks it up on reload.", "add -- npx -y @modelcontextprotocol/server-filesystem ~/code\n  mcp-mesh-node add --name blender --url http://127.0.0.1:9876/mcp"},
		"remove":    {"mcps", "Remove a named MCP from node.yaml; a running node drops it on reload. At a terminal removal asks for confirmation; --yes skips the question.", "remove --yes filesystem"},
		"enable":    {"mcps", "Enable a saved MCP in node.yaml; a running node starts it on reload. Its existing command, credentials and startup policy are preserved.", "enable filesystem"},
		"disable":   {"mcps", "Disable a saved MCP in node.yaml; a running node stops it on reload. Its settings remain in the file for a later enable command.", "disable filesystem"},
		"restart":   {"mcps", "Restart one MCP managed by the running node. Other MCPs and the node's hub connection continue running.", "restart filesystem"},
		"logs":      {"mcps", "Print the recent stderr of one MCP managed by the running node. Use --follow to stream new output or --bytes to limit the initial tail.", "logs --follow filesystem"},
		"wrap":      {"mcps", "Launch a command or connect to a running MCP URL immediately using hub and node defaults from node.yaml. Run it in the foreground or use --add to save it; at a terminal a running node offers to keep the MCP in its configuration.", "wrap -- npx -y @modelcontextprotocol/server-filesystem ~/code"},
	}
	for _, cmd := range root.Commands() {
		if h, ok := help[cmd.Name()]; ok {
			cmd.GroupID, cmd.Long, cmd.Example = h.group, h.long, "  mcp-mesh-node "+h.example
		}
		for _, child := range cmd.Commands() {
			child.Long = child.Short + " using the current node configuration and environment."
			child.Example = "  mcp-mesh-node " + cmd.Name() + " " + child.Name()
		}
	}
}
