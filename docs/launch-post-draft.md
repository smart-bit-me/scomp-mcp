# Launch post draft

Status: draft for Hacker News or Reddit; revise after conclusions from the
technical review.

Related review: [Scomp MCP review — 2026-10-29](mcp-review-2026-10-29.md).

## Possible titles

- Show HN: Scomp MCP — let a local coding agent use tmux on a private host
- I built an MCP bridge to tmux/Screen sessions on private hosts with no SSH route

## Draft

I have a GPU EC2 instance inside a private VPC. It has no public IP, and my
laptop has no VPN route into that network, so SSH is simply unavailable.

The instance already runs the Scomp agent, which makes an outbound WebSocket
connection to a relay. I built `scomp-mcp` so a local MCP-capable coding agent
can use that same path:

```text
coding agent -> local stdio MCP -> HTTPS/WSS relay
             -> outbound agent connection -> tmux/Screen
```

No inbound port, public IP, SSH credential, or security-group change is needed.

I dogfooded it on a real task. From my laptop, Codex selected an existing Screen
session by name, checked and installed build dependencies, generated 100
million public test records, built and verified a 3.2 GB index, and ran a
ten-minute CUDA benchmark on an NVIDIA L4.

The first run exposed a controller bug in the project being tested: its duration
watchdog started before the worker finished loading the large index. The coding
agent diagnosed it, patched and tested the local repository, pushed the fix,
pulled it through the private-host terminal, rebuilt, and repeated the test
successfully. The final run checked 2.256 billion candidates in 600 seconds at
3.759 million candidates per second.

The important design choice is that this is terminal control, not a disguised
remote-execution API:

- The user manually creates the tmux or Screen session.
- The MCP client can attach only to visible authorized sessions.
- Browser approval selects exact hosts, read/write scopes, and credential
  lifetime.
- The remote agent's `readonly`, `approved-only`, or `full` mode remains the
  final input gate.
- Sending input means only "bytes were delivered." It does not invent an exit
  status or claim that a command completed.
- Terminal output is untrusted data and may contain prompt injection.

The relay is part of the trusted computing base: transport uses TLS, but
terminal traffic is not end-to-end encrypted from the relay. I would rather
state that limitation clearly than imply SSH-like properties it does not have.

The current MCP exposes six tools: list hosts, list sessions, open a terminal,
read bounded cursor-based output, send explicit text or named keys, and close
the local terminal handle.

Dogfooding also showed several token-efficiency opportunities. Typed MCP results
currently appear both as textual JSON and structured content; empty reads
repeat invariant IDs; the 20-second maximum wait causes too many polling calls;
and reconnects may replay a large retained buffer. I am considering compact
delta responses, longer waits, bounded literal-marker waiting, and explicit
tail/resync modes.

I am interested in feedback on two questions:

1. Should structured remote execution remain a completely separate protocol
   from interactive terminal control?
2. Which token-saving operations would be most useful without hiding important
   PTY state?

Source: https://github.com/smart-bit-me/scomp-mcp

Architecture and setup: https://scomp.me/en/mcp/
