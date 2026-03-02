#!/bin/bash
# Quick check if the aimux OTEL receiver is running and has data.
# Usage: ./scripts/check-otel.sh

PORT=${1:-4318}
ENDPOINT="http://localhost:$PORT/v1/traces"

echo "Checking OTEL receiver on port $PORT..."

# Check if receiver is listening
STATUS=$(curl -s -X POST "$ENDPOINT" -d '' -o /dev/null -w "%{http_code}" 2>/dev/null)
if [ "$STATUS" = "200" ]; then
    echo "  Receiver: RUNNING (port $PORT)"
else
    echo "  Receiver: NOT RUNNING (got status $STATUS)"
    echo ""
    echo "  Make sure aimux is running with otel.enabled: true in ~/.aimux/config.yaml"
    exit 1
fi

echo ""
echo "To test OTEL tracing:"
echo ""
echo "  1. In aimux, press :new and launch an agent with Tracing: ON"
echo "  2. Use the agent (type a prompt, let it respond)"
echo "  3. The trace header should change from [FILE] to [OTEL]"
echo ""
echo "Or manually start Claude with OTEL:"
echo ""
echo "  CLAUDE_CODE_ENABLE_TELEMETRY=1 \\"
echo "  OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \\"
echo "  OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:$PORT \\"
echo "  claude"
echo ""
echo "Or Codex:"
echo ""
echo "  OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \\"
echo "  OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:$PORT codex"
echo ""
echo "Or Gemini:"
echo ""
echo "  GEMINI_CLI_TELEMETRY_ENABLED=true \\"
echo "  OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \\"
echo "  OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:$PORT \\"
echo "  gemini"
echo ""
echo "NOTE: http/protobuf protocol is required (port 4318)."
echo "      gRPC (port 4317) is NOT supported by aimux's receiver."
