#!/usr/bin/env bash
# Builds the macOS .app bundle and a drag-to-Applications .dmg.
#
# Run from the repository root, on macOS, with the binaries staged:
#
#   build/desktop/mac/stage/lensyxe
#   build/desktop/mac/stage/lensyxe-gui
#   build/desktop/mac/stage/LICENSE
#   build/desktop/mac/stage/README.md
#
# CI assembles that directory by extracting the release archives. Depending on
# goreleaser's own layout instead would tie this script to a template string:
# renaming the archive breaks the build with no useful error.
#
# Why this is a script and not a goreleaser target: goreleaser's dmg target
# cannot produce a *signed* disk image, and an unsigned .dmg that Gatekeeper
# blocks is a worse first impression than a documented two-step install. Signing
# needs an Apple Developer identity and the `codesign` and `notarytool` binaries,
# which only exist on macOS with Xcode installed. So:
#
#   - goreleaser ships the plain binaries inside the archives (works anywhere).
#   - This script assembles and signs the .dmg on a macOS runner.
#
# The script degrades honestly: with no signing identity it still produces a
# working .dmg and says loudly that it is unsigned, rather than failing and
# leaving no artifact at all.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$REPO_ROOT"

APP_NAME="Lensyxe"
VERSION="${VERSION:-$(git describe --tags --abbrev=0 2>/dev/null || echo dev)}"
STAGE="${STAGE:-build/desktop/mac/stage}"
BUILD_DIR="${BUILD_DIR:-build/desktop/mac/out}"
APP_BUNDLE="$BUILD_DIR/$APP_NAME.app"
CONTENTS="$APP_BUNDLE/Contents"

BIN_SRC="$STAGE/lensyxe"
WIZARD_SRC="$STAGE/lensyxe-gui"

log()  { printf '  %s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

# ---- preflight ---------------------------------------------------------------

[[ "$(uname -s)" == "Darwin" ]] || fail "a .dmg must be built on macOS; this is $(uname -s)"
command -v hdiutil >/dev/null || fail "hdiutil not found"

if [[ ! -f "$BIN_SRC" ]]; then
  fail "binary not found at $BIN_SRC (stage it before running this script)"
fi
if [[ ! -f "$WIZARD_SRC" ]]; then
  fail "setup wizard not found at $WIZARD_SRC (stage it before running this script)"
fi

log "version $VERSION"

# ---- bundle ------------------------------------------------------------------

rm -rf "$BUILD_DIR"
mkdir -p "$CONTENTS/MacOS" "$CONTENTS/Resources"

cp "$BIN_SRC"   "$CONTENTS/MacOS/lensyxe"
cp "$WIZARD_SRC" "$CONTENTS/MacOS/lensyxe-gui"
chmod +x "$CONTENTS/MacOS/"*

cp LICENSE "$CONTENTS/Resources/LICENSE"
cp README.md "$CONTENTS/Resources/README.md"

# Info.plist is written by hand rather than templated. It is a dozen keys, and the
# alternative is a plist generator plus its dependencies.
cat > "$CONTENTS/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>${APP_NAME}</string>
  <key>CFBundleDisplayName</key><string>${APP_NAME}</string>
  <key>CFBundleIdentifier</key><string>dev.lensyxe.setup</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>CFBundleShortVersionString</key><string>${VERSION}</string>
  <key>CFBundleExecutable</key><string>lensyxe-gui</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSHighResolutionCapable</key><true/>
  <!-- The setup wizard is the bundle's face. It is a terminal program, so it has
       no window class and no dock icon of its own beyond this one. -->
  <key>LSUIElement</key><false/>
</dict>
</plist>
PLIST

# ---- signing -----------------------------------------------------------------

# Ad-hoc signing always runs: without it macOS refuses to run an unsigned bundle
# at all on Apple Silicon. A Developer ID signature is better and is applied when
# an identity is available.
if command -v codesign >/dev/null; then
  log "ad-hoc signing (required on Apple Silicon)"
  codesign --force --deep --sign - "$APP_BUNDLE" 2>/dev/null \
    || log "warning: ad-hoc signing failed; the bundle may not run on Apple Silicon"

  if [[ -n "${SIGN_IDENTITY:-}" ]]; then
    log "signing with $SIGN_IDENTITY"
    # --options runtime enables the hardened runtime, which notarization requires.
    codesign --force --options runtime --timestamp \
      --sign "$SIGN_IDENTITY" "$APP_BUNDLE"
    if [[ -n "${NOTARY_PROFILE:-}" ]]; then
      log "notarizing"
      xcrun notarytool submit "$APP_BUNDLE" --keychain-profile "$NOTARY_PROFILE" --wait
      xcrun stapler staple "$APP_BUNDLE"
    else
      log "NOT notarized: set NOTARY_PROFILE to a notarytool keychain profile"
    fi
  else
    log "unsigned: set SIGN_IDENTITY to a Developer ID to sign and notarize"
  fi
fi

# ---- dmg ---------------------------------------------------------------------

STAGING="$BUILD_DIR/staging"
mkdir -p "$STAGING"
cp -R "$APP_BUNDLE" "$STAGING/"

# The /Applications symlink is what makes the window a real drag-to-install.
ln -s /Applications "$STAGING/Applications"

DMG_PATH="$BUILD_DIR/${APP_NAME}-${VERSION}.dmg"
rm -f "$DMG_PATH"

hdiutil create \
  -volname "$APP_NAME $VERSION" \
  -srcfolder "$STAGING" \
  -ov -format UDZO \
  -fs HFS+ \
  "$DMG_PATH"

if command -v codesign >/dev/null && [[ -n "${SIGN_IDENTITY:-}" ]]; then
  codesign --force --sign "$SIGN_IDENTITY" "$DMG_PATH"
fi

rm -rf "$STAGING"

log "built $DMG_PATH"
log "unsplash: move ${APP_NAME}.app to /Applications, then run"
log "         /Applications/${APP_NAME}.app/Contents/MacOS/lensyxe setup --headless --write"