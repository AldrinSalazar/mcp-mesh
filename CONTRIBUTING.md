# Contributing

Thanks for helping. Open an issue before starting a substantial change so
the approach can be discussed; small fixes can go straight to a pull
request. Read [Architecture](docs/architecture.md) first: it shows how the
packages fit together.

## Build

You need Go 1.27+ and Node.js 22+.

```sh
make            # the dashboard (web/dist), then bin/mcp-mesh-hub and bin/mcp-mesh-node
make test       # Go tests with the race detector, the dashboard's typecheck and unit tests
make check      # go vet for Linux, macOS and Windows, gofmt, the tests, and the live test
```

The hub embeds `web/dist`. Without the dashboard build it still compiles
and runs, but `/` shows a "dashboard not built" page.

For dashboard work, `npm --prefix web run dev:mock` serves the dashboard
against fixture data with no hub (sign in as `admin` with any password);
`npm --prefix web run dev` proxies `/api` to a hub (`MCPMESH_HUB`, default
`http://127.0.0.1:7000`).

## Tests

| Layer | Command | What it covers |
| --- | --- | --- |
| Unit | `go test -race ./...` | every package; `internal/e2e` runs a hub, a node and an MCP in one process |
| Dashboard | `npm --prefix web test` | the dashboard's logic (vitest) |
| Browser | `make test-browser` | the dashboard in Chromium against its mock API (Playwright; `npx playwright install-deps chromium` once) |
| Live | `make livetest` | the real binaries on one machine: keys, calls, OAuth, groups, approvals, failures (uses ports 7100–7102 and 9123) |
| Machines | `make e2e-vm` | scenarios on Hyper-V VMs from WSL: fresh installs, power loss, reboots, mDNS, TLS, slow networks, backups ([e2e/README.md](e2e/README.md)) |

A change should pass `make check`. Add a test that fails without your
change; extend the live test when you change what a user sees.

GitHub Actions run `make vet`, the Go tests and the dashboard's checks on
every pull request, and build the node for every platform and the hub's
image (`deploy/Dockerfile`). A `v*` tag publishes them: the node archives
as a GitHub release with the changes since the last one, the image on
`ghcr.io`. Maintainers release with
`git tag -a v1.2.3 -m "mcp-mesh 1.2.3" && git push origin v1.2.3`; a tag
such as `v1.2.3-rc.1` makes a pre-release that doesn't become Latest.

## Conventions

- **Go:** the standard library first. MCP work uses
  `github.com/modelcontextprotocol/go-sdk` (read its source rather than
  guessing); WebSockets go through `internal/wire`.
- **Boundaries:** hub and node packages never import each other; shared
  code lives in `internal/proto`, `internal/wire` and `internal/discovery`.
  Change the node ↔ hub protocol with care: nodes and hubs are upgraded
  independently.
- **Logging:** `log/slog`, passed in as a `*slog.Logger` (nil means
  `slog.Default()`). No `fmt.Println` or `log.Printf` in library code.
- **Goroutines** have a clear owner and stop when their context ends or
  their connection closes.
- **Errors** are wrapped with context: `fmt.Errorf("open %s: %w", name, err)`.
- **Comments** explain why, not what. Match the style of the file you edit.
- **Tests** are table-driven where it fits, never sleep to synchronize (use
  channels, or poll with a deadline), use `t.Context()`, and pass with
  `-race`.
- **Platforms:** Linux and macOS are supported; Windows comes later. Put
  OS-specific code in build-tagged files (`_unix.go`, `_linux.go`,
  `_darwin.go`, `_windows.go`). Every package must compile with
  `GOOS=windows go vet`.

### Dashboard (`web/`)

- Vue 3, Vite and TypeScript, with no UI library. Colours and sizes are CSS
  variables in `web/src/styles/tokens.css` (light and dark); never use hex
  colours in components.
- Data comes only from `/api/v1` (`web/src/lib/api.ts`), live state from the
  events stream (`web/src/lib/live.ts`). Screens never poll.
- Status names and colours come from `web/src/lib/status.ts`, formatting
  from `web/src/lib/format.ts`, so the same thing looks the same everywhere.
- Don't commit `web/dist` (only its `.gitkeep`).

## Reporting bugs

Include the hub and node versions (`mcp-mesh-hub --version`,
`mcp-mesh-node --version`), the platform, steps to reproduce, and the
relevant logs (`mcp-mesh-node logs <mcp>`, the hub's log). Remove keys and
other secrets first. Report security problems privately; see
[SECURITY.md](SECURITY.md).

## Licensing of contributions

mcp-mesh is source-available under the PolyForm Noncommercial License
1.0.0, and its author also sells commercial licenses. By submitting a
contribution, you agree that it is licensed to the project under the same
terms and that the author may include it in mcp-mesh under any license,
commercial ones included. You confirm that you have the right to contribute
it.
