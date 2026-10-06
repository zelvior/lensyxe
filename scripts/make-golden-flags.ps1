# This script creates the `golden_flag_test.go` files that register the
# `-update` flag in every test package.
#
# It exists because the registration is mandatory in each package and easy to
# forget when one is added: `go test ./... -update` passes the flag to every
# test binary, and a binary that does not define it fails outright. Run this
# after adding a test package:
#
#   pwsh scripts/make-golden-flags.ps1
#
# The files are committed rather than generated at test time, because a flag
# registered from a helper that only some packages import is exactly the
# half-working state this script prevents.

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

$doc = @(
    "// This blank import registers the -update flag in this test binary.",
    "//",
    "// `go test ./... -update` passes the flag to every test binary it builds, and",
    "// a binary that does not define it fails with `flag provided but not defined`.",
    "// One import per test package is what makes the documented command work",
    "// everywhere rather than only in the package that owns the golden files."
)

# Every directory containing a _test.go file, and the package name it declares.
$targets = @()

function Add-Package {
    param([string]$Dir, [string]$Package)

    $tests = Get-ChildItem $Dir -Filter '*_test.go' -ErrorAction SilentlyContinue
    if (-not $tests) { return }

    $path = Join-Path $Dir "golden_flag_test.go"
    if (Test-Path $path) {
        Write-Output "skip   $($path.Substring($root.Length + 1)) (exists)"
        return
    }

    $lines = @($doc) + @(
        "package $Package",
        "",
        'import _ "github.com/zelvior/lensyxe/internal/golden"',
        ""
    )
    [System.IO.File]::WriteAllText($path, ($lines -join "`n"))
    Write-Output "wrote  $($path.Substring($root.Length + 1))"
}

# The internal packages. The directory name is the package name in every case.
foreach ($n in @(
        "ai", "analyzer", "ci", "code", "compare", "config", "dependencies",
        "gates", "git", "golden", "history", "metrics", "monorepo", "report",
        "risk", "server", "storage", "watch")) {
    Add-Package (Join-Path $root "internal\$n") $n
}

Add-Package (Join-Path $root "pkg\models") "models"
Add-Package (Join-Path $root "cmd\lensyxe") "main"
Add-Package (Join-Path $root "tests") "tests"
Add-Package (Join-Path $root "examples\healthy-go\geometry") "geometry"
Add-Package (Join-Path $root "examples\risky-go\legacy") "legacy"