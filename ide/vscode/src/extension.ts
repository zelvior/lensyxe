import * as vscode from 'vscode';
import { sep } from 'node:path';
import { DiagnosticsProvider } from './providers/DiagnosticsProvider';
import { HealthTreeProvider } from './providers/HealthTreeProvider';
import { LensyxeError, SupersededError, analyze, serveDashboard } from './lensyxe';

let status: vscode.StatusBarItem;
// Typed as LogOutputChannel, not OutputChannel. The declared type is what every
// later call site sees, so annotating it as the base interface hides
// appendMarkdown and makes the health detail fall back to escaped text.
let output: vscode.LogOutputChannel;
let tree: HealthTreeProvider;
let diagnostics: DiagnosticsProvider;

let debounceTimer: NodeJS.Timeout | undefined;
/** Aborts the in-flight run so a newer one can replace it. */
let inFlight: AbortController | undefined;
let workspaceRoot: string | undefined;

export function activate(context: vscode.ExtensionContext): void {
  const cfg = vscode.workspace.getConfiguration('lensyxe');

  // `log: true` yields a LogOutputChannel. The plain overload returns an
  // OutputChannel, which has no appendMarkdown, and the health detail is
  // rendered as Markdown rather than as escaped text.
  output = vscode.window.createOutputChannel('Lensyxe', { log: true });
  diagnostics = new DiagnosticsProvider();
  tree = new HealthTreeProvider();

  status = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 100);
  status.command = 'lensyxe.analyze';

  context.subscriptions.push(output, diagnostics, status);
  context.subscriptions.push(
    vscode.window.registerTreeDataProvider('lensyxe.health', tree),
    vscode.commands.registerCommand('lensyxe.analyze', () => void run({ manual: true })),
    vscode.commands.registerCommand('lensyxe.openDashboard', () => void openDashboard()),
    vscode.commands.registerCommand('lensyxe.showHealthDetail', () => void showDetail()),
  );

  workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (workspaceRoot) {
    tree.setBusy(workspaceRoot);
    renderIdle();
    // Deferred so activation returns immediately. Activation must not wait on a
    // subprocess: the editor is unusable until the host finishes activating, and
    // a cold binary on a large monorepo is seconds.
    void run({ manual: false });
  } else {
    status.hide();
  }

  if (cfg.get<boolean>('runOnSave', true)) {
    context.subscriptions.push(
      vscode.workspace.onDidSaveTextDocument((doc) => {
        if (!doc.isDirty && !isAnalyzable(doc.uri)) return;
        schedule();
      }),
      vscode.workspace.onDidChangeWorkspaceFolders(() => {
        workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
        if (workspaceRoot) schedule();
      }),
      vscode.workspace.onDidOpenTextDocument((doc) => {
        if (isAnalyzable(doc.uri)) schedule();
      }),
    );
  }

  // A configuration change to anything the analysis depends on invalidates the
  // snapshot, so it is re-run rather than left stale.
  context.subscriptions.push(
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (!e.affectsConfiguration('lensyxe')) return;
      if (e.affectsConfiguration('lensyxe.enableInlineDiagnostics')) {
        const snap = tree.getSnapshot();
        if (snap) {
          diagnostics.update(
            snap,
            vscode.workspace.getConfiguration('lensyxe').get('enableInlineDiagnostics', true),
            complexityThreshold(),
          );
        }
      }
      if (e.affectsConfiguration('lensyxe.executablePath') || e.affectsConfiguration('lensyxe.complexityWarningThreshold')) {
        schedule();
      }
    }),
  );
}

export function deactivate(): void {
  inFlight?.abort();
  if (debounceTimer) clearTimeout(debounceTimer);
  // Not closing status or output: both are disposed through context.subscriptions,
  // which VS Code runs for us.
}

function config(): vscode.WorkspaceConfiguration {
  return vscode.workspace.getConfiguration('lensyxe');
}

function complexityThreshold(): number {
  return config().get<number>('complexityWarningThreshold', 10);
}

/** A file the analyzer would count as source, so a save is worth reacting to. */
function isAnalyzable(uri: vscode.Uri): boolean {
  if (uri.scheme !== 'file') return false;
  // Vendored and generated trees are ignored by the analyzer by default, so
  // saving inside one is not a reason to re-scan the repository.
  const ignored = ['node_modules', '.git', 'vendor', 'dist', 'out', '.next'];
  return !ignored.some((dir) => uri.fsPath.split(sep).includes(dir));
}

/**
 * Coalesces rapid events into one analysis.
 *
 * Saving ten files produces ten events. Without this, ten analyses would be
 * queued and the user would be looking at stale results long after the last
 * save. The newest request always wins.
 */
function schedule(): void {
  if (!workspaceRoot) return;
  if (debounceTimer) clearTimeout(debounceTimer);
  debounceTimer = setTimeout(() => {
    debounceTimer = undefined;
    void run({ manual: false });
  }, config().get<number>('debounceMs', 750));
}

interface RunOptions {
  manual: boolean;
}

async function run(opts: RunOptions): Promise<void> {
  const root = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    void vscode.window.showWarningMessage('Open a folder before running Lensyxe.');
    return;
  }
  workspaceRoot = root;

  // Supersede rather than queue. Two concurrent scans of the same tree is one
  // scan too many, and the older result is stale the moment the newer lands.
  inFlight?.abort();
  const controller = new AbortController();
  inFlight = controller;

  if (opts.manual) {
    tree.setBusy(root);
    status.text = '$(sync~spin) Lensyxe: analyzing';
    status.show();
  }

  const started = Date.now();
  try {
    const outcome = await analyze({
      cwd: root,
      executable: config().get<string>('executablePath', 'lensyxe'),
      timeoutMs: config().get<number>('analysisTimeoutMs', 60_000),
      signal: controller.signal,
    });

    tree.update(outcome.snapshot, root);
    diagnostics.update(
      outcome.snapshot,
      config().get<boolean>('enableInlineDiagnostics', true),
      complexityThreshold(),
    );
    renderScore(outcome.snapshot.health.score, outcome.durationMs);

    if (opts.manual) {
      // A quiet confirmation. No modal, no notification: the status bar and the
      // sidebar already changed, and a popup for a completed background job is
      // noise.
      output.appendLine(
        `Analyzed ${outcome.snapshot.root} in ${outcome.durationMs} ms ` +
          `(wall clock ${Date.now() - started} ms), score ${outcome.snapshot.health.score.toFixed(1)}.`,
      );
    }
  } catch (err) {
    if (err instanceof SupersededError) {
      // Expected whenever a newer analysis replaced this one. Staying silent is
      // the entire point of supersession.
      return;
    }
    if (err instanceof LensyxeError) {
      tree.setError(err.message);
      status.text = '$(warning) Lensyxe: unavailable';
      status.show();
      output.appendLine(`${err.message}${err.detail ? `\n${err.detail}` : ''}`);
      if (opts.manual) {
        void vscode.window.showErrorMessage(`Lensyxe: ${err.message}`);
      }
      return;
    }
    tree.setError('Unexpected failure.');
    output.appendLine(`Unexpected: ${String(err)}`);
    if (opts.manual) {
      void vscode.window.showErrorMessage(`Lensyxe: ${String(err)}`);
    }
  } finally {
    if (inFlight === controller) {
      inFlight = undefined;
    }
  }
}

function renderScore(score: number, durationMs: number): void {
  if (!config().get<boolean>('showStatusBar', true)) {
    status.hide();
    return;
  }
  const rounded = Math.round(score);
  status.text = `$(pulse) Lensyxe: ${rounded}/100`;
  // The precise figure and the analysis duration live in the tooltip rather than
  // the status bar, which has room for roughly this much text.
  status.tooltip = `Engineering Health ${score.toFixed(1)} / 100\nAnalyzed in ${durationMs} ms\nClick to re-run.`;
  status.show();
}

function renderIdle(): void {
  status.text = '$(pulse) Lensyxe';
  status.tooltip = 'Click to analyze this folder.';
  status.show();
}

async function openDashboard(): Promise<void> {
  const root = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    void vscode.window.showWarningMessage('Open a folder before starting the dashboard.');
    return;
  }
  try {
    await vscode.window.withProgress(
      { location: vscode.ProgressLocation.Notification, title: 'Starting the Lensyxe dashboard' },
      async () => {
        await serveDashboard({
          cwd: root,
          executable: config().get<string>('executablePath', 'lensyxe'),
        });
      },
    );
    output.appendLine('Dashboard started. It listens on loopback and stops with this window.');
  } catch (err) {
    const message = err instanceof LensyxeError ? err.message : String(err);
    const detail = err instanceof LensyxeError ? err.detail : undefined;
    output.appendLine(`${message}${detail ? `\n${detail}` : ''}`);
    void vscode.window.showErrorMessage(`Lensyxe: ${message}`);
  }
}

/**
 * Prints the score breakdown to the output channel.
 *
 * Plain text rather than Markdown. Neither OutputChannel nor LogOutputChannel
 * has appendMarkdown, so a Markdown table written there would render as literal
 * pipe characters. The sidebar tree is the rendered view; this is the copyable
 * one, and the numbers have to survive being pasted into a pull request.
 */
async function showDetail(): Promise<void> {
  const snap = tree.getSnapshot();
  if (!snap) {
    void vscode.window.showInformationMessage('No analysis yet. Run one from the sidebar toolbar.');
    return;
  }

  const pad = (s: string, n: number) => (s.length >= n ? s : s + ' '.repeat(n - s.length));
  const lines: string[] = [
    '',
    `Engineering Health ${snap.health.score.toFixed(1)} / 100  (grade ${snap.health.grade})`,
    snap.health.summary,
    '',
    `${pad('DIMENSION', 26)}${pad('SCORE', 12)}WEIGHT`,
    '-'.repeat(48),
  ];

  for (const m of snap.health.metrics) {
    if (m.applicable) {
      lines.push(`${pad(m.label, 26)}${pad(m.score.toFixed(1), 12)}${Math.round(m.weight * 100)}%`);
    } else {
      // Stated rather than drawn as a zero, matching the tree and the CLI. A
      // dimension that could not be measured says nothing bad about the code.
      lines.push(
        `${pad(m.label, 26)}${pad('not measured', 12)}${Math.round(m.weight * 100)}% redistributed`,
      );
    }
  }

  const notApplicable = snap.health.metrics.filter((m) => !m.applicable);
  if (notApplicable.length) {
    lines.push('');
    for (const m of notApplicable) {
      lines.push(`${m.label} could not be measured here, so its weight was spread across the dimensions that could.`);
    }
  }

  lines.push(
    '',
    `Analyzed in ${snap.duration_ms} ms  -  schema ${snap.schema_version}  -  ${snap.risks.length} risk(s)`,
  );

  for (const line of lines) {
    output.appendLine(line);
  }
  output.show(true);
}