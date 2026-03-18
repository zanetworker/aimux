# Session pod image for remote Claude Code sessions.
# Interactive CLI — not the Python coordinator loop.
# Two variants (same image):
#   Standalone:  Claude CLI only, no MCP tools
#   Brain+arms:  Claude CLI + k8s-agents MCP server (spawns worker pods)
FROM ubuntu:24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    tmux git curl nodejs npm build-essential ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Install Claude Code CLI
RUN npm install -g @anthropic-ai/claude-code

# Configure git defaults for commits from session pods
RUN git config --global user.name "claude-session" \
    && git config --global user.email "claude@k8s"

# Allow git in /workspace when running as non-root
RUN git config --system --add safe.directory /workspace

# Optional: bundle MCP server binary for brain+arms variant
COPY k8s-agents-mcp /usr/local/bin/

# Optional: pre-configured MCP server settings for brain+arms variant
COPY claude-settings.json /root/.claude/settings.json

# Pod stays alive; aimux attaches via kubectl exec + tmux
CMD ["sleep", "infinity"]
