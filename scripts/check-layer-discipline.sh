#!/usr/bin/env bash
# Checks that core packages don't import frontend libraries (bubbletea, lipgloss, tui/).
# A violation means business logic leaked into a frontend adapter layer.
set -euo pipefail

CORE_PKGS=(
  internal/controller
  internal/agent
  internal/discovery
  internal/compose
  internal/otel
  internal/spawn
  internal/terminal
  internal/mcpserver
  internal/config
)

fail=0
for pkg in "${CORE_PKGS[@]}"; do
  if [ ! -d "$pkg" ]; then continue; fi
  if grep -rn --include="*.go" \
       '"github.com/charmbracelet/bubbletea"' \
       '"github.com/charmbracelet/lipgloss"' \
       '"github.com/zanetworker/aimux/internal/frontend/tui"' \
       "$pkg/" 2>/dev/null | grep -v "_test.go" | grep -q .; then
    echo "LAYER VIOLATION: $pkg imports a TUI/frontend library"
    grep -rn --include="*.go" \
       '"github.com/charmbracelet/bubbletea"' \
       '"github.com/charmbracelet/lipgloss"' \
       '"github.com/zanetworker/aimux/internal/frontend/tui"' \
       "$pkg/" 2>/dev/null | grep -v "_test.go"
    echo "  → Move logic to internal/controller/ or remove the import"
    fail=1
  fi
done

exit $fail
