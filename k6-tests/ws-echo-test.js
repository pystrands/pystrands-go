// ws-echo-test.js — Basic WebSocket echo throughput test
// Measures round-trip latency and message throughput under moderate load.
//
// Usage: k6 run ws-echo-test.js
//        WS_URL=wss://custom.host/ws k6 run ws-echo-test.js

import ws from 'k6/ws';
import { check } from 'k6';
import { Counter, Trend } from 'k6/metrics';

// Custom metrics
const messagesSent = new Counter('messages_sent');
const messagesReceived = new Counter('messages_received');
const roundtripTime = new Trend('roundtrip_time', true); // in ms

const WS_URL = __ENV.WS_URL || 'wss://ws.diwakar.me/ws/perf';

export const options = {
  stages: [
    { duration: '10s', target: 50 },   // ramp up to 50 VUs
    { duration: '30s', target: 50 },   // hold 50 VUs
    { duration: '10s', target: 200 },  // ramp to 200 VUs
    { duration: '30s', target: 200 },  // hold 200 VUs
    { duration: '10s', target: 0 },    // ramp down
  ],
  thresholds: {
    roundtrip_time: ['p(95)<500'],      // 95th percentile < 500ms
    messages_received: ['count>1000'],   // at least 1000 messages echoed
  },
};

export default function () {
  const res = ws.connect(WS_URL, {}, function (socket) {
    socket.on('open', () => {
      // Send a message every 500ms
      socket.setInterval(() => {
        const payload = JSON.stringify({
          vu: __VU,
          iter: __ITER,
          ts: Date.now(),
          data: 'echo-test',
        });
        socket.send(payload);
        messagesSent.add(1);
      }, 500);
    });

    socket.on('message', (msg) => {
      messagesReceived.add(1);
      try {
        const data = JSON.parse(msg);
        if (data.ts) {
          roundtripTime.add(Date.now() - data.ts);
        }
      } catch (_) {
        // non-JSON response, ignore
      }
    });

    socket.on('error', (e) => {
      console.error(`VU ${__VU} error: ${e.error()}`);
    });

    // Keep connection alive for the iteration
    socket.setTimeout(() => socket.close(), 5000);
  });

  check(res, {
    'WebSocket connected': (r) => r && r.status === 101,
  });
}
