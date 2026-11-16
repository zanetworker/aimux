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
  internal/remoteapp
  internal/catalog
  internal/sessionstate
  internal/jsonedit
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

# internal/jsonedit (the config editor) is a leaf: standard library only.
if [ -d internal/jsonedit ] && command -v go >/dev/null; then
  extra=$(go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./internal/jsonedit | grep -v '/internal/jsonedit$' || true)
  if [ -n "$extra" ]; then
    echo "LAYER VIOLATION: internal/jsonedit may import only the standard library; imports:"
    echo "$extra"
    fail=1
  fi
fi

# internal/decide (the decision core) and internal/keyring are reusable: no
# use-case, frontend or remote API imports. decide may use only llm, cost and keyring
# from aimux; keyring only the standard library. internal/autoroute (the mode use
# case) may import decide but no remote API, browserhost or frontend package.
# internal/toolreg (the tool-source registry, its snapshot and Store) is core
# config: no aimux imports. The pins file belongs to internal/toolgateway, which
# hands its records to the Store as a toolreg.PinTable (toolgateway.NewStore),
# so the snapshot dependency points toolgateway -> toolreg only. toolgateway
# may import only mcpproxy and toolreg from aimux (no profile, remote API or
# frontend package), so the compiler and remote serve sit above both.
if command -v go >/dev/null; then
  for spec in "internal/decide:internal/llm internal/cost internal/keyring" "internal/keyring:" "internal/autoroute:internal/decide" "internal/toolreg:" "internal/toolgateway:internal/mcpproxy internal/toolreg"; do
    pkg=${spec%%:*}; allow=${spec#*:}
    [ -d "$pkg" ] || continue
    for imp in $(go list -f '{{join .Imports " "}}' "./$pkg"); do
      case "$imp" in
        github.com/zanetworker/aimux/*)
          ok=0
          for a in $allow; do [ "$imp" = "github.com/zanetworker/aimux/$a" ] && ok=1; done
          if [ $ok = 0 ]; then echo "LAYER VIOLATION: $pkg may not import $imp"; fail=1; fi ;;
        *.*/*)
          if [ "$pkg" = internal/keyring ]; then echo "LAYER VIOLATION: $pkg may import only the standard library; imports $imp"; fail=1; fi ;;
      esac
    done
  done
fi

exit $fail
