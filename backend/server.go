package backend

import (
	"bufio"
	"encoding/json"
	"errors"
	"log"
	"math/rand"
	"net"
	"sync"
	"time"
)

// ServerRequest is a request from the backend to the ws server to perform a specific action(and might return a response)
type ServerRequest struct {
	RequestID string         `json:"request_id"`
	Action    ServerActions  `json:"action"`
	Params    map[string]any `json:"params"`
}

// BackendAction is a queued request data(it basically extends the ServerRequest struct with the conn field, so replies can be sent with RequestID)
type BackendAction struct {
	ServerRequest
	conn net.Conn
}

// SocketConnectionRequest is a queued request with call back to accept or reject the connection
type BackendRequest struct {
	RequestID string         `json:"request_id"`
	Action    BackendActions `json:"action"`
	Params    map[string]any `json:"params"`
}

// BackendResponse is a response from the server to the backend
type BackendResponse struct {
	RequestID string         `json:"request_id"`
	Action    ServerActions  `json:"action"`
	Params    map[string]any `json:"params"`
}

const (
	// HeartbeatInterval is how often the server pings TCP backends
	HeartbeatInterval = 15 * time.Second
	// HeartbeatTimeout is how long to wait for a pong before considering the backend dead
	HeartbeatTimeout = 5 * time.Second
)

// TCPServer represents a TCP server instance
type TCPServer struct {
	listener              net.Listener
	Clients               []net.Conn
	mu                    sync.Mutex
	pendingMu             sync.Mutex                     // protects PendingResponses map
	PendingServerRequests chan BackendAction              // Requests from the backend(python) to the server
	PendingResponses      map[string]chan BackendResponse // Responses pending from the backend(python)
	done                  chan struct{}                   // signals shutdown

	// Message queue for when no backends are connected
	messageQueue []BackendRequest
	queueMu      sync.Mutex
	maxQueueSize int // 0 = disabled, >0 = max buffered messages

	WebsocketActions map[ServerActions]func(map[string]any)
}

// NewTCPServer creates a new TCP server instance
func NewTCPServer() *TCPServer {
	return NewTCPServerWithQueueSize(1000)
}

// NewTCPServerWithQueueSize creates a new TCP server with a configurable queue size.
// queueSize=0 disables queuing (messages dropped when no backends connected).
func NewTCPServerWithQueueSize(queueSize int) *TCPServer {
	server := &TCPServer{
		Clients:               make([]net.Conn, 0),
		PendingServerRequests: make(chan BackendAction, 256),
		PendingResponses:      make(map[string]chan BackendResponse),
		done:                  make(chan struct{}),
		messageQueue:          make([]BackendRequest, 0),
		maxQueueSize:          queueSize,
	}
	return server
}

// Start starts the TCP server on the specified address
func (s *TCPServer) Start(address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	s.listener = listener
	log.Printf("TCP Server listening on %s", address)

	go s.acceptConnections()
	go s.HandleRequests()
	go s.heartbeatLoop()
	return nil
}

// Stop gracefully shuts down the TCP server
func (s *TCPServer) Stop() error {
	// Signal shutdown
	select {
	case <-s.done:
		// Already closed
	default:
		close(s.done)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Close all client connections
	for _, conn := range s.Clients {
		conn.Close()
	}

	// Close the listener
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// GetListener returns the underlying net.Listener
func (s *TCPServer) GetListener() net.Listener {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listener
}

// handleConnection handles a new connection
func (s *TCPServer) handleConnection(conn net.Conn) {
	defer func() {
		s.mu.Lock()
		// Remove the connection from the slice
		for i, c := range s.Clients {
			if c == conn {
				s.Clients = append(s.Clients[:i], s.Clients[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		conn.Close()
	}()

	reader := bufio.NewReader(conn)

	for {
		message, err := reader.ReadString('\n')
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			log.Printf("Error reading message: %v", err)
			return
		}

		log.Println("Received message", message)

		var request ServerRequest
		err = json.Unmarshal([]byte(message), &request)
		if err != nil {
			log.Printf("Error unmarshalling message: %v", err)
			continue // don't kill connection on bad JSON, skip
		}

		select {
		case s.PendingServerRequests <- BackendAction{
			ServerRequest: request,
			conn:          conn,
		}:
		case <-s.done:
			return
		}
	}
}

// acceptConnections handles incoming connections
func (s *TCPServer) acceptConnections() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			log.Printf("Error accepting connection: %v", err)
			continue
		}

		s.mu.Lock()
		s.Clients = append(s.Clients, conn)
		clientCount := len(s.Clients)
		s.mu.Unlock()
		log.Println("Accepted connection", clientCount)

		// Flush queued messages now that a backend is available
		go s.flushQueue()

		go s.handleConnection(conn)
	}
}

// heartbeatLoop periodically pings all TCP backend connections
func (s *TCPServer) heartbeatLoop() {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.pingBackends()
		}
	}
}

// pingBackends sends a heartbeat ping to all connected backends
func (s *TCPServer) pingBackends() {
	s.mu.Lock()
	clients := make([]net.Conn, len(s.Clients))
	copy(clients, s.Clients)
	s.mu.Unlock()

	ping := BackendRequest{
		RequestID: "heartbeat",
		Action:    BackendActionHeartbeat,
		Params:    map[string]any{},
	}
	data, err := json.Marshal(ping)
	if err != nil {
		return
	}
	msg := string(data) + "\n"

	var dead []net.Conn
	for _, conn := range clients {
		conn.SetWriteDeadline(time.Now().Add(HeartbeatTimeout))
		_, err := conn.Write([]byte(msg))
		conn.SetWriteDeadline(time.Time{}) // reset
		if err != nil {
			log.Printf("Backend heartbeat failed for %s: %v", conn.RemoteAddr(), err)
			dead = append(dead, conn)
		}
	}

	if len(dead) > 0 {
		s.mu.Lock()
		for _, d := range dead {
			for i, c := range s.Clients {
				if c == d {
					s.Clients = append(s.Clients[:i], s.Clients[i+1:]...)
					d.Close()
					break
				}
			}
		}
		s.mu.Unlock()
	}
}

// sendMessage sends a message to a random backend client.
// If no backends are connected and queuing is enabled, the message is buffered.
// Connection requests are NEVER queued (they require a synchronous response).
func (s *TCPServer) sendMessage(_message BackendRequest) error {
	message, err := json.Marshal(_message)
	if err != nil {
		return err
	}
	conn, ok := s.GetRandomConnection()
	if !ok {
		// Connection requests must NOT be queued — they need a sync response
		if _message.Action == BackendActionConnectionRequest {
			return errors.New("no backend available to handle connection request")
		}
		return s.enqueue(_message)
	}
	writer := bufio.NewWriter(conn)
	writer.WriteString(string(message) + "\n")
	log.Println("Sent message to", conn.RemoteAddr())
	return writer.Flush()
}

// enqueue adds a message to the queue when no backends are available.
// Returns an error if queuing is disabled (maxQueueSize=0).
func (s *TCPServer) enqueue(msg BackendRequest) error {
	if s.maxQueueSize == 0 {
		return errors.New("no connection available (queue disabled)")
	}

	s.queueMu.Lock()
	defer s.queueMu.Unlock()

	// Drop oldest if queue is full
	if len(s.messageQueue) >= s.maxQueueSize {
		dropped := s.messageQueue[0]
		s.messageQueue = s.messageQueue[1:]
		log.Printf("Queue full (%d), dropped oldest message: %s", s.maxQueueSize, dropped.RequestID)
	}

	s.messageQueue = append(s.messageQueue, msg)
	log.Printf("Message queued (%d/%d): %s %s", len(s.messageQueue), s.maxQueueSize, msg.Action, msg.RequestID)
	return nil
}

// flushQueue drains all queued messages to the connected backends.
// Called when a new backend connects.
func (s *TCPServer) flushQueue() {
	s.queueMu.Lock()
	if len(s.messageQueue) == 0 {
		s.queueMu.Unlock()
		return
	}
	// Take ownership of the queue and reset it
	queue := s.messageQueue
	s.messageQueue = make([]BackendRequest, 0)
	s.queueMu.Unlock()

	log.Printf("Flushing %d queued messages to backend", len(queue))
	for i, msg := range queue {
		if err := s.sendMessage(msg); err != nil {
			// Backend went down again mid-flush — re-queue remaining (unsent ones)
			remaining := queue[i:]
			log.Printf("Flush interrupted at %d/%d: %v, re-queuing %d messages", i, len(queue), err, len(remaining))
			s.queueMu.Lock()
			// Prepend unsent messages back (they're older, should go first)
			s.messageQueue = append(remaining, s.messageQueue...)
			s.queueMu.Unlock()
			return
		}
	}
	log.Printf("Queue flushed successfully")
}

// QueueLen returns the current number of queued messages.
func (s *TCPServer) QueueLen() int {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	return len(s.messageQueue)
}

// GetRandomConnection returns a random connection from the active clients
func (s *TCPServer) GetRandomConnection() (net.Conn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.Clients) == 0 {
		log.Println("No connections available")
		return nil, false
	}

	randomIndex := rand.Intn(len(s.Clients))
	return s.Clients[randomIndex], true
}
