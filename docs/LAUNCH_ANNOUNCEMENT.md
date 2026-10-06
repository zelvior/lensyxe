# Launch announcement

Copy for the initial public release. Each post is written for its venue rather
than being one text pasted four times, because a Hacker News audience, a
`r/golang` audience, and an X audience reward different things.

Before publishing: **read [Claims verification](#claims-verification)** at the
bottom. One claim in the original brief does not survive contact with the code,
and it is flagged there with the reason.

---

## Table of contents

- [Assets](#assets)
- [Hacker News](#hacker-news)
- [Reddit — r/golang](#reddit-rgolang)
- [Reddit — r/programming](#reddit-rprogramming)
- [Reddit — r/devtools](#reddit-rdevtools)
- [X / Twitter](#x-twitter)
- [Demo script](#demo-script)
- [Claims verification](#claims-verification)

---

## Assets

| File | Use |
| :--- | :--- |
| [`assets/social-preview.png`](../assets/social-preview.png) | The preview image for the Hacker News, Reddit, and X posts. 1200×720, 84 KB. |
| [`assets/README_BANNER.svg`](../assets/README_BANNER.svg) | Source of truth for the banner above. Edit this, then re-export. |
| [`assets/architecture.svg`](../assets/architecture.svg) | The pipeline diagram, embedded in the README. |

The PNG is the one to upload: Hacker News, Reddit, and X all rasterise or refuse
SVG, so posting the vector source produces a broken preview or none at all. Keep
the SVG as the editable original — a banner maintained only as a PNG cannot be
re-coloured without repainting every element by hand.

To regenerate the PNG from the SVG:

```bash
# Any headless browser will do; the SVG has no external dependencies.
python -m http.server 8792 --directory assets
# then screenshot http://127.0.0.1:8792/README_BANNER.svg at 1200x720
```

---

## Hacker News

**Title**

> Show HN: Lensyxe – Evidence-backed engineering intelligence for your repositories

**Body**

> I built Lensyxe because every "code quality score" I have used had the same
> problem: it would tell me my repository was a 62 without telling me which file,
> which measurement, or which threshold produced that number. So I could not act
> on it, and eventually I stopped reading it.
>
> Lensyxe is a single Go binary that measures code structure, dependency surface,
> and git maintainability, then reports a 0–100 health score where every finding
> names the file, the number, and the line it crossed.
>
> ```text
> ENGINEERING HEALTH
>    79.6 / 100   C
>   ████████████████░░░░░
>   Overall adequate. Weakest dimension: Maintainability (Git) at 66.6.
>   Code health             81.5     40%
>   Dependency health       90.0     30%
>   Maintainability (Git)   66.6     30%
> ```
>
> A few things I was deliberate about, because they cost me features:
>
> **It does not measure things it cannot measure.** There is no code coverage
> number, no build duration, and no vulnerability score. Measuring build time
> would mean running your build, which measures your machine as much as your
> code. Instead there is a test-to-code *file ratio*, and it is never called
> coverage. When a metric cannot be computed it is marked not-applicable and
> excluded from the weighted total, rather than scored as zero — otherwise one
> missing dimension silently deflates your score and you cannot tell why.
>
> **Hotspot confirmation needs three independent signals.** A big file is not a
> problem; big files are everywhere. A file is only escalated to a confirmed
> hotspot when it clears size, churn, *and* complexity together. The report says
> which ones it had:
>
> ```text
> LOW  Large file / hotspot candidate  legacy/order.go
>   910 code lines; below the confirmed-hotspot bar on churn 0<=30, complexity moderate<=high.
> ```
>
> **The AI layer cannot produce a number.** There is an optional `--explain` that
> summarizes findings. Your key is read from an environment variable *name*, never
> stored in config. And any reply containing a figure that was not in the computed
> findings is *discarded*, not parsed. There is no code path by which a model can
> influence a score, because that would make the score unfalsifiable.
>
> **Output is deterministic.** Every sort has a total ordering and no map iteration
> order reaches stdout. The same commit produces the same report on every machine,
> which is what makes it usable as a CI gate rather than a dashboard.
>
> **It is local-first.** No telemetry, no account, no upload. The only outbound
> request in the entire project is the optional `--explain` call, and it is inert
> without a key. History is a SQLite file in your repository; delete it and the
> history is gone.
>
> **It is a real CI gate, with an exit-code contract.**
>
> | Code | Meaning |
> | :--- | :--- |
> | `0` | Clean. |
> | `1` | Something broke — bad path, crash, usage error. |
> | `2` | A configured threshold was breached. |
>
> The distinction between 1 and 2 matters in CI: "the policy said no" and
> "the tool is broken" are different problems. And the report is printed and the
> snapshot saved *before* the gate is evaluated, so a rejection never erases its
> own evidence.
>
> ```bash
> lensyxe analyze . --fail-under-health 75
> ```
>
> There is a GitHub Action too. It installs through the same `install.sh` a human
> would use, so it inherits the SHA-256 verification rather than reimplementing
> the download.
>
> Where it actually spends its time, measured not guessed — the code walk over a
> 40-file tree is ~26 ms; most of a real run is git subprocess spawn cost, which
> does not scale with your code and varies by platform. I documented the split
> instead of quoting a single flattering number.
>
> The dashboard is a static Next.js export embedded in the binary. No CDN, so no
> third-party script and no outbound request when you open it.
>
> MIT licensed. I would especially like to hear where it is wrong — the scoring is
> a set of opinions expressed as constants, and I would rather argue about the
> thresholds than have them quietly ignored.
>
> Repo: https://github.com/zelvior/lensyxe
>
> Edit: to be clear about what it does not do — no coverage, no build timing, no
> CVE scanning. Those need something the tool genuinely cannot do without running
> your build or shipping data to a server, and a plausible-looking number is worse
> than an absent one.

---

## Reddit — r/golang

> **Title:** [P] Lensyxe: a single-binary engineering health tool, written in Go with no required network access
>
> **Body:**
>
> I made this and would like feedback on the Go side specifically.
>
> Lensyxe walks a repository, measures code structure, dependency surface, and git
> churn, and reports a 0–100 health score with evidence attached to every finding.
> The parts I'd most like eyes on:
>
> - **Zero required network access.** No telemetry, no update check. The only
>   outbound request is an optional `--explain` that is inert without a key.
> - **`CGO_ENABLED=0` everywhere.** The SQLite driver is `modernc.org/sqlite`
>   specifically so the release matrix cross-compiles to `darwin/arm64` and
>   `linux/arm64` without a C toolchain. CI builds all six targets on every PR, so
>   that claim is checked rather than hoped for.
> - **Deterministic output.** Required, not aspirational — a CI gate that produces
>   different output on two machines is worse than no gate. Every sort has a total
>   ordering; report output is pinned by golden-file tests.
> - **Small dependency surface.** 38 modules, most of them transitive from cobra,
>   viper, lipgloss, and the SQLite driver. I am aware that viper is not beloved;
>   it bought me config-file + env-var precedence without me writing it, and I
>   would happily trade it for something smaller if someone makes the case.
>
> Hotspot detection required size, churn, *and* complexity together before it
> escalates anything. Single-factor signals are weak: big files are normal, and
> high-churn files are often just the ones people work on.
>
> It reports what it cannot measure as not-applicable rather than as zero, which
> is the difference between "no test files" and "we couldn't tell".
>
> I went back and forth on naming the unmeasurable things in the README rather
> than leaving them out, on the theory that a tool that implies it measures
> coverage is worse than one that measures less. Curious whether that reads as
> confidence or as hedging.
>
> Repo: https://github.com/zelvior/lensyxe — MIT. Go 1.22+.

---

## Reddit — r/programming

> **Title:** We built a "code health score" tool that refuses to report numbers it can't measure
>
> **Body:**
>
> Most code quality scores I've used have the same shape: a number, a colour, and
> no way to get from the number to the file you need to open. We wanted the
> opposite, so we built one where the score is the last thing you read.
>
> The design rule we kept coming back to: **a metric you cannot compute must be
> marked not-applicable and excluded from the total.** Not scored zero. Because if
> it's scored zero, one missing dimension drags your score down and you have no
> way to tell whether you have a problem or a blind spot.
>
> Concretely, that means:
>
> - **No code coverage number.** We don't execute your tests, so we can't have one.
>   We report a test-to-code file ratio and never call it coverage.
> - **No build duration.** Measuring it means running your build, which measures
>   your hardware and CI runner as much as your code.
> - **No CVE count.** That needs a database and a network call. We measure
>   something adjacent instead: whether your manifests have lockfiles at all, so
>   installs are even reproducible.
> - **No per-package git metrics in monorepos.** Churn and bus factor are
>   repo-level; attributing them to a directory would be making numbers up. The
>   per-package score says `git_applicable: false` and reweights the remaining
>   dimensions.
>
> The second rule: **every finding carries its evidence.** Not "code health is 62"
> but this:
>
> ```text
> LOW  Large file / hotspot candidate  legacy/order.go
>   LOC                    910 code lines
>   Churn                  0 lines modified in the git window
>   Complexity             10.6 estimated cyclomatic complexity (moderate)
>   -> below the confirmed-hotspot bar on churn 0<=30, complexity moderate<=high.
> ```
>
> That file is a *candidate*, not a confirmed hotspot, and the reason is printed.
> Escalation requires size, churn, and complexity together — any one alone is
> weak evidence, because large files are normal and high-churn files are often
> just the ones people work on.
>
> Third: **the AI layer cannot produce a number.** We have an optional summary
> feature. It reads your key from an environment variable name so it never lands
> in a config file that gets committed. And if the model's reply contains a figure
> that wasn't in our computed findings, we throw the reply away rather than
> parse it. A score a language model can nudge isn't a measurement.
>
> It's local-first: no telemetry, no account, no upload, history in a SQLite file
> in your repo. CI exit codes are 0 clean / 1 broken / 2 threshold-breached,
> because "the policy said no" and "the tool crashed" are different problems —
> and the report is written before the gate fires, so a rejection still publishes
> its evidence.
>
> Repo: https://github.com/zelvior/lensyxe — MIT. Written in Go, ~16 MB single
> binary, 26 ms to walk a small tree.

---

## Reddit — r/devtools

> **Title:** I built a local-first repo health CLI and embedded dashboard — would love your feedback on the workflow
>
> **Body:**
>
> Lensyxe — single Go binary, no account, no upload, no telemetry. It scores a
> repo 0–100 and shows its work.
>
> The workflow it aims at:
>
> ```bash
> lensyxe analyze .                          # what is the state
> lensyxe compare v1.0.0 HEAD                # what changed since the tag
> lensyxe history                            # the trend
> lensyxe serve --open                       # local dashboard
> lensyxe analyze . --fail-under-health 75   # the gate
> ```
>
> Things I think matter for dev tooling specifically:
>
> - **The report is deterministic.** Same commit, same bytes, every machine. It's
>    pinned by golden-file tests, so a layout change shows up as a diff you can
>    read rather than a surprise in CI.
> - **Exit codes are a contract.** `0` clean, `1` broken, `2` threshold breached.
>    The report is printed and the snapshot saved *before* the gate evaluates, so
>    a rejected build still leaves you a report to look at.
> - **No dashboard lock-in.** The dashboard is a static export embedded in the
>    binary, served from loopback, every API endpoint read-only. The JSON output
>    is the real interface; the UI is a viewer over it.
> - **No third-party script on the dashboard.** No CDN, because a CDN is both an
>    outbound request and a supply-chain dependency.
> - **Config is in your repo, thresholds are yours.** `.lensyxe.yml`, and env
>    overrides with a documented precedence: flag > file or env > default.
>
> It also refuses to measure what it can't. No coverage, no build timing, no CVE
> score. Full list in the README — I think that section is the most useful part of
> the docs, because most tools leave it implicit.
>
> Optional `--explain` gives a prose summary via your own API key. It's structurally
> incapable of contributing a number: replies containing unmeasured figures are
> discarded. I'd rather ship nothing than ship a score that a model can nudge.
>
> Repo: https://github.com/zelvior/lensyxe — MIT, Go 1.22+.

---

## X / Twitter

**Post 1 — launch**

> Lensyxe: evidence-backed engineering intelligence for your repositories.
>
> A single Go binary that scores a repo 0–100 and shows the file, the number, and
> the threshold behind every finding.
>
> Local-first. No telemetry. No account. No upload.
>
> github.com/zelvior/lensyxe 🧵

**Post 2 — the problem**

> The reason most code health scores get ignored:
>
> they tell you the number without telling you the file.
>
> "Code health: 62" is a colour, not an action.
>
> Every Lensyxe finding names the file, the measurement, and the line it crossed.

**Post 3 — evidence format**

> Lensyxe refuses to escalate a big file to a confirmed hotspot on size alone.
>
> It needs size + churn + complexity together.
>
> So the report reads:
>
> "910 code lines; below the confirmed-hotspot bar on churn 0<=30, complexity moderate<=high."
>
> Which is more useful than a confident wrong answer.

**Post 4 — what it won't do**

> What Lensyxe doesn't measure, stated up front:
>
> ✗ code coverage — we'd have to run your tests
> ✗ build duration — that measures your CI runner
> ✗ CVEs — needs a database and a network call
> ✗ per-package git metrics — churn isn't attributable to a directory
>
> A plausible number is worse than an absent one.

**Post 5 — the AI guard**

> The optional `--explain` layer can summarize findings. It cannot produce a number.
>
> Your key is read from an env var NAME, never stored in config.
>
> And any reply containing a figure that wasn't in our computed findings gets discarded, not parsed.
>
> A score a model can nudge isn't a measurement.

**Post 6 — engineering**

> Built to be a CI gate, not a dashboard:
>
> exit 0 = clean
> exit 1 = something broke
> exit 2 = threshold breached
>
> The report prints BEFORE the gate fires, so a rejection never erases its own evidence.
>
> Output is byte-deterministic. Same commit, same report, every machine.

**Post 7 — close**

> MIT licensed. 100% Go core, CGO_ENABLED=0, cross-compiles to 6 targets with no C toolchain.
>
> I'd most like to hear where it's wrong. The thresholds are opinions written as constants — I'd rather argue about them than have them quietly ignored.
>
> github.com/zelvior/lensyxe

---

## Demo script

For a video or a live walkthrough. Ninety seconds, one idea per shot.

| Time | Action | Point |
| :--- | :--- | :--- |
| 0:00 | `lensyxe analyze .` on a real repo | It produces a score *and* names the weakest dimension. |
| 0:15 | Scroll to a hotspot entry | Shows size, churn, and complexity as separate numbers, plus why it was *not* escalated. |
| 0:30 | `lensyxe analyze . --format json \| jq .health.metrics` | The JSON is the real interface. |
| 0:40 | `lensyxe analyze . --fail-under-health 90; echo $?` | Exit 2, and the report is already on screen. |
| 0:50 | `lensyxe compare v1.0.0 HEAD` | What this branch did to the score. |
| 1:00 | `lensyxe serve --open` | Dashboard from the same binary, loopback only. |
| 1:10 | Show the README's "what it does not measure" table | The differentiator. Close on it. |

---

## Claims verification

Every factual claim in the posts above, with where it comes from. Anything that
could not be verified is marked, and nothing unverifiable appears in the posts.

### Verified by measurement

| Claim | Evidence |
| :--- | :--- |
| Code walk ≈ 26 ms for 40 files / 4,515 code lines | `TestCodeWalkMeetsBudget`, enforced budget 200 ms |
| End-to-end ≈ 0.7–0.9 s for the same fixture; 86% of it git | `TestProfileAnalysisCost` attributes the split per stage |
| 400 files / 44,424 lines ≈ 1.2–2.0 s | `TestAnalyzeMediumRepoMeetsEndToEndBudget` |
| 2,000 files ≈ 3.0 s | `BenchmarkAnalyzeLargeRepo` |
| Deterministic output | `TestRenderersAreDeterministic` renders 5× and compares bytes; `TestAnalysisIsDeterministicAcrossRuns` compares score, grade, and risk IDs across runs |
| Exit codes 0 / 1 / 2 | `TestExitCodeMapping` plus end-to-end assertions in `tests/integration_test.go` |
| Report precedes the gate | Asserted by `TestCommentWrittenOnGateFailure` and the ordering in `cmd/lensyxe/main.go` |
| `CGO_ENABLED=0` on all 6 targets | `.github/workflows/ci.yml` cross-compiles every release target on every PR |
| 86.6% product-code coverage | `go test -coverpkg=./cmd/...,./internal/...,./pkg/...` |
| 551 test functions, 22 packages | `go test ./...` |
| ~16 MB stripped binary | `go build -trimpath -ldflags "-s -w"` |
| AI layer cannot produce a number | `internal/ai` discards any reply containing a figure absent from the input; `explainer_test.go` covers the guard |
| Key read from an env var *name* | `ai_key_env` in config; `LENSYXE_AI_KEY` default; no code path writes a key to config, a snapshot, or a report |
| Installer verifies checksums before extracting | `install.sh` downloads `checksums.txt` first; `scripts/test-install.sh`, 45 assertions |
| Action inherits that verification | `TestActionInheritsChecksumVerification` asserts `action.yml` never extracts an archive itself |
| Documented "not measured" list | `docs/SCORING_SPEC.md#what-is-not-measured`, itself asserted by `TestScoringSpecQuotesLiveConstants` |

### Corrected from the original brief

**"Build trends" is not a Lensyxe feature, and the posts must not claim it.**

`docs/SCORING_SPEC.md` lists build duration under *what is not measured*:

> **Build duration** — Lensyxe does not execute your build. Running it would
> violate local-first and would measure the machine as much as the code.

Claiming it in a launch post would put the launch copy in direct contradiction of
the project's own spec, and a commenter would find that quote within a minute. The
measurement the tool *does* produce is **health trend over time** from its SQLite
history, which is a real signal and is what the `lensyxe history` posts reference.

**"Sub-second execution" is qualified rather than dropped.** It is true for the
fixture sizes people usually see, and it is not true for a 2,000-file monorepo,
which takes about 3 seconds. Rather than pick the flattering framing, the posts
quote the measured split and explain why most of a run is git subprocess spawn
cost. That reads as more credible to the audience this is aimed at, and it is
what the tool's own profile test prints.

### Deliberately not claimed

- **No benchmark suite comparison against other tools.** None has been run.
- **No repository count, star count, or adoption figure.** None exists yet.
- **No "works on any repo of any size."** The 2,000-file fixture takes 3 seconds.
- **No claim of production readiness.** No release has been tagged yet.