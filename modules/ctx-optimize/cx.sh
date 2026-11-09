#!/bin/sh
# cx.sh <verb> — run one ctx-optimize verb in the repo named by in.path, with
# the arguments read from in.a / in.b (written by the step, never interpolated
# into a command line).
set -eu
verb="$1"
repo=$(cat in.path 2>/dev/null | tr -d '\n')
[ -n "$repo" ] && [ -d "$repo" ] || { echo "path is not a directory: $repo"; exit 2; }
a=$(cat in.a 2>/dev/null || true); a=${a%
}
b=$(cat in.b 2>/dev/null || true); b=${b%
}
cd "$repo"
case "$verb" in
up) exec ctx-optimize up ;;
query) exec ctx-optimize query "$a" ;;
card|change-plan|affected) exec ctx-optimize "$verb" "$a" ;;
path) exec ctx-optimize path "$a" "$b" ;;
*) echo "unknown verb $verb"; exit 2 ;;
esac
