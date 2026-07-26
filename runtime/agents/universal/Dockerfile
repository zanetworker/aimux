ARG BASE_IMAGE=ghcr.io/nvidia/openshell-community/sandboxes/base:latest
FROM ${BASE_IMAGE}

USER root

# Remove the base image's standalone Claude binary (v2.1.156, has login issues
# with OpenShell credential references). Install via npm instead so we control
# the exact version and get one that works with OTEL telemetry.
RUN rm -f /usr/local/bin/claude

ARG CLAUDE_CODE_VERSION=2.1.185
ARG CODEX_VERSION=0.141.0
RUN npm install -g \
    @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} \
    @openai/codex@${CODEX_VERSION}

# Claude Code config: skip first-run setup, trust /sandbox, bypass permissions
COPY claude.json /sandbox/.claude.json
COPY settings.json /sandbox/.claude/settings.json

RUN chown -R sandbox:sandbox /sandbox/.claude /sandbox/.claude.json

USER sandbox
WORKDIR /sandbox
ENTRYPOINT ["/bin/bash"]
