# Changelog

All notable changes to Lensyxe are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## About the entries below

Dates for the initial releases are recorded at tag time by the release
automation. Confirm them against the actual tag before publishing, because a
changelog that claims a release date that is wrong is worse than one with no
date.

---

## [Unreleased]

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