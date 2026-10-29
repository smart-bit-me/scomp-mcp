# Scomp MCP review — 2026-10-29

Status: working review; conclusions and implementation decisions are deferred.

## Scope

This review covers `scomp-mcp` v0.1.1 at commit `21da444`, its public MCP page,
and a long live workflow through scomp agent v0.2.5. It focuses on the trust
model, terminal behavior, model-facing output, and token cost. It is not a
security audit of the relay or remote agent.

The code review included:

- the six MCP tools and their schemas;
- credential storage and device authorization;
- HTTP and ticket-authenticated WebSocket clients;
- terminal handle, reconnect, cursor, and retained-buffer behavior;
- terminal-output normalization; and
- normal and race-enabled Go test suites.

Both `go test ./...` and `go test -race ./...` passed. The repository was clean
apart from an existing untracked `.idea/` directory, which was not inspected or
modified.

## Live case studied

The test host was an EC2 GPU instance in a private VPC. It had no public IP and
the laptop had no VPN route into the VPC, so SSH was unavailable. The existing
outbound scomp-agent connection was the only terminal path used.

A local coding agent used `scomp-mcp` to select an existing GNU Screen session
by name, inspect and prepare the host, generate a 100-million-record public
dataset, build and verify a 3.2 GB index, run an NVIDIA L4 workload, diagnose a
duration-watchdog defect in a separate project, push a tested fix, pull and
rebuild it on the private host, and repeat the workload successfully.

This was a useful test because it combined:

- many short command/response interactions;
- quiet operations lasting from tens of seconds to ten minutes;
- a reconnect and cursor-generation change;
- large historical Screen scrollback;
- package, Git, build, CPU, and GPU commands; and
- strict handling of credentials and generated secret material.

## Architecture and trust-model observations

The architectural boundary is clear:

```text
local MCP host
    -> scomp-mcp over stdio
    -> relay API over HTTPS and ticket-authenticated WSS
    -> existing outbound scomp-agent connection
    -> user-created tmux or Screen session
```

Strong properties:

1. The MCP client cannot silently create an undisclosed remote shell. It can
   attach only to sessions reported by the authorized host.
2. Browser approval restricts credentials by exact hosts, scopes, and finite
   lifetime. Credentials are independently revocable.
3. Read access and terminal-write access are separate. The remote session's
   `readonly`, `approved-only`, or `full` mode remains an additional gate.
4. Newline submission is explicit. Sending input means only that bytes were
   delivered to a PTY; it does not claim execution, completion, or exit status.
5. Handles, input frames, retained output, individual reads, and wait times are
   bounded.
6. Reconnects increment a generation and reset the cursor space, allowing the
   client to detect that its previous cursor no longer names the same stream.
7. Documentation says clearly that terminal output is untrusted and that the
   relay can read terminal traffic. TLS transport is not presented as
   end-to-end encryption.

The distinction between interactive terminal control and a deterministic
remote-execution protocol should remain explicit. A structured executor, if
ever added, would need a separate protocol and security decision.

## Findings and opportunities

### 1. Structured results are duplicated as text

Priority: high.

Every typed tool currently returns its value in `structuredContent`. Because
the Go MCP SDK's typed `AddTool` helper also fills an unset `content` field with
the JSON representation of that value, clients receive substantially the same
payload twice.

This was visible on every live call, including empty terminal reads. It is the
clearest immediate token-cost reduction.

Options to test:

- explicitly supply a short compatibility `content` message while retaining
  the structured value;
- use a low-level handler and return only the representation required by the
  supported MCP clients; or
- negotiate behavior after verifying how Codex and other intended clients
  consume structured-only results.

Add wire-level tests before changing this. Compatibility is more important
than saving a small number of bytes in isolation.

### 2. Empty reads repeat invariant metadata

Priority: high.

`scomp_read_terminal` repeats `terminal_id`, `host_uid`, `session_id`, `mode`,
and default-valued flags on every response. The caller already has most of this
from `scomp_open_terminal` and supplied `terminal_id` in the request.

Consider separate response types:

- `OpenResult`: complete identity and initial snapshot;
- `ReadResult`: cursor, generation, output, and only changed/non-default state.

Using `omitempty` for false flags is another small saving, provided absence has
an unambiguous meaning.

### 3. The 20-second long-poll limit causes excessive calls

Priority: high.

Quiet builds and a ten-minute workload required repeated empty reads solely to
keep observing the session. Each read paid tool-call and response overhead even
when no bytes arrived.

Evaluate a 50–60 second maximum wait. It remains short enough for interactive
status reporting while reducing empty polling calls by roughly two-thirds.
Client and reverse-proxy timeouts must be tested before changing the default or
maximum.

### 4. Reconnect resynchronization can replay too much history

Priority: high.

During the live workflow, the WebSocket generation changed. The following read
correctly reported `resynced`, but recovering from the earliest retained cursor
replayed tens of kilobytes of old Screen output. Much of it had already been
seen before the reconnect.

Possible additions:

- a resync policy such as `from: earliest|latest|tail`;
- a bounded `tail_bytes` or `tail_lines` snapshot;
- a compact resync result that reports discarded byte count; and
- documentation showing the safest model behavior after generation changes.

The default must not silently hide output. Any tail/latest behavior should be
explicit and report what was skipped.

### 5. A bounded literal-marker wait would reduce orchestration overhead

Priority: medium.

Many workflows append a unique literal marker to a shell command and wait for
it. A tool that waits for a literal byte sequence, advances the cursor, and
returns a bounded suffix could replace multiple read calls.

This must preserve the current semantics: finding a marker in untrusted PTY
output does not independently prove which program printed it or that a command
succeeded. The marker is a caller convention, not a remote-execution guarantee.

### 6. Terminal echo and visual rendering add noise

Priority: medium.

Screen often echoed the entire submitted shell line. Long one-line commands
were wrapped and partially repainted, consuming tokens before the useful
result. The current normalizer removes escape/control sequences but is not a
terminal emulator, so cursor movement and overprinting cannot always be
represented as clean logical text.

Options include:

- an explicit best-effort `elide_sent_input` mode;
- returning a bounded screen snapshot in addition to the byte stream;
- server-side `tail_lines`; or
- encouraging short remote scripts for complex workflows.

Input elision must be opt-in and must never remove similar output produced by
the remote program without reporting that filtering occurred.

### 7. Discovery can require redundant calls

Priority: medium/low.

`scomp_list_hosts` already includes sessions, but clients may then call
`scomp_list_sessions` before opening one. For the common case—one known host
and one named session—this adds a round trip and repeats metadata.

Potential approaches:

- document that a fresh session embedded in `list_hosts` can be opened
  directly;
- add an exact `open_terminal_by_name` operation; or
- allow `scomp_open_terminal` to accept a mutually exclusive stable selector.

Exact matching and ambiguity errors are important. The server should never
silently choose among multiple hosts or sessions.

### 8. Control-sequence coverage and website wording should agree

Priority: medium.

The normalizer has explicit states for CSI and OSC sequences and handles
incremental UTF-8, CR/LF normalization, common controls, OSC BEL termination,
and OSC ST termination. It is not a full ECMA-48 parser and does not explicitly
consume every string-control family such as DCS, APC, PM, or SOS.

Either extend the parser and tests or narrow the website statement that ANSI,
OSC, clipboard, color, and "other control sequences" are removed. This is an
output-hygiene issue, not a prompt-injection defense: readable terminal text
must still be treated as untrusted.

### 9. Repeated safety prose may contribute schema overhead

Priority: low, pending measurement.

The global server instructions appropriately emphasize untrusted output and PTY
semantics. Some MCP hosts may repeat those instructions alongside every tool
description. Measure the actual schema context before shortening anything;
safety language should not be removed based on assumption alone.

### 10. Documentation has one stale repository-status sentence

Priority: low.

The README says the GitHub repository is private during MVP validation, while
the website and release links present a public repository. Resolve the wording
before using the launch draft.

## Proposed token-cost benchmark

Do not optimize from anecdotes alone. Add a benchmark or integration fixture
that records MCP wire bytes and an approximate model-token count for:

1. listing one host with one session;
2. opening a terminal with a small prompt snapshot;
3. sending a short command and reading its result;
4. ten empty long polls;
5. reading a 16 KiB compiler error;
6. reconnecting after 100 KiB of retained history; and
7. waiting for a caller-supplied literal marker.

Record request bytes, response bytes, duplicated bytes, call count, time to
useful output, and output lost or deliberately skipped. Compare proposed
formats against the current version for both Codex and at least one other MCP
host.

## Suggested implementation order

1. Add the wire/token benchmark and compatibility fixtures.
2. Remove or sharply reduce text/structured duplication.
3. Introduce compact read responses.
4. Test and raise the long-poll maximum.
5. Add explicit reconnect tail/resync behavior.
6. Prototype literal-marker waiting.
7. Improve control-sequence coverage and visual terminal handling.
8. Re-evaluate discovery/open convenience after measurements.

## Questions intentionally left open

- Which MCP clients require textual `content` when `structuredContent` exists?
- Should a terminal handle default to byte-stream recovery or a visual snapshot
  after reconnect?
- Is literal-marker waiting sufficiently useful without encouraging clients to
  mistake it for deterministic execution?
- Should a future structured executor be a different binary, credential scope,
  and remote protocol?
- Is relay-readable terminal traffic acceptable for the intended users, or is
  end-to-end encryption required before broader adoption?
- Which optimizations materially reduce billed tokens rather than just wire
  bytes for the target MCP hosts?

No final product or protocol conclusions are made in this document.
