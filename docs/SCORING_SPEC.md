# Scoring Specification

The exact, deterministic arithmetic behind the Engineering Health Score.

Everything here is a pure function of the analyzer output. There is no
randomness, no time-of-day input, no network call, and no model inference
anywhere in the scoring path. **The same tree always produces the same score.**

If you find a discrepancy between this document and the output of
`lensyxe analyze`, trust the output and consider this file stale. The
implementation is in [`internal/metrics/scorer.go`](../internal/metrics/scorer.go).

---

## Contents

- [Definitions](#definitions)
- [The aggregate score](#the-aggregate-score)
- [Code health](#code-health)
- [Dependency health](#dependency-health)
- [Maintainability (git)](#maintainability-git)
- [Complexity estimation](#complexity-estimation)
- [Hotspots](#hotspots)
- [Risks and impact](#risks-and-impact)
- [Grades](#grades)
- [Per-package scores](#per-package-scores)
- [The AI explanation layer](#the-ai-explanation-layer)
- [What is not measured](#what-is-not-measured)
- [Tuning the constants](#tuning-the-constants)

---

## Definitions

Throughout, `clamp(v, lo, hi)` means `max(lo, min(hi, v))`.

| Symbol | Meaning | Source |
| :--- | :--- | :--- |
| `F` | Number of source files detected | code analyzer |
| `L` | Total code lines (blank lines and comments excluded) | code analyzer |
| `avg` | `L / F`, average code lines per file, rounded to 2dp | code analyzer |
| `langs` | Number of distinct languages detected | code analyzer |
| `H` | Total code lines sitting in hotspot candidates | code analyzer |
| `D` | Declared direct dependencies | dependency analyzer |
| `E` | Number of detected dependency ecosystems | dependency analyzer |

All reported values are rounded to two decimals. Rounding happens at the
presentation boundary and never feeds back into another computation.

---

## The aggregate score

Three weighted components produce the Engineering Health Score.

```
weights:  code 0.40   dependencies 0.30   git 0.30
```

A component is **applicable** only when its input data exists:

| Component | Not applicable when |
| :--- | :--- |
| Code | `F == 0` |
| Dependencies | never — see below |
| Git | the target is not a git repository |

Only applicable components contribute, and their weights are **renormalized**
to sum to 1 before averaging:

```
                  Σ (score_i × weight_i)
  Health.Score =  ───────────────────────────      over applicable i only
                     Σ (weight_i)
```

Renormalizing is why a plain directory that is not a git repository is still
scored: its git component drops out and code plus dependencies share 0.70,
renormalized to `0.40/0.70 = 0.571` and `0.30/0.70 = 0.429`. Punishing a
repository for having no git history would measure something other than its
health.

Each component reports its **normalized weight** after renormalization, and
`0` for a non-applicable one, so the JSON `weight` column always sums to 1.

---

## Code health

```
  Code.Score = 0.50 × sizeScore
             + 0.30 × hotspotScore
             + 0.20 × cohesionScore
```

### Component 1: average file size — two-sided

```
  sizeScore = 100                                        when 25 ≤ avg ≤ 150
           = 100 × (1 − clamp((avg − 150) / 150, 0, 1))   when avg > 150
           = 100 × (1 − clamp((25 − avg) / 15, 0, 1))     when avg < 25
```

The band is the important part. Both extremes are maintenance costs:

- **Above 150 lines/file**: files are too big to change safely. Penalty reaches
  full at 300 lines/file.
- **Below 25 lines/file**: files are fragments. Penalty reaches full at 10
  lines/file.

The lower bound exists because the measure is otherwise gameable. Scoring
fragmentation as harmless means splitting code into many tiny files raises the
average toward the ideal and pins cohesion at 100, so fragmentation
*increases* the score. Two repositories with identical logic then score 100
and 2 purely on how they were cut up. The two-sided band is what makes the
measure resist gaming.

### Component 2: hotspot concentration

```
  hotspotShare = H / L                    (0 when L == 0)
  hotspotScore = 100 × (1 − clamp(hotspotShare / 0.30, 0, 1))
```

Full penalty when 30% of all code lines sit in files above the hotspot
threshold. The threshold is configurable (`hotspot_threshold`, default 400);
see [Hotspots](#hotspots).

### Component 3: language cohesion

```
  cohesionScore = 0                     when avgFilesPerLang < 1
               = 100 × (avgFilesPerLang / 40)   when 1 ≤ avgFilesPerLang < 40
               = 100                               otherwise

  where avgFilesPerLang = F / langs
```

This deliberately measures **language spread only**. It must not be read as a
general "more files is better" signal: granularity is component 1's job, and
rewarding it here too is what originally made the score gameable.

---

## Dependency health

```
  Dep.Score = 0.50 × countScore
            + 0.30 × reproScore
            + 0.20 × spreadScore
```

### Component 1: dependency count

```
  countScore = 100 × (1 − clamp(D / 50, 0, 1))
```

Linear from full credit at zero direct dependencies to zero at 50. Fifty is
the point where the update surface stops being something a person can hold in
their head.

### Component 2: reproducibility

```
  reproScore = 100           when every detected ecosystem has a lockfile
             = 100 × 0.80    otherwise
```

An **absolute** penalty rather than a graded one, because a missing lockfile is
not "somewhat bad": builds stop being deterministic.

### Component 3: ecosystem spread

```
  spreadScore = 100 / E      when E > 1
              = 100          when E ≤ 1
```

Two ecosystems score 50, three score 33.3. Each additional package manager is a
second toolchain to keep current, so the penalty is intentionally steep rather
than linear.

### No manifest

When no supported manifest is found, the metric stays **applicable** at a fixed
baseline rather than dropping out:

```
  Dep.Score = 100 − 10 = 90
```

A project with no manifest has no dependency risk to speak of, but it also has
nothing to verify. Scoring it 100 would claim a clean bill of health for an
unverifiable surface, and dropping the component would quietly shift weight onto
code. 90 says "nothing to see, but nothing confirmed".

---

## Maintainability (git)

```
  Git.Score = 0.30 × cadenceScore
            + 0.25 × freshnessScore
            + 0.25 × churnScore
            + 0.20 × busScore
```

### Cadence

Commit cadence within the window, extrapolated to a week:

```
  cadenceScore = 100                                         when commitsPerWeek ≥ 8
               = 100 × ((commitsPerWeek − 0.5) / (8 − 0.5))  when 0.5 < commitsPerWeek < 8
               = 0                                           otherwise
```

The floor at 0.5 prevents a repository with one commit in 90 days from being
scored as if it were merely slow rather than effectively abandoned.

### Freshness

```
  freshnessScore = 50                        when the last commit date is unknown
                = 100 × (1 − clamp(daysSinceCommit / 180, 0, 1))   otherwise
```

Linear decay to zero at 180 days. An unknown last commit gets 50 — partial
credit, never full, because absence of evidence is not evidence of activity.

### Churn concentration

```
  churnScore = 100 × (1 − clamp(churnConcentration / 0.6, 0, 1))
```

`churnConcentration` is a normalized 0..1 measure of how unevenly window churn
is distributed across files: 0 is perfectly even, 1 is a single file absorbing
everything.

```
              Σ p_i²  −  1/N              Σ p_i² = Σ (churn_i / totalChurn)²
  concentration = ────────────────
                     1 − 1/N
```

The `− 1/N` correction subtracts the concentration a perfectly even
distribution would have, and the `1 − 1/N` divisor renormalizes, so the measure
stays meaningful for a repository with only a handful of files instead of
reading every small repository as maximally concentrated.

### Knowledge distribution

```
  busScore = 85   when busFactor ≤ 1
           = 100  otherwise
```

A bus factor of one is a real risk: one person understands the code.

---

## Complexity estimation

Complexity is **lexical**, not an AST. The scan counts decision tokens and block
depth while a file is already being read for LOC, which keeps the pass
single and allocation-light.

Every figure is reproducible by hand from the source.

### What is counted

**Branch keywords**, matched as whole lowercase words so `ifdef`, `format`, and
`caseLabel` never inflate the count:

```
if  elif  elsif  for  while  case  catch  select  match  except
unless  rescue  when  loop
```

**Short-circuit operators**, `&&` and `||`, each worth one branch point.

**Definition keywords** open a scope so branch points can be attributed per
function: `func`, `fn`, `def`, `defp`, `deff`, `function`, `sub`, `proc`,
`method`, `class`, `impl`, `trait`, `interface`, plus the modifier-prefixed
forms `async`, `export`, `public`, `private`, `protected`, `static`, `override`,
and `abstract`. Declaration keywords are deliberately excluded — `var`, `let`,
`const`, `type`, `struct`, and `enum` introduce names, not executable bodies,
and counting them as functions would inflate the denominator and make genuinely
complex code look simple. A `class` is included because its methods are nested
inside it and would otherwise go unattributed.

### The estimate

```
  complexity = (totalBranches + functions) / functions
```

With no detectable function, the whole file is treated as one unit rather than
dividing by zero or reporting a meaningless 1.0.

```
  density = complexity × 100 / codeLines
```

### Bands

Following the conventional McCabe thresholds:

| Band | Estimated complexity |
| :--- | :--- |
| `low` | ≤ 10 |
| `moderate` | ≤ 20 |
| `high` | ≤ 50 |
| `very_high` | > 50 |

### Languages not scored

Complexity is computed **only** for languages with real control flow. The set
is opt-in — Go, C, C++, C#, Java, JavaScript, TypeScript, Rust, Zig, Swift,
Kotlin, Scala, PHP, Ruby, Python, Shell, SQL, Lua.

Declarative formats such as YAML are excluded, and their files are absent from
the complexity aggregate rather than reported as `low`. In a GitHub Actions
workflow, `if:`, `for:`, and `when:` are *mapping keys*, and any shell embedded
in a `run: |` block would be counted as YAML branching. Scoring that produces a
confident, meaningless number. "Not measured" is the honest answer; a fabricated
score is worse than no score, because it drives the risk engine.

---

## Hotspots

Two independent decisions, deliberately not conflated.

### 1. Candidacy

```
  candidate ⟺ codeLines ≥ hotspot_threshold        (default 400)
```

This is the configurable knob, and it stays exactly as configured.

### 2. Confirmation

```
  confirmed ⟺ codeLines ≥ 500
            AND churn ≥ 30
            AND complexity ≥ high band
```

Confirmation uses **fixed** thresholds, not the configured one. A caller who
lowers `hotspot_threshold` below 500 still sees a wider candidate list, and
simply never sees those candidates confirmed. That is the intent: size alone is
weak evidence of a problem. A long file of declarative data changes rarely and
breaks little.

`classification` lists the factors that fired, in a fixed order, so the token
list is deterministic:

```
  size, churn, complexity, confirmed
```

The `confirmed` token appears only when all three factors fired.

---

## Risks and impact

A **Finding** is a factual observation and never changes the score. A **Risk** is
a derived, evidence-backed consequence that explains a score reduction.

```
  Health.Score = 100 − Σ (risk.impact)   ... conceptually
```

The risks do not sum to the deficit; they *explain* it. Every risk carries its
evidence, so a reader can check each claim against a number and a path without
re-running anything.

### Impact formulas

Each impact is the penalty on a 0–100 scale, then capped to `[0, 15]`.

| Risk | Formula |
| :--- | :--- |
| Confirmed hotspot | `(3 + 2·(lines/500) + 1.5·(churn/30) + 2·(cx/20)) × 0.40` |
| File above complexity band | `(cx / 20) × 8.0 × 0.40` |
| No tests at all | `4.0` (fixed) |
| Low test-to-code ratio | `((0.15 − ratio) / 0.15) × 6.0 × 0.40`, fires below 0.15 |
| Missing lockfile | `5.0 × 0.30` |
| Lockfile older than manifest | `1.0` (fixed) |
| Deep transitive tree | `((multiplier − 10) / 10) × 4.0 × 0.30`, fires when `transitive / direct ≥ 10` and `direct > 5` |
| Bus factor risk | `topAuthorShare × 6.0 × 0.30` |
| Churn concentration | `churnConcentration × 5.0 × 0.30` |
| Repository staleness | `(daysSinceCommit / 180) × 4.0 × 0.30` |
| Weak dimension | `(100 − metricScore) × metricWeight` |

Each weight (`0.40`, `0.30`) is the dimension's share of the overall score, so
a risk cannot claim a larger penalty than the component it belongs to could
possibly cost.

### Severity

Severity is derived from impact alone, so it is always consistent with the
number shown:

```
  critical  ⟸ impact ≥ 12.0
  high      ⟸ impact ≥  6.0
  medium    ⟸ impact ≥  2.0
  low       ⟸ otherwise
```

### Ordering

```
  sort by severity desc, then impact desc, then ID asc
```

The ID tiebreak makes the order total, which keeps output byte-stable across
runs regardless of how the engine iterated its internal maps.

---

## Grades

Fixed, inclusive thresholds:

| Score | Grade |
| :--- | :---: |
| ≥ 90 | A |
| ≥ 80 | B |
| ≥ 70 | C |
| ≥ 60 | D |
| < 60 | F |

---

## Per-package scores

With `--monorepo` (or `detect_workspace: true`), each workspace package is
scored by the **same scorer** on the **same data**, restricted to that package.

A package score is computed from that package's own files and manifests. It is
not a share of the repository score: a small, clean package can legitimately
score higher than a large one that does not.

### Git is excluded

Git-derived components are **not** attributed to a package. Commit cadence,
authorship, and bus factor describe the repository, not a directory, and there
is no honest way to split them. The git component is marked non-applicable,
contributes zero weight, and `GitApplicable` is `false`.

So a package score carries the code and dependency weights, renormalized from
`0.40 + 0.30 = 0.70` to `0.571 + 0.429`. This is stated in every report that
shows a package score, rather than left for the reader to infer from a missing
column.

### No second filesystem pass

The breakdown reuses the per-file records the main walk already produced.
Opening a workspace therefore does not cost one scan per package; it costs one
linear pass over records already in memory.

---

## The AI explanation layer

The `--explain` layer is optional, off by default, and **cannot** influence any
number.

### What it may do

Rewrite figures the deterministic engine already produced into prose.

### What it may never do

- Compute, derive, estimate, round, or invent any figure.
- Introduce a metric absent from the report, however relevant it seems.
- Speculate about causes.
- Name specific tools, vendors, or refactoring techniques.
- Affect the health score, a risk list, or an exit code.

### How that is enforced

1. **The prompt is assembled from a snapshot**, never from source code. It
   contains aggregate statistics and risk titles. No file contents, no
   identifiers from which a repository could be reconstructed.
2. **Every numeric token in the prompt is recorded** as a supplied fact.
3. **The system message forbids** the model from introducing numbers, explicitly.
4. **The response is re-parsed.** Every numeric token in the reply is compared
   against the supplied facts. A figure that was not supplied causes the
   summary to be **discarded with an error**, not shown.
5. **Numbers are normalized** before comparison, so a model that correctly
   restates `80` as `80.0` is not falsely rejected.
6. **Small integers are exempt.** Prose legitimately contains "two risks" and
   "one week". Rejecting those would make the check useless, so only figures
   above 20 — the range where real measurements live — are enforced.
7. **The output is labelled.** Every rendering carries its provider, model, and
   a statement that the score is unaffected.
8. **A provider failure never fails the analysis.** The score, the risk list, and
   the exit code are already computed and printed before the layer runs, so a
   third-party outage cannot make the deterministic result depend on that
   service's uptime.

### Secrets

The API key is read from an environment variable named by `ai_key_env` (default
`LENSYXE_AI_KEY`). It is never written to the config file, never passed as a
command-line argument, never serialized into a snapshot, and never included in
an error message — an error is the string most likely to be pasted into a bug
report.

Providers: `openrouter`, `openai`, `gemini`.

---

## What is not measured

Stated plainly, because a tool that implies it measures something it does not
is worse than one that measures less.

| Not measured | Why, and what is used instead |
| :--- | :--- |
| **Code coverage** | Lensyxe does not instrument tests. The **test-to-code file ratio** is a different, weaker signal and is never called coverage. |
| **Build duration** | Lensyxe does not execute your build. Running it would violate local-first and would measure the machine as much as the code. |
| **Complexity for declarative files** | Excluded entirely rather than estimated; see [Languages not scored](#languages-not-scored). |
| **Lockfile staleness** | Inferred from manifest/lockfile modification times. It never touches the network to check whether versions still resolve. |
| **Vulnerability counts** | Requires a vulnerability database and a network call. Not attempted. |
| **Per-package git signals** | Not attributable to a directory; see [Git is excluded](#git-is-excluded). |
| **Transitive dependency risk** | Counted, not analyzed. No version reaches this tool's analysis. |
| **Per-language scoring** | Complexity is aggregated across languages. A polyglot repository is scored as a whole. |

---

## Tuning the constants

Every threshold above is a Go constant in
[`internal/metrics/scorer.go`](../internal/metrics/scorer.go), exported where
the CLI documents it:

```go
const (
    WeightCode = 0.40
    WeightDeps = 0.30
    WeightGit  = 0.30
)

const (
    MaxFilesPerLanguage = 40
    IdealAvgFileLines   = 150.0
    AvgLinesPenaltyRate = 150.0
    HotspotSharePenalty = 0.30
    MinAvgFileLines     = 25.0
    FragmentPenaltyRate = 15.0

    MaxDirectDeps     = 50
    UnlockedPenalty   = 0.20
    NoManifestPenalty = 0.10

    IdealCommitsPerWeek = 8.0
    LowCadenceFloor     = 0.5
    StaleDaysPenalty    = 180
    ChurnConcentrationThreshold = 0.6
    BusFactorPenalty            = 0.15
)
```

Changing one of these **changes scores for every user of every release**. Treat
them as API: a change belongs in a release note, not a patch. Anything that
should vary per repository is already a config key — see
[CONFIGURATION.md](CONFIGURATION.md).