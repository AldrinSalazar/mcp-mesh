# Hub setup

The hub is one binary, `mcp-mesh-hub`. It keeps its state in one SQLite file
and serves three things: the dashboard and its API, the endpoint nodes
connect to, and the MCP endpoint clients connect to.

## Run the hub

The hub ships as a container image, `ghcr.io/aldrinsalazar/mcp-mesh-hub`,
for linux/amd64, linux/arm64 and linux/arm/v7. Tags: `latest`, a release
(`1.0.0`), its minor and major lines (`1.0`, `1`), and `edge` for the newest
build from `master`.

```sh
docker run -d --name mcp-mesh-hub --restart unless-stopped \
  -p 127.0.0.1:7000:7000 -p 7001:7001 -p 7002:7002 \
  -e MCPMESH_PUBLIC_HOST=<hub host name or address> \
  -v mcp-mesh-hub:/data \
  ghcr.io/aldrinsalazar/mcp-mesh-hub:1
docker logs mcp-mesh-hub        # the first admin password
```

Or with Compose, using [deploy/compose.yaml](../deploy/compose.yaml):

```sh
docker compose -f deploy/compose.yaml up -d
```

`MCPMESH_PUBLIC_HOST` is the name nodes and clients use to reach the hub;
the dashboard puts it in setup commands and snippets. The volume on `/data`
holds all of the hub's state. These examples publish the dashboard (7000)
on the Docker host's loopback only; publish it on your network if you sign
in from another machine, but keep it off the internet.

On first start the hub creates the admin user `admin` and prints its
password to the log, once. Set it yourself with `MCPMESH_ADMIN_PASSWORD`
(or `MCPMESH_ADMIN_PASSWORD_FILE`) before the first start if you prefer.
Sign in to the dashboard at `http://<hub>:7000`.

## Ports

| Port | Path | Who connects | Credential |
| --- | --- | --- | --- |
| 7000 | `/`, `/api/v1` | admins in a browser, scripts | username and password (session cookie) |
| 7001 | `/node` | nodes (WebSocket) | node key, bound to one node name |
| 7001 | `/node/pair` | nodes without a key, asking to join (WebSocket) | none; an admin approves each request |
| 7002 | `/mcp` | MCP clients (Streamable HTTP) | client key, or an OAuth token |

`/healthz` (liveness) and `/readyz` (readiness) on port 7000 need no
credential. `/readyz` answers `ready`, or 503 with what is wrong: the
database isn't writable, a database watcher is failing, the audit log lost
calls, or the dashboard's live data is stale. `/api/v1/health` adds counters.

Keep port 7000 on your network; expose 7002 (and 7001 for remote nodes)
where needed. Behind a reverse proxy, `--listen :8080` serves all three on
one address, and `--trusted-proxies <proxy address or CIDR>` makes the hub
believe the proxy's `X-Forwarded-For` and `X-Forwarded-Proto`.

## Keys and admins

Machines use keys; people use passwords. The dashboard manages both under
Keys and Admins; the hub's command line does the same inside its container:

```sh
docker exec mcp-mesh-hub /mcp-mesh-hub key create node studio-pc    # prints the key and the node's setup command
docker exec mcp-mesh-hub /mcp-mesh-hub key create client claude-code
docker exec mcp-mesh-hub /mcp-mesh-hub key list
docker exec mcp-mesh-hub /mcp-mesh-hub key revoke <id|name>
docker exec -i mcp-mesh-hub /mcp-mesh-hub admin create alex --password-stdin < alex.txt   # admin create|passwd|list|delete
```

A key's secret is shown once; the hub stores only its hash. Revoking a key
closes the connections that used it at once and cancels their calls in
progress. The command line shares the running hub's database, which picks
changes up within a second.

### Pairing

A node started without a key asks the hub for one on `/node/pair`. The
request shows in the dashboard's Approvals with the machine's name, host
name, address and a code such as `K7Q-4MX`; the node shows the same code.
Check that they match, so you approve the machine in front of you and not
another one asking at the same time, then approve it, under its name or
another. The hub creates a node key bound to that name and sends it down
the waiting connection; the node saves it and connects. If a node key for
that name exists already, approving replaces it: the old key is revoked
and its node disconnected. Declining tells the node, which stops asking
until it restarts.

Requests live in memory, only as long as the node stays connected: at most
20 wait at once, 3 from one address. Approvals and declines go to the admin
log. `--pairing=false` (`MCPMESH_PAIRING=false`) turns pairing off; nodes
then need a key made under Keys. With `--auth=false` there are no keys and
nothing to pair.

## Who can use what

- **Groups.** A client key sees only the tools its groups grant: a whole
  node, an MCP, or single tools. Ticking an MCP or node grants it "now and
  later" (tools added later included); ticking tools one by one grants just
  those. A key in no group sees nothing. New client keys join the built-in
  **Everything** group unless you choose others.
- **Trust levels**, per node (hub default: Trust).
  - *Trust*: every tool is available at once.
  - *Review changes*: new tools are available; a tool whose definition
    changes waits for your approval (prompt injections often arrive that
    way).
  - *Approve everything*: the node, then each new or changed tool, waits for
    approval.

  Approvals show a diff of the tool's whole definition.
- **Logs.** The audit log records every tool call, prompt fetch and resource
  read (who, what, how long, the outcome; never arguments or results), kept
  30 days. The admin log records sign-ins, key, admin and group changes,
  trust levels, approvals and OAuth authorizations, kept a year.

## TLS

Plain HTTP is accepted only from loopback and private networks (LAN,
Tailscale). Anything else needs TLS: `--tls-cert` and `--tls-key`, or a
reverse proxy with `--trusted-proxies`.

## Apps that sign in with OAuth

claude.ai, the Claude apps and other clients that add MCP servers by URL use
OAuth instead of a pasted key. The hub is its own authorization server: the
app opens a page where an admin signs in, chooses the groups the app gets,
and allows it. The app then holds tokens tied to a client key you can
revoke like any other.

OAuth needs the hub's client URL to be **HTTPS at a host name**, also on your
own network: MCP clients refuse an authorization server at a private IP
address (`https://192.168.1.10:7002` won't work, `https://mesh.home.lan`
will). Set `--public-host` (or `--client-url`) to that name; the hub logs at
start when its client URL can't work for OAuth. Apps must use exactly that
URL.

## Discovery

The hub announces itself on the local network (mDNS, `_mcp-mesh._tcp`), so
`mcp-mesh-node init` and `discover` can list it. Turn it off with
`--mdns=false`; name it with `--name`. Discovery only lists hubs; a node
never connects to one you didn't pick. It doesn't cross Docker bridge
networks or VPNs: on Linux, run the container with `--network host` (in
place of the `-p` options) for discovery, or give nodes the hub's URL.

## Configuration

Every flag also reads an environment variable.

| Flag | Variable | Default | Meaning |
| --- | --- | --- | --- |
| `--data-dir` | `MCPMESH_DATA_DIR` | `/data` in the image | where `hub.db` lives |
| `--admin-listen`, `--node-listen`, `--client-listen` | `MCPMESH_ADMIN_LISTEN`, … | `:7000`, `:7001`, `:7002` | the three listeners |
| `--listen` | `MCPMESH_LISTEN` | | one address for everything (reverse proxies) |
| `--public-host` | `MCPMESH_PUBLIC_HOST` | `localhost` | host name nodes and clients use; shown in setup commands |
| `--node-url`, `--client-url` | `MCPMESH_NODE_URL`, `MCPMESH_CLIENT_URL` | from `--public-host` | public URLs when a proxy changes them |
| `--tls-cert`, `--tls-key` | `MCPMESH_TLS_CERT`, `MCPMESH_TLS_KEY` | | serve TLS |
| `--trusted-proxies` | `MCPMESH_TRUSTED_PROXIES` | | proxies whose forwarding headers are believed |
| `--allow-insecure` | `MCPMESH_ALLOW_INSECURE` | `false` | accept plain HTTP from public addresses |
| `--auth` | `MCPMESH_AUTH` | `true` | `false` lets anyone who reaches the hub use it (local testing) |
| `--pairing` | `MCPMESH_PAIRING` | `true` | let nodes without a key ask to join; an admin approves each ([Pairing](#pairing)) |
| `--admin-user`, `--admin-password` | `MCPMESH_ADMIN_USER`, `MCPMESH_ADMIN_PASSWORD` (`_FILE`) | `admin`, generated | the first admin, first start only |
| `--call-timeout` | `MCPMESH_CALL_TIMEOUT` | `2m` | longest tool call (MCPs can set their own) |
| `--mdns`, `--name` | `MCPMESH_MDNS`, `MCPMESH_NAME` | `true`, hostname | LAN announcement |
| `--no-update-check` | `MCPMESH_NO_UPDATE_CHECK` | `false` | don't ask GitHub for newer releases ([Updates](#updates)) |
| `--log-level`, `--log-format` | `MCPMESH_LOG_LEVEL`, `MCPMESH_LOG_FORMAT` | `info`, `text` | logging |

## Updates

Once a day the hub asks GitHub's releases API whether a newer mcp-mesh
release is out: one plain GET to `api.github.com`, which sends nothing
about the hub. The answer is kept in the database, so a restart doesn't
ask again; offline or rate limited, the hub keeps the last answer and
says nothing. Builds without a release version (`dev`) never ask.

When a newer release is out, the dashboard shows a banner with the release
notes and the upgrade command, until an admin dismisses it for that
release. To upgrade a hub run with Compose:

```sh
docker compose pull && docker compose up -d
```

The Nodes page marks nodes that run an older version than the latest
release (or than the hub itself, before the hub knows the latest), and
Overview counts them under Needs attention. Nothing is upgraded on its own.

For air-gapped hubs, or to keep the hub from contacting GitHub, use
`--no-update-check` (`MCPMESH_NO_UPDATE_CHECK=true`). Nodes are then
compared with the hub's own version.

## Backup

All of the hub's state is in its data directory, the volume on `/data`.
Stop the hub, copy the volume's contents (`hub.db` and any `hub.db-wal` and
`hub.db-shm` next to it), and start it again:

```sh
docker stop mcp-mesh-hub
docker run --rm -v mcp-mesh-hub:/data -v "$PWD":/backup busybox tar -czf /backup/hub-backup.tgz -C /data .
docker start mcp-mesh-hub
```

To restore, put the copy into the volume before starting a hub, on the same
or another machine; keys, groups, approvals and admins come with it.

## Limits

The hub is a single process: tens of nodes and hundreds of MCPs, for one
person or a small team. See [Architecture](architecture.md#limits) for the
bounds it enforces.
