# README demo

Records `assets/demo.gif` and `assets/web-dashboard.png` from seeded data in a container, so no real sessions or project names appear.

```sh
./docs/demo/record.sh              # GIF + web screenshot
SKIP_WEB=1 ./docs/demo/record.sh   # GIF only
```

Host needs: podman or docker, Go, ffmpeg, pnpm (builds the embedded web UI on a fresh checkout via `make web-build`). The web screenshot also needs curl, Node (the script installs Playwright and its Chromium) and Python 3 with Pillow (`python3 -m pip install pillow`). Before the screenshot, the script waits up to 60 s for the dashboard to list the 4 demo agents and exits 1 if it doesn't.

Smoke test (exit 1 unless discovery finds the 4 seeded agents with the expected statuses):

```sh
podman run --rm -e DEMO_CHECK=1 localhost/aimux-demo
```

## How it works

| File | Role |
|---|---|
| `seed.py` | Writes 4 Claude Code sessions (payments-api active, infra waiting, web and docs idle) under `$HOME/.claude/projects` |
| `fakeagent/` | Stands in for the `claude` binary: idles so discovery finds it, and draws the session transcript when aimux runs `claude --resume <id>` for the split view |
| `entrypoint.sh` | Starts one stand-in per session in tmux, writes `~/.claude/sessions/<pid>.json` the way Claude Code does, then records the tape (or runs the smoke test, or serves `aimux web`) |
| `demo.tape` | The [VHS](https://github.com/charmbracelet/vhs) script: agent list, split view, label a turn, costs |
| `web-shot.mjs`, `round.py` | Playwright screenshot of `aimux web`, then rounded corners and a white margin |
| `Containerfile`, `record.sh` | Build the image with Linux binaries, record MP4 in the container, convert to GIF with ffmpeg on the host (GIF encoding inside a 4 GB podman VM runs out of memory) |

The aimux binary is the real one, built from this checkout; only the agents are stand-ins. Re-record after any TUI or web change that shows up in the demo.

The registry files in `~/.claude/sessions/` follow Claude Code's undocumented format (`pid`, `sessionId`, `cwd`, `status`, `procStart`). If Claude Code changes it, update `entrypoint.sh` together with `internal/discovery/registry.go`.
