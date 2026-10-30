#!/bin/bash
# Local end-to-end test: real relay, agent, tmux, and scomp-mcp, isolated
# under /tmp/scomp-e2e (own HOME, TMUX_TMPDIR, storage, port). SRC must hold
# sibling scomp-server, scomp-agent, and scomp-mcp checkouts; the server is
# built through a go.work in SRC when the agent protocol is not yet released.
# Requires tmux, jq, and curl. E2E_PACED=1 slows the large-output step.
set -euo pipefail
ROOT=/tmp/scomp-e2e
SRC=${SRC:-$HOME/code/BOPOHA}
PORT=18000
BASE=http://localhost:$PORT
pids=()
cleanup() {
  for p in "${pids[@]:-}"; do kill "$p" 2>/dev/null || true; done
  TMUX_TMPDIR=$ROOT/tmux tmux kill-server 2>/dev/null || true
}
trap cleanup EXIT
rm -rf "$ROOT/storage" "$ROOT/home" "$ROOT/tmux" "$ROOT"/*.log "$ROOT/mcp.json" "$ROOT/pin.fifo"
mkdir -p "$ROOT/storage" "$ROOT/home" "$ROOT/tmux" "$ROOT/bin"

echo "== build"
(cd "$SRC/scomp-server" && go build -o "$ROOT/bin/scomp-server" ./cmd/scomp-server)
(cd "$SRC/scomp-agent" && GOWORK=off go build -o "$ROOT/bin/scomp" ./cmd/scomp)
(cd "$SRC/scomp-mcp" && GOWORK=off go build -o "$ROOT/bin/scomp-mcp" ./cmd/scomp-mcp)
(cd "$SRC/scomp-mcp" && GOWORK=off go build -o "$ROOT/driver.bin" ./scripts/e2e/driver)

start_relay() {
  SCOMP_JWT_SECRET='local-e2e-secret-keep-this-stable-0123456789' \
  SCOMP_MCP_VERIFICATION_URL="$BASE/" \
    "$ROOT/bin/scomp-server" --port $PORT --storage-root "$ROOT/storage" >>"$ROOT/relay.log" 2>&1 &
  relay_pid=$!; pids+=($relay_pid)
  for _ in $(seq 50); do curl -fsS -o /dev/null "$BASE/api/hosts" -H 'Authorization: Bearer x' 2>/dev/null && break; curl -s -o /dev/null "$BASE/" && break; sleep 0.1; done
}
echo "== relay"; start_relay

echo "== tmux + agent"
export TMUX_TMPDIR=$ROOT/tmux
tmux new-session -d -s e2e -x 120 -y 40
mkfifo "$ROOT/pin.fifo"
exec 3<>"$ROOT/pin.fifo"
HOME=$ROOT/home "$ROOT/bin/scomp" --server ws://localhost:$PORT/agent --mode full --no-qr <&3 >"$ROOT/agent.log" 2>&1 &
pids+=($!)
for _ in $(seq 50); do grep -q 'UID:' "$ROOT/agent.log" && break; sleep 0.2; done
UID_=$(grep -o 'UID: *[0-9a-f]*' "$ROOT/agent.log" | awk '{print $2}')
echo "agent uid $UID_"

echo "== account + pairing"
curl -sS -X POST $BASE/api/auth/register -H 'Content-Type: application/json' \
  -d '{"email":"e2e@example.com","password":"local-test-password"}' >/dev/null || true
TOKEN=$(curl -sS -X POST $BASE/api/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"e2e@example.com","password":"local-test-password"}' | jq -r .token)
sleep 1
PIN=$(curl -sS -X POST "$BASE/api/hosts/pair/start?uid=$UID_" -H "Authorization: Bearer $TOKEN" | jq -r .pin)
echo "$PIN" >&3
for _ in $(seq 50); do grep -q 'PIN submitted' "$ROOT/agent.log" && break; sleep 0.2; done
sleep 2

echo "== mcp login (typed code, narrowed scopes)"
"$ROOT/bin/scomp-mcp" login --server $BASE --no-browser --config "$ROOT/mcp.json" >"$ROOT/login.log" 2>&1 &
login_pid=$!
for _ in $(seq 50); do grep -qE '^ +[A-Z0-9]{4}-[A-Z0-9]{4}$' "$ROOT/login.log" && break; sleep 0.2; done
CODE=$(grep -oE '^ +[A-Z0-9]{4}-[A-Z0-9]{4}$' "$ROOT/login.log" | tr -d ' ')
URI=$(grep -o 'http[^ ]*' "$ROOT/login.log" | head -1)
echo "verification uri: $URI (must not contain the code)"; [[ "$URI" != *"$CODE"* ]]
curl -sS "$BASE/api/mcp/device/request?user_code=$CODE" -H "Authorization: Bearer $TOKEN" | jq -c '{label,scopes,requester_ip,requester_user_agent}'
jq -n --arg c "$CODE" --arg h "$UID_" '{user_code:$c,allowed_hosts:[$h],lifetime_days:1,scopes:["hosts:read","terminal:read","terminal:write"]}' |
  curl -sS -o /dev/null -w 'approve HTTP %{http_code}\n' -X POST $BASE/api/mcp/device/approve -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' --data-binary @-
wait $login_pid; tail -1 "$ROOT/login.log"

echo "== driver: main"
E2E_STATE=$ROOT/state "$ROOT/driver.bin" "$ROOT/bin/scomp-mcp" "$ROOT/mcp.json" main

echo "== driver: relay restart during a session"
rm -f "$ROOT/ready"
E2E_READY=$ROOT/ready "$ROOT/driver.bin" "$ROOT/bin/scomp-mcp" "$ROOT/mcp.json" after-relay-restart &
driver_pid=$!
for _ in $(seq 100); do [ -f "$ROOT/ready" ] && break; sleep 0.2; done
kill $relay_pid; wait $relay_pid 2>/dev/null || true
sleep 3
start_relay
wait $driver_pid

echo "== revoke"
CRED=$(jq -r .credential.id "$ROOT/mcp.json")
curl -sS -o /dev/null -w 'revoke HTTP %{http_code}\n' -X DELETE "$BASE/api/mcp/credentials/$CRED" -H "Authorization: Bearer $TOKEN"
"$ROOT/driver.bin" "$ROOT/bin/scomp-mcp" "$ROOT/mcp.json" revoked || true
echo "== relay log warnings"; grep -iE 'error|exceed|panic|fatal' "$ROOT/relay.log" | head -10 || true
echo "== done"
