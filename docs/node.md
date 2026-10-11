# Node configuration

The node, `mcp-mesh-node`, runs on each machine that has MCP servers. It
starts them (or connects to ones an app already runs) and shares them with
the hub over one outgoing connection: the machine opens no ports.

## Install

On macOS or Linux (Intel or ARM 64-bit), install it with
[Homebrew](https://brew.sh):

```sh
brew install aldrinsalazar/tap/mcp-mesh-node
```

To upgrade, run `brew upgrade mcp-mesh-node`. A running node keeps the old
version until it restarts: run `mcp-mesh-node stop && mcp-mesh-node start`
(also if it runs as a [service](#start-at-login-or-at-boot)).

Otherwise, download the archive for the machine from the
[latest release](https://github.com/AldrinSalazar/mcp-mesh/releases/latest)
and put `mcp-mesh-node` on your `PATH`:

| | x86-64 | x86 (32-bit) | ARM 64-bit | ARM 32-bit |
| --- | --- | --- | --- | --- |
| Linux | `linux_amd64` | `linux_386` | `linux_arm64` | `linux_armv7` |
| macOS | `darwin_amd64` | | `darwin_arm64` (Apple silicon) | |
| Windows ([preview](#platforms)) | `windows_amd64` | `windows_386` | `windows_arm64` | |

On Linux or macOS, for example:

```sh
curl -fsSL https://github.com/AldrinSalazar/mcp-mesh/releases/latest/download/mcp-mesh-node_linux_amd64.tar.gz | tar -xz
mkdir -p ~/.local/bin && mv mcp-mesh-node_linux_amd64/mcp-mesh-node ~/.local/bin/
```

`SHA256SUMS` in the release lists every archive's checksum. On macOS, a
binary downloaded with a browser is quarantined; clear that with
`xattr -d com.apple.quarantine mcp-mesh-node`.

## Set up a machine

In the dashboard (Keys → Create key → Node key) or with the hub's
[`key create node <machine>`](hub.md#keys-and-admins), create a key named
after the machine. It shows a one-line setup command; run it on that machine:

```sh
mcp-mesh-node init --hub ws://<hub>:7001/node --name <machine> --key <node-key>
```

It writes `~/.config/mcp-mesh/node.yaml` pointing at the hub, names the
machine (a node key only works for the name it was created for) and saves
the key in a file only you can read. Then add MCPs and start the node:

```sh
mcp-mesh-node add -- npx -y @modelcontextprotocol/server-filesystem ~/code
mcp-mesh-node start
```

Running `mcp-mesh-node` alone opens an interactive dashboard that does all
of this: create the config, pick a hub found on the network, add MCPs,
start and stop the node, show logs and tools.

## Launch or connect

- **Launch:** the node runs an MCP command over stdio.

  ```sh
  mcp-mesh-node add --name browser -- npx -y @playwright/mcp@latest
  ```

- **Connect:** an app already serves MCP at a URL (Blender, Fusion 360, a
  game engine, a dev server). The node shares it through the hub, and the
  app keeps listening on localhost only.

  ```sh
  mcp-mesh-node add --name blender --url http://127.0.0.1:9876/mcp
  ```

  Streamable HTTP and the older HTTP+SSE transport are detected when you
  add it (`--transport http|sse` to choose). Connect entries start lazily
  and the hub lists their cached tools while the app is closed; when nothing
  is listening the node shows **waiting for app** and retries.

## Applying changes

`add`, `remove`, `enable`, `disable` and `edit` change `node.yaml` only. Like
an MCP client that reads its config at start, a running node keeps its MCPs
until you apply the change:

```sh
mcp-mesh-node reload        # or R in the dashboard
```

Reloading restarts all of the node's MCPs, after calls in progress finish
(at most a minute). `status` says when there are changes not applied yet. A
new hub key (`key set`) is applied at once.

## Start at login, or at boot

`start` runs the node until you log out. `install` makes it a user service
(systemd on Linux, launchd on macOS) that starts at every login, capturing
your login `PATH` so commands such as `npx` are found. On a Linux machine
nobody logs in to (a server), use `install --at-boot` so it starts when the
machine boots. `uninstall` turns it off and stops the node, keeping the
config. A node installed with Homebrew runs from
`$(brew --prefix)/opt/mcp-mesh-node`, so the service keeps working after
`brew upgrade`.

## node.yaml

```yaml
hub:
  url: ws://hub.lan:7001/node
  key_file: ~/.config/mcp-mesh/node.key
node:
  name: studio-pc            # default: the hostname
mcps:
  fs:
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "~/code"]
    allow_tools: [read_text_file, list_directory]   # enforced on this machine
  browser:
    command: npx
    args: ["-y", "@playwright/mcp@latest"]
    start: lazy              # start on the first call, stop when idle
    sharing: per-client      # a process for each client key
  blender:
    url: http://127.0.0.1:9876/mcp
    start: lazy
    call_timeout: 15m        # renders take a while
```

Each MCP has either `command` (launch) or `url` (connect):

| Key | Meaning |
| --- | --- |
| `command`, `args`, `env`, `dir` | the command to launch and its environment |
| `url`, `headers` | an MCP endpoint an app already serves, and HTTP headers for it |
| `transport` | URL entries: `http` (Streamable HTTP, default) or `sse` (HTTP+SSE) |
| `start` | `eager` (default for commands) or `lazy` (start on the first call; default for URLs) |
| `idle_timeout` | how long a lazy MCP stays up without calls (default `10m`) |
| `sharing` | `shared` (one process for every client, default) or `per-client` (a process for each client key) |
| `max_clients` | `per-client` only: most clients with a process at once (default `8`) |
| `call_timeout` | longest call on this MCP; default the hub's `--call-timeout` |
| `allow_tools` | the only tools the hub may see and call; omit for all |
| `disabled` | keep the entry without serving it |

Values can use `${env:NAME}`. The node refuses to send its key over `ws://`
to a host that isn't local (loopback, private or Tailscale addresses,
`.local`/`.lan` names); use `wss://`, or set `hub.allow_insecure: true` if
you know the path is private.

## Commands

| Command | What it does |
| --- | --- |
| `mcp-mesh-node` (or `tui`) | the interactive dashboard; works with or without a running node |
| `discover` | list hubs announcing themselves on the local network |
| `init`, `edit`, `config check` | create, edit and validate `node.yaml` |
| `add`, `remove`, `enable`, `disable` | change MCPs in `node.yaml` (applied on `reload`) |
| `start`, `stop`, `reload` | run the node in the background, stop it, apply the saved config |
| `install [--at-boot]`, `uninstall` | start at login (or boot) on or off |
| `status [--json]` | the node, its hub connection, its config and each MCP's state |
| `logs <mcp> [-f]`, `restart <mcp>` | an MCP's stderr; restart one MCP |
| `key set`, `key clear` | replace or remove the hub key |
| `env refresh` | capture the login environment again after changing your shell setup |
| `wrap -- <command>` | share one MCP right away, in the foreground |
| `run` | the node daemon in the foreground (what `start` and the service run) |

Scripts can pass `--no-input` (or `MCPMESH_NO_INPUT=1`) to disable prompts.

## Platforms

Linux and macOS. Windows can run `wrap` in the foreground; the background
node and the service are not available there yet.
