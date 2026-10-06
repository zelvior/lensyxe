/**
 * Types mirroring the Go JSON contract exposed under /api/v1.
 *
 * These are hand-written rather than generated so the dashboard fails at build
 * time when the Go contract drifts, instead of rendering `undefined` at
 * runtime. Field names match the `json:"..."` tags in pkg/models exactly.
 */

export type Severity = 'critical' | 'high' | 'medium' | 'low' | 'info';

export interface Metric {
  key: string;
  label: string;
  value: number;
  display: string;
  score: number;
  weight: number;
  detail: string;
  applicable: boolean;
}

export interface Health {
  score: number;
  grade: string;
  summary: string;
  metrics: Metric[];
  components: number;
}

export interface Evidence {
  kind: string;
  label: string;
  detail: string;
  subject?: string;
  value?: number;
}

export interface Risk {
  id: string;
  severity: Severity;
  category: string;
  title: string;
  detail: string;
  subject?: string;
  impact: number;
  evidence: Evidence[];
  recommendation?: string;
}

export interface Hotspot {
  path: string;
  lines: number;
  language: string;
  churn: number;
  complexity: number;
  level?: string;
  classification: string;
  confirmed: boolean;
  rationale: string;
}

export interface ChurnEntry {
  path: string;
  commits: number;
  added: number;
  deleted: number;
  score: number;
}

/** One row of GET /api/v1/history. */
export interface HistoryRecord {
  id: number;
  recorded_at: string;
  root: string;
  commit: string;
  version: string;
  score: number;
  grade: string;
  code_score: number;
  dependency_score: number;
  git_score: number;
  code_applicable: boolean;
  dependency_applicable: boolean;
  git_applicable: boolean;
  risk_count: number;
  critical_risk_count: number;
  high_risk_count: number;
  hotspot_count: number;
  confirmed_hotspots: number;
  files: number;
  source_files: number;
  test_files: number;
  test_file_ratio: number;
  code_lines: number;
  avg_complexity: number;
  max_complexity: number;
  dependency_drift: boolean;
}

/** GET /api/v1/health */
export interface HealthResponse {
  root: string;
  version: string;
  generated_at: string;
  duration_ms: number;
  health: Health;
  code: {
    files: number;
    total_lines: number;
    code_lines: number;
    average_lines: number;
    max_file_lines: number;
    test_files: number;
    source_files: number;
    test_file_ratio: number;
    test_line_ratio: number;
    has_tests: boolean;
  };
  git: {
    is_repository: boolean;
    branch: string;
    head_commit: string;
    total_commits: number;
    window_commits: number;
    authors: number;
    bus_factor: number;
    days_since_commit: number;
    note?: string;
  };
  dependencies: {
    detected: boolean;
    total: number;
    direct: number;
    dev: number;
    indirect: number;
    transitive: number;
    locked: boolean;
    drift: boolean;
  };
  risk_count: number;
  critical_risk_count: number;
  high_risk_count: number;
  hotspot_count: number;
}

/** GET /api/v1/risks */
export interface RisksResponse {
  root: string;
  generated_at: string;
  total: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  risks: Risk[];
}

/** GET /api/v1/hotspots */
export interface HotspotsResponse {
  root: string;
  generated_at: string;
  total: number;
  confirmed: number;
  hotspots: Hotspot[];
  churn: ChurnEntry[];
}

/** GET /api/v1/history */
export interface HistoryResponse {
  root: string;
  count: number;
  /** Oldest first, so a chart can plot it directly without reversing. */
  records: HistoryRecord[];
  delta: { score: number; complexity: number; risks: number } | null;
}