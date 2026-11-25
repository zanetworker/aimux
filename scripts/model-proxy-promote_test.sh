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
  for scenario in guard install moved hud inference retry interrupt; do bash "./$(basename "$0")" "$scenario"; done
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
  moved)
    # After `aimux upgrade` the job runs the service's own copy: the promote swaps that file, not the CLI.
    own=$HOME/.aimux/services/model-proxy
    mkdir -p "$own" "$HOME/Library/LaunchAgents"
    printf service > "$own/aimux"; chmod +x "$own/aimux"
    cat > "$HOME/Library/LaunchAgents/com.aimux.model-proxy.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
  <key>Label</key><string>com.aimux.model-proxy</string>
  <key>ProgramArguments</key><array><string>$own/aimux</string><string>model</string><string>proxy</string><string>serve</string></array>
</dict></plist>
PLIST
    source "$script"
    [[ $stable == "$own/aimux" && $previous == "$own/aimux.prev" ]] || { printf 'FAIL: stable is %s\n' "$stable" >&2; exit 1; }
    install_candidate "$HOME/go/bin/aimux-dev"
    [[ $(< "$own/aimux") == candidate && -x "$own/aimux" && $(< "$own/aimux.prev") == service ]] || { printf 'FAIL: the service copy was not swapped\n' >&2; exit 1; }
    [[ $(< "$HOME/go/bin/aimux") == old && ! -e "$HOME/go/bin/aimux.prev" ]] || { printf 'FAIL: the CLI was touched\n' >&2; exit 1; }
    restore_previous
    [[ $(< "$own/aimux") == service ]] || { printf 'FAIL: rollback did not restore the service copy\n' >&2; exit 1; }
    # A job with a Program key runs that file, whatever its first argument says.
    /usr/libexec/PlistBuddy -c "Add :Program string $HOME/elsewhere/aimux" "$HOME/Library/LaunchAgents/com.aimux.model-proxy.plist"
    [[ $(bash -c 'source "$1"; printf %s "$stable"' _ "$script") == "$HOME/elsewhere/aimux" ]] || { printf 'FAIL: Program was not honoured\n' >&2; exit 1; }
    # A job started through a wrapper names the wrapper: it is never replaced.
    /usr/libexec/PlistBuddy -c "Set :Program /bin/sh" "$HOME/Library/LaunchAgents/com.aimux.model-proxy.plist"
    if out=$(bash "$script" build-dev 2>&1); then printf 'FAIL: a wrapper was accepted as the promotion target\n' >&2; exit 1; fi
    [[ $out == *'not an aimux binary'* ]] || { printf 'FAIL: %s\n' "$out" >&2; exit 1; }
    # A job file with no program at all is refused, not silently replaced by the CLI.
    printf '<?xml version="1.0" encoding="UTF-8"?>\n<plist version="1.0"><dict><key>Label</key><string>x</string></dict></plist>\n' > "$HOME/Library/LaunchAgents/com.aimux.model-proxy.plist"
    if out=$(bash "$script" build-dev 2>&1); then printf 'FAIL: a job file with no program was accepted\n' >&2; exit 1; fi
    [[ $out == *'Cannot read the program'* ]] || { printf 'FAIL: %s\n' "$out" >&2; exit 1; }
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
  inference)
    # A real local HTTP server stands in for the stable proxy: inference_ok must see the answer, not just a 200.
    port_file=$scratch/port
    serve() {
      python3 -I -c '
import http.server, sys
reply = sys.argv[2].encode()
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get("content-length", 0)))
        ok = self.headers.get("x-api-key") == "test-key" and self.path == "/v1/messages"
        self.send_response(200 if ok else 401); self.end_headers(); self.wfile.write(reply)
    def log_message(self, *a): pass
s = http.server.HTTPServer(("127.0.0.1", 0), H); open(sys.argv[1], "w").write(str(s.server_port)); s.serve_forever()
' "$port_file" "$1" &
      server=$!
      for _ in $(seq 50); do [[ -s "$port_file" ]] && break; sleep 0.1; done
    }
    source "$script"
    key=test-key
    serve '{"content":[{"type":"text","text":"AIMUX_PROXY_OK"}]}'
    inference_ok "$(< "$port_file")" || { kill "$server"; printf 'FAIL: a correct answer was rejected\n' >&2; exit 1; }
    kill "$server"; rm -f "$port_file"
    serve '{"type":"error","error":{"type":"api_error","message":"codex app-server exited"}}'
    if inference_ok "$(< "$port_file")"; then kill "$server"; printf 'FAIL: a failed inference passed\n' >&2; exit 1; fi
    kill "$server"
    ;;
  retry)
    # The first answer after a restart can be slow enough to fail. One retry, and only one, decides the promote.
    port_file=$scratch/port
    count_file=$scratch/requests
    serve() {
      : > "$count_file"; rm -f "$port_file"
      python3 -I -c '
import http.server, sys
replies = [r.encode() for r in sys.argv[3:]]
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get("content-length", 0)))
        open(sys.argv[2], "a").write("x")
        reply = replies.pop(0) if len(replies) > 1 else replies[0]
        self.send_response(200); self.end_headers(); self.wfile.write(reply)
    def log_message(self, *a): pass
s = http.server.HTTPServer(("127.0.0.1", 0), H); open(sys.argv[1], "w").write(str(s.server_port)); s.serve_forever()
' "$port_file" "$count_file" "$@" &
      server=$!
      for _ in $(seq 50); do [[ -s "$port_file" ]] && break; sleep 0.1; done
    }
    requests() { wc -c < "$count_file" | tr -d ' '; }
    source "$script"
    key=test-key
    good='{"content":[{"type":"text","text":"AIMUX_PROXY_OK"}]}'
    bad='{"type":"error","error":{"type":"api_error","message":"timeout"}}'
    serve "$bad" "$good"
    stable_inference_ok "$(< "$port_file")" 2>"$scratch/retry.err" || { kill "$server"; printf 'FAIL: a slow first answer rejected the build\n' >&2; exit 1; }
    kill "$server"
    [[ $(requests) == 2 ]] || { printf 'FAIL: expected 2 requests, saw %s\n' "$(requests)" >&2; exit 1; }
    grep -q 'trying once more' "$scratch/retry.err" || { printf 'FAIL: the retry was not announced\n' >&2; exit 1; }
    serve "$good"
    stable_inference_ok "$(< "$port_file")" || { kill "$server"; printf 'FAIL: a correct answer was rejected\n' >&2; exit 1; }
    kill "$server"
    [[ $(requests) == 1 ]] || { printf 'FAIL: a passing check was repeated (%s requests)\n' "$(requests)" >&2; exit 1; }
    serve "$bad"
    if stable_inference_ok "$(< "$port_file")" 2>/dev/null; then kill "$server"; printf 'FAIL: two failed answers passed\n' >&2; exit 1; fi
    kill "$server"
    [[ $(requests) == 2 ]] || { printf 'FAIL: expected exactly one retry, saw %s requests\n' "$(requests)" >&2; exit 1; }
    ;;
  interrupt)
    # An interrupt between the swap and the stable check must restore the previous binary and restart stable.
    mkdir -p "$scratch/fakebin"
    printf '#!/bin/sh\necho "$@" >> "%s/launchctl.log"\n' "$scratch" > "$scratch/fakebin/launchctl"
    chmod +x "$scratch/fakebin/launchctl"
    PATH=$scratch/fakebin:$PATH
    source "$script"
    job=gui/test/com.aimux.model-proxy
    install_candidate "$HOME/go/bin/aimux-dev"
    rollback_armed=1
    rollback_if_armed
    [[ $(< "$HOME/go/bin/aimux") == old ]] || { printf 'FAIL: candidate left installed\n' >&2; exit 1; }
    grep -q 'kickstart -k gui/test/com.aimux.model-proxy' "$scratch/launchctl.log" || { printf 'FAIL: stable not restarted\n' >&2; exit 1; }
    : > "$scratch/launchctl.log"; rollback_armed=0
    install_candidate "$HOME/go/bin/aimux-dev"
    rollback_if_armed
    [[ $(< "$HOME/go/bin/aimux") == candidate && ! -s "$scratch/launchctl.log" ]] || { printf 'FAIL: disarmed rollback acted\n' >&2; exit 1; }
    # A promotion is refused when launchd has another program loaded than the job file names.
    printf '#!/bin/sh\nprintf "\\tstate = running\\n\\tprogram = %%s\\n" "$LOADED"\n' > "$scratch/fakebin/launchctl"
    LOADED=$stable loaded_matches_job_file || { printf 'FAIL: a matching loaded program was refused\n' >&2; exit 1; }
    if LOADED=/somewhere/else/aimux loaded_matches_job_file; then printf 'FAIL: a different loaded program was accepted\n' >&2; exit 1; fi
    ;;
  *) exit 2 ;;
esac
printf 'PASS: %s\n' "${1:-guard}"
