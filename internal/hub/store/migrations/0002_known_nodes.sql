-- Every node that has connected, so the dashboard still lists it while it
-- is offline: the registry forgets a node after its grace period, and all
-- of them when the hub restarts. An admin removes a row with Forget.
-- Times are Unix milliseconds.
CREATE TABLE known_nodes (
    name         TEXT PRIMARY KEY,
    node_version TEXT NOT NULL DEFAULT '',
    os           TEXT NOT NULL DEFAULT '',
    remote_addr  TEXT NOT NULL DEFAULT '',
    key_id       TEXT NOT NULL DEFAULT '',
    first_seen   INTEGER NOT NULL,
    last_seen    INTEGER NOT NULL,                -- when it was last online
    mcps         TEXT NOT NULL DEFAULT '[]'       -- JSON: its MCPs as last listed (known.MCP)
);

-- A hub upgraded to this version already knows its nodes by their keys.
INSERT INTO known_nodes (name, key_id, first_seen, last_seen)
SELECT name, id, created_at, last_used_at FROM keys
WHERE kind = 'node' AND last_used_at IS NOT NULL AND oauth_client IS NULL
ON CONFLICT (name) DO UPDATE SET
    key_id = excluded.key_id, last_seen = excluded.last_seen
    WHERE excluded.last_seen > known_nodes.last_seen;
