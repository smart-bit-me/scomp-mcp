package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/smart-bit-me/scomp-mcp/internal/api"
	"github.com/smart-bit-me/scomp-mcp/internal/terminal"
)

func TestServerAdvertisesSixScopedTools(t *testing.T) {
	client, err := api.NewClient("http://localhost:1", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	server := New(client, terminal.NewManager(client, true), "test", Options{})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	clientSession, err := mcpClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	result, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"scomp_list_hosts": false, "scomp_list_sessions": false,
		"scomp_open_terminal": false, "scomp_read_terminal": false,
		"scomp_send_input": false, "scomp_close_terminal": false,
	}
	for _, tool := range result.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
		if tool.Name == "scomp_send_input" && (tool.Annotations == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint) {
			t.Fatalf("send input annotations = %+v", tool.Annotations)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing tool %s", name)
		}
	}
}

func resultJSON(t *testing.T, result *mcp.CallToolResult) (string, string) {
	t.Helper()
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("content blocks = %d", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content = %T", result.Content[0])
	}
	return string(structured), text.Text
}

func TestResultTextIsSummaryByDefault(t *testing.T) {
	f := newWireFixture(t, Options{})
	result := f.call("scomp_open_terminal", map[string]any{"host_uid": fakeHostUID, "session_id": "work"})
	structured, text := resultJSON(t, result)
	if !strings.Contains(structured, `"output":"user@gpu:~$ "`) {
		t.Fatalf("structured = %s", structured)
	}
	if text == structured || strings.Contains(text, "user@gpu") || !strings.Contains(text, "12 output bytes") {
		t.Fatalf("text = %q", text)
	}
}

func TestJSONTextRepeatsStructuredResult(t *testing.T) {
	f := newWireFixture(t, Options{JSONText: true})
	structured, text := resultJSON(t, f.call("scomp_list_hosts", map[string]any{}))
	if text != structured || !strings.Contains(text, fakeHostUID) {
		t.Fatalf("text = %q structured = %q", text, structured)
	}
}

func TestToolErrorsKeepErrorText(t *testing.T) {
	f := newWireFixture(t, Options{})
	result, err := f.session.CallTool(f.ctx, &mcp.CallToolParams{
		Name: "scomp_read_terminal", Arguments: map[string]any{"terminal_id": "missing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "terminal handle not found") {
		t.Fatalf("result = %+v", result)
	}
}

func TestReadResultOmitsIdentityAndDefaults(t *testing.T) {
	f := newWireFixture(t, Options{})
	view := f.terminalCall("scomp_open_terminal", map[string]any{"host_uid": fakeHostUID, "session_id": "work"})
	f.nextConn()
	structured, _ := resultJSON(t, f.call("scomp_read_terminal", map[string]any{
		"terminal_id": view.TerminalID, "cursor": view.Cursor, "generation": view.Generation,
	}))
	want := fmt.Sprintf(`{"cursor":%d,"generation":1,"output":""}`, view.Cursor)
	if structured != want {
		t.Fatalf("read = %s, want %s", structured, want)
	}

	structured, _ = resultJSON(t, f.call("scomp_read_terminal", map[string]any{
		"terminal_id": view.TerminalID, "cursor": view.Cursor, "generation": 7,
	}))
	if !strings.Contains(structured, `"resynced":true`) || !strings.Contains(structured, `"earliest_cursor":0`) || strings.Contains(structured, "truncated") {
		t.Fatalf("resynced read = %s", structured)
	}
}

func TestReadWaitSchemaMatchesMaximum(t *testing.T) {
	f := newWireFixture(t, Options{})
	tools, err := f.session.ListTools(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "scomp_read_terminal" {
			continue
		}
		schema, _ := json.Marshal(tool.InputSchema)
		if want := fmt.Sprintf("0 to %d", terminal.MaxWait.Milliseconds()); !strings.Contains(string(schema), want) {
			t.Fatalf("wait_ms schema %s does not mention %q", schema, want)
		}
		return
	}
	t.Fatal("scomp_read_terminal not advertised")
}
