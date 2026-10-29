package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/smart-bit-me/scomp-mcp/internal/api"
	"github.com/smart-bit-me/scomp-mcp/internal/terminal"
)

// The wire-cost fixture drives the real MCP server against a fake relay and
// records the JSON-RPC bytes a client exchanges for common terminal workflows.
// Run `make wire-cost` to print the table.

const fakeHostUID = "0123456789abcdef0123456789abcdef"

type fakeRelay struct {
	server  *httptest.Server
	conns   chan *fakeConn
	mu      sync.Mutex
	history []byte
}

type fakeConn struct {
	ws     *websocket.Conn
	mu     sync.Mutex
	inputs chan []byte
}

func (c *fakeConn) write(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteMessage(websocket.BinaryMessage, data)
}

func (c *fakeConn) send(t *testing.T, data []byte) {
	t.Helper()
	if err := c.write(data); err != nil {
		t.Fatalf("fake relay write: %v", err)
	}
}

func newFakeRelay(t *testing.T) *fakeRelay {
	relay := &fakeRelay{conns: make(chan *fakeConn, 4)}
	session := api.Session{ID: "work", Cmd: "screen", Mode: "full", Active: true, Created: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/hosts", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]api.Host{{
			AgentUID: fakeHostUID, Hostname: "gpu-private-1", Online: true,
			Sessions: []api.Session{session}, RegisteredAt: session.Created, AgentVersion: "0.2.5",
		}})
	})
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]api.Session{session})
	})
	mux.HandleFunc("/api/ws-ticket", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"ticket": "ticket"})
	})
	upgrader := websocket.Upgrader{}
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn := &fakeConn{ws: ws, inputs: make(chan []byte, 16)}
		relay.mu.Lock()
		replay := append(append([]byte(nil), relay.history...), "user@gpu:~$ "...)
		relay.mu.Unlock()
		// Replay in frames, as a reattaching agent does.
		for len(replay) > 0 {
			frame := replay[:min(len(replay), 16<<10)]
			replay = replay[len(frame):]
			if conn.write(frame) != nil {
				ws.Close()
				return
			}
		}
		relay.conns <- conn
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				close(conn.inputs)
				return
			}
			conn.inputs <- data
		}
	})
	relay.server = httptest.NewServer(mux)
	t.Cleanup(relay.server.Close)
	return relay
}

// countingTransport records every JSON-RPC message crossing the client side.
type countingTransport struct {
	inner mcp.Transport
	stats *wireStats
}

func (t *countingTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &countingConn{Connection: conn, stats: t.stats}, nil
}

type countingConn struct {
	mcp.Connection
	stats *wireStats
}

func (c *countingConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.Connection.Read(ctx)
	if err == nil {
		c.stats.record(msg, false)
	}
	return msg, err
}

func (c *countingConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	c.stats.record(msg, true)
	return c.Connection.Write(ctx, msg)
}

type wireStats struct {
	mu sync.Mutex
	wireRow
}

type wireRow struct {
	name                                string
	calls, request, response, text, dup int
	model, output, skipped              int
	note                                string
}

func (s *wireStats) record(msg jsonrpc.Message, request bool) {
	encoded, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if request {
		s.request += len(encoded)
		if call, ok := msg.(*jsonrpc.Request); ok && call.Method == "tools/call" {
			s.calls++
		}
		return
	}
	s.response += len(encoded)
	response, ok := msg.(*jsonrpc.Response)
	if !ok || len(response.Result) == 0 {
		return
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	if json.Unmarshal(response.Result, &result) != nil {
		return
	}
	// Codex and Claude Code hand the model the compact structuredContent JSON
	// when present, otherwise the text blocks.
	var structured bytes.Buffer
	if len(result.StructuredContent) > 0 && json.Compact(&structured, result.StructuredContent) == nil {
		s.model += structured.Len()
	}
	for _, block := range result.Content {
		if block.Type != "text" {
			continue
		}
		s.text += len(block.Text)
		if structured.Len() > 0 && block.Text == structured.String() {
			s.dup += len(block.Text)
		}
		if structured.Len() == 0 {
			s.model += len(block.Text)
		}
	}
}

func (s *wireStats) take(name string) wireRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.wireRow
	row.name = name
	s.wireRow = wireRow{}
	return row
}

func (s *wireStats) addOutput(output string, skipped int) {
	s.mu.Lock()
	s.output += len(output)
	s.skipped += skipped
	s.mu.Unlock()
}

type termView struct {
	TerminalID string `json:"terminal_id"`
	Generation uint64 `json:"generation"`
	Cursor     uint64 `json:"cursor"`
	Output     string `json:"output"`
	Resynced   bool   `json:"resynced"`
	Skipped    int    `json:"skipped_bytes"`
	Found      bool   `json:"marker_found"`
}

type wireFixture struct {
	t       *testing.T
	ctx     context.Context
	relay   *fakeRelay
	stats   *wireStats
	session *mcp.ClientSession
}

func newWireFixture(t *testing.T, options Options) *wireFixture {
	ctx := context.Background()
	relay := newFakeRelay(t)
	client, err := api.NewClient(relay.server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	terminals := terminal.NewManager(client, true)
	t.Cleanup(terminals.CloseAll)
	server := New(client, terminals, "test", options)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })
	stats := &wireStats{}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "wire-cost", Version: "test"}, nil)
	session, err := mcpClient.Connect(ctx, &countingTransport{inner: clientTransport, stats: stats}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return &wireFixture{t: t, ctx: ctx, relay: relay, stats: stats, session: session}
}

func (f *wireFixture) call(name string, args map[string]any) *mcp.CallToolResult {
	f.t.Helper()
	result, err := f.session.CallTool(f.ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		f.t.Fatalf("%s: %v", name, err)
	}
	if result.IsError {
		f.t.Fatalf("%s returned tool error: %+v", name, result.Content)
	}
	return result
}

func (f *wireFixture) terminalCall(name string, args map[string]any) termView {
	f.t.Helper()
	result := f.call(name, args)
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		f.t.Fatal(err)
	}
	var view termView
	if err := json.Unmarshal(encoded, &view); err != nil {
		f.t.Fatal(err)
	}
	f.stats.addOutput(view.Output, view.Skipped)
	return view
}

func (f *wireFixture) read(view termView, limit, waitMS int) termView {
	f.t.Helper()
	args := map[string]any{"terminal_id": view.TerminalID, "cursor": view.Cursor, "generation": view.Generation, "wait_ms": waitMS}
	if limit > 0 {
		args["limit"] = limit
	}
	next := f.terminalCall("scomp_read_terminal", args)
	next.TerminalID = view.TerminalID
	return next
}

func (f *wireFixture) nextConn() *fakeConn {
	f.t.Helper()
	select {
	case conn := <-f.relay.conns:
		return conn
	case <-time.After(10 * time.Second):
		f.t.Fatal("terminal did not connect to the fake relay")
		return nil
	}
}

func historyBlock(size int) []byte {
	var out bytes.Buffer
	for line := 0; out.Len() < size; line++ {
		fmt.Fprintf(&out, "[%06d] epoch 3 shard %04d: wrote 4096 records, checksum ok\r\n", line, line%977)
	}
	return out.Bytes()[:size]
}

func compilerError(size int) []byte {
	var out bytes.Buffer
	for line := 0; out.Len() < size; line++ {
		fmt.Fprintf(&out, "src/index/build.rs:%d:17: error[E0308]: mismatched types: expected `u64`, found `usize`\n", 100+line)
	}
	return out.Bytes()[:size]
}

func runWireCost(t *testing.T) []wireRow {
	f := newWireFixture(t, Options{})
	if _, err := f.session.ListTools(f.ctx, nil); err != nil {
		t.Fatal(err)
	}
	rows := []wireRow{f.stats.take("session setup (initialize + tools/list)")}

	f.call("scomp_list_hosts", map[string]any{})
	rows = append(rows, f.stats.take("list one host"))

	view := f.terminalCall("scomp_open_terminal", map[string]any{"host_uid": fakeHostUID, "session_id": "work"})
	conn := f.nextConn()
	rows = append(rows, f.stats.take("open terminal"))

	f.call("scomp_send_input", map[string]any{"terminal_id": view.TerminalID, "text": "printf 'ok\\n'", "submit": true})
	if input := <-conn.inputs; string(input) != "printf 'ok\\n'\r" {
		t.Fatalf("relay input = %q", input)
	}
	conn.send(t, []byte("printf 'ok\\n'\r\nok\r\nuser@gpu:~$ "))
	for !strings.Contains(view.Output, "ok\nuser@gpu") {
		view = f.read(view, 0, 2000)
	}
	rows = append(rows, f.stats.take("short command + read"))

	for range 10 {
		if view = f.read(view, 0, 20); view.Output != "" {
			t.Fatalf("empty poll returned %q", view.Output)
		}
	}
	row := f.stats.take("ten empty long polls")
	row.note = fmt.Sprintf("10 min quiet = %d polls", (10*time.Minute+terminal.MaxWait-1)/terminal.MaxWait)
	rows = append(rows, row)

	conn.send(t, compilerError(16<<10))
	received := 0
	for received < 16<<10 {
		view = f.read(view, 0, 2000)
		received += len(view.Output)
	}
	rows = append(rows, f.stats.take("16 KiB compiler error"))

	// A build whose log ends with a caller-chosen marker. The echoed command
	// contains the format string, not the marker itself.
	build := func(marker string) {
		command := "make index; printf 'DONE_%s\\n' " + marker
		f.call("scomp_send_input", map[string]any{"terminal_id": view.TerminalID, "text": command, "submit": true})
		<-conn.inputs
		conn.send(t, []byte(command+"\r\n"))
		conn.send(t, compilerError(16<<10))
		conn.send(t, []byte("\nDONE_"+marker+"\r\nuser@gpu:~$ "))
		time.Sleep(50 * time.Millisecond)
	}
	f.stats.take("")
	build("a1")
	for !strings.Contains(view.Output, "DONE_a1") {
		view = f.read(view, 0, 2000)
	}
	rows = append(rows, f.stats.take("16 KiB build + marker, plain reads"))
	view = f.read(view, 0, 0) // drain the prompt
	f.stats.take("")
	build("b2")
	args := map[string]any{"terminal_id": view.TerminalID, "cursor": view.Cursor, "generation": view.Generation, "wait_ms": 2000, "until": "DONE_b2", "limit": 2048}
	marked := f.terminalCall("scomp_read_terminal", args)
	if !marked.Found || !strings.HasSuffix(marked.Output, "DONE_b2") {
		t.Fatalf("until read = %+v", marked)
	}
	marked.TerminalID = view.TerminalID
	view = marked
	rows = append(rows, f.stats.take("16 KiB build + marker, until (2 KiB)"))
	view = f.read(view, 0, 0)
	f.stats.take("")

	// Accumulate 100 KiB of history, then drop the WebSocket. The fake relay
	// replays retained scrollback on reattach, as Screen did during dogfood.
	history := historyBlock(100 << 10)
	f.relay.mu.Lock()
	f.relay.history = history
	f.relay.mu.Unlock()
	conn.send(t, history)
	normalized := len(history) - bytes.Count(history, []byte("\r\n"))
	for received = 0; received < normalized; received += len(view.Output) {
		view = f.read(view, 65536, 2000)
	}
	f.stats.take("")
	conn.ws.Close()
	conn = f.nextConn()
	conn.send(t, []byte("\r\nRECONNECT_MARKER\r\n"))
	time.Sleep(200 * time.Millisecond)
	stale := 0
	for {
		view = f.read(view, 65536, 2000)
		if index := strings.Index(view.Output, "RECONNECT_MARKER"); index >= 0 {
			stale += index
			break
		}
		stale += len(view.Output)
	}
	row = f.stats.take("reconnect after 100 KiB history")
	row.note = fmt.Sprintf("%d stale bytes before new output", stale)
	rows = append(rows, row)

	conn.ws.Close()
	conn = f.nextConn()
	conn.send(t, []byte("\r\nTAIL_MARKER\r\n"))
	time.Sleep(200 * time.Millisecond)
	args = map[string]any{"terminal_id": view.TerminalID, "cursor": view.Cursor, "generation": view.Generation, "resync": "tail"}
	tailView := f.terminalCall("scomp_read_terminal", args)
	if !tailView.Resynced || !strings.Contains(tailView.Output, "TAIL_MARKER") || tailView.Skipped == 0 {
		t.Fatalf("tail resync = %+v", tailView)
	}
	rows = append(rows, f.stats.take("reconnect, resync=tail (4 KiB)"))
	f.call("scomp_close_terminal", map[string]any{"terminal_id": view.TerminalID})
	f.stats.take("")

	// Open a session whose agent replays 100 KiB of scrollback, as Screen did.
	view = f.terminalCall("scomp_open_terminal", map[string]any{"host_uid": fakeHostUID, "session_id": "work"})
	f.nextConn()
	for !strings.Contains(view.Output, "user@gpu:~$ ") {
		view = f.read(view, 65536, 2000)
	}
	rows = append(rows, f.stats.take("open with 100 KiB replay"))
	f.call("scomp_close_terminal", map[string]any{"terminal_id": view.TerminalID})
	f.stats.take("")

	view = f.terminalCall("scomp_open_terminal", map[string]any{"host_uid": fakeHostUID, "session_id": "work", "start": "tail"})
	f.nextConn()
	if !strings.HasSuffix(view.Output, "user@gpu:~$ ") || view.Skipped == 0 {
		t.Fatalf("tail open = %+v", view)
	}
	rows = append(rows, f.stats.take("open with 100 KiB replay, start=tail"))
	f.call("scomp_close_terminal", map[string]any{"terminal_id": view.TerminalID})
	return rows
}

func formatWireCost(rows []wireRow) string {
	var out strings.Builder
	w := tabwriter.NewWriter(&out, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "scenario\tcalls\treq B\tresp B\ttext B\tdup B\tmodel B\t~tok\toutput B\tskipped B\t")
	var total wireRow
	for _, row := range rows {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t  %s\n", row.name, row.calls, row.request, row.response,
			row.text, row.dup, row.model, row.model/4, row.output, row.skipped, row.note)
		total.calls += row.calls
		total.request += row.request
		total.response += row.response
		total.text += row.text
		total.dup += row.dup
		total.model += row.model
		total.output += row.output
		total.skipped += row.skipped
	}
	fmt.Fprintf(w, "total\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t\n", total.calls, total.request, total.response,
		total.text, total.dup, total.model, total.model/4, total.output, total.skipped)
	w.Flush()
	return out.String()
}

// TestWireCost prints request/response bytes per workflow. "model B" is the
// result JSON that Codex and Claude Code pass to the model; ~tok is model B/4.
func TestWireCost(t *testing.T) {
	rows := runWireCost(t)
	t.Log("\n" + formatWireCost(rows))
}
