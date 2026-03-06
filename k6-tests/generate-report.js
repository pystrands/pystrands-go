#!/usr/bin/env node
// Simple k6 JSON to HTML report generator

const fs = require('fs');
const path = require('path');

const jsonFile = process.argv[2] || 'results/latest.json';
const outputFile = process.argv[3] || jsonFile.replace('.json', '.html');

if (!fs.existsSync(jsonFile)) {
  console.error(`File not found: ${jsonFile}`);
  process.exit(1);
}

// Parse k6 JSON output (newline-delimited JSON)
const lines = fs.readFileSync(jsonFile, 'utf8').trim().split('\n');
const metrics = {};
const dataPoints = [];

for (const line of lines) {
  try {
    const obj = JSON.parse(line);
    if (obj.type === 'Point' && obj.metric) {
      const name = obj.metric;
      if (!metrics[name]) {
        metrics[name] = { values: [], sum: 0, count: 0, min: Infinity, max: -Infinity };
      }
      const val = obj.data?.value;
      if (typeof val === 'number') {
        metrics[name].values.push(val);
        metrics[name].sum += val;
        metrics[name].count++;
        metrics[name].min = Math.min(metrics[name].min, val);
        metrics[name].max = Math.max(metrics[name].max, val);
      }
    }
  } catch (e) {}
}

// Calculate percentiles
function percentile(arr, p) {
  if (arr.length === 0) return 0;
  const sorted = [...arr].sort((a, b) => a - b);
  const idx = Math.ceil(sorted.length * p) - 1;
  return sorted[Math.max(0, idx)];
}

// Generate HTML
const html = `<!DOCTYPE html>
<html>
<head>
  <title>k6 Load Test Report - PyStrands</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; margin: 40px; background: #f5f5f5; }
    .container { max-width: 1200px; margin: 0 auto; }
    h1 { color: #333; border-bottom: 3px solid #7c3aed; padding-bottom: 10px; }
    h2 { color: #555; margin-top: 30px; }
    .card { background: white; border-radius: 8px; padding: 20px; margin: 15px 0; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
    .metrics-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(250px, 1fr)); gap: 15px; }
    .metric { padding: 15px; border-left: 4px solid #7c3aed; background: #faf5ff; }
    .metric-name { font-weight: 600; color: #333; margin-bottom: 5px; }
    .metric-value { font-size: 24px; color: #7c3aed; }
    .metric-details { font-size: 12px; color: #666; margin-top: 5px; }
    .summary { font-size: 18px; line-height: 1.6; }
    table { width: 100%; border-collapse: collapse; margin-top: 15px; }
    th, td { padding: 10px; text-align: left; border-bottom: 1px solid #eee; }
    th { background: #7c3aed; color: white; }
    tr:hover { background: #f9f9f9; }
    .good { color: #10b981; }
    .warn { color: #f59e0b; }
    .bad { color: #ef4444; }
    .timestamp { color: #999; font-size: 12px; }
  </style>
</head>
<body>
  <div class="container">
    <h1>🚀 PyStrands Load Test Report</h1>
    <p class="timestamp">Generated: ${new Date().toISOString()}</p>
    
    <div class="card">
      <h2>📊 Key Metrics</h2>
      <div class="metrics-grid">
        ${['ws_msgs_received', 'ws_msgs_sent', 'roundtrip_time', 'ws_connecting', 'ws_sessions'].map(name => {
          const m = metrics[name];
          if (!m || m.count === 0) return '';
          const avg = m.sum / m.count;
          const p95 = percentile(m.values, 0.95);
          return `
            <div class="metric">
              <div class="metric-name">${name.replace(/_/g, ' ')}</div>
              <div class="metric-value">${name.includes('time') || name.includes('connecting') ? avg.toFixed(2) + 'ms' : m.count.toLocaleString()}</div>
              <div class="metric-details">
                ${name.includes('time') || name.includes('connecting') 
                  ? `min: ${m.min.toFixed(2)}ms | p95: ${p95.toFixed(2)}ms | max: ${m.max.toFixed(2)}ms`
                  : `avg: ${avg.toFixed(2)} | min: ${m.min} | max: ${m.max}`}
              </div>
            </div>
          `;
        }).join('')}
      </div>
    </div>

    <div class="card">
      <h2>📈 All Metrics</h2>
      <table>
        <tr><th>Metric</th><th>Count</th><th>Avg</th><th>Min</th><th>Max</th><th>p95</th></tr>
        ${Object.entries(metrics)
          .filter(([_, m]) => m.count > 0)
          .sort((a, b) => b[1].count - a[1].count)
          .map(([name, m]) => {
            const avg = m.sum / m.count;
            const p95 = percentile(m.values, 0.95);
            return `<tr>
              <td><strong>${name}</strong></td>
              <td>${m.count.toLocaleString()}</td>
              <td>${avg.toFixed(2)}</td>
              <td>${m.min === Infinity ? '-' : m.min.toFixed(2)}</td>
              <td>${m.max === -Infinity ? '-' : m.max.toFixed(2)}</td>
              <td>${p95.toFixed(2)}</td>
            </tr>`;
          }).join('')}
      </table>
    </div>

    <div class="card">
      <h2>ℹ️ Test Info</h2>
      <p>Source: <code>${jsonFile}</code></p>
      <p>Total data points: ${lines.length.toLocaleString()}</p>
    </div>
  </div>
</body>
</html>`;

fs.writeFileSync(outputFile, html);
console.log(`Report generated: ${outputFile}`);
