// ws-soak-test.js — Long duration soak test
// Sustains 100 VUs for 5 minutes to detect memory leaks, connection drops,
// and throughput degradation over time.
//
// Usage: k6 run ws-soak-test.js
//        WS_URL=wss://custom.host/ws k6 run ws-soak-test.js

import ws from 'k6/ws';
import { check } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const messagesSent = new Counter('messages_sent');
const messagesReceived = new Counter('messages_received');
const connectionDrops = new Counter('connection_drops');
const roundtripTime = new Trend('roundtrip_time', true);
const errorRate = new Rate('error_rate');

const WS_URL = __ENV.WS_URL || 'wss://ws.diwakar.me/ws/perf';

export const options = {
  stages: [
    { duration: '15s', target: 100 },  // ramp up
    { duration: '5m', target: 100 },   // sustained load
    { duration: '15s', target: 0 },    // ramp down
  ],
  thresholds: {
    roundtrip_time: ['p(95)<1000'],
    error_rate: ['rate<0.01'],          // <1% errors over long run
    connection_drops: ['count<50'],     // minimal drops
  },
};

export default function () {
  const res = ws.connect(WS_URL, {}, function (socket) {
    let msgCount = 0;

    socket.on('open', () => {
      // Send a message every 2s — sustainable rate for soak testing
      socket.setInterval(() => {
        msgCount++;
        const msg = JSON.stringify({
          vu: __VU,
          seq: msgCount,
          ts: Date.now(),
          data: 'soak-test-payload-' + 'x'.repeat(128),
        });
        socket.send(msg);
        messagesSent.add(1);
      }, 2000);
    });

    socket.on('message', (msg) => {
      messagesReceived.add(1);
      errorRate.add(0);
      try {
        const data = JSON.parse(msg);
        if (data.ts) {
          roundtripTime.add(Date.now() - data.ts);
        }
      } catch (_) {}
    });

    socket.on('close', () => {
      connectionDrops.add(1);
    });

    socket.on('error', () => {
      errorRate.add(1);
    });

    // Keep connection open for 30s per iteration, then reconnect
    // This lets k6 cycle through iterations during the soak period
    socket.setTimeout(() => socket.close(), 30000);
  });

  check(res, {
    'WebSocket connected': (r) => r && r.status === 101,
  });
}
