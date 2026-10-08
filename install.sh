#!/usr/bin/env sh
set -e

REPO="nasroykh/foxmayn_frappe_manager"
BINARY="ffm"

# Release public keys. Keep in sync with ReleaseKeys in internal/relsig/keys.go
# (TestInstallScriptInSync).
RELEASE_KEYS="6Dsj8Qv2qhi6zYV3LKZqRdkYeFifgEDbr0r+qYE4Jjs= z++AVagHrnyuwSeBHaQULVBwnf0NodJd1r4+LMEsL18="

# verify_signature checks checksums.txt.sig against checksums.txt with the
# domain prefix ffm signs (internal/relsig). It needs OpenSSL 3 for Ed25519
# (`pkeyutl -rawin`). Without it the install continues with the SHA-256 check
# only, with a warning. Returns non-zero only for a signature that is present
# and wrong, or missing when OpenSSL could have checked it.
verify_signature() {
  dir=$1
  if [ "${FFM_SKIP_SIGNATURE:-}" = "1" ]; then
    echo "Warning: skipping the release signature check (FFM_SKIP_SIGNATURE=1)." >&2
    return 0
  fi
  case "$(openssl version 2>/dev/null)" in
    "OpenSSL "[3-9]*) ;;
    *)
      echo "Warning: OpenSSL 3 not found; the release signature was not checked (SHA-256 only)." >&2
      echo "         Install OpenSSL 3 and re-run to check it. 'ffm update' always checks signatures." >&2
      return 0
      ;;
  esac
  if [ ! -s "$dir/checksums.txt.sig" ]; then
    echo "Error: the release has no checksums.txt.sig; refusing to install an unsigned release." >&2
    echo "Re-run with FFM_SKIP_SIGNATURE=1 to bypass at your own risk." >&2
    return 1
  fi
  printf 'ffm release checksums v1\n' > "$dir/signed-message"
  cat "$dir/checksums.txt" >> "$dir/signed-message"
  # openssl base64 -d exits 0 on bad input, so check the decoded size: an
  # Ed25519 signature is exactly 64 bytes.
  tr -d '\r\n' < "$dir/checksums.txt.sig" | openssl base64 -d -A > "$dir/sig.bin" 2>/dev/null || true
  if [ "$(wc -c < "$dir/sig.bin" | tr -d ' ')" != "64" ]; then
    echo "Error: checksums.txt.sig is not a valid Ed25519 signature." >&2
    return 1
  fi
  for key in $RELEASE_KEYS; do
    # SubjectPublicKeyInfo for Ed25519 = fixed 12-byte prefix + raw key.
    printf -- '-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA%s\n-----END PUBLIC KEY-----\n' "$key" > "$dir/release-key.pem"
    if openssl pkeyutl -verify -pubin -inkey "$dir/release-key.pem" -rawin \
      -in "$dir/signed-message" -sigfile "$dir/sig.bin" > /dev/null 2>&1; then
      echo "Release signature verified."
      return 0
    fi
  done
  echo "Error: checksums.txt.sig does not match any ffm release key. The download may have been tampered with." >&2
  return 1
}

# --- detect OS ---
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux)  OS="linux" ;;
  darwin) OS="darwin" ;;
  *)
    echo "Unsupported OS: $OS" >&2
    exit 1
    ;;
esac

# --- detect arch ---
ARCH=$(uname -m)
case "$ARCH" in
  x86_64 | amd64) ARCH="amd64" ;;
  arm64 | aarch64) ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

# --- resolve latest tag ---
VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
  | grep '"tag_name"' \
  | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')

if [ -z "$VERSION" ]; then
  echo "Could not determine latest release version." >&2
  exit 1
fi

echo "Installing ffm ${VERSION} (${OS}/${ARCH})..."

ARCHIVE="ffm_${VERSION#v}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"
CHECKSUM_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# --- download archive + checksums ---
curl -fsSL "$URL" -o "$TMP/$ARCHIVE"
curl -fsSL "$CHECKSUM_URL" -o "$TMP/checksums.txt"
# A missing signature is caught by verify_signature, so the download may fail.
curl -fsSL "$CHECKSUM_URL.sig" -o "$TMP/checksums.txt.sig" 2>/dev/null || true

# --- verify signature, then checksum ---
verify_signature "$TMP" || exit 1

cd "$TMP"
if ! grep -q "  $ARCHIVE\$" checksums.txt; then
  echo "Error: checksums.txt has no entry for $ARCHIVE; refusing to install." >&2
  exit 1
fi
if command -v sha256sum > /dev/null 2>&1; then
  grep "  $ARCHIVE\$" checksums.txt | sha256sum -c - || { echo "Error: checksum mismatch for $ARCHIVE." >&2; exit 1; }
elif command -v shasum > /dev/null 2>&1; then
  grep "  $ARCHIVE\$" checksums.txt | shasum -a 256 -c - || { echo "Error: checksum mismatch for $ARCHIVE." >&2; exit 1; }
else
  echo "Error: no sha256 tool found (sha256sum or shasum); refusing to install an unverified binary." >&2
  exit 1
fi
cd - > /dev/null

# --- extract ---
tar -xzf "$TMP/$ARCHIVE" -C "$TMP"

# --- install ---
INSTALL_DIR=""
if [ -w "/usr/local/bin" ]; then
  INSTALL_DIR="/usr/local/bin"
elif [ -d "$HOME/.local/bin" ]; then
  INSTALL_DIR="$HOME/.local/bin"
else
  mkdir -p "$HOME/.local/bin"
  INSTALL_DIR="$HOME/.local/bin"
fi

mv "$TMP/$BINARY" "$INSTALL_DIR/$BINARY"
chmod +x "$INSTALL_DIR/$BINARY"

echo "Installed to $INSTALL_DIR/$BINARY"
echo "Run 'ffm --help' to get started."

# warn if install dir is not in PATH
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    echo ""
    echo "Note: $INSTALL_DIR is not in your PATH."
    echo "Add this to your shell profile:"
    echo "  export PATH=\"\$PATH:$INSTALL_DIR\""
    ;;
esac
