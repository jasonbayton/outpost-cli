#!/usr/bin/env sh
# install.sh — one-shot installer for the `outpost` CLI.
#
#   curl -fsSL https://raw.githubusercontent.com/jasonbayton/outpost-cli/main/install.sh | sh
#
# Detects your OS + arch, downloads the matching tarball from the latest
# (or pinned) GitHub Release, verifies the SHA-256 against the release's
# checksums.txt, and installs the binary into the first writable
# directory found in: $OUTPOST_INSTALL_DIR, /usr/local/bin, $HOME/.local/bin,
# $HOME/bin. Falls back to writing into the working directory rather
# than failing.
#
# Override knobs (env vars):
#   OUTPOST_VERSION       — pin to a specific tag (default: latest release)
#   OUTPOST_INSTALL_DIR   — install location (default: see above)
#   OUTPOST_NO_VERIFY     — skip checksum verification (not recommended)

set -eu

REPO="jasonbayton/outpost-cli"
BINARY="outpost"

err() { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '%s\n' "$*"; }

# --- detect OS + arch ------------------------------------------------------

detect_platform() {
  uname_s=$(uname -s 2>/dev/null || echo unknown)
  uname_m=$(uname -m 2>/dev/null || echo unknown)
  case "$uname_s" in
    Darwin) os=darwin ;;
    Linux)  os=linux ;;
    MINGW*|MSYS*|CYGWIN*) err "Windows is not supported by this installer; download the .zip from https://github.com/${REPO}/releases manually" ;;
    *) err "unsupported OS: $uname_s" ;;
  esac
  case "$uname_m" in
    x86_64|amd64)        arch=amd64 ;;
    arm64|aarch64)       arch=arm64 ;;
    *) err "unsupported architecture: $uname_m" ;;
  esac
  printf '%s_%s' "$os" "$arch"
}

# --- find a writable bin dir ----------------------------------------------

choose_install_dir() {
  if [ -n "${OUTPOST_INSTALL_DIR:-}" ]; then
    printf '%s' "$OUTPOST_INSTALL_DIR"
    return
  fi
  for dir in /usr/local/bin "$HOME/.local/bin" "$HOME/bin"; do
    if [ -d "$dir" ] && [ -w "$dir" ]; then
      printf '%s' "$dir"
      return
    fi
  done
  # Try /usr/local/bin via sudo as a last resort if the user looks
  # interactive. Otherwise fall back to PWD so the script never aborts
  # without producing a usable binary.
  if [ -t 0 ] && [ -t 1 ] && command -v sudo >/dev/null 2>&1; then
    if [ -d /usr/local/bin ]; then
      printf '%s' /usr/local/bin
      return
    fi
  fi
  printf '%s' "$PWD"
}

# --- resolve which release tag to install ---------------------------------

resolve_version() {
  if [ -n "${OUTPOST_VERSION:-}" ]; then
    case "$OUTPOST_VERSION" in
      v*) printf '%s' "$OUTPOST_VERSION" ;;
      *)  printf 'v%s' "$OUTPOST_VERSION" ;;
    esac
    return
  fi
  # GitHub's "latest" redirect points at /releases/tag/<latest tag>.
  # We follow it without depending on jq or the API.
  url=$(curl -fsSL -o /dev/null -w '%{url_effective}' \
    "https://github.com/${REPO}/releases/latest" 2>/dev/null || true)
  if [ -z "$url" ]; then
    err "could not resolve latest release; set OUTPOST_VERSION=vX.Y.Z to pin"
  fi
  # url ends in /releases/tag/vX.Y.Z
  printf '%s' "${url##*/}"
}

# --- verify SHA-256 against release checksums.txt -------------------------

verify_checksum() {
  archive="$1"
  expected_filename="$2"
  checksums_url="$3"
  if [ "${OUTPOST_NO_VERIFY:-0}" = "1" ]; then
    info "warning: skipping checksum verification (OUTPOST_NO_VERIFY=1)"
    return
  fi
  if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
    info "warning: no sha256sum/shasum on PATH, skipping verification"
    return
  fi
  checksums=$(curl -fsSL "$checksums_url") || err "could not fetch checksums.txt"
  expected=$(printf '%s\n' "$checksums" | awk -v f="$expected_filename" '$2 == f { print $1 }')
  if [ -z "$expected" ]; then
    err "checksum for $expected_filename not found in checksums.txt"
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$archive" | awk '{print $1}')
  else
    actual=$(shasum -a 256 "$archive" | awk '{print $1}')
  fi
  if [ "$actual" != "$expected" ]; then
    err "checksum mismatch for $expected_filename
  expected: $expected
  actual:   $actual"
  fi
}

# --- main ------------------------------------------------------------------

main() {
  command -v curl >/dev/null 2>&1 || err "curl is required"
  command -v tar  >/dev/null 2>&1 || err "tar is required"

  platform=$(detect_platform)
  version=$(resolve_version)
  install_dir=$(choose_install_dir)

  case "$platform" in
    darwin_amd64|darwin_arm64|linux_amd64|linux_arm64) ;;
    *) err "no published binary for $platform" ;;
  esac

  ver_no_v="${version#v}"
  archive_name="${BINARY}_${ver_no_v}_${platform}.tar.gz"
  download_url="https://github.com/${REPO}/releases/download/${version}/${archive_name}"
  checksums_url="https://github.com/${REPO}/releases/download/${version}/checksums.txt"

  tmp=$(mktemp -d 2>/dev/null || mktemp -d -t outpost)
  trap 'rm -rf "$tmp"' EXIT

  info "Downloading ${BINARY} ${version} for ${platform}…"
  curl -fsSL "$download_url" -o "$tmp/$archive_name" \
    || err "download failed: $download_url"

  verify_checksum "$tmp/$archive_name" "$archive_name" "$checksums_url"

  info "Extracting…"
  tar -xzf "$tmp/$archive_name" -C "$tmp"
  if [ ! -x "$tmp/$BINARY" ]; then
    err "tarball did not contain a $BINARY binary; aborting"
  fi

  target="$install_dir/$BINARY"
  # Create the install dir if the user supplied one that doesn't yet
  # exist (common for $HOME/.local/bin on minimal boxes). For
  # /usr/local/bin we never mkdir — that's owned by /etc/skel etc.
  case "$install_dir" in
    /usr/local/bin|/usr/bin) ;;
    *) [ -d "$install_dir" ] || mkdir -p "$install_dir" 2>/dev/null || true ;;
  esac
  if [ -w "$install_dir" ]; then
    install -m 0755 "$tmp/$BINARY" "$target"
  elif [ -t 0 ] && [ -t 1 ] && command -v sudo >/dev/null 2>&1; then
    info "Installing to $target (requires sudo)…"
    sudo install -m 0755 "$tmp/$BINARY" "$target"
  else
    err "cannot write to $install_dir; set OUTPOST_INSTALL_DIR to a writable path"
  fi

  info ""
  info "Installed ${BINARY} ${version} to ${target}"
  case ":$PATH:" in
    *":$install_dir:"*) ;;
    *) info "Note: $install_dir is not in your PATH; add it to ~/.profile or ~/.zshrc" ;;
  esac
  info "Run '${BINARY} auth login --server https://your-outpost.example.org' to get started."
}

main "$@"
