#!/bin/bash
# Record assets/demo.gif in a container. Needs podman (or docker) and Go.
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(git rev-parse --show-toplevel)
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
mkdir -p bin out
# aimux embeds the web UI; rebuild it so the recording shows this checkout.
make -C "$ROOT" web-build >/dev/null
GOOS=linux GOARCH=$ARCH CGO_ENABLED=0 go build -o bin/aimux "$ROOT/cmd/aimux"
GOOS=linux GOARCH=$ARCH CGO_ENABLED=0 go build -o bin/claude ./fakeagent
ENGINE=${ENGINE:-$(command -v podman || command -v docker)}
"$ENGINE" build -q -t localhost/aimux-demo -f Containerfile .
"$ENGINE" run --rm -v "$PWD/out:/out:Z" localhost/aimux-demo
# VHS writes MP4 (GIF encoding runs out of memory in small VMs); convert here.
ffmpeg -y -loglevel error -i out/demo.mp4 \
  -vf "fps=15,scale=1240:-1:flags=lanczos,split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=4" \
  "$ROOT/assets/demo.gif"
echo "wrote assets/demo.gif ($(du -h "$ROOT/assets/demo.gif" | cut -f1))"

# Web dashboard screenshot: serve aimux web from the same seeded container.
if [ "${SKIP_WEB:-}" != 1 ] && command -v node >/dev/null; then
  "$ENGINE" rm -f aimux-demo-web >/dev/null 2>&1 || true
  "$ENGINE" run -d --name aimux-demo-web -e DEMO_MODE=web -p 127.0.0.1:3000:3001 localhost/aimux-demo >/dev/null
  trap '"$ENGINE" rm -f aimux-demo-web >/dev/null 2>&1' EXIT
  # Wait until the dashboard serves all 4 seeded agents (up to 60 s).
  ready=
  for _ in $(seq 60); do
    n=$(curl -fs http://127.0.0.1:3000/api/agents 2>/dev/null | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["agents"]))' 2>/dev/null || true)
    [ "$n" = 4 ] && { ready=1; break; }
    sleep 1
  done
  [ -n "$ready" ] || { echo "web dashboard did not list the 4 demo agents within 60 s" >&2; exit 1; }
  python3 -c 'import PIL' 2>/dev/null || { echo "web screenshot needs Pillow: python3 -m pip install pillow" >&2; exit 1; }
  npm ls playwright >/dev/null 2>&1 || npm i -s --no-save --no-package-lock playwright@1.47.2 >/dev/null
  npx playwright install chromium >/dev/null
  node web-shot.mjs out/web.png
  python3 round.py out/web.png "$ROOT/assets/web-dashboard.png"
  echo "wrote assets/web-dashboard.png"
fi
