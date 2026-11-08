# Contributing to aimux

Thanks for helping. Issues and pull requests are welcome. For small friction reports, `aimux feedback "..."` works straight from the CLI.

## Build

aimux embeds the web dashboard, so build the web UI once on a fresh checkout (needs pnpm), then the binary:

```sh
make web-build     # web/ with pnpm, copied into internal/frontend/web/dist
make build         # ./aimux
make build-all     # both
```

The docs site lives in `docs-site/` (Astro Starlight): `cd docs-site && npm ci && npm run dev`.

## Test

```sh
make test               # unit tests
make test-integration   # adds -tags integration
make test-all           # adds integration and e2e tags
make lint               # golangci-lint
```

`test/e2e/discovery/` runs the real binary against seeded sessions in a container; its README has the commands, and CI runs it on pull requests to `main` that touch `cmd/`, `internal/` or `test/e2e/`.

Run `pre-commit run --all-files` before you push; it runs the secrets scan, whitespace and YAML checks and the linters. CI runs lint and the tests on every pull request to `main`, and the gitleaks secrets scan on every push. A verified false positive goes in `.gitleaksignore` by its fingerprint; a real secret gets rotated, never ignored.

## Pull requests

- Keep each change small and focused, with a test for any behavior change.
- Describe what changed and why in the PR; link the issue it fixes.
- Update the README and the docs site in the same PR when you change something users see (flags, commands, keys, output).

Coding agents: `CLAUDE.md` and `.claude/CLAUDE.md` have the project guide.
