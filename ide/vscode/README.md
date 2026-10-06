# Lensyxe for VS Code

Repository health, file complexity, and hotspot warnings inside the editor.

The extension is a viewer. It runs `lensyxe analyze --format json`, parses the
snapshot, and renders it. It contains no scoring, no thresholds, and no analysis
logic of its own, so the score in the status bar, the squiggle on a file, and
`lensyxe analyze` in a terminal cannot disagree.

## Requirements

- VS Code 1.90 or newer
- The `lensyxe` CLI on `PATH`, or an absolute path in the settings

## Build and install

```bash
npm install
npm run compile
npm run package
code --install-extension lensyxe-vscode-0.1.0.vsix
```

## Commands

| Command | What it does |
| :--- | :--- |
| `Lensyxe: Analyze Repository` | Re-runs the analysis now. |
| `Lensyxe: Open Local Dashboard` | Runs `lensyxe serve --open`, which binds loopback only. |
| `Lensyxe: Show Health Breakdown` | Prints the score table to the output channel. |

## Settings

| Setting | Default | Purpose |
| :--- | :--- | :--- |
| `lensyxe.executablePath` | `lensyxe` | Path to the binary. A bare name is resolved on `PATH`. |
| `lensyxe.enableInlineDiagnostics` | `true` | Editor diagnostics. |
| `lensyxe.runOnSave` | `true` | Re-analyze after a save. |
| `lensyxe.debounceMs` | `750` | Coalescing window for rapid events. |
| `lensyxe.analysisTimeoutMs` | `60000` | Abort the CLI after this long. |
| `lensyxe.complexityWarningThreshold` | `10` | Complexity at which a file is reported. |
| `lensyxe.showStatusBar` | `true` | Show the score in the status bar. |

## Verifying

```bash
npm run compile
npm run smoke -- /path/to/lensyxe /path/to/a/repository
```

`npm run smoke` drives the compiled CLI client against a real binary and checks
that the snapshot parses, that two runs agree, that a missing binary gives an
actionable error, and that a superseded run stays silent.

## Full documentation

[docs/VSCODE_EXTENSION.md](../../docs/VSCODE_EXTENSION.md) covers the settings in
detail, how the scheduling avoids editor lag, and the design decisions behind
showing an unmeasured dimension as "not measured" rather than as zero.

## License

MIT. See [LICENSE](LICENSE).