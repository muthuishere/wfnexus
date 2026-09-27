#!/usr/bin/env bash
# What changed in THIS repository from two weeks before the alert until now —
# the "recent deploys and merged PRs" every incident investigation starts with.
set -uo pipefail
cd "$WFX_WORKSPACE" 2>/dev/null || true
since=$(python3 -c "import datetime as d,os; t=d.datetime.fromisoformat(os.environ['ISSUE_CREATED'].replace('Z','+00:00')); print((t-d.timedelta(days=14)).isoformat())")
echo "commits since $since (newest first):"
git log --since="$since" --date=short --pretty='%h %ad %an: %s' -n 60 2>/dev/null || echo "(no git history here)"
echo
echo "files touched most in that window:"
git log --since="$since" --name-only --pretty=format: 2>/dev/null | grep -v '^$' | sort | uniq -c | sort -rn | head -25 || true
