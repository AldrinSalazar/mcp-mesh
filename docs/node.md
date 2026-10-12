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
(also if it runs as a [service](#start-at-login-or-at-boot)). The node tells
you when there is something to upgrade: see [Updates](#updates).

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

A new machine joins the mesh by pairing: it asks the hub to join, and an
admin approves it in the hub's dashboard. Nobody creates, copies or pastes
a key.

1. **Point the node at the hub and start it.**

   ```sh
   mcp-mesh-node init --hub ws://<hub>:7001/node
   mcp-mesh-node start
   ```

   `init` writes `~/.config/mcp-mesh/node.yaml` pointing at the hub (at a
   terminal, without `--hub`, it lists the hubs it finds on your network).
   The machine's name in the mesh defaults to its host name; `--name`
   proposes another. The node has no key yet, so once started it asks the
   hub to pair, and `start` prints the code the request shows:

   ```
   No hub key, so the node asked the hub to pair: approve this machine in the hub's dashboard (Approvals); check it shows code K7Q-4MX
   ```

   `mcp-mesh-node status` shows the code too, as *waiting for approval*.

2. **Approve it in the hub's dashboard.** Under Approvals, *Machines asking
   to join* lists the request with the machine's name, host name, system,
   address and code; the Overview lists it under *Needs attention*. Click
   **Review**, check that the code matches the one on the machine, and
   approve. You can give it another name here; its tools are named after
   it (`<name>__<mcp>__<tool>`).

3. **Done.** The hub creates a node key bound to that name and sends it to
   the waiting node, which saves it in a file only you can read
   (`node.key`, next to `node.yaml`), writes the name to `node.yaml`, and
   connects. From then on it uses that key, through restarts too; it never
   pairs again unless the key is removed (`key clear`).

Then [add MCPs](#launch-or-connect).

Running `mcp-mesh-node` alone opens an interactive dashboard that does all
of this with one key: **p** (pair with a hub) lists the hubs it finds on the
network (or takes a URL), asks for this machine's name, writes the config,
starts the node and shows the code large until an admin approves it. On a
machine that already has a key, **p** asks first: pairing again removes it.
A key made on the hub is under **?** → *Other ways to join* (**K**).

The code is what makes approving safe: anyone who can reach the hub's node
port can ask to join, under any name, but only the machine you set up shows
the code you see in the dashboard. Don't approve a request whose code you
haven't checked.

### When pairing doesn't go through

| What you see | Why, and what to do |
| --- | --- |
| *pairing declined* | An admin declined the request. The node doesn't ask again until it restarts: `mcp-mesh-node stop && mcp-mesh-node start`, or **r** on the dashboard's pairing screen. |
| *Name in use* on the request | A node key is already bound to that name, perhaps for this same machine before a reinstall. Approving replaces that key, which disconnects the node using it, or give this machine another name. |
| *pairing is turned off on this hub* | The hub runs with `--pairing=false`. Use a [key made on the hub](#with-a-key-made-on-the-hub). |
| *the hub doesn't accept pairing requests* | The hub is older than pairing. Update it, or use a key made on the hub. |
| *ws:// would send the node key unencrypted* | The hub isn't on a local network and its URL is `ws://`. Use its `wss://` URL, or set `hub.allow_insecure: true` if the path is private. |
| *too many machines are waiting for approval*, or *too many pairing requests from this address* | The hub holds at most 20 requests at once, 3 per address. Approve or decline the ones waiting; the node keeps asking. |
| The request disappeared | The node stopped or lost its connection. It asks again, with a new code, when it reconnects. |

### With a key made on the hub

To set up a machine with nobody at the dashboard (from a script, say),
create a key named after it instead, in the dashboard (Keys → Create key →
Node key) or with the hub's
[`key create node <machine>`](hub.md#keys-and-admins). It shows a one-line
setup command; run it on that machine:

```sh
mcp-mesh-node init --hub ws://<hub>:7001/node --name <machine> --key <node-key>
```

A node key only works for the name it was created for, so `--name` names
the machine to match. `mcp-mesh-node key set` replaces the key on a
machine that is already set up.

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

At the top level, `log_level` is `debug`, `info` (default), `warn` or
`error`.

Values can use `${env:NAME}`. The node refuses to send its key over `ws://`
to a host that isn't local (loopback, private or Tailscale addresses,
`.local`/`.lan` names), and for the same reason won't pair over it; use
`wss://`, or set `hub.allow_insecure: true` if you know the path is
private.

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

The dashboard takes the mouse too: click an MCP to select it, a detail tab
to switch to it, any shortcut in the bottom bar to run it, and a hub or a
form field to choose it (click a chosen item again to take it); the wheel
scrolls. Destructive actions still ask first. While the mouse is on, most
terminals select text with Shift+drag (Option+drag in iTerm2); `m`
turns it off and on, and `MCPMESH_NO_MOUSE=1` starts with it off.

## Updates

Once a day the running node asks GitHub's releases API whether a newer
mcp-mesh release is out: one plain GET to `api.github.com`, which sends
nothing about the machine. It keeps the answer in its state directory
(`update.json`), so restarts don't ask again; offline or rate limited, it
keeps the last answer and says nothing. Builds without a release version
(`dev`) never ask, and nothing is upgraded on its own.

When there is something to do, the dashboard's title bar shows
`↑ <version> available`, and the dashboard, `mcp-mesh-node status` and
the node's log say how to upgrade this install (`brew upgrade
mcp-mesh-node`, or the download for this machine).
`mcp-mesh-node --version` adds `(latest: <version>)`. They also say when:

- the hub runs a newer version than the node (the hub sends its version
  when the node connects; the hub's dashboard flags such nodes too);
- the running node is older than the installed `mcp-mesh-node`: it was
  upgraded, and keeps the old binary until it restarts
  (`mcp-mesh-node stop && mcp-mesh-node start`, or **S** then **s** in the
  dashboard).

A hub that no longer accepts this node's protocol turns it away, and the
node stops with a message that says to upgrade.

A node that can't reach GitHub (offline, or GitHub is down) tries again
an hour later and logs the failure only at debug level; nothing else
changes.

## Platforms

Linux and macOS. Windows can run `wrap` in the foreground; the background
node and the service are not available there yet.
