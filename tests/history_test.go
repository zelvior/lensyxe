package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryRecordsAndReads(t *testing.T) {
	dir := t.TempDir()
	copyTree(t, filepath.Join("..", "examples", "healthy-go"), dir)

	// Two recorded runs, so the timeline has something to draw.
	for i := 0; i < 2; i++ {
		res := runCLI(t, dir, "analyze", ".", "--format", "json")
		if res.Code != ExitOK {
			t.Fatalf("record %d exited %d\n%s", i, res.Code, res.Combined())
		}
	}

	// History must be recorded beside the analyzed tree, not in whatever
	// directory the process happened to start from.
	db := filepath.Join(dir, ".lensyxe", "history.db")
	if _, err := os.Stat(db); err != nil {
		t.Fatalf("expected a history database at %s: %v", db, err)
	}

	res := runCLI(t, dir, "history", ".")
	if res.Code != ExitOK {
		t.Fatalf("history exited %d\n%s", res.Code, res.Combined())
	}
	if !strings.Contains(res.Stdout, "Engineering health timeline") {
		t.Errorf("expected a timeline:\n%s", truncate(res.Stdout, 500))
	}
}

func TestNoPersistWritesNoDatabase(t *testing.T) {
	dir := t.TempDir()
	copyTree(t, filepath.Join("..", "examples", "healthy-go"), dir)

	// The assertion below is only meaningful if the target starts empty. A
	// database that arrived with the fixture would make this fail for a reason
	// that has nothing to do with --no-persist, and the failure message would
	// point at the flag rather than at the polluted fixture.
	if _, err := os.Stat(filepath.Join(dir, ".lensyxe")); err == nil {
		t.Fatal("the copied fixture already contains .lensyxe; " +
			"the example directory has been polluted by an analysis run")
	}

	res := runCLI(t, dir, "analyze", ".", "--no-persist")
	if res.Code != ExitOK {
		t.Fatalf("analyze exited %d\n%s", res.Code, res.Combined())
	}
	if _, err := os.Stat(filepath.Join(dir, ".lensyxe")); err == nil {
		t.Error("--no-persist must not create a database")
	}
}
