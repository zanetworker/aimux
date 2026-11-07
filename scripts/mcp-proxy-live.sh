#!/usr/bin/env bash
# Live check: probes the owner's real MCP servers through the proxy.
# This runs only with explicit approval, reads ~/.claude.json read-only,
# changes nothing, and reports which servers work and which fail.
#
# Usage: scripts/mcp-proxy-live.sh
# Needs: go, nc, jq. Uses port 17462 on 127.0.0.1.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
mkdir -p "$WORK"
pids=()
# Every process this script starts is tracked: `aimux mcp proxy refresh` and
# `serve` stop the MCP servers they started when they get SIGTERM, so wait
# for them to finish before removing their files.
cleanup() {
  for p in "${pids[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null || true; done
  for p in "${pids[@]:-}"; do [[ -n "$p" ]] && wait "$p" 2>/dev/null || true; done
  rm -rf "$WORK"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# Build aimux to temp dir
go build -o "$WORK/aimux" "$ROOT/cmd/aimux"

# Copy real ~/.claude.json to temp (read-only)
HOME_DIR="${HOME:-~}"
REAL_CLAUDE_CONFIG="$HOME_DIR/.claude.json"
if [[ ! -f "$REAL_CLAUDE_CONFIG" ]]; then
  echo "No ~/.claude.json found" >&2
  exit 1
fi
cp "$REAL_CLAUDE_CONFIG" "$WORK/claude.json"
# After `aimux mcp proxy install` the moved servers live in the proxy's saved
# definitions, not in ~/.claude.json: copy those too (read-only).
if [[ -f "$HOME_DIR/.aimux/mcp-servers.json" ]]; then
  cp "$HOME_DIR/.aimux/mcp-servers.json" "$WORK/servers.json"
fi

# Temp paths for proxy state
P=(--claude-config "$WORK/claude.json" --servers "$WORK/servers.json" --catalog "$WORK/catalog.json")

wait_port() {
  for _ in $(seq 1 120); do
    nc -z 127.0.0.1 "$1" 2>/dev/null && return 0
    sleep 0.5
  done
  echo "port $1 never opened" >&2
  return 1
}

# Refresh the catalog (starts each server once); a server that fails here is
# still reported below by the probe, so a failure does not stop the check.
# It runs in the background so a cancel reaches the trap, which stops it
# (and through it the servers it started).
"$WORK/aimux" mcp proxy refresh "${P[@]}" > "$WORK/refresh.log" 2>&1 &
refresh=$!
pids+=("$refresh")
wait "$refresh" || true
pids=("${pids[@]/$refresh}") # done: never signal a pid that may be reused

# Start proxy on a port nothing else holds, and make sure the proxy that
# answers is the one started here (never another aimux proxy).
if nc -z 127.0.0.1 17462 2>/dev/null; then
  echo "Port 17462 is already in use; stop whatever listens there and rerun" >&2
  exit 1
fi
"$WORK/aimux" mcp proxy serve "${P[@]}" --port 17462 > "$WORK/proxy.log" 2>&1 &
child=$!
pids+=("$child")
# serve refuses an empty catalog, which happens when every server failed to
# list its tools: report those failures instead of a port timeout.
for _ in $(seq 1 120); do
  nc -z 127.0.0.1 17462 2>/dev/null && break
  if ! kill -0 "$child" 2>/dev/null; then
    echo "The proxy could not start (no server listed its tools). Refresh said:" >&2
    LC_ALL=C tr -d '\000-\010\013-\037\177' < "$WORK/refresh.log" | head -n 40 >&2
    exit 1
  fi
  sleep 0.5
done
wait_port 17462 || exit 1
answering=$("$WORK/aimux" mcp proxy status --json --port 17462 2>/dev/null | jq -r '.pid // 0')
if [[ "$answering" != "$child" ]]; then
  echo "The proxy on port 17462 is not the one this check started (pid $answering, expected $child)" >&2
  exit 1
fi

# Probe every server through the proxy: the daemon starts each one and lists
# its tools. Error text comes from the proxy's status, already scrubbed of
# env values and tokens; nothing else from a server is printed.
if ! "$WORK/aimux" mcp proxy status --probe --json --port 17462 --timeout 4m > "$WORK/probe.json" 2> "$WORK/probe.err"; then
  echo "The proxy probe did not answer; see the proxy log (kept until exit)" >&2
  exit 1
fi

jq -r 'def printable: [explode[] | if . < 32 or . == 127 then 32 else . end] | implode;
  .servers[] | if ((.probeError // "") == "" and (.state == "running" or .state == "idle_stopped"))
    then "ok \(.server | printable) \(.tools)"
    else "failed \(.server | printable) \(((.probeError // "") | if . == "" then null else . end) // .lastError // .state | split("\n")[0] | printable)"
  end' "$WORK/probe.json"

ok_count=$(jq '[.servers[] | select((.probeError // "") == "" and (.state == "running" or .state == "idle_stopped"))] | length' "$WORK/probe.json")
total=$(jq '.servers | length' "$WORK/probe.json")
echo "$ok_count ok, $((total - ok_count)) failed"
exit 0
