import * as vscode from 'vscode';
import {
  Metric,
  Snapshot,
  complexityIcon,
  scoreTone,
  severityIcon,
  severityRank,
} from '../types';

/**
 * A node in the Engineering Health tree.
 *
 * The three scored dimensions come from `health.metrics`, which is the CLI's own
 * list. Nothing is hardcoded, so adding a dimension to the scorer makes it appear
 * here without an extension change — and, more importantly, the sidebar cannot
 * drift from the score the CLI prints.
 */
export type HealthNode =
  | { kind: 'summary'; snapshot: Snapshot }
  | { kind: 'metric'; metric: Metric; snapshot: Snapshot }
  | { kind: 'stat'; parent: string; label: string; value: string; icon?: string }
  | { kind: 'file'; parent: string; path: string; detail: string; icon: string; severity?: string }
  | { kind: 'risk'; risk: Snapshot['risks'][number]; snapshot: Snapshot }
  | { kind: 'notMeasured'; label: string; reason: string }
  | { kind: 'message'; text: string; icon: string };

/**
 * Semantic colours for score bands.
 *
 * These are `charts.*` ThemeColors rather than hex literals for two reasons: a
 * ThemeIcon's colour argument is typed as ThemeColor, so a hex string does not
 * compile, and the named colours follow the user's theme. A sidebar that is
 * legible in a light theme and unreadable in a dark one is not a detail worth
 * arguing about.
 */
const TONE_COLOR: Record<ReturnType<typeof scoreTone>, vscode.ThemeColor> = {
  good: new vscode.ThemeColor('charts.green'),
  ok: new vscode.ThemeColor('charts.blue'),
  warn: new vscode.ThemeColor('charts.yellow'),
  bad: new vscode.ThemeColor('charts.red'),
};

/**
 * Renders the score as a small bar.
 *
 * A bar rather than a bare number because the sidebar sits next to the score,
 * and "which of these is actually bad" is answered by position faster than by
 * reading digits.
 */
function scoreBar(score: number): string {
  const filled = Math.round((score / 100) * 10);
  return '\u2588'.repeat(filled) + '\u2591'.repeat(10 - filled);
}

export class HealthTreeProvider implements vscode.TreeDataProvider<HealthNode> {
  private readonly emitter = new vscode.EventEmitter<HealthNode | undefined>();
  readonly onDidChangeTreeData = this.emitter.event;

  private snapshot: Snapshot | undefined;
  private root: string | undefined;
  private busy = false;
  private lastError: string | undefined;

  /** Replaces the displayed snapshot and redraws. */
  update(snapshot: Snapshot, root: string): void {
    this.snapshot = snapshot;
    this.root = root;
    this.lastError = undefined;
    this.busy = false;
    this.emitter.fire(undefined);
  }

  /** Shows a transient state while a run is in flight. */
  setBusy(root: string): void {
    this.busy = true;
    this.root = root;
    this.lastError = undefined;
    this.emitter.fire(undefined);
  }

  /** Shows a failure, keeping any previously good snapshot visible. */
  setError(message: string): void {
    this.busy = false;
    this.lastError = message;
    this.emitter.fire(undefined);
  }

  getSnapshot(): Snapshot | undefined {
    return this.snapshot;
  }

  getTreeItem(node: HealthNode): vscode.TreeItem {
    switch (node.kind) {
      case 'summary': {
        const { health } = node.snapshot;
        const item = new vscode.TreeItem(
          `${health.score.toFixed(1)} / 100  \u00b7  grade ${health.grade}`,
          vscode.TreeItemCollapsibleState.Collapsed,
        );
        item.iconPath = new vscode.ThemeIcon(
          health.score >= 85 ? 'pass' : health.score >= 70 ? 'pulse' : 'warning',
        );
        item.description = scoreBar(health.score);
        item.tooltip = new vscode.MarkdownString(
          [
            `**${health.score.toFixed(1)} / 100** (grade ${health.grade})`,
            '',
            health.summary,
            '',
            `Analyzed in ${node.snapshot.duration_ms} ms.`,
          ].join('\n'),
        );
        item.contextValue = 'summary';
        return item;
      }

      case 'metric': {
        const { metric } = node;
        const item = new vscode.TreeItem(
          metric.label,
          vscode.TreeItemCollapsibleState.Collapsed,
        );

        if (!metric.applicable) {
          // Not-applicable is not zero. A directory that is not a git repository
          // has no cadence, which says nothing bad about the code; showing it as
          // 0.0 would be inventing a measurement.
          item.description = 'not measured';
          item.iconPath = new vscode.ThemeIcon('circle-slash', TONE_COLOR.warn);
          item.tooltip = new vscode.MarkdownString(
            `**${metric.label}** is not applicable to this repository, so its weight ` +
              `(${Math.round(metric.weight * 100)}%) is redistributed across the ` +
              'dimensions that could be measured.',
          );
          return item;
        }

        item.description = `${metric.score.toFixed(1)}  ${Math.round(metric.weight * 100)}%`;
        item.iconPath = new vscode.ThemeIcon('graph', TONE_COLOR[scoreTone(metric.score)]);
        item.tooltip = new vscode.MarkdownString(
          [
            `**${metric.label}** \u2014 ${metric.score.toFixed(1)} / 100`,
            `Weight: ${Math.round(metric.weight * 100)}%`,
            metric.summary ? `\n${metric.summary}` : '',
          ]
            .filter(Boolean)
            .join('\n\n'),
        );
        item.contextValue = 'metric';
        return item;
      }

      case 'stat': {
        const item = new vscode.TreeItem(node.label, vscode.TreeItemCollapsibleState.None);
        item.description = node.value;
        item.iconPath = new vscode.ThemeIcon(node.icon ?? 'symbol-field');
        return item;
      }

      case 'file': {
        const item = new vscode.TreeItem(node.path, vscode.TreeItemCollapsibleState.None);
        item.description = node.detail;
        item.iconPath = new vscode.ThemeIcon(node.icon);
        item.tooltip = new vscode.MarkdownString(
          `\`${node.path}\`\n\n${node.detail}`,
        );
        // The point of the sidebar: a click lands in the file, not in a report.
        item.command = {
          command: 'vscode.open',
          title: 'Open',
          arguments: [
            vscode.Uri.file(this.absolute(node.path)),
            { selection: undefined, preview: true },
          ],
        };
        item.contextValue = node.severity ? `file:${node.severity}` : 'file';
        return item;
      }

      case 'risk': {
        const item = new vscode.TreeItem(
          node.risk.title,
          vscode.TreeItemCollapsibleState.Collapsed,
        );
        item.iconPath = new vscode.ThemeIcon(severityIcon(node.risk.severity));
        item.description = `${node.risk.severity} \u00b7 impact ${node.risk.impact.toFixed(1)}`;
        item.tooltip = new vscode.MarkdownString(
          [
            `**${node.risk.title}**`,
            '',
            node.risk.detail,
            node.risk.recommendation ? `\n${node.risk.recommendation}` : '',
            '',
            ...node.risk.evidence.map((e) => `- ${e.label}: ${e.detail}`),
          ]
            .filter(Boolean)
            .join('\n'),
        );
        item.contextValue = `risk:${node.risk.severity}`;
        return item;
      }

      case 'notMeasured': {
        const item = new vscode.TreeItem(node.label, vscode.TreeItemCollapsibleState.None);
        item.description = 'not measured';
        item.iconPath = new vscode.ThemeIcon('circle-slash');
        item.tooltip = new vscode.MarkdownString(`**${node.label}** is deliberately not measured.\n\n${node.reason}`);
        return item;
      }

      case 'message': {
        const item = new vscode.TreeItem(node.text, vscode.TreeItemCollapsibleState.None);
        item.iconPath = new vscode.ThemeIcon(node.icon);
        return item;
      }
    }
  }

  getChildren(node?: HealthNode): HealthNode[] {
    const snap = this.snapshot;

    if (this.busy) {
      return [{ kind: 'message', text: 'Analyzing\u2026', icon: 'loading~spin' }];
    }
    if (this.lastError) {
      return [{ kind: 'message', text: this.lastError, icon: 'error' }];
    }
    if (!snap || !node) {
      return [{ kind: 'message', text: 'No analysis yet. Run one from the toolbar.', icon: 'info' }];
    }

    if (node.kind === 'summary') {
      return [
        { kind: 'summary', snapshot: node.snapshot },
        ...this.dimensionNodes(node.snapshot),
        ...this.riskNodes(node.snapshot),
        ...this.omittedNodes(),
      ];
    }

    if (node.kind === 'metric') {
      return this.metricChildren(node.metric.key, node.snapshot);
    }

    if (node.kind === 'risk') {
      const children: HealthNode[] = [];
      if (node.risk.subject) {
        children.push({
          kind: 'file',
          parent: 'risk',
          path: node.risk.subject,
          detail: node.risk.title,
          icon: 'go-to-file',
          severity: node.risk.severity,
        });
      }
      for (const e of node.risk.evidence) {
        children.push({
          kind: 'stat',
          parent: 'risk',
          label: e.label,
          value: e.detail,
          icon: 'evidence',
        });
      }
      return children;
    }

    return [];
  }

  private dimensionNodes(snap: Snapshot): HealthNode[] {
    return snap.health.metrics.map((metric) => ({ kind: 'metric', metric, snapshot: snap }));
  }

  private metricChildren(key: string, snap: Snapshot): HealthNode[] {
    const out: HealthNode[] = [];

    if (key === 'code') {
      const c = snap.code;
      out.push(
        { kind: 'stat', parent: key, label: 'Files', value: `${c.source_files} source / ${c.test_files} test`, icon: 'files' },
        { kind: 'stat', parent: key, label: 'Lines', value: `${c.total_lines} physical / ${c.code_lines} code`, icon: 'symbol-ruler' },
        { kind: 'stat', parent: key, label: 'Largest file', value: `${c.max_file_lines} LOC`, icon: 'arrow-up' },
      );

      if (c.complexity.measured) {
        out.push({
          kind: 'stat',
          parent: key,
          label: 'Complexity',
          value: `avg ${c.complexity.average_complexity.toFixed(1)}, max ${c.complexity.max_complexity.toFixed(1)}`,
          icon: complexityIcon('moderate'),
        });
      } else {
        out.push({
          kind: 'notMeasured',
          label: 'Complexity',
          reason:
            'No control-flow language was detected. Declarative files such as YAML and SQL are excluded ' +
            'because their conditionals are mapping keys rather than branches, so scoring them would produce ' +
            'a confident meaningless number.',
        });
      }

      for (const h of c.hotspots) {
        out.push({
          kind: 'file',
          parent: key,
          path: h.path,
          detail: `${h.lines} LOC \u00b7 cx ${h.complexity.toFixed(1)}${h.confirmed ? ' \u00b7 confirmed' : ''}`,
          icon: h.confirmed ? 'error' : 'warning',
        });
      }
    }

    if (key === 'dependency') {
      const d = snap.dependencies;
      if (!d.detected) {
        out.push({
          kind: 'notMeasured',
          label: 'Dependencies',
          reason: d.note ?? 'No supported dependency manifest was found.',
        });
      } else {
        out.push(
          { kind: 'stat', parent: key, label: 'Ecosystems', value: String(d.ecosystems.length), icon: 'package' },
          { kind: 'stat', parent: key, label: 'Declared', value: `${d.direct} direct / ${d.dev} dev / ${d.total} total`, icon: 'symbol-namespace' },
          { kind: 'stat', parent: key, label: 'Transitive', value: String(d.transitive), icon: 'git-branch' },
        );
        for (const e of d.ecosystems) {
          out.push({
            kind: 'stat',
            parent: key,
            label: e.name,
            value: e.lockfile ? `locked (${e.lockfile})` : 'no lockfile',
            icon: e.lockfile ? 'lock' : 'unlock',
          });
        }
      }
    }

    if (key === 'git') {
      const g = snap.git;
      if (!g.is_repository) {
        out.push({
          kind: 'notMeasured',
          label: 'Git history',
          reason:
            'The analyzed path is not a git repository, so cadence, authorship, and churn cannot be measured. ' +
            'This is reported rather than scored as zero, and the remaining weights are redistributed.',
        });
      } else {
        out.push(
          { kind: 'stat', parent: key, label: 'Commits in window', value: String(g.commits ?? 0), icon: 'git-commit' },
          { kind: 'stat', parent: key, label: 'Authors', value: String(g.authors ?? 0), icon: 'organization' },
        );
      }
    }

    return out;
  }

  private riskNodes(snap: Snapshot): HealthNode[] {
    if (snap.risks.length === 0) {
      return [{ kind: 'message', text: 'No risks detected', icon: 'pass' }];
    }
    // Worst first, then by title so the order is stable between runs.
    const sorted = [...snap.risks].sort((a, b) => {
      const bySeverity = severityRank(b.severity) - severityRank(a.severity);
      if (bySeverity !== 0) return bySeverity;
      return a.title.localeCompare(b.title);
    });
    return sorted.map((risk) => ({ kind: 'risk', risk, snapshot: snap }));
  }

  /**
   * Signals the CLI deliberately does not measure.
   *
   * These are listed rather than omitted because the most common question about
   * a health score is "why is there no coverage number", and answering it with
   * silence looks like an oversight. Showing the reason is more useful and keeps
   * the extension from implying the tool measures something it does not.
   */
  private omittedNodes(): HealthNode[] {
    return [
      { kind: 'stat', parent: 'root', label: '', value: '', icon: 'blank' },
      { kind: 'notMeasured', label: 'Code coverage', reason: 'Measuring it would mean executing the test suite. The test-to-code file ratio is reported inside Code health instead, and is never called coverage.' },
      { kind: 'notMeasured', label: 'Build duration', reason: 'The build is not executed. Timing it would measure the machine and the CI runner as much as the code.' },
      { kind: 'notMeasured', label: 'Vulnerabilities', reason: 'A vulnerability count needs a database and a network call, both of which this tool deliberately avoids.' },
    ];
  }

  /** Joins a repository-relative path onto the analyzed root. */
  private absolute(relative: string): string {
    const root = this.snapshot?.root ?? this.root ?? '';
    if (!root) return relative;
    return vscode.Uri.joinPath(vscode.Uri.file(root), relative).fsPath;
  }
}