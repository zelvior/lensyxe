package gui

import (
	"os"
	"strings"
	"testing"
)

// A plan is computed before anything happens, so the user can read the whole
// install before agreeing to it. These tests use a synthetic Env, which is what
// makes that possible: nothing here touches a real filesystem.
func env() Env {
	return Env{
		Home:                 "/home/u",
		Platform:             "linux",
		StartupExists:        true,
		AgentSupportsService: true,
	}
}

func linuxChoices() Choices {
	c := DefaultChoices()
	c.AcceptedLicense = true
	c.Scope = "user"
	c.InstallDir = "/home/u/.local/bin"
	c.Shell = "bash"
	return c
}

// Nothing is installed without an accepted license. This is the one gate that
// must never be skippable.
func TestPlanRefusesWithoutTheLicense(t *testing.T) {
	c := linuxChoices()
	c.AcceptedLicense = false
	p := BuildPlan(c, env())
	if len(p.Errors) == 0 {
		t.Fatal("a plan was produced without an accepted license")
	}
	if !strings.Contains(p.Errors[0], "license") {
		t.Errorf("the error does not mention the license: %q", p.Errors[0])
	}
}

func TestPlanForAPerUserInstallNeedsNoRoot(t *testing.T) {
	p := BuildPlan(linuxChoices(), env())
	for _, a := range p.Actions {
		if a.NeedsRoot {
			t.Errorf("a per-user install needs root for %q", a.Description)
		}
	}
}

func TestSystemScopeIsMarkedAsNeedingRoot(t *testing.T) {
	c := linuxChoices()
	c.Scope = "system"
	p := BuildPlan(c, env())
	found := false
	for _, a := range p.Actions {
		if a.NeedsRoot {
			found = true
		}
	}
	if !found {
		t.Error("a system-wide install did not report that it needs privileges")
	}
}

// Overwriting a user's configuration is the one action here that can lose work,
// so it is refused rather than prompted.
func TestExistingConfigIsNeverOverwritten(t *testing.T) {
	e := env()
	e.ConfigExists = true
	p := BuildPlan(linuxChoices(), e)

	var config *Action
	for i := range p.Actions {
		if p.Actions[i].Kind == ActionConfig {
			config = &p.Actions[i]
		}
	}
	if config == nil {
		t.Fatal("no config action in the plan")
	}
	if config.Reversible {
		t.Error("an overwrite was marked reversible; overwriting loses work")
	}
	if !strings.Contains(strings.Join(p.Warnings, "\n"), "will not overwrite") {
		t.Errorf("the refusal was not explained: %v", p.Warnings)
	}
}

// A config action that writes the file, plus a second one that checks for the
// file it just wrote, made the executor report its own write as a conflict.
func TestConfigIsASingleAction(t *testing.T) {
	p := BuildPlan(linuxChoices(), env())
	n := 0
	for _, a := range p.Actions {
		if a.Kind == ActionConfig {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d config actions in one plan, want exactly 1: %+v", n, p.Actions)
	}
}

// The PATH entry is skipped when the directory is already there, and the user is
// told why nothing happened.
func TestAlreadyOnPathProducesNoAction(t *testing.T) {
	e := env()
	e.OnPath = true
	p := BuildPlan(linuxChoices(), e)

	for _, a := range p.Actions {
		if a.Kind == ActionPathEntry {
			t.Errorf("a PATH action was planned when the directory is already on PATH")
		}
	}
	if !strings.Contains(strings.Join(p.Warnings, "\n"), "already on PATH") {
		t.Errorf("no warning about the skipped action: %v", p.Warnings)
	}
}

func TestPathActionNamesTheStartupFile(t *testing.T) {
	p := BuildPlan(linuxChoices(), env())
	for _, a := range p.Actions {
		if a.Kind == ActionPathEntry {
			if !strings.Contains(a.Detail, ".bashrc") {
				t.Errorf("the PATH action does not name the file it edits: %q", a.Detail)
			}
			return
		}
	}
	t.Fatal("no PATH action was planned")
}

func TestServiceIsRefusedWhereThereIsNoAgent(t *testing.T) {
	e := env()
	e.AgentSupportsService = false
	c := linuxChoices()
	c.RegisterService = true

	p := BuildPlan(c, e)
	if len(p.Errors) == 0 {
		t.Fatal("a service was planned on a platform with no per-user agent")
	}
	for _, a := range p.Actions {
		if a.Kind == ActionService {
			t.Errorf("a service action was planned anyway: %+v", a)
		}
	}
}

func TestWindowsServiceNamesTheCommand(t *testing.T) {
	e := env()
	e.Platform = "windows"
	c := linuxChoices()
	c.Shell = "windows"
	c.RegisterService = true

	p := BuildPlan(c, e)
	for _, a := range p.Actions {
		if a.Kind == ActionService {
			if !strings.Contains(a.Detail, "sc.exe create") {
				t.Errorf("the Windows service action gives no command: %q", a.Detail)
			}
			if !a.NeedsRoot {
				t.Error("a Windows service registration did not report needing elevation")
			}
			return
		}
	}
	t.Fatal("no service action was planned")
}

func TestLinuxServiceWritesAUserUnit(t *testing.T) {
	c := linuxChoices()
	c.RegisterService = true
	p := BuildPlan(c, env())
	for _, a := range p.Actions {
		if a.Kind == ActionService {
			if !strings.Contains(a.Detail, "systemd/user") {
				t.Errorf("the service is not a user unit: %q", a.Detail)
			}
			if !strings.Contains(a.Detail, "systemctl --user enable") {
				t.Errorf("the action does not say how to enable it: %q", a.Detail)
			}
			return
		}
	}
	t.Fatal("no service action was planned")
}

// A zero hotspot threshold would mark every file as a candidate, which is not a
// configuration so much as a mistake.
func TestInvalidThresholdsAreRejected(t *testing.T) {
	for _, mutate := range []func(*Choices){
		func(c *Choices) { c.HotspotThreshold = 0 },
		func(c *Choices) { c.HotspotThreshold = -1 },
		func(c *Choices) { c.GitWindowDays = 0 },
	} {
		c := linuxChoices()
		mutate(&c)
		p := BuildPlan(c, env())
		if len(p.Errors) == 0 {
			t.Errorf("an invalid configuration was accepted: %+v", c)
		}
	}
}

// The plan must be deterministic, or two runs of the same wizard would show the
// user two different summaries and neither would be trustworthy.
func TestPlanIsDeterministic(t *testing.T) {
	c := linuxChoices()
	c.RegisterService = true
	first := summarise(BuildPlan(c, env()))
	for i := 0; i < 20; i++ {
		if again := summarise(BuildPlan(c, env())); again != first {
			t.Fatalf("plan %d differs:\n%s\n---\n%s", i, first, again)
		}
	}
}

func summarise(p Plan) string {
	var b strings.Builder
	for _, a := range p.Actions {
		b.WriteString(string(a.Kind) + "|" + a.Description + "|" + a.Detail + "\n")
	}
	for _, e := range p.Errors {
		b.WriteString("E:" + e + "\n")
	}
	for _, w := range p.Warnings {
		b.WriteString("W:" + w + "\n")
	}
	return b.String()
}

// The generated config must parse as YAML and carry the chosen values. A
// generator that emits something the loader rejects is worse than none.
func TestGeneratedConfigIsUsable(t *testing.T) {
	c := linuxChoices()
	c.HotspotThreshold = 250
	c.GitWindowDays = 45
	got := ConfigYAML(c)

	if !strings.Contains(got, "hotspot_threshold: 250") {
		t.Errorf("the threshold was not written:\n%s", got)
	}
	if !strings.Contains(got, "git_window_days: 45") {
		t.Errorf("the window was not written:\n%s", got)
	}
	// A short file with a pointer to the full reference, rather than a copy of
	// every key, which nobody edits.
	if lines := strings.Count(got, "\n"); lines > 20 {
		t.Errorf("the generated config is %d lines; it should stay short:\n%s", lines, got)
	}
	if !strings.Contains(got, "docs/CONFIGURATION.md") {
		t.Error("the generated config does not point at the full reference")
	}
}

func TestStepsAreInAWalkableOrder(t *testing.T) {
	got := Steps()
	want := []Step{
		StepWelcome, StepPath, StepShell,
		StepService, StepConfig, StepProgress, StepVerify, StepComplete,
	}
	if len(got) != len(want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("step %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// DefaultChoices must not change any score a user has already seen. The
// thresholds therefore match the shipped defaults.
func TestDefaultThresholdsMatchTheShippedOnes(t *testing.T) {
	c := DefaultChoices()
	if c.HotspotThreshold != 400 {
		t.Errorf("hotspot threshold = %d, want the shipped 400", c.HotspotThreshold)
	}
	if c.GitWindowDays != 90 {
		t.Errorf("git window = %d, want the shipped 90", c.GitWindowDays)
	}
	if c.Scope != "user" {
		t.Errorf("default scope = %q, want user: escalating by default is wrong", c.Scope)
	}
}

// Building a plan must not touch the filesystem. The whole reason Env is a
// parameter is that a dry run is a real code path rather than a promise.
func TestBuildPlanDoesNotTouchDisk(t *testing.T) {
	dir := t.TempDir()
	c := linuxChoices()
	c.InstallDir = dir
	c.RegisterService = true
	c.WriteConfig = true

	p := BuildPlan(c, Env{Home: dir, Platform: "linux", AgentSupportsService: true})
	if len(p.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", p.Errors)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("BuildPlan created %v; planning must not write anything", entries)
	}
}

func TestDescribeEnvDoesNotPanicOnAWritableHome(t *testing.T) {
	// A smoke test rather than an assertion about a specific machine: the point
	// is that the probe survives a real environment.
	t.Setenv("HOME", t.TempDir())
	c := DefaultChoices()
	e := DescribeEnv(c)
	if e.Home == "" {
		t.Error("no home directory was reported")
	}
	if e.Platform == "" {
		t.Error("no platform was reported")
	}
}

func TestPlanHandlesAnEmptyEnvironment(t *testing.T) {
	p := BuildPlan(linuxChoices(), Env{})
	if len(p.Actions) == 0 {
		t.Error("an empty environment produced no actions at all")
	}
}
