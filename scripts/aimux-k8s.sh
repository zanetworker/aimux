#!/bin/bash
# Start aimux with K8s support.
# Port-forwards Redis from the cluster so the K8s provider can connect,
# then starts aimux. Kills the port-forward when aimux exits.

set -e

NAMESPACE="${K8S_NAMESPACE:-agents}"
LOCAL_PORT="${REDIS_LOCAL_PORT:-6380}"

# Start Redis port-forward in background
kubectl port-forward svc/redis "$LOCAL_PORT":6379 -n "$NAMESPACE" \
  --address 127.0.0.1 >/tmp/redis-portforward.log 2>&1 &
PF_PID=$!

cleanup() {
  kill "$PF_PID" 2>/dev/null
}
trap cleanup EXIT INT TERM

# Wait for port-forward to be ready
for i in $(seq 1 10); do
  sleep 0.5
  nc -z 127.0.0.1 "$LOCAL_PORT" 2>/dev/null && break
done

# Run aimux
"$(dirname "$0")/../aimux" "$@"
