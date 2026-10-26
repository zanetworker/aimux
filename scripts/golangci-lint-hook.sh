#!/usr/bin/env bash
# golangci-lint for pre-commit. The web dashboard's built assets
# (internal/frontend/web/dist, gitignored) are embedded with go:embed, so a
# fresh checkout without them (git-ship's temporary worktree) cannot type-check.
# Then a placeholder stands in for the lint run only and is removed after, so a
# later go build never embeds an empty dashboard by accident.
set -euo pipefail

dist=internal/frontend/web/dist
if [ ! -e "$dist" ]; then
  trap 'rm -rf "$dist"' EXIT
  mkdir -p "$dist"
  printf '<!-- placeholder for golangci-lint; build the web app with make build -->\n' > "$dist/index.html"
fi
golangci-lint run --timeout=5m --new-from-rev=HEAD
