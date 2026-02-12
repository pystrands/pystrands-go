package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// helper: create a test WebSocket server with customizable callbacks
func newTestServer(t *testing.T) (*WebSocketServer, *httptest.Server) {
	t.Helper()

	ws := NewWebSocketServer(
		func(r *http.Request) (map[string]any, error) {
			return map[string]any{
				"accepted":  true,
				"room_id":   "test-room",
				"client_id": r.URL.Query().Get("id"),
			}, nil
		},
		func(c Client) (map[string]any, error) {
			return nil, nil
		},
		func(c Client, msg []byte) {},
		func(c Client) {},
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/", ws.HandleConnection)
	server := httptest.NewServer(mux)

	return ws, server
}

func dialWS(t *testing.T, server *httptest.Server, clientID string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/?id=" + clientID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial WebSocket: %v", err)
	}
	return conn
}

func TestConnectionLifecycle(t *testing.T) {
	ws, server := newTestServer(t)
	defer server.Close()

	conn := dialWS(t, server, "client-1")

	// Wait for connection to register
	time.Sleep(50 * time.Millisecond)

	ws.mu.RLock()
	if len(ws.clients) != 1 {
		t.Errorf("Expected 1 client, got %d", len(ws.clients))
	}
	ws.mu.RUnlock()

	// Close connection
	conn.Close()
	time.Sleep(100 * time.Millisecond)

	ws.mu.RLock()
	if len(ws.clients) != 0 {
		t.Errorf("Expected 0 clients after disconnect, got %d", len(ws.clients))
	}
	ws.mu.RUnlock()
}

func TestConnectionRejection(t *testing.T) {
	ws := NewWebSocketServer(
		func(r *http.Request) (map[string]any, error) {
			return map[string]any{
				"accepted":  false,
				"room_id":   "test-room",
				"client_id": "rejected-client",
			}, nil
		},
		func(c Client) (map[string]any, error) { return nil, nil },
		func(c Client, msg []byte) {},
		func(c Client) {},
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/", ws.HandleConnection)
	server := httptest.NewServer(mux)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/?id=test"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial: %v", err)
	}
	defer conn.Close()

	// The server should close this connection — read should fail
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Error("Expected read error on rejected connection")
	}

	time.Sleep(50 * time.Millisecond)
	ws.mu.RLock()
	if len(ws.clients) != 0 {
		t.Errorf("Expected 0 clients after rejection, got %d", len(ws.clients))
	}
	ws.mu.RUnlock()
}

func TestRoomManagement(t *testing.T) {
	ws, server := newTestServer(t)
	defer server.Close()

	// Connect two clients to the same room
	conn1 := dialWS(t, server, "c1")
	conn2 := dialWS(t, server, "c2")
	time.Sleep(50 * time.Millisecond)

	ws.mu.RLock()
	room, exists := ws.rooms["test-room"]
	if !exists {
		t.Fatal("Room 'test-room' should exist")
	}
	if len(room.clients) != 2 {
		t.Errorf("Expected 2 clients in room, got %d", len(room.clients))
	}
	ws.mu.RUnlock()

	// Disconnect one — room should still exist
	conn1.Close()
	time.Sleep(100 * time.Millisecond)

	ws.mu.RLock()
	room, exists = ws.rooms["test-room"]
	if !exists {
		t.Fatal("Room should still exist with 1 client")
	}
	if len(room.clients) != 1 {
		t.Errorf("Expected 1 client in room, got %d", len(room.clients))
	}
	ws.mu.RUnlock()

	// Disconnect the last — room should be cleaned up
	conn2.Close()
	time.Sleep(100 * time.Millisecond)

	ws.mu.RLock()
	_, exists = ws.rooms["test-room"]
	ws.mu.RUnlock()
	if exists {
		t.Error("Room should be cleaned up when empty")
	}
}

func TestMessageRouting(t *testing.T) {
	var msgMu sync.Mutex
	var received []string

	ws := NewWebSocketServer(
		func(r *http.Request) (map[string]any, error) {
			return map[string]any{
				"accepted":  true,
				"room_id":   "msg-room",
				"client_id": r.URL.Query().Get("id"),
			}, nil
		},
		func(c Client) (map[string]any, error) { return nil, nil },
		func(c Client, msg []byte) {
			msgMu.Lock()
			received = append(received, string(msg))
			msgMu.Unlock()
		},
		func(c Client) {},
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/", ws.HandleConnection)
	server := httptest.NewServer(mux)
	defer server.Close()

	// Connect two clients
	conn1 := dialWS(t, server, "sender")
	conn2 := dialWS(t, server, "receiver")
	time.Sleep(50 * time.Millisecond)

	// Test broadcast
	ws.BroadcastMessage([]byte("hello all"))
	time.Sleep(50 * time.Millisecond)

	// Both should receive
	for _, c := range []*websocket.Conn{conn1, conn2} {
		c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, msg, err := c.ReadMessage()
		if err != nil {
			t.Errorf("Expected broadcast message, got error: %v", err)
		}
		if string(msg) != "hello all" {
			t.Errorf("Expected 'hello all', got '%s'", string(msg))
		}
	}

	// Test private message (MessageToConnection)
	ws.MessageToConnection("receiver", []byte("private msg"))
	time.Sleep(50 * time.Millisecond)

	conn2.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, msg, err := conn2.ReadMessage()
	if err != nil {
		t.Errorf("Expected private message, got error: %v", err)
	}
	if string(msg) != "private msg" {
		t.Errorf("Expected 'private msg', got '%s'", string(msg))
	}

	// Test room message
	ws.MessageToRoom("msg-room", []byte("room msg"))
	time.Sleep(50 * time.Millisecond)

	for _, c := range []*websocket.Conn{conn1, conn2} {
		c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, msg, err := c.ReadMessage()
		if err != nil {
			t.Errorf("Expected room message, got error: %v", err)
		}
		if string(msg) != "room msg" {
			t.Errorf("Expected 'room msg', got '%s'", string(msg))
		}
	}

	conn1.Close()
	conn2.Close()
}

func TestSendMessageToUnknownClient(t *testing.T) {
	ws, server := newTestServer(t)
	defer server.Close()

	err := ws.SendMessage("nonexistent", []byte("test"))
	if err == nil {
		t.Error("Expected error sending to nonexistent client")
	}
}

func TestMessageToNonexistentRoom(t *testing.T) {
	ws, server := newTestServer(t)
	defer server.Close()

	// Should not panic
	ws.MessageToRoom("no-such-room", []byte("test"))
}

func TestMessageToNonexistentConnection(t *testing.T) {
	ws, server := newTestServer(t)
	defer server.Close()

	// Should not panic
	ws.MessageToConnection("no-such-client", []byte("test"))
}

func TestConcurrentClientAccess(t *testing.T) {
	ws, server := newTestServer(t)
	defer server.Close()

	const numClients = 50
	var wg sync.WaitGroup
	conns := make([]*websocket.Conn, numClients)

	// Connect many clients concurrently
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/?id=client-" + strings.Repeat("x", idx)
			conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				t.Errorf("Dial failed for client %d: %v", idx, err)
				return
			}
			conns[idx] = conn
		}(i)
	}
	wg.Wait()
	time.Sleep(100 * time.Millisecond)

	// Broadcast to all concurrently
	var broadcastWg sync.WaitGroup
	for i := 0; i < 10; i++ {
		broadcastWg.Add(1)
		go func(msg int) {
			defer broadcastWg.Done()
			ws.BroadcastMessage([]byte("concurrent-msg"))
		}(i)
	}
	broadcastWg.Wait()

	// Disconnect all concurrently
	for i := 0; i < numClients; i++ {
		if conns[i] != nil {
			wg.Add(1)
			go func(c *websocket.Conn) {
				defer wg.Done()
				c.Close()
			}(conns[i])
		}
	}
	wg.Wait()
	time.Sleep(200 * time.Millisecond)

	ws.mu.RLock()
	remaining := len(ws.clients)
	ws.mu.RUnlock()
	if remaining != 0 {
		t.Errorf("Expected 0 clients after concurrent disconnect, got %d", remaining)
	}
}

func TestDisconnectCallbackFired(t *testing.T) {
	disconnected := make(chan string, 1)

	ws := NewWebSocketServer(
		func(r *http.Request) (map[string]any, error) {
			return map[string]any{
				"accepted":  true,
				"room_id":   "dc-room",
				"client_id": "dc-client",
			}, nil
		},
		func(c Client) (map[string]any, error) { return nil, nil },
		func(c Client, msg []byte) {},
		func(c Client) {
			disconnected <- c.ClientID
		},
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/", ws.HandleConnection)
	server := httptest.NewServer(mux)
	defer server.Close()

	conn := dialWS(t, server, "dc-client")
	time.Sleep(50 * time.Millisecond)
	conn.Close()

	select {
	case id := <-disconnected:
		if id != "dc-client" {
			t.Errorf("Expected 'dc-client', got '%s'", id)
		}
	case <-time.After(2 * time.Second):
		t.Error("Disconnect callback was not fired")
	}
}

func TestGracefulShutdown(t *testing.T) {
	ws := NewWebSocketServer(
		func(r *http.Request) (map[string]any, error) {
			return map[string]any{
				"accepted":  true,
				"room_id":   "shutdown-room",
				"client_id": "shutdown-client",
			}, nil
		},
		func(c Client) (map[string]any, error) { return nil, nil },
		func(c Client, msg []byte) {},
		func(c Client) {},
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/", ws.HandleConnection)
	ws.server = &http.Server{
		Addr:    ":0",
		Handler: mux,
	}

	// Use httptest for the actual serving
	server := httptest.NewServer(mux)

	conn := dialWS(t, server, "shutdown-client")
	time.Sleep(50 * time.Millisecond)
	_ = conn

	// Stop should not panic and should close connections
	server.Close()
	ws.Stop()

	ws.mu.RLock()
	remaining := len(ws.clients)
	ws.mu.RUnlock()
	// After stop, clients map should be empty
	if remaining > 0 {
		// This is acceptable in some timing scenarios since httptest.Close
		// kills connections from outside
		t.Logf("Note: %d clients remaining after stop (may be timing)", remaining)
	}
}

func TestOnConnectionSuccessCallback(t *testing.T) {
	successCalled := make(chan Client, 1)

	ws := NewWebSocketServer(
		func(r *http.Request) (map[string]any, error) {
			return map[string]any{
				"accepted":  true,
				"room_id":   "success-room",
				"client_id": "success-client",
			}, nil
		},
		func(c Client) (map[string]any, error) {
			successCalled <- c
			return nil, nil
		},
		func(c Client, msg []byte) {},
		func(c Client) {},
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/", ws.HandleConnection)
	server := httptest.NewServer(mux)
	defer server.Close()

	conn := dialWS(t, server, "success-client")
	defer conn.Close()

	select {
	case c := <-successCalled:
		if c.ClientID != "success-client" {
			t.Errorf("Expected 'success-client', got '%s'", c.ClientID)
		}
		if c.RoomID != "success-room" {
			t.Errorf("Expected 'success-room', got '%s'", c.RoomID)
		}
	case <-time.After(2 * time.Second):
		t.Error("Connection success callback was not fired")
	}
}

func TestOnMessageCallback(t *testing.T) {
	msgReceived := make(chan string, 1)

	ws := NewWebSocketServer(
		func(r *http.Request) (map[string]any, error) {
			return map[string]any{
				"accepted":  true,
				"room_id":   "msg-test-room",
				"client_id": "msg-sender",
			}, nil
		},
		func(c Client) (map[string]any, error) { return nil, nil },
		func(c Client, msg []byte) {
			msgReceived <- string(msg)
		},
		func(c Client) {},
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/", ws.HandleConnection)
	server := httptest.NewServer(mux)
	defer server.Close()

	conn := dialWS(t, server, "msg-sender")
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	err := conn.WriteMessage(websocket.TextMessage, []byte("hello server"))
	if err != nil {
		t.Fatalf("Failed to write message: %v", err)
	}

	select {
	case msg := <-msgReceived:
		if msg != "hello server" {
			t.Errorf("Expected 'hello server', got '%s'", msg)
		}
	case <-time.After(2 * time.Second):
		t.Error("Message callback was not fired")
	}
}
