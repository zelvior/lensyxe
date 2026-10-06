# Security Policy

Lensyxe reads your source code. This document describes what it does with that
code, what it deliberately does not do, and how to report a problem.

---

## Table of contents

- [Supported versions](#supported-versions)
- [Reporting a vulnerability](#reporting-a-vulnerability)
- [Local-first principles](#local-first-principles)
- [Secret isolation](#secret-isolation)
- [What leaves your machine](#what-leaves-your-machine)
- [Supply chain](#supply-chain)
- [The local API server](#the-local-api-server)
- [GitHub Action security](#github-action-security)
- [Data files written on disk](#data-files-written-on-disk)
- [What we consider out of scope](#what-we-consider-out-of-scope)

---

## Supported versions

Security fixes land on the latest release only. There is no long-term support
branch, because the project has no release yet and adding one now would be
ceremony.

| Version | Supported |
| :--- | :--- |
| `latest` release | Yes |
| `v1.0.0-rc*` | Yes, until `v1.0.0` |
| Any older tag | No |

---

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Use GitHub's private vulnerability reporting on the Security tab of the
repository. If that is unavailable, email the maintainer at the address listed in
the repository metadata.

Please include:

- What the issue is, and what an attacker gains.
- Steps to reproduce, ideally minimal.
- The version (`lensyxe version`) and platform.
- Whether any data already on your machine is exposed.

### What to expect

| Stage | Target |
| :--- | :--- |
| Acknowledgement | 72 hours |
| Initial assessment, including whether it is a vulnerability at all | 7 days |
| Fix released | 30 days from confirmation, sooner if practical |
| Public disclosure | On release, credited unless you prefer otherwise |

If a report is not a vulnerability, you will get an explanation of why rather
than silence. If it is, you will get the fix and the credit.

### Disclosure policy

- Report privately first. Do not open a pull request for a vulnerability.
- We will not pursue legal action over good-faith research that stays within
  these principles, avoids user data, and gives us reasonable time to fix.
- We will not ask you to keep a bug secret indefinitely.

---

## Local-first principles

These are design commitments, not aspirations. They are the reason Lensyxe can be
trusted with a private repository.

### Zero mandatory telemetry

There is no analytics, no crash reporting, no update check, no usage ping, and no
phone-home. Lensyxe makes no outbound request as part of `analyze`, `compare`,
`history`, `watch`, or `serve`.

There is no opt-out switch, because there is nothing to opt out of.

If you want to verify this rather than trust it, `internal/ai` is the only
package in the tree that constructs an HTTP client, and it is reachable only
through `--explain`.

### Local-first execution

All analysis happens in your process, against files on your disk. No analysis
result is uploaded, synced, or cached on a server.

The SQLite history database is a file in the analyzed tree (`.lensyxe/history.db`).
Deleting it removes the history completely; there is no remote copy.

### No remote configuration

Configuration comes from your files and your environment. There is no server that
can change what a threshold is, because there is no server.

### No code execution from analyzed input

Lensyxe reads files to count lines and detect language. It does not import,
execute, transpile, or evaluate the code it analyzes. A repository containing
hostile source does not gain anything by being scanned.

### Read-only on the analyzed tree

`analyze` writes exactly one thing: `.lensyxe/history.db`, and only when
persistence is enabled. `--no-persist` suppresses it. No analyzed file is
modified.

---

## Secret isolation

### The key is read by name, never stored

The AI layer takes an environment variable *name* through `ai_key_env`, not a
key value:

```yaml
# .lensyxe.yml — names a variable, contains no secret
ai:
  enabled: true
  ai_key_env: LENSYXE_AI_KEY
```

The rationale is that config files get committed, printed in bug reports, and
serialized into snapshots. A design where a secret can live in one is a design
where a secret eventually does.

### Secrets are never written anywhere

- Not to the config file.
- Not to the SQLite history database.
- Not to the JSON snapshot or any report.
- Not to logs or error messages.

`--explain` sends a request containing your findings to the provider you
configured. Those findings include file paths, line counts, and dependency names.
**If a path name or directory layout is itself sensitive, do not enable
`--explain` on that repository.** This is stated here because it is the one
place where local analysis data can leave the machine by design.

### `GITHUB_TOKEN` is never read

The CLI does not read `GITHUB_TOKEN`. The GitHub Action posts the comment using
the token it already holds; Lensyxe never loads a credential nothing consumes.
Loading it would widen the blast radius for no benefit.

---

## What leaves your machine

| Command | Network |
| :--- | :--- |
| `lensyxe analyze` | None |
| `lensyxe compare` | None |
| `lensyxe history` | None |
| `lensyxe watch` | None |
| `lensyxe serve` | None (listens on loopback only) |
| `lensyxe analyze --explain` | One request to the configured provider |

---

## Supply chain

### Dependencies

The dependency list is short and deliberate. Each addition is a permanent
maintenance and supply-chain liability for a tool whose entire value proposition
is being trustworthy.

Notable choices:

- **`modernc.org/sqlite`**, a pure-Go SQLite, rather than a cgo binding. This
  keeps `CGO_ENABLED=0` working, which is what makes cross-compiling to
  `darwin/arm64` possible without a C toolchain.
- No network client beyond the standard library and the optional AI layer.
- No telemetry SDK, ever.

### Release integrity

`install.sh` is the supported install path and:

1. Downloads `checksums.txt` **first** and verifies the archive against it before
   extracting anything. There is no flag to skip this.
2. Runs the freshly installed binary once and only reports success if
   `lensyxe version` works, so a corrupt or wrong-architecture download fails at
   install time rather than on your next command.
3. Never evaluates downloaded content as shell.
4. Refuses to write to a directory it cannot verify is writable, rather than
   silently falling back somewhere else.

The GitHub Action installs through `install.sh` from the resolved tag rather than
downloading the archive itself, so it inherits the checksum verification. A
second copy of that download logic is exactly how the Action previously ended up
with a wrong URL and no integrity check.

### Reproducibility

`scripts/prepare-release.sh` refuses to build a release from a tree that is
unformatted, has untidy module files, fails `go vet`, or whose linker version
symbols have been renamed. A release built from a messy tree is worse than no
release, because the tag then claims something untrue.

---

## The local API server

`lensyxe serve` is intended for a local dashboard.

- It binds **loopback only**. It is not reachable from the network.
- Every endpoint is **read-only**. There is no endpoint that writes, executes, or
  mutates anything. Analysis is triggered by the CLI, not by an HTTP request.
- The embedded dashboard is static. No third-party script is loaded from a CDN,
  because a CDN is a supply-chain dependency and an outbound request.
- An unknown `/api/` path returns a JSON error, never the dashboard HTML, so an
  API client fails loudly rather than silently parsing HTML.

Do not port-forward or reverse-proxy the server to an untrusted network. It has
no authentication, by design: it is not intended to face one.

---

## GitHub Action security

The Action is a thin wrapper. Notable properties, and the reasoning behind them:

- **Permissions are explicit.** `contents: read` by default, escalated only where
  a step genuinely needs it, so a workflow that adopts the Action does not
  silently gain write access.
- **`pull_request_target` guidance is explicit.** That trigger runs with write
  access to the base repository even for forks, so checking out untrusted code in
  it is a privilege-escalation hazard. See
  [docs/GITHUB_ACTION.md](docs/GITHUB_ACTION.md) for the safe pattern.
- **The cache key includes the version**, the runner OS, and the architecture, so
  a cached binary cannot be served for a different platform.
- **Version input is validated.** Anything that is not a plain version string is
  rejected before it reaches a URL, so the input cannot be used for path
  traversal or argument injection.

If you fork the Action, keep these properties. They are the reason it is safe to
run on a private repository.

---

## Data files written on disk

| Path | Contents | Delete to clear |
| :--- | :--- | :--- |
| `.lensyxe/history.db` | Analysis snapshots, including file paths | Removes all history |
| `.lensyxe/pr-comment.md` | Generated PR comment body | Removes the file |
| `lensyxe` binary | The program itself | Removes the executable |

Snapshots contain file paths, line counts, complexity estimates, and dependency
names. They do not contain file contents. If path names are sensitive, treat the
history database as sensitive data.

---

## What we consider out of scope

These are not vulnerabilities:

- **Lensyxe reports a low health score on a healthy repository.** Scoring is a
  model, and models are arguable. See
  [docs/SCORING_SPEC.md](docs/SCORING_SPEC.md) for the formulas and thresholds.
- **A threshold fires that you disagree with.** That is a policy disagreement.
  Configure the threshold.
- **Output differs between two machines on the same commit.** It should not, and
  if it does that *is* a bug worth reporting, because determinism is a promise.
- **A file in an ignored directory was still scanned.** Check `ignore_dirs`; it
  replaces the default list rather than extending it, so dropping an entry
  re-enables scanning.
- **You enabled `--explain` and findings went to a third party.** Documented
  behaviour, described above.

---

## License

Lensyxe is released under the [MIT License](LICENSE).