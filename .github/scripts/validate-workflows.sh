#!/usr/bin/env bash
# Validate every workflow against LIVE registries, without needing a server to
# already be running.
#
# `wfx validate` posts to the API on purpose: a workflow names providers,
# classifiers, skills and MCP servers, and whether those resolve is a property
# of the server's registries, not of the file. That made `task check` depend on
# a developer happening to have `task api` up — true on every machine we work
# on, false on a clean checkout, so CI failed on its first run with
# "connection refused" and the target had never been hermetic.
#
# So: start one on an ephemeral port, validate, stop. SQLite in a temp dir, so
# it never touches ~/.local/share/wfnexus.
set -euo pipefail

BIN="${BIN:-bin}"
SERVER="$BIN/wfx-server"
CLI="$BIN/wfx"
[ -x "$SERVER" ] || { echo "no $SERVER — run 'task build:api' first" >&2; exit 1; }
[ -x "$CLI" ] || { echo "no $CLI — run 'task build:cli' first" >&2; exit 1; }

# Port 0 would be ideal, but the server prints its address rather than taking
# one back, so pick a high port and retry if it is taken.
PORT="${PORT:-$((20000 + RANDOM % 20000))}"
STATE="$(mktemp -d)"
LOG="$STATE/server.log"
cleanup() {
  [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null || true
  rm -rf "$STATE"
}
trap cleanup EXIT

WFX_MODE=local \
WFX_ADDR="127.0.0.1:$PORT" \
WFX_STORAGE_DRIVER=sqlite \
DATABASE_URL="$STATE/wfnexus.db" \
WFX_ARTIFACT_DRIVER=folder \
WFX_ARTIFACT_DIR="$STATE/artifacts" \
  "$SERVER" >"$LOG" 2>&1 &
PID=$!

for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:$PORT/api/health" >/dev/null 2>&1; then ready=1; break; fi
  kill -0 "$PID" 2>/dev/null || { echo "server exited before it listened:" >&2; tail -20 "$LOG" >&2; exit 1; }
  sleep 0.5
done
[ "${ready:-}" = 1 ] || { echo "server did not listen on $PORT in 30s:" >&2; tail -20 "$LOG" >&2; exit 1; }

rc=0
for f in workflows/*.yaml workflows/*.yml; do
  [ -e "$f" ] || continue
  if WFX_API="http://127.0.0.1:$PORT" "$CLI" validate "$f"; then :; else rc=1; fi
done

[ "$rc" -eq 0 ] && echo "workflows: all valid against live registries"
exit "$rc"
