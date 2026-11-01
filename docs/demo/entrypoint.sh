#!/bin/bash
# Runs inside the demo container: seed sessions, start stand-in agents in
# tmux, register them the way Claude Code does, then record the tape.
set -euo pipefail
python3 /demo/seed.py > /tmp/sessions.txt
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
if [ "${DEMO_CHECK:-}" = 2 ]; then exec sleep infinity; fi
if [ "${DEMO_MODE:-}" = web ]; then
  # aimux web binds 127.0.0.1; socat exposes it on the container port.
  aimux web --port 3000 &
  exec socat TCP-LISTEN:3001,fork,reuseaddr TCP:127.0.0.1:3000
fi
if [ "${DEMO_CHECK:-}" = 1 ]; then
  # Smoke test: discovery must find the 4 seeded agents with their statuses.
  aimux agents --json | python3 -c '
import json, sys
got = sorted((a["project"], a["status"]) for a in json.load(sys.stdin)["agents"])
want = [("docs", "Idle"), ("infra", "Waiting"), ("payments-api", "Active"), ("web", "Idle")]
print("agents:", got)
sys.exit(0 if got == want else "FAIL: want " + str(want))'
  exit $?
fi
vhs /demo/demo.tape
ls -la /tmp/demo.mp4
cp /tmp/demo.mp4 /out/demo.mp4
