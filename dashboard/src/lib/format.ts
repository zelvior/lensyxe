import type { Severity } from './types';

/**
 * Presentation helpers.
 *
 * Severity ordering and labels mirror `pkg/models.Severity` in Go. The two
 * implementations are checked against each other by
 * `internal/server/dashboard_test.go`, which renders a snapshot through the Go
 * constants and asserts the rendered severities line up.
 */

export const SEVERITY_ORDER: readonly Severity[] = [
  'critical',
  'high',
  'medium',
  'low',
  'info',
] as const;

export const SEVERITY_GLYPH: Record<Severity, string> = {
  critical: '🔴',
  high: '🟠',
  medium: '🟡',
  low: '🔵',
  info: '⚪',
};

export const SEVERITY_LABEL: Record<Severity, string> = {
  critical: 'Critical',
  high: 'High',
  medium: 'Medium',
  low: 'Low',
  info: 'Info',
};

/** severityIndex orders unknown severities last so nothing sorts to the top. */
export function severityIndex(severity: string): number {
  const i = SEVERITY_ORDER.indexOf(severity as Severity);
  return i === -1 ? SEVERITY_ORDER.length : i;
}

/** formatScore renders a 0..100 score without trailing zeros. */
export function formatScore(value: number): string {
  if (!Number.isFinite(value)) return '—';
  return Number.isInteger(value) ? String(value) : value.toFixed(1);
}

/** formatPct renders a 0..1 ratio as a percentage. */
export function formatPct(ratio: number): string {
  if (!Number.isFinite(ratio)) return '—';
  return `${(ratio * 100).toFixed(1)}%`;
}

/** signed renders a delta with an explicit sign, as the CLI does. */
export function signed(value: number): string {
  if (!Number.isFinite(value)) return '—';
  if (value > 0) return `+${formatScore(value)}`;
  if (value < 0) return `-${formatScore(Math.abs(value))}`;
  return '0';
}

/**
 * scoreColor maps a 0..100 score to the bar fill.
 *
 * The bands are the same ones `models.Grade` uses, so a green bar and an "A"
 * grade never disagree.
 */
export function scoreColor(score: number): string {
  if (!Number.isFinite(score)) return '#64748b';
  if (score >= 90) return '#16a34a';
  if (score >= 80) return '#65a30d';
  if (score >= 70) return '#ca8a04';
  if (score >= 60) return '#ea580c';
  return '#dc2626';
}