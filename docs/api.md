# JSON API (`/api/v1`)

Served on the hub's admin listener next to the dashboard. The dashboard SPA uses nothing else.

## Conventions

- All responses are JSON with `Cache-Control: no-store`. Errors are `{"error":"message"}`.
- Times are RFC 3339 UTC strings. Empty lists are `[]`, never `null`. Optional fields marked `?` are omitted when empty.
- Unknown `/api/v1/...` paths answer 404; a wrong method answers 405 with an `Allow` header.
- Request bodies must be `Content-Type: application/json`, at most 64 KiB, with no unknown fields. Violations answer 415 (content type), 413 (size) or 400 (anything else).

### Authentication

- **Auth off** (`info.auth` is `false`): everything is open, no cookie and no CSRF token. `/keys` and `/admins` answer 404 `{"error":"auth is off"}`.
- **Auth on**: every endpoint except `GET /info` and `GET|POST|DELETE /session` needs the session cookie (set by sign-in) and answers 401 without it.
- **CSRF**: every state-changing request (not GET/HEAD) made with a session must send the session's token in `X-CSRF-Token` (the `csrf` field of the session response). Missing or wrong: 403 `{"error":"missing or wrong CSRF token"}`. Sign-in is exempt, since there is no session yet.
- **Origin check**: every state-changing request, sign-in included, is rejected with 403 if it has an `Origin` header whose host differs from the request's `Host`. Requests without `Origin` pass.

## Endpoints

### `GET /info` (always open)

```json
{"version":"1.0.0","auth":true,"node_url":"wss://hub.example/node","client_url":"https://hub.example/mcp","discovery_name":"home-hub","oauth":true,"started_at":"2026-10-12T09:00:00Z"}
```

`started_at` is when the hub started: for a minute after, the dashboard shows offline nodes as reconnecting. `oauth` is true when clients can connect with OAuth instead of a pasted key (auth on): the hub is its own authorization server, discovered from `client_url`.

### Session

`GET /session` returns the current session: 200, or 401 when not signed in. With auth off it returns 200 `{"auth":false}`.

```json
{"username":"alice","csrf":"b0f1...","expires":"2026-10-17T10:00:00Z"}
```

`POST /session` signs in. Body `{"username":"alice","password":"..."}`. 200 with the same body as GET (and the cookie); 401 `{"error":"wrong username or password"}`; 429 with a `Retry-After` header (seconds) while rate limited.

`DELETE /session` signs out. Always 204, also when not signed in.

### `GET /nodes`

```json
[{"name":"laptop","node_version":"1.0.0","os":"linux","remote_addr":"10.0.0.5:51234","key_id":"k_1a2b3c4d",
  "connected_at":"2026-10-10T08:00:00Z","online":true,
  "mcps":[{"name":"fs","transport":"stdio","sharing":"shared","start":"lazy","state":"ready","error":"",
           "tools":4,"in_flight":0,"queued":0,"server":"filesystem 1.0","problems":[]}]},
 {"name":"old-pc","node_version":"","os":"","remote_addr":"","key_id":"","connected_at":"2026-10-09T08:00:00Z",
  "online":false,"disconnected_at":"2026-10-09T09:00:00Z","mcps":[]}]
```

`state` is one of `opening`, `ready`, `idle`, `failed`, `closed`. Each MCP also has `prompts`, `resources` (resources and templates), `call_timeout_ms` (its own call timeout from node.yaml; `0` means the hub default) and `clients`: for `sharing: "per-client"` MCPs, how many clients have a process of their own right now. `problems` is an array of strings (`[]` when none): definitions the hub dropped from the MCP's last listing (invalid or oversized tools, for instance), and why. Offline nodes stay listed: the hub remembers every node that has connected (across restarts too), and lists one that isn't connected with `online: false`, `disconnected_at` as when it was last seen, and its MCPs as last listed (`state: "closed"`).

Each node also has `"trust":{"level":"trust","explicit":false,"approved":true}` and `"pending_tools":0`. `level` is `trust` (every tool visible), `review` (new tools visible, a changed tool waits for approval) or `approve` (the node waits for approval, then every new or changed tool does). `explicit` is false when the node follows the hub default (`GET /settings`). `approved` matters for `approve`: until then none of the node's tools are visible. `pending_tools` counts its tools waiting for approval. `outdated` is true when the node runs an older version than the latest release, or, before the hub knows one, than the hub (see `GET /update`).

`DELETE /nodes/{name}` forgets an offline node: it is no longer listed until it connects again. Its key, trust level and approvals stay. 204; 404 for a node the hub doesn't know; 409 while it is connected.

`PUT /nodes/{name}/trust` with `{"level":"review"}` sets a node's level (also for a node that isn't connected, to prepare it), or `{"level":null}` to follow the hub default again. 200 with the new `trust` object; 400 for an unknown level.

`POST /nodes/{name}/approve` approves an `approve`-level node and pins exactly the tools listed in the body: `{"tools":[{"mcp":"fs","tool":"read_file","hash":"…"}]}`, where `hash` is the one from `GET /approvals` that the admin reviewed. The body may be empty: the node is approved and none of its tools (they stay `new`). 200 with the `trust` object; 400 for a tool without `mcp`, `tool` or `hash`; 409 when a listed tool changed or is gone since it was reviewed (same body as for `POST /approvals`), in which case nothing is approved.

### `GET /tools`

```json
[{"name":"laptop__fs__read_file","node":"laptop","mcp":"fs","original_name":"read_file","description":"Read a file","input_schema":{"type":"object"},"approval":"approved","alias":false,"generated_name":"laptop__fs__read_file","description_override":""}]
```

Every tool in the catalog is listed, hidden ones too. `approval` is `approved`, `new` (never approved), `changed` (differs from what was approved) or `node_pending` (its node waits for approval). Only `approved` tools reach clients.

`name` is the name clients see: the tool's alias when `alias` is true, otherwise the generated one. `generated_name` is always `<node>__<mcp>__<tool>` (sanitized). `description` stays the MCP's own; `description_override` is what models see instead (empty when none).

### Tool overrides

Admins can give a tool an alias (its exposed name) and a description override. Overrides are kept even while the tool is offline and apply when it appears. Trust approvals (which hash the upstream definition) and group grants (which use original tool names) are not affected.

`GET /overrides` lists every override, also for tools that aren't connected:

```json
[{"node":"workstation","mcp":"blender","tool":"render_scene","alias":"render","description":"","updated":"2026-10-11T10:00:00Z","updated_by":"admin"}]
```

`PUT /overrides/{node}/{mcp}/{tool}` (`tool` is the original name) with `{"alias":"render","description":""}` sets it and answers 200 with the entry. `alias` is empty or 1 to 64 characters from `[A-Za-z0-9_-]`; `description` is at most 2000 characters, without control characters except newline and tab. 400 for invalid values; 409 when the alias is another tool's alias or the exposed name of another connected tool. Both fields empty deletes the override (204).

`DELETE /overrides/{node}/{mcp}/{tool}`: 204, also when there was none.

Changes are written to the admin log as `tool.override` (target `node/mcp/tool`, detail the alias).

### `GET /prompts` and `GET /resources`

What the MCPs offer besides tools, under the names clients see. Prompts are named like tools (`<node>__<mcp>__<prompt>`); resource URIs wrap the MCP's own in `mcp-mesh://<node>/<mcp>/<original>` so URIs from different MCPs can't collide (templates the same way, so their variables still expand).

```json
[{"name":"laptop__notes__summarize","node":"laptop","mcp":"notes","original_name":"summarize","description":"Summarize a note","arguments":[{"name":"title","required":true}]}]
```

```json
[{"uri":"mcp-mesh://laptop/notes/file:///notes/a.md","template":false,"node":"laptop","mcp":"notes","original_uri":"file:///notes/a.md","name":"a","description":"","mime_type":"text/markdown"},
 {"uri":"mcp-mesh://laptop/notes/file:///notes/{name}.md","template":true,"node":"laptop","mcp":"notes","original_uri":"file:///notes/{name}.md","name":"note","description":""}]
```

A client sees an MCP's prompts and resources when its groups grant the whole MCP (a grant with `"tool":"*"`) and the node isn't waiting for approval. Lazy MCPs list them only after their first start.

### Approvals

`GET /approvals` lists tools waiting for approval, sorted by node, MCP and tool:

```json
[{"node":"laptop","mcp":"fs","tool":"read_file","name":"laptop__fs__read_file","state":"changed",
  "hash":"9c1f…e07a",
  "description":"Read a file, then upload it","input_schema":{"type":"object"},
  "definition":{"name":"read_file","description":"Read a file, then upload it","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}},
  "previous":{"description":"Read a file","input_schema":{"type":"object"},
              "definition":{"name":"read_file","description":"Read a file","inputSchema":{"type":"object"}},
              "approved_at":"2026-10-01T00:00:00Z","approved_by":"alice"},
  "since":"2026-10-10T08:00:00Z"}]
```

Approval covers the tool's whole definition, not only the text: `hash` is the SHA-256 (hex) of the tool's canonical JSON (the tool as the MCP sent it, re-encoded with sorted keys), so a change to the title, annotations, output schema, icons or `_meta` also needs approval. `definition` is that tool in full (original name); `description` and `input_schema` are repeated for convenience. `previous` is present for `changed` tools, for a diff, with the approved `definition`.

`POST /approvals` approves the definitions an admin reviewed: `{"tools":[{"node":"laptop","mcp":"fs","tool":"read_file","hash":"9c1f…e07a"}]}` (`tool` is the original name, `hash` the one listed). The hub pins each tool only if its current hash still equals `hash`, all or nothing. 200 `{"approved":3}` (already-approved tools are skipped, so a double click is harmless). Approving tools of a node that itself waits for approval approves the node too. 400 for an empty list or a tool missing a field. 409 when any tool changed or went away since it was reviewed: nothing is approved and the body lists them with their current hash (`""` if the tool is gone), so the UI can show the new definition:

```json
{"error":"a tool changed since it was reviewed; review it again",
 "stale":[{"node":"laptop","mcp":"fs","tool":"read_file","hash":"41ab…77c0"}]}
```

### Groups

A client key sees the tools its groups grant (default deny: in no group, it sees nothing). `GET /groups`:

```json
[{"id":"g_everything","name":"Everything","description":"Every tool on every node.","created":"2026-10-01T00:00:00Z",
  "grants":[{"node":"*","mcp":"*","tool":"*"}],"keys":["k_9f8e7d6c"]}]
```

A grant allows one node / MCP / tool (the tool's original name); `"*"` matches anything at its level, and a `"*"` level needs `"*"` below it (`{"node":"laptop","mcp":"*","tool":"*"}` is every tool of laptop). `keys` are the IDs of the client keys in the group.

`POST /groups` with `{"name":"Read only","description":"","grants":[...]}`: 201 with the group; 400 for an invalid name or grant; 409 if the name exists (case-insensitive). `PUT /groups/{id}` with the same body replaces name, description and grants: 200. `DELETE /groups/{id}`: 204 (its keys lose what it granted). Unknown ID: 404.

### `GET /calls`

The most recent calls held in memory, newest first.

```json
[{"time":"2026-10-10T08:01:02Z","client_id":"k_9f8e7d6c","client":"claude","kind":"tool","tool":"laptop__fs__read_file",
  "node":"laptop","mcp":"fs","duration_ms":120,"outcome":"ok","error":""}]
```

`kind` is `tool` (a tool call), `prompt` (a prompt fetch; `tool` is the prompt's name) or `resource` (a resource read; `tool` is the URI the client asked for). `outcome` is `ok`, `tool_error` or `error`. Arguments and results are never stored. `error` is the start (at most 500 bytes) of the error: for `tool_error`, the tool's own error text, which can contain whatever the tool put there.

### `GET /stats`

Tool calls in the last hour, counted from the audit log (from the in-memory recent calls when there is no audit log). `errors_last_hour` counts the outcomes `tool_error` and `error`.

```json
{"calls_last_hour":128,"errors_last_hour":3}
```

### `GET /health`

Readiness and metrics. `ready` is false when `problems` is not empty. Each problem is one line saying what is wrong:
- the database is not writable;
- a database watcher is failing, so changes made by the key CLI may be missed;
- keys can't be reloaded, so revocations by another process don't reach live connections (counted in `watchers_failing` too);
- the audit log lost calls in the last minute;
- the events snapshot is over 10 s old, or there has been no complete one 10 s after the hub started.

```json
{"ready":true,"problems":[],
 "metrics":{"nodes_online":2,"mcps_ready":5,"calls_in_flight":1,"calls_queued":0,"calls_waiting_start":0,"client_processes":2,
            "mcp_opens":14,"mcp_open_failures":1,"mcp_session_failures":0,"calls_refused_busy":0,"nodes_refused":0,
            "client_clients":3,"client_sessions":2,"client_sessions_refused":0,"client_requests":1,"client_requests_refused":0,
            "audit_calls_lost":0,"watchers_failing":0,"events_snapshot_age_ms":412}}
```

Current values: `nodes_online`, `mcps_ready`, `calls_in_flight`, `calls_queued`, `calls_waiting_start` (calls waiting for a lazy MCP to start or a per-client process to open), `client_processes` (per-client MCP processes), `client_clients` (client keys with a view), `client_sessions` (open session-based sessions), `client_requests` (open stateless requests and streams), `watchers_failing`, `events_snapshot_age_ms`.

Totals since the hub started:
- `mcp_opens` and `mcp_open_failures`: sessions opened to MCPs, and opens that never became ready.
- `mcp_session_failures`: ready sessions that ended with an error.
- `calls_refused_busy`: calls refused because the MCP's queue was full.
- `nodes_refused`: refused node connections.
- `client_sessions_refused`: new sessions over the per-key cap.
- `client_requests_refused`: stateless requests and streams over the per-key cap.
- `audit_calls_lost`: calls the audit log never wrote.

The admin listener also serves `GET /readyz` without a session (plain HTTP only from local and private addresses, like the API). It answers 200 `ready`, or 503 with the problems one per line, and is meant for probes. `GET /healthz` stays a liveness check that always answers `ok`.

### `GET /audit`

The persistent call log (404 when it is not available). Query parameters, all optional: `since` (inclusive) and `until` (exclusive) as RFC 3339 times, `client_id`, `node`, `mcp`, `tool`, `outcome` (exact matches), `before_id`, `limit` (default 100, at most 1000). Bad values answer 400.

```json
{"entries":[{"id":4211,"time":"...","client_id":"k_9f8e7d6c","client":"claude","kind":"tool","tool":"...","node":"laptop","mcp":"fs",
             "duration_ms":120,"outcome":"ok","error":""}],
 "next_before_id":4150}
```

Entries are newest first. To get the next page, repeat the request with `before_id=<next_before_id>`. `next_before_id` is `0` when the page has fewer entries than the limit (no more pages).

### Keys

`GET /keys` (never includes the hash):

```json
[{"id":"k_1a2b3c4d","kind":"node","name":"laptop","created":"2026-10-01T00:00:00Z","last_used":"2026-10-10T08:00:00Z","last_ip":"10.0.0.5"},
 {"id":"k_9f8e7d6c","kind":"client","name":"claude-ai","created":"2026-10-02T00:00:00Z","groups":["g_everything"],
  "oauth_client":{"id":"oc_5d1c…","name":"Claude"}}]
```

Client keys have `groups` (IDs). `oauth_client` marks the key behind an OAuth authorization (an app that connected through OAuth): it has no secret of its own and its tokens die with it.

`POST /keys` with `{"kind":"node"|"client","name":"laptop"}` answers 201. The secret is shown only here. 400 on a bad kind or name (the message is in `error`). Optional fields: for a client key `"groups":["g_…"]` (default: the `Everything` group if it exists, else none); for a node key `"trust":"trust"|"review"|"approve"` (sets the node's level as with `PUT /nodes/{name}/trust`; default: the hub default).

```json
{"key":{"id":"k_1a2b3c4d","kind":"node","name":"laptop","created":"2026-10-10T08:00:00Z"},
 "secret":"mm_...",
 "setup":{"hub_url":"wss://hub.example/node",
          "init_command":"mcp-mesh-node init --hub wss://hub.example/node --name laptop --key mm_...",
          "command":"mcp-mesh-node key set mm_..."}}
```

`init_command` sets up a new machine as that node: the hub URL, the node's name (the key only works for it) and the key. `command` only replaces the key on a machine that is already set up.

For a client key, `setup` is:

```json
{"mcp_url":"https://hub.example/mcp","header":"Authorization: Bearer mm_...",
 "claude_code":"claude mcp add --transport http mcp-mesh https://hub.example/mcp --header \"Authorization: Bearer mm_...\""}
```

`DELETE /keys/{id}` revokes the key and closes its connections. 200 `{"key":{...}}`; 404 when no key has exactly that ID (names are not accepted).

`PUT /keys/{id}/groups` with `{"groups":["g_…"]}` replaces a client key's groups (`[]` leaves it seeing nothing): 200 `{"key":{...}}`; 400 for a node key or an unknown group.

`GET /keys/{id}/tools` is the key's effective tool list: what that client sees right now (its groups' grants and approvals), in the `GET /tools` format. 400 for a node key.

### Pairing requests

Nodes started without a key that ask to join (see [Pairing](hub.md#pairing)). Auth on only; 404 with auth off.

`GET /pairings`, oldest first (`[]` with pairing turned off):

```json
[{"id":"p_3f9a1c2e7b4d5a60","node":"render-box","hostname":"render-box.lan","os":"linux","node_version":"1.0.0",
  "ip":"192.168.1.52","code":"K7Q-4MX","requested":"2026-10-10T08:00:00Z","name_taken":false}]
```

`node` is the name the machine asked for and `code` what it shows on its screen. `name_taken` is true when a node key is bound to that name already.

`POST /pairings/{id}/approve` creates a node key and sends it to the waiting machine, which saves it and connects: 200 `{"key":{...}}` (the secret is never shown). An optional body `{"name":"studio","replace":true}` names the node otherwise than it asked; `replace` is needed when a node key is bound to the name, and revokes that key (its node disconnects) first, else 409. 400 for a bad name; 404 when the request is gone (approved or declined already, or the machine disconnected); 410 when the machine disconnected while the key was being sent, in which case the key is revoked again. 404 `pairing is turned off on this hub` with `--pairing=false`.

`DELETE /pairings/{id}` declines the request: 204. The machine is told and doesn't ask again until it restarts. 404 when it is gone.

### Settings

`GET /settings`:

```json
{"trust_default":"trust","oauth":{"enabled":true,"issuer":"https://hub.example:7002","resource":"https://hub.example:7002/mcp"},"pairing":true}
```

`pairing` says whether machines without a key may ask to join (`--pairing`).

`PUT /settings` with `{"trust_default":"review"}` changes the level of nodes without their own: 200 with the settings.

### `GET /admin-log`

Who changed what: sign-ins (and failed attempts), keys, admin users, groups, trust levels and approvals, OAuth authorizations, pairing requests. Query parameters `before_id` and `limit` (default 100, at most 1000) page like `/audit`.

```json
{"entries":[{"id":12,"time":"2026-10-10T08:00:00Z","actor":"alice","action":"key.create","target":"k_1a2b3c4d","detail":"node laptop","ip":"10.0.0.5"}],
 "next_before_id":0}
```

`actor` is the admin's username, `cli` for the hub's command line, or empty for a failed sign-in. Actions: `sign_in`, `sign_in_failed`, `sign_out`, `key.create`, `key.revoke`, `key.groups`, `admin.create`, `admin.delete`, `admin.password`, `group.create`, `group.update`, `group.delete`, `trust.level`, `node.approve`, `tool.approve`, `oauth.authorize`, `oauth.denied`, `pairing.approve` (target: the key created; detail: `node <name> from <address>`), `pairing.deny` (target: the name the machine asked for; detail: its address). Replacing a key while approving a pairing also records its `key.revoke`. `oauth.authorize` is recorded when the app redeems its code (that is when the key is made or reused), with the approving admin as actor and the key as target.

### Admins

`GET /admins`:

```json
[{"username":"alice","created":"2026-10-01T00:00:00Z","password_changed":"2026-10-01T00:00:00Z","last_login":"2026-10-10T08:00:00Z"}]
```

`POST /admins` with `{"username":"bob","password":"..."}`: 201 with the user; 409 if the username exists; 400 for a weak password (under 12 characters) or an invalid username.

`PUT /admins/{username}/password` with `{"password":"new","current_password":"old"}`: 204. When `{username}` is the signed-in admin, `current_password` is required and checked (403 if missing or wrong). Changing another admin's password needs no current password. 400 for a weak password, 404 for an unknown user. Setting a password ends all of that user's sessions, so changing your own signs you out: the next request is 401 and the SPA should show sign-in.

`DELETE /admins/{username}`: 204; 404 for an unknown user; 409 for the last admin.

### `GET /update`

Whether a newer mcp-mesh release is out, from the hub's daily check.

```json
{"enabled":true,"version":"v1.2.0","latest":"v1.3.0","update_available":true,
 "release_url":"https://github.com/AldrinSalazar/mcp-mesh/releases/tag/v1.3.0","checked_at":"2026-10-12T09:00:00Z"}
```

`enabled` is false for a build without a release version (`dev`), which never checks; `latest` is empty until a check succeeds, and `checked_at` is then omitted.

### `GET /events` (Server-Sent Events)

`Content-Type: text/event-stream`. On connect, one event per type carrying the current snapshot, then one more whenever that snapshot changes. `data` is a single line holding the same JSON the matching GET returns.

```
event: nodes
data: [{"name":"laptop",...}]

event: calls
data: [...]
```

Types: `nodes`, `tools`, `prompts`, `resources`, `calls`, `stats`, `approvals`, `settings`, `update`, `overrides`, and when auth is on `keys`, `admins`, `groups` and `pairings`. The hub checks for changes about once a second (`calls` therefore shows the most recent ~N calls, as `GET /calls` does). A comment line `: ping` is sent every 25 seconds to keep the connection alive.

A client that falls too far behind is disconnected rather than slowing the others; `EventSource` reconnects by itself and receives a full snapshot again. The same happens when the hub restarts. The stream needs the session cookie like any other endpoint; if the session ends, reconnection gets 401 and the client should show sign-in.
