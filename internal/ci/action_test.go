package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The Action definition must stay loadable by GitHub; a syntax error there
// breaks the whole integration, so it is validated here.
func TestActionDefinitionIsValid(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "action", "action.yml"))
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}

	var action struct {
		Name        string                              `yaml:"name"`
		Description string                              `yaml:"description"`
		Inputs      map[string]struct{ Default string } `yaml:"inputs"`
		Outputs     map[string]struct {
			Value string `yaml:"value"`
		} `yaml:"outputs"`
		Runs struct {
			Using string `yaml:"using"`
			Steps []struct {
				Name string `yaml:"name"`
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
				If   string `yaml:"if"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatalf("action.yml does not parse: %v", err)
	}

	if action.Runs.Using != "composite" {
		t.Errorf("runs.using = %q, want composite", action.Runs.Using)
	}
	if len(action.Runs.Steps) == 0 {
		t.Fatal("a composite action must define steps")
	}

	// Every input named in the specification must exist with the documented
	// default, so a consumer relying on it does not get a silent no-op.
	want := map[string]string{
		"version":                "latest",
		"fail-on-threshold":      "true",
		"comment-on-pr":          "true",
		"format":                 "markdown",
		"path":                   ".",
		"workflow-fail-severity": "none",
	}
	for name, def := range want {
		in, ok := action.Inputs[name]
		if !ok {
			t.Errorf("action.yml is missing the %q input", name)
			continue
		}
		if in.Default != def {
			t.Errorf("input %q default = %q, want %q", name, in.Default, def)
		}
	}

	// The required steps must all be present.
	joined := ""
	runs := ""
	for _, s := range action.Runs.Steps {
		joined += s.Name + "|" + s.Uses + "|" + s.Run + "\n"
		runs += s.Run + "\n"
	}
	for _, needle := range []string{
		"actions/checkout",      // checkout
		"actions/cache",         // binary caching
		"actions/github-script", // comment posting
	} {
		if !strings.Contains(joined, needle) {
			t.Errorf("action.yml has no step referencing %q", needle)
		}
	}

	// The analysis step invokes the binary through a variable, so the command
	// is matched by its parts rather than as one literal string.
	if !strings.Contains(runs, "lensyxe") || !strings.Contains(runs, "analyze") {
		t.Error("action.yml has no step running `lensyxe analyze`")
	}

	// The install step must fail loudly rather than producing a broken binary.
	if !strings.Contains(runs, "curl -fsSL") {
		t.Error("action.yml does not download the release binary")
	}
}

// A declared input that the action body never reads is worse than no input: a
// user sets it, sees no error, and reasonably concludes the setting applied.
//
// This caught two real cases: `format` was declared but hardcoded, and
// `min-health-score` duplicated `fail-under-health` without being wired to
// anything.
func TestEveryActionInputIsReferenced(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "action", "action.yml"))
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}

	var action struct {
		Inputs map[string]struct {
			Default     string `yaml:"default"`
			Description string `yaml:"description"`
		} `yaml:"inputs"`
		Runs struct {
			Steps []struct {
				Name string `yaml:"name"`
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
				// With is typed as raw nodes because its values are not all
				// strings: `fetch-depth: 0` and `persist-credentials: false`
				// are an int and a bool, and a string field would fail to
				// unmarshal the whole document.
				With map[string]yaml.Node `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatalf("action.yml does not parse: %v", err)
	}

	// Collect every expression that could reference an input: step bodies and
	// step `with:` blocks. `with:` values are rendered as raw YAML so a numeric
	// or boolean value still appears as text.
	var body strings.Builder
	for _, s := range action.Runs.Steps {
		body.WriteString(s.Run)
		body.WriteString(s.Uses)
		for _, v := range s.With {
			body.WriteString(v.Value)
		}
	}
	// Outputs are declared outside `runs` and referenced from other workflows,
	// so they are read from the raw document rather than the step bodies.
	raw := string(data)
	inputsSection, outputsSection := raw, ""
	if idx := strings.Index(raw, "\noutputs:"); idx >= 0 {
		inputsSection = raw[:idx]
		outputsSection = raw[idx:]
	}

	for name, spec := range action.Inputs {
		ref := "inputs." + name
		if !strings.Contains(body.String(), ref) &&
			!strings.Contains(outputsSection, ref) &&
			!strings.Contains(inputsSection, ref) {
			t.Errorf("input %q is declared but never referenced; setting it silently does nothing", name)
		}
		if strings.TrimSpace(spec.Description) == "" {
			t.Errorf("input %q has no description", name)
		}
	}
}

// A gate rejection must be distinguishable from a crash, so the action has to
// propagate exit 2 rather than flattening it.
func TestActionPreservesGateExitCode(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "action", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)

	if !strings.Contains(script, `[ "$status" -eq 2 ]`) {
		t.Error("the analyze step does not test for exit code 2 specifically")
	}
	if !strings.Contains(script, `exit 2`) {
		t.Error("the analyze step does not propagate exit code 2")
	}
}

// The comment is generated before the gate is evaluated, so a rejected pull
// request still shows what regressed.
func TestActionPublishesCommentOnFailure(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "action", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)

	// `always()` on the posting step is what makes this true.
	if !strings.Contains(script, "always() && inputs.comment-on-pr") {
		t.Error("the comment-posting step is not conditioned on always(); " +
			"a failed gate would leave no comment")
	}
}
