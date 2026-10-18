#!/bin/sh
# Stands in for the codex CLI: prints the transcript a test wrote to
# $FAKE_SOL_TRANSCRIPT, or fails like an expired login when
# FAKE_SOL_MODE=fail. Each call appends a line to $FAKE_SOL_CALLS.
[ -n "$FAKE_SOL_CALLS" ] && echo call >> "$FAKE_SOL_CALLS"
if [ "$FAKE_SOL_MODE" = fail ]; then
  echo "Error: unexpected status 401 Unauthorized" >&2
  exit 1
fi
cat "$FAKE_SOL_TRANSCRIPT"
