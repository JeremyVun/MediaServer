#!/bin/sh
# Stands in for the codex CLI in tests. FAKE_CODEX_MODE picks the behaviour;
# FAKE_CODEX_RECORD, if set, is a directory that receives the argv, cwd,
# schema and pids.
dir=$(cd "$(dirname "$0")" && pwd)

if [ -n "$FAKE_CODEX_RECORD" ]; then
  printf '%s\0' "$@" > "$FAKE_CODEX_RECORD/argv"
  pwd > "$FAKE_CODEX_RECORD/cwd"
  prev=
  for a in "$@"; do
    [ "$prev" = "--output-schema" ] && cp "$a" "$FAKE_CODEX_RECORD/schema.json"
    prev=$a
  done
  echo $$ > "$FAKE_CODEX_RECORD/pid"
fi

answer() {
  printf '{"type":"thread.started","thread_id":"t"}\n{"type":"turn.started"}\n'
  printf '{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":%s}}\n' "$1"
  printf '{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}\n'
}

case "$FAKE_CODEX_MODE" in
  ok) cat "$dir/transcript-ok.jsonl" ;;
  malformed) answer '"{\"kind\":\"episode\",\"show_title\":"' ;;
  bad-kind) answer '"{\"kind\":\"documentary\",\"show_title\":null,\"season\":null,\"episode\":null,\"episode_end\":null,\"episode_title\":null,\"title\":\"X\",\"year\":null,\"confident\":true}"' ;;
  no-answer) printf '{"type":"thread.started","thread_id":"t"}\n{"type":"turn.completed","usage":{}}\n' ;;
  login-expired)
    cat "$dir/transcript-login-expired.jsonl"
    echo "Error: unexpected status 401 Unauthorized" >&2
    exit 1 ;;
  crash)
    echo "thread 'main' panicked" >&2
    exit 101 ;;
  sleep)
    sleep 30 &
    [ -n "$FAKE_CODEX_RECORD" ] && echo $! > "$FAKE_CODEX_RECORD/sleeppid"
    wait ;;
  *) echo "unknown FAKE_CODEX_MODE=$FAKE_CODEX_MODE" >&2; exit 2 ;;
esac
