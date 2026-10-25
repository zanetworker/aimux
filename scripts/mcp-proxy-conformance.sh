#!/usr/bin/env bash
# Runs the official MCP conformance suite against the reference "everything"
# server twice: over its own HTTP transport, and over stdio behind
# `aimux mcp proxy`. Scenarios that pass directly but fail through the proxy
# are features the proxy drops.
#
# Usage: scripts/mcp-proxy-conformance.sh [workdir]
# Needs: go, npx, nc. Uses ports 17460 and 17461 on 127.0.0.1.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${1:-$(mktemp -d)}"
mkdir -p "$WORK"
SPEC=2025-11-25
EVERYTHING=(npx -y @modelcontextprotocol/server-everything)
pids=()
cleanup() { for p in "${pids[@]:-}"; do kill "$p" 2>/dev/null || true; done; }
trap cleanup EXIT

go build -o "$WORK/aimux" "$ROOT/cmd/aimux"
cat > "$WORK/claude.json" <<JSON
{"mcpServers":{"everything":{"command":"npx","args":["-y","@modelcontextprotocol/server-everything"]}}}
JSON

wait_port() { for _ in $(seq 1 120); do nc -z 127.0.0.1 "$1" 2>/dev/null && return 0; sleep 0.5; done; echo "port $1 never opened" >&2; return 1; }

run_suite() { # name url
  (cd "$WORK" && npx -y @modelcontextprotocol/conformance server --url "$2" --spec-version "$SPEC" > "$WORK/$1.txt" 2>&1) || true
  echo "$1: $(grep '^Total:' "$WORK/$1.txt" || echo 'no summary, see the log')"
}

# Direct: the reference server's own Streamable HTTP transport.
PORT=17460 "${EVERYTHING[@]}" streamableHttp > "$WORK/direct-server.log" 2>&1 &
pids+=($!)
wait_port 17460
run_suite direct http://127.0.0.1:17460/mcp

# Through aimux.
# Every path under $WORK, so the run never reads or writes the real
# ~/.aimux/mcp-servers.json or catalog.
P=(--claude-config "$WORK/claude.json" --servers "$WORK/servers.json" --catalog "$WORK/catalog.json")
"$WORK/aimux" mcp proxy refresh "${P[@]}" > "$WORK/refresh.log"
"$WORK/aimux" mcp proxy serve "${P[@]}" --port 17461 > "$WORK/proxy.log" 2>&1 &
pids+=($!)
wait_port 17461
run_suite proxy http://127.0.0.1:17461/mcp

# Scenarios that pass directly but fail through the proxy.
passed() { grep -E '^✓ ' "$WORK/$1.txt" | sed -E 's/^✓ ([^:]+):.*/\1/' | sort; }
failed() { grep -E '^✗ ' "$WORK/$1.txt" | sed -E 's/^✗ ([^:]+):.*/\1/' | sort; }
echo "Dropped by the proxy (pass directly, fail through it):"
comm -12 <(passed direct) <(failed proxy) | sed 's/^/  /'
echo "Results and logs: $WORK"
