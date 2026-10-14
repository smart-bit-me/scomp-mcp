package mcpserver

import (
	"context"
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
	server := New(client, terminal.NewManager(client, true), "test")
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
