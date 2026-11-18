#!/usr/bin/env bash
# Tests for release-local.sh's guards (#184). Run: bash scripts/release-local_test.sh
set -u
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
fail=0
check() { # name, expected exit (0|1), command...
  local name=$1 want=$2; shift 2
  ( RELEASE_LIB_ONLY=1; . "$ROOT/scripts/release-local.sh"; "$@" ) >/dev/null 2>&1
  local got=$?; [ "$got" -ne 0 ] && got=1
  if [ "$got" = "$want" ]; then echo "ok    $name"; else echo "FAIL  $name (exit $got, want $want)"; fail=1; fi
}
check "exact tag passes"        0 assert_version "wfx v0.2.0 (abc1234, 2026-11-18T00:00:00Z, go1.27)" v0.2.0
check "dirty is refused"        1 assert_version "wfx v0.2.0-dirty (abc1234-dirty, 2026-11-18T00:00:00Z, go1.27)" v0.2.0
check "described is refused"    1 assert_version "wfx v0.2.0-3-gabc1234 (abc1234, 2026-11-18T00:00:00Z, go1.27)" v0.2.0
check "other tag is refused"    1 assert_version "wfx v0.2.1 (abc1234, 2026-11-18T00:00:00Z, go1.27)" v0.2.0
check "prefix tag is refused"   1 assert_version "wfx v0.2.0 (abc1234, 2026-11-18T00:00:00Z, go1.27)" v0.2
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
git -C "$T" init -q && git -C "$T" -c user.email=t@example.com -c user.name=t commit -q --allow-empty -m init
echo a > "$T/f" && git -C "$T" add f && git -C "$T" -c user.email=t@example.com -c user.name=t commit -q -m f
check "clean tree passes"       0 bash -c "cd '$T' && RELEASE_LIB_ONLY=1 . '$ROOT/scripts/release-local.sh' && assert_clean test"
echo b > "$T/f"
check "modified file is refused" 1 bash -c "cd '$T' && RELEASE_LIB_ONLY=1 . '$ROOT/scripts/release-local.sh' && assert_clean test"
exit $fail
