# End-to-end discovery test

Runs the real aimux binary against seeded sessions in a container and fails unless `aimux agents --json` finds:

| Project | Status |
|---|---|
| payments-api | Active |
| infra | Waiting |
| web | Idle |
| docs | Idle |

Inside the container `ps` sees only the stand-in `claude` processes (`fakeagent/`), so the result does not depend on the machine running it. CI runs it in `.github/workflows/e2e-discovery.yml`. Locally:

```sh
make web-build
cd test/e2e/discovery
GOOS=linux CGO_ENABLED=0 go build -o bin/aimux ../../../cmd/aimux
GOOS=linux CGO_ENABLED=0 go build -o bin/claude ./fakeagent
podman build -t aimux-e2e -f Containerfile . && podman run --rm aimux-e2e
```

| File | Role |
|---|---|
| `seed.py` | Writes 4 realistic Claude Code sessions under `$HOME/.claude/projects` |
| `fakeagent/` | Stand-in `claude` binary: discovered by process name, prints the session if opened |
| `entrypoint.sh` | Starts one stand-in per session in tmux, writes `~/.claude/sessions/<pid>.json` the way Claude Code does, then runs the check |
| `Containerfile` | Debian image with tmux, lsof, procps and python3 |
