#!/usr/bin/env bash
# Take a workflow that works locally to the team server, git-natively.
#
#   deploy.sh <workflow.yaml> --project <p> --version <v1.2.0>
#             [--approve --as "agent:<you> (for <person>)"] [--to <git remote>] [--url <host>]
#
# Stages: validate -> propose (branch + commit on the project's repo) -> approve
# (ONLY with --approve) -> publish the version -> verify it is installed and
# scheduled on the server -> dry run there. Ends with DEPLOYED or BLOCKED.
# Without --approve it stops at the proposal and prints the approve command:
# approving is a person's decision, and this script never makes it silently.
set -uo pipefail

FILE=""; PROJECT=""; VERSION=""; APPROVE=""; AS=""; TO=""; URLFLAG=()
while [ $# -gt 0 ]; do
  case "$1" in
    --project) PROJECT="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --approve) APPROVE=1; shift ;;
    --as) AS="$2"; shift 2 ;;
    --to) TO="$2"; shift 2 ;;
    --url) URLFLAG=(--url "$2"); shift 2 ;;
    -h|--help) sed -n 2,12p "$0"; exit 0 ;;
    -*) echo "unknown flag $1" >&2; exit 2 ;;
    *) FILE="$1"; shift ;;
  esac
done
ok()      { printf 'ok  %s\n' "$*"; }
blocked() { printf 'BLOCKED %s: %s\n' "$1" "$2"; printf 'BLOCKED: %s\n' "$1"; exit 1; }

[ -f "$FILE" ] || blocked args "give the workflow file: deploy.sh <workflow.yaml> --project <p> --version <v>"
[ -n "$PROJECT" ] || blocked args "--project is required: the git-backed project on the server that owns this workflow (wfx projects)"
[ -n "$VERSION" ] || blocked args "--version is required: a published version is immutable, so it has to be named"
[ -z "$APPROVE" ] || [ -n "$AS" ] || blocked args "--approve needs --as naming YOU, e.g. --as \"agent:claude (for muthu)\"; never the person's own name"
command -v wfx >/dev/null 2>&1 || blocked cli "wfx is not on PATH"
NAME=$(sed -n 's/^name: *//p' "$FILE" | head -1 | tr -d '"'"'")
[ -n "$NAME" ] || blocked args "$FILE has no top-level name:"

# ---- 1. validate against the server ---------------------------------------
out=$(wfx validate "$FILE" 2>&1) || blocked validate "$out"
ok "valid: $NAME"
if grep -q '^ *- *cron:' "$FILE"; then
  ok "schedule in the file: $(grep -m1 'cron:' "$FILE" | sed 's/.*cron: *//')"
else
  printf '    note: no on.schedule in %s; it will only run when dispatched\n' "$FILE"
fi

# ---- 2. the project must be git-backed -------------------------------------
wfx projects 2>/dev/null | grep -qE "^ *$PROJECT( |$)" || blocked project "no project $PROJECT on the server -> wfx project new $PROJECT (or wfx project add <repo url> --as $PROJECT)"

# ---- 3. propose ------------------------------------------------------------
# A pending proposal for this workflow is reused rather than duplicated, so
# "stop for approval, then re-run with --approve" approves what was reviewed.
PENDING=$(wfx workflow proposals --project "$PROJECT" --workflow "$NAME" 2>/dev/null \
  | awk -v w="$NAME" '$3==w && $5=="pending" {print $1}' | head -1)
if [ -n "$PENDING" ]; then
  out="proposed (already pending) $NAME in $PROJECT — proposal $PENDING on its branch (pending)"
else
  out=$(wfx apply "$FILE" --project "$PROJECT" 2>&1) || blocked propose "$out"
fi
printf '%s\n' "$out" | sed 's/^/    /'
if printf '%s\n' "$out" | grep -q '^installed '; then
  blocked propose "project $PROJECT is not git-backed with a commit, so the save was written directly instead of proposed -> give the repo a first commit, then re-run"
fi
PID=$(printf '%s\n' "$out" | sed -n 's/.* proposal \([0-9a-f-]\{36\}\) .*/\1/p' | head -1)
if printf '%s\n' "$out" | grep -q 'is unchanged'; then
  ok "no change to propose: the server already has this version of $NAME"
else
  [ -n "$PID" ] || blocked propose "no proposal id in the answer (above)"
  ok "proposed: $PID"
  # ---- 4. approve (only when told to) ---------------------------------------
  if [ -z "$APPROVE" ]; then
    printf 'WAITING: approve with\n    wfx workflow approve %s --as "<you>"\nthen re-run this with --approve --as "<you>" (the proposal is reused)\n' "$PID"
    exit 3
  fi
  out=$(wfx workflow approve "$PID" --as "$AS" 2>&1) || blocked approve "$out"
  ok "$out"
fi

# ---- 5. publish the version -------------------------------------------------
if [ -n "$TO" ]; then
  out=$(wfx publish "$FILE" --version "$VERSION" --to "$TO" 2>&1) || blocked publish "$(printf '%s\n' "$out" | grep -v '\[toolnexus\]')"
else
  out=$(wfx publish "$FILE" --version "$VERSION" --project "$PROJECT" "${URLFLAG[@]}" 2>&1) || blocked publish "$(printf '%s\n' "$out" | grep -v '\[toolnexus\]')"
fi
printf '%s\n' "$out" | grep -v '\[toolnexus\]' | sed 's/^/    /'
ok "published $NAME@${VERSION#v}"

# ---- 6. installed and scheduled, on the server -----------------------------
show=$(wfx workflows show "$NAME" 2>&1) || blocked verify "the server does not have $NAME: $show"
printf '%s\n' "$show" | sed -n '1,3p' | sed 's/^/    /'
printf '%s\n' "$show" | sed -n '2p' | grep -q "/$PROJECT/" || printf '    note: loaded from %s, not from project %s\n' "$(printf '%s\n' "$show" | sed -n 2p)" "$PROJECT"
if grep -q '^ *- *cron:' "$FILE"; then
  printf '%s\n' "$show" | grep -q '^schedule ' || blocked verify "the file has a schedule but the server's copy of $NAME has none"
  ok "scheduled: $(printf '%s\n' "$show" | grep -m1 '^schedule ' | sed 's/^schedule *//')"
fi

# ---- 7. dry run on the server ---------------------------------------------
dry=$(wfx dryrun "$NAME" 2>&1)
printf '%s\n' "$dry" | sed 's/^/    /'
printf '%s\n' "$dry" | grep -q 'would run' || blocked dryrun "the server says it would not run (above)"
ok "dry run: would run"
printf 'DEPLOYED %s@%s to project %s\n' "$NAME" "${VERSION#v}" "$PROJECT"
