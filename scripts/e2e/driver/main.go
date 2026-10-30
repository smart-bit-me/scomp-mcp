// Drives scomp-mcp serve over stdio against a local relay and agent.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type view struct {
	TerminalID string `json:"terminal_id"`
	Generation uint64 `json:"generation"`
	Cursor     uint64 `json:"cursor"`
	Output     string `json:"output"`
	Resynced   bool   `json:"resynced"`
	Skipped    int    `json:"skipped_bytes"`
	Found      bool   `json:"marker_found"`
	Reason     string `json:"disconnect_reason"`
	Disc       bool   `json:"disconnected"`
	Connected  *bool  `json:"connected"`
}

var session *mcp.ClientSession
var ctx = context.Background()

func call(name string, args map[string]any) (view, *mcp.CallToolResult) {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		fail("%s: %v", name, err)
	}
	if result.IsError {
		fail("%s tool error: %v", name, result.Content[0].(*mcp.TextContent).Text)
	}
	encoded, _ := json.Marshal(result.StructuredContent)
	var v view
	json.Unmarshal(encoded, &v)
	return v, result
}

func fail(format string, args ...any) {
	fmt.Printf("FAIL: "+format+"\n", args...)
	os.Exit(1)
}

func step(format string, args ...any) { fmt.Printf("ok: "+format+"\n", args...) }

func main() {
	bin, config, mode := os.Args[1], os.Args[2], os.Args[3]
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "1"}, nil)
	cmd := exec.Command(bin, "serve", "--config", config)
	cmd.Stderr = os.Stderr
	var err error
	session, err = client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		fail("connect: %v", err)
	}
	defer session.Close()

	if mode == "revoked" {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "scomp_list_hosts", Arguments: map[string]any{}})
		if err != nil || !res.IsError {
			fail("revoked credential still works: %v %+v", err, res)
		}
		step("revoked credential rejected: %s", res.Content[0].(*mcp.TextContent).Text)
		return
	}

	var hosts struct {
		Hosts []struct {
			UID      string `json:"agent_uid"`
			Online   bool   `json:"online"`
			Sessions []struct {
				ID   string `json:"id"`
				Mode string `json:"mode"`
			} `json:"sessions"`
		} `json:"hosts"`
	}
	_, raw := call("scomp_list_hosts", map[string]any{})
	encoded, _ := json.Marshal(raw.StructuredContent)
	json.Unmarshal(encoded, &hosts)
	if len(hosts.Hosts) != 1 || !hosts.Hosts[0].Online {
		fail("hosts = %s", encoded)
	}
	host := hosts.Hosts[0].UID
	sessionID := ""
	for _, s := range hosts.Hosts[0].Sessions {
		if strings.Contains(s.ID, "e2e") || len(hosts.Hosts[0].Sessions) == 1 {
			sessionID = s.ID
		}
	}
	if sessionID == "" {
		fail("no e2e session in %s", encoded)
	}
	step("list_hosts: host online, session %s", sessionID)

	switch mode {
	case "main":
		runMain(host, sessionID)
	case "after-relay-restart":
		runReconnect(host, sessionID)
	}
}

func open(host, sessionID string, extra map[string]any) view {
	args := map[string]any{"host_uid": host, "session_id": sessionID}
	for k, v := range extra {
		args[k] = v
	}
	v, _ := call("scomp_open_terminal", args)
	return v
}

func read(v view, args map[string]any) view {
	base := map[string]any{"terminal_id": v.TerminalID, "cursor": v.Cursor, "generation": v.Generation}
	for k, val := range args {
		base[k] = val
	}
	next, _ := call("scomp_read_terminal", base)
	next.TerminalID = v.TerminalID
	return next
}

func send(v view, text string) {
	call("scomp_send_input", map[string]any{"terminal_id": v.TerminalID, "text": text, "submit": true})
}

func runMain(host, sessionID string) {
	v := open(host, sessionID, nil)
	step("open: generation %d, %d bytes", v.Generation, len(v.Output))

	send(v, "printf 'E2E_%s\\n' 0k1")
	r := read(v, map[string]any{"until": "E2E_0k1", "wait_ms": 10000, "limit": 512})
	if !r.Found {
		fail("marker not found: %+v", r)
	}
	step("until: marker found, %d bytes returned, %d skipped (echo did not match)", len(r.Output), r.Skipped)
	v = r

	// Fill more than the agent's 1 MiB ring so reattach replays a full ring
	// through chunked snapshots.
	big := "seq 1 250000"
	if paced := os.Getenv("E2E_PACED"); paced != "" {
		big = "for i in $(seq 1 45); do seq 1 5000; sleep 0.1; done"
	}
	send(v, big+"; printf 'E2E_%s\\n' big")
	r = read(v, map[string]any{"until": "E2E_big", "wait_ms": 50000, "limit": 256})
	if !r.Found {
		fail("big marker not found: %+v", r)
	}
	step("big output: marker found with %d bytes skipped, %d returned", r.Skipped, len(r.Output))
	v = r
	call("scomp_close_terminal", map[string]any{"terminal_id": v.TerminalID})

	start := time.Now()
	w := open(host, sessionID, nil)
	total := len(w.Output)
	for i := 0; i < 40 && !strings.Contains(w.Output, "E2E_big"); i++ {
		w = read(w, map[string]any{"limit": 65536, "wait_ms": 2000})
		total += len(w.Output)
	}
	if !strings.Contains(w.Output, "E2E_big") && total < 900000 {
		fail("full-ring replay incomplete: %d bytes", total)
	}
	step("reattach (earliest): full-ring replay %d bytes in %s, still connected", total, time.Since(start).Round(time.Millisecond))
	call("scomp_close_terminal", map[string]any{"terminal_id": w.TerminalID})

	t := open(host, sessionID, map[string]any{"start": "tail", "tail_bytes": 2048})
	if t.Skipped < 100000 || len(t.Output) > 2048 {
		fail("tail open: skipped %d, output %d", t.Skipped, len(t.Output))
	}
	step("reattach (start=tail): %d bytes, skipped %d", len(t.Output), t.Skipped)

	q := read(t, map[string]any{"wait_ms": 3000})
	if q.Disc || q.Resynced {
		fail("quiet read: %+v", q)
	}
	step("long poll after tail open: connected, %d bytes of live repaint", len(q.Output))
	os.WriteFile(os.Getenv("E2E_STATE"), []byte(t.TerminalID), 0o600)
	call("scomp_close_terminal", map[string]any{"terminal_id": t.TerminalID})
}

func runReconnect(host, sessionID string) {
	v := open(host, sessionID, map[string]any{"start": "tail"})
	step("open before relay restart: generation %d", v.Generation)
	os.WriteFile(os.Getenv("E2E_READY"), []byte("1"), 0o600)
	// The orchestrator restarts the relay now; keep long-polling.
	deadline := time.Now().Add(90 * time.Second)
	sawDown := false
	for time.Now().Before(deadline) {
		r := read(v, map[string]any{"wait_ms": 5000, "resync": "tail", "tail_bytes": 1024})
		if r.Disc {
			sawDown = true
			fmt.Printf("   disconnected: %s\n", r.Reason)
		}
		if r.Resynced {
			step("reconnected: generation %d -> %d, resynced, skipped %d (saw disconnect: %v)", v.Generation, r.Generation, r.Skipped, sawDown)
			send(r, "printf 'E2E_%s\\n' back")
			m := read(r, map[string]any{"until": "E2E_back", "wait_ms": 10000})
			if !m.Found {
				fail("input after reconnect: %+v", m)
			}
			step("input works after reconnect")
			call("scomp_close_terminal", map[string]any{"terminal_id": v.TerminalID})
			return
		}
		v.Cursor, v.Generation = r.Cursor, r.Generation
	}
	fail("no reconnect within 90 s")
}
