#!/usr/bin/env bash
# Tests for release-image-check.sh with a stubbed docker on PATH: the stub
# accepts the build and answers `docker run` with a chosen version line.
# Run: bash scripts/release-image-check_test.sh
set -u
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
cat > "$T/docker" <<'STUB'
#!/usr/bin/env bash
[ "$1" = run ] && echo "$FAKE_VERSION"
exit 0
STUB
chmod +x "$T/docker"

fail=0
check() { # name, expected exit (0|1), reported version, tag
  local name=$1 want=$2
  FAKE_VERSION="$3" PATH="$T:$PATH" bash "$ROOT/scripts/release-image-check.sh" "$4" >/dev/null 2>&1
  local got=$?; [ "$got" -ne 0 ] && got=1
  if [ "$got" = "$want" ]; then echo "ok    $name"; else echo "FAIL  $name (exit $got, want $want)"; fail=1; fi
}
check "exact tag passes"     0 "wfx-server v0.4.0 (abc1234, 2026-11-24T00:00:00Z, go1.26.11)" v0.4.0
check "other tag is refused" 1 "wfx-server v0.4.1 (abc1234, 2026-11-24T00:00:00Z, go1.26.11)" v0.4.0
check "dirty is refused"     1 "wfx-server v0.4.0-dirty (abc1234-dirty, 2026-11-24T00:00:00Z, go1.26.11)" v0.4.0
check "unstamped is refused" 1 "wfx-server dev" v0.4.0
check "prefix is refused"    1 "wfx-server v0.4.0 (abc1234, 2026-11-24T00:00:00Z, go1.26.11)" v0.4
exit $fail
