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

// Go load tester for PyStrands — pushes the server to its limits

type Stats struct {
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

type LoadMessage struct {
	ID   string  `json:"id"`
	Ts   float64 `json:"ts"`
	Data string  `json:"d"`
}

func runClient(wsURL string, clientID int, duration time.Duration, stats *Stats, wg *sync.WaitGroup, mode string) {
	defer wg.Done()
	defer func() {
		if r := recover(); r != nil {
			stats.errors.Add(1)
		}
	}()

	u, _ := url.Parse(wsURL)
	dialer := websocket.Dialer{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
	}

	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		stats.errors.Add(1)
		return
	}
	defer conn.Close()

	deadline := time.Now().Add(duration)
	payload := "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" // 36 bytes

	if mode == "blast" {
		// Fire-and-forget: alternate send/recv rapidly
		// gorilla/websocket doesn't support concurrent read+write on same conn
		// So we do rapid non-blocking style: send N, then try recv, repeat
		msgID := 0
		burstSize := 10
		for time.Now().Before(deadline) {
			// Send burst
			for i := 0; i < burstSize && time.Now().Before(deadline); i++ {
				msg := LoadMessage{
					ID:   fmt.Sprintf("%d-%d", clientID, msgID),
					Ts:   float64(time.Now().UnixMicro()) / 1e6,
					Data: payload,
				}
				data, _ := json.Marshal(msg)
				conn.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
				if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
					stats.errors.Add(1)
					return
				}
				stats.sent.Add(1)
				msgID++
			}
			// Drain recv
			for {
				conn.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
				_, resp, err := conn.ReadMessage()
				if err != nil {
					break // timeout or error, go back to sending
				}
				stats.received.Add(1)
				var lm LoadMessage
				if json.Unmarshal(resp, &lm) == nil && lm.Ts > 0 {
					lat := (float64(time.Now().UnixMicro())/1e6 - lm.Ts) * 1000
					if lat > 0 && lat < 30000 {
						stats.addLatency(lat)
					}
				}
			}
		}
		// Final drain
		for i := 0; i < 100; i++ {
			conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			_, resp, err := conn.ReadMessage()
			if err != nil {
				break
			}
			stats.received.Add(1)
			var lm LoadMessage
			if json.Unmarshal(resp, &lm) == nil && lm.Ts > 0 {
				lat := (float64(time.Now().UnixMicro())/1e6 - lm.Ts) * 1000
				if lat > 0 && lat < 30000 {
					stats.addLatency(lat)
				}
			}
		}

	} else {
		// Sequential: send → wait → recv → send
		msgID := 0
		for time.Now().Before(deadline) {
			msg := LoadMessage{
				ID:   fmt.Sprintf("%d-%d", clientID, msgID),
				Ts:   float64(time.Now().UnixMicro()) / 1e6,
				Data: payload,
			}
			data, _ := json.Marshal(msg)
			conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				stats.errors.Add(1)
				break
			}
			stats.sent.Add(1)

			conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			_, resp, err := conn.ReadMessage()
			if err != nil {
				continue
			}
			stats.received.Add(1)
			var lm LoadMessage
			if json.Unmarshal(resp, &lm) == nil && lm.Ts > 0 {
				lat := (float64(time.Now().UnixMicro())/1e6 - lm.Ts) * 1000
				if lat > 0 && lat < 30000 {
					stats.addLatency(lat)
				}
			}
			msgID++
		}
	}
}

func main() {
	wsHost := flag.String("host", "localhost:8082", "WebSocket host:port")
	numClients := flag.Int("clients", 100, "Number of concurrent clients")
	duration := flag.Int("duration", 15, "Test duration in seconds")
	mode := flag.String("mode", "blast", "Test mode: blast or sequential")
	batchSize := flag.Int("batch", 100, "Connection batch size")
	flag.Parse()

	wsURL := fmt.Sprintf("ws://%s/ws/loadtest", *wsHost)
	dur := time.Duration(*duration) * time.Second

	log.Printf("PyStrands Load Tester")
	log.Printf("  Target:   %s", wsURL)
	log.Printf("  Clients:  %d", *numClients)
	log.Printf("  Duration: %ds", *duration)
	log.Printf("  Mode:     %s", *mode)

	// Handle Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	stats := &Stats{}

	// Connect clients in batches
	log.Printf("Connecting %d clients...", *numClients)
	connectStart := time.Now()

	var wg sync.WaitGroup
	connected := atomic.Int64{}

	for batch := 0; batch < *numClients; batch += *batchSize {
		batchEnd := batch + *batchSize
		if batchEnd > *numClients {
			batchEnd = *numClients
		}

		var batchWg sync.WaitGroup
		for i := batch; i < batchEnd; i++ {
			wg.Add(1)
			batchWg.Add(1)
			go func(id int) {
				defer batchWg.Done()
				connected.Add(1)
				runClient(wsURL, id, dur, stats, &wg, *mode)
			}(i)
		}
		// Small delay between batches
		time.Sleep(50 * time.Millisecond)
	}

	connectTime := time.Since(connectStart)
	log.Printf("Connected %d clients in %v", connected.Load(), connectTime)

	// Live stats printer
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			log.Printf("  [live] sent=%d recv=%d errors=%d",
				stats.sent.Load(), stats.received.Load(), stats.errors.Load())
		}
	}()

	// Wait for completion or Ctrl+C
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-sigChan:
		log.Println("Interrupted!")
	}

	// Results
	sent := stats.sent.Load()
	recv := stats.received.Load()
	errs := stats.errors.Load()
	durationSec := float64(*duration)
	loss := float64(0)
	if sent > 0 {
		loss = (1.0 - float64(recv)/float64(sent)) * 100
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Printf("  RESULTS: %d clients, %s mode, %ds\n", *numClients, *mode, *duration)
	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Printf("  Sent:      %d (%.0f msg/s)\n", sent, float64(sent)/durationSec)
	fmt.Printf("  Received:  %d (%.0f msg/s)\n", recv, float64(recv)/durationSec)
	fmt.Printf("  Errors:    %d\n", errs)
	fmt.Printf("  Loss:      %.1f%%\n", loss)
	fmt.Printf("  Latency:   p50=%.1fms  p95=%.1fms  p99=%.1fms\n",
		stats.percentile(0.50), stats.percentile(0.95), stats.percentile(0.99))
	fmt.Printf("  Max lat:   %.1fms\n", stats.percentile(1.0))
	_ = math.Max(0, 0) // keep math import
	fmt.Println("═══════════════════════════════════════════════════")
}
