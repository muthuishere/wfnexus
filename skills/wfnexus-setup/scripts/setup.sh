#!/usr/bin/env bash
# wfnexus from nothing to ready, on one machine with Docker.
#
#   setup.sh [--dir <wfnexus checkout>] [--port 8090] [--project wfnexus] [--no-build]
#
# Every stage prints one line: "ok  <stage>" or "BLOCKED <stage>: <why> -> <what to do>".
# The last line is either "READY <url>" or "BLOCKED: <stage>". Exit 0 only when ready.
#
# SECRETS: the admin token is read from the server's log into a shell variable
# and handed to curl; key values come from the caller's environment and are
# piped into `wfx env set`. None of them is ever printed, logged or written to
# a file by this script.
set -uo pipefail

DIR="${WFX_DIR:-}"
PORT="${WFX_PORT:-}"
PROJECT="${WFX_PROJECT_NAME:-}"
BUILD="--build"
while [ $# -gt 0 ]; do
  case "$1" in
    --dir) DIR="$2"; shift 2 ;;
    --port) PORT="$2"; shift 2 ;;
    --project) PROJECT="$2"; shift 2 ;;
    --no-build) BUILD=""; shift ;;
    -h|--help) sed -n 2,12p "$0"; exit 0 ;;
    *) echo "unknown argument $1" >&2; exit 2 ;;
  esac
done

ok()      { printf 'ok  %s\n' "$*"; }
note()    { printf '    %s\n' "$*"; }
blocked() { printf 'BLOCKED %s: %s\n' "$1" "$2"; printf 'BLOCKED: %s\n' "$1"; exit 1; }

# ---- 1. docker ------------------------------------------------------------
command -v docker >/dev/null 2>&1 || blocked docker "docker is not installed -> install Docker Desktop (or OrbStack/colima) and start it"
docker info >/dev/null 2>&1 || blocked docker "the Docker daemon is not running -> start Docker Desktop and re-run"
docker compose version >/dev/null 2>&1 || blocked docker "docker compose v2 is missing -> update Docker Desktop"
ok "docker $(docker version -f '{{.Server.Version}}' 2>/dev/null)"

# ---- 2. the checkout and its .env ----------------------------------------
if [ -z "$DIR" ]; then
  d="$PWD"
  while [ "$d" != "/" ]; do
    if [ -f "$d/docker-compose.yml" ] && [ -f "$d/infra/Dockerfile" ]; then DIR="$d"; break; fi
    d=$(dirname "$d")
  done
fi
[ -n "$DIR" ] && [ -f "$DIR/docker-compose.yml" ] || blocked checkout "no wfnexus checkout found -> git clone https://github.com/muthuishere/wfnexus && re-run with --dir wfnexus"
cd "$DIR" || blocked checkout "cannot enter $DIR"
if [ ! -f .env ] && [ -f compose.env.example ]; then
  cp compose.env.example .env
  note "created .env from compose.env.example (no keys in it; keys go in with wfx env set)"
fi
# The caller's flags win over .env: compose reads the shell before the file.
[ -n "$PORT" ] && export WFX_PORT="$PORT"
[ -n "$PROJECT" ] && export WFX_PROJECT_NAME="$PROJECT"
envval() { sed -n "s/^$1=//p" .env 2>/dev/null | tail -1; }
PORT="${WFX_PORT:-$(envval WFX_PORT)}"; PORT="${PORT:-8090}"
PROJECT="${WFX_PROJECT_NAME:-$(envval WFX_PROJECT_NAME)}"; PROJECT="${PROJECT:-wfnexus}"
export WFX_PORT="$PORT" WFX_PROJECT_NAME="$PROJECT"
URL="http://localhost:$PORT"
ok "checkout $DIR (project $PROJECT, port $PORT)"

# ---- 3. up ----------------------------------------------------------------
running=$(docker compose -p "$PROJECT" ps -q wfx-server 2>/dev/null)
if [ -z "$running" ] && lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
  blocked port "port $PORT is already in use by something else -> re-run with --port <free port>"
fi
if ! docker compose -p "$PROJECT" up -d $BUILD >/tmp/wfx-setup-up.$$ 2>&1; then
  tail -20 /tmp/wfx-setup-up.$$; rm -f /tmp/wfx-setup-up.$$
  blocked compose "docker compose up failed (output above)"
fi
rm -f /tmp/wfx-setup-up.$$
ok "docker compose up -d"

# ---- 4. health ------------------------------------------------------------
# The first boot of a fresh install exits on purpose (it mints the admin and
# refuses to serve unauthenticated); restart: unless-stopped brings it back.
healthy=""
for _ in $(seq 1 100); do
  if curl -fsS "$URL/api/health" >/dev/null 2>&1; then healthy=1; break; fi
  sleep 3
done
[ -n "$healthy" ] || { docker compose -p "$PROJECT" logs --tail 30 wfx-server | grep -v 'admin token'; blocked health "$URL/api/health did not answer in 5 minutes (log above)"; }
ok "healthy at $URL"

# ---- 5. the wfx CLI -------------------------------------------------------
if ! command -v wfx >/dev/null 2>&1; then
  blocked cli "wfx is not on PATH -> sh $DIR/install.sh (or: cd $DIR && task install), then re-run"
fi
ok "wfx $(wfx version 2>/dev/null | head -1)"

# ---- 6. skills ------------------------------------------------------------
wfx install --skills >/tmp/wfx-setup-skills.$$ 2>&1 || { cat /tmp/wfx-setup-skills.$$; rm -f /tmp/wfx-setup-skills.$$; blocked skills "wfx install --skills failed (output above)"; }
ok "skills: $(grep -m1 -iE "installed|already" /tmp/wfx-setup-skills.$$ | sed "s/^ *//")"
rm -f /tmp/wfx-setup-skills.$$

# ---- 7. login -------------------------------------------------------------
# Loopback installs have no auth at all; this one binds :8090 in a container,
# so it requires a login. An existing context for this host is reused.
unset WFX_API

# device_login <contexts-file> — runs `wfx login` against that contexts file
# and approves its code with the bearer in $BEARER. The bearer goes to curl on
# stdin (-H @-), so it is not in any argv; nothing here prints it.
device_login() {
  local ctx="$1" out lpid code approved rc
  out=$(mktemp)
  WFX_CONTEXTS="$ctx" wfx login --url "$URL" >"$out" 2>&1 &
  lpid=$!
  code=""
  for _ in $(seq 1 30); do
    code=$(sed -n 's/^ *\([A-Z0-9]\{4\}-[A-Z0-9]\{4\}\) *$/\1/p' "$out" | head -1)
    [ -n "$code" ] && break
    sleep 1
  done
  if [ -z "$code" ]; then kill $lpid 2>/dev/null; cat "$out"; rm -f "$out"; return 1; fi
  approved=$(printf 'Authorization: Bearer %s\n' "$BEARER" | curl -fsS -X POST -H @- \
    -H 'Content-Type: application/json' -d "{\"user_code\":\"$code\",\"action\":\"approve\"}" \
    "$URL/api/device/verify" 2>&1)
  wait $lpid; rc=$?
  rm -f "$out"
  [ $rc -eq 0 ] || { echo "$approved"; return 1; }
}
# ctx_token <contexts-file> — the token for this host, to stdout (captured, never shown)
ctx_token() {
  python3 -c 'import json,sys; c=json.load(open(sys.argv[1])); print(c["contexts"][sys.argv[2]]["token"])' "$1" "localhost:$PORT" 2>/dev/null
}
CTXFILE="${WFX_CONTEXTS:-$HOME/.config/wfx/contexts.json}"

if wfx context list 2>/dev/null | grep -q "localhost:$PORT"; then
  wfx context use "localhost:$PORT" >/dev/null 2>&1 || true
fi
if wfx context list 2>/dev/null | grep -q "localhost:$PORT" && wfx env >/dev/null 2>&1; then
  ok "logged in to $URL (existing context)"
else
  BEARER=$(docker compose -p "$PROJECT" logs wfx-server 2>/dev/null | sed -n 's/.*admin token: *\(wfx_[A-Za-z0-9_-]*\).*/\1/p' | tail -1)
  [ -n "$BEARER" ] || blocked login "no bootstrap admin token in the server log (it is printed once, on the first boot of an empty database) -> run: wfx login --url $URL and approve it in the browser as an existing admin"
  msg=$(device_login "$CTXFILE") || { BEARER=""; blocked login "device approval failed: $msg"; }
  BEARER=""
  ok "logged in to $URL as admin (token kept in the 0600 contexts file $CTXFILE)"
fi

# ---- 7b. a credential for steps -------------------------------------------
# A `wfx` command inside a step (wfx judge, wfx state) talks to this server
# through WFX_API, which the server sets. On an authenticated server it also
# needs a token, and steps inherit none: it is GIVEN one by name, WFX_API_TOKEN,
# from the env store. It is a separate login from yours, so logging yourself
# out does not break running workflows.
if wfx env 2>/dev/null | grep -q '^ *WFX_API_TOKEN'; then
  ok "env: WFX_API_TOKEN already stored (steps can call wfx)"
else
  BEARER=$(ctx_token "$CTXFILE")
  stepctx=$(mktemp -d)/contexts.json
  if [ -n "$BEARER" ] && device_login "$stepctx" >/dev/null; then
    ctx_token "$stepctx" | wfx env set WFX_API_TOKEN >/dev/null 2>&1 \
      && ok "env: WFX_API_TOKEN stored (a separate login, for wfx inside steps)" \
      || note "could not store WFX_API_TOKEN; wfx judge inside a step will be refused"
  else
    note "could not mint a step credential; wfx judge inside a step will be refused"
  fi
  BEARER=""
  rm -rf "$(dirname "$stepctx")"
fi

# ---- 8. keys --------------------------------------------------------------
# From the caller's environment, piped, never echoed. Absent keys are named.
missing=""
for k in TYPESAFE_API_KEY OPENROUTER_API_KEY; do
  if [ -n "${!k:-}" ]; then
    printf '%s\n' "${!k}" | wfx env set "$k" >/dev/null 2>&1 && ok "env: $k stored (secret)" || blocked env "wfx env set $k failed"
  elif wfx env 2>/dev/null | grep -q "^ *$k"; then
    ok "env: $k already stored"
  else
    missing="$missing $k"
  fi
done
[ -n "$missing" ] && note "not set:$missing  (TYPESAFE_API_KEY is what jev-direct needs; set later with: wfx env set TYPESAFE_API_KEY)"

# ---- 9. a free model ------------------------------------------------------
models=$(wfx models opencode-acp --free 2>&1) || blocked models "wfx models opencode-acp --free failed: $models"
printf '%s\n' "$models" | sed 's/^/    /'
if printf '%s\n' "$models" | grep -q 'NOT offered'; then
  note "opencode does not list the configured model for this (anonymous) account; it answered anyway in rehearsal. If a step refuses to start, give it  model: <a free id above>"
fi
ok "free models listed"

# ---- 10. doctor -----------------------------------------------------------
doc=$(wfx doctor 2>&1) || blocked doctor "wfx doctor failed: $doc"
# The full report is long (every provider in the registry, most of them
# optional and unconfigured); what decides READY is the defaults.
printf '%s\n' "$doc" | grep -E '^(default|storage|shell|skills)|opencode-acp|jev-direct|default provider' | sed 's/^/    /'
if printf '%s\n' "$doc" | grep -q 'default provider .* not'; then
  blocked doctor "$(printf '%s\n' "$doc" | grep 'default provider' | head -1 | sed 's/^ *//')"
fi
ok "doctor: default provider ready ($(printf '%s\n' "$doc" | grep -c '✗') optional entries not configured; run wfx doctor for all)"

# The server requires login (requests reach it over the docker bridge, not
# loopback), so a bare URL opens the sign-in card. `wfx ui` opens it signed in
# with a one-time, 60-second link; no token is ever in the URL.
note "open the UI signed in:  wfx ui --url $URL"
if [ -n "$missing" ] && printf '%s\n' "$missing" | grep -q TYPESAFE_API_KEY; then
  printf 'READY %s  (agent steps ready on free models; decide:/judge need TYPESAFE_API_KEY)\n' "$URL"
else
  printf 'READY %s\n' "$URL"
fi
