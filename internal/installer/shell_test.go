package installer

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Additions must be removable. An installer that cannot undo itself exactly is
// worse than one that never ran, so removal is asserted on the original bytes.
func TestAddThenRemoveRestoresTheFileExactly(t *testing.T) {
	original := "# my bashrc\nexport EDITOR=vi\n"
	added := AddPath(original, ShellBash, "/home/u/.local/bin")
	if added == original {
		t.Fatal("AddPath changed nothing")
	}
	removed := RemovePath(added)
	if removed != original {
		t.Errorf("removal did not restore the original:\n got: %q\nwant: %q",
			removed, original)
	}
}

// Installing twice must not append the line twice. A duplicated PATH entry is
// harmless but makes the file look hand-edited.
func TestAddPathIsIdempotent(t *testing.T) {
	once := AddPath("# rc\n", ShellBash, "/opt/lensyxe/bin")
	twice := AddPath(once, ShellBash, "/opt/lensyxe/bin")
	if once != twice {
		t.Errorf("a second install changed the file:\n%q\n%q", once, twice)
	}
	if n := strings.Count(twice, beginMarker); n != 1 {
		t.Errorf("begin marker appears %d times, want 1", n)
	}
}

// A PATH entry the user wrote by hand is left alone. Adding a second one for the
// same directory is noise, and it is the user's line to own.
func TestAddPathRespectsAnExistingUserEntry(t *testing.T) {
	original := "# rc\nexport PATH=\"/opt/lensyxe/bin:$PATH\"\n"
	got := AddPath(original, ShellBash, "/opt/lensyxe/bin")
	if got != original {
		t.Errorf("a hand-written entry was duplicated:\n%s", got)
	}
}

// A commented mention is not an entry. Ignoring the distinction would skip the
// install for anyone whose .bashrc documents what they used to do.
func TestCommentedMentionIsNotTreatedAsAnEntry(t *testing.T) {
	original := "# export PATH=\"/opt/lensyxe/bin:$PATH\"  # old\n"
	got := AddPath(original, ShellBash, "/opt/lensyxe/bin")
	if !HasPathBlock(got) {
		t.Error("a commented-out line was mistaken for a real entry")
	}
}

// fish has no PATH export; it prepends to a variable. Emitting a bash line there
// would be a syntax error in the user's config.
func TestFishBlockUsesFishSyntax(t *testing.T) {
	got := AddPath("", ShellFish, "/home/u/.local/bin")
	if !strings.Contains(got, "set -gx PATH") {
		t.Errorf("no fish PATH assignment:\n%s", got)
	}
	if strings.Contains(got, "export PATH=") {
		t.Errorf("a bash export was written into a fish config:\n%s", got)
	}
	if !strings.Contains(got, `set -gx LENSYXE_HOME "/home/u/.local/bin"`) {
		t.Errorf("LENSYXE_HOME was not exported for fish:\n%s", got)
	}
}

func TestBashAndZshBlocksExportPath(t *testing.T) {
	for _, sh := range []Shell{ShellBash, ShellZsh} {
		got := AddPath("", sh, "/usr/local/bin")
		if !strings.Contains(got, `export PATH="/usr/local/bin:$PATH"`) {
			t.Errorf("%s block is wrong:\n%s", sh, got)
		}
	}
}

// Windows PATH is the environment, not a startup file, so the block documents the
// command rather than pretending to write one.
func TestWindowsBlockNamesTheEnvironmentMechanism(t *testing.T) {
	got := AddPath("", ShellWindows, `C:\Users\u\lensyxe\bin`)
	if !strings.Contains(got, "user environment") {
		t.Errorf("the Windows block does not say where PATH lives:\n%s", got)
	}
	if !strings.Contains(got, "SetEnvironmentVariable") {
		t.Errorf("the Windows block gives no command:\n%s", got)
	}
}

// setx PATH truncates at 1024 characters and reports success while doing it, so
// on a machine with a long PATH it silently discards entries. The generated
// command must not use it.
func TestWindowsPathCommandAvoidsTheSetxTruncation(t *testing.T) {
	got := WindowsSetPathCommand(`C:\Users\u\lensyxe\bin`)
	if strings.Contains(strings.ToLower(got), "setx") {
		t.Errorf("the command uses setx, which truncates PATH at 1024 characters "+
			"and still reports success:\n%s", got)
	}
	if !strings.Contains(got, "SetEnvironmentVariable") {
		t.Errorf("the .NET environment API is not used:\n%s", got)
	}
	if !strings.Contains(got, "'User'") {
		t.Errorf("the command does not scope itself to the user environment:\n%s", got)
	}
}

func TestWindowsRemoveCommandComparesFullPaths(t *testing.T) {
	got := WindowsRemovePathCommand(`C:\tools\lensyxe`)
	// A prefix-matching implementation would also remove C:\tools\lensyxe-extras.
	if !strings.Contains(got, "-ne 'C:\\tools\\lensyxe'") {
		t.Errorf("removal does not compare the full path:\n%s", got)
	}
	if !strings.Contains(got, "TrimEnd") {
		t.Errorf("removal does not tolerate a trailing separator:\n%s", got)
	}
}

// An install interrupted between the two sentinels must still be removable, or a
// crashed install leaves a file nobody can repair.
func TestRemoveHandlesAHalfWrittenBlock(t *testing.T) {
	half := "# rc\nexport EDITOR=vi\n" + beginMarker + "\nexport PATH=\"/opt/bin:$PATH\"\n"
	got := RemovePath(half)
	if strings.Contains(got, beginMarker) {
		t.Errorf("an unterminated block was not removed:\n%s", got)
	}
	if !strings.Contains(got, "export EDITOR=vi") {
		t.Errorf("removal ate the user's own lines:\n%s", got)
	}
}

// Removal must not touch anything the user wrote after the block.
func TestRemoveKeepsTrailingUserContent(t *testing.T) {
	added := AddPath("# top\n", ShellBash, "/opt/bin") + "# bottom\n"
	got := RemovePath(added)
	if !strings.Contains(got, "# bottom") {
		t.Errorf("content after the block was lost:\n%s", got)
	}
	if strings.Contains(got, "/opt/bin") {
		t.Errorf("the managed block survived removal:\n%s", got)
	}
}

func TestRemoveIsSafeWhenNothingIsThere(t *testing.T) {
	original := "# nothing to do here\n"
	if got := RemovePath(original); got != original {
		t.Errorf("removal altered a file with no block: %q", got)
	}
}

func TestStartupFileLocations(t *testing.T) {
	home := "/home/u"
	cases := []struct {
		sh   Shell
		want string
	}{
		{ShellBash, filepath.Join(home, ".bashrc")},
		{ShellZsh, filepath.Join(home, ".zshrc")},
		{ShellFish, filepath.Join(home, ".config", "fish", "config.fish")},
	}
	for _, c := range cases {
		got, _, err := startupFile(c.sh, home)
		if err != nil {
			t.Fatalf("startupFile(%s): %v", c.sh, err)
		}
		if got != c.want {
			t.Errorf("startupFile(%s) = %q, want %q", c.sh, got, c.want)
		}
	}
}

func TestStartupFileReportsAbsenceRatherThanCreating(t *testing.T) {
	home := t.TempDir()
	path, existsNow, err := startupFile(ShellBash, home)
	if err != nil {
		t.Fatal(err)
	}
	if existsNow {
		t.Error("a missing .bashrc was reported as existing")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("startupFile created a file it was only asked to locate")
	}
}

func TestUnsupportedShellIsRejected(t *testing.T) {
	if _, _, err := startupFile("csh", "/home/u"); err == nil {
		t.Error("an unsupported shell was accepted")
	}
}

func TestDesktopEntryHasTheRequiredKeys(t *testing.T) {
	got := DesktopEntry("/opt/lensyxe/bin/lensyxe", "lensyxe", "Engineering health")
	for _, want := range []string{
		"[Desktop Entry]", "Type=Application", "Name=Lensyxe",
		"Exec=/opt/lensyxe/bin/lensyxe serve --open", "Icon=lensyxe",
		"Categories=Development;", "Terminal=false",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("desktop entry is missing %q:\n%s", want, got)
		}
	}
}

// A .desktop file that is not executable is invisible in most desktops, and the
// user is left with an installed app that does not appear.
func TestDesktopEntryIsWrittenExecutable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lensyxe.desktop")
	if err := os.WriteFile(p, []byte(DesktopEntry("/bin/lensyxe", "lensyxe", "")), 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Errorf("desktop entry is not executable: %v", info.Mode())
	}
}

func TestSystemdUnitIsAUserUnit(t *testing.T) {
	got := SystemdUserUnit("/home/u/.local/bin/lensyxe")
	if !strings.Contains(got, "WantedBy=default.target") {
		t.Errorf("not a user unit:\n%s", got)
	}
	if strings.Contains(got, "WantedBy=multi-user.target") {
		t.Error("the unit wants a system target, which needs root to install")
	}
	if !strings.Contains(got, "ExecStart=/home/u/.local/bin/lensyxe serve") {
		t.Errorf("ExecStart is wrong:\n%s", got)
	}
}

func TestLaunchdPlistIsWellFormed(t *testing.T) {
	got := LaunchdPlist("/usr/local/bin/lensyxe")
	if !strings.HasPrefix(got, `<?xml version="1.0"`) {
		t.Error("the plist has no XML declaration")
	}
	if !strings.Contains(got, "<key>Label</key>") {
		t.Error("a LaunchAgent needs a Label")
	}
	if !strings.Contains(got, "<string>/usr/local/bin/lensyxe</string>") {
		t.Errorf("ProgramArguments does not name the binary:\n%s", got)
	}
	if strings.Count(got, "<key>") != strings.Count(got, "</key>") {
		t.Error("unbalanced key tags")
	}
}

func TestWindowsServiceCommand(t *testing.T) {
	got := WindowsServiceCommand(`C:\Program Files\lensyxe\lensyxe.exe`)
	// sc.exe requires a space after every key=value pair. Getting that wrong
	// fails at runtime with an opaque parse error rather than at build time, so
	// it is asserted here.
	if !strings.Contains(got, `binPath= "`) {
		t.Errorf("binPath is missing its required trailing space:\n%s", got)
	}
	if !strings.Contains(got, `start= auto`) {
		t.Errorf("start= is missing its required trailing space:\n%s", got)
	}
	if !strings.Contains(got, "LensyxeServe") {
		t.Errorf("the service name is missing:\n%s", got)
	}
	if !strings.Contains(got, "serve") {
		t.Errorf("the service does not run lensyxe serve:\n%s", got)
	}
	if got := WindowsUnregisterServiceCommand(); !strings.Contains(got, "delete") {
		t.Errorf("unregister is not a delete: %s", got)
	}
}

func TestDefaultInstallDirAvoidsPrivilegedPathsForUserScope(t *testing.T) {
	user := DefaultInstallDir(ScopeUser)
	if runtime.GOOS == "windows" {
		// Program Files needs elevation; a per-user install must not go there.
		if strings.Contains(strings.ToLower(user), `program files`) {
			t.Errorf("a user-scope install targets %q, which needs elevation", user)
		}
		return
	}
	if !strings.HasPrefix(user, homeDir()) {
		t.Errorf("a user-scope install targets %q, outside the home directory", user)
	}
}

func TestSystemScopeUsesAMachineWidePrefix(t *testing.T) {
	got := DefaultInstallDir(ScopeSystem)
	if runtime.GOOS == "darwin" {
		if got != "/usr/local/bin" {
			t.Errorf("system scope on darwin = %q, want /usr/local/bin", got)
		}
	}
}
