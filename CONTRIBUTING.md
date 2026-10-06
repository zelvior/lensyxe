# Contributing to Lensyxe

Thank you for considering a contribution. This document covers the architecture
you need to know, how to get set up, the testing rules, and what a good pull
request looks like here.

Two things are worth reading before anything else, because they explain most of
the design decisions in the codebase:

- [What Lensyxe does not measure](docs/SCORING_SPEC.md#what-is-not-measured) — the
  list of things that would be easy to fake and are deliberately absent.
- [Local-first principles](SECURITY.md#local-first-principles) — why there is no
  telemetry and only one optional outbound request.

---

## Table of contents

- [Architecture](#architecture)
- [Development setup](#development-setup)
- [Testing rules](#testing-rules)
- [Code style](#code-style)
- [Submitting a pull request](#submitting-a-pull-request)
- [Reporting bugs](#reporting-bugs)

---

## Architecture

Lensyxe is a single binary with no daemon, no server requirement, and no
mandatory network access. Everything it knows comes from reading the filesystem
and, when the directory is a Git repository, from shelling out to `git`.

### The pipeline

`analyze` runs five stages in a fixed order. The order is load-bearing, not
incidental: each stage consumes the previous stage's output, and several of them
could not work if run in a different sequence.

```
target directory
      │
      ▼
┌──────────────┐   file walk, LOC, language detection, lexical complexity
│ internal/code│   → CodeStats, []FileRecord, hotspots
└──────┬───────┘
       │
       ├──►┌────────────────┐  manifests and lockfiles
       │   │internal/deps  │  → DependencyStats
       │   └───────┬────────┘
       │
       ├──►┌────────────────┐  git log over the window, per-path churn
       │   │internal/git   │  → GitStats
       │   └───────┬────────┘
       │
       ▼       ▼
┌──────────────────┐  weighted, clamped 0-100
│internal/metrics  │  → Health, per-metric scores and weights
└────────┬─────────┘
         │
         ▼
┌────────────────┐    CodeStats + GitStats + DependencyStats + Health
│internal/risk   │    → []Risk with evidence, impact, and severity
└────────┬───────┘
         │
         ▼
┌──────────────────────────────────────────┐  terminal · json · markdown
│internal/report / internal/storage        │  → snapshot · SQLite row
└──────────────────────────────────────────┘
```

`internal/analyzer` orchestrates the stages and owns the timeout. It is the only
package that knows the sequence; every other package is independently testable.

### Package map

| Package | Responsibility | Must not |
| :--- | :--- | :--- |
| `cmd/lensyxe` | CLI surface, exit codes, flag/config precedence | Contain analysis logic |
| `internal/code` | Filesystem walk, LOC, languages, lexical complexity | Touch the network or Git |
| `internal/git` | Churn, authorship, cadence over a time window | Score anything |
| `internal/dependencies` | Manifest and lockfile parsing | Assume a package manager |
| `internal/metrics` | Weighted scoring, clamping, grades | Invent inputs |
| `internal/risk` | Risk rules, evidence, severity, impact | Read the filesystem |
| `internal/report` | All human-facing rendering | Mutate a snapshot |
| `internal/storage` | SQLite history, `modernc.org/sqlite` | Hold analysis state |
| `internal/analyzer` | Stage orchestration, timeout, wiring | Reimplement a stage |
| `internal/monorepo` | Workspace detection, per-package attribution | Make a second filesystem pass |
| `internal/ai` | Optional BYOK explanation layer | Compute or infer a metric |
| `internal/server` | Embedded dashboard and read-only API | Serve write endpoints |
| `internal/compare` | Two-revision diff | Recompute a score from scratch |
| `internal/gates` | Threshold evaluation | Decide what is healthy |
| `pkg/models` | The on-disk schema | Contain logic |
| `ide/vscode` | The VS Code extension, in TypeScript | Score anything or duplicate a threshold |
| `action` | GitHub Action metadata | Contain logic the CLI should own |

### The extension is a viewer, not a second implementation

`ide/vscode` contains no analysis. It runs `lensyxe analyze --format json` and
renders the snapshot, so the score in its status bar cannot disagree with the
score in a terminal.

Two consequences for contributors:

- The dimensions come from `health.metrics` in the snapshot, never from a copy of
  the list in the extension. Adding a scored dimension makes it appear in the
  sidebar without touching the TypeScript.
- An unmeasured dimension is rendered as `not measured`, never as `0`. Drawing it
  as a zero would invent a measurement and drag the apparent score down for a
  blind spot. See [docs/SCORING_SPEC.md](docs/SCORING_SPEC.md#what-is-not-measured).

### Why `ide/vscode` carries a go.mod

It is a module boundary, not a Go module, and there is no Go in that directory.

Without it, `go build ./...` and `go test ./...` descend into `node_modules/`, and
at least one popular npm dependency ships Go source inside its package. The
Lensyxe module would then compile a transitive JavaScript dependency's code — so
an unrelated npm update, or a Go version bump, could break the project's build for
a reason with nothing to do with Lensyxe.

The boundary stops the parent module at that directory. It has no `require` block
and no dependency on the parent module.

To work on the extension:

```bash
cd ide/vscode
npm install
npm run compile      # tsc, no errors expected
npm run smoke -- ../lensyxe ../examples/risky-go
npm run package
```

`npm run smoke` drives the compiled CLI client against a real binary. The client
imports nothing from `vscode` precisely so it can be exercised this way — which
is how the abort-classification bug was found.

### Invariants

These are the rules that keep the output trustworthy. Breaking one is a bug even
if every test still passes.

1. **Scoring is pure.** `internal/metrics` and `internal/risk` take values and
   return values. They do not read files, call `git`, or consult the clock.
   Anything time-dependent must be measured upstream and passed in.

2. **Rendering is deterministic.** Given the same snapshot, every renderer emits
   byte-identical output. Every sort has an explicit total ordering; no map
   iteration order may reach stdout, JSON, or a golden file.

3. **Nothing is claimed that was not measured.** A metric that cannot be computed
   is marked not-applicable and excluded from the weighted total, rather than
   being scored as zero or as a default. See
   [What is not measured](docs/SCORING_SPEC.md#what-is-not-measured).

4. **The AI layer never produces a number.** `internal/ai` summarizes findings
   that have already been computed. Replies containing figures that were not in
   the input are discarded rather than parsed. There is no code path by which a
   model can influence a score.

5. **No required network access.** The only outbound request in the project is
   the optional `--explain` layer, and it is inert without a key.

6. **Report before gate.** The report is printed and the snapshot persisted
   *before* thresholds are evaluated. A rejection that erased its own evidence
   would be the worst possible outcome for a gate.

7. **Exit codes are a contract.** `0` clean, `1` crash or bad usage, `2`
   threshold breach. `2` exists so a pipeline can distinguish "the policy said
   no" from "something broke".

8. **`CGO_ENABLED=0` must work.** The SQLite driver is pure Go precisely so
   cross-compilation needs no C toolchain. Keep it that way.

---

## Development setup

### Requirements

- **Go 1.22 or newer.** This is a floor, not a target; do not raise it without a
  discussion in an issue. The release matrix builds with `CGO_ENABLED=0`.
- **Git**, if you want the history and comparison metrics. Every other metric
  works without it, and reports correctly say so instead of guessing.
- **Node 18+**, only to rebuild the embedded dashboard. See
  [Rebuilding the dashboard](#rebuilding-the-dashboard).

### Getting started

```bash
git clone https://github.com/zelvior/lensyxe.git
cd lensyxe

# Run it against this repository.
go run ./cmd/lensyxe analyze .

# JSON, for piping.
go run ./cmd/lensyxe analyze . --format json

# Fail the build under 75 health.
go run ./cmd/lensyxe analyze . --fail-under-health 75
```

Do not commit a built binary. `go run ./cmd/lensyxe` is the supported way to
exercise a working tree.

### Configuration

Lensyxe reads `.lensyxe.yml` (or `.lensyxe.yaml`) from the target directory and
its parents. Flags beat the file; `--no-gate` beats every threshold, including
flags on the same command line.

A malformed config never aborts a run. The engine degrades to defaults and
prints a warning, because a typo in a config file should not be able to block a
pipeline.

See [docs/CONFIGURATION.md](docs/CONFIGURATION.md) for every key.

### Environment variables

| Variable | Purpose |
| :--- | :--- |
| `LENSYXE_AI_KEY` | API key for `--explain`. Never written to config. |
| `LENSYXE_AI_PROVIDER` | Provider override. |
| `LENSYXE_AI_MODEL` | Model override. |
| `LENSYXE_GIT_WINDOW_DAYS` | Override the Git history window. |
| `LENSYXE_MIN_HEALTH_SCORE` | Override the health floor. |
| `LENSYXE_DETECT_WORKSPACE` | Force workspace detection on or off. |
| `LENSYXE_VERSION` | Release to install, used by `install.sh`. |
| `LENSYXE_PREFIX` | Install directory, used by `install.sh`. |

`LENSYXE_CONFIG`, `LENSYXE_HOME`, and `LENSYXE_LOG_LEVEL` are **not**
implemented. Config discovery walks the directory tree and there is no logging
subsystem; adding either is a feature request, not a rebrand.

The key is read from an environment variable *name*, supplied via
`ai_key_env`, and is never stored in a config file. Config files get committed,
printed, and serialized into snapshots.

---

## Testing rules

### Running the suite

```bash
go test ./...                        # everything
go test -short ./...                 # skips budget and serve tests
go vet ./... && gofmt -l .           # both must be clean
```

Coverage, measured against the shipped packages:

```bash
go test -coverpkg=./cmd/...,./internal/...,./pkg/... \
        -coverprofile=cov.out ./cmd/... ./internal/... ./pkg/...
go tool cover -func=cov.out
```

The `-coverpkg` list is deliberate. `examples/` holds fixtures, and
`examples/risky-go` is *supposed* to have no test files, because it is the
fixture proving the `require_tests` gate rejects an untested repository. Go
still instruments its statements and counts every one as uncovered, which drags
the whole-module number down for no reason other than a fixture doing its job.

### Where tests live

| Location | Covers |
| :--- | :--- |
| `internal/*/[name]_test.go` | That package's behaviour, including boundaries. |
| `internal/report/golden_test.go` | Byte-exact report output against `testdata/golden/`. |
| `tests/integration_test.go` | The compiled binary: exit codes, flag precedence, HTTP API. |
| `tests/benchmark_test.go` | Performance budgets that fail rather than report. |
| `tests/profile_test.go` | Cost attribution, so a budget breach is diagnosable. |
| `examples/` | Committed fixtures whose properties are documented and asserted. |

### Rules

1. **A benchmark that reports is not a guard.** If a performance target matters,
   write a test that fails when it is breached. `go test ./tests/` enforces the
   budgets; `go test -bench=. ./tests/` merely measures them.

2. **Every test package must register `-update`.** `go test ./... -update`
   passes the flag to every test binary, so a package without the registration
   fails outright with `flag provided but not defined`. Run
   `pwsh scripts/make-golden-flags.ps1` after adding a package.

3. **A missing golden file is a failure, not an implicit creation.** A golden
   that appears without review has not been shown to be correct, and accepting
   one would let a broken renderer define its own expected output. Regenerate
   with `go test ./... -update` and read the diff.

4. **No timing-dependent assertions.** A test that asserts "five events produce
   one analysis" inside a 30 ms window fails under parallel load for reasons
   that have nothing to do with the code. Use a wide window, or wait on a
   condition. `internal/watch`'s debounce tests are the reference.

5. **Never share `t.TempDir()` across tests.** It is cleaned up when the creating
   test finishes. Shared fixtures belong under one directory created in
   `TestMain`, as `tests/fixtures_test.go` does.

6. **Test the mechanism, not the number.** Asserting "the score went down" passes
   even when it went down for the wrong reason. Assert that the specific file
   was reported as a hotspot candidate and that it was *not* confirmed without
   churn.

7. **Integration tests drive the real binary** via `exec.Command`. Exit codes and
   argument parsing are properties of the process; an in-process call cannot
   observe them.

### Docs are tested

`cmd/lensyxe/docs_test.go` builds the real command tree and asserts that the
documentation matches it: every flag exists, every flag is documented, every
command is documented, scoring constants quoted in the spec match the source,
every config key is covered, every Action input is referenced, internal links
resolve, anchors exist, and the repository name is consistent.

This is deliberate. Documentation that drifts from the code is worse than no
documentation, because it is trusted. **If you change a flag, a config key, an
Action input, or a scoring constant, run the tests and fix the docs in the same
commit.**

---

## Code style

- **Go 1.22, idiomatic.** No third-party helpers where the standard library has
  one. The dependency list is short and deliberate; adding a dependency needs a
  reason in the PR description.
- **Exported identifiers get doc comments that say why**, not what. `// Analyze
  walks the tree` restates the name. `// Analyze returns per-file records
  because the monorepo breakdown re-aggregates them instead of making a second
  pass` earns its place.
- **Errors are wrapped with context**: `fmt.Errorf("config: read %s: %w", path, err)`.
  Error strings start with the package or stage name.
- **No `panic` outside `init` and genuinely impossible states.** A bad input is a
  returned error, not a crash. The CLI turns errors into exit code 1.
- **Unexported helpers exist for testability**, and are tested directly. If a
  helper is not worth testing on its own, it probably should be inline.
- **Comments explain reasoning, not mechanics.** The codebase's comments lean on
  *why* a design was chosen and what the rejected alternative was, because that
  is the information a reader cannot reconstruct.

### Regenerating things

```bash
# Golden report output, after an intentional layout change.
go test ./... -update

# The risky example's generated bulk.
go run scripts/gen-risy-example.go

# Golden flag registrations, after adding a test package.
pwsh scripts/make-golden-flags.ps1
```

### Rebuilding the dashboard

The binary embeds the dashboard through `//go:embed`, and `//go:embed` can only
read a directory inside the declaring package. `dashboard/out` is outside
`internal/server`, so the export is copied in before the Go build:

```bash
cd dashboard && npm install && npm run build && cd ..
cp -r dashboard/out/. internal/server/assets/
go build ./cmd/lensyxe
```

`scripts/prepare-release.sh` performs this copy and then refuses to continue if
the tree is unformatted, the module files are untidy, `go vet` fails, or the
linker version symbols have been renamed. A release built from an untidy tree is
worse than no release, because the tag then claims something untrue.

---

## Submitting a pull request

1. **Open an issue first** for anything that changes behaviour, output format, or
   a scoring constant. It is cheaper to agree on the approach than to rewrite a
   finished branch.
2. **One concern per PR.** A refactor and a behaviour change in the same diff
   cannot be reviewed.
3. **Add or update tests** for the behaviour you changed. A bug fix without a
   regression test will be asked for one.
4. **Update the documentation in the same commit**, and let
   `go test ./cmd/lensyxe/` verify it.
5. **Keep `gofmt -l .` and `go vet ./...` clean.**
6. **Write a description that explains the why.** What was wrong, what you chose,
   and what you rejected. A diff usually shows the what.
7. **Regenerate goldens only deliberately**, and include the diff in the PR so a
   reviewer can see that the layout change is the intended one.

### Commit messages

Conventional Commits, because the changelog is generated from them:

```
feat(code): count generated files as source
fix(report): keep the grade visible when health is unmeasured
docs(spec): document the churn window default
test(golden): cover the empty-repository rendering
```

### CI gates

Seven jobs run on every pull request:

| Job | What it covers |
| :--- | :--- |
| `test` | The suite on Linux, macOS, and Windows. |
| `race` | The race detector, which a single-platform run can miss. |
| `cross` | Compilation for the six release targets. |
| `lint` | `golangci-lint`, pinned to the version `.golangci.yml` was verified against. |
| `coverage` | Product-code coverage via `-coverpkg`. |
| `release-config` | `goreleaser check`. |
| `repo` | The workflow, issue-form, YAML, and extension-manifest validators. |

The `repo` job exists because none of that configuration is read by the Go build.
A malformed workflow simply does not run; an issue form with a bad schema renders
blank. Both fail quietly, so something has to check them deliberately.

Release builds additionally run `scripts/prepare-release.sh`. A red CI run is the
reviewer's first stop, so push green.

---

## Reporting bugs

Report bugs through the issue tracker, not by opening a pull request. Please
include:

- The Lensyxe version (`lensyxe version`) and platform.
- The exact command and the full output, including exit code.
- The relevant part of your `.lensyxe.yml`, with secrets removed.

If a metric looks wrong, say which metric and why. A disagreement about a
*threshold* is often a disagreement about the threshold's purpose, and that
discussion is more valuable than a patch that moves a number.

For security issues, follow [SECURITY.md](SECURITY.md) instead. Please do not
open a public issue for one.

---

## Code of conduct

Participation is governed by [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## License

Contributions are accepted under the [MIT License](LICENSE).