#!/usr/bin/env bash
#
# Prepare the tree for a goreleaser build.
#
# This exists as a script rather than as inline YAML because goreleaser's
# before-hooks are plain command strings, and the logic here needs conditionals,
# quoting, and a real exit code. A hook that cannot fail the build is not a
# verification step.
#
# It is idempotent and safe to run by hand. The release workflow calls it too,
# so a local `goreleaser release --snapshot --clean` and a CI release produce
# the same binary from the same tree.
#
# Failure is deliberate: a release built from an untidy tree or an unformatted
# file is worse than no release, because the tag then claims something untrue.

set -euo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"

log() { printf '  %s\n' "$*"; }

# ---------------------------------------------------------------- dashboard
#
# The Go binary embeds the dashboard through //go:embed, which can only read a
# directory inside the package that declares it. dashboard/out is outside
# internal/server, so the export has to be copied in before the Go build.

target="internal/server/assets"

if [ -d dashboard/out ] && [ -n "$(ls -A dashboard/out 2>/dev/null)" ]; then
  rm -rf "$target"
  mkdir -p "$target"
  cp -r dashboard/out/. "$target"/
  # Restore .gitkeep. It is committed so a fresh clone has at least one file for
  # //go:embed to match, and the rm -rf above would otherwise delete it locally,
  # making `git status` report a deletion that no one actually made. Without
  # this, running this script once dirties the working tree for no reason.
  touch "$target/.gitkeep"
  log "embedded $(find "$target" -type f | wc -l | tr -d ' ') dashboard files"
else
  # Not fatal. The binary still builds and the API still serves; only the UI is
  # missing, and the server prints an explanation at startup instead of serving
  # a blank page. A warning is the right outcome here because a release without
  # the dashboard is degraded, not broken.
  echo "::warning::dashboard/out is empty; this build ships without the dashboard"
  mkdir -p "$target"
  # //go:embed needs at least one file to match, and an empty directory is a
  # compile error rather than a graceful degradation.
  [ -f "$target/.gitkeep" ] || touch "$target/.gitkeep"
fi

# --------------------------------------------------------------- gofmt
#
# A release that reformats code would mean the tag and the source disagree.

unformatted="$(gofmt -l . 2>/dev/null | grep -v '^dashboard/' || true)"
if [ -n "$unformatted" ]; then
  echo "::error::these files are not gofmt-formatted:"
  echo "$unformatted"
  exit 1
fi
log "gofmt clean"

# ----------------------------------------------------------- module files
#
# -diff reports what tidy would change and exits non-zero, without rewriting.
# Rewriting here would silently absorb a dependency change into a tag.

if ! output="$(go mod tidy -diff 2>&1)"; then
  echo "::error::go.mod or go.sum is not tidy:"
  echo "$output"
  exit 1
fi
log "module files tidy"

# ---------------------------------------------------------------- vet

if ! output="$(go vet ./... 2>&1)"; then
  echo "::error::go vet failed:"
  echo "$output"
  exit 1
fi
log "go vet clean"

# ------------------------------------------------- linked version metadata
#
# The ldflags in .goreleaser.yaml write to these symbols by name. A rename on
# the Go side would make the linker silently do nothing, producing a binary
# that reports "dev" while claiming to be a release. So the names are checked
# against the source rather than trusted.

for symbol in Version Commit Date BuiltBy; do
  if ! grep -qE "^	${symbol} = " cmd/lensyxe/version.go; then
    echo "::error::cmd/lensyxe/version.go no longer declares '${symbol}'; the ldflags in .goreleaser.yaml would silently stop working"
    exit 1
  fi
done
log "version symbols match the ldflags"

log "release preparation complete"