#!/bin/sh
# tm.sh <verb> — run tmvp against the product skill named by in.skill, with
# every argument read from in.* files (written by the step, never
# interpolated into a command line). Credentials stay with tmvp: the step
# only ever sees secret NAMES.
set -eu
rd() { v=$(cat "in.$1" 2>/dev/null || true); printf '%s' "${v%
}"; }
skill=$(rd skill)
[ -n "$skill" ] && [ -e "$skill" ] || { echo "skill is not a path: $skill"; exit 2; }
case "$1" in
check) exec tmvp --skill "$skill" check ;;
read)
  envs=$(rd envs); sys=$(rd system); verb=$(rd verb); arg=$(rd arg); diff=$(rd diff)
  set -- --skill "$skill" "${envs:-all}" "$sys" "$verb"
  [ -n "$arg" ] && set -- "$@" "$arg"
  [ "$diff" = true ] && set -- "$@" --diff
  exec tmvp "$@" ;;
write)
  env=$(rd env); sys=$(rd system); stmt=$(rd statement); reason=$(rd reason); force=$(rd force); preview=$(rd preview)
  [ -n "$reason" ] || { echo "reason is required"; exit 2; }
  set -- --skill "$skill" write "$env" "$sys" "$stmt" --reason "$reason"
  [ "$preview" = true ] && set -- "$@" --preview
  [ "$force" = true ] && set -- "$@" --force
  exec tmvp "$@" ;;
*) echo "unknown verb $1"; exit 2 ;;
esac
