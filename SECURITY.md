# Security

mcp-mesh lets agents call tools on your machines, so security reports are
welcome and taken seriously.

## Reporting a vulnerability

Please report it privately, not in a public issue: use **Report a
vulnerability** under the repository's Security tab (GitHub private
vulnerability reporting). Include what an attacker can do, how to reproduce
it, and the versions affected. You'll get an answer as soon as possible,
and credit in the fix's release notes if you want it.

## What is in scope

- Getting past the hub's checks: keys, admin sign-in and sessions, CSRF,
  OAuth, groups and grants, trust levels and approvals.
- A hub making a node run something its `node.yaml` doesn't offer, or
  reaching tools `allow_tools` excludes.
- Secrets leaking: keys, tokens or passwords in logs, the API or the
  database in clear.
- Crashing or exhausting the hub or a node from the network.

## Running it safely

- Serve the hub over TLS (or a reverse proxy) when it is reachable beyond
  your own network; plain HTTP is accepted only from loopback and private
  addresses.
- Keep the dashboard's port (7000) private.
- Give each machine and each client its own key, and revoke keys you no
  longer use.
- Use the *Review changes* or *Approve everything* trust level for nodes
  whose MCP servers you don't control: a tool whose description changes is
  then held until you approve it.
