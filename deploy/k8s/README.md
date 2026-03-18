# K8s Manifests

Quick reference for the Kubernetes manifests in this directory. All resources deploy into the `agents` namespace.

## Apply Everything

```bash
kubectl create namespace agents
kubectl apply -f deploy/k8s/
```

## Manifest Inventory

| File | What it creates | Notes |
|------|----------------|-------|
| `redis.yaml` | PVC, Deployment, Service (LoadBalancer) | AOF persistence, password auth, nodePort 30379 |
| `secrets.yaml` | Secrets: `redis-secret`, `repo-secret`, `llm-keys` | Placeholder values only. Create from literals (see below) |
| `rbac.yaml` | ServiceAccount `mcp-server`, Role, RoleBinding | Grants get/update on Deployments for spawn/scale |
| `networkpolicy.yaml` | NetworkPolicy `redis-access` | Only `team-component: agent` or `lead` pods reach Redis |
| `otel-collector.yaml` | ConfigMap, Deployment, Service | OTLP/gRPC :4317, OTLP/HTTP :4318. aimux reads spans here |
| `agent-claude-coder.yaml` | Deployment (replicas: 0) | Claude Code SDK worker, role=coder |
| `agent-claude-researcher.yaml` | Deployment (replicas: 0) | Claude Code SDK worker, role=researcher (haiku) |
| `agent-claude-reviewer.yaml` | Deployment (replicas: 0) | Claude Code SDK worker, role=reviewer |
| `agent-claude-session.yaml` | Deployment (replicas: 0) | Interactive Claude Code CLI pod, attach via kubectl exec |
| `agent-gemini-coder.yaml` | Deployment (replicas: 0) | Gemini worker, gemini-2.5-pro |
| `agent-gemini-researcher.yaml` | Deployment (replicas: 0) | Gemini worker, gemini-2.0-flash |
| `hook-config.json` | (not a manifest) | Claude Code hook to block local Agent tool when using K8s mode |

All agent Deployments start at replicas: 0. The `spawn_agent` MCP tool scales them up on demand.

## Prerequisites

- K8s cluster with LoadBalancer support (or NodePort fallback)
- `kubectl` configured and pointing at the target cluster
- CNI plugin that enforces NetworkPolicy (Calico, Cilium, OVN-Kubernetes)

## Secrets Setup

Create secrets from literal values before applying manifests:

```bash
kubectl create secret generic redis-secret \
  --from-literal=password=YOUR_REDIS_PASSWORD \
  -n agents

kubectl create secret generic repo-secret \
  --from-literal=token=ghp_YOUR_GITHUB_PAT \
  --from-literal=host=github.com/your-org/your-repo.git \
  -n agents

kubectl create secret generic llm-keys \
  --from-literal=anthropic=sk-ant-YOUR_ANTHROPIC_KEY \
  -n agents
```

Do **not** apply `secrets.yaml` directly. It contains base64 placeholders only.

## Verify

```bash
kubectl get all -n agents
kubectl get secret -n agents
kubectl logs deployment/redis -n agents
kubectl logs deployment/otel-collector -n agents
```
