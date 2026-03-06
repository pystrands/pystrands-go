// ws-stress-test.js — High connection count stress test
// Tests the server under extreme connection counts with varying payload sizes.
//
// Usage: k6 run ws-stress-test.js
//        WS_URL=wss://custom.host/ws k6 run ws-stress-test.js

import ws from 'k6/ws';
import { check } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

// Custom metrics
const connectionSuccess = new Rate('connection_success');
const messageThrough = new Counter('message_throughput');
const errorRate = new Rate('error_rate');
const roundtripTime = new Trend('roundtrip_time', true);

const WS_URL = __ENV.WS_URL || 'wss://ws.diwakar.me/ws/perf';

// Payloads: 64 bytes and ~1KB
const PAYLOAD_SMALL = 'x'.repeat(64);
const PAYLOAD_LARGE = 'x'.repeat(1024);

export const options = {
  stages: [
    { duration: '30s', target: 1000 },  // ramp to 1000 VUs
    { duration: '60s', target: 1000 },  // hold 1000 VUs
    { duration: '30s', target: 2000 },  // ramp to 2000 VUs
    { duration: '30s', target: 2000 },  // hold 2000 VUs
    { duration: '10s', target: 0 },     // ramp down
  ],
  thresholds: {
    connection_success: ['rate>0.95'],   // 95%+ connections succeed
    error_rate: ['rate<0.05'],           // <5% error rate
    roundtrip_time: ['p(99)<2000'],      // 99th percentile < 2s
  },
};

export default function () {
  // Alternate between small and large payloads per VU
  const payload = __VU % 2 === 0 ? PAYLOAD_SMALL : PAYLOAD_LARGE;
  const payloadLabel = __VU % 2 === 0 ? '64B' : '1KB';
  let connected = false;

  const res = ws.connect(WS_URL, {}, function (socket) {
    socket.on('open', () => {
      connected = true;
      connectionSuccess.add(1);

      // Send messages every 1s to keep load sustainable at high VU counts
      socket.setInterval(() => {
        const msg = JSON.stringify({
          vu: __VU,
          ts: Date.now(),
          size: payloadLabel,
          data: payload,
        });
        socket.send(msg);
        messageThrough.add(1);
      }, 1000);
    });

    socket.on('message', (msg) => {
      messageThrough.add(1);
      errorRate.add(0); // successful message = no error
      try {
        const data = JSON.parse(msg);
        if (data.ts) {
          roundtripTime.add(Date.now() - data.ts);
        }
      } catch (_) {}
    });

    socket.on('error', (e) => {
      errorRate.add(1);
      if (!connected) connectionSuccess.add(0);
    });

    socket.setTimeout(() => socket.close(), 8000);
  });

  if (!res || res.status !== 101) {
    connectionSuccess.add(0);
    errorRate.add(1);
  }

  check(res, {
    'WebSocket connected': (r) => r && r.status === 101,
  });
}
