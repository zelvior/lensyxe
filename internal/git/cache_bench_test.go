package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// resetCache clears the process-global history cache.
//
// It exists for tests and benchmarks. Exported to nothing outside the package on
// purpose: production code must never be able to clear the cache, because doing
// so silently turns a fast path back into nine subprocesses.
func resetCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cache = map[historyKey]historyOutputs{}
}

// BenchmarkAnalyzeHistory measures a history read with and without the cache,
// within one process.
//
// Measuring this across separate `lensyxe analyze` invocations measures nothing
// useful: the cache is process-global, so every new command starts cold. The
// case it exists for is a long-lived process -- `lensyxe watch` re-analyzing on
// every save, or `lensyxe serve` behind its reload button -- where the commit
// has not moved and the same history is read again and again.
func BenchmarkAnalyzeHistory(b *testing.B) {
	// The repository root is two levels up; a test's working directory is this
	// package, which has no .git of its own.
	root, err := filepath.Abs("../..")
	if err != nil {
		b.Fatalf("resolve repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		b.Skip("not inside a git repository")
	}

	cfg := DefaultConfig()
	cfg.WindowDays = 90
	ctx := context.Background()

	if _, err := Analyze(ctx, root, cfg); err != nil {
		b.Fatalf("warm-up analyze: %v", err)
	}

	b.Run("cold", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			// Clearing before each timed call is what makes this cold, and it is
			// the only honest way to measure the uncached path from inside a
			// process that has already warmed the cache once.
			b.StopTimer()
			resetCache()
			b.StartTimer()

			if _, err := Analyze(ctx, root, cfg); err != nil {
				b.Fatalf("analyze: %v", err)
			}
		}
	})

	b.Run("cached", func(b *testing.B) {
		if _, err := Analyze(ctx, root, cfg); err != nil {
			b.Fatalf("warm analyze: %v", err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := Analyze(ctx, root, cfg); err != nil {
				b.Fatalf("analyze: %v", err)
			}
		}
	})

	// The sliding window means an entry is only valid for the day it was
	// computed for. This reports how long a cached entry can be trusted, which
	// is a property of the key and worth stating explicitly.
	b.Run("key-overhead", func(b *testing.B) {
		commit := "0123456789abcdef0123456789abcdef01234567"
		since := time.Now()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			k, ok := newHistoryKey(root, commit, 90, since)
			if !ok {
				b.Fatal("expected a cacheable key")
			}
			if _, hit := k.lookup(); hit {
				b.Fatal("an empty cache produced a hit")
			}
		}
	})
}
