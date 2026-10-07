# Distribution

How Lensyxe gets onto a machine, and why each route is shaped the way it is.

## The constraint everything else follows from

The Go build is **pure, with `CGO_ENABLED=0`**, and that is not an optimisation —
it is what lets one runner cross-compile all six platform/architecture
combinations with no C toolchain installed. `modernc.org/sqlite` was chosen over
`mattn/go-sqlite3` for exactly this reason.

Any component that needs cgo, a webview, or an OpenGL toolkit breaks that matrix.
That is the single reason the setup wizard is a terminal program rather than a
window, and the reason the repository ships ten direct dependencies rather than
several hundred.

## Four routes, in order of least friction

| Route | Best for |
| :--- | :--- |
| `curl … \| bash` | Everything. No privileges, no package manager, one file. |
| `go install github.com/zelvior/lensyxe/cmd/lensyxe@latest` | Go developers. |
| Native package (`.deb`, `.rpm`, `Setup.exe`) | Machines managed by a package manager. |
| `lensyxe-gui` | First-time users who want a guided install. |

The archive with a `checksums.txt` is the path of least resistance and the one
every other route is verified against. The packages carry the same binary.

## The setup wizard

```
lensyxe-gui            # guided, six steps, in the terminal
lensyxe setup --write  # derive a config from this repository, non-interactive
lensyxe setup --headless --write   # servers and containers
lensyxe installer --dry-run       # report PATH changes without applying them
lensyxe installer --remove        # undo them
```

The wizard and `lensyxe setup` share one step model in `internal/gui`, so they
cannot disagree about what an install does. `--headless` **requires `--write`**:
with nobody to read a proposal, setup must write the file or do nothing.

Everything added to a shell startup file is wrapped in sentinels:

```
# >>> lensyxe >>>
export PATH="/home/u/.local/bin:$PATH"
# <<< lensyxe <<<
```

so removal is exact rather than best-effort. An install-then-remove cycle
restores the original file byte for byte, including the separating blank line.
An entry you wrote yourself is never duplicated, and an interrupted install
leaves nothing unrecoverable.

**Windows PATH is not set with `setx`.** `setx PATH` truncates its value at 1024
characters and reports success while doing it, silently discarding the rest. The
generated command uses the .NET environment API and is *printed* rather than
executed, so you can read it before it runs.

## Packages

`.deb` and `.rpm` are built by goreleaser's `nfpms` target, alongside the
archives. They carry the binary, the man page, and a desktop entry.

**No distribution repository is configured.** Nothing is pushed anywhere. That is
deliberate and reverses an earlier decision recorded in `.goreleaser.yaml`, which
held that a stale package is worse than no package — users would install an old
version from a repository instead of a checksummed release. That risk is real, and
it is avoided rather than argued away: a package that exists only on the release
page cannot go stale in a repository, because there is no repository.

The `.deb` has **no maintainer scripts**. A `postinst` that rewrites PATH is how a
package manager ends up fighting an installer over the same file.
`lensyxe installer --yes` is the sanctioned way to do that, and it is reversible.

## What is built where

| Artifact | Built by | Where |
| :--- | :--- | :--- |
| Six binaries, archives, checksums | goreleaser | any runner |
| `.deb`, `.rpm` | goreleaser `nfpms` | any runner |
| `LensyxeSetup-{amd64,arm64}.exe` | `build/desktop/win/nsis.nsi` | Windows runner (makensis) |
| `Lensyxe-{darwin-amd64,darwin-arm64}.dmg` | `build/desktop/mac/build.sh` | macOS runner (hdiutil, codesign) |

Neither installer is a goreleaser target. NSIS exists only on Windows, and
`hdiutil` and `codesign` only on macOS, so they cannot run on the Linux runner
that builds everything else. An installer produced on the wrong platform would
also be the wrong artifact to test on.

Both jobs download the `dist` artifact from the `build` job rather than
rebuilding. An installer therefore always ships the exact binary that
`checksums.txt` covers — a rebuilt binary would be a different artifact under the
same name, which is precisely what makes a checksum useless.

The archives are named with the architecture (`LensyxeSetup-amd64.exe`,
`Lensyxe-darwin-arm64.dmg`) rather than shipping four files called
`LensyxeSetup.exe`.

### Neither installer is signed

This is stated plainly because an unsigned artifact that fails is worse than one
labelled unsigned:

- **Windows.** SmartScreen reports an "unknown publisher" on first run. That is
  the absence of a code-signing certificate, not a corrupt binary.
- **macOS.** Gatekeeper blocks the `.dmg` on first open. Right-click → Open works
  once, or:

  ```bash
  xattr -dr com.apple.quarantine Lensyxe-darwin-arm64.dmg
  ```

Both jobs read `MACOS_SIGN_IDENTITY` and `MACOS_NOTARY_PROFILE` secrets if they
are configured. With an identity present the disk image is signed, the hardened
runtime is enabled, and it is notarised through `notarytool`. With none
configured the ad-hoc signature is applied — which macOS still requires on Apple
Silicon — and the job summary says the image is unsigned.

**There is no goreleaser `dmg` target, on purpose.** goreleaser cannot sign a disk
image, and an unsigned `.dmg` is blocked by Gatekeeper on current macOS — shipping
one from goreleaser would produce an artifact that looks *broken* rather than one
that looks unsigned. `build/desktop/mac/build.sh` assembles and signs it on a
macOS runner instead.

### A failed installer does not block the release

The six binaries and their checksums are published by the `build` job
regardless. An `installer-summary` job runs with `if: always()` and reports which
installer artifacts exist, so a failure is discoverable in the Actions log rather
than only by noticing something missing on the release page. A missing installer
degrades to the documented two-step install; it does not block the binaries.

## Uninstalling

Every route can be undone:

| Platform | Command |
| :--- | :--- |
| Any (shell) | `lensyxe installer --remove --yes` |
| Windows | The uninstaller, or the Start Menu entry |
| macOS | Delete the app from `/Applications` |
| `.deb` | `apt remove lensyxe` |
| `.rpm` | `rpm -e lensyxe` |

Your `.lensyxe.yml` is never touched by any of them. It holds your decisions, and
no installer is entitled to delete it. The history database under
`~/.config/lensyxe/` is left alone for the same reason; remove it yourself if you
want the space back.