# Risky Go example

A deliberately unhealthy Go project used by the integration suite.

It scores badly for identifiable, documented reasons, so the tests can assert on
specific findings rather than on "the score went down":

- One oversized file at roughly 900 code lines, which puts it well over the
  size threshold.
- No test files at all, so the test-ratio risk fires.
- One dependency, so the dependency metrics have something to measure.

## What this example does and does not trigger

It is worth being precise, because the distinction is the interesting part:

| Signal | Status | Why |
| :--- | :--- | :--- |
| Size | Over threshold | `legacy/order.go` is about 900 code lines |
| Churn | Absent | The directory is not a git repository, so churn is 0 |
| Complexity | `moderate` (~10.6) | Below the `high` band the confirmation rule requires |

So the file is a **hotspot candidate** and is **not** a confirmed hotspot.
Confirmation requires size, churn, and complexity together, and this fixture
deliberately satisfies only one of them.

That is intentional. A fixture whose single file were confirmed would leave the
subtler path untested: the one where a large file is reported and the report
still explains *why* it was not escalated. The confirmed case is covered by the
generated fixtures in the benchmark suite, which carry a real git history.

If a gate does not reject this repository, the gate is not working.

## Regenerating

The bulk of `legacy/order.go` is mechanical and generated, because a 900-line
hand-written file would be unreviewable:

```bash
go run scripts/gen-risky-example.go
```