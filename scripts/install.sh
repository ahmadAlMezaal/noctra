#!/bin/sh
# Nightshift installer — turnkey one-liner install of the latest release binary.
#
#   curl -fsSL https://raw.githubusercontent.com/ahmadAlMezaal/nightshift/main/scripts/install.sh | sh
#
# (This same script is intended to be served at https://getnightshift.dev/install.sh later.)
#
# It detects your OS/arch, downloads the matching GoReleaser archive + checksums
# from the latest GitHub release, verifies the SHA-256, and installs the
# `nightshift` binary to ~/.local/bin (override with NIGHTSHIFT_BIN). Pin a
# specific release with VERSION=v1.2.3.
set -e

REPO="ahmadAlMezaal/nightshift"
INSTALL_DIR="${NIGHTSHIFT_BIN:-$HOME/.local/bin}"

err() { echo "error: $*" >&2; exit 1; }

# --- temp dir + cleanup -----------------------------------------------------
TMP="$(mktemp -d 2>/dev/null || mktemp -d -t nightshift)"
trap 'rm -rf "$TMP"' EXIT INT TERM

# --- detect OS --------------------------------------------------------------
os="$(uname -s)"
case "$os" in
	Linux)  OS="linux" ;;
	Darwin) OS="darwin" ;;
	*)      err "unsupported OS: $os (Nightshift ships linux + darwin binaries; use Docker elsewhere)" ;;
esac

# --- detect arch ------------------------------------------------------------
arch="$(uname -m)"
case "$arch" in
	x86_64|amd64)   ARCH="amd64" ;;
	aarch64|arm64)  ARCH="arm64" ;;
	armv7l)         ARCH="armv7" ;;
	*)              err "unsupported architecture: $arch" ;;
esac

# --- resolve version --------------------------------------------------------
if [ -n "${VERSION:-}" ]; then
	TAG="$VERSION"
else
	echo "Resolving latest release …"
	TAG="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
		| grep '"tag_name"' \
		| head -n1 \
		| sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')"
	[ -n "$TAG" ] || err "could not resolve the latest release tag"
fi

# GoReleaser strips the leading "v" from {{ .Version }} in archive names.
VER_NO_V="$(printf '%s' "$TAG" | sed 's/^v//')"
ASSET="nightshift_${VER_NO_V}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/$REPO/releases/download/$TAG"

echo "Installing nightshift $TAG ($OS/$ARCH) …"

# --- download archive + checksums ------------------------------------------
curl -fsSL "$BASE/$ASSET" -o "$TMP/$ASSET" \
	|| err "download failed: $BASE/$ASSET"
curl -fsSL "$BASE/checksums.txt" -o "$TMP/checksums.txt" \
	|| err "download failed: $BASE/checksums.txt"

# --- verify sha256 ----------------------------------------------------------
want="$(awk -v a="$ASSET" '$2 == a { print $1 }' "$TMP/checksums.txt")"
[ -n "$want" ] || err "no checksum for $ASSET in checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
	got="$(sha256sum "$TMP/$ASSET" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
	got="$(shasum -a 256 "$TMP/$ASSET" | awk '{print $1}')"
else
	err "need sha256sum or shasum to verify the download"
fi
[ "$got" = "$want" ] || err "checksum mismatch for $ASSET (got $got, want $want)"

# --- extract + install ------------------------------------------------------
tar -xzf "$TMP/$ASSET" -C "$TMP" nightshift \
	|| err "could not extract nightshift from $ASSET"

mkdir -p "$INSTALL_DIR"
# Unlink any existing binary first so reinstalling over a running nightshift
# doesn't fail with "text file busy" (applies to both install and the cp fallback).
rm -f "$INSTALL_DIR/nightshift" 2>/dev/null
install -m 0755 "$TMP/nightshift" "$INSTALL_DIR/nightshift" 2>/dev/null \
	|| { cp "$TMP/nightshift" "$INSTALL_DIR/nightshift" && chmod 0755 "$INSTALL_DIR/nightshift"; }

echo "✓ Installed nightshift to $INSTALL_DIR/nightshift"

# --- PATH hint --------------------------------------------------------------
case ":$PATH:" in
	*":$INSTALL_DIR:"*) ;;
	*)
		echo
		echo "⚠️  $INSTALL_DIR is not on your PATH. Add it, e.g.:"
		echo "    echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.profile && . ~/.profile"
		;;
esac

# --- next steps -------------------------------------------------------------
cat <<EOF

Next steps:
  1. Authenticate the tools Nightshift drives:
       gh auth login
       claude   (or: codex login)
  2. Configure Nightshift:
       nightshift setup
  3. Run it as a background service (systemd hosts):
       nightshift install-service --start
     …or just run it directly:
       nightshift run

Upgrade later with:  nightshift update
EOF
