package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `blast <file>` used to be read as `blast <directory>`. The filesystem then
// handed git a file as its working directory, and the command failed with
//
//	fork/exec ...git.exe: The directory name is invalid
//
// An opaque OS error naming git rather than the path the user typed, for what
// is plainly a file review. Two arguments worked, so the documented way to name
// a file required a redundant "." in front of it.

func TestSplitTargetTreatsASingleFileAsAFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "profiler.go")
	if err := os.WriteFile(file, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &app{}
	a.cfg.Target = dir

	target, files := splitTargetAndFiles(a, []string{file})
	if target != dir {
		t.Errorf("target = %q, want the configured directory %q", target, dir)
	}
	if len(files) != 1 || files[0] != file {
		t.Errorf("files = %v, want exactly [%s]", files, file)
	}
}

func TestSplitTargetTreatsASingleDirectoryAsTheTarget(t *testing.T) {
	dir := t.TempDir()
	a := &app{}
	a.cfg.Target = "."

	target, files := splitTargetAndFiles(a, []string{dir})
	if target != dir {
		t.Errorf("target = %q, want %q", target, dir)
	}
	if len(files) != 0 {
		t.Errorf("files = %v, want none: a lone directory is a target, not a file", files)
	}
}

// A path that does not exist is left to the directory check, which reports it in
// terms the user can act on. Guessing here would turn a typo into a silent
// analysis of the wrong place.
func TestSplitTargetLeavesAMissingPathToTheDirectoryCheck(t *testing.T) {
	a := &app{}
	a.cfg.Target = "."

	missing := filepath.Join(t.TempDir(), "does-not-exist.go")
	target, files := splitTargetAndFiles(a, []string{missing})
	if files != nil {
		t.Errorf("a missing path was guessed to be a file: %v", files)
	}
	if target != missing {
		t.Errorf("target = %q, want %q", target, missing)
	}
}

func TestSplitTargetWithTwoArgumentsIsTargetThenFiles(t *testing.T) {
	dir := t.TempDir()
	a := &app{}
	a.cfg.Target = "."

	target, files := splitTargetAndFiles(a, []string{dir, "a.go", "b.go"})
	if target != dir {
		t.Errorf("target = %q, want %q", target, dir)
	}
	if len(files) != 2 {
		t.Errorf("files = %v, want two", files)
	}
}

func TestSplitTargetWithNoArgumentsUsesTheConfiguredTarget(t *testing.T) {
	a := &app{}
	a.cfg.Target = "/somewhere"
	target, files := splitTargetAndFiles(a, nil)
	if target != "/somewhere" || files != nil {
		t.Errorf("got (%q, %v), want (\"/somewhere\", nil)", target, files)
	}
}

// requireDir is the second half of the fix: whatever reaches it as a target must
// be a directory, and the error has to name the path rather than surface later
// from whichever tool exec'd first.
func TestRequireDirRejectsAFileWithAReadableError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.go")
	if err := os.WriteFile(file, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := requireDir(file)
	if err == nil {
		t.Fatal("a file was accepted as a target")
	}
	// The message must name the path and say what was expected. An error that
	// only says "invalid argument" sends the reader looking in the wrong place.
	if !strings.Contains(err.Error(), file) {
		t.Errorf("the error does not name the path: %v", err)
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("the error does not say what was wrong: %v", err)
	}
}

func TestRequireDirReportsAMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	err := requireDir(missing)
	if err == nil {
		t.Fatal("a missing path was accepted")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("the error does not name the path: %v", err)
	}
}

func TestRequireDirAcceptsADirectory(t *testing.T) {
	if err := requireDir(t.TempDir()); err != nil {
		t.Errorf("a directory was rejected: %v", err)
	}
}
