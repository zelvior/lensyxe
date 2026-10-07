// Package gui holds the installation wizard's step model.
//
// It contains no rendering and no platform calls. Every step is a function of the
// current Choices and the current environment, so the whole wizard is a pure
// state machine that can be tested exhaustively and rendered by any frontend.
//
// That split is deliberate. A windowed frontend built on a webview or an OpenGL
// toolkit drags in cgo, a native toolchain, and hundreds of modules; the console
// frontend that ships in this binary needs none of that. Keeping the steps here
// means a native frontend added later is a shell around these decisions rather
// than a second implementation that can disagree with them.
//
// Nothing in this package escalates privileges, and nothing writes outside the
// paths the user selected.
package gui

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zelvior/lensyxe/internal/installer"
)

// Step is the identifier of one wizard step.
type Step string

const (
	StepWelcome  Step = "welcome"
	StepPath     Step = "path"
	StepShell    Step = "shell"
	StepService  Step = "service"
	StepConfig   Step = "config"
	StepProgress Step = "progress"
	StepVerify   Step = "verify"
	StepComplete Step = "complete"
)

// Steps is the order the wizard visits them in.
func Steps() []Step {
	return []Step{
		StepWelcome, StepPath, StepShell,
		StepService, StepConfig, StepProgress, StepVerify, StepComplete,
	}
}

// Choices is everything the user has decided. Every step reads from it and nothing
// else, so the wizard's outcome is fully described by this struct.
type Choices struct {
	// AcceptedLicense records agreement to LICENSE. Setup is refused without it.
	AcceptedLicense bool
	// Scope selects where the binary is placed.
	Scope installer.Scope
	// InstallDir is where the binary goes.
	InstallDir string
	// Shell is the startup mechanism to modify.
	Shell installer.Shell
	// AddToPath requests PATH registration.
	AddToPath bool
	// RegisterService requests a background `lensyxe serve`.
	RegisterService bool
	// WriteConfig requests a generated .lensyxe.yml.
	WriteConfig bool
	// HotspotThreshold is the candidate line count.
	HotspotThreshold int
	// GitWindowDays is the churn window.
	GitWindowDays int
}

// DefaultChoices returns the starting state for a platform.
//
// The defaults are chosen so that running straight through produces something
// correct: a per-user install, the detected shell, and thresholds matching the
// tool's own shipped defaults so the generated config does not silently change
// any score a user has already seen.
func DefaultChoices() Choices {
	return Choices{
		Scope:            installer.ScopeUser,
		InstallDir:       installer.DefaultInstallDir(installer.ScopeUser),
		Shell:            installer.DetectShell(),
		AddToPath:        true,
		RegisterService:  false,
		WriteConfig:      true,
		HotspotThreshold: 400,
		GitWindowDays:    90,
	}
}

// Env describes the environment a plan is built against, so planning never
// touches the real system during a test or a dry run.
type Env struct {
	Home     string
	Platform string
	// HasBinary reports whether a lensyxe executable already exists in
	// InstallDir.
	HasBinary bool
	// OnPath reports whether InstallDir is already on PATH.
	OnPath bool
	// StartupExists reports whether the shell startup file exists.
	StartupExists bool
	// ConfigExists reports whether a .lensyxe.yml is already present.
	ConfigExists bool
	// AgentSupportsService reports whether the platform has a per-user service
	// mechanism at all.
	AgentSupportsService bool
}

// DescribeEnv inspects the real environment for the current choices.
func DescribeEnv(c Choices) Env {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	e := Env{
		Home:                 home,
		Platform:             runtime.GOOS,
		OnPath:               onPath(c.InstallDir),
		AgentSupportsService: runtime.GOOS == "windows" || runtime.GOOS == "darwin",
	}
	if info, err := os.Stat(filepath.Join(c.InstallDir, binaryName())); err == nil && !info.IsDir() {
		e.HasBinary = true
	}
	if _, present, err := installer.StartupFileFor(c.Shell, home); err == nil {
		e.StartupExists = present
	}
	if _, err := os.Stat(filepath.Join(home, configFileName)); err == nil {
		e.ConfigExists = true
	}
	return e
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "lensyxe.exe"
	}
	return "lensyxe"
}

// configFileName is the configuration file the wizard writes.
const configFileName = ".lensyxe.yml"

func onPath(dir string) bool {
	if dir == "" {
		return false
	}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return true
		}
	}
	return false
}

// Action is one thing the wizard will do.
type Action struct {
	// Kind classifies the action.
	Kind ActionKind
	// Description is what happens, in one line.
	Description string
	// Detail is the exact command or file operation, so the user can see it
	// before it runs rather than after.
	Detail string
	// Reversible reports whether an undo is available.
	Reversible bool
	// NeedsRoot reports that the action will fail without privileges.
	NeedsRoot bool
}

// ActionKind classifies an action.
type ActionKind string

const (
	ActionCopyBinary  ActionKind = "copy-binary"
	ActionPathEntry   ActionKind = "path-entry"
	ActionDesktopFile ActionKind = "desktop-file"
	ActionService     ActionKind = "service"
	ActionConfig      ActionKind = "config"
)

// Plan is the full set of actions a set of choices implies.
//
// A plan is computed before anything happens, so the user can read the whole
// install -- including what it would modify and what it cannot undo -- before
// agreeing to any of it.
type Plan struct {
	Choices Choices
	Actions []Action
	// Warnings are conditions that do not block the install but change what it
	// means.
	Warnings []string
	// Errors are conditions that must be resolved first.
	Errors []string
}

// BuildPlan derives the actions from the choices and environment.
//
// It never touches the filesystem beyond what DescribeEnv already read, which is
// what makes it safe to call for a preview.
func BuildPlan(c Choices, e Env) Plan {
	p := Plan{Choices: c}

	if !c.AcceptedLicense {
		p.Errors = append(p.Errors,
			"the license has not been accepted; nothing will be installed")
	}

	// --- binary ---
	installPath := filepath.Join(c.InstallDir, binaryName())
	if e.HasBinary {
		p.Actions = append(p.Actions, Action{
			Kind: ActionCopyBinary, Reversible: false,
			Description: "Replace the existing lensyxe at " + installPath,
			Detail:      "an existing binary will be overwritten",
		})
		p.Warnings = append(p.Warnings,
			"a lensyxe binary is already present at "+installPath+
				" and will be replaced; keeping a copy is your call")
	} else {
		p.Actions = append(p.Actions, Action{
			Kind: ActionCopyBinary, Reversible: true,
			Description: "Install lensyxe to " + installPath,
			Detail:      "install " + installPath,
		})
	}
	if c.Scope == installer.ScopeSystem {
		p.Actions[len(p.Actions)-1].NeedsRoot = true
	}

	// --- PATH ---
	if c.AddToPath {
		if e.OnPath {
			p.Warnings = append(p.Warnings,
				c.InstallDir+" is already on PATH; no change is needed")
		} else {
			a := Action{
				Kind: ActionPathEntry, Reversible: true,
				Description: "Add " + c.InstallDir + " to PATH",
			}
			switch c.Shell {
			case installer.ShellWindows:
				a.Detail = installer.WindowsSetPathCommand(c.InstallDir)
			default:
				file, _, _ := installer.StartupFileFor(c.Shell, e.Home)
				if !e.StartupExists {
					a.Reversible = true
					a.Detail = "create " + file
					p.Warnings = append(p.Warnings,
						file+" does not exist yet and will be created")
				} else {
					a.Detail = "append a marked block to " + file
				}
			}
			p.Actions = append(p.Actions, a)
		}
	}

	// --- desktop launcher ---
	if e.Platform != "windows" && e.Platform != "darwin" {
		launcher := filepath.Join(e.Home, ".local", "share", "applications", "lensyxe.desktop")
		p.Actions = append(p.Actions, Action{
			Kind: ActionDesktopFile, Reversible: true,
			Description: "Add a Lensyxe application launcher",
			Detail:      "write " + launcher,
		})
	}

	// --- background service ---
	if c.RegisterService {
		switch {
		case !e.AgentSupportsService:
			p.Errors = append(p.Errors,
				"this platform has no per-user service mechanism lensyxe will "+
					"register itself with; use systemd on Linux, launchd on macOS, "+
					"or a Windows service, and run `lensyxe serve` yourself")
		case e.Platform == "windows":
			p.Actions = append(p.Actions, Action{
				Kind: ActionService, Reversible: true, NeedsRoot: true,
				Description: "Register lensyxe serve as a Windows service",
				Detail:      installer.WindowsServiceCommand(installPath),
			})
		case e.Platform == "darwin":
			agent := filepath.Join(e.Home, "Library", "LaunchAgents", "dev.lensyxe.serve.plist")
			p.Actions = append(p.Actions, Action{
				Kind: ActionService, Reversible: true,
				Description: "Register a launchd agent for lensyxe serve",
				Detail:      "write " + agent,
			})
		default:
			// A systemd path is POSIX no matter which host is planning the
			// install, so the detail the user reads is slash-separated even when
			// the plan is being built on Windows.
			unit := posixJoin(e.Home, ".config/systemd/user/lensyxe.service")
			p.Actions = append(p.Actions, Action{
				Kind: ActionService, Reversible: true,
				Description: "Register a systemd user unit for lensyxe serve",
				Detail:      "write " + unit + "; then: systemctl --user enable --now lensyxe.service",
			})
		}
	}

	// --- config ---
	if c.WriteConfig {
		target := filepath.Join(e.Home, configFileName)
		// The threshold values are validated before they are described, and the
		// validation is not a separate action. Emitting it as one produced two
		// config actions, and the executor then wrote the file for the first
		// and refused it for the second.
		if c.HotspotThreshold <= 0 {
			p.Errors = append(p.Errors,
				"hotspot_threshold must be positive; zero would mark every file "+
					"as a hotspot candidate")
		}
		if c.GitWindowDays <= 0 {
			p.Errors = append(p.Errors, "git_window_days must be positive")
		}
		a := Action{
			Kind: ActionConfig, Reversible: true,
			Description: "Write a starter " + configFileName,
			Detail: fmt.Sprintf("write %s (hotspot_threshold=%d, git_window_days=%d)",
				target, c.HotspotThreshold, c.GitWindowDays),
		}
		if e.ConfigExists {
			// Overwriting someone's existing configuration is the one thing here
			// that can lose work, so it is refused rather than prompted.
			a.Reversible = false
			a.Description = "Refuse to overwrite the existing " + configFileName
			a.Detail = target + " already exists and will not be overwritten"
			p.Warnings = append(p.Warnings,
				target+" already exists; the wizard will not overwrite it")
		}
		p.Actions = append(p.Actions, a)
	}
	return p
}

// posixJoin builds a slash-separated path for display.
//
// Used for paths that name a *different* platform's convention than the host
// running the wizard. Writing "\home\u\.config\systemd\user\lensyxe.service" as
// the instructions for a systemd unit is wrong even when the plan is being built
// on Windows.
func posixJoin(elems ...string) string {
	joined := path.Join(elems...)
	return strings.ReplaceAll(joined, `\`, "/")
}

// ConfigYAML renders the starter configuration for the chosen thresholds.
//
// It is deliberately short. A generated file that repeats every key of the
// documented template is worse than a short one: the template already exists for
// anyone who wants the full reference, and a generated file full of commented
// defaults is a file nobody edits.
func ConfigYAML(c Choices) string {
	var b strings.Builder
	b.WriteString("# Lensyxe configuration\n#\n")
	b.WriteString("# Written by the setup wizard. Every key is optional; the values\n")
	b.WriteString("# below are the built-in defaults, listed so you can see what is\n")
	b.WriteString("# being set and change it.\n#\n")
	b.WriteString("# Full reference: docs/CONFIGURATION.md\n\n")
	fmt.Fprintf(&b, "# Code lines at or above which a file is a hotspot candidate.\nhotspot_threshold: %d\n\n", c.HotspotThreshold)
	fmt.Fprintf(&b, "# Days of git history considered by the churn and cadence metrics.\ngit_window_days: %d\n", c.GitWindowDays)
	return b.String()
}
