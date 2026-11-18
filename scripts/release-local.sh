#!/usr/bin/env bash
# Publish a release from this machine. There are no hosted runners: this is
# the whole release, in the order the old tag workflow ran it.
#
#   task release:publish -- v0.4.0
#
# Nothing is published until every step before it has passed:
#   1. preflight  on main, clean, not shallow, equal to origin/main, tag unused,
#                 tools present
#   2. gate       task ci (a release does not skip the gate)
#   3. build      tag locally, task release (every binary, the tarballs,
#                 checksums.txt)
#   4. assert     the host binary reports the tag; every artifact is in
#                 checksums.txt
#   5. publish    push the tag, create the GitHub release, push the server and
#                 worker images and the Helm chart to ghcr.io
#
# If anything fails before the tag is pushed, the local tag is deleted again.
# No token is ever printed: gh, docker and helm read it from a pipe.
#
# SKIP_IMAGES=1 publishes the binaries only (no images, no chart).
set -euo pipefail

step() { echo; echo "==> $1"; }
die() { echo "release: $1" >&2; exit 1; }

# assert_clean: no tracked file differs from HEAD. Checked before anything,
# after the gate and after the build: a step that modifies a tracked file
# would otherwise ship binaries built from uncommitted changes.
assert_clean() {
  local changed
  changed="$(git status --porcelain --untracked-files=no)"
  [ -z "$changed" ] || die "$1: tracked files changed: $(echo "$changed" | tr '\n' ' ')"
}

# assert_version: the binary reports EXACTLY the tag ("wfx v0.2.0 (…"). A
# -dirty or described (v0.2.0-3-gabc) version does not match.
assert_version() {
  case "$1" in *" $2 ("*) ;; *) die "built binary reports '$1', not exactly $2" ;; esac
}

# A test sources this file for the functions above only.
[ "${RELEASE_LIB_ONLY:-}" = 1 ] && return 0

TAG="${1:-}"
[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || {
  echo "usage: task release:publish -- vMAJOR.MINOR.PATCH" >&2; exit 2; }

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

# ---- 1. preflight ----------------------------------------------------------
step "preflight"
for t in git gh task go node npm shasum; do command -v "$t" >/dev/null || die "$t is not installed"; done
if [ "${SKIP_IMAGES:-}" != 1 ]; then
  command -v helm >/dev/null || die "helm is not installed (or SKIP_IMAGES=1)"
  docker buildx version >/dev/null 2>&1 || die "docker buildx is not available (or SKIP_IMAGES=1)"
  gh auth status 2>&1 | grep -q "write:packages" \
    || die "the gh token cannot push packages: run 'gh auth refresh -s write:packages' (or SKIP_IMAGES=1)"
fi
[ "$(git rev-parse --is-shallow-repository)" = false ] || die "shallow clone: git describe would lie"
[ "$(git rev-parse --abbrev-ref HEAD)" = main ] || die "not on main"
assert_clean "preflight"
git fetch --quiet --tags origin main
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || die "HEAD is not origin/main"
git rev-parse -q --verify "refs/tags/$TAG" >/dev/null && die "tag $TAG already exists"
git ls-remote --exit-code --tags origin "refs/tags/$TAG" >/dev/null 2>&1 && die "tag $TAG already exists on origin"

OWNER="$(gh repo view --json owner -q .owner.login)"
REPO="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
echo "releasing $TAG of $REPO from $(git rev-parse --short HEAD)"

# ---- 2. gate ---------------------------------------------------------------
step "gate: task ci"
(cd apps/ui && npm ci --silent)
GOWORK=off task ci
assert_clean "after the gate"

# ---- 3. build --------------------------------------------------------------
step "build"
git tag -a "$TAG" -m "$TAG"
PUSHED=0
cleanup() { [ "$PUSHED" = 1 ] || git tag -d "$TAG" >/dev/null 2>&1 || true; }
trap cleanup EXIT
GOWORK=off task release
assert_clean "after the build"

# ---- 4. assert -------------------------------------------------------------
step "assert: the built binary reports the tag"
BIN="bin/release/wfx-$(go env GOOS)-$(go env GOARCH)"
[ -x "$BIN" ] || die "no $BIN to ask"
GOT="$("$BIN" version | head -n1)"
echo "tag=$TAG built=$GOT"
assert_version "$GOT" "$TAG"

step "assert: every artifact is covered by checksums.txt"
(
  cd bin/release
  missing=0
  for f in *; do
    [ "$f" = checksums.txt ] && continue
    grep -q "  $f\$" checksums.txt || { echo "not in checksums.txt: $f" >&2; missing=1; }
  done
  [ "$missing" -eq 0 ]
) || die "an artifact is not covered by checksums.txt"

# ---- 5. publish ------------------------------------------------------------
step "publish: tag and GitHub release"
git push origin "refs/tags/$TAG"
PUSHED=1
NOTES="$(mktemp)"
cat > "$NOTES" <<'EOF'
## Verify what you downloaded

```sh
shasum -a 256 -c checksums.txt --ignore-missing
```

## macOS

These binaries are **unsigned and not notarised**, so macOS quarantines
anything downloaded. Clear the attribute once, per binary:

```sh
xattr -d com.apple.quarantine ./wfx
```

## Air-gapped install (no registry, no toolchain)

Take `wfx-server-linux-amd64.tar.gz` **or** `wfx-server-linux-arm64.tar.gz`
— one per machine architecture — plus `checksums.txt`. Verify, carry it
in, unpack, `docker compose up -d`. Nothing in it pulls an application
image from a registry.
EOF
gh release create "$TAG" --title "$TAG" --generate-notes --notes-file "$NOTES" bin/release/*
rm -f "$NOTES"

if [ "${SKIP_IMAGES:-}" = 1 ]; then
  echo; echo "released $TAG (binaries only: SKIP_IMAGES=1)"; exit 0
fi

step "publish: images"
V="${TAG#v}"; MINOR="${V%.*}"
GH_USER="$(gh api user -q .login)"
gh auth token | docker login ghcr.io -u "$GH_USER" --password-stdin >/dev/null
# Built from the REPOSITORY's Dockerfiles. infra/package/ (the tarball's own
# definitions) is untouched: it must keep working with no registry at all.
for img in "wfx-server:infra/Dockerfile" "wfx-runner:infra/Dockerfile.runner"; do
  name="${img%%:*}"; file="${img#*:}"
  docker buildx build --platform linux/amd64,linux/arm64 -f "$file" \
    -t "ghcr.io/$OWNER/$name:$V" -t "ghcr.io/$OWNER/$name:$MINOR" -t "ghcr.io/$OWNER/$name:latest" \
    --push .
done

step "publish: Helm chart"
CHART="$(mktemp -d)"
helm lint infra/helm/wfnexus
helm package infra/helm/wfnexus --version "$V" --app-version "$V" -d "$CHART"
gh auth token | helm registry login ghcr.io -u "$GH_USER" --password-stdin >/dev/null
helm push "$CHART/wfnexus-$V.tgz" "oci://ghcr.io/$OWNER/charts"
rm -rf "$CHART"

echo; echo "released $TAG: binaries, images and chart"
