/**
 * The subset of the Lensyxe snapshot contract this extension consumes.
 *
 * Every field here is taken from `lensyxe analyze --format json` and was read
 * off real output rather than guessed. Two things are deliberate:
 *
 * - Optional fields are genuinely optional in Go (`omitempty`). A snapshot from a
 *   directory that is not a git repository has no churn, and a risk that is not
 *   about a specific file has no subject. Modelling those as required would push
 *   `undefined` handling into every consumer.
 *
 * - There is no `testing` or `build` dimension, because Lensyxe does not score
 *   either. Code coverage and build duration are listed under "what is not
 *   measured" in docs/SCORING_SPEC.md, and adding a placeholder field for them
 *   here would invite the sidebar to invent a score the CLI never produced.
 */

/** Severity of a risk, ordered most severe first. */
export type Severity = 'critical' | 'high' | 'medium' | 'low';

/** Lexical complexity band for a file. */
export type ComplexityLevel = 'low' | 'moderate' | 'high' | 'very_high';

/** One scored dimension of the Engineering Health Score. */
export interface Metric {
  key: string;
  label: string;
  score: number;
  weight: number;
  /**
   * False when the dimension could not be measured. Its weight is redistributed
   * across the dimensions that could be, rather than counting as a zero, which
   * is why an unmeasured metric must not be displayed as a score.
   */
  applicable: boolean;
  summary?: string;
}

export interface Evidence {
  kind: string;
  label: string;
  detail: string;
  value?: number;
}

export interface Risk {
  id: string;
  severity: Severity;
  category: string;
  title: string;
  detail: string;
  /** Repository-relative file path, present only for file-scoped risks. */
  subject?: string;
  impact: number;
  evidence: Evidence[];
  recommendation?: string;
}

export interface Hotspot {
  /** Repository-relative path. */
  path: string;
  lines: number;
  language: string;
  churn: number;
  complexity: number;
  level?: ComplexityLevel;
  /** Sorted comma-separated factors: size, churn, complexity, confirmed. */
  classification: string;
  confirmed: boolean;
  rationale: string;
}

export interface FileComplexity {
  path: string;
  language: string;
  lines: number;
  functions: number;
  branch_points: number;
  max_nesting: number;
  estimated_complexity: number;
  max_function_complexity: number;
  level?: ComplexityLevel;
  density: number;
}

export interface ComplexitySummary {
  measured: boolean;
  files: number;
  functions: number;
  branch_points: number;
  max_nesting: number;
  average_complexity: number;
  max_complexity: number;
  max_complexity_file: string;
  very_high_functions: number;
  high_functions: number;
  worst_files: FileComplexity[];
}

export interface CodeStats {
  files: number;
  source_files: number;
  test_files: number;
  has_tests: boolean;
  test_file_ratio: number;
  test_line_ratio: number;
  total_lines: number;
  code_lines: number;
  max_file_lines: number;
  average_lines: number;
  complexity: ComplexitySummary;
  hotspots: Hotspot[];
}

export interface DependencyStats {
  detected: boolean;
  ecosystems: Array<{ name: string; manifest: string; lockfile?: string }>;
  total: number;
  direct: number;
  dev: number;
  transitive: number;
  /** False when a manifest has no lockfile, which is the drift signal. */
  locked: boolean;
  drift: boolean;
  note?: string;
}

export interface GitStats {
  is_repository: boolean;
  commits?: number;
  authors?: number;
}

export interface Health {
  score: number;
  grade: string;
  summary: string;
  components?: number;
  metrics: Metric[];
}

export interface Snapshot {
  schema_version: string;
  tool: string;
  version: string;
  root: string;
  generated_at: string;
  duration_ms: number;
  code: CodeStats;
  git: GitStats;
  dependencies: DependencyStats;
  health: Health;
  risks: Risk[];
  findings?: Array<{
    severity: string;
    title: string;
    subject?: string;
    detail: string;
  }>;
}

/**
 * Severity rank for ordering. Higher is worse.
 *
 * An unknown severity sorts last rather than first, so a new severity added by
 * the CLI cannot make a file jump to the top of the problems list.
 */
export function severityRank(s: string): number {
  switch (s) {
    case 'critical':
      return 4;
    case 'high':
      return 3;
    case 'medium':
      return 2;
    case 'low':
      return 1;
    default:
      return 0;
  }
}

/** Icon id for a severity, using VS Code's built-in codicons. */
export function severityIcon(s: string): string {
  switch (s) {
    case 'critical':
      return 'error';
    case 'high':
      return 'warning';
    case 'medium':
      return 'warning';
    default:
      return 'info';
  }
}

/**
 * Colour for a 0-100 score, matching the CLI's terminal rendering.
 *
 * The bands are the CLI's own grade boundaries rather than invented ones, so the
 * sidebar and `lensyxe analyze` never disagree about what counts as healthy.
 */
export function scoreTone(score: number): 'good' | 'ok' | 'warn' | 'bad' {
  if (score >= 85) return 'good';
  if (score >= 70) return 'ok';
  if (score >= 55) return 'warn';
  if (score >= 40) return 'warn';
  return 'bad';
}

/** Codicon id for a complexity band. */
export function complexityIcon(level: string | undefined): string {
  switch (level) {
    case 'very_high':
      return 'error';
    case 'high':
      return 'warning';
    case 'moderate':
      return 'info';
    default:
      return 'symbol-file';
  }
}