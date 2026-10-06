//go:build ignore

// Validates that the GitHub Actions workflows parse as YAML and reports their
// top-level structure.
//
// It exists because a workflow with a YAML error does not fail loudly: GitHub
// shows a red annotation on the file and simply does not run the job, so a
// broken workflow looks identical to a passing one that reported nothing.
//
// Uses the yaml.v3 already in the module graph, so it needs no new dependency.
//
//	cd dashboard && npm run build && cd ..
//	go run scripts/check-workflows.go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func main() {
	dir := ".github/workflows"
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read %s: %v\n", dir, err)
		os.Exit(1)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml")) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Fprintf(os.Stderr, "no workflows in %s\n", dir)
		os.Exit(1)
	}

	failed := false
	for _, name := range names {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", name, err)
			failed = true
			continue
		}

		var doc struct {
			Name string                    `yaml:"name"`
			On   map[string]any            `yaml:"on"`
			Jobs map[string]map[string]any `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", name, err)
			failed = true
			continue
		}
		if len(doc.Jobs) == 0 {
			fmt.Fprintf(os.Stderr, "FAIL %s: parses, but declares no jobs\n", name)
			failed = true
			continue
		}

		var jobs []string
		for j := range doc.Jobs {
			jobs = append(jobs, j)
		}
		sort.Strings(jobs)

		triggers := make([]string, 0, len(doc.On))
		for t := range doc.On {
			triggers = append(triggers, t)
		}
		sort.Strings(triggers)

		fmt.Printf("ok   %-18s name=%-6q triggers=[%s] jobs=[%s]\n",
			name, doc.Name, strings.Join(triggers, ","), strings.Join(jobs, ","))
	}

	if failed {
		os.Exit(1)
	}
	fmt.Printf("\n%d workflow(s) parsed\n", len(names))
}
