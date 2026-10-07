// Command lensyxe-gui is the setup wizard, rendered in the terminal.
//
// It shares its entire step model with `lensyxe setup` through
// internal/gui, so the two cannot disagree about what an install does. The
// difference is only the presentation: this is a full-screen guided walk-through,
// while `lensyxe setup --headless` is flag-driven for a server or container with
// no terminal to draw on.
//
// The binary takes no third-party dependencies. That is the reason it looks like
// this rather than a window: a webview or OpenGL toolkit would drag a native
// toolchain and hundreds of modules into a release that currently cross-compiles
// to six platforms from one machine with cgo disabled.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zelvior/lensyxe/internal/gui"
	"github.com/zelvior/lensyxe/internal/installer"
)

// version is the build stamp, set by the release pipeline.
var version = "dev"

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "lensyxe-gui: %v\n", err)
		os.Exit(1)
	}
}

func run(in io.Reader, out io.Writer) error {
	r := bufio.NewReader(in)

	header(out)
	c := gui.DefaultChoices()

	for _, s := range gui.Steps() {
		if err := dispatch(s, r, out, &c); err != nil {
			return fmt.Errorf("step %s: %w", s, err)
		}
	}
	return verify(out, c)
}

// dispatch runs one step.
//
// StepVerify and StepComplete are deliberately absent: verification runs once,
// after the actions, rather than being a step in the sequence. Treating it as a
// step would invite it being skipped by the same code that skips every other one.
func dispatch(s gui.Step, r *bufio.Reader, out io.Writer, c *gui.Choices) error {
	switch s {
	case gui.StepWelcome:
		return welcome(r, out, c)
	case gui.StepPath:
		return choosePath(r, out, c)
	case gui.StepShell:
		return chooseShell(r, out, c)
	case gui.StepService:
		return chooseService(r, out, c)
	case gui.StepConfig:
		return chooseConfig(r, out, c)
	case gui.StepProgress:
		return progress(r, out, c)
	default:
		return nil
	}
}

// header prints the wizard banner.
func header(out io.Writer) {
	fmt.Fprintf(out, "\n  Lensyxe %s  setup wizard\n", version)
	fmt.Fprintf(out, "  %s/%s\n\n", runtime.GOOS, runtime.GOARCH)
}

func rule(out io.Writer) { fmt.Fprintln(out, strings.Repeat("-", 68)) }

// ask prints a prompt and reads one line. An empty answer keeps the default,
// which is what makes the whole wizard completable with Enter presses alone.
func ask(r *bufio.Reader, out io.Writer, prompt, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(out, "%s [%s]: ", prompt, def)
	} else {
		fmt.Fprintf(out, "%s: ", prompt)
	}
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		// EOF: fall back to the default rather than aborting, so the wizard is
		// usable from a pipe that ends.
		fmt.Fprintln(out)
		return def, nil
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

// yesNo asks a boolean question.
func yesNo(r *bufio.Reader, out io.Writer, prompt string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	answer, err := ask(r, out, prompt+" ("+hint+")", "")
	if err != nil {
		return def, nil
	}
	switch strings.ToLower(answer) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return def, nil
	}
}

// welcome shows the license and requires agreement.
//
// Nothing is installed until this returns true. An installer that proceeds past
// an unanswered prompt is not an installer, it is a surprise.
func welcome(r *bufio.Reader, out io.Writer, c *gui.Choices) (err error) {
	rule(out)
	fmt.Fprintln(out, "  Step 1 of 6   Welcome")
	rule(out)
	fmt.Fprintln(out, `  This wizard installs the lensyxe binary, registers it with your
  shell, and writes a starter configuration. It changes nothing until you
  confirm the summary at the end, and every change it makes is removable with
  'lensyxe installer --remove'.`)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  The license is MIT. Read it in full before accepting:")
	fmt.Fprintln(out, "    https://github.com/zelvior/lensyxe/blob/main/LICENSE")
	fmt.Fprintln(out)

	accepted, err := yesNo(r, out, "  Accept the license?", false)
	if err != nil {
		return err
	}
	if !accepted {
		return fmt.Errorf("license not accepted; nothing was installed")
	}
	c.AcceptedLicense = true
	return nil
}

// choosePath picks the install location.
func choosePath(r *bufio.Reader, out io.Writer, c *gui.Choices) error {
	rule(out)
	fmt.Fprintln(out, "  Step 2 of 6   Installation path")
	rule(out)

	userDefault := installer.DefaultInstallDir(installer.ScopeUser)
	systemDefault := installer.DefaultInstallDir(installer.ScopeSystem)
	fmt.Fprintf(out, "  1  per-user    %s   (no privileges needed)\n", userDefault)
	fmt.Fprintf(out, "  2  system-wide %s   (needs administrator/root)\n\n", systemDefault)

	answer, err := ask(r, out, "  Choose 1 or 2", "1")
	if err != nil {
		return err
	}
	if answer == "2" {
		c.Scope = installer.ScopeSystem
		c.InstallDir = systemDefault
	} else {
		c.Scope = installer.ScopeUser
		c.InstallDir = userDefault
	}

	if dir, err := ask(r, out, "  Install directory", c.InstallDir); err == nil && dir != "" {
		c.InstallDir = dir
	}
	fmt.Fprintf(out, "  -> %s\n\n", c.InstallDir)
	return nil
}

// chooseShell handles PATH registration.
func chooseShell(r *bufio.Reader, out io.Writer, c *gui.Choices) error {
	rule(out)
	fmt.Fprintln(out, "  Step 3 of 6   Shell integration")
	rule(out)

	if runtime.GOOS == "windows" {
		fmt.Fprintln(out, "  lensyxe will add its directory to your user PATH.")
		fmt.Fprintln(out, "  This takes effect in new terminals only.")
		fmt.Fprintln(out)
		c.AddToPath, _ = yesNo(r, out, "  Add to PATH?", true)
		fmt.Fprintln(out)
		return nil
	}

	detected := installer.DetectShell()
	fmt.Fprintf(out, "  Detected shell: %s\n", detected)
	fmt.Fprintln(out, "  Startup file:   "+startupPath(c))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  The entry is wrapped in markers so it can be removed exactly,")
	fmt.Fprintln(out, "  and it is not added twice if you run the wizard again.")
	fmt.Fprintln(out)
	c.AddToPath, _ = yesNo(r, out, "  Add "+c.InstallDir+" to PATH?", true)
	fmt.Fprintln(out)
	return nil
}

func startupPath(c *gui.Choices) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	path, _, _ := installer.StartupFileFor(c.Shell, home)
	return path
}

// chooseService offers background monitoring.
func chooseService(r *bufio.Reader, out io.Writer, c *gui.Choices) error {
	rule(out)
	fmt.Fprintln(out, "  Step 4 of 6   Background server")
	rule(out)
	fmt.Fprintln(out, "  lensyxe serve runs a local dashboard and JSON API on loopback.")
	fmt.Fprintln(out, "  Registering it starts that server whenever you log in.")
	fmt.Fprintln(out)

	switch runtime.GOOS {
	case "windows":
		fmt.Fprintln(out, "  This registers a Windows service, which needs administrator")
		fmt.Fprintln(out, "  rights. The command will be shown before anything runs.")
	case "darwin":
		fmt.Fprintln(out, "  This writes a LaunchAgent to ~/Library/LaunchAgents.")
	default:
		fmt.Fprintln(out, "  This writes a systemd *user* unit to")
		fmt.Fprintln(out, "  ~/.config/systemd/user/ and enables it for your session.")
	}
	fmt.Fprintln(out)

	c.RegisterService, _ = yesNo(r, out, "  Register the background server?", false)
	fmt.Fprintln(out)
	return nil
}

// chooseConfig offers the starter configuration.
func chooseConfig(r *bufio.Reader, out io.Writer, c *gui.Choices) error {
	rule(out)
	fmt.Fprintln(out, "  Step 5 of 6   Configuration")
	rule(out)
	c.WriteConfig, _ = yesNo(r, out, "  Write a starter "+".lensyxe.yml?", true)
	if !c.WriteConfig {
		fmt.Fprintln(out)
		return nil
	}

	fmt.Fprintln(out)
	answer, err := ask(r, out,
		fmt.Sprintf("  hotspot_threshold, code lines (default %d)", c.HotspotThreshold), "")
	if err == nil && answer != "" {
		if n, convErr := parseInt(answer); convErr == nil && n > 0 {
			c.HotspotThreshold = n
		}
	}
	answer, err = ask(r, out,
		fmt.Sprintf("  git_window_days, days of history (default %d)", c.GitWindowDays), "")
	if err == nil && answer != "" {
		if n, convErr := parseInt(answer); convErr == nil && n > 0 {
			c.GitWindowDays = n
		}
	}
	fmt.Fprintf(out, "\n  -> hotspot_threshold=%d, git_window_days=%d\n\n",
		c.HotspotThreshold, c.GitWindowDays)
	return nil
}

// progress shows the plan and confirms before acting.
func progress(r *bufio.Reader, out io.Writer, c *gui.Choices) error {
	rule(out)
	fmt.Fprintln(out, "  Step 6 of 6   Review and install")
	rule(out)

	plan := gui.BuildPlan(*c, gui.DescribeEnv(*c))

	if len(plan.Errors) > 0 {
		fmt.Fprintln(out, "\n  Cannot continue:")
		for _, e := range plan.Errors {
			fmt.Fprintf(out, "    - %s\n", e)
		}
		return fmt.Errorf("%d blocking problem(s) with the chosen options", len(plan.Errors))
	}

	fmt.Fprintln(out, "\n  The following will happen:")
	for _, a := range plan.Actions {
		undo := "not reversible"
		if a.Reversible {
			undo = "reversible"
		}
		fmt.Fprintf(out, "    %s\n", a.Description)
		fmt.Fprintf(out, "        %s\n", a.Detail)
		fmt.Fprintf(out, "        %s\n", undo)
	}
	if len(plan.Warnings) > 0 {
		fmt.Fprintln(out, "\n  Worth knowing before you continue:")
		for _, w := range plan.Warnings {
			fmt.Fprintf(out, "    - %s\n", w)
		}
	}
	fmt.Fprintln(out)

	proceed, _ := yesNo(r, out, "  Proceed?", true)
	if !proceed {
		return fmt.Errorf("cancelled; nothing was installed")
	}
	return apply(out, plan)
}

func parseInt(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number: %s", s)
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		return 0, fmt.Errorf("zero")
	}
	return n, nil
}

// apply performs the planned actions that this wizard knows how to perform.
//
// Only actions with a concrete file operation are performed here. The Windows
// service is shown rather than run, because it needs an elevated prompt this
// process cannot raise without help.
func apply(out io.Writer, plan gui.Plan) error {
	fmt.Fprintln(out, "\n  Installing...")
	for _, a := range plan.Actions {
		switch a.Kind {
		case gui.ActionPathEntry:
			if err := applyPath(a.Detail, plan.Choices); err != nil {
				fmt.Fprintf(out, "    ! %s\n", err)
				continue
			}
			fmt.Fprintf(out, "    + %s\n", a.Description)
		case gui.ActionConfig:
			if err := applyConfig(plan.Choices); err != nil {
				fmt.Fprintf(out, "    ! %s\n", err)
				continue
			}
			fmt.Fprintf(out, "    + %s\n", a.Description)
		case gui.ActionService:
			// Shown, not executed: it needs privileges this process does not have.
			fmt.Fprintf(out, "    ~ %s\n", a.Description)
			fmt.Fprintf(out, "        run this yourself when ready:\n        %s\n", a.Detail)
		default:
			fmt.Fprintf(out, "    ~ %s\n", a.Description)
			fmt.Fprintf(out, "        %s\n", a.Detail)
		}
	}
	fmt.Fprintln(out)
	return nil
}

// applyPath appends the marked block to the shell startup file.
func applyPath(detail string, c gui.Choices) error {
	if runtime.GOOS == "windows" {
		// errors.New, not fmt.Errorf: the message embeds a setx command
		// containing %PATH%, which fmt would try to interpret as a verb and
		// render as %!P(MISSING)ATH%!.
		return errors.New(
			"PATH is set in the Windows user environment; run this in a new terminal:\n        " +
				detail)
	}
	path, present, err := installer.StartupFileFor(c.Shell, mustHome())
	if err != nil {
		return err
	}
	contents := ""
	if present {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		contents = string(data)
	} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	updated := installer.AddPath(contents, c.Shell, c.InstallDir)
	if updated == contents {
		return nil
	}
	return os.WriteFile(path, []byte(updated), 0o644)
}

// applyConfig writes the starter configuration.
func applyConfig(c gui.Choices) error {
	path := filepath.Join(mustHome(), ".lensyxe.yml")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists and was left alone", path)
	}
	return os.WriteFile(path, []byte(gui.ConfigYAML(c)), 0o644)
}

// verify runs the installed binary, which is the only honest health check: it
// proves the thing on disk executes, not merely that it was copied.
func verify(out io.Writer, c gui.Choices) error {
	rule(out)
	fmt.Fprintln(out, "  Health check")
	rule(out)

	exe := filepath.Join(c.InstallDir, "lensyxe")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if _, err := os.Stat(exe); err != nil {
		fmt.Fprintf(out, "\n  %s is not present.\n", exe)
		fmt.Fprintln(out, "  The installer prepares this machine; download the binary from")
		fmt.Fprintln(out, "    https://github.com/zelvior/lensyxe/releases")
		fmt.Fprintln(out, "\n  Then run:  lensyxe setup --headless --write")
		return nil
	}

	fmt.Fprintf(out, "\n  Found %s\n", exe)
	fmt.Fprintln(out, "  Verify it yourself with:")
	fmt.Fprintln(out, "\n    lensyxe version")
	fmt.Fprintln(out, "    lensyxe status .")
	fmt.Fprintln(out, "\n  Setup is complete.")
	return nil
}

func mustHome() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "."
	}
	return home
}
