package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// testPtyServer wires a Server around one hand-built session backed by a real
// PTY pair, and returns the ws:// URL of its terminal socket plus the tty side
// (what the "agent" would read).
func testPtyServer(t *testing.T) (*Session, *httptest.Server, string) {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	t.Cleanup(func() { ptmx.Close(); tty.Close() })

	sess := &Session{
		ID:      "test-1",
		ring:    newRingBuffer(1 << 16),
		clients: map[chan []byte]bool{},
		ptmx:    ptmx,
	}
	sess.ring.Write([]byte("previous output\r\n"))

	srv := &Server{
		mgr: &Manager{sessions: map[string]*Session{"test-1": sess}},
		hub: newHub(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/pty/{id}", srv.handlePtyWS)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	return sess, ts, "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/pty/test-1"
}

func clientCount(s *Session) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

// A client that attaches gets the scrollback ring replayed, and its keystrokes
// reach the PTY — this is the path a reconnect re-runs, so it has to hold.
func TestPtyWSReplaysRingAndForwardsInput(t *testing.T) {
	sess, _, url := testPtyServer(t)

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, snap, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if !strings.Contains(string(snap), "previous output") {
		t.Errorf("attach did not replay the ring, got %q", snap)
	}

	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("hello")); err != nil {
		t.Fatalf("write input: %v", err)
	}
	buf := make([]byte, 64)
	sess.ptmx.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := sess.ptmx.Read(buf) // echoed back by the tty line discipline
	if err != nil {
		t.Fatalf("read from pty: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "hello") {
		t.Errorf("keystrokes did not reach the PTY, got %q", buf[:n])
	}
}

// A peer that disappears must release its client slot. Before the keepalive
// work the writer goroutine returned quietly on failure and left the client
// registered, so every suspend/resume leaked one.
func TestPtyWSReleasesClientWhenPeerVanishes(t *testing.T) {
	sess, _, url := testPtyServer(t)

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for clientCount(sess) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := clientCount(sess); got != 1 {
		t.Fatalf("client count after attach = %d, want 1", got)
	}

	conn.UnderlyingConn().Close() // yank the TCP connection, no close frame

	deadline = time.Now().Add(5 * time.Second)
	for clientCount(sess) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := clientCount(sess); got != 0 {
		t.Errorf("client count after peer vanished = %d, want 0 (leak)", got)
	}
}

// The server must ping an otherwise silent connection: that ping is what turns
// a half-open socket (laptop suspended, no FIN ever sent) into a read timeout
// instead of a goroutine parked forever.
func TestPtyWSPingsIdleConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the ping period")
	}
	_, _, url := testPtyServer(t)

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	pinged := make(chan struct{}, 1)
	conn.SetPingHandler(func(string) error {
		select {
		case pinged <- struct{}{}:
		default:
		}
		return nil
	})
	// Pings surface through the read loop, so keep reading (and discarding).
	go func() {
		for {
			conn.SetReadDeadline(time.Now().Add(wsPingPeriod + 10*time.Second))
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	select {
	case <-pinged:
	case <-time.After(wsPingPeriod + 10*time.Second):
		t.Errorf("no ping within %v — a half-open socket would hang forever", wsPingPeriod)
	}
}
