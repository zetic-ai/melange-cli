#!/bin/sh
# Installer for the melange CLI (https://github.com/zetic-ai/melange-cli).
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/zetic-ai/melange-cli/main/script/install.sh | sh
#
# Environment:
#   MELANGE_VERSION      install a specific version (e.g. v1.2.3); default: latest release
#   MELANGE_INSTALL_DIR  install directory; default /usr/local/bin, falling back
#                        to ~/.local/bin when /usr/local/bin is not writable
set -eu

REPO="zetic-ai/melange-cli"
BINARY="melange"

err() {
    echo "install.sh: $*" >&2
    exit 1
}

# --- Detect OS/arch ---------------------------------------------------------
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
    darwin | linux) ;;
    *) err "unsupported OS: $os (use GitHub Releases or 'go install' for other platforms)" ;;
esac

arch=$(uname -m)
case "$arch" in
    x86_64 | amd64) arch="amd64" ;;
    arm64 | aarch64) arch="arm64" ;;
    *) err "unsupported architecture: $arch" ;;
esac

# --- Resolve version --------------------------------------------------------
version="${MELANGE_VERSION:-}"
if [ -z "$version" ]; then
    version=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" |
        grep '"tag_name"' | head -n 1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
    [ -n "$version" ] || err "could not determine the latest release; set MELANGE_VERSION explicitly"
fi
# Strip the leading v for the archive name (archives use the bare version).
bare_version=${version#v}

archive="${BINARY}_${bare_version}_${os}_${arch}.tar.gz"
base_url="https://github.com/${REPO}/releases/download/${version}"

# --- Download and verify ----------------------------------------------------
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

echo "Downloading ${BINARY} ${version} (${os}/${arch})..." >&2
curl -fsSL -o "${tmpdir}/${archive}" "${base_url}/${archive}" ||
    err "download failed: ${base_url}/${archive}"
curl -fsSL -o "${tmpdir}/checksums.txt" "${base_url}/checksums.txt" ||
    err "download failed: ${base_url}/checksums.txt"

expected=$(grep " ${archive}\$" "${tmpdir}/checksums.txt" | awk '{print $1}')
[ -n "$expected" ] || err "no checksum found for ${archive} in checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "${tmpdir}/${archive}" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "${tmpdir}/${archive}" | awk '{print $1}')
else
    err "need sha256sum or shasum to verify the download"
fi
[ "$expected" = "$actual" ] || err "checksum mismatch for ${archive} (expected ${expected}, got ${actual})"
echo "Checksum verified." >&2

tar -xzf "${tmpdir}/${archive}" -C "$tmpdir"
[ -f "${tmpdir}/${BINARY}" ] || err "archive did not contain the ${BINARY} binary"

# --- Install ----------------------------------------------------------------
install_dir="${MELANGE_INSTALL_DIR:-/usr/local/bin}"
if [ ! -d "$install_dir" ] || [ ! -w "$install_dir" ]; then
    if [ -z "${MELANGE_INSTALL_DIR:-}" ]; then
        echo "${install_dir} is not writable; installing to ~/.local/bin instead." >&2
        install_dir="${HOME}/.local/bin"
        mkdir -p "$install_dir"
    else
        err "MELANGE_INSTALL_DIR=${install_dir} is not a writable directory"
    fi
fi

chmod +x "${tmpdir}/${BINARY}"
mv "${tmpdir}/${BINARY}" "${install_dir}/${BINARY}"
echo "Installed ${install_dir}/${BINARY} (${version})." >&2

case ":${PATH}:" in
    *":${install_dir}:"*) ;;
    *) echo "Note: ${install_dir} is not on your PATH; add it to your shell profile." >&2 ;;
esac

echo "Next step: run '${BINARY} auth login' (or export MELANGE_API_KEY) to authenticate." >&2
