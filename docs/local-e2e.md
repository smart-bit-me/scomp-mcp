# Local end-to-end test

This maintainer workflow exercises a locally built relay, web client, agent,
and `scomp-mcp` without changing production. Run it from a workspace containing
the sibling `scomp-server`, `scomp-web`, `scomp-agent`, and `scomp-mcp`
repositories. Keep the server, agent, account API, login, and MCP Inspector in
separate terminals.

`scripts/e2e/run.sh` automates the whole workflow below, including a large
output burst, full-history reattach, tail resync, a relay restart during an open
terminal, and revocation. It isolates its relay, agent identity, tmux server,
and credentials under `/tmp/scomp-e2e`:

```sh
SRC=$HOME/code/BOPOHA scripts/e2e/run.sh
```

The steps below do the same by hand.

The filesystem-auth web UI has no account-login screen, so the workflow uses
the local account API to pair the host and approve the MCP request.

## 1. Start the relay

From `scomp-server`:

```sh
make build
SCOMP_JWT_SECRET='local-mcp-test-secret-keep-this-stable' \
SCOMP_MCP_VERIFICATION_URL='http://localhost:8000/' \
./bin/scomp-server --port 8000 --storage-root /tmp/scomp-mcp-e2e \
  --static-dir ../scomp-web
```

Use a dedicated storage root and keep the JWT secret stable for the duration of
the test.

## 2. Start a terminal session and agent

Create a test session, then start the locally built agent in the foreground and
copy the UID it prints:

```sh
tmux new-session -d -s mcp-test
cd ../scomp-agent
make build
./bin/scomp --server ws://localhost:8000/agent --mode full --no-qr
```

`--mode full` is deliberate because this test submits a harmless command. Use
`readonly` for observation-only testing.

## 3. Create an account and pair the host

In another terminal, use a password of at least 12 bytes. Repeated registration
may return HTTP 409; login still works with the existing local account.

```sh
curl -sS -X POST http://localhost:8000/api/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"mcp-test@example.com","password":"local-test-password"}'

SCOMP_TEST_ACCOUNT_TOKEN=$(curl -sS -X POST http://localhost:8000/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"mcp-test@example.com","password":"local-test-password"}' | jq -r .token)

SCOMP_TEST_HOST_UID='paste-the-agent-uid-here'
curl -sS -X POST \
  "http://localhost:8000/api/hosts/pair/start?uid=${SCOMP_TEST_HOST_UID}" \
  -H "Authorization: Bearer ${SCOMP_TEST_ACCOUNT_TOKEN}"
```

Type the returned PIN into the foreground agent terminal.

## 4. Start MCP login

From `scomp-mcp`, start login and leave it polling while you approve the printed
code:

```sh
make build
./scomp-mcp login --server http://localhost:8000 --no-browser \
  --config /tmp/scomp-mcp-e2e.json
```

## 5. Approve the device request

In the account terminal, paste the user code printed by `scomp-mcp login` and
approve only the test host:

```sh
SCOMP_TEST_USER_CODE='PASTE-CODE'
jq -n --arg code "${SCOMP_TEST_USER_CODE}" --arg host "${SCOMP_TEST_HOST_UID}" \
  '{user_code:$code,allowed_hosts:[$host],lifetime_days:1}' |
  curl -sS -X POST http://localhost:8000/api/mcp/device/approve \
    -H "Authorization: Bearer ${SCOMP_TEST_ACCOUNT_TOKEN}" \
    -H 'Content-Type: application/json' --data-binary @-
```

The waiting login command should save the one-day test credential and exit.

## 6. Exercise the tools

Confirm the credential and launch the MCP Inspector:

```sh
./scomp-mcp status --config /tmp/scomp-mcp-e2e.json
npx -y @modelcontextprotocol/inspector ./scomp-mcp serve \
  --config /tmp/scomp-mcp-e2e.json
```

Exercise the complete lifecycle:

1. Call `scomp_list_hosts` and select the paired online host.
2. Call `scomp_list_sessions` and select `tmux:mcp-test` in `full` mode.
3. Call `scomp_open_terminal` and retain its terminal ID, cursor, and
   generation.
4. Call `scomp_send_input` with a harmless marker and explicit `submit: true`.
5. Call `scomp_read_terminal` repeatedly with the latest cursor and generation
   until the marker is visible. The first read may contain only echoed input.
6. Call `scomp_close_terminal` even when an earlier step fails.

Do not interpret successful input delivery as command completion or fabricate
an exit status.

## 7. Test revocation and clean up

Revoke the credential through the local account API or web UI and confirm that
the next API/tool request fails. Then stop the relay and agent, remove the
isolated tmux session, and remove only the temporary files created for this
test:

```sh
tmux kill-session -t mcp-test
rm -f /tmp/scomp-mcp-e2e.json
```

Remove `/tmp/scomp-mcp-e2e` after confirming that it is the dedicated test
storage root and that no process is using it.
