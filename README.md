<p align="center">
  <img src="docs/images/banner.png" alt="mcp-mesh: MCP tools, across your machines. Self-hosted infrastructure for AI agents." width="896">
</p>

<p align="center">
  <img src="https://img.shields.io/badge/MCP-2025--11--25%20%C2%B7%202026--07--28-3555e8?labelColor=59636e" alt="MCP 2025-11-25 · 2026-07-28">
  <img src="https://img.shields.io/badge/runs%20on-Linux%20%C2%B7%20macOS-3555e8?labelColor=59636e" alt="Runs on Linux · macOS">
  <img src="https://img.shields.io/badge/hub-Docker-3555e8?labelColor=59636e" alt="Hub: Docker">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-PolyForm%20Noncommercial-3555e8?labelColor=59636e" alt="License: PolyForm Noncommercial"></a>
</p>

mcp-mesh brings the MCP servers on your machines into one place. Connect an
AI agent to your hub and let it use tools from your desktop, laptop, or home
server.

Run the hub once. Add a node on each machine. Keep your tools where they are.

[Quick start](#quick-start) · [Built for teams](#built-for-teams) · [How it works](#how-it-works) · [Documentation](#documentation) · [Commercial use](#commercial-use)

## What you can do

- **Use tools across machines**<br>
  Let an agent on your laptop render in Blender on your workstation.
- **Connect different MCPs**<br>
  Launch a command or connect to an MCP server already running in an app.
- **See what is happening**<br>
  Check node health, browse tools, and inspect live calls and errors.
- **Control access**<br>
  Create separate keys for machines and clients; revoke them immediately.

<p align="center">
  <img src="docs/images/dashboard.png" alt="The hub dashboard's overview: nodes online, MCP servers, available tools, calls per hour and recent calls" width="896">
</p>

<sub>The hub dashboard: nodes, tools, calls, and access in one place.</sub>

## Built for teams

One hub gives every agent in a team the same tools, and gives whoever runs
it control over what each agent may do.

- **Self-hosted.** The hub is one container with one SQLite file. Tool
  calls, keys and logs stay on your infrastructure.
- **No open ports on workstations.** Nodes connect out to the hub; the
  machines that run MCP servers accept no connections.
- **Least privilege.** Each client key sees only what its groups grant: a
  whole machine, one MCP server, or single tools. A key in no group sees
  nothing.
- **Tool changes are reviewed.** An MCP server that rewrites a tool's
  description is a classic prompt-injection path. Per-machine trust levels
  hold changed (or all new) tools until an admin approves the exact diff.
- **An audit trail.** Every call is logged with who, what, how long and the
  outcome, never the arguments or results. Sign-ins, key, group and
  approval changes have their own admin log.
- **Instant revocation.** Revoking a key closes its connections and cancels
  its calls in progress.
- **Sign-in for apps.** Claude and other apps connect with OAuth; an admin
  approves each app and picks what it gets.

<p align="center">
  <img src="docs/images/dashboard-approvals.png" alt="An approval waiting: a tool's description changed to ask the agent to send ~/.ssh to the notes server, shown as a diff of the tool's definition" width="896">
</p>

<sub>Approvals: an MCP server changed a tool to ask agents for your SSH keys. The tool stays hidden until an admin approves the change.</sub>

<p align="center">
  <img src="docs/images/dashboard-groups.png" alt="Editing a group: a tree of nodes, MCP servers and tools to grant, and the client keys in the group" width="560">
</p>

<sub>Groups: grant whole machines, MCP servers or single tools to client keys.</sub>

Using mcp-mesh at work needs a commercial license; see
[Commercial use](#commercial-use).

## How it works

An MCP server normally exposes tools on one machine. mcp-mesh makes those
tools available through a hub, so an agent can reach them from another
device.

<p align="center">
  <img src="docs/images/how-it-works.png" alt="An AI client connects to the mcp-mesh hub with a client key; nodes on the workstation, mac-mini and laptop connect to the hub with node keys" width="896">
</p>

Tools are named for their machine and MCP, so agents know where an action
runs:

```
workstation__blender__render
```

| Piece | Role |
| --- | --- |
| **Hub** | Runs once; routes tools and hosts the dashboard. |
| **Node** | Runs on each machine and shares its MCP servers with the hub. |
| **MCP** | A tool server that a node launches or connects to. |
| **Tool** | An action an agent can call, such as render or read_file. |
| **Client** | An AI agent or app connected to the hub. |
| **Call** | One tool invocation, recorded in the activity / audit log. |

## Quick start

You will need a machine for the hub, a machine with an MCP server, and an
MCP-compatible client. The hub and node can run on the same machine.

### 1. Run the hub

The hub runs in Docker. Save this as `compose.yaml` (or use
[deploy/compose.yaml](deploy/compose.yaml)), set the name your machines will
reach the hub by, and start it:

```yaml
services:
  hub:
    image: ghcr.io/aldrinsalazar/mcp-mesh-hub:1
    restart: unless-stopped
    environment:
      MCPMESH_PUBLIC_HOST: hub.home.lan # the hub's host name or address
    volumes:
      - hub-data:/data                  # all of the hub's state
    ports:
      - "127.0.0.1:7000:7000"           # dashboard; drop 127.0.0.1: to open it to your network
      - "7001:7001"                     # nodes
      - "7002:7002"                     # AI clients
volumes:
  hub-data:
```

```sh
docker compose up -d
docker compose logs hub     # the first admin password is printed once
```

Open the dashboard at `http://localhost:7000` and sign in as `admin`.

[Hub setup →](docs/hub.md)

### 2. Connect a machine

Download `mcp-mesh-node` for that machine from the
[latest release](https://github.com/AldrinSalazar/mcp-mesh/releases/latest)
(Linux and macOS; Windows is a preview). In the dashboard's Keys, create a
node key named after the machine. The dashboard shows a one-line setup
command; run it on that machine:

```sh
mcp-mesh-node init --hub ws://<hub>:7001/node --name <machine> --key <node-key>
```

### 3. Add your MCP servers

Run `mcp-mesh-node` for the node's terminal dashboard: add MCP servers,
start the node, and watch each server's state, logs and tools. Closing it
leaves the node running, and it can start the node at login.

<p align="center">
  <img src="docs/images/node-tui.png" alt="The node's terminal dashboard: the node running, connected to the hub, five MCP servers with their state and tool counts, and the filesystem server's tools" width="896">
</p>

Launch an MCP command, or connect to an MCP server an app already runs
(Blender, a game engine, a dev server). The app keeps listening on
localhost; the node shares it through the hub.

<p align="center">
  <img src="docs/images/node-tui-add.png" alt="Adding an MCP server in the terminal dashboard: connect to a running server at http://127.0.0.1:30010/mcp, named unreal, started lazily" width="640">
</p>

The same works from scripts:

```sh
mcp-mesh-node add -- npx -y @modelcontextprotocol/server-filesystem ~/code
mcp-mesh-node add --name blender --url http://127.0.0.1:9876/mcp
mcp-mesh-node start
```

[Node configuration →](docs/node.md)

### 4. Connect an AI agent

**With a key** (Claude Code, Claude Desktop, Cursor, any MCP client):
create a client key in the dashboard. Keys shows ready-to-paste setup for
each client; for Claude Code:

```sh
claude mcp add --transport http mcp-mesh http://<hub>:7002/mcp \
  --header "Authorization: Bearer <client-key>"
```

**With OAuth** (claude.ai and the Claude apps, and other apps that add MCP
servers by URL): in Claude, open Customize → Connectors, add a custom
connector, and paste the hub's URL, `https://<hub>/mcp`. Claude opens the
hub's sign-in page: an admin signs in, chooses the groups the app gets, and
allows it. On Team and Enterprise plans, an owner adds the connector for the
organization first.

<p align="center">
  <img src="docs/images/oauth-consent.png" alt="The hub's authorization page: Claude wants to use the tools on home-lab; the admin picks the groups it gets (Design studio, Everything, Read only), names its key, and allows or denies" width="480">
</p>

Claude connects to remote MCP servers from Anthropic's cloud, so for
claude.ai the hub's client port must be reachable from the internet at an
HTTPS host name, for example behind a reverse proxy. The dashboard can stay
private.

> [!NOTE]
> **Keys are shown once.** Copy a key when it is created and keep it
> private. Revoking it cuts access off immediately.

[Client setup →](docs/clients.md)

## Documentation

| Guide | What you will find |
| --- | --- |
| [Hub setup](docs/hub.md) | Docker, ports, keys and admins, TLS, OAuth, and backups. |
| [Node configuration](docs/node.md) | Installing, commands, app connections, and MCP start modes. |
| [Client setup](docs/clients.md) | Connect Claude Code, Claude Desktop, Cursor, and apps that use OAuth. |
| [API reference](docs/api.md) | Endpoints, authentication, and live updates. |
| [Architecture](docs/architecture.md) | Modules, their boundaries, and how a call travels. |

## Contributing

Contributions are welcome: bug reports, documentation, client examples, and
code. Open an issue before starting a substantial change so the approach can
be discussed.

- **Report a bug**<br>
  Include your hub and node versions, steps to reproduce, and relevant logs.
- **Improve the docs**<br>
  Fix unclear instructions or add a working example from your setup.
- **Submit a pull request**<br>
  Explain the change, keep it focused, and describe how you verified it.
  See [CONTRIBUTING.md](CONTRIBUTING.md) for building and testing.

Use Discussions for questions and ideas. Remove keys and other secrets from
reports, logs, and examples before sharing them.

## License

mcp-mesh is source-available under the
[PolyForm Noncommercial License 1.0.0](LICENSE): free for personal,
research, educational and other non-commercial use. Commercial use needs a
commercial license from the author. See [LICENSE](LICENSE) for the terms.

### Commercial use

To use mcp-mesh in a company or for any commercial purpose, contact
[mcp-mesh@astronauta.dev](mailto:mcp-mesh@astronauta.dev) for a commercial
license. Tell us a little about your team and how you plan to use it.
