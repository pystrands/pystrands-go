// Package bridge provides a bridge between the client and the server.
package bridge

import (
	"log"
	"net/http"
	"strconv"

	"github.com/pystrand/pystrand-go/backend"
	"github.com/pystrand/pystrand-go/client"
	"github.com/pystrand/pystrand-go/config"
)

type Bridge struct {
	_backend  *backend.TCPServer
	webSocket *client.WebSocketServer
	config    *config.Config
}

func NewBridge() *Bridge {
	// Load configuration
	cfg := config.LoadConfig()

	_backend := backend.NewTCPServerWithQueueSize(cfg.QueueSize)
	_backend.WebsocketActions = make(map[backend.ServerActions]func(map[string]any))

	onConnectionRequest := func(r *http.Request) (map[string]any, error) {
		metaData, err := _backend.NewSocketConnection(r.Header, r.URL.Path, r.RemoteAddr)
		if err != nil {
			_backend.HandleError(map[string]any{
				"headers":     r.Header,
				"url":         r.URL.Path,
				"remote_addr": r.RemoteAddr,
			}, err.Error())
			return nil, err
		}
		return metaData, nil
	}

	onConnectionSuccess := func(_client client.Client) (map[string]any, error) {
		clientData := map[string]any{
			"client_id": _client.ClientID,
			"room_id":   _client.RoomID,
			"metadata":  _client.MetaData,
		}
		_backend.HandleConnectionSuccess(clientData)
		return clientData, nil
	}

	onMessage := func(_client client.Client, message []byte) {
		clientData := map[string]any{
			"client_id": _client.ClientID,
			"room_id":   _client.RoomID,
			"metadata":  _client.MetaData,
		}
		_backend.HandleMessage(clientData, message)
	}

	onDisconnect := func(_client client.Client) {
		clientData := map[string]any{
			"client_id": _client.ClientID,
			"room_id":   _client.RoomID,
			"metadata":  _client.MetaData,
		}
		_backend.HandleDisconnect(clientData)
	}

	webSocket := client.NewWebSocketServer(
		onConnectionRequest,
		onConnectionSuccess,
		onMessage,
		onDisconnect,
	)

	// add a new action to the backend
	_backend.WebsocketActions[backend.ServerActionMessageToRoom] = func(params map[string]any) {
		message := params["message"].(string)
		webSocket.MessageToRoom(params["room_id"].(string), []byte(message))
	}
	_backend.WebsocketActions[backend.ServerActionMessageToConnection] = func(params map[string]any) {
		message := params["message"].(string)
		webSocket.MessageToConnection(params["conn_id"].(string), []byte(message))
	}
	_backend.WebsocketActions[backend.ServerActionBroadcastMessage] = func(params map[string]any) {
		message := params["message"].(string)
		webSocket.BroadcastMessage([]byte(message))
	}

	return &Bridge{
		_backend:  _backend,
		webSocket: webSocket,
		config:    cfg,
	}
}

func (b *Bridge) Start() {
	// Start TCP server
	tcpAddr := ":" + strconv.Itoa(b.config.TCPPort)
	if err := b._backend.Start(tcpAddr); err != nil {
		log.Printf("Error starting TCP server: %v\n", err)
		return
	}
	log.Printf("TCP server started on port %d\n", b.config.TCPPort)

	// Start WebSocket server
	wsAddr := ":" + strconv.Itoa(b.config.WebSocketPort)
	b.webSocket.Start(wsAddr)
	log.Printf("WebSocket server started on port %d\n", b.config.WebSocketPort)
}

func (b *Bridge) Stop() {
	b._backend.Stop()
	b.webSocket.Stop()
}
