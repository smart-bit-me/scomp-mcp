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
	first, err := h.read(context.Background(), 0, 0, 3, 0, 0)
	if err != nil || first.Output != "hel" || first.Cursor != 3 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := h.read(context.Background(), first.Cursor, first.Generation, 10, 0, 0)
	if err != nil || second.Output != "lo" || second.Cursor != 5 {
		t.Fatalf("second=%+v err=%v", second, err)
	}

	h.append([]byte(strings.Repeat("x", maxRetainedBytes+1)))
	truncated, err := h.read(context.Background(), 1, first.Generation, 10, 0, 0)
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
	result, err := h.read(context.Background(), 0, 1, 10, time.Second, 0)
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
	if _, err := h.read(context.Background(), 0, 1, 10, MaxWait+time.Millisecond, 0); err == nil {
		t.Fatal("wait above maximum accepted")
	}
	if MaxWait > 55*time.Second {
		t.Fatalf("MaxWait %s leaves too little margin under 60-second MCP host tool timeouts", MaxWait)
	}
}

func newTestHandle(t *testing.T, generation uint64, data string) *handle {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &handle{id: "terminal", connected: true, generation: generation, notify: make(chan struct{}), ctx: ctx}
	h.append([]byte(data))
	return h
}

func TestTailResyncSkipsAndReports(t *testing.T) {
	h := newTestHandle(t, 2, strings.Repeat("x", 90)+"0123456789")
	result, err := h.read(context.Background(), 500, 1, 100, 0, 10)
	if err != nil || !result.Resynced || result.Output != "0123456789" || result.Skipped != 90 || result.Cursor != 100 {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	result, err = h.read(context.Background(), 500, 1, 100, 0, 0)
	if err != nil || len(result.Output) != 100 || result.Skipped != 0 {
		t.Fatalf("earliest result=%+v err=%v", result, err)
	}
}

func TestTailIgnoredForValidCursor(t *testing.T) {
	h := newTestHandle(t, 1, strings.Repeat("x", 100))
	result, err := h.read(context.Background(), 20, 1, 100, 0, 10)
	if err != nil || len(result.Output) != 80 || result.Skipped != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestTailDoesNotSplitUTF8(t *testing.T) {
	h := newTestHandle(t, 1, "abcé")
	result, err := h.read(context.Background(), 0, 0, 100, 0, 1)
	if err != nil || result.Output != "" || result.Skipped != 5 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	result, err = h.read(context.Background(), 0, 0, 100, 0, 2)
	if err != nil || result.Output != "é" || result.Skipped != 3 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestTailWaitsForReplayToSettle(t *testing.T) {
	h := newTestHandle(t, 1, "")
	go func() {
		for range 5 {
			time.Sleep(50 * time.Millisecond)
			h.append([]byte("old-scrollback\n"))
		}
		h.append([]byte("$ "))
	}()
	result, err := h.read(context.Background(), 0, 0, 100, 0, 2)
	if err != nil || result.Output != "$ " || result.Skipped != 75 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestTailBytesValidation(t *testing.T) {
	for _, tc := range []struct {
		policy  string
		n, want int
		ok      bool
	}{
		{"", 0, 0, true}, {"earliest", 0, 0, true}, {"tail", 0, defaultTailBytes, true},
		{"tail", 10, 10, true}, {"tail", maxReadBytes + 1, 0, false}, {"earliest", 10, 0, false},
		{"latest", 0, 0, false},
	} {
		got, err := TailBytes(tc.policy, tc.n)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("TailBytes(%q, %d) = %d, %v", tc.policy, tc.n, got, err)
		}
	}
}

func TestReadUntilReturnsBoundedSuffix(t *testing.T) {
	h := newTestHandle(t, 1, "echo DONE_%s x\nlog line one\nlog line two\nDONE_x\n$ ")
	result, err := h.readUntil(context.Background(), 0, 1, 10, 0, "DONE_x")
	if err != nil || !result.Found || result.Output != "two\nDONE_x" || result.Cursor != 47 || result.Skipped != 37 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	next, err := h.read(context.Background(), result.Cursor, 1, 100, 0, 0)
	if err != nil || next.Output != "\n$ " {
		t.Fatalf("next=%+v err=%v", next, err)
	}
}

func TestReadUntilWaitsForSplitMarker(t *testing.T) {
	h := newTestHandle(t, 1, "building\nDON")
	go func() {
		time.Sleep(20 * time.Millisecond)
		h.append([]byte("E_7f\n"))
	}()
	result, err := h.readUntil(context.Background(), 0, 1, 100, time.Second, "DONE_7f")
	if err != nil || !result.Found || result.Output != "building\nDONE_7f" || result.Skipped != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestReadUntilTimeoutKeepsCursor(t *testing.T) {
	h := newTestHandle(t, 1, "0123456789")
	result, err := h.readUntil(context.Background(), 4, 1, 100, 20*time.Millisecond, "DONE")
	if err != nil || result.Found || result.Output != "" || result.Cursor != 4 || result.Pending != 6 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestReadUntilReturnsOnResync(t *testing.T) {
	h := newTestHandle(t, 2, "DONE")
	result, err := h.readUntil(context.Background(), 0, 1, 100, time.Second, "DONE")
	if err != nil || !result.Resynced || result.Found || result.Cursor != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestReadUntilRejectsInvalidMarkers(t *testing.T) {
	h := newTestHandle(t, 1, "")
	for _, marker := range []string{"", "a\nb", "\x1b[0m", strings.Repeat("x", maxMarkerBytes+1), "\xff"} {
		if _, err := h.readUntil(context.Background(), 0, 1, 100, 0, marker); err == nil {
			t.Errorf("marker %q accepted", marker)
		}
	}
}
