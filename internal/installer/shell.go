// Package installer performs the OS integration an installation needs: PATH
// registration, shell startup files, application launchers, and background
// services.
//
// Every change this package makes is reversible and marked. Additions to a shell
// startup file are wrapped in sentinel comments so removal is exact rather than
// best-effort, and the same markers make the operation idempotent: installing
// twice does not append the line twice, and an installer that cannot cleanly undo
// itself is worse than one that never ran.
//
// Nothing here writes to a system location without being told to. The default
// scope is the current user's home, because a per-machine install is a privileged
// operation and this package does not escalate on its own.
//
// The Windows PATH is managed through the documented user-environment mechanism
// rather than through the registry API, so this package stays pure Go with no cgo
// and no platform-specific build tags.
package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Scope is where an installation places its files.
type Scope string

const (
	// ScopeUser installs under the current user's home. The default, and the only
	// scope that needs no privileges.
	ScopeUser Scope = "user"
	// ScopeSystem installs under a machine-wide prefix such as /usr/local and
	// requires privileges the caller must already hold.
	ScopeSystem Scope = "system"
)

// Shell identifies a shell startup mechanism.
type Shell string

const (
	ShellBash    Shell = "bash"
	ShellZsh     Shell = "zsh"
	ShellFish    Shell = "fish"
	ShellWindows Shell = "windows"
	ShellUnknown Shell = ""
)

// The sentinels bracketing everything this package writes into a startup file.
//
// A block comment is used rather than a single line so that removal deletes the
// whole unit: a path added on one line and a note added on the next cannot be
// removed independently, and removing only the path would leave the file
// changed in a way nobody asked for.
const (
	beginMarker = "# >>> lensyxe >>>"
	endMarker   = "# <<< lensyxe <<<"
)

// DefaultInstallDir returns the conventional location for a scope.
//
// On Windows a per-user install lives under AppData rather than Program Files,
// because Program Files is not writable without elevation and failing halfway
// through an install is worse than installing somewhere unremarkable.
func DefaultInstallDir(scope Scope) string {
	switch runtime.GOOS {
	case "windows":
		if scope == ScopeSystem {
			return `C:\Program Files\lensyxe`
		}
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, "lensyxe", "bin")
		}
		return filepath.Join(homeDir(), "lensyxe", "bin")
	case "darwin":
		if scope == ScopeSystem {
			return "/usr/local/bin"
		}
		return filepath.Join(homeDir(), ".local", "bin")
	default:
		if scope == ScopeSystem {
			return "/usr/local/bin"
		}
		return filepath.Join(homeDir(), ".local", "bin")
	}
}

// homeDir returns the user's home directory, preferring the environment so the
// result is predictable in tests.
func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	if h := os.Getenv("USERPROFILE"); h != "" {
		return h
	}
	return "."
}

// DetectShell guesses which shell configuration to modify.
//
// The guess is only a default. A user with both bash and zfc will have both, and
// editing the wrong one silently does nothing, which is why the caller always
// shows what was chosen and offers the alternatives.
func DetectShell() Shell {
	if runtime.GOOS == "windows" {
		return ShellWindows
	}
	shell := os.Getenv("SHELL")
	switch {
	case strings.HasSuffix(shell, "/zsh"), strings.HasSuffix(shell, "zsh"):
		return ShellZsh
	case strings.HasSuffix(shell, "/fish"), strings.HasSuffix(shell, "fish"):
		return ShellFish
	case strings.HasSuffix(shell, "/bash"), strings.HasSuffix(shell, "bash"):
		return ShellBash
	}
	if _, err := os.Stat(filepath.Join(homeDir(), ".zshrc")); err == nil {
		return ShellZsh
	}
	if _, err := os.Stat(filepath.Join(homeDir(), ".bashrc")); err == nil {
		return ShellBash
	}
	return ShellBash
}

// StartupFileFor returns the file a shell reads at login for a given home, and
// whether it currently exists.
//
// Exported so the wizard can plan against the real path without writing to it.
func StartupFileFor(sh Shell, home string) (string, bool, error) {
	return startupFile(sh, home)
}

// WindowsSetPathCommand returns the command that adds a directory to the current
// user's PATH.
//
// Rendered rather than executed. Changing the environment is a visible, persistent
// change, and a tool that performs one without being asked is a tool nobody runs
// twice.
//
// The command deliberately does NOT use `setx PATH`. setx truncates its value at
// 1024 characters on Windows, so on a machine with a long PATH it silently
// discards the entries past that point -- a destructive edit that still reports
// success. Setting the variable through the .NET environment API has no such
// limit.
//
// This is a multi-line PowerShell snippet because the read, the append and the
// write have to be one expression; a user-environment PATH that was truncated in
// the middle would be worse than no change at all.
func WindowsSetPathCommand(dir string) string {
	return fmt.Sprintf(
		`$p = [Environment]::GetEnvironmentVariable('PATH','User'); `+
			`if ($p -notlike '*;%s;*' -and $p -notlike '%s;*') { `+
			`[Environment]::SetEnvironmentVariable('PATH', `+
			`($p.TrimEnd(';') + ';%s'), 'User') }`, dir, dir, dir)
}

// WindowsRemovePathCommand returns the command that removes a directory from the
// current user's PATH.
//
// Written as a filter rather than a string replace so that a directory that is a
// prefix of another one -- C:\tools\lensyxe and C:\tools\lensyxe-extras -- is not
// removed along with it.
func WindowsRemovePathCommand(dir string) string {
	return fmt.Sprintf(
		`$p = [Environment]::GetEnvironmentVariable('PATH','User') -split ';' | `+
			`Where-Object { $_ -and $_.TrimEnd('\') -ne '%s' }; `+
			`[Environment]::SetEnvironmentVariable('PATH', ($p -join ';'), 'User')`, dir)
}

// startupFile returns the file a shell reads at login, and whether one exists.
//
// An absent file is reported rather than created silently: appending to a .bashrc
// that was never there produces a file the user did not write and may not want,
// so the caller decides.
func startupFile(sh Shell, home string) (string, bool, error) {
	switch sh {
	case ShellBash:
		p := filepath.Join(home, ".bashrc")
		ok := exists(p)
		return p, ok, nil
	case ShellZsh:
		p := filepath.Join(home, ".zshrc")
		ok := exists(p)
		return p, ok, nil
	case ShellFish:
		// fish uses universal variables rather than a PATH export line, so the
		// block differs. The file is still .config/fish/config.fish.
		p := filepath.Join(home, ".config", "fish", "config.fish")
		ok := exists(p)
		return p, ok, nil
	case ShellWindows:
		// Windows has no startup file; PATH lives in the environment.
		return "", true, nil
	default:
		return "", false, fmt.Errorf("unsupported shell %q", sh)
	}
}

// pathBlock renders the block this package manages for a shell.
func pathBlock(sh Shell, dir string) string {
	var lines []string
	lines = append(lines, beginMarker)
	lines = append(lines, "# Added by the lensyxe installer. Remove with `lensyxe installer --remove`.")
	switch sh {
	case ShellFish:
		// fish prepends to a variable rather than exporting PATH.
		lines = append(lines, "set -gx LENSYXE_HOME \""+dir+"\"")
		lines = append(lines,
			"if not contains $PATH \"$LENSYXE_HOME\"\n"+
				"    set -gx PATH $LENSYXE_HOME $PATH\n"+
				"end")
	case ShellWindows:
		lines = append(lines, "# The Windows PATH is set in the user environment, not here.")
		lines = append(lines, WindowsSetPathCommand(dir))
	default:
		lines = append(lines, "export PATH=\""+dir+":$PATH\"")
	}
	lines = append(lines, endMarker)
	return strings.Join(lines, "\n") + "\n"
}

// AddPath returns the startup file's contents with the PATH entry added.
//
// Idempotent: a file that already contains the block is returned unchanged, and
// a file that already lists the directory by some other means is left alone too,
// because a second copy is noise even when it was not written by us.
func AddPath(contents string, sh Shell, dir string) string {
	if HasPathBlock(contents) || alreadyOnPath(contents, dir) {
		return contents
	}
	block := pathBlock(sh, dir)
	if contents == "" {
		return block
	}
	if !strings.HasSuffix(contents, "\n") {
		contents += "\n"
	}
	return contents + "\n" + block
}

// RemovePath returns the startup file's contents with the managed block removed.
//
// Only the marked block is removed. Anything the user wrote, including a PATH
// line of their own, is left exactly as it was.
func RemovePath(contents string) string {
	start := strings.Index(contents, beginMarker)
	if start < 0 {
		return contents
	}
	end := strings.Index(contents[start:], endMarker)
	if end < 0 {
		// A begin marker with no end marker is a half-finished edit from a
		// crashed install. Removing to the end of file is the only safe
		// interpretation of "undo what we started".
		return strings.TrimRight(contents[:start], "\n")
	}
	stop := start + end + len(endMarker)
	// Swallow the newline that terminates the end marker.
	if stop < len(contents) && contents[stop] == '\n' {
		stop++
	}
	prefix := contents[:start]
	// AddPath inserts a blank line before the block. Removal has to take it
	// back too, or every install-then-remove cycle leaves a growing run of empty
	// lines behind and the file is never byte-identical to what it was.
	if strings.HasSuffix(prefix, "\n\n") {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix + contents[stop:]
}

// HasPathBlock reports whether the contents carry the managed block.
func HasPathBlock(contents string) bool {
	return strings.Contains(contents, beginMarker) && strings.Contains(contents, endMarker)
}

// alreadyOnPath reports whether the directory is already referenced, by us or by
// the user.
func alreadyOnPath(contents, dir string) bool {
	if dir == "" {
		return false
	}
	for _, line := range strings.Split(contents, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, dir) {
			return true
		}
	}
	return false
}

// DesktopEntry renders a freedesktop.org .desktop file.
//
// Written by hand rather than templated from a third-party library because the
// format is a flat INI with three required keys and the alternative is a
// dependency that exists to emit eleven lines.
func DesktopEntry(execPath, iconPath, comment string) string {
	var b strings.Builder
	b.WriteString("[Desktop Entry]\n")
	b.WriteString("Type=Application\n")
	b.WriteString("Name=Lensyxe\n")
	if comment != "" {
		b.WriteString("Comment=" + comment + "\n")
	}
	b.WriteString("Exec=" + execPath + " serve --open\n")
	b.WriteString("Icon=" + iconPath + "\n")
	// Categories place it in the development menu rather than the utilities one.
	b.WriteString("Categories=Development;\n")
	b.WriteString("Terminal=false\n")
	// StartupWMClass is absent because there is no window to associate yet.
	b.WriteString("Keywords=engineering;health;metrics;repository;\n")
	b.WriteString("MimeType=inode/directory;\n")
	return b.String()
}

// SystemdUserUnit renders a systemd user unit for `lensyxe serve`.
//
// A *user* unit rather than a system one deliberately: a system unit needs root
// to install, and this tool does not escalate. A user unit starts with the login
// session, which is what "monitor my workspace while I work" means.
func SystemdUserUnit(execPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Lensyxe local dashboard server
Documentation=https://github.com/zelvior/lensyxe
After=network.target

[Service]
Type=simple
ExecStart=%s serve
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, execPath)
}

// LaunchdPlist renders a per-user LaunchAgent for `lensyxe serve`.
func LaunchdPlist(execPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>dev.lensyxe.serve</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>serve</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <false/>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, execPath,
		filepath.Join(homeDir(), "Library", "Logs", "lensyxe.log"),
		filepath.Join(homeDir(), "Library", "Logs", "lensyxe.log"))
}

// WindowsServiceCommand returns the sc.exe invocation that registers
// `lensyxe serve` as a Windows service.
//
// Rendered rather than executed: creating a service needs an elevated prompt, and
// a tool that silently elevates is a tool nobody trusts. The caller shows the
// command and runs it only if asked.
func WindowsServiceCommand(execPath string) string {
	return fmt.Sprintf(
		`sc.exe create LensyxeServe binPath= "\"%s\" serve" start= auto DisplayName= "Lensyxe Dashboard Server"`,
		execPath)
}

// WindowsUnregisterServiceCommand returns the removal counterpart.
func WindowsUnregisterServiceCommand() string {
	return "sc.exe delete LensyxeServe"
}

func exists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
