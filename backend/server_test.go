package backend

import (
	"bufio"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"
)

func startTestTCPServer(t *testing.T) (*TCPServer, string) {
	t.Helper()
	s := NewTCPServer()
	s.WebsocketActions = make(map[ServerActions]func(map[string]any))

	err := s.Start(":0") // random port
	if err != nil {
		t.Fatalf("Failed to start TCP server: %v", err)
	}
	addr := s.GetListener().Addr().String()
	return s, addr
}

func dialTCP(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Failed to dial TCP: %v", err)
	}
	return conn
}

func TestTCPServerStartStop(t *testing.T) {
	s, _ := startTestTCPServer(t)
	time.Sleep(50 * time.Millisecond)
	err := s.Stop()
	if err != nil {
		t.Errorf("Stop returned error: %v", err)
	}
}

func TestTCPClientConnect(t *testing.T) {
	s, addr := startTestTCPServer(t)
	defer s.Stop()

	conn := dialTCP(t, addr)
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	s.mu.Lock()
	count := len(s.Clients)
	s.mu.Unlock()

	if count != 1 {
		t.Errorf("Expected 1 TCP client, got %d", count)
	}
}

func TestTCPClientDisconnect(t *testing.T) {
	s, addr := startTestTCPServer(t)
	defer s.Stop()

	conn := dialTCP(t, addr)
	time.Sleep(50 * time.Millisecond)
	conn.Close()
	time.Sleep(100 * time.Millisecond)

	s.mu.Lock()
	count := len(s.Clients)
	s.mu.Unlock()

	if count != 0 {
		t.Errorf("Expected 0 TCP clients after disconnect, got %d", count)
	}
}

func TestGetRandomConnection(t *testing.T) {
	s, addr := startTestTCPServer(t)
	defer s.Stop()

	// No connections — should return false
	_, ok := s.GetRandomConnection()
	if ok {
		t.Error("Expected no connection available")
	}

	// Add a connection
	conn := dialTCP(t, addr)
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	c, ok := s.GetRandomConnection()
	if !ok {
		t.Error("Expected connection available")
	}
	if c == nil {
		t.Error("Expected non-nil connection")
	}
}

func TestSendMessageToBackend(t *testing.T) {
	s, addr := startTestTCPServer(t)
	defer s.Stop()

	conn := dialTCP(t, addr)
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	err := s.sendMessage(BackendRequest{
		RequestID: "test-123",
		Action:    BackendActionNewMessage,
		Params:    map[string]any{"message": "hello"},
	})
	if err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}

	// Read from the TCP connection
	reader := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("Failed to read: %v", err)
	}

	var req BackendRequest
	err = json.Unmarshal([]byte(line), &req)
	if err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}
	if req.RequestID != "test-123" {
		t.Errorf("Expected request_id 'test-123', got '%s'", req.RequestID)
	}
	if req.Action != BackendActionNewMessage {
		t.Errorf("Expected action 'new_message', got '%s'", req.Action)
	}
}

func TestHandleRequestsResponse(t *testing.T) {
	s, addr := startTestTCPServer(t)
	defer s.Stop()

	conn := dialTCP(t, addr)
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	// Create a pending response channel
	requestID := "resp-test-1"
	respCh := make(chan BackendResponse, 1)
	s.pendingMu.Lock()
	s.PendingResponses[requestID] = respCh
	s.pendingMu.Unlock()

	// Simulate backend sending a response via TCP
	response := ServerRequest{
		RequestID: requestID,
		Action:    ServerActionResponse,
		Params:    map[string]any{"accepted": true, "client_id": "c1"},
	}
	data, _ := json.Marshal(response)
	conn.Write(append(data, '\n'))

	select {
	case resp := <-respCh:
		if resp.RequestID != requestID {
			t.Errorf("Expected request_id '%s', got '%s'", requestID, resp.RequestID)
		}
		if resp.Params["accepted"] != true {
			t.Error("Expected accepted=true")
		}
	case <-time.After(2 * time.Second):
		t.Error("Timed out waiting for response")
	}
}

func TestHandleRequestsWebsocketAction(t *testing.T) {
	s, addr := startTestTCPServer(t)
	defer s.Stop()

	actionCalled := make(chan map[string]any, 1)
	s.WebsocketActions[ServerActionBroadcastMessage] = func(params map[string]any) {
		actionCalled <- params
	}

	conn := dialTCP(t, addr)
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	// Send a broadcast action from backend
	request := ServerRequest{
		RequestID: "action-1",
		Action:    ServerActionBroadcastMessage,
		Params:    map[string]any{"message": "broadcast-test"},
	}
	data, _ := json.Marshal(request)
	conn.Write(append(data, '\n'))

	select {
	case params := <-actionCalled:
		if params["message"] != "broadcast-test" {
			t.Errorf("Expected message 'broadcast-test', got '%v'", params["message"])
		}
	case <-time.After(2 * time.Second):
		t.Error("Timed out waiting for websocket action")
	}
}

func TestMultipleTCPBackends(t *testing.T) {
	s, addr := startTestTCPServer(t)
	defer s.Stop()

	conn1 := dialTCP(t, addr)
	conn2 := dialTCP(t, addr)
	conn3 := dialTCP(t, addr)
	defer conn1.Close()
	defer conn2.Close()
	defer conn3.Close()
	time.Sleep(50 * time.Millisecond)

	s.mu.Lock()
	count := len(s.Clients)
	s.mu.Unlock()

	if count != 3 {
		t.Errorf("Expected 3 TCP clients, got %d", count)
	}

	// Disconnect one
	conn2.Close()
	time.Sleep(100 * time.Millisecond)

	s.mu.Lock()
	count = len(s.Clients)
	s.mu.Unlock()

	if count != 2 {
		t.Errorf("Expected 2 TCP clients, got %d", count)
	}
}

func TestConcurrentTCPAccess(t *testing.T) {
	s, addr := startTestTCPServer(t)
	defer s.Stop()

	var wg sync.WaitGroup
	const numClients = 20

	conns := make([]net.Conn, numClients)
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				t.Errorf("Dial failed: %v", err)
				return
			}
			conns[idx] = conn
		}(i)
	}
	wg.Wait()
	time.Sleep(100 * time.Millisecond)

	s.mu.Lock()
	count := len(s.Clients)
	s.mu.Unlock()

	if count != numClients {
		t.Errorf("Expected %d TCP clients, got %d", numClients, count)
	}

	// Close all concurrently
	for i := 0; i < numClients; i++ {
		if conns[i] != nil {
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				c.Close()
			}(conns[i])
		}
	}
	wg.Wait()
	time.Sleep(200 * time.Millisecond)

	s.mu.Lock()
	remaining := len(s.Clients)
	s.mu.Unlock()

	if remaining != 0 {
		t.Errorf("Expected 0 TCP clients after disconnect, got %d", remaining)
	}
}

func TestHeartbeatAction(t *testing.T) {
	// Verify the heartbeat action constant is defined
	if BackendActionHeartbeat != "heartbeat" {
		t.Errorf("Expected heartbeat action, got '%s'", BackendActionHeartbeat)
	}
}

func TestSendMessageNoConnection(t *testing.T) {
	s := NewTCPServer()
	s.WebsocketActions = make(map[ServerActions]func(map[string]any))

	err := s.sendMessage(BackendRequest{
		RequestID: "no-conn",
		Action:    BackendActionNewMessage,
		Params:    map[string]any{},
	})
	if err == nil {
		t.Error("Expected error when no connections available")
	}
}
