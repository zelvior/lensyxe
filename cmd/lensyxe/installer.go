package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zelvior/lensyxe/internal/installer"
)

// newInstallerCmd builds `lensyxe installer`.
//
// The wizard and the platform installers point users here to undo what they did,
// so this command exists as the documented counterpart to both. It reports what it
// would change and does nothing without confirmation.
func newInstallerCmd(a *app) *cobra.Command {
	var (
		remove  bool
		dryRun  bool
		yes     bool
		dir     string
		sh      string
		writeDT bool
	)

	cmd := &cobra.Command{
		Use:   "installer",
		Short: "Add or remove shell, PATH, and application integration",
		Long: strings.TrimSpace(`
Manage the OS integration an installation needs.

By default this reports what it would change and asks before doing it. Nothing is
written without --yes.

  --remove     Remove the PATH entry this tool added
  --dry-run    Report only, never write
  --desktop    Also install a freedesktop.org launcher (Linux)

Everything added to a shell startup file is wrapped in markers:

  >>> lensyxe >>>   ...   <<< lensyxe <<<

so removal is exact rather than best-effort, and an existing entry you wrote
yourself is never touched.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dir == "" {
				dir = installer.DefaultInstallDir(
					cfgScopeOf(string(installer.ScopeUser), remove))
			}
			shell := installer.DetectShell()
			if sh != "" {
				shell = installer.Shell(sh)
			}

			home, err := os.UserHomeDir()
			if err != nil || home == "" {
				return errors.New("could not determine the home directory")
			}

			if dryRun || !yes {
				fmt.Fprintf(cmd.OutOrStdout(), "Would %s PATH integration for %s\n",
					verb(remove), dir)
				path, present, ferr := installer.StartupFileFor(shell, home)
				if ferr != nil {
					return ferr
				}
				if runtime.GOOS == "windows" {
					fmt.Fprintln(cmd.OutOrStdout(), "  "+installer.WindowsSetPathCommand(dir))
				} else {
					if !present {
						fmt.Fprintf(cmd.OutOrStdout(), "  (create %s)\n", path)
					} else {
						fmt.Fprintf(cmd.OutOrStdout(), "  (modify %s)\n", path)
					}
				}
				if writeDT && runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
					fmt.Fprintf(cmd.OutOrStdout(), "  (write %s)\n",
						filepath.Join(home, ".local", "share", "applications", "lensyxe.desktop"))
				}
				if !dryRun {
					fmt.Fprintln(cmd.OutOrStdout(), "\nRe-run with --yes to apply.")
				}
				return nil
			}
			return applyInstaller(cmd, installerApply{
				remove: remove, dir: dir, shell: shell,
				home: home, desktop: writeDT,
			})
		},
	}

	f := cmd.Flags()
	f.BoolVar(&remove, "remove", false, "remove the PATH entry instead of adding it")
	f.BoolVar(&dryRun, "dry-run", false, "report what would change without writing")
	f.BoolVar(&yes, "yes", false, "apply the changes without prompting")
	f.StringVar(&dir, "dir", "", "installation directory (defaults to the per-user location)")
	f.StringVar(&sh, "shell", "", "shell to modify: bash, zsh, or fish")
	f.BoolVar(&writeDT, "desktop", false, "install a freedesktop.org launcher (Linux)")

	return cmd
}

func verb(remove bool) string {
	if remove {
		return "remove"
	}
	return "add"
}

// cfgScopeOf picks the scope an installer command operates in.
func cfgScopeOf(scope string, remove bool) installer.Scope {
	if remove {
		// Removal targets the user's own files. A system install's PATH entry
		// lives in a machine-wide profile this tool will not edit unasked.
		return installer.ScopeUser
	}
	if scope == string(installer.ScopeSystem) {
		return installer.ScopeSystem
	}
	return installer.ScopeUser
}

type installerApply struct {
	remove  bool
	dir     string
	shell   installer.Shell
	home    string
	desktop bool
}

// applyInstaller performs the changes.
func applyInstaller(cmd *cobra.Command, a installerApply) error {
	out := cmd.OutOrStdout()

	if runtime.GOOS == "windows" {
		if a.remove {
			fmt.Fprintln(out, "Run in an administrator terminal to remove the PATH entry:")
			fmt.Fprintln(out, "  [Environment]::SetEnvironmentVariable('PATH', "+
				"$([Environment]::GetEnvironmentVariable('PATH','User') -replace "+
				"[regex]::Escape(';"+a.dir+"'),''), 'User')")
			return nil
		}
		fmt.Fprintln(out, "Run in a terminal to apply this:")
		fmt.Fprintln(out, "  "+installer.WindowsSetPathCommand(a.dir))
		fmt.Fprintln(out, "\nIt takes effect in new terminals.")
		return nil
	}

	path, present, err := installer.StartupFileFor(a.shell, a.home)
	if err != nil {
		return err
	}

	contents := ""
	if present {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		contents = string(data)
	} else if !a.remove {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	}

	var updated string
	if a.remove {
		updated = installer.RemovePath(contents)
	} else {
		updated = installer.AddPath(contents, a.shell, a.dir)
	}

	if updated == contents {
		fmt.Fprintln(out, "Nothing to do: the entry is already in the state you asked for.")
	} else if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	} else {
		fmt.Fprintf(out, "Updated PATH integration in %s (%s)\n", path, verb(a.remove))
	}

	if a.desktop && runtime.GOOS != "darwin" {
		if err := writeDesktopEntry(a.home, a.dir); err != nil {
			return err
		}
		fmt.Fprintln(out, "Installed the application launcher.")
	}
	return nil
}

// writeDesktopEntry installs the freedesktop.org launcher.
//
// The mode is 0755 rather than 0644 because a launcher that is not executable is
// invisible in most desktops, which presents as "the app did not install".
func writeDesktopEntry(home, dir string) error {
	dirPath := filepath.Join(home, ".local", "share", "applications")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return err
	}
	exePath := filepath.Join(dir, "lensyxe")
	body := installer.DesktopEntry(exePath, "lensyxe", "Engineering health for this repository")
	target := filepath.Join(dirPath, "lensyxe.desktop")
	if err := os.WriteFile(target, []byte(body), 0o755); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	// update-desktop-database is not always present, so its absence is ignored
	// rather than failing an install that has otherwise succeeded.
	if err := exec.Command("update-desktop-database", dirPath).Run(); err != nil {
		fmt.Fprintf(os.Stderr,
			"lensyxe: launcher written, but the desktop database was not refreshed "+
				"(%v); it may not appear in the menu until you log out and in\n", err)
	}
	return nil
}
