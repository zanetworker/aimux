# Kubernetes Quickstart

Run AI coding agents on Kubernetes, controlled from your laptop via aimux.

## Zero-Setup Path (fastest)

Just point at a cluster. Aimux auto-creates everything on first spawn:

```bash
# 1. Enable K8s in aimux config
cat >> ~/.aimux/config.yaml << 'EOF'
kubernetes:
  enabled: true
  namespace: "agents"
EOF

# 2. Make sure your auth env vars are set (Vertex AI or API key)
# Vertex AI:
export CLAUDE_CODE_USE_VERTEX=1
export CLOUD_ML_REGION=us-east5
export ANTHROPIC_VERTEX_PROJECT_ID=your-project

# Or API key:
export ANTHROPIC_API_KEY=sk-ant-...

# 3. Run aimux → :new → Session → Remote → claude
aimux
```

Aimux auto-creates the `agents` namespace, auth secrets from your env vars, and the deployment. No `kubectl apply` needed.

## Full Setup (with Redis coordination)

For Hybrid mode (local Claude dispatching tasks to K8s workers), you need Redis.

### Prerequisites

- A Kubernetes cluster (any: kind, minikube, EKS, GKE, OpenShift)
- `kubectl` configured and pointing at your cluster
- Go 1.24+ (to build the MCP server)
- Docker or Podman (only if building custom images)

## Architecture (30-second version)

```
┌──────────────────────┐         ┌──────────────────────────────────┐
│  Your laptop         │         │  Kubernetes cluster              │
│                      │         │                                  │
│  aimux (TUI)         │         │  ┌───────┐    ┌──────────────┐  │
│    + K8s provider    │◄───────►│  │ Redis │◄──►│ Agent pods   │  │
│                      │         │  │       │    │ (0..N each)  │  │
│  Claude Code         │         │  └───────┘    └──────────────┘  │
│    + MCP server      │─────────│───► K8s API (scale deploys)     │
└──────────────────────┘  redis  └──────────────────────────────────┘
                          + k8s API
```

- **MCP server** runs on your laptop. Claude Code calls it to spawn agents, create tasks, and scale down.
- **Redis** is the coordination bus. Agents register heartbeats, claim tasks, and report results through it.
- **Agent pods** start at 0 replicas. The MCP `spawn_agent` tool scales them up on demand. They auto-register in Redis and start claiming tasks.
- **aimux** discovers K8s agents via Redis heartbeats and shows them alongside local agents in the same table.

## Step 1: Deploy Redis

Create the namespace and secrets, then deploy Redis.

```bash
kubectl create namespace agents
```

Create the three required secrets (substitute your real values):

```bash
kubectl create secret generic redis-secret \
  --from-literal=password=YOUR_REDIS_PASSWORD \
  -n agents

kubectl create secret generic llm-keys \
  --from-literal=anthropic=sk-ant-YOUR_KEY \
  -n agents

kubectl create secret generic repo-secret \
  --from-literal=token=ghp_YOUR_GITHUB_PAT \
  --from-literal=host=github.com/youruser/yourrepo.git \
  -n agents
```

Deploy Redis:

```bash
kubectl apply -f deploy/k8s/redis.yaml
```

Verify it is running:

```bash
kubectl get pods -n agents -l app=redis
```

Get the external endpoint (LoadBalancer):

```bash
kubectl get svc redis -n agents
```

The `EXTERNAL-IP` column shows the address. If using `kind` or `minikube`, use port-forward instead:

```bash
kubectl port-forward svc/redis 6379:6379 -n agents
```

Test connectivity:

```bash
redis-cli -h <EXTERNAL-IP> -a YOUR_REDIS_PASSWORD ping
# Expected: PONG
```

## Step 2: Deploy Agent Workers

Apply RBAC, network policy, and agent deployments:

```bash
kubectl apply -f deploy/k8s/rbac.yaml
kubectl apply -f deploy/k8s/networkpolicy.yaml
kubectl apply -f deploy/k8s/agent-claude-coder.yaml
kubectl apply -f deploy/k8s/agent-claude-reviewer.yaml
kubectl apply -f deploy/k8s/agent-claude-researcher.yaml
```

Or apply everything at once:

```bash
kubectl apply -f deploy/k8s/
```

All deployments start at **0 replicas**. Nothing runs (and nothing costs money) until you explicitly spawn agents.

Verify the deployments exist:

```bash
kubectl get deployments -n agents -l app.kubernetes.io/part-of=k8s-agents
```

You should see `agent-claude-coder`, `agent-claude-reviewer`, `agent-claude-researcher`, and `redis`, all with `0/0` ready (except Redis at `1/1`).

## Step 3: Build Agent Images (optional)

Skip this if using the pre-built images at `quay.io/azaalouk/agent-claude:latest`.

Build the Claude worker image:

```bash
docker build -t agent-claude:latest -f runtime/agents/claude/Dockerfile .
```

Build the Gemini worker image (uses a separate Dockerfile and requires a `google` key in the `llm-keys` secret):

```bash
docker build -t agent-gemini:latest -f runtime/agents/gemini/Dockerfile .
```

Push to your registry:

```bash
docker tag agent-claude:latest your-registry.com/agent-claude:latest
docker push your-registry.com/agent-claude:latest
```

Then update the `image:` field in the deployment YAMLs to point at your registry.

## Step 4: Set Up the MCP Server

Build the MCP server binary:

```bash
go build -o bin/k8s-agents-mcp ./cmd/mcp/
```

Register it in Claude Code's settings (`~/.claude/settings.json`):

```json
{
  "mcpServers": {
    "k8s-agents": {
      "command": "/absolute/path/to/bin/k8s-agents-mcp",
      "env": {
        "REDIS_URL": "redis://:YOUR_REDIS_PASSWORD@REDIS_HOST:6379",
        "KUBECONFIG": "/Users/you/.kube/config",
        "K8S_NAMESPACE": "agents",
        "TEAM_ID": "my-team",
        "MAX_AGENTS": "20",
        "MAX_COST_USD": "100"
      }
    }
  }
}
```

**Environment variables:**

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `REDIS_URL` | Yes | `redis://localhost:6379` | Redis connection string with password |
| `KUBECONFIG` | Yes (local) | in-cluster | Path to kubeconfig file |
| `K8S_NAMESPACE` | No | `agents` | Namespace where agent deployments live |
| `TEAM_ID` | No | `default` | Redis key prefix for team isolation |
| `MAX_AGENTS` | No | `20` | Hard cap on concurrent agent pods |
| `MAX_COST_USD` | No | `100` | Cost warning threshold |
| `GITHUB_TOKEN` | No | | For `cleanup_branches` tool |
| `GITHUB_REPO` | No | | `owner/repo` for branch cleanup |

Verify it works by restarting Claude Code and asking it to run the `list_agents` tool. It should return "No agents running".

## Step 5: Configure aimux

Add the `kubernetes` section to `~/.aimux/config.yaml`:

```yaml
providers:
  claude:
    enabled: true
  codex:
    enabled: true

kubernetes:
  enabled: true
  redis_url: "redis://:YOUR_REDIS_PASSWORD@REDIS_HOST:6379"
  team_id: "my-team"
  namespace: "agents"
  kubeconfig: "~/.kube/config"
```

| Field | Description |
|-------|-------------|
| `enabled` | Turns on K8s agent discovery in aimux |
| `redis_url` | Same Redis URL as the MCP server |
| `team_id` | Must match the MCP server's `TEAM_ID` |
| `namespace` | K8s namespace for agent deployments |
| `kubeconfig` | Path to kubeconfig (omit for in-cluster) |
| `otel_endpoint` | Optional. OTEL collector URL for remote agent traces |

## Step 6: Use It

Start aimux:

```bash
aimux
```

### Spawn agents and create tasks

In Claude Code (not aimux), tell Claude to use the MCP tools:

> "Spawn 2 Claude coders and have them implement feature X and feature Y in parallel."

Claude will call `spawn_agent` and `create_task` via the MCP server. The agents scale up, register in Redis, claim tasks, and report results.

### Watch agents in aimux

K8s agents appear in the agent table with a `K8S` badge in the LOC column. They show provider, role, status (active/idle/dead), and current task.

### View tasks

Press `T` from the agent list to see the tasks view showing all pending, running, and completed tasks.

### Check costs

Press `c` from the agent list. K8s agent costs are tracked via Redis token counters alongside local agent costs.

### Scale down

When work is complete, tell Claude:

> "Scale down the Claude coders."

Claude calls `scale_down`, which sets replicas to 0. Pods terminate, costs stop.

You can also scale down manually:

```bash
kubectl scale deployment agent-claude-coder --replicas=0 -n agents
```

## Remote Sessions (optional)

Remote sessions give you a full Claude Code CLI running inside a K8s pod, with your repo pre-cloned and optional MCP tools for spawning worker pods.

### Build the session image

```bash
# Build the MCP server binary first (it gets bundled into the image)
GOOS=linux GOARCH=amd64 go build -o bin/k8s-agents-mcp ./cmd/mcp/
cp bin/k8s-agents-mcp runtime/agents/session/k8s-agents-mcp

# Create the Claude settings for the session pod
cat > runtime/agents/session/claude-settings.json << 'EOF'
{
  "mcpServers": {
    "k8s-agents": {
      "command": "/usr/local/bin/k8s-agents-mcp"
    }
  }
}
EOF

docker build -t claude-session:latest -f runtime/agents/session/Dockerfile runtime/agents/session/
docker push your-registry.com/claude-session:latest
```

### Deploy the session pod

```bash
kubectl apply -f deploy/k8s/agent-claude-session.yaml
```

This creates a Deployment at 0 replicas. The pod uses an init container to clone your repo into `/workspace`.

### Start a remote session

From aimux, use `:new` and select **Session** > **Remote (pod)**. aimux scales up the session deployment, waits for the pod, and attaches via `kubectl exec` into a tmux session running Claude Code.

### Disconnect and reconnect

Close the aimux pane or press `Esc`. The pod keeps running. Reopen the agent in aimux to reattach to the same tmux session. The conversation continues where you left off.

To stop the pod and save costs:

```bash
kubectl scale deployment agent-claude-session --replicas=0 -n agents
```

## Troubleshooting

### Redis not reachable

```bash
# Check the Redis pod is running
kubectl get pods -n agents -l app=redis

# Check the service has an endpoint
kubectl get endpoints redis -n agents

# Test from inside the cluster
kubectl run redis-test --rm -it --image=redis:7-alpine -n agents -- \
  redis-cli -h redis -a YOUR_REDIS_PASSWORD ping
```

### Agents not registering

```bash
# Check if agent pods are running
kubectl get pods -n agents -l team-component=agent

# Check agent logs for Redis connection errors
kubectl logs -n agents -l app=agent-claude-coder --tail=50

# Verify agents appear in Redis
redis-cli -h REDIS_HOST -a YOUR_REDIS_PASSWORD HGETALL team:my-team:heartbeat
```

### MCP server errors

```bash
# Check Claude Code's MCP server logs
# On macOS:
cat ~/Library/Logs/Claude/mcp-server-k8s-agents.log

# Verify the binary runs standalone
REDIS_URL=redis://:pass@localhost:6379 KUBECONFIG=~/.kube/config ./bin/k8s-agents-mcp
# Should start without errors and wait for stdin (MCP stdio protocol)
```

### Pods stuck in Pending

```bash
# Check events for scheduling issues
kubectl describe pod -n agents -l team-component=agent | grep -A5 Events

# Common causes: insufficient CPU/memory, missing secrets, image pull errors
kubectl get events -n agents --sort-by='.lastTimestamp' | tail -20
```

### Tasks stuck in pending

```bash
# Check if any agents are alive and have the right role
redis-cli -h REDIS_HOST -a YOUR_REDIS_PASSWORD HGETALL team:my-team:heartbeat

# Check task details
redis-cli -h REDIS_HOST -a YOUR_REDIS_PASSWORD HGETALL team:my-team:task:TASK_ID
```
