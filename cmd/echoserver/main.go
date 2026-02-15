package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Pure Go WebSocket echo server — for benchmarking raw throughput
// No TCP backend, no protocol overhead, just echo

var upgrader = websocket.Upgrader{
	ReadBufferSize:  65536,
	WriteBufferSize: 65536,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type Stats struct {
	connections atomic.Int64
	messages    atomic.Int64
	errors      atomic.Int64
}

var stats Stats

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		stats.errors.Add(1)
		return
	}
	defer conn.Close()
	stats.connections.Add(1)
	defer stats.connections.Add(-1)

	// Set reasonable limits
	conn.SetReadLimit(1 << 20) // 1MB max message

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		stats.messages.Add(1)

		// Echo back immediately
		if err := conn.WriteMessage(msgType, data); err != nil {
			stats.errors.Add(1)
			return
		}
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","connections":%d,"messages":%d,"errors":%d}`,
		stats.connections.Load(), stats.messages.Load(), stats.errors.Load())
}

func main() {
	port := flag.Int("port", 8082, "WebSocket server port")
	workers := flag.Int("workers", 0, "GOMAXPROCS (0 = use all CPUs)")
	flag.Parse()

	if *workers > 0 {
		// runtime.GOMAXPROCS(*workers) — uncomment if needed
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/", handleWS)
	mux.HandleFunc("/health", handleHealth)

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: mux,
	}

	// Stats printer
	go func() {
		var lastMsgs int64
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			msgs := stats.messages.Load()
			rate := float64(msgs-lastMsgs) / 5.0
			log.Printf("[stats] conns=%d msgs=%d rate=%.0f/s errors=%d",
				stats.connections.Load(), msgs, rate, stats.errors.Load())
			lastMsgs = msgs
		}
	}()

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	go func() {
		<-sigChan
		log.Println("Shutting down...")
		server.Close()
	}()

	log.Printf("Echo server listening on :%d", *port)
	log.Printf("WebSocket endpoint: ws://localhost:%d/ws/", *port)
	log.Printf("Health check: http://localhost:%d/health", *port)

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}
