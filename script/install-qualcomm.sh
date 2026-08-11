#!/bin/sh
# One-line installer for the Qualcomm-focused Melange CLI and its agent skill.
# https://github.com/zetic-ai/melange-cli
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/zetic-ai/melange-cli/main/script/install-qualcomm.sh | sh
#
# Options (pass with `| sh -s -- <flags>`, or use the environment variables):
#   --version VERSION     MELANGE_VERSION       install a specific release (e.g. v1.2.3); default: latest
#   --install-dir DIR     MELANGE_INSTALL_DIR   binary directory; default /usr/local/bin,
#                                               falling back to ~/.local/bin when not writable
#   --skill-only          MELANGE_SKIP_CLI=1    install only the agent skill
#   --cli-only            MELANGE_SKIP_SKILL=1  install only the CLI
#   --agent "A B"         MELANGE_SKILL_AGENTS  agents to install the skill for;
#                                               default "universal claude-code"
#   --require-signature   MELANGE_REQUIRE_SIGNATURE=1
#                                               fail unless the release signature is verified
#   --help
#
# Security: release checksums are always verified. The checksum manifest itself
# is signed keylessly by this repository's release workflow; when `cosign` is on
# PATH the signature is verified too, and --require-signature makes that
# mandatory. Install cosign from
# https://docs.sigstore.dev/cosign/system_config/installation/.
set -eu

# This script is written for POSIX sh, but people do pipe installers into their
# login shell. zsh does not word-split unquoted parameters, which would hand the
# agent list to `npx skills add` as one argument; shwordsplit restores the
# POSIX behaviour this script is written against.
if [ -n "${ZSH_VERSION:-}" ]; then
    setopt shwordsplit 2>/dev/null || true
fi

REPO="zetic-ai/melange-cli"
BINARY="melange-qualcomm"
SKILL="melange-qualcomm"

# --- Output -----------------------------------------------------------------
# Everything the installer says goes to stderr, so `curl … | sh` leaves stdout
# clean for callers that capture it.
info() { printf '%s\n' "$*" >&2; }
step() { printf '==> %s\n' "$*" >&2; }
warn() { printf 'warning: %s\n' "$*" >&2; }
err() {
    printf 'install-qualcomm.sh: %s\n' "$*" >&2
    exit 1
}

have() { command -v "$1" >/dev/null 2>&1; }

# Printed from a heredoc rather than read out of $0: under `curl … | sh` this
# script has no path on disk to read the comment header back from.
usage() {
    cat >&2 <<'USAGE'
Install the Qualcomm-focused Melange CLI and its agent skill.

  curl -fsSL https://raw.githubusercontent.com/zetic-ai/melange-cli/main/script/install-qualcomm.sh | sh

Options (append with `| sh -s -- <flags>`, or use the environment variable):
  --version VERSION     MELANGE_VERSION       release to install (e.g. v1.2.3); default: latest
  --install-dir DIR     MELANGE_INSTALL_DIR   binary directory; default /usr/local/bin,
                                              falling back to ~/.local/bin when not writable
  --skill-only          MELANGE_SKIP_CLI=1    install only the agent skill
  --cli-only            MELANGE_SKIP_SKILL=1  install only the CLI
  --agent "A B"         MELANGE_SKILL_AGENTS  agents to install the skill for;
                                              default "universal claude-code"
  --require-signature   MELANGE_REQUIRE_SIGNATURE=1
                                              fail unless the release signature is verified
  -h, --help            show this message
USAGE
    exit 0
}

# shell_quote wraps a string in single quotes for safe reuse inside another
# shell command, escaping any embedded single quote. Install paths are usually
# boring, but a path with a space or an apostrophe must not produce a
# remediation line that breaks when pasted.
shell_quote() {
    printf "'"
    printf '%s' "$1" | sed "s/'/'\\\\''/g"
    printf "'"
}

is_vsemver() {
    case "$1" in
        "" | *[!0-9A-Za-z.+-]*) return 1 ;;
    esac
    printf '%s\n' "$1" | LC_ALL=C grep -Eq \
        '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*)|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)(\.((0|[1-9][0-9]*)|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
}

# --- Options ----------------------------------------------------------------
version="${MELANGE_VERSION:-}"
install_dir="${MELANGE_INSTALL_DIR:-}"
skip_cli="${MELANGE_SKIP_CLI:-}"
skip_skill="${MELANGE_SKIP_SKILL:-}"
skill_agents="${MELANGE_SKILL_AGENTS:-universal claude-code}"
require_signature="${MELANGE_REQUIRE_SIGNATURE:-}"

while [ $# -gt 0 ]; do
    case "$1" in
        --version)
            [ $# -ge 2 ] || err "--version needs a value (e.g. --version v1.2.3)"
            version="$2"
            shift 2
            ;;
        --version=*)
            version="${1#--version=}"
            shift
            ;;
        --install-dir)
            [ $# -ge 2 ] || err "--install-dir needs a value"
            install_dir="$2"
            shift 2
            ;;
        --install-dir=*)
            install_dir="${1#--install-dir=}"
            shift
            ;;
        --agent)
            [ $# -ge 2 ] || err "--agent needs a value (e.g. --agent \"universal claude-code\")"
            skill_agents="$2"
            shift 2
            ;;
        --agent=*)
            skill_agents="${1#--agent=}"
            shift
            ;;
        --skill-only)
            skip_cli=1
            shift
            ;;
        --cli-only)
            skip_skill=1
            shift
            ;;
        --require-signature)
            require_signature=1
            shift
            ;;
        -h | --help) usage ;;
        *) err "unknown option: $1 (run with --help)" ;;
    esac
done

[ -n "$skip_cli" ] && [ -n "$skip_skill" ] &&
    err "--skill-only and --cli-only cancel each other out; pick one"

have curl || err "curl is required"
have tar || err "tar is required"
[ -n "${HOME:-}" ] || err "HOME is not set"

# Fail before downloading anything if the requested verification is impossible.
if [ -n "$require_signature" ] && [ -z "$skip_cli" ] && ! have cosign; then
    err "cosign is required by --require-signature; install it from https://docs.sigstore.dev/cosign/system_config/installation/"
fi

# --- Detect OS/arch ---------------------------------------------------------
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
    darwin | linux) ;;
    mingw* | msys* | cygwin* | windows*)
        err "this installer does not support Windows; use the signed GitHub release archive"
        ;;
    *) err "unsupported OS: $os (use GitHub Releases for other platforms)" ;;
esac

arch=$(uname -m)
case "$arch" in
    x86_64 | amd64) arch="amd64" ;;
    arm64 | aarch64) arch="arm64" ;;
    *) err "unsupported architecture: $arch" ;;
esac

# --- Resolve version --------------------------------------------------------
# The binary and the skill are pinned to the same release tag, so an agent never
# drives one version of the CLI with another version's skill.
if [ -n "$version" ] && ! is_vsemver "$version"; then
    err "version must be a v-prefixed semantic version (for example v1.2.3 or v1.2.3-rc.1)"
fi
if [ -z "$version" ]; then
    # The /releases/latest redirect resolves the newest non-prerelease tag
    # without spending an anonymous GitHub API request. That quota is per IP and
    # is the usual reason a piped installer fails on a shared or CI network.
    latest_url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
        "https://github.com/${REPO}/releases/latest" 2>/dev/null) || latest_url=""
    case "$latest_url" in
        */releases/tag/*) version="${latest_url##*/releases/tag/}" ;;
        *) version="" ;;
    esac
    if ! is_vsemver "$version"; then
        version=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null |
            grep '"tag_name"' | head -n 1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/') || version=""
    fi
    [ -n "$version" ] || err "could not determine the latest release; pass --version explicitly"
    is_vsemver "$version" ||
        err "latest release tag is not a v-prefixed semantic version: $version"
fi
# Strip the leading v for the archive name (archives use the bare version).
bare_version=${version#v}

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT
trap 'rm -rf "$tmpdir"; exit 130' INT
trap 'rm -rf "$tmpdir"; exit 143' TERM

# --- Install the CLI --------------------------------------------------------
installed_bin=""
target_dir=""
if [ -z "$skip_cli" ]; then
    archive="${BINARY}_${bare_version}_${os}_${arch}.tar.gz"
    base_url="https://github.com/${REPO}/releases/download/${version}"

    step "Downloading ${BINARY} ${version} (${os}/${arch})"
    curl -fsSL -o "${tmpdir}/${archive}" "${base_url}/${archive}" ||
        err "download failed: ${base_url}/${archive}"
    curl -fsSL -o "${tmpdir}/checksums.txt" "${base_url}/checksums.txt" ||
        err "download failed: ${base_url}/checksums.txt"

    if have cosign; then
        curl -fsSL -o "${tmpdir}/checksums.txt.sigstore.json" \
            "${base_url}/checksums.txt.sigstore.json" ||
            err "download failed: ${base_url}/checksums.txt.sigstore.json"
        if cosign verify-blob \
            --bundle "${tmpdir}/checksums.txt.sigstore.json" \
            --certificate-identity "https://github.com/${REPO}/.github/workflows/release.yml@refs/tags/${version}" \
            --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
            "${tmpdir}/checksums.txt" >/dev/null 2>&1; then
            info "Release signature verified."
        elif cosign verify-blob \
            --bundle "${tmpdir}/checksums.txt.sigstore.json" \
            --certificate-identity "https://github.com/${REPO}/.github/workflows/release.yml@refs/heads/main" \
            --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
            "${tmpdir}/checksums.txt" >/dev/null 2>&1; then
            warn "Release signature verified against refs/heads/main (workflow_dispatch for ${version}); expected refs/tags/${version}."
            info "Release signature verified."
        elif [ -n "$require_signature" ]; then
            err "signature verification failed for checksums.txt"
        else
            warn "signature verification failed for checksums.txt — continuing with SHA-256 verification only."
            warn "Install cosign and re-run with --require-signature to enforce signature verification."
        fi
    elif [ -n "$require_signature" ]; then
        err "cosign is required by --require-signature; install it from https://docs.sigstore.dev/cosign/system_config/installation/"
    else
        warn "cosign not found — verifying the SHA-256 checksum only."
        info "         For signature verification too, install cosign"
        info "         (https://docs.sigstore.dev/cosign/system_config/installation/)"
        info "         and re-run with --require-signature."
    fi

    expected=$(grep " ${archive}\$" "${tmpdir}/checksums.txt" | awk '{print $1}')
    [ -n "$expected" ] || err "no checksum found for ${archive} in checksums.txt"

    if have sha256sum; then
        actual=$(sha256sum "${tmpdir}/${archive}" | awk '{print $1}')
    elif have shasum; then
        actual=$(shasum -a 256 "${tmpdir}/${archive}" | awk '{print $1}')
    else
        err "need sha256sum or shasum to verify the download"
    fi
    [ "$expected" = "$actual" ] ||
        err "checksum mismatch for ${archive} (expected ${expected}, got ${actual})"
    info "Checksum verified."

    tar -xzf "${tmpdir}/${archive}" -C "$tmpdir" ||
        err "could not extract ${archive} (a gzip-capable tar is required)"
    [ -f "${tmpdir}/${BINARY}" ] || err "archive did not contain the ${BINARY} binary"

    target_dir="${install_dir:-/usr/local/bin}"
    # An explicitly requested directory is created on demand; the default one is
    # never created, because a missing /usr/local/bin is the signal to fall back
    # to the per-user location rather than to start making system directories.
    if [ -n "$install_dir" ] && [ ! -d "$target_dir" ]; then
        mkdir -p "$target_dir" || err "could not create install directory ${target_dir}"
    fi
    if [ ! -d "$target_dir" ] || [ ! -w "$target_dir" ]; then
        if [ -z "$install_dir" ]; then
            info "${target_dir} is not writable; installing to ~/.local/bin instead."
            target_dir="${HOME}/.local/bin"
            mkdir -p "$target_dir" || err "could not create ${target_dir}"
        else
            err "install directory ${target_dir} is not a writable directory"
        fi
    fi

    chmod 755 "${tmpdir}/${BINARY}"
    # Remove before moving: overwriting a running binary in place fails with
    # ETXTBSY on Linux, and unlink+rename never leaves a half-written melange
    # on disk for a concurrent shell to run.
    rm -f "${target_dir}/${BINARY}"
    mv "${tmpdir}/${BINARY}" "${target_dir}/${BINARY}" ||
        err "could not install into ${target_dir}"
    installed_bin="${target_dir}/${BINARY}"
    info "Installed ${installed_bin} (${version})."
fi

# --- Install the agent skill ------------------------------------------------
# Global skill directories, per agent. `npx skills` owns these paths; the
# fallback below reproduces the layout for the two it can place without
# guessing, so a machine without Node still ends up with a working skill.
skill_dir_for() {
    case "$1" in
        universal) printf '%s\n' "${XDG_CONFIG_HOME:-$HOME/.config}/agents/skills" ;;
        claude-code) printf '%s\n' "${CLAUDE_CONFIG_DIR:-$HOME/.claude}/skills" ;;
        *) return 1 ;;
    esac
}

copy_skill_to() {
    # $1 = parent skills directory, $2 = agent label
    [ -n "$1" ] || err "empty skill directory for $2"
    mkdir -p "$1" || err "could not create $1"
    # Remove first: `cp -R` onto an existing directory merges into it, which
    # would strand files from a previous version. This also clears a symlink
    # left behind by an earlier `npx skills add`.
    rm -rf "$1/$SKILL"
    cp -R "$skill_src" "$1/$SKILL" || err "could not install the skill into $1"
    info "Installed skill '$SKILL' for ${2}: $1/$SKILL"
}

fetch_skill_source() {
    [ -f "${tmpdir}/src/skills/${SKILL}/SKILL.md" ] && return 0
    mkdir -p "${tmpdir}/src"
    curl -fsSL -o "${tmpdir}/source.tar.gz" \
        "https://github.com/${REPO}/archive/refs/tags/${version}.tar.gz" ||
        err "could not download the skill source for ${version}"
    tar -xzf "${tmpdir}/source.tar.gz" -C "${tmpdir}/src" --strip-components=1 ||
        err "could not extract the skill source"
    [ -f "${tmpdir}/src/skills/${SKILL}/SKILL.md" ] ||
        err "release ${version} does not contain skills/${SKILL}"
}

skill_installed=""
skill_note=""
if [ -z "$skip_skill" ]; then
    step "Installing the ${SKILL} agent skill"
    skill_done=""
    if have npx; then
        # The published `skills` CLI is the supported path: it knows every agent
        # layout and records the install, so `npx skills update` works later. It
        # needs a recent Node, so its failure is routine rather than fatal — the
        # output is captured and only surfaced when it does fail.
        # shellcheck disable=SC2086 # skill_agents is a deliberate word list
        if npx --yes skills add "$REPO" --skill "$SKILL" \
            --agent $skill_agents --global --yes >"${tmpdir}/npx.log" 2>&1; then
            skill_done=1
            skill_installed="$skill_agents"
            info "Installed skill '$SKILL' via 'npx skills add'."
        else
            warn "'npx skills add' failed (it needs a recent Node); copying the skill files directly instead."
            tail -n 3 "${tmpdir}/npx.log" | sed 's/^/  | /' >&2
        fi
    else
        info "Node/npx not found; copying the skill files directly."
    fi

    if [ -z "$skill_done" ]; then
        fetch_skill_source
        skill_src="${tmpdir}/src/skills/${SKILL}"
        for agent in $skill_agents; do
            if agent_dir=$(skill_dir_for "$agent"); then
                copy_skill_to "$agent_dir" "$agent"
                skill_installed="${skill_installed} ${agent}"
            else
                warn "cannot place the skill for '${agent}' without a working 'npx skills'; install Node 22+ and run: npx skills add ${REPO} --skill ${SKILL} --agent ${agent} --global --yes"
            fi
        done
        [ -n "$skill_installed" ] ||
            err "no skill was installed; install Node 22+ and run: npx skills add ${REPO} --skill ${SKILL} --global --yes"
        skill_note="installed by copy — re-run this installer to update it"
    fi
fi

# --- Report -----------------------------------------------------------------
next_cmd="$BINARY"
if [ -n "$installed_bin" ]; then
    # When the install directory is not on PATH, `melange` will not resolve in
    # this shell or the next one — so an unqualified "run melange auth login" is
    # not a runnable instruction. Print the exact line that fixes the current
    # shell, the persistent equivalent for the shell actually in use, and
    # qualify the next step with the full path so it works either way.
    case ":${PATH}:" in
        *":${target_dir}:"*) ;;
        *)
            next_cmd="$installed_bin"
            # Keep $HOME symbolic in the printed lines so a shared dotfile stays
            # portable across machines.
            display_dir="$target_dir"
            case "$target_dir" in
                "${HOME}"/*) display_dir="\$HOME${target_dir#"${HOME}"}" ;;
            esac
            posix_line="export PATH=\"${display_dir}:\$PATH\""

            # SHELL is routinely unset in containers, cron and CI, so it is
            # read defensively — an unset one just falls through to the
            # generic advice below.
            user_shell="${SHELL:-}"
            case "${user_shell##*/}" in
                fish)
                    # fish has no `export`, and $PATH there is a list that
                    # "$PATH" would flatten into one space-joined entry — the
                    # POSIX line corrupts PATH rather than extending it.
                    path_line="set -gx PATH \"${display_dir}\" \$PATH"
                    persist="fish_add_path \"${display_dir}\""
                    ;;
                zsh)
                    path_line="$posix_line"
                    persist="echo $(shell_quote "$posix_line") >> ~/.zshrc"
                    ;;
                bash)
                    path_line="$posix_line"
                    if [ "$(uname -s)" = "Darwin" ]; then
                        persist="echo $(shell_quote "$posix_line") >> ~/.bash_profile"
                    else
                        persist="echo $(shell_quote "$posix_line") >> ~/.bashrc"
                    fi
                    ;;
                *)
                    path_line="$posix_line"
                    persist="add that line to your shell profile"
                    ;;
            esac

            info ""
            info "${target_dir} is not on your PATH."
            info "  For this shell:  ${path_line}"
            info "  To persist:      ${persist}"
            ;;
    esac
fi

info ""
step "Done."
[ -n "$installed_bin" ] && info "  CLI:   ${installed_bin} (${version})"
if [ -n "$skill_installed" ]; then
    # Squeeze the separator the fallback loop accumulates into single spaces.
    agent_list=$(printf '%s' "$skill_installed" | tr -s ' ' | sed 's/^ //; s/ $//')
    info "  Skill: ${SKILL} → ${agent_list}${skill_note:+ (${skill_note})}"
fi
info ""
info "Next steps:"
info "  1. Authenticate: ${next_cmd} auth login   (or export MELANGE_API_KEY=ztp_…)"
info "     Get a token at https://melange.zetic.ai/settings?tab=pat"
[ -n "$skill_installed" ] &&
    info "  2. Restart your coding agent so it discovers the ${SKILL} skill."

exit 0
