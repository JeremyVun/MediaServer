#!/usr/bin/env bash
#
# 24 h soak test (M9). Drives a RUNNING media-server through its real HTTP API
# in a tight loop — upload a file, direct-play + seek it, transcode-play + seek
# it, then delete and purge it — while sampling process RSS, goroutine count,
# and ffmpeg child count. At the end it asserts a flat RSS trend and zero
# leaked ffmpeg children/goroutines, and writes pprof heap+goroutine snapshots
# from before and after for a manual diff.
#
# The server must be running with debug.pprof_port set (see config.example.yml)
# so this script can read /debug/pprof. Point PPROF_URL at that loopback port.
#
# Usage:
#   scripts/soak.sh                       # 24 h against localhost:8484
#   SOAK_DURATION=10m scripts/soak.sh     # short run
#   SOAK_DURATION=45s scripts/soak.sh     # smoke
#
# Env overrides:
#   BASE_URL        default http://127.0.0.1:8484
#   PPROF_URL       default http://127.0.0.1:6060   (match config debug.pprof_port)
#   SOAK_DURATION   default 24h  (accepts 30, 45s, 10m, 24h)
#   ROOT_ID         default: first online+attached root
#   FFMPEG          default ffmpeg
#   OUT_DIR         default ./soak-out
#   SAMPLE_EVERY_S  default 60   (seconds between resource samples)
#   RSS_GROWTH_PCT  default 25   (max allowed final-vs-baseline RSS growth)
#   GORO_GROWTH     default 50   (max allowed goroutine increase)
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:8484}"
PPROF_URL="${PPROF_URL:-http://127.0.0.1:6060}"
SOAK_DURATION="${SOAK_DURATION:-24h}"
FFMPEG="${FFMPEG:-ffmpeg}"
OUT_DIR="${OUT_DIR:-./soak-out}"
SAMPLE_EVERY_S="${SAMPLE_EVERY_S:-60}"
RSS_GROWTH_PCT="${RSS_GROWTH_PCT:-25}"
GORO_GROWTH="${GORO_GROWTH:-50}"

mkdir -p "$OUT_DIR"

die() { echo "soak: $*" >&2; exit 1; }

# --- tiny JSON reader (python3): json_get '<dotted.path>' < body -------------
# The program is passed via -c so the piped JSON stays on stdin (a `-` +
# heredoc would hand the heredoc to stdin and starve json.load).
json_get() {
  python3 -c '
import sys, json
d = json.load(sys.stdin)
for part in sys.argv[1].split("."):
    if part == "":
        continue
    d = d[int(part)] if part.isdigit() else d[part]
print("" if d is None else d)
' "$1"
}

to_seconds() {
  local v="$1"
  case "$v" in
    *h) echo $(( ${v%h} * 3600 ));;
    *m) echo $(( ${v%m} * 60 ));;
    *s) echo "${v%s}";;
    *)  echo "$v";;
  esac
}

# GET/POST/DELETE helpers that fail loudly on non-2xx. Body -> stdout.
http() { # method path [curl-args...]
  local method="$1" path="$2"; shift 2
  local body code
  body="$(curl -sS -X "$method" "$@" -w $'\n%{http_code}' "$BASE_URL$path")" || die "curl $method $path failed"
  code="${body##*$'\n'}"; body="${body%$'\n'*}"
  case "$code" in 2*) printf '%s' "$body";; *) die "$method $path -> HTTP $code: $body";; esac
}

command -v python3 >/dev/null || die "python3 required for JSON parsing"
command -v "$FFMPEG" >/dev/null || die "ffmpeg not found (set FFMPEG=)"

PORT="${BASE_URL##*:}"; PORT="${PORT%%/*}"
SERVER_PID="${SERVER_PID:-$(lsof -nP -iTCP:"$PORT" -sTCP:LISTEN -t 2>/dev/null | head -1 || true)}"
[ -n "$SERVER_PID" ] || die "could not find server PID listening on :$PORT (set SERVER_PID=)"

curl -sf "$BASE_URL/api/health" >/dev/null || die "server health check failed at $BASE_URL"
curl -sf "$PPROF_URL/debug/pprof/" >/dev/null || die "pprof not reachable at $PPROF_URL (set debug.pprof_port and PPROF_URL)"

# Resolve target root.
ROOTS_JSON="$(http GET /api/roots)"
if [ -z "${ROOT_ID:-}" ]; then
  ROOT_ID="$(printf '%s' "$ROOTS_JSON" | python3 -c '
import sys, json
data = json.load(sys.stdin)
roots = data if isinstance(data, list) else data.get("roots", [])
for r in roots:
    if r.get("online") and r.get("attached", True):
        print(r["id"]); break
')"
fi
[ -n "$ROOT_ID" ] || die "no online+attached root found; set ROOT_ID="
echo "soak: server pid=$SERVER_PID root_id=$ROOT_ID base=$BASE_URL pprof=$PPROF_URL"

# --- fixture: one small H.264/AAC MP4, reused with a fresh name each round ----
FIXTURE="$OUT_DIR/soak-fixture.mp4"
if [ ! -f "$FIXTURE" ]; then
  echo "soak: generating fixture"
  # ~30 s so a forced full transcode stays alive long enough to be sampled;
  # low resolution keeps each transcode cheap.
  "$FFMPEG" -hide_banner -nostdin -y -v error \
    -f lavfi -i testsrc=size=320x180:rate=15 \
    -f lavfi -i sine=frequency=1000:sample_rate=48000 \
    -t 30 -c:v libx264 -pix_fmt yuv420p -c:a aac "$FIXTURE"
fi
FIXTURE_SIZE="$(stat -f%z "$FIXTURE")"

# --- resource sampling -------------------------------------------------------
rss_kb()   { ps -o rss= -p "$SERVER_PID" 2>/dev/null | tr -d ' '; }
goroutines() { curl -s "$PPROF_URL/debug/pprof/goroutine?debug=1" | sed -n '1s/.*total //p'; }
ffmpeg_children() { ps -ax -o ppid=,comm= | awk -v p="$SERVER_PID" '$1==p && $2 ~ /ffmpeg/' | wc -l | tr -d ' '; }

snapshot() { # label
  curl -s "$PPROF_URL/debug/pprof/heap" -o "$OUT_DIR/heap.$1.pprof" || true
  curl -s "$PPROF_URL/debug/pprof/goroutine?debug=1" -o "$OUT_DIR/goroutine.$1.txt" || true
}

# --- one soak iteration ------------------------------------------------------
soak_once() { # token
  local token="$1"
  local fname="Soak.Probe.${token}.(2020).mp4"

  # Upload (single chunk; fixture < 8 MB chunk cap).
  local up id
  up="$(http POST /api/uploads -H 'Content-Type: application/json' \
        --data "{\"filename\":\"$fname\",\"size\":$FIXTURE_SIZE,\"root_id\":$ROOT_ID}")"
  id="$(printf '%s' "$up" | json_get id)"
  http PUT "/api/uploads/$id" \
    -H "Content-Range: bytes 0-$((FIXTURE_SIZE-1))/$FIXTURE_SIZE" \
    -H 'Content-Type: application/octet-stream' \
    --data-binary "@$FIXTURE" >/dev/null
  http POST "/api/uploads/$id/complete" >/dev/null

  # Wait for the probe to create the item (search FTS is prefix-matched).
  local item_id='' deadline=$(( $(date +%s) + 30 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    item_id="$(http GET "/api/search?q=$token" | json_get items.0.id 2>/dev/null || true)"
    [ -n "$item_id" ] && break
    sleep 0.5
  done
  [ -n "$item_id" ] || die "item for $token never appeared"

  local detail file_id
  detail="$(http GET "/api/items/$item_id")"
  file_id="$(printf '%s' "$detail" | json_get files.0.id)"

  # Direct play + two seeks (Range requests on the raw stream).
  local direct_caps='{"file_id":'"$file_id"',"capabilities":{"containers":["mp4","mov"],"video_codecs":["h264"],"audio_codecs":["aac"],"max_height":2160,"native_hls":true}}'
  http POST "/api/items/$item_id/play" -H 'Content-Type: application/json' --data "$direct_caps" >/dev/null
  curl -sf -o /dev/null -H "Range: bytes=0-65535" "$BASE_URL/api/files/$file_id/stream" || die "stream head failed"
  curl -sf -o /dev/null -H "Range: bytes=$((FIXTURE_SIZE/2))-$((FIXTURE_SIZE-1))" "$BASE_URL/api/files/$file_id/stream" || die "stream seek failed"

  # Transcode play (force a full video transcode via unsupported video codec)
  # + HLS seek + teardown. Full transcode is the heaviest ffmpeg path and runs
  # long enough to be observed live below.
  local hls_caps='{"file_id":'"$file_id"',"capabilities":{"containers":["nope"],"video_codecs":["vp9"],"audio_codecs":["aac"],"max_height":2160,"native_hls":true}}'
  local play sid url
  play="$(http POST "/api/items/$item_id/play" -H 'Content-Type: application/json' --data "$hls_caps")"
  sid="$(printf '%s' "$play" | json_get session_id 2>/dev/null || true)"
  if [ -n "$sid" ]; then
    curl -sf -o /dev/null "$BASE_URL/api/sessions/$sid/master.m3u8" || true
    curl -sf -o /dev/null "$BASE_URL/api/sessions/$sid/init.mp4" || true
    curl -sf -o /dev/null "$BASE_URL/api/sessions/$sid/seg-00000.m4s" || true
    curl -sf -o /dev/null "$BASE_URL/api/sessions/$sid/seg-00001.m4s" || true   # seek forward
    # Observe the transcode child while it is live (short fixtures exit between
    # the periodic samples, so the peak below would otherwise read 0).
    local f; f="$(ffmpeg_children)"
    [ "$f" -gt "$max_ffmpeg" ] && max_ffmpeg="$f"
    http POST "/api/sessions/$sid/teardown" >/dev/null || true
  fi

  # Clean up: soft delete then purge bytes so the library/disk stays flat.
  http DELETE "/api/items/$item_id" >/dev/null
  http DELETE "/api/items/$item_id/purge" >/dev/null || true
}

# --- run ---------------------------------------------------------------------
DURATION_S="$(to_seconds "$SOAK_DURATION")"
END=$(( $(date +%s) + DURATION_S ))
snapshot before
BASE_RSS="$(rss_kb)"; BASE_GORO="$(goroutines)"
echo "soak: baseline rss=${BASE_RSS}KB goroutines=${BASE_GORO} — running ${DURATION_S}s"
printf 'ts\titer\trss_kb\tgoroutines\tffmpeg\n' > "$OUT_DIR/samples.tsv"

iter=0; last_sample=0; max_ffmpeg=0
while [ "$(date +%s)" -lt "$END" ]; do
  iter=$((iter+1))
  soak_once "soak$(printf '%06d' "$iter")"
  now="$(date +%s)"
  if [ $(( now - last_sample )) -ge "$SAMPLE_EVERY_S" ]; then
    last_sample="$now"
    r="$(rss_kb)"; g="$(goroutines)"; f="$(ffmpeg_children)"
    [ "$f" -gt "$max_ffmpeg" ] && max_ffmpeg="$f"
    printf '%s\t%d\t%s\t%s\t%s\n' "$now" "$iter" "$r" "$g" "$f" | tee -a "$OUT_DIR/samples.tsv"
  fi
done

echo "soak: ran $iter iterations; settling 90s for idle HLS reaping"
sleep 90
snapshot after
FINAL_RSS="$(rss_kb)"; FINAL_GORO="$(goroutines)"; FINAL_FFMPEG="$(ffmpeg_children)"

echo "----------------------------------------------------------------------"
echo "iterations       : $iter"
echo "rss  baseline    : ${BASE_RSS}KB"
echo "rss  final       : ${FINAL_RSS}KB"
echo "goroutines       : ${BASE_GORO} -> ${FINAL_GORO}"
echo "ffmpeg peak/final: ${max_ffmpeg} / ${FINAL_FFMPEG}"
echo "snapshots        : $OUT_DIR/{heap,goroutine}.{before,after}.*"
echo "----------------------------------------------------------------------"

fail=0
rss_limit=$(( BASE_RSS + BASE_RSS * RSS_GROWTH_PCT / 100 ))
if [ "${FINAL_RSS:-0}" -gt "$rss_limit" ]; then
  echo "FAIL: RSS grew ${BASE_RSS}KB -> ${FINAL_RSS}KB (> ${RSS_GROWTH_PCT}%)"; fail=1
fi
if [ "$(( FINAL_GORO - BASE_GORO ))" -gt "$GORO_GROWTH" ]; then
  echo "FAIL: goroutines grew ${BASE_GORO} -> ${FINAL_GORO} (> ${GORO_GROWTH})"; fail=1
fi
if [ "${FINAL_FFMPEG:-0}" -ne 0 ]; then
  echo "FAIL: ${FINAL_FFMPEG} ffmpeg child(ren) still running 90s after last player closed"; fail=1
fi

if [ "$fail" -eq 0 ]; then
  echo "PASS: flat RSS, no goroutine leak, no orphaned ffmpeg"
else
  echo "See $OUT_DIR/samples.tsv and the pprof snapshots for the leak."
fi
exit "$fail"
