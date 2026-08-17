#!/usr/bin/env bash
# Advisory hint: warns when only one frontend (TUI or web) changes in a commit.
# Exits 0 always — this is a reminder, not a hard failure.
# Add "# parity: N/A" to the commit message to suppress the hint intentionally.
set -uo pipefail

changed=$(git diff --cached --name-only 2>/dev/null)
msg=$(git log -1 --format="%s %b" 2>/dev/null || echo "")

# Suppress if commit message contains the override marker
if echo "$msg" | grep -q "parity: N/A"; then
  exit 0
fi

tui_count=$(echo "$changed" | grep -c "internal/frontend/tui/" || true)
web_count=$(echo "$changed" | grep -c "internal/frontend/web/" || true)

if [ "${tui_count:-0}" -gt 0 ] && [ "${web_count:-0}" -eq 0 ]; then
  echo ""
  echo "PARITY HINT: TUI files changed without matching web files."
  echo "  Changed TUI files:"
  echo "$changed" | grep "internal/frontend/tui/" | sed 's/^/    /'
  echo "  → Run the 'frontend-parity' skill audit, or add '# parity: N/A' to suppress."
  echo ""
fi

if [ "${web_count:-0}" -gt 0 ] && [ "${tui_count:-0}" -eq 0 ]; then
  echo ""
  echo "PARITY HINT: Web files changed without matching TUI files."
  echo "  Changed web files:"
  echo "$changed" | grep "internal/frontend/web/" | sed 's/^/    /'
  echo "  → Run the 'frontend-parity' skill audit, or add '# parity: N/A' to suppress."
  echo ""
fi

exit 0
