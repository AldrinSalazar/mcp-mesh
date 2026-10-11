-- The hub's database. Times are Unix milliseconds (UTC). Secrets, codes and
-- tokens are stored as hex SHA-256 hashes, never in clear.

-- Machine credentials: node keys (bound to a node name) and client keys.
CREATE TABLE keys (
    id           TEXT PRIMARY KEY,                -- "k_" + 8 hex chars
    kind         TEXT NOT NULL CHECK (kind IN ('node', 'client')),
    name         TEXT NOT NULL,                   -- node name for node keys, a label otherwise
    hash         TEXT NOT NULL UNIQUE,            -- hex SHA-256 of the secret
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    last_ip      TEXT,
    oauth_client TEXT REFERENCES oauth_clients (id) ON DELETE SET NULL -- set for a key behind an OAuth authorization
);

-- Tool calls, prompt fetches and resource reads (kept 30 days).
CREATE TABLE audit (
    id          INTEGER PRIMARY KEY,
    time        INTEGER NOT NULL,
    client_id   TEXT NOT NULL,                    -- key ID, or "anonymous" with auth off
    client      TEXT NOT NULL,                    -- key name at the time of the call
    kind        TEXT NOT NULL DEFAULT 'tool' CHECK (kind IN ('tool', 'prompt', 'resource')),
    tool        TEXT NOT NULL,                    -- exposed tool or prompt name, or the resource URI
    node        TEXT NOT NULL,
    mcp         TEXT NOT NULL,
    duration_ms INTEGER NOT NULL,
    outcome     TEXT NOT NULL,                    -- ok, tool_error, error
    error       TEXT NOT NULL DEFAULT '',
    args        TEXT                              -- JSON, reserved for argument logging
);
CREATE INDEX audit_time ON audit (time);
CREATE INDEX audit_client_time ON audit (client_id, time);

-- People sign in to the dashboard with a username and password.
CREATE TABLE admin_users (
    id                  TEXT PRIMARY KEY,           -- "u_" + 8 hex chars
    username            TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash       TEXT NOT NULL,              -- argon2id, PHC string format
    created_at          INTEGER NOT NULL,
    password_changed_at INTEGER NOT NULL,
    last_login_at       INTEGER
);

CREATE TABLE sessions (
    hash         TEXT PRIMARY KEY,                  -- hex SHA-256 of the cookie value
    user_id      TEXT NOT NULL REFERENCES admin_users (id) ON DELETE CASCADE,
    csrf         TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    ip           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user ON sessions (user_id);
CREATE INDEX sessions_expires ON sessions (expires_at);

-- A client key sees the union of its groups' grants. Default deny: a key in
-- no group sees nothing.
CREATE TABLE groups (
    id          TEXT PRIMARY KEY,                 -- "g_" + 8 hex chars
    name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
    description TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);

-- One row grants one node / MCP / tool. "*" matches anything at that level,
-- and a "*" level makes the levels below it "*" too.
CREATE TABLE group_grants (
    group_id TEXT NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    node     TEXT NOT NULL,
    mcp      TEXT NOT NULL,
    tool     TEXT NOT NULL,                       -- original tool name
    PRIMARY KEY (group_id, node, mcp, tool)
);

CREATE TABLE key_groups (
    key_id   TEXT NOT NULL REFERENCES keys (id) ON DELETE CASCADE,
    group_id TEXT NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    PRIMARY KEY (key_id, group_id)
);
CREATE INDEX key_groups_group ON key_groups (group_id);

-- New client keys join this group unless told otherwise; it can be edited
-- or deleted like any other.
INSERT INTO groups (id, name, description, created_at)
VALUES ('g_everything', 'Everything', 'Every tool on every node.', CAST(strftime('%s', 'now') AS INTEGER) * 1000);
INSERT INTO group_grants (group_id, node, mcp, tool) VALUES ('g_everything', '*', '*', '*');

-- Per-node trust. A node without a row uses the hub default (settings
-- "trust.default", itself "trust" when unset).
--   trust   every tool is visible; nothing to approve
--   review  new tools are visible at once; a changed tool waits for approval
--   approve the node waits for approval, then every new or changed tool does
CREATE TABLE nodes (
    name        TEXT PRIMARY KEY,
    trust       TEXT NOT NULL CHECK (trust IN ('trust', 'review', 'approve')),
    approved_at INTEGER,                          -- node approval ("approve" only)
    approved_by TEXT NOT NULL DEFAULT ''
);

-- What was approved for each tool: a hash of its whole definition (every
-- field the MCP sent, as canonical JSON), kept so a change can be shown as a
-- diff. A tool whose current hash differs from its approved hash is pending.
CREATE TABLE tool_pins (
    node          TEXT NOT NULL,
    mcp           TEXT NOT NULL,
    tool          TEXT NOT NULL,                  -- original tool name
    hash          TEXT NOT NULL,                  -- hex SHA-256, see trust.Hash
    description   TEXT NOT NULL,
    input_schema  TEXT NOT NULL,                  -- JSON
    approved_at   INTEGER NOT NULL,
    approved_by   TEXT NOT NULL DEFAULT '',       -- admin username, or "auto"
    definition    TEXT NOT NULL DEFAULT '{}',     -- canonical tool JSON
    PRIMARY KEY (node, mcp, tool)
);

-- Tool aliases and description overrides, set by admins. A row may name a
-- tool that isn't connected right now; it applies when it appears.
CREATE TABLE tool_overrides (
    node        TEXT NOT NULL,
    mcp         TEXT NOT NULL,
    tool        TEXT NOT NULL,                    -- original tool name
    alias       TEXT NOT NULL DEFAULT '',         -- exposed name; '' keeps the generated one
    description TEXT NOT NULL DEFAULT '',         -- '' keeps the MCP's
    updated_at  INTEGER NOT NULL,
    updated_by  TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (node, mcp, tool)
);
CREATE UNIQUE INDEX tool_overrides_alias ON tool_overrides (alias) WHERE alias <> '';

-- Hub settings, and the URLs the running hub advertises (for its CLI).
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- OAuth: the hub is the authorization server for /mcp. A client is
-- registered dynamically (RFC 7591) or identified by its metadata document
-- URL; an approved authorization becomes a client key (kind 'client',
-- oauth_client set, no usable secret) so groups, revocation, last-used and
-- the audit log work the same as for pasted keys.
CREATE TABLE oauth_clients (
    id            TEXT PRIMARY KEY,               -- "oc_" + random, or the metadata document URL
    name          TEXT NOT NULL,
    redirect_uris TEXT NOT NULL,                  -- JSON array
    metadata      TEXT NOT NULL,                  -- JSON as registered or fetched
    created_at    INTEGER NOT NULL
);

-- A code carries what the admin chose: an existing key of the same OAuth
-- client to reuse, or the name and groups of a new key, made when the code
-- is exchanged (an unredeemed consent leaves nothing behind).
CREATE TABLE oauth_codes (
    hash           TEXT PRIMARY KEY,
    client_id      TEXT NOT NULL,
    redirect_uri   TEXT NOT NULL,
    code_challenge TEXT NOT NULL,                 -- PKCE S256
    resource       TEXT NOT NULL,
    expires_at     INTEGER NOT NULL,              -- negative once used (replay detection)
    key_id         TEXT REFERENCES keys (id) ON DELETE CASCADE, -- the key to reuse; NULL = make one
    key_name       TEXT NOT NULL DEFAULT '',      -- for a new key
    groups         TEXT NOT NULL DEFAULT '[]',    -- JSON array of group IDs, for a new key
    approved_by    TEXT NOT NULL DEFAULT '',      -- admin username, for the admin log
    approved_ip    TEXT NOT NULL DEFAULT '',
    family         TEXT                           -- set at exchange; 'replayed' if a replay came first
);

-- Refresh-token rotation detects reuse (RFC 9700 section 4.14.2): every
-- token belongs to the family of the grant (code exchange) it came from, and
-- a refresh token is marked consumed instead of deleted when it is used.
-- Presenting a consumed one means it leaked, so the whole family is revoked.
CREATE TABLE oauth_tokens (
    hash        TEXT PRIMARY KEY,
    key_id      TEXT NOT NULL REFERENCES keys (id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('access', 'refresh')),
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    family      TEXT NOT NULL DEFAULT '',
    consumed_at INTEGER
);
CREATE INDEX oauth_tokens_key ON oauth_tokens (key_id);
CREATE INDEX oauth_tokens_expires ON oauth_tokens (expires_at);
CREATE INDEX oauth_tokens_family ON oauth_tokens (family);

-- Who changed what: sign-ins, keys, admins, groups, approvals, OAuth grants.
CREATE TABLE admin_log (
    id     INTEGER PRIMARY KEY,
    time   INTEGER NOT NULL,
    actor  TEXT NOT NULL,                         -- admin username, "cli", or "" (failed sign-in)
    action TEXT NOT NULL,                         -- e.g. "sign_in", "key.create"; see adminlog
    target TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT '',
    ip     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX admin_log_time ON admin_log (time);
