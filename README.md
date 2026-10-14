# scomp-mcp

`scomp-mcp` gives a local MCP-capable LLM controlled access to existing
tmux or GNU Screen sessions on remote hosts connected through scomp. The host
can have only a private IP: the scomp agent keeps the existing outbound relay
connection, so the laptop needs no VPN, inbound port, or SSH credential.

The component is deliberately separate from the remote `scomp` agent. It is an
MCP server over local stdio and a normal scoped client of `scomp-server`.

## Trust model

This is interactive terminal control, not a hidden remote command executor.
The user must manually create a tmux/Screen session and start the scomp agent on
the remote host:

```sh
tmux new-session -s work
scomp --mode full
```

The MCP tools can attach only to sessions the agent reports. Terminal output is
untrusted program output and may contain prompt-injection text. Input delivery
does not prove that a shell ran a command and does not provide an exit status.

## Build and login

```sh
go build -o scomp-mcp ./cmd/scomp-mcp
./scomp-mcp login
./scomp-mcp status
```

Login opens a browser approval page. The user must select at least one paired
host and explicitly approve the requested scopes and lifetime. Normal login
requests terminal read/write access; `scomp-mcp login --read-only` omits write
access. Credentials default to 30 days and the server caps them at 90 days.

The opaque credential is stored at the OS user config location under
`scomp/mcp.json` (`~/.config/scomp/mcp.json` on typical Linux systems), written
atomically with mode `0600`. `SCOMP_MCP_TOKEN` and `SCOMP_MCP_SERVER` can
override file values for ephemeral environments; environment variables may be
visible to other local processes and should be used carefully.

For a self-hosted development relay on the same laptop:

```sh
scomp-mcp login --server http://localhost:8080
```

Non-loopback relay URLs must use HTTPS.

The hosted relay must be upgraded before the default `scomp-mcp login` works.
An HTTP 404 from login means `/api/mcp/device/start` is not deployed on that
relay yet.

## Local end-to-end test

The local filesystem-auth web UI has no account-login screen, so this workflow
uses its account API to pair the host and approve the MCP request. Keep the
server, agent, login, and approval commands in separate terminals.

1. Start the modified relay from `scomp-server`:

   ```sh
   make build
   SCOMP_JWT_SECRET='local-mcp-test-secret-keep-this-stable' \
   SCOMP_MCP_VERIFICATION_URL='http://localhost:8000/' \
   ./bin/scomp-server --port 8000 --storage-root /tmp/scomp-mcp-e2e \
     --static-dir ../scomp-web
   ```

2. Manually create a terminal session, then start the modified test agent in
   the foreground and copy the UID it prints:

   ```sh
   tmux new-session -d -s mcp-test
   cd ../scomp-agent
   make build
   ./bin/scomp --server ws://localhost:8000/agent --mode full --no-qr
   ```

3. In another terminal, create a local account, obtain its token, and start
   pairing. Use a password of at least 12 bytes. Repeated registration may
   return `409`; login still works with the existing account.

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

4. Start login from `scomp-mcp`; it waits while you approve the printed code:

   ```sh
   ./scomp-mcp login --server http://localhost:8000 --no-browser \
     --config /tmp/scomp-mcp-e2e.json
   ```

5. In the account terminal, paste the printed code and approve the paired host:

   ```sh
   SCOMP_TEST_USER_CODE='PASTE-CODE'
   jq -n --arg code "${SCOMP_TEST_USER_CODE}" --arg host "${SCOMP_TEST_HOST_UID}" \
     '{user_code:$code,allowed_hosts:[$host],lifetime_days:1}' |
     curl -sS -X POST http://localhost:8000/api/mcp/device/approve \
       -H "Authorization: Bearer ${SCOMP_TEST_ACCOUNT_TOKEN}" \
       -H 'Content-Type: application/json' --data-binary @-
   ```

6. Confirm the credential and launch it from an MCP host:

   ```sh
   ./scomp-mcp status --config /tmp/scomp-mcp-e2e.json
   npx -y @modelcontextprotocol/inspector ./scomp-mcp serve \
     --config /tmp/scomp-mcp-e2e.json
   ```

   Call `scomp_list_hosts`, `scomp_list_sessions`, `scomp_open_terminal`,
   `scomp_send_input` with explicit `submit: true`, and
   `scomp_read_terminal`. Running `scomp-mcp serve` directly appears idle by
   design because it waits for MCP JSON-RPC on stdin.

## MCP client configuration

Configure the MCP host to launch stdio directly; do not place the access token
in the MCP configuration:

```json
{
  "mcpServers": {
    "scomp": {
      "command": "/absolute/path/to/scomp-mcp",
      "args": ["serve"]
    }
  }
}
```

`stdout` is reserved for MCP protocol frames. Diagnostics and login messages go
to `stderr`, without terminal contents or credentials.

## Tools

- `scomp_list_hosts`
- `scomp_list_sessions`
- `scomp_open_terminal`
- `scomp_read_terminal`
- `scomp_send_input`
- `scomp_close_terminal`

Text sent by `scomp_send_input` is not submitted unless `submit: true` is
explicit. Named keys include `enter`, `tab`, `escape`, `ctrl_c`, and arrow keys.
The server credential scope and the remote agent's `readonly`, `approved-only`,
or `full` mode are both enforced.

## Development

```sh
make test
make test-race
make build
```

The GitHub repository is private during the MVP validation period.

## License

Licensed under the **GNU Affero General Public License v3.0** — see
[`LICENSE`](LICENSE), matching `scomp-agent`.
