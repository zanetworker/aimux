## aimux 0.9.0

### Web Dashboard
Full browser-based UI at `aimux web`. Card grid with agent status, session history table, trace panel with annotations, export (JSONL/OTEL), filtering, sorting, content search, and a plugin system for custom dashboard tabs.

### Dark/Light Themes
Toggle between dark and light themes in the web dashboard. Dark uses the aimux palette (pure black, red accent). Light uses Red Hat brand colors with inverted elevation.

### Launch Modes
Spawn agents in **tmux** (persistent, detachable) or **direct** (child process) mode. Quick launch directories for fast access to frequent projects.

### Google Tasks Integration
Launch agents with task context from Google Tasks. Task title and notes are injected into the agent prompt. Supports MCP and Google Workspace API backends.

### Documentation Site
Full documentation at [zanetworker.github.io/aimux](https://zanetworker.github.io/aimux/) built with Starlight. 14 pages covering getting started, configuration, all guides, and advanced topics.

### Code Quality
Resolved all 108 golangci-lint issues. Added golangci-lint to pre-commit hooks. Proper error logging via debuglog for HTTP responses, PTY writes, and subprocess execution.

### Repo Cleanup
Purged 96MB of compiled binaries from git history. Fresh clones are now ~41MB instead of ~225MB.
