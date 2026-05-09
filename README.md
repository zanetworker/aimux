<p align="center">
  <img src="assets/logo.png" width="128" alt="aimux logo">
  <br>
  <strong>aimux</strong><br>
  <sub>Tame the agent sprawl.</sub>
  <br><br>
  <sub>See all your agents. Trace what they did. Judge if it was good.</sub>
</p>

<p align="center">
  <a href="https://github.com/zanetworker/aimux/releases/latest"><img src="https://img.shields.io/github/v/release/zanetworker/aimux?style=flat-square" alt="Release"></a>
  <img src="https://img.shields.io/badge/license-MIT-blue?style=flat-square" alt="License">
  <img src="https://img.shields.io/badge/go-1.24%2B-00ADD8?style=flat-square&logo=go" alt="Go 1.24+">
  <a href="https://zanetworker.github.io/aimux/"><img src="https://img.shields.io/badge/docs-online-FF3131?style=flat-square" alt="Docs"></a>
</p>

<p align="center">
  <img src="assets/demo.gif" alt="aimux demo" width="800">
</p>

You're running 5 agents across 3 projects. One just edited 47 files. Was it right? Every other tool tells you which agents are running. aimux tells you what they actually did, and whether it was good.

**Trace** turn-by-turn prompts, responses, and tool calls. **Annotate** turns as GOOD, BAD, or WASTE. **Export** to MLflow, Jaeger, or JSONL.

## Quick Start

```bash
# Install
brew install zanetworker/aimux/aimux

# TUI dashboard
aimux

# Web dashboard
aimux web
```

Requires **tmux**. Auto-discovers running Claude, Codex, and Gemini agents.

## Features

| Feature | Description |
|---------|-------------|
| Multi-provider | Claude, Codex, Gemini auto-discovery |
| TUI + Web | Terminal dashboard and browser-based UI |
| Live tracing | Real-time turn-by-turn agent traces |
| Annotations | Label turns GOOD/BAD/WASTE with notes |
| Cost tracking | Per-agent, per-turn cost with model pricing |
| Export | OTEL to MLflow/Jaeger, JSONL to disk |
| Launch modes | Direct or tmux, with task-driven prompts |
| Dark/light themes | Toggle in web dashboard |
| Plugins | Extensible dashboard with custom tabs |
| Kubernetes | Run agents on K8s, control from laptop |

## Documentation

Full documentation at **[zanetworker.github.io/aimux](https://zanetworker.github.io/aimux/)**

- [Getting Started](https://zanetworker.github.io/aimux/getting-started/)
- [Configuration](https://zanetworker.github.io/aimux/configuration/)
- [Web Dashboard](https://zanetworker.github.io/aimux/guides/web-dashboard/)
- [TUI Keybindings](https://zanetworker.github.io/aimux/guides/tui-keybindings/)
- [Adding a Provider](https://zanetworker.github.io/aimux/advanced/adding-a-provider/)
- [Kubernetes Quickstart](https://zanetworker.github.io/aimux/advanced/k8s-quickstart/)

## Built With

[Bubble Tea](https://github.com/charmbracelet/bubbletea) |
[Lip Gloss](https://github.com/charmbracelet/lipgloss) |
[charmbracelet/x/vt](https://github.com/charmbracelet/x) |
[creack/pty](https://github.com/creack/pty) |
[OpenTelemetry](https://opentelemetry.io/)

## License

[MIT](LICENSE)
