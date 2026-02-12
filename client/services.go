package client

import (
	"log"

	"github.com/gorilla/websocket"
)

func (r *Room) BroadcastMessage(message []byte) {
	for _, cc := range r.clients {
		cc.WriteMessage(websocket.TextMessage, message)
	}
}

func (c *Client) SendMessage(message []byte) {
	c.Conn.WriteMessage(websocket.TextMessage, message)
}

func (s *WebSocketServer) MessageToRoom(roomID string, message []byte) {
	s.mu.RLock()
	room, ok := s.rooms[roomID]
	if !ok {
		s.mu.RUnlock()
		log.Printf("Room %s not found", roomID)
		return
	}
	// Copy clients from room under lock
	clients := make([]*connClient, 0, len(room.clients))
	for _, c := range room.clients {
		clients = append(clients, c)
	}
	s.mu.RUnlock()

	for _, cc := range clients {
		cc.WriteMessage(websocket.TextMessage, message)
	}
}

func (s *WebSocketServer) MessageToConnection(clientID string, message []byte) {
	s.mu.RLock()
	cc, exists := s.clients[clientID]
	s.mu.RUnlock()

	if !exists {
		log.Printf("Client %s not found", clientID)
		return
	}
	cc.WriteMessage(websocket.TextMessage, message)
}
