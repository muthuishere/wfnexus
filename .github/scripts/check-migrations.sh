#!/usr/bin/env bash
# Every migration exists in BOTH dialects, and every migration has both its up
# and its down. Enforced by the build because review is not an enforcement
# mechanism — openspec/config.yaml states the convention and nothing checked it.
#
# Pairing is by SLUG, not by number: the two dialects number independently
# (apps/api/migrations/000009_step_resolution ↔ migrations/sqlite/000003_step_resolution)
# because sqlite's 000001_init folds in several early Postgres migrations. The
# slugs it folds in are listed below; that list is closed. Anything new needs
# its own file in both dialects.
set -uo pipefail

PG=apps/api/migrations
LITE=apps/api/migrations/sqlite

# Slugs already inside sqlite's 000001_init baseline. Do not extend this list —
# a new migration gets a new file in both dialects.
FOLDED_INTO_SQLITE_INIT="init run_base_ref step_pending run_project workers worker_name_unique env_vars"

fail=0
note() { echo "FAIL: $*" >&2; fail=1; }
slug() { basename "$1" | sed -E 's/^[0-9]+_//; s/\.(up|down)\.sql$//'; }

folded() {
  # `local`: this used to reuse `s`, clobbering the caller's slug and making
  # every later check report the wrong expected filename.
  local candidate
  for candidate in $FOLDED_INTO_SQLITE_INIT; do [ "$candidate" = "$1" ] && return 0; done
  return 1
}
has_slug() { # dir, slug
  ls "$1"/*_"$2".up.sql >/dev/null 2>&1
}

# A primary-dialect migration needs a sqlite counterpart with the same slug.
for f in "$PG"/*.up.sql; do
  [ -e "$f" ] || continue
  s=$(slug "$f")
  folded "$s" && continue
  has_slug "$LITE" "$s" || note "$f has no sqlite counterpart — expected $LITE/<n>_$s.up.sql"
done

# And no sqlite-only migration either.
for f in "$LITE"/*.up.sql; do
  [ -e "$f" ] || continue
  s=$(slug "$f")
  [ "$s" = init ] && continue
  has_slug "$PG" "$s" || note "$f has no primary-dialect counterpart — expected $PG/<n>_$s.up.sql"
done

# Up and down, in both directories.
for dir in "$PG" "$LITE"; do
  for f in "$dir"/*.up.sql; do
    [ -e "$f" ] || continue
    down="${f%.up.sql}.down.sql"
    [ -f "$down" ] || note "$f has no down migration — expected $down"
  done
  for f in "$dir"/*.down.sql; do
    [ -e "$f" ] || continue
    up="${f%.down.sql}.up.sql"
    [ -f "$up" ] || note "$f has no up migration — expected $up"
  done
done

[ "$fail" -eq 0 ] || exit 1
echo "migrations: paired by slug across both dialects, up and down present"
