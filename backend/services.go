package backend

import (
	"log"

	"github.com/google/uuid"
)

// ServerActions enum
type ServerActions string
type BackendActions string

const (
	// ServerActions
	ServerActionResponse            ServerActions = "response"
	ServerActionMessageToRoom       ServerActions = "message_to_room"
	ServerActionMessageToConnection ServerActions = "message_to_connection"
	ServerActionBroadcastMessage    ServerActions = "broadcast"

	// BackendActions
	BackendActionConnectionRequest BackendActions = "connection_request"
	BackendActionNewMessage        BackendActions = "new_message"
	BackendActionDisconnected      BackendActions = "disconnected"
	BackendActionConnectionSuccess BackendActions = "new_connection"
	BackendActionError             BackendActions = "error"
	BackendActionHeartbeat         BackendActions = "heartbeat"
)

// NewSocketConnection tells the backend about a new connection and waits for approval
func (s *TCPServer) NewSocketConnection(headers map[string][]string, url string, remoteAddr string) (map[string]any, error) {
	requestID := uuid.New().String()

	// Create the response channel before sending, under lock
	responseCh := make(chan BackendResponse, 1)
	s.pendingMu.Lock()
	s.PendingResponses[requestID] = responseCh
	s.pendingMu.Unlock()

	err := s.sendMessage(BackendRequest{
		RequestID: requestID,
		Action:    BackendActionConnectionRequest,
		Params: map[string]any{
			"headers":     headers,
			"url":         url,
			"remote_addr": remoteAddr,
		},
	})
	if err != nil {
		s.pendingMu.Lock()
		delete(s.PendingResponses, requestID)
		s.pendingMu.Unlock()
		return nil, err
	}

	log.Println("Sent request to backend:", requestID, BackendActionConnectionRequest)
	response := <-responseCh
	log.Println("Received response channel:", response.RequestID, response.Action)

	s.pendingMu.Lock()
	delete(s.PendingResponses, requestID)
	s.pendingMu.Unlock()

	return response.Params, nil
}

func (s *TCPServer) HandleMessage(context map[string]any, message []byte) {
	requestID := uuid.New().String()
	s.sendMessage(BackendRequest{
		RequestID: requestID,
		Action:    BackendActionNewMessage,
		Params:    map[string]any{"message": string(message), "context": context},
	})
}

func (s *TCPServer) HandleDisconnect(context map[string]any) {
	requestID := uuid.New().String()
	s.sendMessage(BackendRequest{
		RequestID: requestID,
		Action:    BackendActionDisconnected,
		Params:    map[string]any{"context": context},
	})
}

func (s *TCPServer) HandleConnectionSuccess(context map[string]any) {
	requestID := uuid.New().String()
	s.sendMessage(BackendRequest{
		RequestID: requestID,
		Action:    BackendActionConnectionSuccess,
		Params:    map[string]any{"context": context},
	})
}

func (s *TCPServer) HandleError(context map[string]any, errorMessage string) {
	requestID := uuid.New().String()
	s.sendMessage(BackendRequest{
		RequestID: requestID,
		Action:    BackendActionError,
		Params: map[string]any{
			"context": context,
			"error":   errorMessage,
		},
	})
}

// HandleRequests processes incoming requests from TCP backends
func (s *TCPServer) HandleRequests() {
	for {
		select {
		case request, ok := <-s.PendingServerRequests:
			if !ok {
				return
			}
			log.Println("Received request:", request.RequestID, request.Action, request.Params)
			switch request.Action {
			case ServerActionResponse:
				s.pendingMu.Lock()
				ch, exists := s.PendingResponses[request.RequestID]
				s.pendingMu.Unlock()
				if exists {
					ch <- BackendResponse{
						RequestID: request.RequestID,
						Action:    ServerActionResponse,
						Params:    request.Params,
					}
				} else {
					log.Printf("No pending response channel for request %s", request.RequestID)
				}
			default:
				if action, ok := s.WebsocketActions[request.Action]; ok {
					action(request.Params)
				}
			}
		case <-s.done:
			return
		}
	}
}
