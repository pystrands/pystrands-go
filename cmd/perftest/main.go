package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// PyStrands Performance Tester — self-contained, Go-only
// Tests raw WebSocket broker throughput without backend dependencies

type Stats struct {
	connected atomic.Int64
	sent      atomic.Int64
	received  atomic.Int64
	errors    atomic.Int64
	latencies []float64
	latMu     sync.Mutex
}

func (s *Stats) addLatency(ms float64) {
	s.latMu.Lock()
	s.latencies = append(s.latencies, ms)
	s.latMu.Unlock()
}

func (s *Stats) percentile(p float64) float64 {
	s.latMu.Lock()
	defer s.latMu.Unlock()
	if len(s.latencies) == 0 {
		return 0
	}
	sort.Float64s(s.latencies)
	idx := int(float64(len(s.latencies)) * p)
	if idx >= len(s.latencies) {
		idx = len(s.latencies) - 1
	}
	return s.latencies[idx]
}

type Message struct {
	Type string  `json:"type"`
	ID   int64   `json:"id"`
	Ts   float64 `json:"ts"`
	Data string  `json:"data,omitempty"`
}

func runEchoClient(wsURL string, clientID int, duration time.Duration, stats *Stats, wg *sync.WaitGroup, payloadSize int) {
	defer wg.Done()

	u, _ := url.Parse(wsURL)
	dialer := websocket.Dialer{
		ReadBufferSize:  16384,
		WriteBufferSize: 16384,
	}

	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		stats.errors.Add(1)
		return
	}
	stats.connected.Add(1)

	// Generate payload
	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = 'x'
	}
	payloadStr := string(payload)

	deadline := time.Now().Add(duration)
	var msgID int64
	var writeMu sync.Mutex
	closed := atomic.Bool{}

	// Reader goroutine
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		defer func() {
			if r := recover(); r != nil {
				// Ignore panics from websocket reads on closed conn
			}
		}()
		for !closed.Load() {
			conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			_, data, err := conn.ReadMessage()
			if err != nil {
				if closed.Load() || time.Now().After(deadline) {
					return
				}
				continue
			}
			stats.received.Add(1)

			var msg Message
			if json.Unmarshal(data, &msg) == nil && msg.Ts > 0 {
				lat := (float64(time.Now().UnixMicro())/1e6 - msg.Ts) * 1000
				if lat > 0 && lat < 60000 {
					stats.addLatency(lat)
				}
			}
		}
	}()

	// Writer loop
	for time.Now().Before(deadline) {
		msgID++
		msg := Message{
			Type: "echo",
			ID:   msgID,
			Ts:   float64(time.Now().UnixMicro()) / 1e6,
			Data: payloadStr,
		}
		data, _ := json.Marshal(msg)

		writeMu.Lock()
		conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
		writeErr := conn.WriteMessage(websocket.TextMessage, data)
		writeMu.Unlock()

		if writeErr != nil {
			stats.errors.Add(1)
			break
		}
		stats.sent.Add(1)

		// Small sleep to prevent pure CPU spin
		time.Sleep(50 * time.Microsecond)
	}

	// Signal shutdown and close
	closed.Store(true)
	conn.Close()

	// Wait for reader to finish
	select {
	case <-recvDone:
	case <-time.After(300 * time.Millisecond):
	}
}

func runBlastClient(wsURL string, clientID int, duration time.Duration, stats *Stats, wg *sync.WaitGroup, payloadSize int) {
	defer wg.Done()
	defer func() {
		if r := recover(); r != nil {
			stats.errors.Add(1)
		}
	}()

	u, _ := url.Parse(wsURL)
	dialer := websocket.Dialer{
		ReadBufferSize:  65536,
		WriteBufferSize: 65536,
	}

	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		stats.errors.Add(1)
		return
	}
	defer conn.Close()
	stats.connected.Add(1)

	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = 'x'
	}
	payloadStr := string(payload)

	deadline := time.Now().Add(duration)
	var msgID int64

	// Pure send blast — no waiting for responses
	for time.Now().Before(deadline) {
		msgID++
		msg := Message{
			Type: "blast",
			ID:   msgID,
			Ts:   float64(time.Now().UnixMicro()) / 1e6,
			Data: payloadStr,
		}
		data, _ := json.Marshal(msg)

		conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			stats.errors.Add(1)
			return
		}
		stats.sent.Add(1)
	}
}

func runPingPongClient(wsURL string, clientID int, duration time.Duration, stats *Stats, wg *sync.WaitGroup, payloadSize int) {
	defer wg.Done()
	defer func() {
		if r := recover(); r != nil {
			stats.errors.Add(1)
		}
	}()

	u, _ := url.Parse(wsURL)
	dialer := websocket.Dialer{
		ReadBufferSize:  16384,
		WriteBufferSize: 16384,
	}

	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		stats.errors.Add(1)
		return
	}
	defer conn.Close()
	stats.connected.Add(1)

	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = 'x'
	}
	payloadStr := string(payload)

	deadline := time.Now().Add(duration)
	var msgID int64

	// Strict ping-pong: send one, wait for response, repeat
	for time.Now().Before(deadline) {
		msgID++
		sendTs := float64(time.Now().UnixMicro()) / 1e6
		msg := Message{
			Type: "ping",
			ID:   msgID,
			Ts:   sendTs,
			Data: payloadStr,
		}
		data, _ := json.Marshal(msg)

		conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			stats.errors.Add(1)
			break
		}
		stats.sent.Add(1)

		// Wait for response
		conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		_, resp, err := conn.ReadMessage()
		if err != nil {
			stats.errors.Add(1)
			continue
		}
		stats.received.Add(1)

		var respMsg Message
		if json.Unmarshal(resp, &respMsg) == nil && respMsg.Ts > 0 {
			lat := (float64(time.Now().UnixMicro())/1e6 - respMsg.Ts) * 1000
			if lat > 0 && lat < 60000 {
				stats.addLatency(lat)
			}
		}
	}
}

func main() {
	wsHost := flag.String("host", "localhost:8082", "WebSocket host:port")
	wsPath := flag.String("path", "/ws/perf", "WebSocket path")
	numClients := flag.Int("clients", 100, "Number of concurrent clients")
	duration := flag.Int("duration", 10, "Test duration in seconds")
	mode := flag.String("mode", "echo", "Test mode: echo, blast, pingpong")
	payloadSize := flag.Int("payload", 64, "Payload size in bytes")
	rampUp := flag.Int("rampup", 0, "Ramp-up time in ms (0 = instant)")
	flag.Parse()

	wsURL := fmt.Sprintf("ws://%s%s", *wsHost, *wsPath)
	dur := time.Duration(*duration) * time.Second

	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════╗")
	fmt.Println("║       PyStrands Performance Tester v1.0           ║")
	fmt.Println("╚═══════════════════════════════════════════════════╝")
	fmt.Printf("  Target:     %s\n", wsURL)
	fmt.Printf("  Clients:    %d\n", *numClients)
	fmt.Printf("  Duration:   %ds\n", *duration)
	fmt.Printf("  Mode:       %s\n", *mode)
	fmt.Printf("  Payload:    %d bytes\n", *payloadSize)
	fmt.Println()

	// Handle Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	stats := &Stats{}
	var wg sync.WaitGroup

	// Select client function based on mode
	var clientFn func(string, int, time.Duration, *Stats, *sync.WaitGroup, int)
	switch *mode {
	case "blast":
		clientFn = runBlastClient
	case "pingpong":
		clientFn = runPingPongClient
	default:
		clientFn = runEchoClient
	}

	// Start clients
	log.Printf("Starting %d clients...", *numClients)
	startTime := time.Now()

	rampDelay := time.Duration(*rampUp) * time.Millisecond / time.Duration(*numClients)
	for i := 0; i < *numClients; i++ {
		wg.Add(1)
		go clientFn(wsURL, i, dur, stats, &wg, *payloadSize)
		if rampDelay > 0 {
			time.Sleep(rampDelay)
		}
	}

	// Live stats
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				elapsed := time.Since(startTime).Seconds()
				sent := stats.sent.Load()
				recv := stats.received.Load()
				log.Printf("  [%4.0fs] conn=%d sent=%d (%.0f/s) recv=%d err=%d",
					elapsed, stats.connected.Load(), sent, float64(sent)/elapsed, recv, stats.errors.Load())
			case <-sigChan:
				return
			}
		}
	}()

	// Wait for completion
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-sigChan:
		fmt.Println("\nInterrupted!")
	}

	// Results
	elapsed := time.Since(startTime).Seconds()
	sent := stats.sent.Load()
	recv := stats.received.Load()
	errs := stats.errors.Load()
	conn := stats.connected.Load()

	loss := float64(0)
	if sent > 0 {
		loss = (1.0 - float64(recv)/float64(sent)) * 100
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Printf("  RESULTS: %d clients, %s mode, %.1fs\n", *numClients, *mode, elapsed)
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Printf("  Connected: %d / %d\n", conn, *numClients)
	fmt.Printf("  Sent:      %d (%.0f msg/s)\n", sent, float64(sent)/elapsed)
	fmt.Printf("  Received:  %d (%.0f msg/s)\n", recv, float64(recv)/elapsed)
	fmt.Printf("  Errors:    %d\n", errs)
	if *mode != "blast" {
		fmt.Printf("  Loss:      %.1f%%\n", loss)
		fmt.Printf("  Latency:   p50=%.2fms  p95=%.2fms  p99=%.2fms\n",
			stats.percentile(0.50), stats.percentile(0.95), stats.percentile(0.99))
		fmt.Printf("  Max lat:   %.2fms\n", stats.percentile(1.0))
	}
	fmt.Println("═══════════════════════════════════════════════════════")
	_ = math.Max(0, 0)
}
