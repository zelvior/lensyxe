# Changelog

All notable changes to Lensyxe are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## About the entries below

Dates are written by hand at release time. An earlier version of this file
claimed the release automation recorded them; nothing in `scripts/` or either
workflow does that, so the claim was removed rather than left standing.

---

## [Unreleased]

### Added

- **`lensyxe-gui`** — a six-step setup wizard: welcome and license, install
  path, shell integration, background server, config generation, and a review
  before anything is applied. It renders in the terminal and takes **no
  third-party dependencies**.
- **`lensyxe installer`** — PATH registration, shell startup files, and a
  freedesktop.org launcher, with `--remove` to undo all of it.
- **`lensyxe setup --headless`** — non-interactive setup for servers and
  containers. Requires `--write`, because with nobody to read a proposal setup
  must write the file or do nothing.
- **Native packages** — `.deb` and `.rpm` via goreleaser's `nfpms` target, and a
  Windows `LensyxeSetup.exe` from `build/desktop/win/nsis.nsi`.
- **`docs/DISTRIBUTION.md`** — the four install routes and why each is shaped the
  way it is.

### Why the wizard is not a window

Fyne and Wails both conflict with this project's published guarantee of
zero-dependency installation. Either would take `go.mod` from ten direct
dependencies to hundreds, require cgo and a native toolchain on every one of the
six platforms in the release matrix, and add a Node toolchain to the pipeline —
which would break the `CGO_ENABLED=0` cross-compile that lets one runner produce
all six binaries.

So `internal/gui` holds the platform-independent step model with no rendering and
no platform calls, and both frontends render it: `lensyxe-gui` as a terminal
program, `lensyxe setup --headless` for machines with no terminal. They share one
model, so they cannot disagree about what an install does. A native frontend added
later is a shell around these decisions rather than a second implementation of
them.

### Every change is reversible and marked

Shell startup entries are wrapped in sentinels, so removal is exact rather than
best-effort. A test asserts that install-then-remove restores the original file
**byte for byte**, including the blank line separating the block — otherwise
repeated cycles leave a growing run of empty lines. Installing twice adds nothing,
an entry the user wrote themselves is never duplicated, and an install
interrupted between the sentinels is still removable.

**Windows PATH is not set with `setx PATH`.** `setx` truncates its value at 1024
characters and reports success while doing it, silently discarding everything
past that point. The generated command uses the .NET environment API, which has no
such limit, and it is printed rather than executed so it can be read before it
runs.

### The plan is shown before anything happens

`BuildPlan` computes the full set of actions — including what it would modify, what
it cannot undo, and what needs privileges — from a `Choices` value and an `Env`
probe. Nothing is written until the user confirms. A test asserts that planning
does not touch the filesystem, so a dry run is a real code path rather than a
promise.

### Where this reverses an earlier decision

`.goreleaser.yaml` previously recorded a decision against `.deb` and `.rpm`,
holding that a stale package is worse than no package because users would install
an old version from a repository instead of a checksummed release. That is still
true, and it is why **no distribution repository is configured**: a package that
exists only on the release page cannot go stale in a repository, because there is
no repository. The `.deb` also carries no maintainer scripts — a `postinst` that
rewrites PATH is how a package manager ends up fighting an installer over the same
file.

There is **no goreleaser `dmg` target**, on purpose: goreleaser cannot sign a disk
image, and an unsigned `.dmg` is blocked by Gatekeeper on current macOS, so
shipping one would produce an artifact that looks broken rather than one that
looks unsigned. `build/desktop/mac/build.sh` assembles and signs it on a macOS
runner with a Developer ID.

- **`lensyxe gap`** — joins what the code looks like to what actually ran, and
  reports the difference in three sections: critical path hotspots (complicated
  code that executes), deprioritized debt (complicated code that does not), and
  phantom code candidates (no runtime evidence at all).

  **Critical Path Risk = Static Complexity × log10(runtime hits + 1).** The
  logarithm stops one very hot function from swamping the ranking — 10,000× the
  hits moves the score about 3×. At zero hits the multiplier is `log10(1) = 0`, so
  never-running code scores zero here by construction, which is why it is reported
  as its own finding rather than as a low score in this one.

  Static Complexity reuses the analyzer `lensyxe analyze` already uses (45%
  complexity, 30% size, 25% churn), saturating at the same McCabe limit, so the
  two commands cannot disagree about what is complex.

### Profile formats, and why there is no new dependency

`gap` reads Go `.pprof` (gzipped protobuf), OpenTelemetry span JSON, and HTTP
access logs, detecting the format from file content rather than the extension.

pprof is decoded from the `profile.proto` wire format directly rather than by
adding `github.com/google/pprof` — this project ships six binaries and has kept
its direct dependency count low. The decoder is about 200 lines because only eight
top-level fields are needed. The field numbers were **confirmed against a profile
the Go toolchain produced** rather than taken from documentation, and
`TestParsePprofAgainstARealProfile` decodes a real generated profile so the
implementation cannot drift into a plausible-looking but wrong decoder.

### Why the output is qualified where it is

The failure mode of a tool like this is confident nonsense, so the limits are
stated rather than smoothed over:

- **Unattributable observations are never spread around.** A profile that resolved
  30% of its observations reports 30%, with the shortfall printed. It is not
  averaged across the codebase to make coverage look complete.
- **Phantom code is gated on attribution coverage.** "This never ran" is a claim
  about absence, and absence is only assertable where the profile could see the
  code. Below `--min-coverage` (default 0.5) files fall through to debt instead.
- **A symbol-less profile is reported as such**, rather than producing an empty
  table that reads like nothing ran.
- **Ambiguous file names are not joined.** After normalising a profile's build
  paths to base names, `internal/git/analyzer.go` and `internal/code/analyzer.go`
  collide. Those joins are refused and counted rather than guessed — on this
  repository that is 18 shared names.
- **A version mismatch is surfaced.** If the profile records
  `service.version=1.4.2` and the tree reports `1.0.0`, the two halves are
  describing different builds.
- **Access logs are not joined to code.** A log line records a route, not a
  handler; its counts are reported against routes with that stated.

A profile samples one window and is not a description of the program. Nothing here
forecasts demand.

- **`lensyxe topology`** — three independent analyses of repository structure,
  selected with `--bus-factor`, `--temporal`, `--boundaries`, or none for all
  three, and rendered as a table or JSON.
  - **Bus factor.** Recency-weighted ownership per file and directory. A file is
    flagged at risk only when one author both holds more than the share
    threshold *and* has not committed there within the staleness window; either
    condition alone is not a risk, since a single-author file can be actively
    maintained. Both the weighted and the raw commit share are printed, because
    the gap between them is what shows whether a file belongs to whoever wrote it
    first or to whoever works on it now.
  - **Temporal coupling.** File pairs that co-change in commits at or above the
    coupling threshold *without any import between them*. Pairs that share a
    package or have a direct import are excluded, because a compiler already ties
    those together and calling them hidden would be wrong.
  - **Boundaries.** Regions inferred from `pkg`, `internal`, `cmd`, `apps`, and
    `services` where present, and from top-level directories otherwise. Three
    rules are enforced: nothing below `cmd/` may import it, no package may import
    the module root, and there may be no local import cycle. Cycles are reported
    as one finding rather than as N pairwise edges.
- **`internal/gitlog`** — a single reader for dated commits and the files they
  touched, shared by the topology analyses.

### Notes on `lensyxe topology`

Ownership decay is `exp(-ln2 × age / half-life)`, so a commit made exactly one
half-life ago weighs exactly 0.5. An earlier version used `exp(-age/half-life)`,
which decayed by a factor of *e* per half-life and gave 0.368 where the parameter
name promised 0.5 — every share derived from it was wrong by a constant. A
parameter named for what it does has to actually do it.

The boundary audit reports that it found no violations on this repository, and
that is a real result rather than an empty scan: 31 packages across 130 Go files
were parsed, and the rule that `cmd/` may not be imported from below is checked
against all of them.

`--temporal` overlaps `lensyxe blast`. They answer the same coupling question at
different scopes: `topology --temporal` surveys the whole repository, `blast`
asks what a specific pending change is likely to affect. Blast Radius remains the
one to use during review.

Two of the three analyses withhold a number when the history cannot support it.
Ownership decay needs commits spread over time, and below 30 days of history the
at-risk verdict is withheld while the shares are still reported. Co-change needs
at least 10 commits, below which a ratio is dominated by coincidence. In both
cases the output names the threshold and states that the limit is the data rather
than a finding about the code.

- **`lensyxe cognitive`** — measures three structural properties per file and
  combines them into a 0-100 friction index: variable lifetime span (lines from a
  local's declaration to its last use), context-switch density (distinct call
  targets per hundred lines), and scope depth. Weights are documented and must sum
  to 1; a configuration that does not is rejected rather than silently
  renormalised, because renormalising would make the documented weights a lie and
  the index unreproducible from them.

  **There is no reading-time figure and no flag to enable one.** There is no
  validated mapping from these properties to the minutes a person will spend
  reading a file, so a number carrying that unit would be a measurement nobody
  took. The index is a defined, rankable quantity; a duration is not.

  Go is measured exactly with `go/parser`. TypeScript and JavaScript are measured
  line by line, which is approximate; variable lifetime is not observable without
  a parser for those languages and is excluded rather than guessed. Each file is
  printed with its method, and approximate files never enter the repository median,
  so the two are never compared as if they were the same measurement.
- **`lensyxe decay`** — three separate findings from git history: unchanged files
  inside the activity window, knowledge silos, and an activity half-life.
- **`lensyxe blast`** — co-change coupling: which files have historically changed
  in the same commit as the files you are changing. With no arguments it reviews
  the pending changeset. Coupling is commits-touching-both over
  commits-touching-the-rarer-file.

### Notes on the three new commands

Two evidence floors in `blast`, deliberately separate: one governs whether a
single pair's co-occurrence counts, the other whether the history can support any
ratio at all. Below ten commits nothing is predicted, because over four commits a
coupling ratio is dominated by coincidence.

`blast` is verified against ground truth rather than its own output: every
co-changing pair in this repository was computed directly from `git`, and the
command was confirmed to agree on both a positive (`.goreleaser.yaml` ↔
`release.yml`, 4 shared commits) and a negative (`api.go` ↔ `api_test.go`, 2
shared commits, correctly below the floor).

The half-life in `decay` is not reported below 180 days of history. Over a
shorter span any curve fits and the fitted number would describe the shape of the
available commits rather than the decay of the code. The gate message says
explicitly that this is a limit of the data, not a finding. The other two
findings are still reported — refusing to fit is not a reason to withhold what
can be measured. Silos need at least three commits: without that floor every
two-commit file in any repository reports as a 100% silo, and this repository's
own first run of the finding returned twenty-five such entries.

Silos overlap the repository-wide bus factor `lensyxe analyze` already reports,
and the output says so rather than presenting them as an independent signal.

All three are described in terms of what they compute. Hotspot analysis and
co-change coupling are extensions of published work; none of the three is claimed
to be novel, and none of the output is presented as a prediction.

- **`lensyxe setup`** — proposes a `.lensyxe.yml` from a measurement of the
  repository rather than from the documented template, which cannot know what is
  actually present. `ignore_dirs` lists only build directories that exist,
  `git_window_days` is scaled to the real age of the history, and
  `timeout_seconds` is derived from the observed scan duration and never
  tightened below the shipped default.
- **`lensyxe status`** — the same analysis `analyze` performs, rendered in a few
  lines: score, three dimensions, risk count, and the weakest dimension. It does
  not record a run to the history database.
- **Git-style aliases** — `an`, `st`, `log`/`lg`, `diff`/`df`, `w`, `ui`. Aliases
  rather than duplicate commands, so one implementation and one documented
  behaviour.

`setup` deliberately declines to set three things, and says so in its output
rather than omitting them silently:

- `hotspot_threshold`, because it changes the code health score.
- The CI thresholds in the config file, because `min_health_score`,
  `require_tests`, and `fail_on_drift` are not configuration keys. The real
  gates are the `--fail-under-health`, `--fail-on-critical-risk`, and
  `--fail-on-test-ratio-drop` flags on `analyze`, and they are printed as a
  command line instead. Writing them into a config file would produce a file
  that looks authoritative and is silently ignored on every line in it.
- The AI explanation layer, which stays off.

Nothing is written without `--write`, an existing file is never replaced without
`--force`, and rendering is byte-identical across runs.

## [1.0.0-rc1] - 2026-10-06

The first public release. It is a release candidate because it has never been
installed by anyone outside the machine it was built on, and because the parts
of the pipeline that only run at tag time are exercised for the first time by
this very tag.

### Changed

- **Rebranded from OpenLens to Lensyxe.** Module path is now
  `github.com/zelvior/lensyxe`. The executable is `lensyxe`, the config file is
  `.lensyxe.yml` (or `.lensyxe.yaml`), environment variables are prefixed
  `LENSYXE_`, the history database is `.lensyxe/history.db`, and the GitHub
  Action is `zelvior/lensyxe-action`. Every user-visible string, report field,
  and document was updated. No scoring behaviour, exit code, flag, or output
  format changed as part of the rename.

### Added

- **Golden-file testing.** Report output is pinned byte-for-byte in
  `internal/report/testdata/golden/`, with `go test ./... -update` to regenerate.
  A missing golden is a failure rather than an implicit creation.
- **Integration suite.** `tests/` drives the compiled binary through
  `exec.Command`, covering exit codes, flag-over-config precedence, the
  `--no-gate` override, and the read-only HTTP API.
- **Performance budgets.** Time and allocation ceilings are enforced by tests
  rather than merely printed by benchmarks, and are tunable per environment.
- **Cost attribution.** `TestProfileAnalysisCost` attributes an analysis to its
  stages so a budget breach is diagnosable rather than merely visible.
- **`CONTRIBUTING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`, and
  `docs/ROADMAP.md`.**

### Fixed

- **The Action's baseline resolution step had never run.** Its condition read
  `inputs.pr-comment`, which is not a declared input; it is the CLI *flag* the
  Action passes to the binary. GitHub evaluates an undeclared input to the empty
  string rather than erroring, so `'' == 'true'` was permanently false. The
  symptom was invisible because the step only exports a base ref, so the pull
  request comment silently lost its baseline delta and nothing failed.
  `scripts/check-repo-yaml.go` now asserts that every `inputs.*` reference is
  declared, and that every declared input is read.
- **`LENSYXE_*` environment overrides now work when there is no config file.**
  `Load` returned early on "config not found", before the merge step ran, so the
  environment was never consulted. Every `LENSYXE_*` variable was therefore
  inert on any repository without a `.lensyxe.yml`, which is the case where an
  env-only override is the only option a user has. Config keys are also now
  bound to their variables explicitly, because viper's `AutomaticEnv` does not
  list environment-only keys in `AllKeys()`.
  The failure was silent: a threshold set through the environment looked
  configured, produced no error, and gated nothing.
- `--no-gate` now suppresses CLI threshold flags as well as configured ones. The
  pairing `--no-gate --fail-under-health 70` previously had undefined
  precedence; `--no-gate` wins, while the PR comment and workflow report are
  still published, since those are outputs rather than gates.
- The GitHub Action installs through `install.sh` instead of building a release
  URL itself. Its own copy of the artifact-name mapping had drifted and produced
  a 404 on every platform, and it downloaded and executed a binary without
  verifying it against `checksums.txt`.
- `go.mod` declared a module path inconsistent with the published repository,
  which made the documented `go install` fail outright.
- The debounce burst test no longer asserts a timing property that fails under
  parallel load.

---

## [v1.0.0-rc1]

The first release candidate. Everything below is new relative to no prior
release; it is listed by capability rather than by commit.

### Core analysis

- **`lensyxe analyze [path]`** walks a target tree and reports lines of code,
  language distribution, file sizes, lexical complexity, dependency state, and
  Git-derived maintainability signals.
- **Health score**, 0 to 100, produced by weighted metrics that are renormalized
  when a metric is not applicable, so an unmeasured dimension cannot silently
  deflate the total. Graded A through F.
- **Risk engine** turning measurements into findings, each with evidence, an
  impact weight, and a severity. Hotspot confirmation requires size, churn, and
  complexity together; satisfying one factor yields a *candidate*, and the
  report says so.
- **Lexical complexity estimation** for control-flow languages. YAML is excluded
  by default: in a GitHub Actions workflow `if:` and `for:` are mapping keys, so
  scoring them yields a confident meaningless number.
- **Dependency analysis** across npm, Go, Python, and Cargo manifests and
  lockfiles, distinguishing declared from locked and reporting drift.

### Output

- **Terminal report**, deterministic and byte-stable, with a colour profile that
  degrades cleanly when piped.
- **`--format json`**, an indent-stable snapshot for pipelines and the
  dashboard.
- **`--format markdown`**, for pull request bodies and docs, free of ANSI
  escapes.
- **`--pr-comment`**, writing a comment even when a gate rejects the run, because
  a rejection that published no evidence would be the worst outcome for a gate.
- **`--baseline`**, producing movement deltas against a stored snapshot.

### History and comparison

- **`lensyxe history [path]`** renders a health timeline from a local SQLite
  database. Pure Go via `modernc.org/sqlite`, so `CGO_ENABLED=0` cross-compiles.
- **`lensyxe compare <revA> <revB>`** diffs two revisions, showing what appeared,
  what resolved, and what changed.
- **`lensyxe watch [path]`** re-analyzes on change with debouncing.

### Gates

- **Thresholds** on minimum health, maximum health drop, complexity increase,
  risk count, hotspot count, dependency drift, and test presence. Numeric
  thresholds distinguish *unset* from *zero*, so `max_risk_count: 0` means no
  risks tolerated.
- **Exit codes as a contract:** `0` clean, `1` crash or bad usage, `2` threshold
  breach.
- **The report is printed and the snapshot persisted before the gate is
  evaluated**, so a rejection always leaves its evidence behind.

### Monorepos

- **Workspace detection** for npm, pnpm, Yarn, Cargo, and Go workspaces.
- **Per-package scoring** with `--monorepo`, re-aggregating records the main
  walk already retained rather than making a second filesystem pass.
- **Git signals are not attributed per package.** Cadence and bus factor are
  repository-level; attributing them to a directory would be fabrication.
  Package scores carry code and dependency weights, `git_applicable` is false,
  and every report states this.

### Local dashboard

- **`lensyxe serve [path]`** serves an embedded dashboard and a read-only JSON
  API on loopback. No third-party script is loaded from a CDN, because a CDN is
  an outbound request and a supply-chain dependency.

### Optional AI explanation

- **`--explain`** summarizes findings that have already been computed. Bring your
  own key, read from an environment variable *name* rather than a value. Replies
  containing figures that were not in the input are **discarded**, not parsed:
  there is no code path by which a model can influence a score.

### What is deliberately absent

Stated plainly, because a tool that implies it measures something it does not is
worse than one that measures less.

| Not measured | What exists instead |
| :--- | :--- |
| Code coverage | Test-to-code **file ratio**, never called coverage |
| Build duration | Nothing; the tool does not execute your build |
| Vulnerabilities | Nothing; that needs a database and a network call |
| Complexity for YAML/SQL config | Excluded rather than estimated |
| Per-package git signals | Not attributable to a directory |

---

## [v0.1.0]

The initial internal release: the core engine only.

### Added

- Filesystem analysis: file counts, lines of code, blank and comment
  separation, language detection, and file-size distribution.
- The health score and its weighted metrics.
- The risk engine with evidence and impact weighting.
- Terminal and JSON output.

### Known limitations at this stage

- No history storage, so no trend reporting.
- No threshold gates.
- No dependency analysis; the dependency metrics reported as not applicable and
  were excluded from the total.
- No monorepo support.
- No Git signals.

Everything above was addressed in `v1.0.0-rc1`.

---

## Versioning policy

- **Pre-1.0 minor versions** may break. `v0.x` carries no compatibility promise.
- **`v1.0.0-rc` releases** may break between candidates.
- **`v1.0.0` and later** follow semantic versioning. Output that a script parses,
  including JSON field names and exit codes, is treated as a public interface
  and will not change in a patch.

[Keep a Changelog]: https://keepachangelog.com/en/1.1.0/
[Semantic Versioning]: https://semver.org/spec/v2.0.0.html