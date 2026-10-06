# GitHub Action

`zelvior/lensyxe-action` runs Lensyxe in a pull-request workflow: it analyzes
the repository, enforces thresholds, and posts a comment with the metric
deltas.

## Contents

- [Quick start](#quick-start)
- [Inputs](#inputs)
- [Outputs](#outputs)
- [Exit codes](#exit-codes)
- [How the baseline comparison works](#how-the-baseline-comparison-works)
- [The comment](#the-comment)
- [Workflow recipes](#workflow-recipes)
- [Permissions](#permissions)
- [Self-hosting and pinning](#self-hosting-and-pinning)
- [What the Action does not do](#what-the-action-does-not-do)
- [Troubleshooting](#troubleshooting)

---

## Quick start

```yaml
name: Lensyxe

on:
  pull_request:

permissions:
  contents: read
  pull-requests: write

jobs:
  health:
    runs-on: ubuntu-latest
    steps:
      - uses: zelvior/lensyxe-action@v1
        with:
          fail-under-health: 70
          fail-on-critical-risk: true
```

That gives you a health gate and a PR comment with score deltas, and nothing
else. No repository contents leave the runner.

## Inputs

| Input | Type | Default | Description |
| :--- | :--- | :--- | :--- |
| `version` | string | `latest` | Lensyxe version to install, for example `0.3.0`. Use `latest` for the newest release. |
| `path` | string | `.` | Directory to analyze, relative to the repository root. |
| `format` | string | `markdown` | Human-readable report format for the job summary: `terminal`, `markdown`, or `json`. |
| `fail-on-threshold` | boolean | `true` | Enforce the threshold inputs below. When `false`, they are ignored and the step cannot reject the build. |
| `comment-on-pr` | boolean | `true` | Post or update the Lensyxe comment on the pull request. |
| `fail-under-health` | string | *(unset)* | Exit 2 when the health score is below this value. Empty disables the gate. |
| `fail-on-critical-risk` | boolean | `false` | Exit 2 when any critical risk is detected. |
| `fail-on-test-ratio-drop` | string | *(unset)* | Exit 2 when the test-to-code file ratio falls by more than this many percentage points. |
| `workflow-fail-severity` | string | `none` | Also exit 2 when a `.github/workflows` finding reaches this severity or worse: `critical`, `high`, `medium`, `low`, `none`. |
| `baseline` | string | *(unset)* | Path to a base-revision snapshot JSON. When omitted, the base ref is analyzed instead. |
| `token` | string | *(unset)* | Token used to post the comment. Defaults to the workflow token. |

### Deprecated

| Input | Status |
| :--- | :--- |
| `min-health-score` | **Ignored.** It duplicated `fail-under-health`, so setting it produced no threshold check and no warning. Use `fail-under-health`. |

Setting it still emits a workflow warning pointing at the replacement.

### A note on `format`

Machine-readable extraction always runs against the Lensyxe JSON report,
regardless of this setting, because the Action's own outputs are parsed from it.
`format` controls the **human-readable** report written to the job summary, which
is a second rendering of the same deterministic analysis.

## Outputs

| Output | Description |
| :--- | :--- |
| `score` | Overall health score, 0–100. |
| `grade` | Letter grade for the score. |
| `risk-count` | Number of risks detected. |
| `comment-file` | Path to the rendered comment body on disk. |
| `comment-body` | The rendered comment body. |

```yaml
- uses: zelvior/lensyxe-action@v1
  id: lens
- run: echo "scored ${{ steps.lens.outputs.score }} (${{ steps.lens.outputs.grade }})"
```

Outputs are empty strings when the analysis produced nothing, so a downstream
step must tolerate that rather than assuming a number.

## Exit codes

| Code | Meaning | Effect |
| ---: | :--- | :--- |
| `0` | Success | Step passes. |
| `1` | Failure | Bad path, unreadable input, or a crash. |
| `2` | Policy rejection | A configured threshold was breached. |

`2` is deliberately distinct from `1`. A branch protection rule can require this
check while still letting genuine failures stand out as infrastructure problems.

The comment is posted **even when the gate fails**, because the step runs its
posting step under `always()`. A rejection that published no evidence would be
the worst possible outcome for a gate.

## How the baseline comparison works

Metric deltas need a base measurement. The Action produces one of two ways:

1. **Explicit artifact.** Set `baseline` to a snapshot JSON produced by an
   earlier `lensyxe analyze --format json --no-persist` and uploaded as an
   artifact.

2. **Automatic, no artifact required.** When `baseline` is unset and the event
   carries a base revision, the Action exports the base commit with
   `git archive` into a temporary directory and analyzes it there:

   ```bash
   git archive "$base" | tar -x -C "$tmp/base"
   (cd "$tmp/base" && lensyxe analyze . --format json --no-persist --no-gate)
   ```

   The working tree is never modified and HEAD is never moved. A base that
   cannot be analyzed produces a **warning, not a failure** — the comment then
   shows absolute values instead of deltas.

If neither is available, the comment reports absolute figures and says the
baseline is unavailable. It never invents a comparison.

## The comment

```markdown
## 🔍 Lensyxe Engineering Impact

**Overall Health:** `88.0 → 86.0` (↓2)

| Category | Score | Delta | Status |
| :--- | :---: | :---: | :---: |
| Code | 91.0 | 0 | 🟢 Stable |
| Dependencies | 94.0 | 0 | 🟢 Stable |
| Testing | 25.0% | 0 | 🟢 Stable |
| Maintainability | 78.0 | 0 | 🟢 Stable |
| Build | — | — | ⚪ Not measured |

- **Testing**: test-to-code file ratio, not line coverage
- **Build**: Lensyxe does not execute the build, so duration is not measured

### ⚠ Detected Risks (1)

- 🔴 **Confirmed hotspot** — `internal/engine.go`
  - 900 code lines
```

Every number in the comment is either a snapshot field or a delta computed from
two snapshots. The comment carries a hidden marker
(`<!-- lensyxe:engineering-impact -->`) so a later run **updates** the existing
comment instead of posting a new one on every push, which would bury the pull
request.

### What the comment deliberately does not claim

- **Testing** is the test-to-code *file ratio*, labelled as such. Lensyxe does
  not instrument tests and has no line-coverage figure.
- **Build** is reported as *not measured*. Lensyxe does not execute your build,
  so it has no duration to report.

A row like `Build | — | — | ⚪ Not measured` is more useful than a plausible
number nobody can verify.

## Workflow recipes

### Gate on a score floor

```yaml
- uses: zelvior/lensyxe-action@v1
  with:
    fail-under-health: 70
```

### Gate with repository thresholds only

Put the policy in `.lensyxe.yml` so it is versioned with the code, and let the
Action enforce it:

```yaml
# .lensyxe.yml
min_health_score: 70
max_health_drop: 5
require_tests: true
```

```yaml
- uses: zelvior/lensyxe-action@v1
```

The Action always enforces `.lensyxe.yml` thresholds; `fail-on-threshold`
controls the additional flags on the Action itself.

### Comment only, never block

Useful while you are establishing a baseline. Turning the gate on before the
team agrees on a number produces noise nobody reads.

```yaml
- uses: zelvior/lensyxe-action@v1
  with:
    fail-on-threshold: false
```

### Gate on a test-ratio regression

```yaml
- uses: zelvior/lensyxe-action@v1
  with:
    fail-on-test-ratio-drop: 2
```

The gate is skipped when no baseline is available, so a first run on a new
repository cannot fail.

### Lint your own CI configuration

```yaml
- uses: zelvior/lensyxe-action@v1
  with:
    workflow-fail-severity: high
    comment-on-pr: false
```

Flags unsafe workflow patterns: unpinned third-party actions, `permissions:
write-all`, `pull_request_target` combined with a privileged checkout, missing
caching, and excessive matrix fan-out. See
[`internal/ci`](../internal/ci/analyzer.go) for the full rule set.

### Only on pull requests

```yaml
on:
  pull_request:
    branches: [main]

jobs:
  health:
    if: github.event.pull_request.head.repo.full_name == github.repository
    runs-on: ubuntu-latest
    steps:
      - uses: zelvior/lensyxe-action@v1
```

The `if` guard keeps `pull_request` runs from executing fork code with a token
that has write permissions. For untrusted forks, use `pull_request` and accept
that the comment is skipped, or gate on `pull_request_target` **without**
checking out the PR head — never both together.

## Permissions

The Action needs the minimum:

```yaml
permissions:
  contents: read        # checkout
  pull-requests: write  # post the comment
```

Set `comment-on-pr: false` and you can drop the write scope entirely:

```yaml
permissions:
  contents: read
```

### The token

The Action posts the comment itself using `github.token` by default, so the CLI
never reads a credential. Pass `token:` only for a cross-repository comment.

Lensyxe never reads `GITHUB_TOKEN` or any other credential. The Action holds the
token, not the tool.

## Self-hosting and pinning

### Pin the Action to a commit SHA

Tag refs are mutable: whoever owns the repository can repoint `v1` at different
content. For a security-sensitive gate, pin the SHA.

```yaml
- uses: zelvior/lensyxe-action@8f4b7f84864484a7bf31766abe9204da3cbe65b3 # v1
```

### Pin the Lensyxe version

```yaml
with:
  version: '0.3.0'
```

This makes the run reproducible and the Action's own cache key meaningful. The
`v` prefix is optional: `0.3.0` and `v0.3.0` both resolve, because the Action
strips a leading `v` before looking up the release. Release tags carry the
prefix; the release asset names do not.

### How the Action installs Lensyxe

The Action runs `install.sh` from the resolved tag rather than fetching the
release archive itself.

That indirection is deliberate. Release assets are named
`lensyxe_{version}_{Title(Os)}_{x86_64|arm64}.{tar.gz|zip}`, with a `.zip` for
Windows. A second copy of that mapping inside `action.yml` previously drifted
from the release spec and 404'd on every platform. It also meant the Action was
downloading and executing a binary without checking it against the release's
`checksums.txt`. Delegating means there is one place that knows how to spell a
release asset, and the integrity check cannot be bypassed by editing a workflow.

If you are auditing a workflow, the install is one line:

```yaml
curl -fsSL "$installer" | LENSYXE_VERSION="$version" LENSYXE_PREFIX="$dir" sh
```

### Use Lensyxe without the Action

The CLI is the actual product; the Action is a thin wrapper.

```yaml
- uses: actions/setup-go@v5
  with:
    go-version: '1.22'

- run: go install github.com/zelvior/lensyxe/cmd/lensyxe@latest
- run: lensyxe analyze . --format markdown
- run: lensyxe analyze . --fail-under-health 70
```

## What the Action does not do

| Not done | Why |
| :--- | :--- |
| Sends your source anywhere | Analysis runs on the runner. No repository contents leave it. |
| Requires a token to analyze | The token is only for posting the comment. |
| Executes your build | No build execution, so no build duration is reported. |
| Measures coverage | Lensyxe does not instrument tests. |
| Posts a new comment each push | It updates its own comment via a hidden marker. |
| Fails the build before publishing evidence | The comment is posted even on exit 2. |

## Troubleshooting

### The comment did not appear

Check, in order:

1. The event is `pull_request`, not `push`. The Action only comments on pull
   requests.
2. `comment-on-pr` is not `false`.
3. The workflow has `pull-requests: write`.
4. For a fork, `pull_request` runs with a read-only token. The comment is
   skipped; this is expected and correct.

### `Lensyxe thresholds were not met (exit 2)`

Working as intended. The comment on the pull request explains which thresholds
breached. Configure branch protection on this check to enforce it.

### `unknown --fail-on-coverage-drop`

Expected. Lensyxe does not measure code coverage. Use
`fail-on-test-ratio-drop`, which gates the test-to-code file ratio — a different
and weaker signal, which is why the flag is named for what it measures.

### The Action cannot find a release

`version` defaults to `latest`, which is resolved against the GitHub API. Pin
`version` to a concrete release, or pass `baseline`/`token` if you are behind a
proxy that restricts API access.

### A step-level check

The Action's own workflow analysis can flag the workflow you are writing:

```bash
lensyxe analyze . --workflow-fail-severity medium
```

This is a good pre-submission check, since the Action will otherwise tell you
about it on the pull request.