#!/bin/bash
# Wrapper: port-forwards Redis, reads secrets from cluster, then runs the MCP server.
# Claude Code invokes this script as the MCP server command.

set -e

NAMESPACE="${K8S_NAMESPACE:-agents}"
LOCAL_PORT="${REDIS_LOCAL_PORT:-6380}"

# Read secrets from cluster (nothing stored locally)
REDIS_PASSWORD=$(kubectl get secret redis-secret -n "$NAMESPACE" \
  -o jsonpath='{.data.password}' | base64 -d)

GITHUB_TOKEN=$(kubectl get secret repo-secret -n "$NAMESPACE" \
  -o jsonpath='{.data.token}' | base64 -d)

# Start Redis port-forward in background
kubectl port-forward svc/redis "$LOCAL_PORT":6379 -n "$NAMESPACE" \
  --address 127.0.0.1 >/tmp/redis-portforward.log 2>&1 &
PF_PID=$!

# Clean up port-forward on exit
trap "kill $PF_PID 2>/dev/null; exit" EXIT INT TERM

# Wait for port-forward to be ready (up to 5s)
for i in $(seq 1 10); do
  sleep 0.5
  nc -z 127.0.0.1 "$LOCAL_PORT" 2>/dev/null && break
done

# Run the MCP server with all required env vars
export REDIS_URL="redis://:${REDIS_PASSWORD}@127.0.0.1:${LOCAL_PORT}"
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"
export GITHUB_TOKEN="$GITHUB_TOKEN"
export GITHUB_REPO="${GITHUB_REPO:-zanetworker/k8s-agents}"

exec "$(dirname "$0")/../bin/k8s-agents-mcp"
