#!/usr/bin/env bash
# Publish the built docs site (site/dist) to the gh-pages branch. There are no
# hosted runners: `task site:deploy` runs the site gates and the build, then
# this script. GitHub Pages serves the branch (Settings > Pages > Deploy from a
# branch: gh-pages, / (root)).
#
# Only main is published, and only when it equals origin/main: the pages cite
# commits, and verify-symbols checked them against this one.
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"
die() { echo "site-deploy: $1" >&2; exit 1; }

[ -f site/dist/index.html ] || die "no site/dist: run task site:deploy, not this script"
[ "$(git rev-parse --abbrev-ref HEAD)" = main ] || die "not on main"
[ -z "$(git status --porcelain --untracked-files=no)" ] || die "the working tree has changes"
git fetch --quiet origin main
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || die "HEAD is not origin/main"

SHA="$(git rev-parse --short HEAD)"
WT="$(mktemp -d)"
cleanup() { git worktree remove --force "$WT" >/dev/null 2>&1 || true; rm -rf "$WT"; }
trap cleanup EXIT

# One commit, no history: the branch is a build output, not a record.
git worktree add --quiet --detach "$WT"
(
  cd "$WT"
  git checkout --quiet --orphan gh-pages-publish
  git rm -rf --quiet . >/dev/null
  cp -R "$ROOT/site/dist/." .
  touch .nojekyll
  git add -A
  git commit --quiet -m "site: build of $SHA"
  git push --force --quiet origin HEAD:refs/heads/gh-pages
)
git branch -D gh-pages-publish >/dev/null 2>&1 || true
echo "published the site from $SHA to gh-pages"
