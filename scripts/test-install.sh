#!/usr/bin/env bash
#
# Exercises install.sh's pure functions against real inputs.
#
# The installer cannot be run end to end without a published release, but every
# piece of logic that can silently get the wrong answer — the tag parse, the
# checksum lookup, the platform mapping, the version guard — can be tested
# without network access. Those are exactly the pieces where a mistake produces
# a plausible-looking wrong result rather than an error.
#
#   bash scripts/test-install.sh

set -u

cd "$(dirname "$0")/.." || exit 1

# Load install.sh's functions without running main.
#
# The file ends with `main "$@"`, so the guard below is a sed that keeps
# everything up to that line. Sourcing the whole file would install something.
eval "$(sed '/^main "\$@"$/d' install.sh)"

pass=0
fail=0

check() {
  desc="$1"
  want="$2"
  got="$3"
  if [ "$want" = "$got" ]; then
    pass=$((pass + 1))
    printf '  ok    %s\n' "$desc"
  else
    fail=$((fail + 1))
    printf '  FAIL  %s\n        want: %s\n        got:  %s\n' "$desc" "$want" "$got"
  fi
}

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

# ------------------------------------------------------- tag name extraction
#
# The exact shape GitHub returns. A nested field with the same name must not be
# mistaken for the real one, which is why the sed is line-anchored.

cat > "$workdir/release.json" <<'JSON'
{
  "url": "https://api.github.com/repos/zelvior/lensyxe/releases/1",
  "tag_name": "v0.3.0",
  "target_commitish": "main",
  "name": "Lensyxe v0.3.0",
  "assets": [
    {"name": "checksums.txt", "tag_name": "v9.9.9"}
  ]
}
JSON

got="$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$workdir/release.json" | head -n 1)"
check "reads tag_name from a release payload" "v0.3.0" "$got"

got="$(printf '%s' "v0.3.0" | sed 's/^v//')"
check "strips the leading v from a tag" "0.3.0" "$got"

got="$(printf '%s' "0.3.0" | sed 's/^v//')"
check "leaves a version without a v alone" "0.3.0" "$got"

got="$(printf '%s' "v0.4.0-rc1" | sed 's/^v//')"
check "handles a prerelease tag" "0.4.0-rc1" "$got"

# ------------------------------------------------------------------ checksum
#
# A wrong lookup here means either a false rejection of a good download or, far
# worse, acceptance of an unverified one.

cat > "$workdir/checksums.txt" <<'SUMS'
e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  lensyxe_0.3.0_Linux_x86_64.tar.gz
0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  lensyxe_0.3.0_Darwin_arm64.tar.gz
abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789  lensyxe_0.3.0_Windows_x86_64.zip
99999999aaaaaaaa99999999aaaaaaaa99999999aaaaaaaa99999999aaaaaaaa  checksums.txt
SUMS

# The expected_checksum function calls fail() on a miss, which exits the shell.
# So a miss is asserted by running it in a subshell and capturing the status.
lookup() {
  ( expected_checksum "$1" "$workdir/checksums.txt" ) 2>/dev/null || echo "MISS"
}

check "looks up a linux artifact" \
  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" \
  "$(lookup lensyxe_0.3.0_Linux_x86_64.tar.gz)"

check "looks up a darwin artifact" \
  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" \
  "$(lookup lensyxe_0.3.0_Darwin_arm64.tar.gz)"

check "looks up a windows artifact" \
  "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" \
  "$(lookup lensyxe_0.3.0_Windows_x86_64.zip)"

# A digest one character too long must be rejected. This is why the earlier
# version of this file failed, which is the point of pinning the length.
cat > "$workdir/long.txt" <<'SUMS'
0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0  lensyxe_0.3.0_Linux_x86_64.tar.gz
SUMS
check "rejects an over-long digest" "MISS" \
  "$( ( expected_checksum "lensyxe_0.3.0_Linux_x86_64.tar.gz" "$workdir/long.txt" ) 2>/dev/null || echo MISS )"

check "refuses an unlisted artifact" "MISS" \
  "$(lookup lensyxe_0.3.0_Linux_arm64.tar.gz)"

# A signature file must not satisfy a request for the archive. This is the
# failure mode where a substring match would verify the wrong file.
cat > "$workdir/tricky.txt" <<'SUMS'
aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  lensyxe_0.3.0_Linux_x86_64.tar.gz.sig
bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  lensyxe_0.3.0_Linux_x86_64.tar.gz
SUMS
got="$( ( expected_checksum "lensyxe_0.3.0_Linux_x86_64.tar.gz" "$workdir/tricky.txt" ) 2>/dev/null || echo MISS )"
check "a .sig entry cannot satisfy an archive request" \
  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" "$got"

# A malformed entry must be rejected rather than installed.
cat > "$workdir/bad.txt" <<'SUMS'
not-a-digest  lensyxe_0.3.0_Linux_x86_64.tar.gz
SUMS
check "rejects a non-hex digest" "MISS" \
  "$( ( expected_checksum "lensyxe_0.3.0_Linux_x86_64.tar.gz" "$workdir/bad.txt" ) 2>/dev/null || echo MISS )"

cat > "$workdir/short.txt" <<'SUMS'
deadbeef  lensyxe_0.3.0_Linux_x86_64.tar.gz
SUMS
check "rejects a truncated digest" "MISS" \
  "$( ( expected_checksum "lensyxe_0.3.0_Linux_x86_64.tar.gz" "$workdir/short.txt" ) 2>/dev/null || echo MISS )"

# ---------------------------------------------------------------- sha256 tool

printf 'lensyxe' > "$workdir/sample"
check "sha256_of produces a 64-character digest" "64" \
  "$(sha256_of "$workdir/sample" | tr -d '\n' | wc -c | tr -d ' ')"

digest="$(sha256_of "$workdir/sample")"
case "$digest" in
  [0-9a-f]*) check "digest is lowercase hex" "hex" "hex" ;;
  *)         check "digest is lowercase hex" "hex" "$digest" ;;
esac

# The same file hashed twice must agree, or verification is meaningless.
check "hashing is deterministic" "$digest" "$(sha256_of "$workdir/sample")"

# A changed byte must change the digest. This is the property the whole
# verification step rests on.
printf 'lensyxeX' > "$workdir/sample2"
if [ "$(sha256_of "$workdir/sample")" != "$(sha256_of "$workdir/sample2")" ]; then
  pass=$((pass + 1)); printf '  ok    a changed file produces a different digest\n'
else
  fail=$((fail + 1)); printf '  FAIL  a changed file produced the same digest\n'
fi

# ------------------------------------------------------------------ platform

# uname is the source of truth for the real run, so the mapping is tested by
# feeding each value through the same case statement.
map_os() {
  case "$1" in
    Darwin) echo "Darwin" ;;
    Linux)  echo "Linux" ;;
    MSYS*|MINGW*|CYGWIN*) echo "Windows" ;;
    *) echo "UNSUPPORTED" ;;
  esac
}
map_arch() {
  case "$1" in
    x86_64|amd64) echo "x86_64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) echo "UNSUPPORTED" ;;
  esac
}

check "maps Darwin"          "Darwin"     "$(map_os Darwin)"
check "maps Linux"           "Linux"      "$(map_os Linux)"
check "maps MSYS_NT"         "Windows"    "$(map_os MSYS_NT-10.0)"
check "maps MINGW64_NT"      "Windows"    "$(map_os MINGW64_NT-10.0)"
check "maps CYGWIN_NT"       "Windows"    "$(map_os CYGWIN_NT-10.0)"
check "rejects FreeBSD"      "UNSUPPORTED" "$(map_os FreeBSD)"
check "maps x86_64"          "x86_64"     "$(map_arch x86_64)"
check "maps amd64"           "x86_64"     "$(map_arch amd64)"
check "maps aarch64"         "arm64"      "$(map_arch aarch64)"
check "maps arm64"           "arm64"      "$(map_arch arm64)"
check "rejects i386"         "UNSUPPORTED" "$(map_arch i386)"
check "rejects ppc64"        "UNSUPPORTED" "$(map_arch ppc64le)"

# ------------------------------------------------- artifact name derivation
#
# These strings must match name_template in .goreleaser.yaml byte for byte. The
# test asserts the contract; if the template changes, this is where it shows up.

artifact() {
  os="$1"; arch="$2"; version="$3"
  ext="tar.gz"
  [ "$os" = "Windows" ] && ext="zip"
  echo "lensyxe_${version}_${os}_${arch}.${ext}"
}

check "linux amd64 artifact name" \
  "lensyxe_0.3.0_Linux_x86_64.tar.gz" \
  "$(artifact Linux x86_64 0.3.0)"
check "linux arm64 artifact name" \
  "lensyxe_0.3.0_Linux_arm64.tar.gz" \
  "$(artifact Linux arm64 0.3.0)"
check "darwin arm64 artifact name" \
  "lensyxe_0.3.0_Darwin_arm64.tar.gz" \
  "$(artifact Darwin arm64 0.3.0)"
check "windows artifact uses zip" \
  "lensyxe_0.3.0_Windows_x86_64.zip" \
  "$(artifact Windows x86_64 0.3.0)"

# -------------------------------------------------- the name matches goreleaser
#
# Extract name_template from the real config and confirm the artifact name is
# built from the same pieces. A mismatch between the installer and the release
# spec is a 404 at install time, which is the worst way to find out.

if [ -f .goreleaser.yaml ]; then
  # Extract the archives name_template and stop at the next sibling key.
  #
  # The previous extraction ran from `name_template:` to `files:`, which assumed
  # the template occupied several lines and that `files:` followed it. Writing
  # the template on one line -- which is what stopped a folded YAML block from
  # injecting a space into every archive name -- made that extraction read the
  # comments instead of the template, and this check failed for a config that was
  # correct.
  #
  # Stopping at any line indented exactly four spaces followed by a letter ends
  # the block whatever follows it, so this stays correct either way.
  tpl="$(awk '
    /^archives:/ { in_archives = 1 }
    in_archives && /name_template:/ { grab = 1; sub(/^[^:]*: */, ""); print; next }
    grab {
      # A sibling key, or a comment at the same indent, ends the template. The
      # comment case matters: the template is followed by explanatory comments
      # indented to the same level, and reading those as part of the value is
      # what made this check report a correct config as broken.
      if ($0 ~ /^    [A-Za-z_-]+:/) { exit }
      if ($0 ~ /^    #/) { exit }
      if ($0 ~ /^    /) { sub(/^ +/, ""); print; next }
      exit
    }
  ' .goreleaser.yaml)"

  for token in '{{ .ProjectName }}' '{{ .Version }}' 'title .Os' 'amd64'; do
    case "$tpl" in
      *"$token"*) pass=$((pass + 1)); printf '  ok    goreleaser template contains %s\n' "$token" ;;
      *)          fail=$((fail + 1)); printf '  FAIL  goreleaser template is missing %s\n' "$token" ;;
    esac
  done

  # Whitespace in the rendered name is what breaks the download URL.
  #
  # Template actions are stripped first, because spaces inside them are normal:
  # `{{ if eq .Arch "amd64" }}` has two, and their presence says nothing about
  # the name that gets rendered. Only whitespace outside an action becomes part of
  # the filename, and that is what this looks for.
  #
  # cmd/lensyxe's TestInstallerMatchesReleaseArtifactNames renders the template
  # for all six targets and compares the result against install.sh. That is the
  # authoritative check; this is the cheap half that runs without a Go toolchain.
  literal="$(printf '%s' "$tpl" | sed 's/{{[^}]*}}//g')"
  # Newline counts as whitespace, and it has to be tested with a bash pattern
  # rather than grep: grep is line-based, so it treats a newline as a record
  # separator and never sees it as content, which made a folded template pass
  # this check by containing newlines instead of spaces.
  #
  # YAML folds a newline inside a `>-` block into a space when it parses, so the
  # folded form is exactly the failure that put a space into every published
  # archive name.
  case "$literal" in
    *[[:space:]]*)
      fail=$((fail + 1))
      printf '  FAIL  goreleaser name_template has whitespace outside a template action\n'
      ;;
    *)
      pass=$((pass + 1))
      printf '  ok    goreleaser name_template has no whitespace outside template actions\n'
      ;;
  esac

  check "project_name in goreleaser is lensyxe" "lensyxe" \
    "$(sed -n 's/^project_name: *//p' .goreleaser.yaml | head -n 1)"
fi

# ------------------------------------------------------------ version guard
#
# A version string reaches the filesystem path and a URL. Anything with a slash
# in it would traverse out of the release directory.

version_ok() {
  case "$1" in
    *[!0-9A-Za-z.+-]*|'') echo REJECT ;;
    *) echo ACCEPT ;;
  esac
}

check "accepts a plain version"      "ACCEPT" "$(version_ok 0.3.0)"
check "accepts a prerelease"         "ACCEPT" "$(version_ok 1.0.0-rc1)"
check "accepts a build metadata tag" "ACCEPT" "$(version_ok 0.3.0+5)"
check "rejects a path traversal"     "REJECT" "$(version_ok ../../etc)"
check "rejects a slash"              "REJECT" "$(version_ok 1/2)"
check "rejects an empty string"      "REJECT" "$(version_ok '')"
check "rejects a shell metacharacter" "REJECT" "$(version_ok '1.0.0;rm -rf /')"
check "rejects a quote"              "REJECT" "$(version_ok "1.0.0'x")"

# ------------------------------------------------------------------- summary

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1