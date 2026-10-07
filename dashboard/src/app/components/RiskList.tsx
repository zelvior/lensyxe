import type { Risk, Severity } from 'lib/types';
import {
  SEVERITY_GLYPH,
  SEVERITY_LABEL,
  formatScore,
  severityIndex,
} from 'lib/format';

const SEVERITY_CLASS: Record<string, string> = {
  critical: 'border-red-500/40 bg-red-500/5',
  high: 'border-orange-500/40 bg-orange-500/5',
  medium: 'border-yellow-600/40 bg-yellow-600/5',
  low: 'border-blue-500/40 bg-blue-500/5',
  info: 'border-slate-700 bg-slate-800/30',
};

/** RiskList renders each risk with the evidence that justifies it. */
export function RiskList({ risks }: { risks: Risk[] }) {
  if (risks.length === 0) {
    return (
      <p className="text-sm text-slate-300">
        No risks detected.
      </p>
    );
  }

  return (
    <ul className="space-y-3">
      {risks.map((r) => (
        <li
          key={r.id}
          className={`rounded-lg border p-4 ${
            SEVERITY_CLASS[r.severity] ?? SEVERITY_CLASS.info
          }`}
        >
          <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
            <h3 className="text-sm font-medium text-slate-100">
              <span aria-hidden className="mr-1.5">
                {SEVERITY_GLYPH[r.severity] ?? '⚪'}
              </span>
              {SEVERITY_LABEL[r.severity] ?? r.severity}
              <span className="ml-2 font-normal text-slate-300">{r.title}</span>
            </h3>
            <span className="tabular text-xs text-slate-300">
              −{formatScore(r.impact)} pts
            </span>
          </div>

          {r.subject && (
            <p className="mt-1 font-mono text-xs text-slate-300">{r.subject}</p>
          )}

          <p className="mt-2 text-xs leading-relaxed text-slate-400">{r.detail}</p>

          {r.evidence.length > 0 && (
            <ul className="mt-2.5 space-y-1 border-l border-slate-700/60 pl-3">
              {r.evidence.map((e, i) => (
                <li key={`${r.id}-ev-${i}`} className="text-xs text-slate-300">
                  {e.label && (
                    <span className="font-medium text-slate-400">{e.label}: </span>
                  )}
                  {e.detail}
                </li>
              ))}
            </ul>
          )}

          {r.recommendation && (
            <p className="mt-2.5 text-xs text-emerald-400/80">
              → {r.recommendation}
            </p>
          )}
        </li>
      ))}
    </ul>
  );
}

/**
 * RiskSummary renders the severity counts as a compact strip.
 *
 * Sorting by severityIndex keeps the order stable for unknown severities,
 * which are placed last rather than sorted alphabetically into the middle of
 * the critical band.
 */
export function RiskSummary({
  critical,
  high,
  medium,
  low,
}: {
  critical: number;
  high: number;
  medium: number;
  low: number;
}) {
  const counts: Array<[Severity, number]> = (
    [
      ['critical', critical],
      ['high', high],
      ['medium', medium],
      ['low', low],
    ] as Array<[Severity, number]>
  ).sort((a, b) => severityIndex(a[0]) - severityIndex(b[0]));

  return (
    <div className="flex flex-wrap gap-3">
      {counts.map(([sev, n]) => (
        <span
          key={sev}
          className="inline-flex items-center gap-1.5 rounded bg-slate-800/60 px-2.5 py-1 text-xs"
        >
          <span aria-hidden>{SEVERITY_GLYPH[sev]}</span>
          <span className="tabular text-slate-200">{n}</span>
          <span className="text-slate-300">{SEVERITY_LABEL[sev]}</span>
        </span>
      ))}
    </div>
  );
}