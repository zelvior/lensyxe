import * as vscode from 'vscode';
import { Snapshot } from '../types';

/** The collection name shown in the Problems panel. */
export const DIAGNOSTIC_SOURCE = 'lensyxe';

/**
 * Maps file complexity and hotspot findings onto editor diagnostics.
 *
 * Two rules keep this from becoming noise, which is the failure mode that makes
 * people switch a linter off permanently:
 *
 * 1. Diagnostics are grouped into one per file, not one per finding. A 900-line
 *    file that is both large and complex gets one entry carrying both reasons,
 *    rather than two squiggles on the same line saying the same thing.
 *
 * 2. The whole file is not underlined. A range covering hundreds of lines is
 *    unreadable and drowns out real problems. The diagnostic is anchored to the
 *    first line, with the measurements in the message.
 */
export class DiagnosticsProvider {
  private readonly collection: vscode.DiagnosticCollection;

  constructor() {
    this.collection = vscode.languages.createDiagnosticCollection('lensyxe');
  }

  dispose(): void {
    this.collection.dispose();
  }

  clear(): void {
    this.collection.clear();
  }

  /**
   * Replaces all diagnostics for the workspace.
   *
   * Everything is cleared and rebuilt rather than diffed. A diff is the
   * cheaper option in the common case, but it has to carry identity across runs
   * to avoid stale entries, and a stale diagnostic on a file that no longer has
   * the problem is worse than a few milliseconds of work.
   */
  update(snapshot: Snapshot, enabled: boolean, complexityThreshold: number): void {
    this.collection.clear();
    if (!enabled) {
      return;
    }

    const root = vscode.Uri.file(snapshot.root);

    for (const [relative, diagnostic] of this.build(snapshot, complexityThreshold)) {
      const uri = vscode.Uri.joinPath(root, relative);
      // set() takes a list even for one entry. Replacing rather than appending
      // is what keeps a resolved finding from lingering after a re-analysis.
      this.collection.set(uri, [diagnostic]);
    }
  }

  private build(
    snapshot: Snapshot,
    complexityThreshold: number,
  ): Array<[string, vscode.Diagnostic]> {
    const out = new Map<string, vscode.Diagnostic>();
    const byPath = new Map<string, string[]>();

    const add = (relative: string, message: string, severity: vscode.DiagnosticSeverity, code: string) => {
      const existing = byPath.get(relative);
      if (existing) {
        existing.push(message);
        return;
      }
      byPath.set(relative, [message]);

      const diagnostic = new vscode.Diagnostic(
        // Anchored to the first line. The measurement is a property of the file,
        // so there is no more specific line to point at, and a range spanning
        // the whole file is visually worse than useless.
        new vscode.Range(0, 0, 0, 0),
        message,
        severity,
      );
      diagnostic.source = DIAGNOSTIC_SOURCE;
      diagnostic.code = code;
      out.set(relative, diagnostic);
    };

    // ---------------------------------------------------------- hotspots
    for (const h of snapshot.code.hotspots) {
      // The rationale is the CLI's own explanation of which factors it had, and
      // is more useful than anything this extension could reconstruct. It is
      // also what tells a reader that a candidate is not a confirmed hotspot.
      const kind = h.confirmed ? 'Confirmed hotspot' : 'Hotspot candidate';
      add(
        h.path,
        `${kind}: ${h.rationale}`,
        h.confirmed ? vscode.DiagnosticSeverity.Warning : vscode.DiagnosticSeverity.Information,
        'hotspot',
      );
    }

    // --------------------------------------------------------- complexity
    for (const f of snapshot.code.complexity.worst_files) {
      if (f.estimated_complexity < complexityThreshold) {
        continue;
      }
      // `high` and `very_high` warrant a warning; `moderate` is reported as
      // information unless the user has lowered the threshold. Defaulting
      // everything to Warning is how a diagnostic provider gets ignored.
      const severity =
        f.level === 'very_high' || f.level === 'high'
          ? vscode.DiagnosticSeverity.Warning
          : vscode.DiagnosticSeverity.Information;
      add(
        f.path,
        `Estimated complexity ${f.estimated_complexity.toFixed(1)} (${f.level ?? 'unknown'}) ` +
          `across ${f.functions} functions, ${f.branch_points} branch points, ` +
          `max nesting ${f.max_nesting}.`,
        severity,
        'complexity',
      );
    }

    // -------------------------------------------------------------- risks
    for (const risk of snapshot.risks) {
      if (!risk.subject) {
        continue;
      }
      const severity =
        risk.severity === 'critical'
          ? vscode.DiagnosticSeverity.Error
          : risk.severity === 'high'
            ? vscode.DiagnosticSeverity.Warning
            : vscode.DiagnosticSeverity.Information;
      add(
        risk.subject,
        `${risk.title} (${risk.severity}). ${risk.detail}`,
        severity,
        risk.id,
      );
    }

    // Second pass: now that every reason for a file is collected, fold them
    // into the single diagnostic for that file.
    for (const [relative, messages] of byPath) {
      const diagnostic = out.get(relative);
      if (!diagnostic) continue;
      if (messages.length > 1) {
        diagnostic.message = messages.join('\n\n');
      }
    }

    return [...out.entries()];
  }
}