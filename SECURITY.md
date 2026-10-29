# Security policy

## Trust model

`scomp-mcp` gives a local MCP host access to existing tmux or GNU Screen
sessions on explicitly authorized remote hosts. It does not create a hidden
shell, and terminal input is interactive PTY delivery rather than a structured
command-execution API. Successful delivery does not prove execution,
completion, or an exit status.

Three controls limit access:

- the MCP credential is scoped to selected hosts and to host/read/write
  capabilities;
- `scomp-mcp` checks those scopes before opening or writing to a terminal;
- the remote agent's `readonly`, `approved-only`, or `full` mode remains the
  final input policy.

Anyone who can control the configured local MCP host may be able to invoke the
tools allowed by that credential. Treat the MCP host configuration and local
OS account as trusted.

## Terminal output and relay visibility

Remote terminal output is untrusted program data. It can contain misleading
text or prompt-injection attempts. MCP hosts and models must not treat it as
trusted instructions. The `until` marker wait matches caller-chosen text in
that untrusted output; a match is not evidence of command completion, success,
or an exit status.

Connections use HTTPS and WSS transport encryption, but terminal traffic is not
end-to-end encrypted from the scomp relay. The relay processes terminal content
in readable form and remains part of the trusted computing base. The client
does not persist terminal output locally.

## Protecting and revoking credentials

- The browser approval flow should be used only for requests you initiated.
- Approve only required hosts, scopes, and lifetime. Use
  `scomp-mcp login --read-only` when input is unnecessary.
- On typical Linux systems, the token is stored in
  `~/.config/scomp/mcp.json` with mode `0600`; on macOS, in
  `~/Library/Application Support/scomp/mcp.json`. Do not commit, print, copy into
  MCP configuration, or include it in support reports.
- `SCOMP_MCP_TOKEN` is intended for ephemeral environments. Environment
  variables may be visible to other local processes.
- `scomp-mcp logout` deletes the local copy but cannot invalidate copies made
  elsewhere. Revoke the credential from the signed-in scomp web Profile when a
  laptop, token, or MCP host may be compromised.
- Revocation and expiry reject future API requests and close affected active
  terminal streams.

## Reporting a vulnerability

Do not open a public issue for an unpatched vulnerability. Use the repository's
private GitHub Security Advisory reporting flow, or contact the repository
owner privately if that flow is unavailable. Include impact, reproduction
steps, and the affected version. Do not include real access tokens, device
codes, WebSocket tickets, terminal output, or user data.

You should receive an acknowledgement within 72 hours. Coordinated disclosure
timing will be agreed after the report is reproduced and a fix is available.

## Supported versions

Security fixes are made on the latest release line. Upgrade to the newest
published release before reporting behavior that may already have been fixed.
