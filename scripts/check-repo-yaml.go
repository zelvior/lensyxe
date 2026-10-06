//go:build ignore

// Validates every YAML file the repository ships that the Go build never reads.
//
// Dependabot configuration, Action metadata, issue forms, and the release spec
// are all parsed by GitHub rather than by Go, so a mistake in any of them is
// invisible to `go build`, `go vet`, and the test suite. Each failure mode is
// also quiet: a malformed dependabot.yml means dependencies silently stop being
// updated, and a malformed Action means it does not run.
//
// Syntax checking alone is the floor, not the goal. This also checks the few
// structural properties that are cheap to assert and expensive to discover:
// required top-level keys, and that the Action declares an input for each
// `inputs.<name>` the composite steps actually reference.
//
//	go run scripts/check-repo-yaml.go
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// semanticChecks are the files where structure matters beyond syntax, and what
// must be true of them.
//
// Syntax alone is applied to every YAML file found; this list is only for the
// files where a missing key means something silently stops working.
var semanticChecks = map[string][]string{
	".github/dependabot.yml": {"version", "updates"},
	".goreleaser.yaml":       {"version", "builds"},
	"action/action.yml":      {"name", "description", "runs"},
	".lensyxe.yml":           nil,
}

// skipDirs are trees whose YAML is generated or vendored, and not ours to keep
// valid.
//
// dashboard/out and internal/server/assets are a Next.js export, dashboard/.next
// is a build cache, and node_modules is 301 packages of someone else's
// configuration. Validating generated or vendored YAML produces findings nobody
// can act on, and walks thousands of files to produce them.
//
// ide/vscode/node_modules matters here for an extra reason: one popular npm
// dependency ships Go source as well, which is why ide/vscode carries a nested
// go.mod to keep the Go toolchain out of it. A validator that ignored the
// boundary would reintroduce the problem it was meant to prevent.
var skipDirs = map[string]bool{
	"dashboard/out":           true,
	"dashboard/.next":         true,
	"dashboard/node_modules":  true,
	"internal/server/assets":  true,
	"ide/vscode/node_modules": true,
	"ide/vscode/out":          true,
	".git":                    true,
	"node_modules":            true,
}

// actionInputRef matches `${{ inputs.foo }}` and
// `${{ inputs['foo'] }}`, the two forms a composite action may reference.
var actionInputRef = regexp.MustCompile(`\$\{\{\s*inputs\.([A-Za-z0-9_-]+)`)

func main() {
	problems := 0
	checked := 0

	// Every YAML file in the tree, discovered rather than listed.
	//
	// A hardcoded list has the wrong failure mode: it validates the files
	// someone remembered and silently skips the file added last week. Since the
	// whole point of this check is that Go's build cannot see any of this,
	// skipping one defeats it.
	files, err := findYAML(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "walk: %v\n", err)
		os.Exit(1)
	}
	sort.Strings(files)

	for _, f := range files {
		checked++
		require, semantic := semanticChecks[f]
		if errs := checkYAML(f, f, require, semantic); len(errs) > 0 {
			fmt.Printf("FAIL %s\n", f)
			for _, m := range errs {
				fmt.Printf("       %s\n", m)
			}
			problems += len(errs)
			continue
		}
		if semantic {
			fmt.Printf("ok   %s  (structure checked)\n", f)
		} else {
			fmt.Printf("ok   %s\n", f)
		}
	}

	// Every file with an entry in semanticChecks must have been found, or the
	// entry has gone stale after a rename and the check silently stopped
	// applying.
	for path := range semanticChecks {
		if !contains(files, path) {
			fmt.Printf("FAIL %s\n", path)
			fmt.Printf("       %s\n", "listed in semanticChecks but not present in the tree; "+
				"the rule has gone stale after a rename")
			problems++
		}
	}

	if errs := checkActionInputs(); len(errs) > 0 {
		fmt.Println("FAIL action/action.yml input references")
		for _, m := range errs {
			fmt.Printf("       %s\n", m)
		}
		problems += len(errs)
	} else {
		fmt.Println("ok   action/action.yml input references")
	}

	fmt.Printf("\n%d file(s) checked, %d problem(s)\n", checked, problems)
	if problems > 0 {
		os.Exit(1)
	}
}

// findYAML walks root and returns every YAML file, skipping generated trees.
func findYAML(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(path)
		rel = strings.TrimPrefix(rel, "./")

		if d.IsDir() {
			if rel != "." && skipDirs[rel] {
				return fs.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".yml" || ext == ".yaml" {
			out = append(out, rel)
		}
		return nil
	})
	return out, err
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func checkYAML(path, label string, require []string, semantic bool) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{err.Error()}
	}

	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return []string{fmt.Sprintf("does not parse as YAML: %v", err)}
	}

	var errs []string
	for _, k := range require {
		if _, ok := doc[k]; !ok {
			errs = append(errs, fmt.Sprintf("%s config has no %q key", label, k))
		}
	}

	// An empty document parses cleanly and means nothing, which is a confusing
	// way to fail. Only files we expect to have content are checked for this:
	// an intentionally empty YAML file is legitimate.
	if semantic && len(doc) == 0 {
		errs = append(errs, "parses but is empty")
	}

	return errs
}

// checkActionInputs asserts that every input the Action declares is referenced,
// and that every input referenced in the steps is declared.
//
// The first direction is the one that matters: an input nobody reads is a
// setting a user can change with no effect, which is worse than not offering it.
// internal/ci asserts the same property, and this is the copy that runs before
// the Action is ever published.
func checkActionInputs() []string {
	data, err := os.ReadFile("action/action.yml")
	if err != nil {
		return []string{err.Error()}
	}

	var doc struct {
		Inputs map[string]any `yaml:"inputs"`
		Runs   struct {
			Steps []struct {
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return []string{fmt.Sprintf("does not parse as YAML: %v", err)}
	}

	declared := map[string]bool{}
	for name := range doc.Inputs {
		declared[name] = true
	}

	referenced := map[string]bool{}
	for _, m := range actionInputRef.FindAllStringSubmatch(string(data), -1) {
		referenced[m[1]] = true
	}

	var errs []string

	unused := []string{}
	for name := range declared {
		if !referenced[name] {
			unused = append(unused, name)
		}
	}
	sort.Strings(unused)
	for _, name := range unused {
		errs = append(errs, fmt.Sprintf(
			"input %q is declared but never referenced; a user can set it with no effect", name))
	}

	undeclared := []string{}
	for name := range referenced {
		if !declared[name] {
			undeclared = append(undeclared, name)
		}
	}
	sort.Strings(undeclared)
	for _, name := range undeclared {
		errs = append(errs, fmt.Sprintf(
			"input %q is referenced by a step but never declared", name))
	}

	return errs
}
