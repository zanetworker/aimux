#!/bin/bash
# End-to-end discovery test: seed Claude sessions, start one stand-in
# `claude` process per session in tmux, register them the way Claude Code
# does, then check that the real aimux binary finds all 4 with the right
# statuses. Inside the container ps sees only these processes.
set -euo pipefail
python3 /e2e/seed.py > /tmp/sessions.txt
mkdir -p "$HOME/.claude/sessions"
while read -r project sid; do
  tmux new-session -d -s "$project" -c "/work/$project" \
    "FAKE_BANNER='claude  ·  $project  ·  opus 5.5' claude --model opus"
done < /tmp/sessions.txt
sleep 1
while read -r project sid; do
  pid=$(pgrep -f "^claude --model opus" | while read -r p; do
          [ "$(readlink /proc/$p/cwd)" = "/work/$project" ] && echo "$p"; done | head -1 || true)
  lstart=$(ps -o lstart= -p "$pid" | awk '{$1=$1};1')
  status=idle; [ "$project" = payments-api ] && status=busy
  printf '{"pid":%s,"sessionId":"%s","cwd":"/work/%s","status":"%s","procStart":"%s"}\n' \
    "$pid" "$sid" "$project" "$status" "$lstart" > "$HOME/.claude/sessions/$pid.json"
done < /tmp/sessions.txt
cd /work
aimux agents --json | python3 -c '
import json, sys
got = sorted((a["project"], a["status"]) for a in json.load(sys.stdin)["agents"])
want = [("docs", "Idle"), ("infra", "Waiting"), ("payments-api", "Active"), ("web", "Idle")]
print("agents:", got)
sys.exit(0 if got == want else "FAIL: want " + str(want))'
