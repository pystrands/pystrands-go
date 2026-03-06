#!/usr/bin/env bash
# run-all.sh — Run all k6 WebSocket tests sequentially
# Results are saved to results/ with timestamps.
#
# Usage: ./run-all.sh [WS_URL]
# Example: ./run-all.sh wss://ws.diwakar.me/ws/perf

set -euo pipefail
cd "$(dirname "$0")"

export WS_URL="${1:-${WS_URL:-wss://ws.diwakar.me/ws/perf}}"
RESULTS_DIR="results"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

mkdir -p "$RESULTS_DIR"

echo "============================================"
echo "PyStrands WebSocket Performance Test Suite"
echo "Target: $WS_URL"
echo "Time:   $(date)"
echo "============================================"
echo ""

run_test() {
  local name="$1"
  local script="$2"
  local outfile="$RESULTS_DIR/${name}-${TIMESTAMP}.json"

  echo "▶ Running: $name"
  echo "  Script:  $script"
  echo "  Output:  $outfile"
  echo ""

  k6 run --out json="$outfile" "$script" 2>&1 | tee "$RESULTS_DIR/${name}-${TIMESTAMP}.log"

  echo ""
  echo "✅ $name complete"
  echo "--------------------------------------------"
  echo ""
}

run_test "echo"   "ws-echo-test.js"
run_test "stress" "ws-stress-test.js"
run_test "soak"   "ws-soak-test.js"

echo "============================================"
echo "All tests complete!"
echo "Results in: $RESULTS_DIR/"
echo "============================================"
