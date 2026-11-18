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
# The base new issues are counted from: LINT_FROM, else on pre-push the
# remote's ref pre-commit passes (PRE_COMMIT_FROM_REF: the pushed commits),
# else HEAD (on commit: the staged change). A base that does not resolve
# (a shallow or single-branch clone) falls back to HEAD with a note, never
# to an unfiltered run. --new-from-rev filters by changed lines: a finding
# on an unchanged line (a helper left unused by a deletion elsewhere) is
# only caught by a full run (GOOS=linux golangci-lint run ./...) and CI.
from=${LINT_FROM:-${PRE_COMMIT_FROM_REF:-HEAD}}
if ! git rev-parse --verify -q "$from^{commit}" >/dev/null; then
  echo "golangci-lint hook: $from not found; checking uncommitted changes only (run make lint-linux)" >&2
  from=HEAD
fi
golangci-lint run --timeout=5m --new-from-rev="$from"
