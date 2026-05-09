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

One agent just silently deleted your auth config. Another rewrote 47 files. aimux shows you exactly what happened, and lets you judge if it was good.

## Quick Start

```bash
brew install zanetworker/aimux/aimux
aimux          # terminal dashboard
aimux web      # browser dashboard at localhost:9090
```

Requires **tmux**. Auto-discovers running Claude, Codex, and Gemini agents.

## What It Does

**See everything.** Live trace of every prompt, response, and tool call as agents work. Watch file edits, bash commands, and API calls turn by turn.

**Judge quality.** Label turns GOOD, BAD, or WASTE. Add notes. Build eval datasets from real production sessions, not synthetic benchmarks.

**Export anywhere.** OTEL to MLflow or Jaeger for evaluation pipelines. JSONL to disk for offline analysis. Your annotations become feedback assessments.

**Zero setup.** No config, no instrumentation, no SDK. Run `aimux` and it finds every running agent on your machine.

## Features

<table>
<tr>
<td width="50%">

**Discovery & Monitoring**
- Auto-discovers Claude, Codex, and Gemini agents
- Live status: active, idle, waiting, error
- Per-agent token usage and cost tracking
- Model-aware pricing (Opus, Sonnet, Haiku, GPT, Gemini)
- Cross-session search ("which agent edited auth.go?")

</td>
<td width="50%">

**Tracing & Evaluation**
- Turn-by-turn trace of prompts, responses, tool calls
- Live streaming as agents work (JSONL tailing + SSE)
- Annotate turns as GOOD / BAD / WASTE with notes
- Export to MLflow, Jaeger, or any OTLP backend
- JSONL export with annotations for offline eval

</td>
</tr>
<tr>
<td>

**Two Interfaces**
- **TUI**: keyboard-driven, split-pane trace + live session
- **Web**: card grid, filtering, session history, trace panel
- Dark and light themes (web dashboard)
- Plugin system for custom dashboard tabs

</td>
<td>

**Agent Management**
- Launch agents in tmux (persistent) or direct mode
- Google Tasks integration for task-driven launches
- macOS notifications on permission prompts, errors, completion
- Session history with browse, search, and resume
- Kubernetes support: run agents on K8s pods

</td>
</tr>
</table>

<p align="center">
  <img src="assets/annotations.png" alt="Trace with annotations" width="380">
  <img src="assets/costs.png" alt="Cost tracking" width="380">
</p>

## Documentation

Full documentation at **[zanetworker.github.io/aimux](https://zanetworker.github.io/aimux/)**

- [Getting Started](https://zanetworker.github.io/aimux/getting-started/) - install, first run, CLI commands
- [Configuration](https://zanetworker.github.io/aimux/configuration/) - full config.yaml reference
- [Web Dashboard](https://zanetworker.github.io/aimux/guides/web-dashboard/) - cards, filters, sessions, themes
- [TUI Keybindings](https://zanetworker.github.io/aimux/guides/tui-keybindings/) - keyboard shortcuts
- [Tracing & Annotations](https://zanetworker.github.io/aimux/guides/tracing/) - trace view, OTEL, JSONL
- [Launch Modes](https://zanetworker.github.io/aimux/guides/launch-modes/) - tmux, direct, task-driven
- [Adding a Provider](https://zanetworker.github.io/aimux/advanced/adding-a-provider/) - extend with new agents
- [Kubernetes](https://zanetworker.github.io/aimux/advanced/k8s-quickstart/) - run agents on K8s

## Built With

[Bubble Tea](https://github.com/charmbracelet/bubbletea) |
[Lip Gloss](https://github.com/charmbracelet/lipgloss) |
[charmbracelet/x/vt](https://github.com/charmbracelet/x) |
[creack/pty](https://github.com/creack/pty) |
[OpenTelemetry](https://opentelemetry.io/) |
[Starlight](https://starlight.astro.build/)

## License

[MIT](LICENSE)
