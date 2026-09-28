#!/usr/bin/env bash
# Every site gate, in the order CI runs them. Adapted from toolnexus's site/tests/run-all.sh:
# the point is the same — the docs cannot drift from the code without this failing.
#
#   bash site/tests/run-all.sh
#
#   1. verify-symbols   every command, env var, package, template, skill, commit and asset the
#                       pages name exists on this commit
#   2. coverage-gate    every template, skill, CLI line and provider has its page; no orphans
#   3. wfx help         when Go is available: the binary's real `wfx help` equals the usage text
#                       the CLI reference was generated from
#   4. build            astro build, then the pages that must exist are in dist/
set -uo pipefail

SITE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO="$(cd "$SITE/.." && pwd)"
FAILED=()

step() { echo ""; echo "==> $1"; }

step "verify-symbols"
node "$SITE/scripts/verify-symbols.mjs" --strict || FAILED+=("verify-symbols")

step "coverage-gate"
node "$SITE/scripts/coverage-gate.mjs" --strict || FAILED+=("coverage-gate")

step "wfx help == generated CLI reference"
if command -v go >/dev/null 2>&1; then
	got="$(cd "$REPO/apps/api" && GOWORK=off go run ./cmd/wfx help 2>&1)"
	want="$(node --input-type=module -e "import('$SITE/scripts/generate-pages.mjs').then(m => process.stdout.write(m.wfxUsage()))")"
	if [ "$got" == "$want" ]; then echo "identical ($(printf '%s\n' "$got" | wc -l | tr -d ' ') lines)"; else
		echo "DIFFERENT — the reference would not match the binary"; diff <(printf '%s\n' "$want") <(printf '%s\n' "$got") | head -20
		FAILED+=("wfx-help")
	fi
else
	echo "skipped: go not installed"
fi

step "astro build"
(cd "$SITE" && npm run build --silent) || FAILED+=("build")
for p in index.html quickstart/index.html demo/index.html agent-start/index.html architecture/index.html \
	reference/cli/index.html templates/index.html skills/catalogue/index.html providers/registry/index.html \
	demo.mp4 poster.jpg run-flow.svg run-flow.mp4 run-flow.gif run-flow.html llms.txt; do
	[ -f "$SITE/dist/$p" ] || { echo "missing from dist: $p"; FAILED+=("dist:$p"); }
done

echo ""
if [ ${#FAILED[@]} -gt 0 ]; then echo "SITE GATES FAILED: ${FAILED[*]}"; exit 1; fi
echo "SITE GATES PASSED — every name checked, every page present, build green."
