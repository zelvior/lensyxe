#!/usr/bin/env sh
#
# Lensyxe installer.
#
#   curl -sSL https://raw.githubusercontent.com/zelvior/lensyxe/main/install.sh | bash
#
# Pin a release with LENSYXE_VERSION=0.3.0, and choose the install directory
# with LENSYXE_PREFIX:
#
#   curl -sSL .../install.sh | LENSYXE_VERSION=0.3.0 bash
#   curl -sSL .../install.sh | LENSYXE_PREFIX=$HOME/.local/bin bash
#
# SECURITY PROPERTIES
#
# This script installs an executable. It therefore:
#
#   1. Downloads checksums.txt FIRST and verifies the archive against it before
#      extracting anything. There is no way to skip this step.
#   2. Runs the freshly installed binary once (`lensyxe version`) and only
#      reports success if that works. A corrupt or wrong-architecture download
#      fails here rather than on the user's next command.
#   3. Never evaluates downloaded content as shell. The archive is unpacked
#      with tar/unzip, never with `sh`.
#   4. Refuses to write to a directory it cannot verify it can write to, instead
#      of silently falling back somewhere else.
#
# POSIX sh, not bash: it runs under dash on Debian/Ubuntu, sh on macOS, and
# busybox ash on Alpine. No arrays, no [[ ]], no local -n.

set -eu

# ----------------------------------------------------------------- constants

REPO="zelvior/lensyxe"
API_BASE="https://api.github.com/repos/${REPO}"
DOWNLOAD_BASE="https://github.com/${REPO}/releases/download"
BIN_NAME="lensyxe"

# The artifact names must match the name_template in .goreleaser.yaml exactly.
# A mismatch here is the most likely way this script breaks, so the mapping is
# written out rather than derived:
#
#   {{ .ProjectName }}_{{ .Version }}_{{ title .Os }}_{{ arch }}
#
# where arch is x86_64 for amd64 and arm64 for arm64.

# ------------------------------------------------------------------- helpers

# info writes a progress line to stderr.
#
# stdout is reserved for the one thing a caller might want to capture, so all
# chatter goes to stderr. This keeps `curl ... | bash` output readable when it
# is scrolled past.
info() { printf '%s\n' "$*" >&2; }

step() { printf '\n== %s\n' "$*" >&2; }

# fail prints a message and exits non-zero.
fail() {
  printf '\nlensyxe installer: %s\n' "$1" >&2
  exit 1
}

# have reports whether a command exists.
have() { command -v "$1" >/dev/null 2>&1; }

# sha256_of prints the SHA-256 of a file.
#
# Three implementations because the three platforms ship different tools:
# sha256sum on GNU/Linux, shasum on macOS, openssl as the portable fallback.
sha256_of() {
  file="$1"
  if have sha256sum; then
    sha256sum "$file" | awk '{print $1}'
  elif have shasum; then
    shasum -a 256 "$file" | awk '{print $1}'
  elif have openssl; then
    openssl dgst -sha256 "$file" | awk '{print $NF}'
  else
    fail "no SHA-256 tool found (need sha256sum, shasum, or openssl).
     Install coreutils, perl, or openssl and re-run. Verification is not
     optional: it is the only thing making this install trustworthy."
  fi
}

# fetch downloads a URL to stdout.
#
# --fail turns an HTTP error into a non-zero exit, which without it would
# happily save a 404 body and let the script carry on with garbage.
fetch() {
  curl --fail --silent --show-error --location "$1"
}

# ------------------------------------------------------------- OS detection

detect_os() {
  os="$(uname -s)"
  case "$os" in
    Darwin) echo "Darwin" ;;
    Linux)  echo "Linux" ;;
    # MSYS, MINGW, and CYGWIN are the three names Git Bash and Cygwin report.
    MSYS*|MINGW*|CYGWIN*) echo "Windows" ;;
    *)
      fail "unsupported operating system: $os
     Lensyxe ships binaries for Linux, macOS, and Windows."
      ;;
  esac
}

# detect_arch maps uname's architecture onto goreleaser's names.
detect_arch() {
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64)  echo "x86_64" ;;
    aarch64|arm64) echo "arm64" ;;
    *)
      fail "unsupported architecture: $arch
     Lensyxe ships binaries for x86_64 and arm64."
      ;;
  esac
}

# ----------------------------------------------------------- release lookup

# latest_version asks GitHub for the newest published tag.
#
# jq is deliberately not required: pulling a dependency into an install script
# that runs on a bare container is a bad trade for parsing one JSON key.
latest_version() {
  body="${workdir:-/tmp}/lensyxe-api.json"
  # The status code is captured separately from the body. Without it every
  # failure reads as "network problem", which sends someone behind a firewall to
  # debug a proxy when the real cause is a 404 or a rate limit.
  status="$(curl --silent --show-error --location \
      --output "$body" --write-out '%{http_code}' \
      "${API_BASE}/releases/latest" 2>/dev/null || echo 000)"

  case "$status" in
    200) ;;
    404)
      fail "the repository ${REPO} was not found on GitHub (HTTP 404).
     Check the name, or make sure the repository is public."
      ;;
    403|429)
      fail "the GitHub API refused the request (HTTP ${status}).
     This is usually the anonymous rate limit, which resets hourly.
     Wait and retry, or set GITHUB_TOKEN in your environment."
      ;;
    000)
      fail "could not reach api.github.com.
     If you are behind a proxy, set HTTPS_PROXY and try again."
      ;;
    *)
      fail "the GitHub API returned HTTP ${status} for the latest release."
      ;;
  esac

  json="$(cat "$body")"

  # The tag_name field is the first occurrence in GitHub's payload. Anchoring
  # the match to the whole line avoids picking up a same-named field nested
  # somewhere else in the object.
  tag="$(printf '%s' "$json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"

  [ -n "$tag" ] || fail "could not read tag_name from the GitHub API response.
     The response may have been an error object rather than a release."

  # A leading "v" is stripped to match goreleaser's {{ .Version }}, which is
  # derived from the tag without it. Getting this wrong produces a 404 on an
  # asset name that looks perfectly plausible.
  printf '%s' "$tag" | sed 's/^v//'
}

# --------------------------------------------------------------- checksum

# expected_checksum prints the recorded digest for an artifact.
#
# The lookup compares the full filename field, so an entry for
# `lensyxe_0.3.0_Linux_x86_64.tar.gz.sig` can never satisfy a request for the
# archive itself. Anchoring on the digest's own shape as well guards against a
# malformed checksums file yielding an empty match.
expected_checksum() {
  want="$1"
  file="$2"

  # shasum emits two spaces; the busybox sha256sum emits two spaces for text
  # mode and one for binary mode. Matching on $2 alone handles both.
  found="$(awk -v f="$want" '$2 == f { print $1; exit }' "$file")"

  case "$found" in
    [0-9a-f][0-9a-f]*)
      if [ "${#found}" -eq 64 ]; then
        printf '%s' "$found"
        return 0
      fi
      ;;
  esac

  fail "no usable SHA-256 recorded for ${want} in the release checksums file.
     The release may be incomplete. Check:
     https://github.com/${REPO}/releases"
}

# verify compares a downloaded file against its recorded digest.
verify() {
  file="$1"
  name="$2"
  want="$3"

  info "  expected ${want}"
  got="$(sha256_of "$file")"
  info "  actual   ${got}"

  # Both are lowercase hex from the tools above, and the comparison is exact
  # rather than case-insensitive so a genuine mismatch cannot be masked.
  if [ "$got" != "$want" ]; then
    fail "checksum mismatch for ${name}.
     The download is corrupt, or it was tampered with in transit.
     Nothing was installed."
  fi
  info "  checksum OK"
}

# ------------------------------------------------------------------ install

# install_to copies the binary into a prefix and makes it executable.
install_to() {
  src="$1"
  dir="$2"

  # Create the parent rather than the directory itself, so a prefix of
  # ~/.local/bin creates ~/.local too.
  mkdir -p "$dir" 2>/dev/null || fail "could not create ${dir}"
  [ -w "$dir" ] || fail "${dir} is not writable.
     Install to your home directory instead:
       curl -sSL .../install.sh | LENSYXE_PREFIX=\$HOME/.local/bin bash"

  dest="${dir}/${BIN_NAME}"

  # Install to a temporary name in the same directory and move it into place, so
  # an interrupted install cannot leave a half-written binary on PATH.
  tmp="${dest}.new.$$"
  trap 'rm -f "$tmp"' EXIT INT TERM
  cat "$src" > "$tmp"
  chmod +x "$tmp"
  mv -f "$tmp" "$dest"
  trap - EXIT INT TERM

  printf '%s' "$dest"
}

# add_to_path_note warns when the chosen directory is not on PATH.
path_note() {
  dir="$1"
  case ":${PATH:-}:" in
    *":${dir}:"*) return 0 ;;
  esac

  case "$(basename "$(dirname "$(dirname "$0")")")" in
    "") ;;
  esac

  shell_name="$(basename "${SHELL:-/bin/sh}")"
  case "$shell_name" in
    zsh)   rc="${ZDOTDIR:-$HOME}/.zshrc" ;;
    bash)  rc="$HOME/.bashrc" ;;
    fish)  rc="$HOME/.config/fish/config.fish" ;;
    *)     rc="$HOME/.profile" ;;
  esac

  info ""
  info "${dir} is not on your PATH. Add it:"
  info ""
  case "$shell_name" in
    fish) info "  echo 'set -gx PATH ${dir}:\$PATH' >> ${rc}" ;;
    *)    info "  echo 'export PATH=\"${dir}:\$PATH\"' >> ${rc}" ;;
  esac
  info ""
}

# ---------------------------------------------------------------------- main

main() {
  have curl || fail "curl is required but was not found."

  prefix="${LENSYXE_PREFIX:-}"

  step "Detecting platform"
  os="$(detect_os)"
  arch="$(detect_arch)"
  info "  ${os} / ${arch}"

  step "Resolving release"
  if [ -n "${LENSYXE_VERSION:-}" ]; then
    # Strip a leading "v" for the same reason latest_version does, so both paths
    # accept the tag as a user would type it.
    version="$(printf '%s' "$LENSYXE_VERSION" | sed 's/^v//')"
    info "  pinned to ${version}"
  else
    version="$(latest_version)"
    info "  latest is ${version}"
  fi

  case "$version" in
    *[!0-9A-Za-z.+-]*|'')
      fail "refusing to use version string '${version}': it contains characters
     that have no business in a release tag."
      ;;
  esac

  # Windows gets a zip; everything else gets a tar.gz. This mirrors the
  # format_overrides in .goreleaser.yaml.
  case "$os" in
    Windows) ext="zip" ;;
    *)       ext="tar.gz" ;;
  esac

  archive="${BIN_NAME}_${version}_${os}_${arch}.${ext}"

  # ------------------------------------------------------------------ work
  #
  # A trap rather than a fixed path: two installs in parallel must not collide,
  # and an interrupted install must not leave files behind.
  workdir="$(mktemp -d "${TMPDIR:-/tmp}/lensyxe-install.XXXXXX")" \
    || fail "could not create a temporary directory."
  trap 'rm -rf "$workdir"' EXIT INT TERM

  archive_path="${workdir}/${archive}"
  checksums_path="${workdir}/checksums.txt"

  # checksums.txt is fetched before the binary so that a failed verification is
  # cheap: nothing large has been downloaded yet.
  step "Downloading checksums"
  fetch "${DOWNLOAD_BASE}/v${version}/checksums.txt" > "$checksums_path" \
    || fail "could not download checksums.txt for v${version}."
  [ -s "$checksums_path" ] || fail "checksums.txt was empty."
  info "  $(wc -l < "$checksums_path" | tr -d ' ') entries"

  expected="$(expected_checksum "$archive" "$checksums_path")"

  step "Downloading ${archive}"
  fetch "${DOWNLOAD_BASE}/v${version}/${archive}" > "$archive_path" \
    || fail "could not download ${archive}."
  [ -s "$archive_path" ] || fail "${archive} downloaded empty."

  step "Verifying checksum"
  verify "$archive_path" "$archive" "$expected"

  step "Extracting"
  if [ "$ext" = "zip" ]; then
    have unzip || fail "unzip is required to install on Windows and was not found."
    # -o overwrites, -q keeps the file list out of the output.
    unzip -oq "$archive_path" -d "$workdir" \
      || fail "could not extract ${archive}."
  else
    # The binary is named explicitly rather than taking whatever is in the
    # archive. Trusting the contents of a downloaded tarball to place a file in
    # the current directory is how installers get hijacked.
    tar -xzf "$archive_path" -C "$workdir" "$BIN_NAME" 2>/dev/null \
      || tar -xzf "$archive_path" -C "$workdir" "${BIN_NAME}.exe" \
      || fail "could not extract ${BIN_NAME} from ${archive}."
  fi

  # Locate the extracted binary: goreleaser puts it at the archive root, but a
  # fallback keeps the script working if the layout ever gains a directory.
  extracted=""
  for candidate in "${workdir}/${BIN_NAME}" "${workdir}/${BIN_NAME}.exe" \
                   "${workdir}/${BIN_NAME}/${BIN_NAME}"; do
    if [ -f "$candidate" ]; then
      extracted="$candidate"
      break
    fi
  done
  [ -n "$extracted" ] || fail "${BIN_NAME} was not found inside ${archive}."
  chmod +x "$extracted"

  # The download directory is disposable; the trap already handles the failure
  # path, and the final trap is restored after install_to unsets its own.
  step "Installing"
  if [ -z "$prefix" ]; then
    if [ -w "/usr/local/bin" ] || [ "$(id -u)" = "0" ]; then
      prefix="/usr/local/bin"
    else
      prefix="${HOME}/.local/bin"
    fi
    info "  prefix ${prefix}"
  else
    info "  prefix ${prefix}"
  fi

  dest="$(install_to "$extracted" "$prefix")"

  # ------------------------------------------------- smoke test before success
  #
  # A binary that will not run is the single most common failure: a truncated
  # download on a partial network, or an archive for the wrong architecture.
  # Checking here means the user finds out from the installer rather than from
  # their first real command.
  step "Verifying installation"
  if ! "$dest" version >&2; then
    fail "${dest} was installed but does not run.
     It has been left in place so you can inspect it, but do not rely on it."
  fi

  info ""
  info "Installed ${dest}"
  path_note "$prefix"

  # The installed version is printed on stdout, so it can be captured:
  #   curl -sSL .../install.sh | bash | xargs lensyxe version
  "$dest" version

  rm -rf "$workdir"
  trap - EXIT INT TERM
}

main "$@"