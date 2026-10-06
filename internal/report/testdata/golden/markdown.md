# demo: engineering health

Overall adequate. 2 risk(s) detected; highest is 🔴 CRITICAL (Confirmed hotspot).
## Health score

**71.5 / 100** (grade C)

| Dimension | Score | Weight | Detail |
| --- | ---: | ---: | --- |
| Code health | 55.0 | 40% | size 40, hotspots 20% of lines, cohesion 50 |
| Dependency health | 90.0 | 30% | count 86, reproducibility 80, spread 50 |
| Maintainability (Git) | 75.0 | 30% | cadence 20, freshness 100, churn 83, bus factor 85 |

## Code

- **Files**: 32 source, 8 test (ratio 0.20)
- **Lines**: 2400 code, 600 blank/comment, 3000 total
- **Average**: 60.0 LOC per file, largest 900 LOC

| Language | Files | Lines | Share |
| --- | ---: | ---: | ---: |
| Go | 32 | 2000 | 83.3% |
| Shell | 8 | 400 | 16.7% |

- **Complexity** (lexical estimate): average 5.2, max 42.0 in `internal/engine.go`, max nesting 9

| Worst-complexity files | Lines | Complexity | Level | Nesting |
| --- | ---: | ---: | --- | ---: |
| `internal/engine.go` | 900 | 42.0 | very_high | 9 |
| `internal/report.go` | 400 | 5.3 | low | 4 |

### Hotspots

| File | Lines | Churn | Complexity | Classification |
| --- | ---: | ---: | ---: | --- |
| `internal/engine.go` | 900 | 220 | 42.0 | size,churn,complexity,confirmed **confirmed** |
| `internal/legacy.go` | 600 | 4 | 3.0 | size |

## Dependencies

- **Counts**: 4 direct, 2 dev, 6 total, 48 transitive
- **Reproducibility**: all ecosystems locked

| Ecosystem | Manifest | Lockfile | Direct | Dev | Transitive |
| --- | --- | --- | ---: | ---: | ---: |
| gomod | `go.mod` | `go.sum` | 4 | 2 | 48 |
| npm | `package.json` | `**none**` | 3 | 1 | 0 |

## Git

- **Branch**: `main` at `abc123def456`
- **Commits**: 100 total, 20 in the last 90 days (1.6/week)
- **Authors**: 3, bus factor 1, top author share 0%
- **Churn**: +500 / -120 lines across 15 files, concentration 0.20

| High-churn files | Commits | +Lines | -Lines |
| --- | ---: | ---: | ---: |
| `internal/engine.go` | 5 | 100 | 20 |

## Risks (2)

| Severity | Risk | Category | Impact |
| --- | --- | --- | ---: |
| 🔴 CRITICAL | Confirmed hotspot | code | 9.4 |
| 🟠 HIGH | Missing lockfile | dependency | 4.5 |

### 🔴 CRITICAL Confirmed hotspot

Subject: `internal/engine.go`

Hotspot detected in internal/engine.go.

| Evidence | Measurement | Value |
| --- | --- | ---: |
| LOC | 900 code lines | 900.00 |
| Churn | 220 lines modified in the git window | 220.00 |
| Complexity | 42 estimated cyclomatic complexity | 42.00 |

**Recommendation**: Split internal/engine.go until no unit exceeds the size threshold.

### 🟠 HIGH Missing lockfile

Subject: `package.json`

Missing lockfile for package.json.

| Evidence | Measurement | Value |
| --- | --- | ---: |
| Lockfile | no lockfile found | 0.00 |

---

_Lensyxe 0.3.0-test - schema v0.2 - deterministic local analysis, weights: code 40%, dependencies 30%, git 30%._
