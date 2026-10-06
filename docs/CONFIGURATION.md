# Configuration

The complete schema for `.lensyxe.yml`, plus the CI exit-code contract.

Every key is optional. A missing or malformed config **never** aborts a run:
Lensyxe warns on stderr and falls back to the built-in defaults, so a typo
degrades the analysis rather than blocking it.

## Contents

- [Precedence](#precedence)
- [Discovery](#discovery)
- [Analysis](#analysis)
- [Monorepo detection](#monorepo-detection)
- [History](#history)
- [Compare](#compare)
- [Watch](#watch)
- [AI explanation layer](#ai-explanation-layer)
- [Thresholds](#thresholds)
- [Exit codes](#exit-codes)
- [Environment variables](#environment-variables)
- [A complete example](#a-complete-example)

---

## Precedence

For every setting that exists in more than one place:

```
command-line flag  >  config file / environment  >  built-in default
```

A value is used from the highest-precedence source that sets it. Omitting a key
means "use the default", not "use zero".

## Discovery

1. `--config <path>`, if given. A path that does not exist or does not parse is
   a **warning, not an error**: the run continues with the built-in defaults.
2. Otherwise `.lensyxe.yml`, then `.lensyxe.yaml`, in the working directory.
3. Then each parent directory, walking upward, **bounded to 5 levels**.
4. Then `$HOME`.

The first match wins. The bounded upward walk is what makes
`lensyxe analyze ./cmd/x` still find the repository-level config instead of
silently using defaults. The bound matters: past a few levels the user is more
likely to be editing an unrelated tree than looking for a repository config, and
an unbounded walk could escape the repository entirely and pick up an unrelated
`.lensyxe.yml`.

### Environment variables

Every config key can also be set as an environment variable with the `LENSYXE_`
prefix and the key name upper-cased:

```bash
export LENSYXE_MIN_HEALTH_SCORE=70
export LENSYXE_GIT_WINDOW_DAYS=30
export LENSYXE_DETECT_WORKSPACE=true
```

These sit at the same precedence level as the config file: a command-line flag
still wins over both.

---

## Analysis

### `git_window_days`

**Type:** integer · **Default:** `90`

Days of git history the churn, cadence, and freshness metrics consider. The
window bounds how far back the analyzer reads commits; commits outside it do not
contribute.

### `hotspot_threshold`

**Type:** integer · **Default:** `400`

Code lines, blank lines and comments excluded, at or above which a file becomes
a hotspot **candidate**.

This controls candidacy only. A candidate is marked **confirmed** — the only
classification that can raise a high-severity risk — when it clears all three of
these fixed thresholds simultaneously:

- 500 code lines
- 30 lines added or deleted inside the git window
- estimated complexity in the `high` band or above

Lowering this value widens the candidate list without lowering the bar for
confirmation. That is deliberate: size alone is weak evidence of a problem. A
long file of declarative data changes rarely and breaks little.

### `enable_complexity`

**Type:** boolean · **Default:** `true`

Turn the lexical complexity estimator on.

Turning it off **zeroes** every complexity number rather than reporting a
fabricated value. Hotspot confirmation stops working, because confirmation
requires the complexity factor, and no file can be confirmed without it.

### `max_file_bytes`

**Type:** integer · **Default:** `2097152` (2 MiB)

Files larger than this are skipped entirely, so vendored blobs and generated
artifacts cannot distort LOC counts. Setting it low enough to skip real source
makes the score wrong, not neutral.

### `ignore_dirs`

**Type:** list of strings · **Default:** the list below

Directory names pruned from the code walk, **matched at any depth**:

```yaml
ignore_dirs:
  - .git
  - .hg
  - .svn
  - .idea
  - .vscode
  - node_modules
  - vendor
  - dist
  - build
  - out
  - target
  - coverage
  - .next
  - .nuxt
  - .cache
  - .venv
  - venv
  - __pycache__
  - .terraform
  - .gradle
  - bin
  - obj
  - assets
  - public
  - static
```

A directory named in this list is never walked. This changes the denominator of
every code metric, so removing an entry usually changes the score.

**This list replaces the built-in default rather than extending it.** That is
deliberate: it means you can drop an entry you disagree with, for example if
your project keeps hand-written source under a directory named `assets`.

The cost is that **you must update this list when a new Lensyxe release adds a
default**, or the added entry stops being ignored for you. `assets` is the one
most likely to bite: it is the conventional name for bundler output, and scoring
a minified webpack chunk as source distorts the language breakdown, the average
file size, and the score. A 2,600-line minified chunk inflates the average file
size across the whole repository.

### `timeout_seconds`

**Type:** integer · **Default:** `60`

Overall wall-clock budget for the scan. `0` disables the deadline.

---

## Monorepo detection

### `detect_workspace`

**Type:** boolean · **Default:** `false`

Detect a workspace layout and score each package individually.

Off by default because most repositories are not workspaces, and detection reads
several root manifests to find that out. When enabled, every analyzing command
produces a per-package breakdown. `lensyxe analyze --monorepo` turns it on for a
single run regardless of this setting.

### Recognized layouts

Checked in this fixed order, so a repository with several workspace files always
resolves to the same one:

| Order | File | Layout |
| ---: | :--- | :--- |
| 1 | `pnpm-workspace.yaml` | `pnpm` |
| 2 | `lerna.json` | `lerna` |
| 3 | `go.work` | `go-work` |
| 4 | `Cargo.toml` with `[workspace]` | `cargo` |
| 5 | `package.json` with `workspaces` | `npm` |

A directory becomes a package only when a recognized manifest declares it:
`package.json`, `go.mod`, `Cargo.toml`, or `pyproject.toml`. Guessing from folder
names produces a workspace report for repositories that have none, and a
confidently wrong list is worse than no list.

Negated patterns (`!packages/legacy`) are honored. A pattern escaping the
repository root is rejected. `node_modules`, `vendor`, `target`, and other
dependency directories are never descended into and never reported.

---

## History

### `history_limit`

**Type:** integer · **Default:** `10`

Default row count for `lensyxe history`. The `--limit` flag overrides it.

### `database_path`

**Type:** string · **Default:** `.lensyxe/history.db`

The SQLite database backing the history store. A relative path resolves against
the **analyzed repository**, not the working directory, so
`lensyxe analyze /other/repo` records against `/other/repo`. An absolute path
is used as-is.

Delete this file to erase all recorded history. History is pruned to 500 rows per
repository root.

---

## Compare

### `compare_root`

**Type:** string · **Default:** `""`

Repository `lensyxe compare` operates within. Empty means the working directory.
The `--compare-root` flag overrides it.

---

## Watch

### `watch_debounce_ms`

**Type:** integer · **Default:** `250`

Milliseconds to coalesce filesystem events into one re-analysis.

Editors write a file in several operations. Without a debounce, one save
triggers a burst of redundant scans.

### `watch_interval_seconds`

**Type:** integer · **Default:** `0`

Also re-analyze every N seconds even when nothing changed. `0` reacts to changes
only, which is what an idle repository should do.

---

## AI explanation layer

**Off unless you configure it.** See
[SCORING_SPEC.md](SCORING_SPEC.md#the-ai-explanation-layer) for the guarantees.

The layer only rewrites figures the deterministic engine already produced into
prose. It never computes a score, derives a metric, or invents a number. A
summary containing an unmeasured figure is **discarded with an error**, not
shown.

### `explain`

**Type:** boolean · **Default:** `false`

Enable the explanation layer by default. The `--explain` flag overrides this per
run.

### `ai_provider`

**Type:** string · **Default:** `""`

Provider to call: `openrouter`, `openai`, or `gemini`. Empty defers to
`$LENSYXE_AI_PROVIDER`. An unrecognized name is an error, checked before any
network call.

### `ai_model`

**Type:** string · **Default:** `""`

Model identifier. Empty uses `$LENSYXE_AI_MODEL`, then the provider's own
default. Precedence is flag or config, then environment, then provider default.

### `ai_key_env`

**Type:** string · **Default:** `LENSYXE_AI_KEY`

The **name** of the environment variable holding the API key — not the key.

This indirection is deliberate. A config file gets committed, printed, copied
into snapshots, and attached to bug reports. A key read from an environment
variable does not. Lensyxe never writes the key to disk, never passes it as a
command-line argument, and never includes it in an error message.

```bash
export LENSYXE_AI_KEY=sk-...
export LENSYXE_AI_PROVIDER=openai
lensyxe analyze --explain
```

With no key configured, `--explain` prints a note and the analysis is unaffected.

---

## Thresholds

CI gates. Every rule is optional; a configured breach makes `lensyxe analyze`
exit `2`, after the report has been printed and the snapshot recorded.

Numeric thresholds use an explicit **unset sentinel of `-1`** rather than zero.
This is the difference between `max_risk_count: 0` meaning "no risks tolerated"
and it meaning "the rule is disabled". An explicit `0` is honored as a real
bound.

### `min_health_score`

**Type:** number · **Default:** unset

Fail when the overall health score is below this value.

### `max_health_drop`

**Type:** number · **Default:** unset

Fail when the score falls more than this many points versus the previous stored
snapshot.

Skipped on the very first recorded run: a repository with no history cannot have
regressed against one.

### `max_complexity_increase`

**Type:** number · **Default:** unset

Fail when average estimated complexity rises by more than this.

With a baseline available the rule measures the **increase**. Without one it
measures the **absolute** value, which is still meaningful.

### `max_risk_count`

**Type:** integer · **Default:** unset

Fail when the total risk count exceeds this. `0` means no risks tolerated.

### `max_hotspots`

**Type:** integer · **Default:** unset

Fail when the confirmed hotspot count exceeds this. Only confirmed hotspots
count — see [SCORING_SPEC.md](SCORING_SPEC.md#hotspots).

### `fail_on_drift`

**Type:** boolean · **Default:** unset

Fail when a manifest has no lockfile, so installs are not reproducible.

### `require_tests`

**Type:** boolean · **Default:** unset

Fail when no test files are detected. Not evaluated when the repository has no
source files, since "no tests" is not a finding about a repository with no code.

---

## Exit codes

| Code | Meaning | When |
| ---: | :--- | :--- |
| `0` | Success | Analysis completed, no threshold breached. |
| `1` | Failure | Bad path, unreadable input, analyzer crash, or usage error. |
| `2` | Policy rejection | A configured threshold was breached. |

Code `2` is deliberately distinct from `1`. A pipeline can treat `1` as
"something broke" and `2` as "the gate rejected this code" — the difference
between a bug report and a rejected pull request.

### Ordering

The gate runs **after** the report and **after** persistence:

```
analyze → print report → record snapshot → evaluate gates → exit 2 if breached
```

A rejected run therefore still leaves a stored snapshot and, with `--pr-comment`,
a published comment explaining what regressed. A rejection that erased its own
evidence would be the worst possible outcome for a gate.

### Gate precedence

Command-line gates and config-file gates are both evaluated; either can cause
the rejection. `--no-gate` skips them entirely.

```yaml
# Reject below 70, or on any critical risk.
min_health_score: 70
require_tests: true
```

```bash
# The same policy, per run.
lensyxe analyze . --fail-under-health 70 --fail-on-critical-risk
```

---

## Environment variables

| Variable | Purpose |
| :--- | :--- |
| `LENSYXE_AI_KEY` | API key for the explanation layer. Default name; override with `ai_key_env`. |
| `LENSYXE_AI_PROVIDER` | `openrouter`, `openai`, or `gemini`. |
| `LENSYXE_AI_MODEL` | Model identifier. |
| `GITHUB_TOKEN` | Read by the GitHub Action to post a pull-request comment. Lensyxe never reads it. |
| `HTTPS_PROXY` | Honored by the installer and the explanation layer. |

Lensyxe has **no telemetry**. It makes no outbound request during analysis.
The only network access in the whole project is the optional `--explain` layer
and the pull-request comment, which the GitHub Action posts, not the CLI.

---

## A complete example

```yaml
# Lensyxe configuration

git_window_days: 90
hotspot_threshold: 400
enable_complexity: true
max_file_bytes: 2097152
timeout_seconds: 60

detect_workspace: true

history_limit: 20
database_path: .lensyxe/history.db

compare_root: ""

watch_debounce_ms: 250
watch_interval_seconds: 0

explain: false
ai_provider: ""
ai_model: ""
ai_key_env: LENSYXE_AI_KEY

# Thresholds. Every one is optional.
min_health_score: 70
max_health_drop: 5
max_complexity_increase: 1.5
max_risk_count: 20
max_hotspots: 3
fail_on_drift: true
require_tests: true
```