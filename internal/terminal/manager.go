package terminal

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/smart-bit-me/scomp-mcp/internal/api"
	"github.com/smart-bit-me/scomp-mcp/internal/presentation"
)

const (
	maxHandles       = 8
	maxRetainedBytes = 1 << 20
	maxReadBytes     = 64 << 10
	defaultReadBytes = 16 << 10
	maxInputBytes    = 8 << 10
	maxWait          = 20 * time.Second
	initialWait      = 3200 * time.Millisecond
)

type Result struct {
	TerminalID string `json:"terminal_id"`
	HostUID    string `json:"host_uid"`
	SessionID  string `json:"session_id"`
	Mode       string `json:"mode"`
	Generation uint64 `json:"generation"`
	Cursor     uint64 `json:"cursor"`
	Earliest   uint64 `json:"earliest_cursor"`
	Output     string `json:"output"`
	Truncated  bool   `json:"truncated"`
	Connected  bool   `json:"connected"`
	Resynced   bool   `json:"resynced"`
}

type Manager struct {
	api      *api.Client
	canWrite bool
	mu       sync.Mutex
	handles  map[string]*handle
}

type handle struct {
	id        string
	hostUID   string
	sessionID string
	mode      string
	cols      uint16
	rows      uint16
	api       *api.Client

	ctx    context.Context
	cancel context.CancelFunc

	mu         sync.Mutex
	conn       *websocket.Conn
	writeMu    sync.Mutex
	connected  bool
	generation uint64
	base       uint64
	data       []byte
	notify     chan struct{}
	normalizer presentation.Normalizer
}

func NewManager(client *api.Client, canWrite bool) *Manager {
	return &Manager{api: client, canWrite: canWrite, handles: make(map[string]*handle)}
}

func (m *Manager) Open(ctx context.Context, hostUID, sessionID string, cols, rows uint16) (Result, error) {
	if !validHostUID(hostUID) || !safeSessionID(sessionID) {
		return Result{}, errors.New("invalid host_uid or session_id")
	}
	if cols == 0 {
		cols = 120
	}
	if rows == 0 {
		rows = 40
	}
	sessions, err := m.api.Sessions(ctx, hostUID)
	if err != nil {
		return Result{}, err
	}
	var selected *api.Session
	for index := range sessions {
		if sessions[index].ID == sessionID {
			selected = &sessions[index]
			break
		}
	}
	if selected == nil {
		return Result{}, errors.New("session not found on the authorized host")
	}

	m.mu.Lock()
	if len(m.handles) >= maxHandles {
		m.mu.Unlock()
		return Result{}, fmt.Errorf("open terminal limit reached (%d)", maxHandles)
	}
	id, err := randomID()
	if err != nil {
		m.mu.Unlock()
		return Result{}, err
	}
	handleCtx, cancel := context.WithCancel(context.Background())
	h := &handle{
		id: id, hostUID: hostUID, sessionID: sessionID, mode: selected.Mode,
		cols: cols, rows: rows, api: m.api, ctx: handleCtx, cancel: cancel,
		notify: make(chan struct{}),
	}
	m.handles[id] = h
	m.mu.Unlock()

	connection, err := h.dial(ctx)
	if err != nil {
		m.remove(id, h)
		cancel()
		return Result{}, err
	}
	h.mu.Lock()
	h.conn = connection
	h.connected = true
	h.generation = 1
	h.signalLocked()
	h.mu.Unlock()
	go h.run(connection)

	result, err := h.read(ctx, 0, 0, defaultReadBytes, initialWait)
	if err != nil {
		m.Close(id)
		return Result{}, err
	}
	return result, nil
}

func (m *Manager) Read(ctx context.Context, terminalID string, cursor, generation uint64, limit int, wait time.Duration) (Result, error) {
	h, err := m.get(terminalID)
	if err != nil {
		return Result{}, err
	}
	return h.read(ctx, cursor, generation, limit, wait)
}

func (m *Manager) Send(terminalID, text, key string, submit bool) error {
	h, err := m.get(terminalID)
	if err != nil {
		return err
	}
	if !m.canWrite {
		return errors.New("credential does not have terminal:write scope")
	}
	if h.mode == "readonly" {
		return errors.New("remote session is readonly")
	}
	if (text == "") == (key == "") {
		return errors.New("provide exactly one of text or key")
	}
	var payload []byte
	if key != "" {
		value, ok := namedKeys[key]
		if !ok {
			return errors.New("unsupported key")
		}
		if h.mode == "approved-only" && !approvedOnlyKeys[key] {
			return errors.New("remote approved-only mode rejects this key")
		}
		payload = []byte(value)
	} else {
		if h.mode == "approved-only" {
			if !submit || (text != "y" && text != "n") {
				return errors.New("approved-only mode accepts only y/n with submit, Enter, Tab, Escape, or Ctrl+C")
			}
		}
		if !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
			return errors.New("text must be valid UTF-8 without NUL")
		}
		payload = []byte(text)
		if submit {
			payload = append(payload, '\r')
		}
	}
	if len(payload) == 0 || len(payload) > maxInputBytes {
		return fmt.Errorf("input must be between 1 and %d bytes", maxInputBytes)
	}
	return h.write(payload)
}

func (m *Manager) Close(terminalID string) error {
	m.mu.Lock()
	h := m.handles[terminalID]
	if h != nil {
		delete(m.handles, terminalID)
	}
	m.mu.Unlock()
	if h == nil {
		return errors.New("terminal handle not found")
	}
	h.cancel()
	h.mu.Lock()
	connection := h.conn
	h.conn = nil
	h.connected = false
	h.signalLocked()
	h.mu.Unlock()
	if connection != nil {
		connection.Close()
	}
	return nil
}

func (m *Manager) CloseAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.handles))
	for id := range m.handles {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Close(id)
	}
}

func (m *Manager) get(id string) (*handle, error) {
	m.mu.Lock()
	h := m.handles[id]
	m.mu.Unlock()
	if h == nil {
		return nil, errors.New("terminal handle not found")
	}
	return h, nil
}

func (m *Manager) remove(id string, expected *handle) {
	m.mu.Lock()
	if m.handles[id] == expected {
		delete(m.handles, id)
	}
	m.mu.Unlock()
}

func (h *handle) dial(ctx context.Context) (*websocket.Conn, error) {
	ticket, err := h.api.WSTicket(ctx)
	if err != nil {
		return nil, err
	}
	query := url.Values{
		"uid":     {h.hostUID},
		"session": {h.sessionID},
		"ticket":  {ticket},
		"cols":    {fmt.Sprint(h.cols)},
		"rows":    {fmt.Sprint(h.rows)},
	}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	connection, response, err := dialer.DialContext(ctx, h.api.WebSocketURL("/ws", query), nil)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("open terminal WebSocket: %w", err)
	}
	connection.SetReadLimit(1 << 20)
	return connection, nil
}

func (h *handle) run(connection *websocket.Conn) {
	current := connection
	for {
		for {
			messageType, data, err := current.ReadMessage()
			if err != nil {
				break
			}
			if messageType == websocket.BinaryMessage {
				h.append(h.normalizer.Feed(data))
			}
		}
		current.Close()
		h.mu.Lock()
		if h.conn == current {
			h.conn = nil
			h.connected = false
			h.signalLocked()
		}
		h.mu.Unlock()

		var next *websocket.Conn
		for _, delay := range []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second} {
			select {
			case <-h.ctx.Done():
				return
			case <-time.After(delay):
			}
			connectCtx, cancel := context.WithTimeout(h.ctx, 20*time.Second)
			next, _ = h.dial(connectCtx)
			cancel()
			if next != nil {
				break
			}
		}
		if next == nil {
			return
		}
		h.mu.Lock()
		h.conn = next
		h.connected = true
		h.generation++
		h.base += uint64(len(h.data))
		h.data = h.data[:0]
		h.normalizer.Reset()
		h.signalLocked()
		h.mu.Unlock()
		current = next
	}
}

func (h *handle) append(data []byte) {
	if len(data) == 0 {
		return
	}
	h.mu.Lock()
	h.data = append(h.data, data...)
	if excess := len(h.data) - maxRetainedBytes; excess > 0 {
		h.data = append([]byte(nil), h.data[excess:]...)
		h.base += uint64(excess)
	}
	h.signalLocked()
	h.mu.Unlock()
}

func (h *handle) read(ctx context.Context, cursor, generation uint64, limit int, wait time.Duration) (Result, error) {
	if limit == 0 {
		limit = defaultReadBytes
	}
	if limit < 1 || limit > maxReadBytes {
		return Result{}, fmt.Errorf("limit must be between 1 and %d", maxReadBytes)
	}
	if wait < 0 || wait > maxWait {
		return Result{}, fmt.Errorf("wait must be between 0 and %s", maxWait)
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		h.mu.Lock()
		end := h.base + uint64(len(h.data))
		resynced := generation != 0 && generation != h.generation
		truncated := cursor < h.base && cursor != 0
		start := cursor
		if start == 0 || start < h.base || start > end {
			start = h.base
			if cursor > end {
				resynced = true
			}
		}
		offset := int(start - h.base)
		available := len(h.data) - offset
		if available > limit {
			available = limit
		}
		output := append([]byte(nil), h.data[offset:offset+available]...)
		result := Result{
			TerminalID: h.id, HostUID: h.hostUID, SessionID: h.sessionID, Mode: h.mode,
			Generation: h.generation, Cursor: start + uint64(available), Earliest: h.base,
			Output: string(output), Truncated: truncated, Connected: h.connected, Resynced: resynced,
		}
		notify := h.notify
		h.mu.Unlock()
		if len(output) > 0 || wait == 0 || resynced || !result.Connected {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-h.ctx.Done():
			return result, nil
		case <-notify:
		case <-deadline.C:
			return result, nil
		}
	}
}

func (h *handle) write(payload []byte) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	h.mu.Lock()
	connection := h.conn
	connected := h.connected
	h.mu.Unlock()
	if !connected || connection == nil {
		return errors.New("terminal is disconnected")
	}
	connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := connection.WriteMessage(websocket.BinaryMessage, payload); err != nil {
		return fmt.Errorf("deliver terminal input: %w", err)
	}
	return nil
}

func (h *handle) signalLocked() {
	close(h.notify)
	h.notify = make(chan struct{})
}

var namedKeys = map[string]string{
	"enter":  "\r",
	"tab":    "\t",
	"escape": "\x1b",
	"ctrl_c": "\x03",
	"up":     "\x1b[A",
	"down":   "\x1b[B",
	"right":  "\x1b[C",
	"left":   "\x1b[D",
}

var approvedOnlyKeys = map[string]bool{
	"enter": true, "tab": true, "escape": true, "ctrl_c": true,
}

func validHostUID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func safeSessionID(value string) bool {
	if value == "" || len(value) > 256 || value == "." || value == ".." {
		return false
	}
	return !strings.ContainsAny(value, "/\\\x00")
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", value[:]), nil
}
