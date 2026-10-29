package terminal

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHandleReadCursorAndTruncation(t *testing.T) {
	h := &handle{
		id: "terminal", hostUID: strings.Repeat("a", 32), sessionID: "session", mode: "full",
		connected: true, generation: 1, notify: make(chan struct{}),
	}
	h.append([]byte("hello"))
	first, err := h.read(context.Background(), 0, 0, 3, 0)
	if err != nil || first.Output != "hel" || first.Cursor != 3 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := h.read(context.Background(), first.Cursor, first.Generation, 10, 0)
	if err != nil || second.Output != "lo" || second.Cursor != 5 {
		t.Fatalf("second=%+v err=%v", second, err)
	}

	h.append([]byte(strings.Repeat("x", maxRetainedBytes+1)))
	truncated, err := h.read(context.Background(), 1, first.Generation, 10, 0)
	if err != nil || !truncated.Truncated || truncated.Earliest == 0 {
		t.Fatalf("truncated=%+v err=%v", truncated, err)
	}
}

func TestHandleReadWaitsForOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &handle{id: "terminal", connected: true, generation: 1, notify: make(chan struct{}), ctx: ctx}
	go func() {
		time.Sleep(10 * time.Millisecond)
		h.append([]byte("ready"))
	}()
	result, err := h.read(context.Background(), 0, 1, 10, time.Second)
	if err != nil || result.Output != "ready" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestInputValidation(t *testing.T) {
	if !validHostUID(strings.Repeat("a", 32)) || validHostUID(strings.Repeat("g", 32)) {
		t.Fatal("host UID validation")
	}
	if !safeSessionID("tmux-work") || safeSessionID("../session") {
		t.Fatal("session ID validation")
	}
}

func TestApprovedOnlyKeyPolicyMatchesAgent(t *testing.T) {
	for _, key := range []string{"enter", "tab", "escape", "ctrl_c"} {
		if !approvedOnlyKeys[key] {
			t.Errorf("expected %s to be approved", key)
		}
	}
	for _, key := range []string{"up", "down", "left", "right"} {
		if approvedOnlyKeys[key] {
			t.Errorf("expected %s to be rejected", key)
		}
	}
}

func TestHandleReadRejectsWaitAboveMaximum(t *testing.T) {
	h := &handle{connected: true, generation: 1, notify: make(chan struct{})}
	if _, err := h.read(context.Background(), 0, 1, 10, MaxWait+time.Millisecond); err == nil {
		t.Fatal("wait above maximum accepted")
	}
	if MaxWait > 55*time.Second {
		t.Fatalf("MaxWait %s leaves too little margin under 60-second MCP host tool timeouts", MaxWait)
	}
}
