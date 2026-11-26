<h1 align="center"><img src="assets/aimux-lockup.svg" alt="aimux" height="56"></h1>

<p align="center">Mission control for AI coding agents: see, steer and evaluate every session.</p>

<p align="center">
  <a href="https://github.com/zanetworker/aimux/releases/latest"><img src="https://img.shields.io/github/v/release/zanetworker/aimux?style=flat-square" alt="release"></a>
  <a href="https://github.com/zanetworker/aimux/actions"><img src="https://img.shields.io/github/actions/workflow/status/zanetworker/aimux/ci.yml?style=flat-square" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?style=flat-square" alt="MIT license"></a>
</p>

<p align="center">
  <a href="https://aimux.ai">Docs</a> ·
  <a href="#install">Install</a> ·
  <a href="#quickstart">Quickstart</a> ·
  <a href="https://github.com/zanetworker/aimux/releases">Changelog</a>
</p>

<p align="center">
  <img src="assets/demo.gif" alt="aimux TUI: the agent list, then a split view with the trace and diff on the left and the live Claude Code session on the right, a GOOD label, and the costs view" width="800">
  <br><em>Four agents in one list, then one agent's trace beside its live session, a turn labeled GOOD, and cost per project.</em>
</p>

You run five agents in five tmux panes and lose track of which one rewrote `auth.go`. aimux finds every running and past session, shows each one turn by turn (prompts, tool calls, diffs), and lets you label turns so the good and bad runs become eval data.

## Install

```sh
brew install zanetworker/aimux/aimux
```

Requires tmux. No config, SDK or instrumentation: aimux reads the session files Claude Code and Codex already write.

## Quickstart

```sh
aimux agents
```

```text
PID   PROVIDER  STATUS   PROJECT       MODEL
7     claude    Active   payments-api  opus
25    claude    Idle     docs          opus
13    claude    Idle     web           opus
19    claude    Waiting  infra         opus
```

Then open the dashboard:

```sh
aimux          # terminal UI
aimux web      # browser UI at localhost:3000
```

## Why aimux

- **See every agent at once.** Running and past Claude Code and Codex sessions in one list, with live status, CPU, tokens and cost, and search across all of them ("which agent edited auth.go?").
- **Know what each agent actually did.** A turn-by-turn trace of prompts, tool calls and diffs, with a PR-style review panel. [Tracing →](https://aimux.ai/guides/tracing/)
- **Turn real runs into evals.** Label turns GOOD, BAD or WASTE and export them as MLflow feedback, to any OTLP backend, or as JSONL. [MLflow →](https://aimux.ai/guides/mlflow-integration/)
- **Run and reach agents anywhere.** Launch them locally, in OpenShell sandboxes or on Kubernetes. From your phone, follow your Claude sessions, continue idle ones and answer approvals. [Remote sandboxes →](https://aimux.ai/guides/remote-sandboxes/)

## What's inside

| Area | What you get | Docs |
|---|---|---|
| Discover | Auto-discovery of Claude Code and Codex, live status (working, idle, needs you, error; the same words on the phone), cross-session search, starred sessions, generated titles | [Web dashboard](https://aimux.ai/guides/web-dashboard/) |
| Trace and review | Turn-by-turn trace, inline diffs, PR-style review panel, live diff pane beside the session terminal | [Tracing](https://aimux.ai/guides/tracing/) |
| Cost | Per-agent and per-turn cost with model-aware pricing | [Cost tracking](https://aimux.ai/guides/cost-tracking/) |
| Evaluate | GOOD / BAD / WASTE labels with notes; export to MLflow, Jaeger or any OTLP backend, or JSONL | [MLflow](https://aimux.ai/guides/mlflow-integration/) |
| Launch | Six-axis launcher, saved profiles, quick-launch directories, launches from Google Tasks, continue an idle session with `aimux chat` | [Launch modes](https://aimux.ai/guides/launch-modes/), [Tasks](https://aimux.ai/guides/tasks/), [Chat](https://aimux.ai/guides/chat/) |
| Run remotely | OpenShell sandboxes with terminal, traces and kill; agents on Kubernetes pods | [Remote sandboxes](https://aimux.ai/guides/remote-sandboxes/), [Kubernetes](https://aimux.ai/advanced/k8s-quickstart/) |
| Reach | `aimux remote serve`: follow and continue Claude sessions from the iPhone app or the phone web client over tailscale, answer approvals, type into sessions open in iTerm2, and open a phone session on the Mac | [Remote access](https://aimux.ai/guides/remote-access/) |
| Share MCP | `aimux mcp proxy install`: your stdio MCP servers, and HTTP ones that need no OAuth (their header values kept in the Keychain), run once in a shared daemon, each at its own endpoint under its original name (permission rules unchanged), started on first call, shared ones stopped when idle and browser ones kept for as long as their Claude Code session runs, and a server you add later in a session moved behind it by a hook; `aimux mcp serve`: session search and resume tools for agents | [MCP proxy](https://aimux.ai/guides/mcp-proxy/) |
| Use Codex as Claude Code's model (experimental) | `aimux model proxy serve`: an Anthropic Messages API on 127.0.0.1:7385 (key in the Keychain item `aimux-model-proxy`) answered by `codex app-server` on your ChatGPT plan. Claude Code still runs every tool, so its hooks, permissions, skills and MCP stay as they are; Codex only decides, with its own tools switched off. The stable listener also writes the plan's Codex allowance to `~/.aimux/codex-usage.json` every 2 minutes. `aimux model proxy status` reports whether it is answering and how its last turn went, `aimux model proxy stats` summarises its recent requests from a JSON history, and turns ask for Codex's Fast service tier, which spends the allowance faster (`--service-tier standard` asks for no tier, leaving Codex's own default) | [Stable and dev channels](docs-site/src/content/docs/advanced/model-proxy-channels.mdx), [Spike results](docs/superpowers/specs/2026-11-21-model-proxy-codex-spike-results.md) |
| Read the web | Wingman, a Chrome side panel that summarizes the page and answers questions about it. It is a client of the remote API on its own socket: `aimux browser install` registers the native messaging host with Chrome and Chrome Beta, `aimux browser setup` prepares the sandbox images and providers and reports tools to review, `aimux tools status\|diff\|approve` reviews and pins the read-only tools (`aimux tools add notes\|mcp\|cli\|http` and `remove` edit `tool-sources.yaml`; once `aimux servers migrate` has split it into `servers.yaml` and `tool-sets.yaml`, edit those instead, see [Servers](https://aimux.ai/guides/servers/)), `aimux mcp proxy gateway install` runs the tool gateway worker that serves them to sandboxes, `aimux remote serve` creates the `aimux-work-readonly` OpenShell sandbox on the first work question, `aimux sandbox status\|setup\|verify` shows the policy state of each available `runs: sandbox` profile's sandbox and of the tool runner (a `runs: run` profile has no sandbox of its own), builds both images (`localhost/wingman-sandbox` and the tool runner's `localhost/aimux-tools`) and creates the providers (`wingman-claude`, plus the optional `aimux-github` only when the secret store has the item `aimux-github`; `cli` tools such as `gh` run in the tool runner, and so do `http-openapi` sources, which serve their OpenAPI GET operations as tools; sources with a `credential` are not supported yet), and checks that sandbox's egress, and `aimux browser host` is the relay Chrome starts. Page questions run under the `page` profile, work questions under `work-readonly`, which exposes only pinned lookups (wiki, Jira, Slack, Gmail, Drive, and GitHub once you run `aimux tools add cli gh` and approve `gh:readonly`; after `aimux servers migrate`, add a `gh` server with `type: cli` and `pack: gh` to `servers.yaml` and a `gh:readonly` set to `tool-sets.yaml` instead, then run `aimux tools approve gh:readonly`) | [Wingman](https://aimux.ai/guides/wingman/) |
| Constrain | Profiles: named contracts for what a session may reach (tools, network, files, credentials) and where it runs, narrow-only inheritance, a digest per profile. `aimux profile list\|show\|check\|migrate`; per-client scopes and limits in `~/.config/aimux/clients.yaml` | [Profiles](https://aimux.ai/guides/profiles/), [Local clients](https://aimux.ai/guides/remote-access/#local-clients) |
| Interfaces | Keyboard TUI with split panes; web dashboard with cards, filters and dark/light themes; desktop and terminal-bell notifications | [TUI keys](https://aimux.ai/guides/tui-keybindings/), [Notifications](https://aimux.ai/guides/notifications/) |
| Extend | Plugin tabs, new providers, `--json` on every listing command, `aimux agent-context` for the full CLI contract | [Plugins](https://aimux.ai/guides/plugins/), [Agent usage](https://aimux.ai/guides/agent-usage/), [Add a provider](https://aimux.ai/advanced/adding-a-provider/) |

Use the Claude Code or Codex built-in history instead if you run one agent at a time; aimux pays off when several run in parallel.

**What it doesn't do:** it doesn't plan or orchestrate multi-agent work, and it only supports Claude Code and Codex today ([add a provider](https://aimux.ai/advanced/adding-a-provider/)).

<p align="center">
  <img src="assets/web-dashboard.png" alt="aimux web dashboard: agents grouped by project with status and cost, and the selected agent's trace with its tool calls on the right, dark theme" width="820">
  <br><em>The web dashboard (<code>aimux web</code>): every agent by project, and one agent's trace.</em>
</p>

## How it works

<p align="center">
  <img src="assets/architecture.png" alt="aimux reads Claude Code and Codex sessions read-only, feeds discovery, trace and export, and serves the TUI, web dashboard, remote API and MCP proxy; it launches agents into OpenShell sandboxes or Kubernetes, and exports go to MLflow or Jaeger" width="820">
</p>

## Usage

| Task | Command |
|---|---|
| Search past sessions in all projects | `aimux sessions "auth.go" --all --list --json --limit 5` |
| Start an agent and wait for it | `aimux spawn claude --dir ./proj --wait --json` |
| Resume a past session | `aimux resume <session-id>` |
| Jump to a running agent's iTerm2 pane | `aimux jump <session-id or pid>` |
| Continue a Claude session that is not open in a terminal | `aimux chat <session-id> "next step?"` |
| Export a trace | `aimux export <session-id>` |
| Follow Claude sessions from your phone | `aimux remote serve` (via tailscale serve) |
| Share MCP servers across sessions | `aimux mcp proxy install --dry-run`, then `aimux mcp proxy install` |
| Run Claude Code on Codex (ChatGPT plan, no Claude quota) | `aimux model proxy serve`, then in the shell you start sessions from: `lines="$(aimux model proxy env --codex)" && eval "$lines"` (the proxy's address, its key and a `codex:<model>` model, `gpt-6.2-sol` unless you name one); `eval "$(aimux model proxy env --off)"` undoes it |
| Set up the Wingman side panel | `aimux browser install --extension-id <id>`, `aimux browser setup`, `aimux tools approve <set>...`, `aimux mcp proxy gateway install`, `aimux sandbox verify`, `aimux sandbox status` |
| Review the tools a profile may call | `aimux tools status`, `aimux tools diff`, `aimux tools approve`; add or remove a source with `aimux tools add notes --folder <dir>`, `aimux tools add mcp ...`, `aimux tools add cli gh`, `aimux tools add http --spec ... --base-url ...`, `aimux tools remove <source>` (with `servers.yaml`, edit it and `tool-sets.yaml` instead; see [Servers](https://aimux.ai/guides/servers/)) |
| Define servers and their secrets | `aimux servers discover <name> --cwd <dir>` (the variables a server reads, with a suggested reference each), `aimux servers migrate` (split `tool-sources.yaml` into `servers.yaml` and `tool-sets.yaml`) |
| Review tools whose definition changed since you approved them | `aimux tools review` (on a terminal: each diff, then y, n or a for all the rest) |
| Set up everything missing, then check it | `aimux setup` (on a terminal, lists the parts and offers to turn them on; then runs only missing steps), `aimux doctor` (read-only, one fix per problem) |
| Check the background services | `aimux status` (model proxy, MCP proxy, tool gateway, collector; one fix per problem; exit 1 when one needs attention) |
| Turn an engine of the inference proxy on or off, or add a gateway | `aimux engine enable anthropic`, `aimux engine add enmaas=https://gateway.example`, `aimux engine disable enmaas`, `aimux engine remove enmaas`; `aimux enable <part>` and `aimux disable <part>` for a part. Each of these changes one thing in `~/.aimux/config.yaml`, keeps your comments, and makes the service match, asking before a restart. `aimux engine list` only reads: what the file states beside what the running proxy answers with. `aimux disable inference-proxy` stops the proxy and keeps it stopped until it is enabled again; the MCP proxy is turned off with `aimux mcp proxy uninstall` ([guide](docs-site/src/content/docs/guides/parts.mdx)) |
| Say in one file which parts are on, and have the services match | `parts:` in `~/.aimux/config.yaml`; `aimux setup --check` shows what would change, `aimux setup --apply` does it, changing only the arguments that come from the settings and putting a job file back when its service does not answer ([guide](docs-site/src/content/docs/guides/parts.mdx)) |
| Bring the model proxy, the MCP proxy and the collector to this build | `aimux upgrade` (the tool gateway is not moved yet and is listed with its own command; copies this build into each service's own folder, restarts it, and puts the previous build back if it does not answer; `--dry-run` to see the plan) |
| See whether a shell's sessions go through the model proxy, on which engine, and whether it answers | `aimux model proxy route [--model <model>]` (one line for a status line; `--json`; exits 1 when the proxy the shell points at cannot answer) |
| Use Claude and Codex models in one Claude Code session, both counted | `aimux model proxy serve --anthropic`, then `eval "$(aimux model proxy env)"` in the shell you start sessions from: a Claude model is answered by Anthropic on your own login, a `codex:` model by Codex. `eval "$(aimux model proxy env --off)"` undoes it. `--messages-upstream name=address` adds another Messages API address, such as an internal gateway, reached as `<name>:<model>` and paid for with its own key, read from the secret-store entry `<name>-key` by default (`--messages-upstream-key` says where else it is kept) |
| See what your sessions spent | `aimux cost` (model calls and tokens from the ledger, per session, model or day; `--since 7d`, `--by model`, `--session <id>`; what could not be counted is listed, never shown as zero, including calls whose answer carried no token figures) |
| See what a profile allows | `aimux profile list`, `aimux profile show work-readonly`, `aimux profile check <name>` |

<details>
<summary>All commands</summary>

```text
agent-context  Machine-readable CLI surface for agents
agents         List running AI agents
browser        Wingman browser side panel: Chrome native messaging host and setup (the sandbox is aimux sandbox)
chat           Send a message to an existing Claude session and stream the reply
collect        Run headless OTEL collector
configs        List named agent configurations
cost           Show what your sessions spent: model calls and tokens from the ledger
disable        Turn a part of aimux off
doctor         Read-only checks of tools, router, port 7390, Wingman serve, sandbox and gateway, each problem with its fix; exit 1 on any failure
enable         Turn a part of aimux on
engine         See and change which engines the inference proxy answers with
environments   List configured environments
export         Export a session trace
feedback       Send feedback about the CLI
jump           Bring a running agent's terminal pane to the front
kill           Kill a running agent
mcp            MCP server for session search and remote agents
profile        Manage profiles (list, show, check, migrate; save|get|delete act on the v1 store)
remote         API for your sessions from other devices, through tailscale serve
resume         Resume a past session
router         Inspect and try Auto mode's mode router: status and test
sandbox        The OpenShell sandboxes profiles and the tool runner run in: status, setup (images and providers) and verify
sessions       Browse and search past sessions
setup          Check every concern and run only the missing setup steps; on a terminal, offer to review changed tools
spawn          Start a new AI agent session
status         Check the model proxy, MCP proxy, tool gateway, and collector
tools          Review the tools aimux exposes: status, diff and approve pins; add or remove sources
upgrade        Bring the background services to this build
version        Show aimux version
web            Launch web dashboard
```

</details>

Full docs: [getting started](https://aimux.ai/getting-started/), [configuration](https://aimux.ai/configuration/).

## Contributing

Issues and PRs welcome. Run `aimux feedback "..."` to report friction straight from the CLI.

## License

[MIT](LICENSE)
