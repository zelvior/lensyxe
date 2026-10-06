package tests

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/internal/report"
	"github.com/zelvior/lensyxe/internal/risk"
	"github.com/zelvior/lensyxe/pkg/models"
)

// Small helpers the benchmarks reference, kept here so benchmark_test.go reads
// as a benchmark file rather than as an import list.

// memStats captures allocation totals for one measured run.
type memStats struct {
	// totalAlloc is the cumulative bytes allocated during the measured run.
	totalAlloc uint64
	// mallocs is the number of distinct allocations.
	mallocs uint64
}

// measure runs fn once with allocation accounting enabled.
//
// runtime.ReadMemStats stops the world, which is why it is called once around a
// whole analysis rather than around each step. A second call inside the
// measured region would dominate the number it is trying to report.
func measure(fn func()) memStats {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)

	return memStats{
		totalAlloc: after.TotalAlloc - before.TotalAlloc,
		mallocs:    after.Mallocs - before.Mallocs,
	}
}

// humanBytes renders a byte count with a unit, for log lines.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return itoa(int(n)) + " B"
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	whole := n / div
	frac := (n % div) * 10 / div
	return itoa(int(whole)) + "." + itoa(int(frac)) + " " + []string{
		"KB", "MB", "GB", "TB",
	}[exp]
}

// itoa avoids importing strconv for two call sites in test code.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// riskAssess is the risk engine entry point, indirected so the benchmark file
// does not need to import it alongside the other analysis packages.
var riskAssess = func(
	code models.CodeStats,
	git models.GitStats,
	deps models.DependencyStats,
	health models.Health,
) []models.Risk {
	return risk.Assess(code, git, deps, health)
}

// renderJSON is the JSON renderer, indirected for the same reason.
var renderJSON = func(w interface{ Write([]byte) (int, error) }, snap *models.Snapshot) error {
	// report.RenderJSON takes an io.Writer; the narrower interface above keeps
	// the discardWriter type local to this file.
	return report.RenderJSON(writerFunc(w.Write), snap)
}

// writerFunc adapts a method value to io.Writer.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// discardWriter counts bytes without retaining them.
//
// It exists instead of io.Discard so the benchmark can assert that something was
// actually written: a renderer that silently produced nothing would otherwise
// benchmark as infinitely fast.
type discardWriter struct{ n int }

func (d *discardWriter) Write(p []byte) (int, error) {
	d.n += len(p)
	return len(p), nil
}

// workspaceFixture builds a small npm workspace for the monorepo benchmark.
func workspaceFixture(t testing.TB) string {
	t.Helper()

	files := map[string]string{
		"package.json":                    `{"name":"root","private":true,"workspaces":["packages/*"]}`,
		"packages/core/package.json":      `{"name":"@bench/core"}`,
		"packages/core/src/index.ts":      tsSource(180),
		"packages/core/src/index.test.ts": tsSource(60),
		"packages/ui/package.json":        `{"name":"@bench/ui"}`,
		"packages/ui/src/index.ts":        tsSource(240),
		"packages/util/package.json":      `{"name":"@bench/util"}`,
		"packages/util/src/index.ts":      tsSource(120),
	}

	// Names are sorted by newRepoWithFiles, so the tree is identical between
	// runs regardless of map iteration order.
	dir := newRepoWithFiles(t, files)

	// newRepoWithFiles writes a go.mod so the dependency analyzer has a
	// manifest. For this workspace fixture that would register a second
	// ecosystem alongside npm and halve the spread score, which is not what the
	// monorepo benchmark is measuring, so it is removed.
	if err := os.Remove(filepath.Join(dir, "go.mod")); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove go.mod: %v", err)
	}

	return dir
}

// tsSource renders TypeScript with a controlled number of branch points.
func tsSource(lines int) string {
	var b strings.Builder
	b.WriteString("export function compute(seed: number): number {\n")
	b.WriteString("  let total = 0;\n")

	perIter := (lines - 4) / 4
	if perIter < 1 {
		perIter = 1
	}
	for i := 0; i < perIter; i++ {
		b.WriteString("  if (seed > " + itoa(i) + ") {\n")
		b.WriteString("    total += seed * " + itoa(i%5) + ";\n")
		b.WriteString("  }\n")
	}
	b.WriteString("  return total;\n}\n")
	return b.String()
}
