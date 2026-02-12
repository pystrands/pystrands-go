package client

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client represents a connected WebSocket client.
// The exported struct is safe to copy (no mutex) — it's used in callbacks.
type Client struct {
	Conn     *websocket.Conn
	MetaData map[string]any
	RoomID   string
	ClientID string
}

// connClient wraps a Client with a write mutex for internal use
type connClient struct {
	Client
	writeMu sync.Mutex // protects concurrent writes to WebSocket conn
}

// WriteMessage is a thread-safe wrapper around Conn.WriteMessage
func (c *connClient) WriteMessage(messageType int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.Conn.WriteMessage(messageType, data)
}

type Room struct {
	ID      string
	clients map[string]*connClient
}

// WebSocketServer represents the WebSocket server
type WebSocketServer struct {
	upgrader            websocket.Upgrader
	rooms               map[string]*Room
	clients             map[string]*connClient
	mu                  sync.RWMutex // protects rooms and clients maps
	server              *http.Server // for graceful shutdown
	onConnectionRequest func(r *http.Request) (map[string]any, error)
	onConnectionSuccess func(_client Client) (map[string]any, error)
	onMessage           func(_client Client, message []byte)
	onDisconnect        func(_client Client)
}

// NewWebSocketServer creates a new WebSocket server instance
func NewWebSocketServer(
	onConnectionRequest func(r *http.Request) (map[string]any, error),
	onConnectionSuccess func(_client Client) (map[string]any, error),
	onMessage func(_client Client, message []byte),
	onDisconnect func(_client Client),
) *WebSocketServer {
	return &WebSocketServer{
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		rooms:               make(map[string]*Room),
		clients:             make(map[string]*connClient),
		onConnectionRequest: onConnectionRequest,
		onConnectionSuccess: onConnectionSuccess,
		onMessage:           onMessage,
		onDisconnect:        onDisconnect,
	}
}

func (s *WebSocketServer) Start(port string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/", s.HandleConnection)

	s.server = &http.Server{
		Addr:    port,
		Handler: mux,
	}

	if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("WebSocket server error: %v", err)
	}
}

func (s *WebSocketServer) Stop() {
	// Graceful shutdown with 5 second timeout
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.server.Shutdown(ctx); err != nil {
			log.Printf("WebSocket server shutdown error: %v", err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.clients {
		c.Conn.Close()
	}
}

// HandleConnection handles new WebSocket connections
func (s *WebSocketServer) HandleConnection(w http.ResponseWriter, r *http.Request) {
	metaData, err := s.onConnectionRequest(r)
	if err != nil {
		log.Printf("Failed to handle new connection request: %v", err)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade connection: %v", err)
		return
	}

	if metaData["accepted"] == false {
		log.Println("Connection rejected by Backend")
		conn.Close()
		return
	}

	roomID := metaData["room_id"].(string)

	s.mu.Lock()
	room, ok := s.rooms[roomID]
	if !ok {
		room = &Room{
			ID:      roomID,
			clients: make(map[string]*connClient),
		}
		s.rooms[roomID] = room
	}

	// Add client to the map
	cc := &connClient{
		Client: Client{
			Conn:     conn,
			MetaData: metaData,
			RoomID:   roomID,
			ClientID: metaData["client_id"].(string),
		},
	}
	s.clients[cc.ClientID] = cc
	room.clients[cc.ClientID] = cc
	totalClients := len(s.clients)
	s.mu.Unlock()

	log.Printf("New client connected. Total clients: %d", totalClients)

	// Send connection success event
	if s.onConnectionSuccess != nil {
		metaData["accepted"] = true
		_, err := s.onConnectionSuccess(cc.Client)
		if err != nil {
			log.Printf("Failed to send connection success event: %v", err)
		}
	}

	// Handle messages from this client
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Error reading message: %v", err)
			s.handleDisconnect(cc.ClientID)
			return
		}

		// Handle the received message
		s.onMessage(cc.Client, message)
	}
}

// handleDisconnect handles client disconnection
func (s *WebSocketServer) handleDisconnect(clientID string) {
	s.mu.Lock()
	cc, exists := s.clients[clientID]
	if !exists {
		s.mu.Unlock()
		return
	}

	// Remove client from room
	if room, ok := s.rooms[cc.RoomID]; ok {
		delete(room.clients, clientID)
		// Clean up empty rooms
		if len(room.clients) == 0 {
			delete(s.rooms, cc.RoomID)
		}
	}

	delete(s.clients, clientID)
	totalClients := len(s.clients)
	s.mu.Unlock()

	// Call disconnect handler and close conn outside the lock
	s.onDisconnect(cc.Client)
	cc.Conn.Close()

	log.Printf("Client disconnected. Total clients: %d", totalClients)
}

// BroadcastMessage sends a message to all connected clients
func (s *WebSocketServer) BroadcastMessage(message []byte) {
	s.mu.RLock()
	clients := make([]*connClient, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.RUnlock()

	for _, cc := range clients {
		err := cc.WriteMessage(websocket.TextMessage, message)
		if err != nil {
			log.Printf("Error broadcasting message: %v", err)
			s.handleDisconnect(cc.ClientID)
		}
	}
}

// SendMessage sends a message to a specific client
func (s *WebSocketServer) SendMessage(clientID string, message []byte) error {
	s.mu.RLock()
	cc, exists := s.clients[clientID]
	s.mu.RUnlock()

	if !exists {
		log.Printf("client not found")
		return errors.New("client not found")
	}
	return cc.WriteMessage(websocket.TextMessage, message)
}
