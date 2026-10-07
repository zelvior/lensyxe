/**
 * Reads a `lensyxe analyze --format json` snapshot and reshapes it into the
 * responses the dashboard already renders.
 *
 * Why this exists: the dashboard is built to talk to `/api/v1`, which is served
 * by the Go process reading a local repository. On a static host there is no
 * process and no repository, so the same components are fed a snapshot the user
 * produced themselves and brought with them.
 *
 * Two properties this file is careful about:
 *
 *   - Nothing leaves the browser. The snapshot is parsed in memory and never
 *     uploaded, which is the only way a hosted page can honour a tool whose
 *     promise is local-first and no-telemetry.
 *   - The input is validated rather than cast. A JSON file is user-supplied and
 *     a missing field must produce a message naming the field, not an
 *     `undefined` flowing into a chart that renders as zero.
 */

import type {
  Evidence,
  Health,
  HealthResponse,
  HistoryResponse,
  Hotspot,
  HotspotsResponse,
  LanguageStat,
  Metric,
  Risk,
  RisksResponse,
  Severity,
  ChurnEntry,
} from './types';

/** The snapshot document, as `analyze --format json` emits it. */
interface Snapshot {
  schema_version?: string;
  tool?: string;
  version?: string;
  root?: string;
  generated_at?: string;
  duration_ms?: number;
  health?: unknown;
  code?: unknown;
  git?: unknown;
  dependencies?: unknown;
  risks?: unknown;
}

/** Thrown for input that is not a Lensyxe snapshot, carrying a usable message. */
export class SnapshotError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'SnapshotError';
  }
}

function obj(v: unknown, field: string): Record<string, unknown> {
  if (typeof v !== 'object' || v === null || Array.isArray(v)) {
    throw new SnapshotError(`"${field}" is missing or is not an object`);
  }
  return v as Record<string, unknown>;
}

function arr(v: unknown, field: string): unknown[] {
  if (!Array.isArray(v)) {
    throw new SnapshotError(`"${field}" is missing or is not a list`);
  }
  return v;
}

function num(v: unknown, fallback: number): number {
  return typeof v === 'number' && Number.isFinite(v) ? v : fallback;
}

function str(v: unknown, fallback: string): string {
  return typeof v === 'string' ? v : fallback;
}

function bool(v: unknown, fallback: boolean): boolean {
  return typeof v === 'boolean' ? v : fallback;
}

const SEVERITIES: readonly Severity[] = [
  'critical',
  'high',
  'medium',
  'low',
  'info',
];

/**
 * A severity outside the known set is coerced to `info` rather than rejected.
 *
 * The snapshot may come from a newer Lensyxe than this page. Dropping a whole
 * report because one risk carries a severity this build has never heard of is
 * the wrong failure: the score is still valid, and an unknown severity is
 * genuinely less important than a known one.
 */
function severity(v: unknown): Severity {
  return SEVERITIES.includes(v as Severity) ? (v as Severity) : 'info';
}

function toMetric(v: unknown): Metric {
  const m = obj(v, 'health.metrics[]');
  return {
    key: str(m.key, ''),
    label: str(m.label, ''),
    value: num(m.value, 0),
    display: str(m.display, ''),
    score: num(m.score, 0),
    weight: num(m.weight, 0),
    detail: str(m.detail, ''),
    applicable: bool(m.applicable, true),
  };
}

function toEvidence(v: unknown): Evidence {
  if (typeof v !== 'object' || v === null) {
    return { kind: '', label: '', detail: '' };
  }
  const e = v as Record<string, unknown>;
  return {
    kind: str(e.kind, ''),
    label: str(e.label, ''),
    detail: str(e.detail, ''),
    subject: typeof e.subject === 'string' ? e.subject : undefined,
    value: typeof e.value === 'number' ? e.value : undefined,
  };
}

function toRisk(v: unknown): Risk {
  const r = obj(v, 'risks[]');
  return {
    id: str(r.id, ''),
    severity: severity(r.severity),
    category: str(r.category, ''),
    title: str(r.title, ''),
    detail: str(r.detail, ''),
    subject: typeof r.subject === 'string' ? r.subject : undefined,
    impact: num(r.impact, 0),
    evidence: arr(r.evidence ?? [], 'risks[].evidence').map(toEvidence),
    recommendation:
      typeof r.recommendation === 'string' ? r.recommendation : undefined,
  };
}

function toHotspot(v: unknown): Hotspot {
  const h = obj(v, 'code.hotspots[]');
  return {
    path: str(h.path, ''),
    lines: num(h.lines, 0),
    language: str(h.language, ''),
    churn: num(h.churn, 0),
    complexity: num(h.complexity, 0),
    level: typeof h.level === 'string' ? h.level : undefined,
    classification: str(h.classification, ''),
    confirmed: bool(h.confirmed, false),
    rationale: str(h.rationale, ''),
  };
}

function toChurn(v: unknown): ChurnEntry {
  const c = obj(v, 'git.churn[]');
  return {
    path: str(c.path, ''),
    commits: num(c.commits, 0),
    added: num(c.added, 0),
    deleted: num(c.deleted, 0),
    score: num(c.score, 0),
  };
}

function toLanguage(v: unknown): LanguageStat {
  const l = obj(v, 'code.languages[]');
  return {
    name: str(l.name, ''),
    files: num(l.files, 0),
    lines: num(l.lines, 0),
    test_files: num(l.test_files, 0),
  };
}

/** OverviewData mirrors the shape useOverview returns for the live API. */
export interface SnapshotOverview {
  health: HealthResponse;
  risks: RisksResponse;
  hotspots: HotspotsResponse;
  history: HistoryResponse;
  /** Where the figures came from, so the UI can say so. */
  meta: { root: string; generatedAt: string; version: string; schema: string };
}

/**
 * adapt converts a parsed snapshot into the four API response shapes.
 *
 * The counts the API computes server-side (`risk_count`, `hotspot_count`, the
 * severity tallies) are recomputed here rather than read, because the snapshot
 * does not carry them as top-level fields and inventing zeros would misreport a
 * report full of findings.
 */
export function adapt(input: unknown): SnapshotOverview {
  const s = obj(input, 'document') as Snapshot;

  // `health` and `code` are the two a dashboard cannot render without. Checked
  // up front so the failure names the missing half rather than surfacing as an
  // empty gauge three components deep.
  const h = obj(s.health, 'health');
  const code = obj(s.code, 'code');
  const git = typeof s.git === 'object' && s.git !== null ? (s.git as Record<string, unknown>) : {};
  const deps =
    typeof s.dependencies === 'object' && s.dependencies !== null
      ? (s.dependencies as Record<string, unknown>)
      : {};

  const health: Health = {
    score: num(h.score, 0),
    grade: str(h.grade, ''),
    summary: str(h.summary, ''),
    metrics: arr(h.metrics ?? [], 'health.metrics').map(toMetric),
    components: num(h.components, 0),
  };

  const risks = arr(s.risks ?? [], 'risks').map(toRisk);
  const hotspots = arr(code.hotspots ?? [], 'code.hotspots').map(toHotspot);
  const churn = arr(git.churn ?? [], 'git.churn').map(toChurn);

  const tally = (want: Severity) =>
    risks.filter((r) => r.severity === want).length;

  const generatedAt = str(s.generated_at, '');
  const root = str(s.root, '');

  return {
    health: {
      root,
      version: str(s.version, ''),
      generated_at: generatedAt,
      duration_ms: num(s.duration_ms, 0),
      health,
      code: {
        files: num(code.files, 0),
        total_lines: num(code.total_lines, 0),
        code_lines: num(code.code_lines, 0),
        average_lines: num(code.average_lines, 0),
        max_file_lines: num(code.max_file_lines, 0),
        test_files: num(code.test_files, 0),
        source_files: num(code.source_files, 0),
        test_file_ratio: num(code.test_file_ratio, 0),
        test_line_ratio: num(code.test_line_ratio, 0),
        has_tests: bool(code.has_tests, false),
        languages: arr(code.languages ?? [], 'code.languages').map(toLanguage),
      },
      git: {
        is_repository: bool(git.is_repository, false),
        branch: str(git.branch, ''),
        head_commit: str(git.head_commit, ''),
        total_commits: num(git.total_commits, 0),
        window_commits: num(git.window_commits, 0),
        authors: num(git.authors, 0),
        bus_factor: num(git.bus_factor, 0),
        days_since_commit: num(git.days_since_commit, 0),
        note: typeof git.note === 'string' ? git.note : undefined,
      },
      dependencies: {
        detected: bool(deps.detected, false),
        total: num(deps.total, 0),
        direct: num(deps.direct, 0),
        dev: num(deps.dev, 0),
        indirect: num(deps.indirect, 0),
        transitive: num(deps.transitive, 0),
        locked: bool(deps.locked, false),
        drift: bool(deps.drift, false),
      },
      risk_count: risks.length,
      critical_risk_count: tally('critical'),
      high_risk_count: tally('high'),
      hotspot_count: hotspots.length,
    },
    risks: {
      root,
      generated_at: generatedAt,
      total: risks.length,
      critical: tally('critical'),
      high: tally('high'),
      medium: tally('medium'),
      low: tally('low'),
      risks,
    },
    hotspots: {
      root,
      generated_at: generatedAt,
      total: hotspots.length,
      confirmed: hotspots.filter((h2) => h2.confirmed).length,
      hotspots,
      churn,
    },
    // A snapshot carries no timeline: history lives in the SQLite database on
    // the machine that produced it, and `analyze --format json` does not embed
    // it. An empty series renders as "no history" rather than a fabricated
    // flat line.
    history: { root, count: 0, records: [], delta: null },
    meta: {
      root,
      generatedAt,
      version: str(s.version, ''),
      schema: str(s.schema_version, ''),
    },
  };
}

/** parseSnapshot validates then adapts, with the file name for context. */
export function parseSnapshot(text: string, filename = 'the file'): SnapshotOverview {
  let doc: unknown;
  try {
    doc = JSON.parse(text);
  } catch (err) {
    throw new SnapshotError(
      `${filename} is not valid JSON: ${err instanceof Error ? err.message : String(err)}`,
    );
  }
  const o = obj(doc, filename);
  if (!('health' in o) || !('code' in o)) {
    throw new SnapshotError(
      `${filename} is JSON but not a Lensyxe snapshot. Export one with:\n` +
        '  lensyxe analyze . --format json > lensyxe.json',
    );
  }
  return adapt(o);
}