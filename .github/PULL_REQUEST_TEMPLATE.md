<!--
  Kept deliberately short. A long template is a long thing to read before
  writing anything, and contributors stop reading checklists they did not write.

  What is here is the subset of Lensyxe's invariants that a reviewer cannot
  verify by reading the diff alone. The full list is in CONTRIBUTING.md.
-->

## What this changes

<!-- One or two sentences. What is different after this merge, and why. -->

## Why

<!--
  The reason the change exists, and what you rejected. A diff shows the what.

  If this fixes a bug, link the issue. If it closes a disagreement about a
  threshold, say which side won and why — those decisions are worth recording
  because the next person will otherwise relitigate them.
-->

Closes #

## Checklist

**Required for every pull request:**

- [ ] `go test ./...` passes
- [ ] `go vet ./...` is clean and `gofmt -l .` is empty
- [ ] Added or updated tests for the behaviour this changes. A bug fix without a
      regression test will be asked for one.
- [ ] `go test ./... -update` was run **and the diff reviewed**, if the report
      schema or any report layout changed. Golden files are reviewed artefacts,
      not a formality.
- [ ] If a flag, config key, Action input, or scoring constant changed, the
      documentation changed in the same commit. `go test ./cmd/lensyxe/` enforces
      this and will fail otherwise.
- [ ] If a new test package was added, it registers the `-update` flag
      (`pwsh scripts/make-golden-flags.ps1`), or `go test ./... -update` fails in
      the packages that lack it.

**If any of these apply, please confirm:**

- [ ] Scoring still performs no I/O. `internal/metrics` and `internal/risk` take
      values and return values; anything time-dependent is measured upstream and
      passed in.
- [ ] No metric was invented. A signal that cannot be computed is reported as
      not-applicable and excluded from the weighted total, not defaulted to zero
      and not named in a way that implies more than it delivers.
- [ ] The AI layer still cannot produce a number. Replies containing figures that
      were not in the computed findings are discarded, not parsed.
- [ ] Output stays deterministic. Every sort has an explicit total ordering and
      no map iteration order reaches stdout, JSON, or a golden file.
- [ ] No required network access was added. The only outbound request in the
      project is the optional `--explain` layer.
- [ ] `CGO_ENABLED=0` still builds. The SQLite driver is pure Go specifically so
      the release matrix cross-compiles without a C toolchain.
- [ ] Exit codes are unchanged, or the change to them is intentional and called
      out. `0` clean, `1` broken, `2` threshold breach — pipelines depend on the
      distinction.
- [ ] Any new dependency is justified in the description. The dependency list is
      a permanent supply-chain liability for a tool whose value proposition is
      being trustworthy.

## Testing notes

<!--
  Anything a reviewer cannot infer: what you tested by hand, what you could not
  test and why, and anything that looks odd but is deliberate.
-->