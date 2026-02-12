package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Minimal echo backend in Go — as fast as possible
// Simulates what a Python backend does, but at native speed

type Message struct {
	RequestID string         `json:"request_id"`
	Action    string         `json:"action"`
	Params    map[string]any `json:"params"`
}

type Backend struct {
	conn    net.Conn
	reader  *bufio.Reader
	writer  *bufio.Writer
	mu      sync.Mutex
	msgCount atomic.Int64
	id      string
}

func NewBackend(addr string, id string) (*Backend, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Backend{
		conn:   conn,
		reader: bufio.NewReaderSize(conn, 65536),
		writer: bufio.NewWriterSize(conn, 65536),
		id:     id,
	}, nil
}

func (b *Backend) send(msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, err = b.writer.Write(append(data, '\n'))
	if err != nil {
		return err
	}
	return b.writer.Flush()
}

func (b *Backend) Run() {
	clientCounter := atomic.Int64{}

	for {
		line, err := b.reader.ReadBytes('\n')
		if err != nil {
			log.Printf("[%s] Read error: %v", b.id, err)
			return
		}

		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		switch msg.Action {
		case "connection_request":
			clientID := fmt.Sprintf("%s-client-%d", b.id, clientCounter.Add(1))
			b.send(Message{
				RequestID: msg.RequestID,
				Action:    "response",
				Params: map[string]any{
					"accepted":  true,
					"client_id": clientID,
					"room_id":   "load-test",
					"metadata":  map[string]any{},
				},
			})

		case "new_message":
			b.msgCount.Add(1)
			params := msg.Params
			context, _ := params["context"].(map[string]any)
			clientID, _ := context["client_id"].(string)
			message, _ := params["message"].(string)

			b.send(Message{
				RequestID: msg.RequestID,
				Action:    "message_to_connection",
				Params: map[string]any{
					"conn_id": clientID,
					"message": message,
				},
			})

		case "disconnected", "heartbeat", "new_connection":
			// ignore
		}
	}
}

func main() {
	addr := "127.0.0.1:8083"
	numBackends := 10

	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	if len(os.Args) > 2 {
		n, err := strconv.Atoi(os.Args[2])
		if err == nil {
			numBackends = n
		}
	}

	log.Printf("Starting %d echo backends connecting to %s", numBackends, addr)

	var backends []*Backend
	for i := 0; i < numBackends; i++ {
		b, err := NewBackend(addr, fmt.Sprintf("go-backend-%d", i+1))
		if err != nil {
			log.Fatalf("Failed to connect backend %d: %v", i+1, err)
		}
		backends = append(backends, b)
		log.Printf("[%s] Connected", b.id)
	}

	// Start all backends
	for _, b := range backends {
		go b.Run()
	}

	// Stats printer
	for {
		time.Sleep(5 * time.Second)
		total := int64(0)
		for _, b := range backends {
			total += b.msgCount.Load()
		}
		log.Printf("Total messages processed: %d", total)
	}
}
