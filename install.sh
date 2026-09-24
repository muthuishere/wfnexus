#!/bin/sh
# Install the wfx client. POSIX sh, no bashisms — it has to run under dash,
# busybox ash and macOS's old bash alike.
#
#   curl -fsSL https://raw.githubusercontent.com/muthuishere/wfnexus/main/install.sh | sh
#   WFX_VERSION=v0.1.0 sh install.sh        # pin a version
#   WFX_INSTALL_DIR=~/bin  sh install.sh    # somewhere else on your PATH
#
# It needs no Go, no Node, no container runtime and no clone of the repository,
# and it writes nothing outside your own directories — no sudo, ever.
#
# THE ONE RULE: it verifies the download against the release's checksums.txt
# before anything lands on your PATH, and it REFUSES to install on a mismatch
# or when the checksum cannot be obtained. Unverified is not a warning here.
set -eu

REPO="${WFX_REPO:-muthuishere/wfnexus}"
BASE="${WFX_BASE_URL:-https://github.com/$REPO/releases}"
INSTALL_DIR="${WFX_INSTALL_DIR:-$HOME/.local/bin}"   # same dir `task install` uses
BIN=wfx

die() { echo "wfx install: $*" >&2; exit 1; }
say() { echo "$*"; }

# ---- what machine is this -------------------------------------------------
os=$(uname -s)
arch=$(uname -m)
case "$os" in
  Linux)  GOOS=linux ;;
  Darwin) GOOS=darwin ;;
  *) die "unsupported operating system '$os'.
  Published platforms: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64.
  Windows is served by install.ps1." ;;
esac
case "$arch" in
  x86_64|amd64)  GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) die "unsupported architecture '$arch' (detected $os/$arch).
  Published platforms: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64." ;;
esac

# ---- a downloader and a digest tool ---------------------------------------
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" "$1"; }
else
  die "neither curl nor wget is available"
fi

if command -v shasum >/dev/null 2>&1; then
  digest() { shasum -a 256 "$1" | cut -d' ' -f1; }
elif command -v sha256sum >/dev/null 2>&1; then
  digest() { sha256sum "$1" | cut -d' ' -f1; }
else
  die "no sha256 tool (shasum or sha256sum) — refusing to install unverified"
fi

# ---- which release --------------------------------------------------------
if [ -n "${WFX_VERSION:-}" ]; then
  VERSION="$WFX_VERSION"
  URL="$BASE/download/$VERSION"
else
  VERSION="latest"
  URL="$BASE/latest/download"
fi

ASSET="$BIN-$GOOS-$GOARCH"
TMP=$(mktemp -d "${TMPDIR:-/tmp}/wfx-install.XXXXXX") || die "cannot create a temp dir"
# A partial download must never survive as something on your PATH.
trap 'rm -rf "$TMP"' EXIT INT TERM

say "wfx: $GOOS/$GOARCH, $VERSION"

# The checksum file FIRST: if it cannot be had, there is nothing to verify
# against and we stop before we have even downloaded a binary.
fetch "$URL/checksums.txt" "$TMP/checksums.txt" \
  || die "could not download $URL/checksums.txt — refusing to install unverified"
[ -s "$TMP/checksums.txt" ] || die "checksums.txt is empty — refusing to install unverified"

want=$(awk -v f="$ASSET" '$2 == f || $2 == "*"f {print $1; exit}' "$TMP/checksums.txt")
[ -n "$want" ] || die "no checksum published for '$ASSET' — refusing to install unverified"

fetch "$URL/$ASSET" "$TMP/$ASSET" || die "download failed: $URL/$ASSET"
[ -s "$TMP/$ASSET" ] || die "downloaded file is empty — a partial download, nothing installed"

got=$(digest "$TMP/$ASSET")
if [ "$got" != "$want" ]; then
  rm -f "$TMP/$ASSET"
  die "CHECKSUM MISMATCH for $ASSET
  published: $want
  download:  $got
  Nothing was installed and the download has been deleted."
fi
say "verified sha256 $got"

# ---- install --------------------------------------------------------------
mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR"
chmod +x "$TMP/$ASSET"
# Land it via a temp name in the SAME directory, then rename: a rename is
# atomic, so an interrupted install can never leave a half-written wfx behind.
mv "$TMP/$ASSET" "$INSTALL_DIR/.$BIN.incoming.$$" || die "cannot write to $INSTALL_DIR"
mv "$INSTALL_DIR/.$BIN.incoming.$$" "$INSTALL_DIR/$BIN" || die "cannot install into $INSTALL_DIR"

say "installed $INSTALL_DIR/$BIN"

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) say ""
     say "$INSTALL_DIR is not on your PATH. Add it:"
     say "  echo 'export PATH=\"\$PATH:$INSTALL_DIR\"' >> ~/.profile && . ~/.profile" ;;
esac

if [ "$GOOS" = darwin ]; then
  say ""
  say "macOS: this binary is downloaded, unsigned and un-notarised, so Gatekeeper"
  say "quarantines it. Clear that once:"
  say "  xattr -d com.apple.quarantine $INSTALL_DIR/$BIN"
fi

say ""
say "try: $BIN version"
