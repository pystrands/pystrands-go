# PyStrands Performance Benchmarks

**Test Date:** February 15, 2026  
**Hardware:** Apple Mac Mini M4, 10-core, 16GB RAM  
**Go Version:** 1.26.0  
**OS:** macOS 15.3 (Tahoe)

## Test Setup

- **Server:** Pure Go WebSocket echo server (`cmd/echoserver`)
- **Client:** Go performance tester (`cmd/perftest`)
- **Protocol:** WebSocket (gorilla/websocket)
- **Payload:** 64 bytes JSON messages

## Results Summary

| Test | Clients | Mode | Duration | Msg/s (sent) | Msg/s (recv) | p50 lat | p99 lat | Loss |
|------|---------|------|----------|--------------|--------------|---------|---------|------|
| Baseline | 100 | pingpong | 10s | 112,879 | 112,879 | 0.87ms | 1.39ms | 0% |
| Scale Up | 1,000 | pingpong | 15s | 110,560 | 110,560 | 9.01ms | 9.58ms | 0% |
| Stress | 5,000 | pingpong | 17s | 100,859 | 98,275 | 35ms | 229ms | 2.6% |
| Async | 500 | echo | 15s | 92,918 | 60,962 | 0.07ms | 7.21ms | 34% |
| Blast | 100 | blast | 0.8s | 651,916 | - | - | - | 100% |

## Key Findings

### Throughput
- **Sustained throughput:** ~100-112K msg/sec round-trip
- **Peak ingestion:** 650K+ msg/sec (fire-and-forget, server overwhelmed)
- **Connection handling:** 5,000 concurrent connections stable

### Latency
- **100 clients:** p50 = 0.87ms, p99 = 1.39ms (sub-millisecond median)
- **1,000 clients:** p50 = 9ms, p99 = 9.6ms (consistent ~10ms)
- **5,000 clients:** p50 = 35ms, p99 = 229ms (degradation under extreme load)

### Reliability
- **0% loss** up to 1,000 concurrent clients
- **2.6% loss** at 5,000 clients (timeouts under load)
- Echo mode (async) shows 34% loss when server can't keep up with backpressure

## Comparison to Previous VPS Benchmark

| Metric | VPS (2-CPU) | Mac Mini M4 | Improvement |
|--------|-------------|-------------|-------------|
| Ingestion | 191K msg/s | 650K+ msg/s | ~3.4x |
| E2E throughput | 5.5K msg/s | 112K msg/s | ~20x |
| CPU cores | 2 | 10 | 5x |

## Running the Tests

```bash
# Build tools
cd pystrands-go
go build -o echoserver ./cmd/echoserver/
go build -o perftest ./cmd/perftest/

# Start echo server
./echoserver -port 8082

# Run performance test
./perftest -host localhost:8082 -clients 100 -duration 10 -mode pingpong

# Modes:
#   pingpong - strict request-response (measures true latency)
#   echo     - async send with concurrent receive
#   blast    - fire-and-forget (tests max ingestion)
```

## Notes

- **Pingpong mode** is the most reliable measure of real-world performance
- **Echo mode** shows backpressure issues when server can't keep up
- **Blast mode** tests raw write capacity but overwhelms most servers
- Mac Mini M4 provides excellent single-machine performance
- For production, test with actual Python backends connected via TCP
