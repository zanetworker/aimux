#!/bin/bash
# test-otel.sh — Run from a SEPARATE terminal (not inside Claude Code).
# Tests whether Claude Code sends OTEL data to a local receiver.
#
# Usage: ./scripts/test-otel.sh

set -e

PORT=14999
SNIFFER_PID=""

cleanup() {
    [ -n "$SNIFFER_PID" ] && kill "$SNIFFER_PID" 2>/dev/null
    wait "$SNIFFER_PID" 2>/dev/null
}
trap cleanup EXIT

echo "=== aimux OTEL diagnostic test ==="
echo ""

# 1. Start a sniffer HTTP server
python3 -c '
from http.server import HTTPServer, BaseHTTPRequestHandler
import datetime, sys

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        cl = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(cl) if cl > 0 else b""
        ts = datetime.datetime.now().strftime("%H:%M:%S")
        print(f"  [{ts}] {self.command} {self.path} cl={cl} ct={self.headers.get(\"Content-Type\",\"?\")}", flush=True)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b"{}")
    def do_GET(self):
        self.do_POST()
    def log_message(self, *a):
        pass

HTTPServer(("127.0.0.1", '"$PORT"'), H).serve_forever()
' &
SNIFFER_PID=$!
sleep 0.5
echo "[1/4] Sniffer listening on port $PORT (PID $SNIFFER_PID)"

# 2. Show the env vars we'll set
echo ""
echo "[2/4] Env vars:"
echo "  CLAUDE_CODE_ENABLE_TELEMETRY=1"
echo "  OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf"
echo "  OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:$PORT"
echo "  OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://localhost:$PORT/v1/logs"
echo "  OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/protobuf"
echo "  OTEL_LOGS_EXPORTER=otlp"
echo "  OTEL_METRICS_EXPORTER=otlp"
echo "  OTEL_METRIC_EXPORT_INTERVAL=2000"
echo "  OTEL_LOGS_EXPORT_INTERVAL=2000"
echo ""

# 3. Run Claude in --print mode
echo "[3/4] Starting Claude Code (--print mode, 15s timeout)..."
echo "  Waiting for OTEL requests..."
echo ""

CLAUDE_CODE_ENABLE_TELEMETRY=1 \
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:$PORT \
OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://localhost:$PORT/v1/logs \
OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/protobuf \
OTEL_LOGS_EXPORTER=otlp \
OTEL_METRICS_EXPORTER=otlp \
OTEL_METRIC_EXPORT_INTERVAL=2000 \
OTEL_LOGS_EXPORT_INTERVAL=2000 \
timeout 15 claude --print "say just the word hello" 2>&1 || true

# 4. Wait for flush
echo ""
echo "[4/4] Waiting 5s for OTEL SDK to flush..."
sleep 5

echo ""
echo "=== Done. If no POST requests appeared above, Claude Code is not sending OTEL data. ==="
