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

func TestSendMessageNoConnection_QueueDisabled(t *testing.T) {
	s := NewTCPServerWithQueueSize(0) // queue disabled
	s.WebsocketActions = make(map[ServerActions]func(map[string]any))

	err := s.sendMessage(BackendRequest{
		RequestID: "no-conn",
		Action:    BackendActionNewMessage,
		Params:    map[string]any{},
	})
	if err == nil {
		t.Error("Expected error when no connections and queue disabled")
	}
}

func TestSendMessageNoConnection_QueueEnabled(t *testing.T) {
	s := NewTCPServerWithQueueSize(100)
	s.WebsocketActions = make(map[ServerActions]func(map[string]any))

	err := s.sendMessage(BackendRequest{
		RequestID: "queued-1",
		Action:    BackendActionNewMessage,
		Params:    map[string]any{"message": "hello"},
	})
	if err != nil {
		t.Errorf("Expected message to be queued, got error: %v", err)
	}
	if s.QueueLen() != 1 {
		t.Errorf("Expected queue length 1, got %d", s.QueueLen())
	}
}

func TestQueueDropsOldest(t *testing.T) {
	s := NewTCPServerWithQueueSize(3)
	s.WebsocketActions = make(map[ServerActions]func(map[string]any))

	// Fill the queue
	for i := 0; i < 5; i++ {
		s.sendMessage(BackendRequest{
			RequestID: "msg-" + string(rune('A'+i)),
			Action:    BackendActionNewMessage,
			Params:    map[string]any{},
		})
	}

	if s.QueueLen() != 3 {
		t.Errorf("Expected queue capped at 3, got %d", s.QueueLen())
	}

	// Oldest two (A, B) should have been dropped, remaining: C, D, E
	s.queueMu.Lock()
	first := s.messageQueue[0].RequestID
	s.queueMu.Unlock()
	if first != "msg-C" {
		t.Errorf("Expected oldest in queue to be 'msg-C', got '%s'", first)
	}
}

func TestFlushQueueOnBackendConnect(t *testing.T) {
	s, addr := startTestTCPServer(t)
	defer s.Stop()

	// Queue some messages while no backends connected
	// (we need to manipulate the queue directly since startTestTCPServer starts the server
	// but we haven't connected a backend yet... actually the server has no clients yet)
	s.queueMu.Lock()
	s.maxQueueSize = 100
	s.messageQueue = append(s.messageQueue,
		BackendRequest{RequestID: "q1", Action: BackendActionNewMessage, Params: map[string]any{"message": "queued-1"}},
		BackendRequest{RequestID: "q2", Action: BackendActionNewMessage, Params: map[string]any{"message": "queued-2"}},
		BackendRequest{RequestID: "q3", Action: BackendActionNewMessage, Params: map[string]any{"message": "queued-3"}},
	)
	s.queueMu.Unlock()

	// Now connect a backend — should trigger flush
	conn := dialTCP(t, addr)
	defer conn.Close()

	// Read the flushed messages
	reader := bufio.NewReader(conn)
	received := make([]BackendRequest, 0)
	for i := 0; i < 3; i++ {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("Failed to read flushed message %d: %v", i, err)
		}
		var req BackendRequest
		json.Unmarshal([]byte(line), &req)
		received = append(received, req)
	}

	// Verify order (FIFO)
	if received[0].RequestID != "q1" || received[1].RequestID != "q2" || received[2].RequestID != "q3" {
		t.Errorf("Queue flush not in FIFO order: got %s, %s, %s",
			received[0].RequestID, received[1].RequestID, received[2].RequestID)
	}

	// Queue should be empty now
	time.Sleep(100 * time.Millisecond)
	if s.QueueLen() != 0 {
		t.Errorf("Expected empty queue after flush, got %d", s.QueueLen())
	}
}

func TestConnectionRequestNeverQueued(t *testing.T) {
	s := NewTCPServerWithQueueSize(100) // queue enabled
	s.WebsocketActions = make(map[ServerActions]func(map[string]any))

	// No backends connected — connection_request should error, NOT queue
	err := s.sendMessage(BackendRequest{
		RequestID: "conn-req-1",
		Action:    BackendActionConnectionRequest,
		Params:    map[string]any{"headers": map[string]any{}, "url": "/ws/", "remote_addr": "10.0.0.1"},
	})
	if err == nil {
		t.Error("Expected error for connection_request with no backends, should never be queued")
	}
	if s.QueueLen() != 0 {
		t.Errorf("Connection request should NOT be in queue, but queue has %d items", s.QueueLen())
	}

	// Regular messages should still queue
	err = s.sendMessage(BackendRequest{
		RequestID: "msg-1",
		Action:    BackendActionNewMessage,
		Params:    map[string]any{"message": "hello"},
	})
	if err != nil {
		t.Errorf("Regular message should queue, got error: %v", err)
	}
	if s.QueueLen() != 1 {
		t.Errorf("Expected 1 queued message, got %d", s.QueueLen())
	}
}

func TestQueueDefaultSize(t *testing.T) {
	s := NewTCPServer()
	if s.maxQueueSize != 1000 {
		t.Errorf("Expected default queue size 1000, got %d", s.maxQueueSize)
	}
}

func TestFullLifecycle_ConnectQueueReconnect(t *testing.T) {
	s, addr := startTestTCPServer(t)
	defer s.Stop()
	s.maxQueueSize = 100

	// Step 1: Connect a backend
	conn1 := dialTCP(t, addr)
	time.Sleep(50 * time.Millisecond)

	// Step 2: Send a message — should go directly to backend
	s.sendMessage(BackendRequest{
		RequestID: "direct-1",
		Action:    BackendActionNewMessage,
		Params:    map[string]any{"message": "live-msg"},
	})

	reader1 := bufio.NewReader(conn1)
	conn1.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := reader1.ReadString('\n')
	if err != nil {
		t.Fatalf("Backend should have received direct message: %v", err)
	}
	var directMsg BackendRequest
	json.Unmarshal([]byte(line), &directMsg)
	if directMsg.RequestID != "direct-1" {
		t.Errorf("Expected direct-1, got %s", directMsg.RequestID)
	}

	// Step 3: Backend disconnects (simulate crash)
	conn1.Close()
	time.Sleep(150 * time.Millisecond) // wait for server to detect disconnect

	// Step 4: Messages arrive while no backend — should queue
	s.sendMessage(BackendRequest{
		RequestID: "queued-A",
		Action:    BackendActionNewMessage,
		Params:    map[string]any{"message": "offline-msg-A"},
	})
	s.sendMessage(BackendRequest{
		RequestID: "queued-B",
		Action:    BackendActionNewMessage,
		Params:    map[string]any{"message": "offline-msg-B"},
	})

	if s.QueueLen() != 2 {
		t.Errorf("Expected 2 queued messages, got %d", s.QueueLen())
	}

	// Step 5: New backend connects — should auto-flush
	conn2 := dialTCP(t, addr)
	defer conn2.Close()

	reader2 := bufio.NewReader(conn2)
	received := make([]BackendRequest, 0)
	for i := 0; i < 2; i++ {
		conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := reader2.ReadString('\n')
		if err != nil {
			t.Fatalf("Failed to read flushed message %d: %v", i, err)
		}
		var req BackendRequest
		json.Unmarshal([]byte(line), &req)
		received = append(received, req)
	}

	// Verify FIFO order
	if received[0].RequestID != "queued-A" {
		t.Errorf("Expected first flushed message 'queued-A', got '%s'", received[0].RequestID)
	}
	if received[1].RequestID != "queued-B" {
		t.Errorf("Expected second flushed message 'queued-B', got '%s'", received[1].RequestID)
	}

	// Queue should be empty
	time.Sleep(100 * time.Millisecond)
	if s.QueueLen() != 0 {
		t.Errorf("Expected empty queue after flush, got %d", s.QueueLen())
	}
}

func TestQueueLenEmpty(t *testing.T) {
	s := NewTCPServer()
	if s.QueueLen() != 0 {
		t.Errorf("Expected empty queue, got %d", s.QueueLen())
	}
}
