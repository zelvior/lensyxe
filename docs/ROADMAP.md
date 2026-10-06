# Roadmap

Where Lensyxe is going, and why. This is a statement of intent, not a schedule.
Items are ordered by dependency, not by date, and nothing here is committed to
shipping in a particular release.

---

## Table of contents

- [Principles that constrain the roadmap](#principles-that-constrain-the-roadmap)
- [Phase status](#phase-status)
- [Core Engine](#core-engine)
- [Local SQLite Storage](#local-sqlite-storage)
- [GitHub Action](#github-action)
- [Embedded Dashboard](#embedded-dashboard)
- [Bring-your-own-key AI](#bring-your-own-key-ai)
- [IDE Plugins](#ide-plugins)
- [Explicitly out of scope](#explicitly-out-of-scope)

---

## Principles that constrain the roadmap

Four rules decide which proposals get accepted. They are listed first because
they explain more rejections than any feature list does.

1. **Never claim a measurement that was not taken.** A metric that cannot be
   computed is reported as not applicable and excluded from the weighted score.
   It is never defaulted, never guessed, and never named in a way that implies
   more than it delivers. This is why there is no coverage number, no build
   timing, and no vulnerability feed.

2. **Determinism is a feature.** The same commit must produce the same output on
   every machine. Every sort has a total ordering; nothing time-dependent or
   map-ordered reaches the output. A metric that cannot be computed
   deterministically does not ship.

3. **Local-first, no required network.** Analysis never leaves the machine.
   There is no telemetry, no update check, and no remote configuration. A
   feature that would need a server is not a feature of this tool.

4. **Evidence over verdicts.** A finding names the file, the measurement, and
   the threshold it crossed. "Code health is 62" is not useful; "`legacy/order.go`
   is 910 lines with churn 0 and complexity moderate, so it is a candidate, not a
   confirmed hotspot" is.

---

## Phase status

| Phase | Capability | State |
| :--- | :--- | :--- |
| 1 | Core engine: walk, LOC, languages, complexity | Shipped |
| 2 | Scoring and risk model | Shipped |
| 3 | History and comparison | Shipped |
| 4 | SQLite persistence and trends | Shipped |
| 5 | Thresholds and CI gates | Shipped |
| 6 | GitHub Action and workflow analysis | Shipped |
| 7 | Monorepo detection and per-package scoring | Shipped |
| 8 | Bring-your-own-key explanation | Shipped |
| 9 | Release pipeline and installer | Shipped |
| 10 | Documentation | Shipped |
| 11 | Test suite: unit, golden, benchmark, integration | Shipped |
| 12 | IDE plugins | Planned |
| 13 | Language server protocol surface | Planned |

---

## Core Engine

Shipped, with deliberate gaps that are documented rather than papered over.

### Done

- Filesystem walk with a configurable ignore list. `ignore_dirs` **replaces** the
  defaults rather than extending them, so an entry can be dropped. The cost is
  having to resync on upgrade, which is the honest trade.
- Line counting that separates physical, code, blank, and comment lines.
- Language detection across a broad extension set.
- Lexical complexity estimation with nesting depth, limited to control-flow
  languages. YAML and SQL are excluded because their conditionals are mapping
  keys, not branches.
- Hotspot detection requiring size, churn, and complexity together.
- `ignore_dirs`, `hotspot_threshold`, `enable_complexity`, `git_window_days`.

### Planned

- **More precise complexity estimation** for languages with an off-the-shelf
  parser available in pure Go. The current lexical estimator counts branch
  keywords, which cannot see through a helper function or a higher-order
  construct. This is the largest known accuracy gap and it is a gap in the
  *documented* direction: the spec says "estimated", not "measured".
- **Generated and vendored code detection** that does not rely purely on path.
  Currently handled by directory names alone, so a `generated.go` in `internal/`
  is counted as source.
- **Diff-aware analysis**: score only what a pull request touched. The signals
  exist; the scoping does not.

### Explicitly not planned

- A "code quality score" that cannot be traced to specific files and
  measurements.
- Any metric derived from network calls. See principle 3.

---

## Local SQLite Storage

Shipped.

### Done

- History database at `.lensyxe/history.db`, written beside the analyzed tree.
- Pure-Go SQLite via `modernc.org/sqlite`, so `CGO_ENABLED=0` cross-compiles to
  every target in the release matrix without a C toolchain.
- Trend reporting over the stored timeline.
- Baseline snapshots for delta reporting.

### Planned

- **Retention policy.** History currently grows without bound. A configurable
  retention window with automatic pruning is needed before this is safe to run
  on a large repository for years.
- **Schema migration on read.** The database records a schema version, and a
  mismatch is detected. Converting an older database in place, rather than
  requiring it to be deleted, is the missing piece.
- **Export to JSON Lines** for streaming into an external time-series store,
  keeping the database the source of truth and the export the interface.

### Explicitly not planned

- A hosted sync service. A tool that promises local-first analysis and then
  requires an account is a different product.
- Telemetry written to the local database. Local storage is not a loophole.

---

## GitHub Action

Shipped.

### Done

- Composite action at `zelvior/lensyxe-action`, posting or updating a pull
  request comment.
- Installs through `install.sh` from the resolved tag, inheriting checksum
  verification. A private copy of the download logic is exactly how it
  previously ended up with a wrong URL and no integrity check.
- Exit-code contract wired to workflow failure.
- Workflow analysis, inspecting GitHub Actions definitions themselves.
- Version pinning with cache keys including version, OS, and architecture.

### Planned

- **Matrix reporting**, so a multi-repository workspace gets one summary rather
  than a comment per package.
- **A diff-scoped gate**, running the full analysis once and then applying
  thresholds to the pull request's touched files. Cheaper than today and far less
  noisy.
- **Self-hosted runner support**, verified against the platform matrix rather
  than assumed.
- **Signed provenance** using the GitHub artifact attestations API, exposed as
  an input so a regulated environment can require verification.

### Explicitly not planned

- Reading `GITHUB_TOKEN` inside the CLI. The Action posts the comment using a
  token it already holds; the CLI loading a credential nothing consumes only
  widens the blast radius.
- A marketplace-hosted action under an org the maintainer does not control.

---

## Embedded Dashboard

Shipped.

### Done

- Embedded in the binary through `//go:embed`, served by `lensyxe serve`.
- Read-only JSON API for health, risks, hotspots, history, and version.
- Loopback-only binding; every endpoint read-only.
- No third-party script loaded from a CDN. A CDN is an outbound request and a
  supply-chain dependency, and this tool's premise is that neither exists.

### Planned

- **Trend charts** over the stored history, which the API already exposes and
  the UI does not yet draw.
- **A risk-detail view** linking each finding to the file and the measurement
  that produced it, following principle 4.
- **Offline-first asset pipeline**, so the dashboard build is reproducible from a
  clean checkout with a pinned toolchain.
- **Per-package drill-down** for monorepos, from the workspace data already in
  the snapshot.

### Explicitly not planned

- A hosted dashboard. That would require uploading source metadata, which
  principle 3 forbids.
- A web UI that phones home for anything, including a font. The dashboard uses
  system fonts.

---

## Bring-your-own-key AI

Shipped, deliberately constrained.

### Done

- `--explain` summarizing findings that have already been computed.
- Key read from an environment variable *name*, never stored in config, because
  config files get committed, printed, and serialized into snapshots.
- A fabrication guard: a reply containing figures absent from the input is
  **discarded** rather than parsed. There is no code path by which a model can
  influence a score.
- Providers: OpenRouter, OpenAI, and Gemini.

### Planned

- **Richer summaries** of the same deterministic findings, still with no ability
  to state a number.
- **Per-finding explanation** rather than one summary per report, so a reviewer
  can read the reasoning for the finding they are looking at.
- **Streaming output** with an explicit "model is responding" marker, so it is
  never confused with deterministic output.

### Explicitly not planned

- **Any scoring, ranking, or recommendation from a model.** The guard is
  structural, and it stays structural.
- **A hosted inference service.** There is no Lensyxe-in-the-cloud, by design.
- **Training on user repositories**, for any purpose, ever.
- **A plugin system for third-party models** that could route requests somewhere
  the user did not intend.

---

## IDE Plugins

Planned. This is the phase most likely to change shape, because the value is in
the feedback loop, not in the surface area.

### Design intent

The editor plugin should make Lensyxe's deterministic findings visible *at the
moment of editing*, which is where a finding is cheap to fix. A comment left on
a pull request three days later is worth less than the same finding shown on the
line that caused it.

### Planned

- **Editor diagnostics** from the risk engine, mapped onto the files that
  triggered them.
- **Inline hotspot and complexity annotations**, refreshed incrementally.
- **A Language Server Protocol server**, so any LSP-capable editor gets the
  diagnostics without a bespoke plugin.
- **Status-bar health for the workspace**, updating on save.
- **Lightbulb quick fixes** for the mechanical findings, such as splitting an
  oversized file. Only where the fix is mechanical and unambiguous.

### Constraints

- **Deterministic output only.** The plugin shows what Lensyxe measured. It must
  not reimplement scoring, and it must not add heuristics of its own, because
  then two surfaces would disagree about the same file.
- **Local-first.** A plugin that requires an account or a running server defeats
  the purpose. The LSP server runs in the editor's process.
- **Optional.** Absence of the plugin never changes what `lensyxe analyze`
  reports.

### Sequencing

LSP first, editor plugins second. One protocol serving every editor beats N
bespoke integrations, and it is the smaller surface to keep correct.

---

## Explicitly out of scope

Collected here so that these are settled questions rather than recurring ones.

| Request | Why not |
| :--- | :--- |
| Code coverage percentage | Not measurable without executing the test suite. A test-to-code **file ratio** is provided and never called coverage. |
| Vulnerability scanning | Requires a vulnerability database and network access. Both violate the premises. |
| Build-time measurement | The tool does not execute your build. Timing it would mean becoming a build tool. |
| Automatic fixes by a model | The only automated fixes are mechanical ones in the editor plugin. Anything requiring judgement is left to you. |
| Hosted dashboard or sync | Requires uploading source metadata. |
| Telemetry of any kind | There is no opt-out because there is nothing to opt out of. |
| Team leaderboards | Turns a diagnostic tool into a surveillance tool. |
| Arbitrary user-defined scoring formulas | Would make the score uninterpretable and the spec unmaintainable. |
| Linting rules | Use a linter. Lensyxe measures properties a linter cannot: churn-weighted hotspots, maintainability, and dependency drift. |

---

## Related documents

- [CHANGELOG.md](../CHANGELOG.md) — what actually shipped
- [docs/SCORING_SPEC.md](SCORING_SPEC.md) — the formulas, exactly
- [docs/CONFIGURATION.md](CONFIGURATION.md) — every configuration key
- [docs/CLI_REFERENCE.md](CLI_REFERENCE.md) — every command and flag
- [SECURITY.md](../SECURITY.md) — the local-first guarantees
- [CONTRIBUTING.md](../CONTRIBUTING.md) — architecture and contribution rules