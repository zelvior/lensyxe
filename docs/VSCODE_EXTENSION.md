# Lensyxe for VS Code

Surfaces Lensyxe's deterministic findings where they are cheap to act on: in the
editor, at the moment of the change, rather than three days later in a pull
request comment.

The extension is a viewer. It spawns `lensyxe analyze --format json`, parses the
snapshot, and renders it. It contains no scoring, no thresholds, and no analysis
logic of its own — so the score in the status bar, the squiggle on a file, and
`lensyxe analyze` in a terminal cannot disagree.

---

## Table of contents

- [Requirements](#requirements)
- [Build from source](#build-from-source)
- [Install the packaged extension](#install-the-packaged-extension)
- [What you get](#what-you-get)
- [Settings](#settings)
- [Commands](#commands)
- [How it stays responsive](#how-it-stays-responsive)
- [Architecture](#architecture)
- [Testing](#testing)
- [Design decisions worth arguing with](#design-decisions-worth-arguing-with)
- [Troubleshooting](#troubleshooting)

---

## Requirements

| | |
| :--- | :--- |
| VS Code | 1.90 or newer |
| Node.js | 18 or newer, to build |
| The `lensyxe` CLI | On `PATH`, or an absolute path in the settings |

The CLI is a separate binary by design. The extension does not embed it, does not
download it, and does not shell out to anything else.

---

## Build from source

```bash
git clone https://github.com/zelvior/lensyxe.git
cd lensyxe/ide/vscode

npm install          # ~301 packages, no native builds
npm run compile      # tsc, emits ./out
```

`npm run compile` completes with no TypeScript errors. The configuration is
strict — `strict`, `noUnusedLocals`, `noUnusedParameters`, `noImplicitReturns`,
`noUncheckedIndexedAccess` — because this code runs inside the editor's shared
process and a latent undefined is a crash in someone else's typing session.

To try it without packaging:

```bash
code --extensionDevelopmentPath=/path/to/lensyxe/ide/vscode
```

---

## Install the packaged extension

```bash
cd lensyxe/ide/vscode
npm run package
```

That produces `lensyxe-vscode-0.1.0.vsix`, about 22 KB across 12 files. Install
it:

```bash
code --install-extension lensyxe-vscode-0.1.0.vsix
```

The package contains only what VS Code loads: the compiled `out/**/*.js`, the two
icon files, the manifest, the README, and the license. `.vscodeignore` is a deny
list — worth knowing if you add a build artifact and find it missing from the
VSIX, because an allow list here silently drops the entrypoint instead.

To uninstall:

```bash
code --uninstall-extension zelvior.lensyxe-vscode
```

The extension id is `zelvior.lensyxe-vscode`, from `publisher` and `name` in
`package.json`.

---

## What you get

### Status bar

```text
$(pulse) Lensyxe: 87/100
```

Shows the overall Engineering Health Score. The exact figure, the grade, and how
long the analysis took are in the tooltip. Click it to re-run.

Colours follow the CLI's own grade boundaries, so the sidebar and the terminal
never disagree about what counts as healthy.

### Sidebar: Engineering Health

The activity bar gains a **Lensyxe** icon. The tree is driven by
`health.metrics` from the snapshot — the CLI's own list, not a hardcoded one, so
adding a dimension to the scorer makes it appear here without touching the
extension.

```text
87.4 / 100  ·  grade B                       ████████░░
├─ Code health                               82.5  40%
│  ├─ Files                     32 source / 8 test
│  ├─ Largest file                           900 LOC
│  └─ internal/engine.go        900 LOC · cx 31.0 · confirmed
├─ Dependency health                         94.0  30%
│  ├─ Locked                       locked (go.sum)
│  └─ npm                             no lockfile
├─ Maintainability (Git)                       -     -
│  └─ Git history          not measured
├─ (risks, worst first)
└─ Not measured
   ├─ Code coverage
   ├─ Build duration
   └─ Vulnerabilities
```

Hotspot and risk rows open the file when clicked.

### Diagnostics

With `lensyxe.enableInlineDiagnostics` on, findings appear in the Problems panel
and as inline warnings:

- **Hotspot candidate / confirmed hotspot** — the CLI's own rationale, verbatim,
  including which factors it had and which it was missing.
- **Complexity** — estimated cyclomatic complexity, function and branch-point
  counts, and nesting depth.
- **File-scoped risks** — severity mapped to Error / Warning / Information.

One diagnostic per file, not one per finding, and anchored to the first line. A
900-line file that is both large and complex gets one entry carrying both
reasons, rather than two squiggles on the same line saying the same thing.

---

## Settings

| Setting | Default | Purpose |
| :--- | :--- | :--- |
| `lensyxe.executablePath` | `lensyxe` | Path to the binary. A bare name is resolved on `PATH`. |
| `lensyxe.enableInlineDiagnostics` | `true` | Editor diagnostics. Turning it off leaves the status bar and sidebar untouched. |
| `lensyxe.runOnSave` | `true` | Re-analyze after a save. |
| `lensyxe.debounceMs` | `750` | Coalescing window for rapid events. Mirrors the CLI's watcher. |
| `lensyxe.analysisTimeoutMs` | `60000` | Abort the CLI after this long. |
| `lensyxe.complexityWarningThreshold` | `10` | Complexity at which a file is reported. Matches the `moderate` band. |
| `lensyxe.showStatusBar` | `true` | Show the score in the status bar. |

There is no bespoke logging toggle. The extension logs to a `LogOutputChannel`, so
**Output: Log Level** in VS Code's settings controls verbosity the way it does for
every other extension. A project-specific switch would have been a second control
for the same thing.

---

## Commands

| Command | Palette name | What it does |
| :--- | :--- | :--- |
| `lensyxe.analyze` | Lensyxe: Analyze Repository | Re-runs the analysis now. |
| `lensyxe.openDashboard` | Lensyxe: Open Local Dashboard | Runs `lensyxe serve --open`, which binds loopback only. |
| `lensyxe.showHealthDetail` | Lensyxe: Show Health Breakdown | Prints the score table to the output channel. |

---

## How it stays responsive

Editor lag is the failure mode that gets an extension uninstalled, so the
scheduling is explicit about it.

**Activation never blocks.** The extension is activated on `onStartupFinished`,
not `*`, and the first analysis is fired without being awaited. Activation
returns immediately; a cold binary on a large monorepo takes seconds, and the
editor is unusable until activation completes.

**Newer runs supersede older ones.** They are never queued. Saving ten files
produces ten events, and running ten analyses would leave the sidebar showing
stale results long after the last save. An in-flight run is aborted by the next
one.

**Supersession is silent.** An aborted run is not an error. This one was a real
bug before it was a design: aborting a spawned child makes Node emit an `error`
event *before* `close`, so a naive implementation reports every superseded run as
"Could not start lensyxe: The operation was aborted" — an error popup on every
single save. `scripts/smoke.js` now asserts it.

**One subprocess per analysis.** Nothing spawns per keystroke, per file, or per
rendered row. The snapshot is fetched once and everything else is fed from it.

**Nothing blocks the extension host.** All parsing and rendering is synchronous
work on data already in memory, sized by the snapshot rather than by the
repository.

**Output is capped.** A very large monorepo can produce tens of megabytes of
JSON. The buffer has a hard ceiling and fails with an actionable message rather
than exhausting the extension host.

**Analysis does not persist.** `--no-persist` is always passed. An editor that
wrote to your repository's history database on every save would be a surprise,
and the dashboard reads history only when you ask it to.

---

## The icon

`media/icon.png` is generated, not drawn by hand:

```bash
npm run icon   # go run scripts/gen-icon.go
```

The generator draws the same pulse glyph as `activity-bar.svg` with no
third-party rasterizer, so the two stay in step and the icon can be rebuilt
after any change to the shape. It is committed because a marketplace listing
needs a file, and regenerating it on every build would make the repository the
wrong place to review it.

---

## Architecture

```text
src/
├── extension.ts                        activation, commands, scheduling
├── lensyxe.ts                          the CLI client: spawn, parse, classify
├── types.ts                            the JSON contract, mirroring the Go types
└── providers/
    ├── HealthTreeProvider.ts           the sidebar tree
    └── DiagnosticsProvider.ts          the Problems panel

scripts/
├── smoke.js                            drives the compiled client against a real binary
├── gen-icon.go                         renders media/icon.png
└── check-manifest-docs.go              asserts package.json and these docs agree
```

`lensyxe.ts` and `types.ts` import nothing from `vscode`. That is deliberate: it
is what lets the CLI client be exercised by a plain Node script against the real
binary, which is how the supersession and timeout bugs were found.

The provider classes hold no subprocess logic and the CLI client holds no UI
logic, so neither can be tested only through the other.

**Why this directory carries a `go.mod`.** It is a module boundary, not a Go
module, and there is no Go here. Without it, `go build ./...` walks
`node_modules/` — and at least one popular npm dependency ships Go source in its
package. The Lensyxe module would then compile a transitive JavaScript
dependency's code, so an unrelated npm update or a Go version bump could break
the project's build. The boundary stops the parent module at this directory.

---

## Testing

```bash
npm run compile
npm run smoke -- /path/to/lensyxe /path/to/a/repository
```

`npm run smoke` drives the compiled CLI client against a real `lensyxe` binary
and checks:

- the snapshot parses and carries `health.score` and `health.metrics`
- the root echoes back, so the snapshot is the one that was asked for
- **two runs agree** — the extension inherits the CLI's determinism guarantee, and
  this is where a violation would show up
- a missing binary produces an actionable message rather than a raw `ENOENT`
- a superseded run stays silent
- a timeout is distinguishable from a supersession, because only one is worth
  interrupting the user for

Run it against `examples/risky-go` for a repository that trips every path.

`check-manifest-docs.go` guards the other direction: that this document and
`package.json` still describe the same extension. It reports a setting this
document lists but the manifest does not declare, a declared setting that is
undocumented, a documented default that disagrees with the manifest, a command
whose palette title appears nowhere, and a missing icon or entrypoint. A settings
table is exactly the kind of thing that drifts when only one of the two files is
edited, and nothing in the Go build reads either.

```bash
go run ide/vscode/scripts/check-manifest-docs.go
```

Both run in CI, along with `npm run compile`, `npm run icon`, and the smoke test.

---

## Design decisions worth arguing with

**Not-applicable is never shown as zero.** A directory that is not a git
repository has no cadence, which says nothing bad about the code. The sidebar
shows `not measured` and the weight is described as redistributed, because that
is what happened. Drawing it as `0.0` would invent a measurement and would drag
the apparent score down for a blind spot.

**"Not measured" is listed, not omitted.** Code coverage, build duration, and
vulnerability counts appear at the bottom of the tree with the reason. The most
common question about a health score is "why is there no coverage number", and
answering it with silence reads as an oversight. Listing them also keeps the
extension from implying the tool measures something it does not — see
[SCORING_SPEC.md](../docs/SCORING_SPEC.md#what-is-not-measured).

**The extension never scores.** The tree is built from `health.metrics`, not from
a copy of the dimensions. That is what makes "the status bar and the terminal
agree" a structural property rather than a promise.

**Complexity findings above the threshold are Warning only for `high` and
`very_high`.** A `moderate` file is Information unless you have deliberately
lowered `complexityWarningThreshold`. Making everything a Warning is how a
diagnostic provider gets ignored.

**The dashboard is launched, not embedded.** The extension runs
`lensyxe serve --open` and lets the CLI open the browser. Reimplementing a server
in the extension host would mean the dashboard runs wherever the editor runs,
which is the opposite of the intent.

---

## Troubleshooting

**"Cannot find the lensyxe binary"**

The CLI is not on the editor's `PATH`. VS Code inherits `PATH` from how it was
launched, so a terminal that has it is not sufficient — a GUI launch on macOS
often does not. Set an absolute path:

```json
{ "lensyxe.executablePath": "/usr/local/bin/lensyxe" }
```

**"The analysis output was not a Lensyxe snapshot"**

`executablePath` points at something else. The client validates that
`health.score` and `health.metrics` exist precisely so this produces a clear
message instead of a confusing `undefined` later.

**The score is stale**

A save in `node_modules`, `.git`, `vendor`, `dist`, `out`, or `.next` does not
trigger a re-analysis, because the analyzer ignores those directories by default.
Run `Lensyxe: Analyze Repository` explicitly.

**Nothing happens on save**

Check `lensyxe.runOnSave` is on, and that the first analysis succeeded — the
sidebar shows the error inline rather than as a popup. `Lensyxe: Show Health
Breakdown` and the output channel carry the detail.

**Analysis is slow on a large monorepo**

Most of an end-to-end run is git subprocess spawn cost, not analysis; the code
walk over a 40-file tree is about 26 ms of a roughly 700 ms run. Raise
`lensyxe.debounceMs` to trade freshness for fewer runs, or use `.lensyxe.yml`
`ignore_dirs` to shrink the tree.

---

## Related

- [docs/CLI_REFERENCE.md](../docs/CLI_REFERENCE.md)
- [docs/SCORING_SPEC.md](../docs/SCORING_SPEC.md)
- [docs/CONFIGURATION.md](../docs/CONFIGURATION.md)
- [SECURITY.md](../SECURITY.md) — the extension adds no network access of its own
- [CONTRIBUTING.md](../CONTRIBUTING.md)