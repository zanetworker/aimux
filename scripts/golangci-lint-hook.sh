#!/usr/bin/env bash
# golangci-lint for pre-commit. The web dashboard's built assets
# (internal/frontend/web/dist, gitignored) are embedded with go:embed, so a
# checkout without them (git-ship's temporary worktree, or an empty dist) cannot
# type-check. Then a placeholder stands in for the lint run only and is removed
# after, so a later go build never embeds an empty dashboard by accident.
set -euo pipefail

dist=internal/frontend/web/dist
placeholder=$dist/index.html
# go:embed of a directory skips names starting with . or _ and needs one other file.
# No pipe: under pipefail an early-exiting reader could make a built dist look empty.
if [ -z "$(find "$dist" -type f ! -name '.*' ! -name '_*' -print -quit 2>/dev/null)" ]; then
  if [ -e "$dist" ]; then
    trap 'rm -f "$placeholder"' EXIT
  else
    trap 'rm -rf "$dist"' EXIT
  fi
  mkdir -p "$dist"
  printf '<!-- placeholder for golangci-lint; build the web app with make build -->\n' > "$placeholder"
fi
golangci-lint run --timeout=5m --new-from-rev=HEAD
