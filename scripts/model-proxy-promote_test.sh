#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")"
script=$PWD/model-proxy-promote
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
export HOME="$scratch"
mkdir -p "$HOME/go/bin"
printf old > "$HOME/go/bin/aimux"
printf candidate > "$HOME/go/bin/aimux-dev"
chmod +x "$HOME/go/bin/aimux" "$HOME/go/bin/aimux-dev"

if [[ $# == 0 ]]; then
  for scenario in guard install hud; do bash "./$(basename "$0")" "$scenario"; done
  exit
fi
case $1 in
  guard)
    for op in promote rollback; do
      if output=$(CLAUDECODE=1 bash "$script" "$op" 2>&1); then
        printf 'FAIL: %s was allowed inside Claude Code\n' "$op" >&2
        exit 1
      fi
      [[ "$output" == *'outside Claude Code'* ]] || { printf 'FAIL: %s\n' "$output" >&2; exit 1; }
      [[ $(< "$HOME/go/bin/aimux") == old && ! -e "$HOME/go/bin/aimux.prev" ]]
    done
    ;;
  install)
    source "$script"
    install_candidate "$HOME/go/bin/aimux-dev"
    [[ $(< "$HOME/go/bin/aimux") == candidate && -x "$HOME/go/bin/aimux" ]]
    [[ $(< "$HOME/go/bin/aimux.prev") == old && -x "$HOME/go/bin/aimux.prev" ]]
    [[ $(< "$HOME/go/bin/aimux-dev") == candidate ]]
    restore_previous
    [[ $(< "$HOME/go/bin/aimux") == old && -x "$HOME/go/bin/aimux" ]]
    rm "$HOME/go/bin/aimux.prev"
    if restore_previous; then exit 1; fi
    [[ $(< "$HOME/go/bin/aimux") == old ]]
    ;;
  hud)
    repo=$HOME/go/src/github.com/zanetworker/claude-hud
    mkdir -p "$repo/src"
    git init -q "$repo"
    printf 'first\n' > "$repo/src/index.ts"
    git -C "$repo" add src/index.ts
    git -C "$repo" -c user.name=Test -c user.email=test@example.invalid -c commit.gpgsign=false -c core.hooksPath=/dev/null commit -qm first
    first=$(git -C "$repo" rev-parse HEAD)
    printf 'second\n' > "$repo/src/index.ts"
    git -C "$repo" add src/index.ts
    git -C "$repo" -c user.name=Test -c user.email=test@example.invalid -c commit.gpgsign=false -c core.hooksPath=/dev/null commit -qm second
    printf 'in progress\n' > "$repo/src/index.ts"
    current=$HOME/.claude/claude-hud-pins/current
    bash "$script" pin-hud "$first"
    [[ $(< "$current/src/index.ts") == first ]]
    bash "$script" pin-hud
    [[ $(< "$current/src/index.ts") == second && $(< "$repo/src/index.ts") == 'in progress' ]]
    if bash "$script" pin-hud refs/heads/does-not-exist; then exit 1; fi
    [[ $(< "$current/src/index.ts") == second ]]
    ;;
  *) exit 2 ;;
esac
printf 'PASS: %s\n' "${1:-guard}"
