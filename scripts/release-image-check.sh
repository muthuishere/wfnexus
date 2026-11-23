#!/usr/bin/env bash
# Build the server image for this machine's architecture, load it locally and
# refuse it unless `wfx-server --version` reports EXACTLY the tag. Nothing is
# pushed. release-local.sh runs this before it publishes anything.
#
#   bash scripts/release-image-check.sh v0.4.0 [docker build args...]
#
# Tested with a stubbed docker: scripts/release-image-check_test.sh.
set -euo pipefail

TAG="${1:?usage: release-image-check.sh TAG [build args...]}"
shift

CHECK="wfx-server-release-check:$TAG"
docker buildx build --load -f infra/Dockerfile "$@" -t "$CHECK" .
GOT="$(docker run --rm --entrypoint wfx-server "$CHECK" --version | head -n1)"
docker image rm "$CHECK" >/dev/null 2>&1 || true
echo "tag=$TAG image=$GOT"
# "wfx-server v0.4.0 (abc1234, …": a -dirty, described or other tag is refused.
case "$GOT" in
  *" $TAG ("*) ;;
  *) echo "release: server image reports '$GOT', not exactly $TAG" >&2; exit 1 ;;
esac
