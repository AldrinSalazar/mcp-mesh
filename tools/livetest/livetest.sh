#!/usr/bin/env bash
# livetest runs the real hub and node binaries against fake MCPs in a sandbox
# and checks what a user would see. Unit tests have passed while the binaries
# were broken, so run this before calling a change done.
#
#   tools/livetest/livetest.sh            # build, test, clean up
#   KEEP=1 tools/livetest/livetest.sh     # keep the sandbox and logs
#
# It uses ports 7100-7102 and 9123 on 127.0.0.1 and its own XDG directories,
# so it doesn't touch your node config, socket or service.
set -euo pipefail
cd "$(dirname "$0")/../.."
export PATH=$HOME/.local/go/bin:$PATH GOCACHE=${GOCACHE:-$PWD/.cache/go-build}

sbx=$(mktemp -d /tmp/mm-livetest.XXXXXX)
mkdir -p "$sbx"/{bin,cfg,state,run}
export XDG_CONFIG_HOME=$sbx/cfg XDG_STATE_HOME=$sbx/state XDG_RUNTIME_DIR=$sbx/run MCPMESH_NO_INPUT=1
hub=http://127.0.0.1:7102/mcp
pids=()
failures=0

cleanup() {
	"$sbx/bin/mcp-mesh-node" stop >/dev/null 2>&1 || true
	for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
	wait 2>/dev/null || true
	if [[ ${KEEP:-} == 1 || $failures -gt 0 ]]; then
		echo "sandbox kept: $sbx (hub.log, app.log, state/mcp-mesh/node.log)"
	else
		rm -rf "$sbx"
	fi
}
trap cleanup EXIT

pass() { echo "  ok    $1"; }
fail() {
	echo "  FAIL  $1"
	echo "        $2"
	failures=$((failures + 1))
}

# Every subprocess is bounded: curl by --max-time (the curl function below),
# mcpcall and the CLIs by coreutils timeout, so one hung process fails its check
# instead of hanging the run. Polling deadlines alone can't do that.
curl() { command curl --max-time "${curl_timeout:-20}" "$@"; }

# status_problem MODE STATUS: why a command's exit status is wrong for MODE, or
# nothing. MODE is "ok" (must exit 0), "fail" (must exit non-zero) or "any".
status_problem() {
	case $1 in
	ok) (($2 == 0)) || echo "exit $2, wanted 0; " ;;
	fail) (($2 != 0)) || echo "exit 0, wanted non-zero; " ;;
	esac
}

# expect[_ok|_fail] NAME PATTERN CMD...: CMD's output must match the extended
# regex; _ok also requires exit status 0, _fail a non-zero one. Plain expect
# ignores the status, for output that matters more than how the command ended.
expect_mode() {
	local mode=$1 name=$2 pattern=$3 out status=0 why
	shift 3
	out=$("$@" 2>&1) || status=$?
	why=$(status_problem "$mode" "$status")
	if [[ -z $why ]] && grep -Eq -- "$pattern" <<<"$out"; then pass "$name"; else fail "$name" "${why}got: ${out:0:300}"; fi
}
expect() { expect_mode any "$@"; }
expect_ok() { expect_mode ok "$@"; }
expect_fail() { expect_mode fail "$@"; }

# run NAME CMD...: CMD must succeed.
run() {
	local name=$1 out
	shift
	if out=$("$@" 2>&1); then pass "$name"; else fail "$name" "exit $?: ${out:0:300}"; fi
}

# eventually[_ok|_fail] SECONDS NAME PATTERN CMD...: like expect, retrying until
# the deadline; the last attempt is what gets reported.
eventually_mode() {
	local mode=$1 seconds=$2 name=$3 pattern=$4 out status deadline why
	shift 4
	deadline=$((SECONDS + seconds))
	while :; do
		status=0
		out=$("$@" 2>&1) || status=$?
		why=$(status_problem "$mode" "$status")
		if [[ -z $why ]] && grep -Eq -- "$pattern" <<<"$out"; then pass "$name"; return; fi
		if ((SECONDS >= deadline)); then fail "$name" "after ${seconds}s ${why}got: ${out:0:300}"; return; fi
		sleep 0.2
	done
}
eventually() { eventually_mode any "$@"; }
eventually_ok() { eventually_mode ok "$@"; }
eventually_fail() { eventually_mode fail "$@"; }

# callas KEY ARGS...: mcpcall against the hub as the key's client (an empty key
# sends none). call_timeout bounds one invocation; the 24 MB calls raise it.
callas() {
	local key=$1
	shift
	MCPMESH_CLIENT_KEY=$key timeout "${call_timeout:-30}" "$sbx/bin/mcpcall" "$hub" "$@"
}
call() { callas "${MCPMESH_CLIENT_KEY:-}" "$@"; }
hubctl() { timeout 30 "$sbx/bin/mcp-mesh-hub" --data-dir "$sbx/hub-data" "$@"; }
node() { timeout 60 "$sbx/bin/mcp-mesh-node" "$@"; }
# secret_of KIND: the key secret in `key create` output read from stdin.
secret_of() { grep -Eo "mm_$1_[0-9A-Za-z]{43}" | head -1; }

# api METHOD PATH [JSON]: call the JSON API with the API session's cookie and
# CSRF token; prints the status code, then the body.
api() {
	local args=(-sS -b "$sbx/api.jar" -c "$sbx/api.jar" -X "$1" -w '\n%{http_code}' -H "X-CSRF-Token: ${csrf:-}")
	[[ -n ${3:-} ]] && args+=(-H 'Content-Type: application/json' --data "$3")
	curl "${args[@]}" "http://127.0.0.1:7100/api/v1$2" | tac
}
admin_password=livetest-admin-password
# api_login: sign the API session in as the first admin and keep its CSRF token.
api_login() {
	local out
	out=$(api POST /session "{\"username\":\"admin\",\"password\":\"$admin_password\"}")
	csrf=$(grep -Eo '"csrf":"[^"]+"' <<<"$out" | cut -d'"' -f4)
	[[ -n $csrf ]]
}

start_app() {
	"$sbx/bin/fakeapp" 127.0.0.1:9123 >>"$sbx/app.log" 2>&1 &
	app=$!
	pids+=("$app")
}

echo "building the dashboard"
# The hub embeds web/dist, so the SPA is built first.
(
	. "${NVM_DIR:-$HOME/.nvm}/nvm.sh" >/dev/null 2>&1 || true
	cd web
	[[ -d node_modules ]] || npm ci --no-audit --no-fund >/dev/null
	npm run build >/dev/null
)
echo "building into $sbx/bin"
# -race makes the binaries report data races in their logs, checked at the end.
go build -race -o "$sbx/bin/" ./cmd/... ./tools/fakeapp ./tools/mcpcall

echo "keys"
node_key=$(hubctl key create node livetest | secret_of node)
client_key=$(hubctl key create client livetest-client | secret_of client)
if [[ -n $node_key && -n $client_key ]]; then
	pass "key create prints node and client secrets"
else
	fail "key create" "missing secrets in output"
fi
expect_ok "keys are stored in hub.db" "hub.db" ls "$sbx/hub-data"
expect_fail "there are no admin keys" "sign in with a password" hubctl key create admin someone
expect_ok "key list shows names, never secrets" "livetest-client" hubctl key list
if hubctl key list | grep -q "mm_"; then fail "key list hides secrets" "found a secret"; else pass "key list hides secrets"; fi
export MCPMESH_CLIENT_KEY=$client_key

echo "hub, node and MCPs"
MCPMESH_ADMIN_PASSWORD=$admin_password "$sbx/bin/mcp-mesh-hub" --data-dir "$sbx/hub-data" --admin-listen 127.0.0.1:7100 --node-listen 127.0.0.1:7101 \
	--client-listen 127.0.0.1:7102 --mdns=false >"$sbx/hub.log" 2>&1 &
pids+=($!)
start_app
eventually_ok 10 "hub answers /healthz" "ok" curl -fsS http://127.0.0.1:7100/healthz
expect_ok "hub serves the dashboard app" '<div id="app">' curl -fsS http://127.0.0.1:7100/
expect_ok "dashboard deep links serve the app" '<div id="app">' curl -fsS http://127.0.0.1:7100/nodes
asset=$(curl -fsS http://127.0.0.1:7100/ | grep -Eo '/assets/index-[^"]+\.js' | head -1)
expect_ok "dashboard assets are cached for good" "immutable" curl -fsSI "http://127.0.0.1:7100$asset"
expect_ok "dashboard sends a CSP" "Content-Security-Policy: default-src 'self'" curl -fsSI http://127.0.0.1:7100/
run "init" node init --hub ws://127.0.0.1:7101/node
printf 'node:\n  name: livetest\n' >>"$sbx/cfg/mcp-mesh/node.yaml"
run "key set" node key set "$node_key"
expect_ok "node.yaml points at the key file" "key_file: .*node.key" cat "$sbx/cfg/mcp-mesh/node.yaml"
expect_ok "status says a hub key is set, without showing it" "Hub key: set" node status
if node status --json | grep -q "mm_node_"; then fail "status hides the key" "found the key"; else pass "status hides the key"; fi
run "add a connect MCP" node add --name app --url http://127.0.0.1:9123/sse
expect_ok "SSE transport detected" "^ +transport: sse" cat "$sbx/cfg/mcp-mesh/node.yaml"
run "add a launch MCP" node add --name cli -- "$sbx/bin/fakeapp" stdio
run "add a per-client MCP" node add --name pc --sharing per-client --max-clients 4 -- "$sbx/bin/fakeapp" stdio
run "add an MCP with its own call timeout" node add --name timed --call-timeout 2s -- "$sbx/bin/fakeapp" stdio
run "add a stateless HTTP MCP with its own call timeout" node add --name http --call-timeout 2s --url http://127.0.0.1:9123/stateless
run "add an MCP that lists malformed tools" node add --name bad -- "$sbx/bin/fakeapp" stdio bad-tools
expect_ok "per-client and timeout settings are saved" "sharing: per-client" cat "$sbx/cfg/mcp-mesh/node.yaml"
expect_ok "config is valid" "app" node status
run "start the node" node start
eventually_ok 20 "hub lists both MCPs' tools" "livetest__cli__render" call
expect_ok "hub lists the connect MCP" "livetest__app__render" call
eventually_ok 20 "an MCP with malformed tools keeps its good ones" "livetest__bad__render" call
if call | grep -Eq "no_schema|number_schema"; then fail "malformed tools are dropped" "listed: $(call | grep -E 'no_schema|number_schema')"; else pass "malformed tools are dropped"; fi
expect_ok "node and hub speak protocol 1" "node connected.*protocol=1" cat "$sbx/hub.log"

echo "calls"
expect_ok "launch MCP call" "isError=false\): rendered one" call livetest__cli__render '{"scene":"one"}'
expect_ok "connect MCP call" "isError=false\): rendered two" call livetest__app__render '{"scene":"two"}'
call_timeout=120 expect_ok "24 MB result (chunked frames)" "isError=false\): 25165824 bytes" call livetest__cli__big '{"mb":24}'
call_timeout=120 expect_ok "24 MB result over SSE" "isError=false\): 25165824 bytes" call livetest__app__big '{"mb":24}'

echo "MCP changes apply on reload"
# Like an MCP client, a running node reads its MCPs at start: a change is
# saved, and applied when the user reloads (which restarts all of them).
cli_pid=$(call livetest__cli__pid '{}' | grep -Eo '[0-9]+$')
expect_ok "adding an MCP to a running node only saves it" "mcp-mesh-node reload" node add --name late -- "$sbx/bin/fakeapp" stdio
expect_ok "status says the change isn't applied" "not applied yet" node status
if call | grep -q 'livetest__late__'; then fail "the hub doesn't list it before reload" "listed"; else pass "the hub doesn't list it before reload"; fi
expect_ok "the running MCPs weren't restarted" ": $cli_pid\$" call livetest__cli__pid '{}'
expect_ok "reload applies it" "node reloaded" node reload
eventually_ok 20 "the hub lists the new MCP after reload" "livetest__late__render" call

echo "auth"
expect_fail "call without a client key is refused" "401|[Uu]nauthorized" callas "" livetest__cli__render '{"scene":"x"}'
expect_fail "node key is refused as a client key" "401|[Uu]nauthorized" callas "$node_key" livetest__cli__render '{"scene":"x"}'
expect_ok "/healthz needs no key" "ok" curl -fsS http://127.0.0.1:7100/healthz
expect_ok "/readyz says the hub is ready" "^ready" curl -sS http://127.0.0.1:7100/readyz
expect_ok "unknown API paths are 404, not the app" "^404$" curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:7100/api/nodes
expect_ok "the first admin user exists" "^admin " hubctl admin list
temp_key=$(hubctl key create client livetest-temp | secret_of client)
eventually_ok 10 "a fresh client key works (hub picks up the database change)" "isError=false\): rendered temp" callas "$temp_key" livetest__cli__render '{"scene":"temp"}'
# mcp_raw KEY [SESSION] JSON: one raw MCP 2025-11-25 POST; prints the status,
# then the headers and body.
mcp_raw() {
	local args=(-sS -D - -o - -w '\n%{http_code}' -H "Authorization: Bearer $1" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream')
	[[ -n $2 ]] && args+=(-H "Mcp-Session-Id: $2" -H 'MCP-Protocol-Version: 2025-11-25')
	curl "${args[@]}" --data "$3" "$hub" | tac
}
init_out=$(mcp_raw "$temp_key" "" '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"livetest","version":"1"}}}')
temp_session=$(grep -i '^mcp-session-id:' <<<"$init_out" | head -1 | cut -d' ' -f2 | tr -d '\r')
if [[ -n $temp_session ]]; then pass "a session-based client gets a session"; else fail "a session-based client gets a session" "got: ${init_out:0:300}"; fi
mcp_raw "$temp_key" "$temp_session" '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null
expect_ok "another client's key can't use that session" '^403' mcp_raw "$client_key" "$temp_session" '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
expect_ok "the session's own key still can" '"name":"livetest__cli__render"' mcp_raw "$temp_key" "$temp_session" '{"jsonrpc":"2.0","id":3,"method":"tools/list"}'
run "revoke the client key" hubctl key revoke livetest-temp
eventually_fail 10 "revoked client key stops working" "401|[Uu]nauthorized" callas "$temp_key" livetest__cli__render '{"scene":"x"}'

echo "JSON API"
expect_ok "/api/v1/info is open" "^200" api GET /info
expect_ok "/api/v1/nodes needs a session" "^401" api GET /nodes
expect_ok "API sign-in with a wrong password is refused" "^401" api POST /session '{"username":"admin","password":"wrong-password-123"}'
if api_login; then pass "API sign-in returns a CSRF token"; else fail "API sign-in" "no CSRF token"; fi
expect_ok "API lists the node's MCPs" "^200[^$]*" api GET /nodes
expect_ok "API node list names the node and its key" '"name":"livetest".*"key_id":"k_' api GET /nodes
expect_ok "API tools list has the launch MCP" "livetest__cli__render" api GET /tools
expect_ok "API says which definitions were dropped and why" '"name":"bad"[^}]*"problems":\["tool \\"no_schema\\" dropped: [^]]*missing input schema' api GET /nodes
expect_ok "audit log has the earlier calls" '"tool":"livetest__cli__render".*"outcome":"ok"' api GET '/audit?tool=livetest__cli__render'
expect_ok "stats count the last hour's calls" '"calls_last_hour":[1-9][0-9]*,' api GET /stats
saved_csrf=$csrf
csrf=wrong
expect_ok "creating a key without the CSRF token is refused" "^403" api POST /keys '{"kind":"client","name":"api-key"}'
csrf=$saved_csrf
created=$(api POST /keys '{"kind":"client","name":"api-key"}')
api_secret=$(secret_of client <<<"$created")
api_key_id=$(grep -Eo '"id":"k_[0-9a-f]+"' <<<"$created" | head -1 | cut -d'"' -f4)
if [[ $created == 201* && -n $api_secret && -n $api_key_id ]]; then pass "API creates a client key with its setup"; else fail "API creates a client key" "got: ${created:0:300}"; fi
expect_ok "the API-created key works" "isError=false\): rendered api" callas "$api_secret" livetest__cli__render '{"scene":"api"}'
expect_ok "API revokes the key by id" "^200" api DELETE "/keys/$api_key_id"
expect_fail "the revoked key stops working at once" "401|[Uu]nauthorized" callas "$api_secret" livetest__cli__render '{"scene":"x"}'
run "admin create from the CLI" bash -c "printf '%s\n' second-admin-password | '$sbx/bin/mcp-mesh-hub' --data-dir '$sbx/hub-data' admin create second --password-stdin"
expect_ok "the new admin can sign in" "^200" curl -sS -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data '{"username":"second","password":"second-admin-password"}' http://127.0.0.1:7100/api/v1/session
expect_ok "API sign-out" "^204" api DELETE /session
expect_ok "signed out: the API needs a session again" "^401" api GET /nodes

echo "access control"
api_login || fail "API sign-in for access control" "no CSRF token"
group=$(api POST /groups '{"name":"cli only","grants":[{"node":"livetest","mcp":"cli","tool":"*"}]}')
group_id=$(grep -Eo '"id":"g_[0-9a-f]+"' <<<"$group" | head -1 | cut -d'"' -f4)
if [[ $group == 201* && -n $group_id ]]; then pass "API creates a group"; else fail "API creates a group" "got: ${group:0:300}"; fi
narrow=$(api POST /keys "{\"kind\":\"client\",\"name\":\"narrow\",\"groups\":[\"$group_id\"]}")
narrow_secret=$(secret_of client <<<"$narrow")
narrow_id=$(grep -Eo '"id":"k_[0-9a-f]+"' <<<"$narrow" | head -1 | cut -d'"' -f4)
eventually_ok 10 "a key in a group sees only its grants" "livetest__cli__render" callas "$narrow_secret"
if callas "$narrow_secret" 2>&1 | grep -q "livetest__app__"; then
	fail "a key in a group doesn't see other MCPs" "it lists livetest__app tools"
else
	pass "a key in a group doesn't see other MCPs"
fi
expect_fail "calling an ungranted tool looks like an unknown tool" "unknown tool" callas "$narrow_secret" livetest__app__render '{"scene":"x"}'
expect_ok "the effective tool list matches" 'livetest__cli__render' api GET "/keys/$narrow_id/tools"
expect_ok "a key in no group sees nothing" "^200" api PUT "/keys/$narrow_id/groups" '{"groups":[]}'
# mcpcall prints one tool per line, so an empty list is an empty line.
eventually_ok 10 "after leaving its group the key sees no tools" '^$' callas "$narrow_secret"
expect_ok "keys made with the CLI join Everything" '"name":"livetest-client"[^}]*"groups":\["g_everything"\]' api GET /keys

expect_ok "a node can be set to need approval" '"level":"approve"' api PUT /nodes/livetest/trust '{"level":"approve"}'
eventually_fail 10 "an unapproved node's tools are hidden from clients" "unknown tool" call livetest__cli__render '{"scene":"x"}'
expect_ok "approvals list the node's tools" '"state":"node_pending"' api GET /approvals
# An approval names the definitions the admin saw, by hash.
cli_refs=$(api GET /approvals | sed 1d | jq -c '[.[] | select(.mcp == "cli") | {mcp, tool, hash}]')
expect_ok "approving with a stale hash is refused" '^409' api POST /nodes/livetest/approve '{"tools":[{"mcp":"cli","tool":"render","hash":"0000"}]}'
expect_ok "a refused approval leaves the node unapproved" '"state":"node_pending"' api GET /approvals
expect_ok "approving the node and the tools it was shown" '"approved":true' api POST /nodes/livetest/approve "{\"tools\":$cli_refs}"
eventually_ok 10 "approved tools are callable again" "rendered approved" call livetest__cli__render '{"scene":"approved"}'
# The node ran at the default level (trust) first, so its tools were pinned
# automatically and are approved as soon as the node is.
expect_ok "tools pinned earlier are approved with the node" "rendered pinned" call livetest__app__render '{"scene":"pinned"}'
render_ref=$(jq -c '[.[0] | {node: "livetest", mcp, tool, hash}]' <<<"$cli_refs")
expect_ok "approving an approved tool again is a no-op" '"approved":0' api POST /approvals "{\"tools\":$render_ref}"
expect_ok "the node goes back to the default" "^200" api PUT /nodes/livetest/trust '{"level":null}'

expect_ok "protected resource metadata names the hub as authorization server" '"authorization_servers":\["http://localhost:7102"\]' curl -fsS http://127.0.0.1:7102/.well-known/oauth-protected-resource/mcp
expect_ok "authorization server metadata requires PKCE" '"code_challenge_methods_supported":\["S256"\]' curl -fsS http://127.0.0.1:7102/.well-known/oauth-authorization-server
expect_ok "/mcp without credentials points clients at OAuth" 'resource_metadata="http://localhost:7102/.well-known/oauth-protected-resource/mcp"' curl -sS -o /dev/null -D - -X POST http://127.0.0.1:7102/mcp
reg=$(curl -sS -H 'Content-Type: application/json' --data '{"client_name":"livetest app","redirect_uris":["http://127.0.0.1:9/cb"],"token_endpoint_auth_method":"none"}' http://127.0.0.1:7102/oauth/register)
oauth_client=$(grep -Eo '"client_id":"oc_[^"]+"' <<<"$reg" | cut -d'"' -f4)
if [[ -n $oauth_client ]]; then pass "dynamic client registration"; else fail "dynamic client registration" "got: ${reg:0:300}"; fi
authz="http://127.0.0.1:7102/oauth/authorize?response_type=code&client_id=$oauth_client&redirect_uri=http%3A%2F%2F127.0.0.1%3A9%2Fcb&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256&state=s1"
expect_ok "the authorize page asks a signed-out visitor to sign in" 'name="password"' curl -fsS "$authz"
# A whole OAuth grant as an app makes it: sign in, allow, exchange the code,
# use and rotate the tokens. Reusing a spent refresh token revokes the grant.
verifier=livetest-pkce-verifier-0123456789-abcdefghijklmnop
challenge=$(printf %s "$verifier" | openssl dgst -sha256 -binary | base64 | tr '+/' '-_' | tr -d '=')
oauth_jar="$sbx/oauth.jar"
authz_flow="http://127.0.0.1:7102/oauth/authorize?response_type=code&client_id=$oauth_client&redirect_uri=http%3A%2F%2F127.0.0.1%3A9%2Fcb&code_challenge=$challenge&code_challenge_method=S256&state=s2"
# oauth_post PAGE [NAME=VALUE...]: submit the page's form as a browser would
# (hidden inputs and checked boxes) plus the given fields; prints the response
# headers and body.
oauth_post() {
	local page=$1 args=() field
	shift
	while read -r field; do
		[[ -n $field ]] && args+=(--data-urlencode "$field")
	done < <(grep -Eo '<input type="(hidden|checkbox|radio)" name="[^"]*" value="[^"]*"( checked)?>' <<<"$page" |
		grep -E 'type="hidden"|checked>' | sed -E 's/.*name="([^"]*)" value="([^"]*)".*/\1=\2/')
	for field in "$@"; do args+=(--data-urlencode "$field"); done
	curl -sS -D - -b "$oauth_jar" -c "$oauth_jar" "${args[@]}" http://127.0.0.1:7102/oauth/authorize
}
token_req() { curl -sS http://127.0.0.1:7102/oauth/token "$@"; }
signin_page=$(curl -sS -b "$oauth_jar" -c "$oauth_jar" "$authz_flow")
consent_page=$(oauth_post "$signin_page" action=signin username=admin "password=$admin_password")
expect_ok "OAuth: the admin signs in and sees the consent page" 'value="allow"' echo "$consent_page"
allowed=$(oauth_post "$consent_page" action=allow key_name=livetest-oauth)
code=$(grep -i '^location:' <<<"$allowed" | grep -Eo 'code=[^&[:space:]]+' | cut -d= -f2)
if [[ -n $code ]]; then pass "OAuth: allowing redirects back with a code"; else fail "OAuth: allowing redirects back with a code" "got: ${allowed:0:300}"; fi
expect_ok "OAuth: no key exists until the code is redeemed" "^$" sh -c "\"$sbx/bin/mcp-mesh-hub\" --data-dir \"$sbx/hub-data\" key list | grep livetest-oauth || true"
grant=$(token_req -d grant_type=authorization_code -d "code=$code" -d "client_id=$oauth_client" -d redirect_uri=http://127.0.0.1:9/cb -d "code_verifier=$verifier")
access=$(jq -r '.access_token // empty' <<<"$grant")
refresh_tok=$(jq -r '.refresh_token // empty' <<<"$grant")
if [[ -n $access && -n $refresh_tok ]]; then pass "OAuth: the code buys tokens"; else fail "OAuth: the code buys tokens" "got: ${grant:0:300}"; fi
expect_ok "OAuth: the key is made at exchange" "livetest-oauth" hubctl key list
expect_ok "OAuth: the access token lists tools" "livetest__" callas "$access"
rotated=$(token_req -d grant_type=refresh_token -d "refresh_token=$refresh_tok" -d "client_id=$oauth_client")
new_access=$(jq -r '.access_token // empty' <<<"$rotated")
expect_ok "OAuth: the new access token works" "livetest__" callas "$new_access"
expect_ok "OAuth: reusing a spent refresh token is refused" '"error":"invalid_grant".*already used' token_req -d grant_type=refresh_token -d "refresh_token=$refresh_tok" -d "client_id=$oauth_client"
expect_ok "OAuth: and revokes the grant's newest tokens" "^401" curl -sS -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $new_access" "$hub"
reconsent=$(curl -sS -b "$oauth_jar" -c "$oauth_jar" "$authz_flow")
expect_ok "OAuth: re-authorizing offers the app's key" 'type="radio" name="key" value="k_[0-9a-f]+" checked' echo "$reconsent"
expect_ok "the admin log records changes" '"action":"node.approve".*"action":"group.create"' api GET /admin-log

echo "prompts, resources, progress, timeouts, per-client, aliases"
expect_ok "prompts are listed" "livetest__app__brief" call :prompts
expect_ok "a prompt can be fetched" "user: Render forest" call :prompt livetest__app__brief '{"scene":"forest"}'
expect_ok "resources are listed under the hub's URI" "mcp-mesh://livetest/app/fake://scene/current" call :resources
expect_ok "a resource can be read" "mcp-mesh://livetest/app/fake://scene/current: the current scene" call :read mcp-mesh://livetest/app/fake://scene/current
expect_ok "templates are listed" "mcp-mesh://livetest/app/fake://scene/\{name\}" call :templates
expect_ok "a templated resource can be read" "scene lake" call :read mcp-mesh://livetest/app/fake://scene/lake
eventually_ok 10 "resource reads are in the audit log" '"kind":"resource","tool":"mcp-mesh://livetest/app/fake://scene/lake"' api GET "/audit?node=livetest&mcp=app"
expect_ok "the health endpoint has metrics" '"ready":true.*"mcps_ready":[1-9]' api GET /health
expect_ok "progress reaches the client" "progress: 3" call livetest__cli__progress '{"steps":3}'
expect_fail "an MCP's own timeout ends a slow call" "after ([1-4](\.[0-9]+)?s)" call livetest__timed__slow '{"seconds":30}'
# The stdio "timed" MCP can't show an upstream request being stopped. This one
# is stateless Streamable HTTP (2026-07-28) whose handler blocks until its
# request context ends, so the count only rises if the node really cancelled
# its POST when the call timed out.
eventually_ok 20 "the stateless HTTP MCP is listed" "livetest__http__hold" call
expect_ok "no hold call has been cancelled yet" '\): 0$' call livetest__http__cancelled '{}'
expect_fail "the HTTP MCP's own timeout ends a held call" "after ([1-4](\.[0-9]+)?s)" call livetest__http__hold '{}'
eventually_ok 10 "the HTTP MCP's handler saw its request end" '\): 1$' call livetest__http__cancelled '{}'
expect_ok "the HTTP MCP logged the ended request" "hold: context ended" cat "$sbx/app.log"
second_key=$(hubctl key create client livetest-second | secret_of client)
eventually_ok 10 "the second client key works" "isError=false" callas "$second_key" livetest__pc__pid '{}'
pid_a=$(call livetest__pc__pid '{}' | grep -Eo '[0-9]+$')
pid_a2=$(call livetest__pc__pid '{}' | grep -Eo '[0-9]+$')
pid_b=$(callas "$second_key" livetest__pc__pid '{}' | grep -Eo '[0-9]+$')
if [[ -n $pid_a && $pid_a == "$pid_a2" && -n $pid_b && $pid_a != "$pid_b" ]]; then
	pass "per-client MCP: one process per client, reused by that client"
else
	fail "per-client MCP" "pids: first client $pid_a then $pid_a2, second client $pid_b"
fi
expect_ok "the hub counts the per-client processes" '"name":"pc"[^}]*"clients":2' api GET /nodes
expect_ok "the node shows the per-client processes" "per-client, 2 running" node status
expect_ok "an alias can be set" '"alias":"render_scene"' api PUT /overrides/livetest/cli/render '{"alias":"render_scene","description":"Render a scene by name."}'
eventually_ok 10 "clients see the alias" "^render_scene$" call
expect_ok "the alias can be called" "rendered aliased" call render_scene '{"scene":"aliased"}'
expect_ok "removing the alias" "^204" api DELETE /overrides/livetest/cli/render
eventually_ok 10 "the generated name is back" "^livetest__cli__render$" call

echo "failures and recovery"
call livetest__cli__crash '{}' >/dev/null 2>&1 || true
eventually_ok 20 "crashed launch MCP comes back" "rendered back" call livetest__cli__render '{"scene":"back"}'
kill "$app"
wait "$app" 2>/dev/null || true
expect_fail "closed app: caller learns why" "isError=true\).*(unavailable|not running|listening|refused)" call livetest__app__render '{"scene":"x"}'
start_app
eventually_ok 20 "reopened app serves calls again" "rendered again" call livetest__app__render '{"scene":"again"}'

echo "revocation in flight"
# under10s: an mcpcall result that came back in less than ten seconds.
under10s='after ([0-9]+ms|[0-9](\.[0-9]+)?s)'
# cli_in_flight NAME COUNT_PATTERN: wait until the hub counts that many calls
# running on the cli MCP, i.e. the background calls have reached it (the API
# session from "access control" is still signed in).
cli_in_flight() {
	eventually_ok 10 "$1" "\"name\":\"cli\"[^}]*\"in_flight\":$2," api GET /nodes
}
# slow_call KEY OUT N: start a 30s call in the background and wait until it is
# in flight (N calls running on the MCP, counting this one).
slow_call() {
	call_timeout=60 callas "$1" livetest__cli__slow '{"seconds":30}' >"$2" 2>&1 &
	pids+=($!)
	cli_in_flight "a call is in flight on the cli MCP ($3 running)" "[$3-9]"
}
slow_key=$(hubctl key create client livetest-slow | secret_of client)
eventually_ok 10 "the client key for the slow call works" "isError=false\): rendered slow" callas "$slow_key" livetest__cli__render '{"scene":"slow"}'
slow_call "$slow_key" "$sbx/slow-client.out" 1
slow_call "$client_key" "$sbx/slow-other.out" 2
run "revoke a client key during its call" hubctl key revoke livetest-slow
# The session closes with the call, so the client may see the cut-off
# rather than the hub's "cancelled" message.
eventually_ok 10 "its in-flight call is cancelled at once" "error $under10s: .*(cancelled|terminated)" cat "$sbx/slow-client.out"
cli_in_flight "only the other client's call is still running" "1"
expect_ok "hub logs the client key's revocation" "client key revoked.*livetest-slow.*disconnected=true" cat "$sbx/hub.log"
if [[ -s $sbx/slow-other.out ]]; then fail "another client's call keeps running" "got: $(cat "$sbx/slow-other.out")"; else pass "another client's call keeps running"; fi
slow_call "$client_key" "$sbx/slow-node.out" 2
run "revoke the node key during a call" hubctl key revoke livetest
eventually_ok 10 "the call on that node fails at once" "isError=true\)" cat "$sbx/slow-node.out"
expect_ok "hub disconnects only that key's connection" "node key revoked.*node=livetest.*disconnected=\[livetest\]" cat "$sbx/hub.log"
eventually_ok 20 "node status says its key was rejected" "rejected" node status
node_key=$(hubctl key create node livetest | secret_of node)
run "key set with a new node key" node key set "$node_key"
eventually_ok 30 "node reconnects with the new key" "rendered renewed" call livetest__cli__render '{"scene":"renewed"}'

echo "node leaves"
run "stop the node" node stop
api_login || fail "API sign-in again" "no CSRF token"
eventually_ok 10 "API shows the node offline" '"name":"livetest"[^]]*"online":false' api GET /nodes
expect_fail "calls to an offline node fail cleanly" "isError=true\)" call livetest__cli__render '{"scene":"x"}'

echo "node with a key for another name"
other_key=$(hubctl key create node someone-else | secret_of node)
run "key set to a key bound to another node" node key set "$other_key"
run "start the node with the wrong key" node start
eventually_ok 20 "hub refuses the node" "node refused" cat "$sbx/hub.log"
eventually_ok 20 "node status says its key was rejected" "rejected" node status
run "stop the node again" node stop

if grep -Eq 'panic|DATA RACE' "$sbx/hub.log" "$sbx/state/mcp-mesh/node.log" 2>/dev/null; then
	fail "no panics in logs" "$(grep -Eh 'panic|DATA RACE' "$sbx/hub.log" "$sbx/state/mcp-mesh/node.log" | head -3)"
else
	pass "no panics in logs"
fi

if ((failures)); then
	echo "$failures check(s) failed"
	exit 1
fi
echo "all checks passed"
