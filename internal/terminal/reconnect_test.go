package terminal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/smart-bit-me/scomp-mcp/internal/api"
)

// fakeStreamRelay serves /api/ws-ticket and /ws. While refusing, /ws answers
// 403 as the relay does when the agent is offline.
type fakeStreamRelay struct {
	mu       sync.Mutex
	refusing bool
	conns    chan *websocket.Conn
}

func newReconnectHandle(t *testing.T, window time.Duration) (*handle, *fakeStreamRelay) {
	t.Helper()
	oldInitial, oldMax, oldWindow := reconnectInitial, reconnectMax, reconnectWindow
	reconnectInitial, reconnectMax, reconnectWindow = 20*time.Millisecond, 100*time.Millisecond, window
	t.Cleanup(func() { reconnectInitial, reconnectMax, reconnectWindow = oldInitial, oldMax, oldWindow })

	relay := &fakeStreamRelay{conns: make(chan *websocket.Conn, 4)}
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws-ticket", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"ticket": "ticket"})
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		relay.mu.Lock()
		refusing := relay.refusing
		relay.mu.Unlock()
		if refusing {
			http.Error(w, "uid not connected", http.StatusForbidden)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		relay.conns <- conn
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client, err := api.NewClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &handle{
		id: "terminal", hostUID: strings.Repeat("a", 32), sessionID: "work", mode: "full",
		cols: 80, rows: 24, api: client, ctx: ctx, cancel: cancel, notify: make(chan struct{}),
	}
	conn, err := h.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.conn, h.connected, h.generation = conn, true, 1
	go h.run(conn)
	return h, relay
}

func (r *fakeStreamRelay) next(t *testing.T) *websocket.Conn {
	t.Helper()
	select {
	case conn := <-r.conns:
		return conn
	case <-time.After(5 * time.Second):
		t.Fatal("no connection")
		return nil
	}
}

func (r *fakeStreamRelay) setRefusing(value bool) {
	r.mu.Lock()
	r.refusing = value
	r.mu.Unlock()
}

func closeWith(t *testing.T, conn *websocket.Conn, code int, text string) {
	t.Helper()
	conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, text))
	conn.Close()
}

func TestReconnectOutlastsAgentOutage(t *testing.T) {
	h, relay := newReconnectHandle(t, 3*time.Second)
	first := relay.next(t)
	relay.setRefusing(true)
	closeWith(t, first, websocket.CloseNormalClosure, "agent disconnected")

	time.Sleep(300 * time.Millisecond) // several refused attempts
	result, err := h.read(context.Background(), 0, 1, 100, 0, 0)
	if err != nil || result.Connected || !strings.Contains(result.Reason, "HTTP 403") || !strings.HasSuffix(result.Reason, "; reconnecting") {
		t.Fatalf("during outage: %+v err=%v", result, err)
	}

	// A waiting read does not return early just because the stream is down.
	done := make(chan Result, 1)
	go func() {
		result, _ := h.read(context.Background(), 0, 1, 100, 3*time.Second, 0)
		done <- result
	}()
	time.Sleep(100 * time.Millisecond)
	relay.setRefusing(false)
	relay.next(t)
	select {
	case result := <-done:
		if !result.Connected || !result.Resynced || result.Generation != 2 || result.Reason != "" {
			t.Fatalf("after reconnect: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read did not return after reconnect")
	}
}

func TestPolicyCloseStopsReconnecting(t *testing.T) {
	h, relay := newReconnectHandle(t, 3*time.Second)
	closeWith(t, relay.next(t), websocket.ClosePolicyViolation, "host removed")
	deadline := time.Now().Add(2 * time.Second)
	for {
		result, err := h.read(context.Background(), 0, 1, 100, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Connected {
			if result.Reason != "relay closed the stream: 1008 host removed" {
				t.Fatalf("reason = %q", result.Reason)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("still connected")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-relay.conns:
		t.Fatal("reconnected after a policy close")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestReconnectGivesUpAfterWindow(t *testing.T) {
	h, relay := newReconnectHandle(t, 150*time.Millisecond)
	relay.setRefusing(true)
	closeWith(t, relay.next(t), 1013, "slow consumer")
	time.Sleep(600 * time.Millisecond)
	start := time.Now()
	result, err := h.read(context.Background(), 0, 1, 100, time.Second, 0)
	if err != nil || result.Connected || !strings.HasSuffix(result.Reason, "; stopped retrying") || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestDisconnectReasonIsBoundedAndPrintable(t *testing.T) {
	reason, permanent := describeDisconnect(&websocket.CloseError{Code: 1013, Text: "slow\x1b[31m consumer\n" + strings.Repeat("x", 300)})
	if permanent || len(reason) > 160 || strings.ContainsAny(reason, "\x1b\n") {
		t.Fatalf("reason=%q permanent=%v", reason, permanent)
	}
}
