#!/usr/bin/env bash
# End-to-end check of examples/fullstack-app: run after `npm run build -w fullstack-app`.
# Usage: scripts/e2e.sh [node|bun]
set -uo pipefail
cd "$(dirname "$0")/../examples/fullstack-app"

RUNTIME="${1:-node}"
PORT="${PORT:-$(python3 -c "import socket;s=socket.socket();s.bind(('127.0.0.1',0));print(s.getsockname()[1])")}"
BASE="http://127.0.0.1:$PORT"
LOG="$(mktemp)"
FAILS=0

pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAILS=$((FAILS + 1)); }
check() { if eval "$2"; then pass "$1"; else fail "$1"; fi; }
hdr() { curl -s -o /dev/null -D - "${@:2}" "$BASE$1" | tr -d '\r'; }

PORT=$PORT NEXTGIN_RUNTIME=$RUNTIME NEXTGIN_DEBUG_HEADERS=1 GIN_MODE=release ./server >"$LOG" 2>&1 &
SERVER=$!
trap 'kill $SERVER 2>/dev/null' EXIT
for _ in $(seq 1 150); do
  kill -0 $SERVER 2>/dev/null || { echo "server exited during startup:"; cat "$LOG"; exit 1; }
  curl -sf "$BASE/api/health" >/dev/null && break; sleep 0.2
done
workers() { pgrep -P "$SERVER" | xargs; }

echo "runtime=$RUNTIME server pid=$SERVER"

check "Go API: GET /api/health" '[[ "$(curl -s $BASE/api/health)" == "{\"status\":\"ok\"}" ]]'

H=$(hdr /)
check "static page / prerendered by Go"       'grep -qi "^x-nextgin-source: prerendered" <<<"$H" && grep -q "^HTTP/1.1 200" <<<"$H"'
H=$(hdr /about)
check "/about: Go applied next.config header"  'grep -qi "^x-powered-by-go: gin" <<<"$H" && grep -qi "x-nextgin-source: prerendered" <<<"$H"'
ETAG=$(grep -i '^etag:' <<<"$H" | cut -d' ' -f2)
check "/about: ETag revalidates to 304"        '[[ "$(curl -s -o /dev/null -w %{http_code} -H "If-None-Match: $ETAG" $BASE/about)" == 304 ]]'
H=$(hdr /robots.txt)
check "public/robots.txt served by Go"         'grep -qi "x-nextgin-source: static" <<<"$H" && grep -q "^HTTP/1.1 200" <<<"$H"'
CHUNK=$(curl -s $BASE/ | grep -o '/_next/static/[^"]*\.js' | head -1)
H=$(hdr "$CHUNK")
check "build chunk immutable from Go"          'grep -qi "cache-control: public,max-age=31536000,immutable" <<<"$H" && grep -qi "x-nextgin-source: static" <<<"$H"'
H=$(hdr /home)
check "redirect rule handled by Next (307 /)"  'grep -q "^HTTP/1.1 307" <<<"$H" && grep -qi "^location: /$" <<<"$H"'
H=$(hdr /todos)
check "/todos SSR via worker + proxy.ts ran"   'grep -qi "x-nextgin-source: worker" <<<"$H" && grep -qi "x-next-proxy: 1" <<<"$H"'
check "/todos SSR fetched data from Go API"    'curl -s $BASE/todos | grep -q "Serve Next.js from Go"'
H=$(hdr /about -H "RSC: 1")
# Without the client-computed _rsc hash Next 16 answers 307 -> ?_rsc (header validation); either way it must be the worker.
check "RSC navigation request -> worker"       'grep -qi "x-nextgin-source: worker" <<<"$H" && grep -qiE "content-type: text/x-component|^location: /about\?_rsc" <<<"$H"'

T=$(curl -s -N -o "$LOG.stream" -w '%{time_starttransfer} %{time_total}' $BASE/stream)
read -r TTFB TOTAL <<<"$T"
check "Suspense streams: TTFB<1s ($TTFB), total>=1.5s ($TOTAL)" 'awk "BEGIN{exit !($TTFB < 1.0 && $TOTAL >= 1.5)}" && grep -q "Loaded after" "$LOG.stream"'
T=$(curl -s -N -o /dev/null -H 'Accept-Encoding: gzip' -w '%{time_starttransfer} %{time_total}' $BASE/stream)
read -r TTFB TOTAL <<<"$T"
check "Suspense streams with gzip: TTFB<1s ($TTFB)" 'awk "BEGIN{exit !($TTFB < 1.0 && $TOTAL >= 1.5)}"'

# Server Action via progressive-enhancement form POST (what a no-JS browser does).
ACTION_FIELD=$(curl -s $BASE/todos | grep -o 'name="\$ACTION_ID_[a-f0-9]*"' | head -1 | sed 's/name="//; s/"$//')
TITLE="e2e-$RANDOM"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -H "Origin: $BASE" -F "$ACTION_FIELD=" -F "title=$TITLE" $BASE/todos)
check "Server Action POST ($ACTION_FIELD -> $CODE) persisted via Go API" '[[ -n "$ACTION_FIELD" ]] && curl -s $BASE/api/todos | grep -q "$TITLE"'
check "Client-component API call: PATCH /api/todos/2" 'curl -s -X PATCH -H "content-type: application/json" -d "{\"done\":true}" $BASE/api/todos/2 | grep -q "\"done\":true"'

# Crash recovery: kill one worker, traffic keeps flowing and it comes back.
WPIDS=$(workers)
VICTIM=$(cut -d' ' -f1 <<<"$WPIDS")
kill -9 "$VICTIM" 2>/dev/null
OK=1; for _ in $(seq 1 10); do [[ "$(curl -s -o /dev/null -w %{http_code} $BASE/todos)" == 200 ]] || OK=0; done
check "serving continues after worker SIGKILL"  '[[ $OK == 1 ]]'
sleep 3
check "crashed worker restarted ($VICTIM replaced)" '[[ $(workers | wc -w) -ge 2 ]] && ! grep -qw "$VICTIM" <<<"$(workers)" && grep -q "restarting" "$LOG"'

SOCKS=$(ls /tmp/nextgin-$SERVER-*.sock 2>/dev/null | wc -l)
FINAL_WORKERS=$(workers)
kill -TERM $SERVER; wait $SERVER 2>/dev/null
sleep 0.5
check "graceful shutdown: $SOCKS sockets removed, no workers left" '[[ $SOCKS -ge 2 ]] && ! ls /tmp/nextgin-$SERVER-*.sock >/dev/null 2>&1 && ! for p in $FINAL_WORKERS; do kill -0 $p 2>/dev/null && exit 0; done'

echo
if [[ $FAILS -eq 0 ]]; then echo "all checks passed"; else echo "$FAILS check(s) failed — server log: $LOG"; exit 1; fi
