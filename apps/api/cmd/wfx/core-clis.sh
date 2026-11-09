#!/bin/sh
# core-clis.sh — install the CLIs the core modules wrap (apl, gh, aos,
# ctx-optimize) into $CORE_BIN (default ~/.local/bin). No sudo. Idempotent: a
# CLI already on PATH (or in $CORE_BIN) is skipped. Language toolchains are
# bring-your-own; these four are not.
#
#   sh core-clis.sh                  all four
#   sh core-clis.sh gh ctx-optimize  just these
#
# Sources:
#   gh            github.com/cli/cli releases            gh_<v>_<os>_<arch>.tar.gz
#   aos           github.com/muthuishere/aos install.sh  (its own release picker)
#   ctx-optimize  github.com/muthuishere/ctx-optimize     ctx-optimize_<v>_<os>_<arch>.tar.gz
#   apl           github.com/deemwar-products/apl (PRIVATE) apl_<os>_<arch>, via an authenticated gh
#
# Used by: `wfx server deploy` (every deploy), infra/Dockerfile*, and each
# module's `requires.cli[].install`.
set -u
BIN="${CORE_BIN:-$HOME/.local/bin}"
mkdir -p "$BIN"
export PATH="$BIN:$PATH"
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT

latest() { # owner/repo -> tag without the leading v
  curl -fsSL "https://api.github.com/repos/$1/releases/latest" | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -1
}

one() {
  if command -v "$1" >/dev/null 2>&1; then echo "ok    $1 ($(command -v "$1"))"; return 0; fi
  case "$1" in
  gh)
    v=$(latest cli/cli) && [ -n "$v" ] || return 1
    ext=tar.gz; [ "$OS" = darwin ] && ext=zip
    curl -fsSL -o "$TMP/gh.$ext" "https://github.com/cli/cli/releases/download/v$v/gh_${v}_$( [ "$OS" = darwin ] && echo macOS || echo "$OS")_$ARCH.$ext" || return 1
    if [ "$ext" = zip ]; then (cd "$TMP" && unzip -q gh.zip); else tar -xzf "$TMP/gh.tar.gz" -C "$TMP"; fi
    cp "$TMP"/gh_*/bin/gh "$BIN/gh" ;;
  aos)
    curl -fsSL https://raw.githubusercontent.com/muthuishere/aos/main/install.sh | AOS_INSTALL_DIR="$BIN" sh ;;
  ctx-optimize)
    v=$(latest muthuishere/ctx-optimize) && [ -n "$v" ] || return 1
    curl -fsSL "https://github.com/muthuishere/ctx-optimize/releases/download/v$v/ctx-optimize_${v}_${OS}_$ARCH.tar.gz" |
      tar -xz -C "$TMP" && cp "$TMP/ctx-optimize" "$BIN/ctx-optimize" ;;
  apl)
    command -v gh >/dev/null 2>&1 || { echo "skip  apl: needs gh (private release)"; return 1; }
    gh release download -R deemwar-products/apl -p "apl_${OS}_$ARCH" -O "$BIN/apl" --clobber || {
      echo "skip  apl: gh is not logged in to an account that can read deemwar-products/apl"; return 1; } ;;
  *) echo "unknown CLI: $1" >&2; return 2 ;;
  esac
  chmod 755 "$BIN/$1" 2>/dev/null
  echo "inst  $1 -> $BIN/$1"
}

[ $# -gt 0 ] || set -- gh apl
rc=0
for c in "$@"; do one "$c" || rc=1; done
exit $rc
