# Maintainer guide

Operating procedure for whoever holds commit rights on Lensyxe. Written down so
that the judgement calls are consistent whether one person or five make them.

It is deliberately opinionated about triage and deliberately light on ceremony
about everything else. A project this size does not need a release council.

---

## Table of contents

- [Current state](#current-state)
- [Triage rules](#triage-rules)
- [Labels](#labels)
- [When to close something without merging it](#when-to-close-something-without-merging-it)
- [Versioning and tagging](#versioning-and-tagging)
- [Release checklist](#release-checklist)
- [Security advisories](#security-advisories)
- [Commit authority](#commit-authority)
- [When the project needs a different shape](#when-the-project-needs-a-different-shape)
- [Dependabot and automation](#dependabot-and-automation)

---

## Current state

| | |
| :--- | :--- |
| Stage | Pre-1.0. No tagged release yet. |
| License | MIT |
| Go | 1.22 or newer. Do not raise without discussion. |
| Release matrix | `linux`, `darwin`, `windows` × `amd64`, `arm64`, all with `CGO_ENABLED=0` |
| Maintainers | One, with commit rights. See [below](#commit-authority). |

Everything in this document applies to a solo maintainer, and most of it stops
being ambiguous the day there is a second one.

---

## Triage rules

The goal of triage is to spend a contributor's attention on the highest-value
item available, and to do it in a way that makes the next decision obvious.
Labels exist so that decision is mechanical.

### The order questions get answered

Work issues in this order. Not by severity — by how much a person's time is
blocked per minute of yours.

1. **Is it a security report?** Stop everything and follow
   [Security advisories](#security-advisories). Nothing else matters until that
   is resolved one way or the other.
2. **Is it a regression?** Something that used to work and does not. These cost
   the most trust per issue and are the fastest to earn back. Reproduce it
   before deciding.
3. **Does CI fail on a platform or target it used to pass on?** A broken release
   matrix is worse than a missing feature, because it silently stops shipping.
4. **Is it a correctness bug in a number?** A wrong score is worse than no score,
   because it is acted on.
5. **Everything else**, in the order it arrived.

### What counts as a bug

**A bug is behaviour that contradicts the documentation or an invariant.** That
covers a wrong score, a crash, a flag that does not do what its help text says, a
determinism violation, and an exit code that disagrees with the contract.

### What is not a bug, and how to tell the difference quickly

| Report | What it is | Where it goes |
| :--- | :--- | :--- |
| "There is no coverage number" | A deliberate omission, documented | Answer with the link, close |
| "The score dropped and I disagree" | A disagreement with a model | Answer with the formula, maybe a threshold discussion |
| "It scored my repo 40" | Expected behaviour on a repo Lensyxe finds hard | Ask for `--format json` and the scoring spec |
| "Can it measure X" | A feature request | Re-label and redirect |
| "It is slow" | Possibly real | Ask for the file count and the timing, then check the budget |
| "It crashed on my private repo" | A bug | Ask for a reproduction shape, not the code |

The most common report by a wide margin is a request for something Lensyxe
deliberately does not measure. Those are not invalid — they are the roadmap, and
some of them will eventually win. The response is to agree that the request is
reasonable, explain what stops it, and point at
[docs/ROADMAP.md](ROADMAP.md). Closing those politely is worth more than closing
them quickly.

### The determinism rule

If two runs of the same commit on two machines produce different output, that is
a bug regardless of how minor it looks, and it gets triaged as a regression. It
is also the report hardest to investigate, so ask for both outputs immediately
rather than asking for a reproduction first.

### First response

Aim to acknowledge within a week, and within two days for a regression. The
acknowledgement does not need a diagnosis; it needs to say what the next step is
and who owns it. "I cannot reproduce this yet, here is what I have tried" is a
complete and useful first reply.

---

## Labels

| Label | Meaning | Applied when |
| :--- | :--- | :--- |
| `bug` | Contradicts the docs or an invariant | On triage |
| `enhancement` | New capability | On triage |
| `needs-triage` | Unclassified | Automatically on issue creation |
| `needs-repro` | Cannot reproduce from the description | When the first attempt fails |
| `good-first-issue` | Small, self-contained, obvious fix | Deliberately, sparingly |
| `blocked` | Waiting on something external | When it is genuinely waiting |
| `docs` | Documentation only | On triage |
| `dependencies` | Automated dependency bump | By Dependabot |
| `review-needed` | Touches the dependency surface | By Dependabot |
| `breaking` | Breaks a documented interface | By the author |

Use `good-first-issue` only when the fix is genuinely obvious. A label that
routinely turns out to be a design problem teaches contributors to ignore it,
which is worse than not having it.

---

## When to close something without merging it

Closing a contribution is not a judgement on the person. Say so in the reply,
and be specific about whether the problem is the approach or the goal.

- **The goal is wrong, the idea is good.** Say what would make it acceptable.
  This is the common case, and the reply should invite a revision.
- **It conflicts with a documented invariant.** Name the invariant and link to
  it. If the invariant is what should change, that is a different conversation
  and it belongs in an issue.
- **It is a duplicate.** Link the canonical issue, close, and carry on.
- **It cannot be maintained.** This has to be said plainly and without hedging.
  A reviewer who cannot tell whether a change was rejected for taste or for
  maintainability learns that reviews here are arbitrary.
- **It is inactive.** Offer to hold it rather than closing it. Stale-but-wanted
  is a normal state for a feature request.

---

## Versioning and tagging

### Scheme

Semantic Versioning, `vMAJOR.MINOR.PATCH`, tags annotated and signed off in the
release workflow.

```
v0.3.0        release
v1.0.0-rc1    release candidate
v1.0.0        first stable release
```

### What counts as which

**MAJOR** — anything a script could notice:

- Removing or renaming a flag, config key, or Action input.
- Changing the meaning of an existing flag rather than adding one.
- Changing an exit code.
- Changing a JSON field name, type, or enum value.
- Raising the minimum Go version, or dropping a platform from the matrix.
- Changing a scoring constant in a way that moves existing scores.

**MINOR** — new capability that does not break the above:

- A new command, flag, config key, or Action input.
- A new supported language or package ecosystem.
- A new risk rule or metric, **provided** existing scores do not move for a
  repository that would not have triggered it.

**PATCH** — no interface change and no score change:

- A crash fix.
- A performance fix that leaves output identical.
- A documentation fix.

### The awkward middle, called out deliberately

Adding a risk rule almost always moves some existing scores, which is a MAJOR
change by the rule above, but feels like a MINOR one. **Treat it as MAJOR.**

The reason is that the score is the product. A repository that scored 82 last
month and scores 74 today with no code change is the single most confusing thing
this tool could do, and a version number is the only signal a user has that
something moved. The same applies to a scoring-constant change: shipping it as a
PATCH and telling people to read the changelog is not good enough.

The exception is a change that cannot affect an existing repository — a rule
about a language nobody has files for, say — which is a MINOR.

### Pre-1.0

Until `v1.0.0` exists, minor versions may break. That is the convention and it is
stated in `CHANGELOG.md`, but it is not an excuse to break things without saying
so in the release notes.

### Tagging

- Tags are `vX.Y.Z` exactly, with no prefix variations.
- **Version comes from the tag.** Never bump a version in a commit; the tag is
  the source of truth and a version string in a file can disagree with it.
- Annotated tags, not lightweight. `git tag -a v1.0.0 -m "v1.0.0"`.
- Tags are immutable. A tag pushed by mistake is deleted and re-pushed, with a
  note in the release notes, rather than being moved in place.

---

## Release checklist

The release workflow is triggered by a `v*` tag and does the verification,
cross-compilation, checksums, and publication. This is the human part.

### Before tagging

1. **The tree is clean.** `git status` empty, `git fetch --prune`, no
   uncommitted changes.
2. **CI is green on the commit you are tagging**, including the cross-compile
   matrix and the race detector. Not green on `main` and green on a branch is not
   green.
3. **`gofmt -l .` is empty and `go vet ./...` is clean.**
4. **`go mod tidy -diff` produces no diff.** A release with drifting module files
   is a release whose provenance is slightly untrue.
5. **`scripts/prepare-release.sh` passes.** It embeds the dashboard, and refuses
   to continue if any of the above is untrue.
6. **`scripts/test-install.sh` passes.** The checksum verification and platform
   mapping are asserted there, and they are what a user's first command depends
   on.
7. **The dashboard is current.** If `dashboard/` changed, `npm run build` has
   been run and copied into `internal/server/assets/`. A release shipping a stale
   dashboard is a release that does not contain the code in the repository.
8. **`CHANGELOG.md` describes the release**, written for someone deciding whether
   to upgrade rather than as a commit log.
9. **The version string in no source file.** `Version` is injected by the linker;
   a hardcoded version anywhere is a bug.
10. **The release notes contain every MAJOR and MINOR change**, because people
    read them and do not read commits.

### Tagging

```bash
git fetch --prune
git checkout main
git pull --ff-only
git status                      # must be clean
git log --oneline -5            # confirm what you are about to tag

git tag -a v1.0.0 -m "v1.0.0: <one-line summary>"
git push origin main
git push origin v1.0.0
```

Pushing the tag triggers the workflow. Watch it. A failed release is fixed by
fixing forward and re-tagging a new patch version, never by deleting the tag —
except where the release is genuinely broken and the tag has not been
downloaded by anyone, in which case delete it and say so.

### After the workflow succeeds

1. **Verify the release page** shows every expected asset: six archives plus
   `checksums.txt`.
2. **Install from the release**, on at least one platform, using `install.sh`.
   Not `go install` — the release artefacts are a different build and the point
   is to test what users get.
3. **Check the Action's download URL.** It is derived from the release spec and
   it has been wrong before; a successful release with an Action that cannot
   fetch the binary is not a release that works.
4. **Verify `lensyxe version`** reports the tagged version rather than `dev`. If
   it says `dev`, the linker symbols in `.goreleaser.yaml` have drifted from
   `cmd/lensyxe/version.go`, and the binary is unidentifiable.
5. **Confirm the checksum verification actually fails** when given a corrupted
   archive. Testing only the happy path leaves the most important property
   unverified.

---

## Security advisories

`SECURITY.md` is the public-facing version. This is the internal procedure.

### Reporting

- Intake is GitHub's private vulnerability reporting on the repository's Security
  tab. There is deliberately no public intake path, because a public issue is a
  disclosure.
- **Never** ask a reporter to open a public issue, and never triage a suspected
  vulnerability in the open.
- If a vulnerability is reported publicly, treat the disclosure as having
  already happened and go straight to assessment.

### Response targets

| Stage | Target |
| :--- | :--- |
| Acknowledgement | 72 hours |
| Initial assessment, including a judgement on whether it is a vulnerability | 7 days |
| Fix released | 30 days from confirmation |
| Public disclosure | On release |

Missing an acknowledgement deadline is worse than missing a fix deadline. The
reporter cannot tell the difference between "busy" and "ignored", and a silent
report generates the worst possible second impression.

### Assessment

Classify before fixing, because the classification changes who must be involved:

- **Critical** — code execution, credential exposure, or a supply-chain
  compromise. Fix on an expedited timeline, and treat the dependency graph as
  suspect.
- **High** — data disclosure, or a bypass of a documented security property.
  30 days.
- **Moderate** — a weakened guarantee without a practical exploit. Next
  release.
- **Low** — hardening. When convenient.

If it is **not** a vulnerability, say so with the reasoning. "This is by design,
here is the design decision" is a complete and correct answer, and withholding it
to avoid disappointing someone is worse than telling them.

### Disclosure

- Coordinate the public date with the reporter. Credit them unless they prefer
  otherwise.
- Do not publish a technical write-up before the fix ships.
- Publish the advisory even for Low-severity findings. A silent fix teaches
  nobody that the channel works.
- If a dependency is the cause, the advisory must say which version fixes it and
  what the workaround is. "Upgrade" is not a workaround for a vulnerable version.

### Proactively

- Act on GitHub Dependabot security updates on the project's timeline, not
  automatically. They are deliberately not auto-merged.
- Audit `SECURITY.md` claims when the implementation changes. If a claim is no
  longer true, the advisory channel is a promise about behaviour that no longer
  holds, which is worse than having made no promise.

---

## Commit authority

Today there is one maintainer with commit rights. That is recorded here so that
the absence is a decision rather than an oversight.

Rights are not merged from pull requests by anyone without them. When a second
maintainer is added, the decision is explicit and written down, and it is never
automatic as a reward for contributing.

Two commitments about how this stays honest:

- **No self-approval.** A maintainer's own pull request gets the same CI as
  anyone else's, and gets merged only when it is green.
- **No unreviewed changes to the release path.** Anything touching
  `.goreleaser.yaml`, `install.sh`, `.github/workflows/release.yml`, or the
  Action's install step is security-relevant by definition and is reviewed even
  when the author is the only person with rights.

---

## When the project needs a different shape

Written down in advance so that growth does not turn into drift.

**At a second maintainer:** write down how decisions get made, because implicit
consensus stops working immediately.

**At sustained outside contribution:** add a `GOVERNANCE.md` and a decision
record for anything contentious.

**At `v1.0.0`:** stop calling pre-1.0 breaking changes acceptable and start
holding the line on the documented interface.

**When the Action or the installer carries a vulnerability:** treat as Critical
regardless of exploitability. Those two artefacts execute code on someone else's
machine, which is a higher trust position than the CLI's.

---

## Dependabot and automation

Configured in `.github/dependabot.yml`. The policy, in one paragraph: monthly
for Go modules, weekly for Actions, grouped so a bump is reviewable in
isolation, never auto-merged.

Treat a dependency pull request as a real review. The point of grouping them is
that each one is small enough to actually read, and skipping that is how a
supply-chain change walks in behind nine unrelated bumps.

Two specific rules:

- **A bump that changes a direct dependency needs a reason in the description.**
  It changes what the project ships, which is a different kind of decision from
  moving a transitive patch.
- **A bump that moves output must be rejected or called out.** If a dependency
  bump changes a golden file, that is either a real behaviour change or a
  determinism failure. Neither should pass silently.