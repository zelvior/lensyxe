'use client';

import { useState } from 'react';
import type { HealthResponse, HistoryRecord } from 'lib/types';
import { formatPct, formatScore, signed } from 'lib/format';

/**
 * One comparable figure.
 *
 * `applicable` on both sides is what decides whether a delta can exist at all.
 * A dimension that was not measured in one of the two runs has no baseline, and
 * subtracting from a number that was never taken is not a small difference.
 */
interface Row {
  label: string;
  now: number | null;
  then: number | null;
  /** Lower is better for risk and hotspot counts. */
  lowerIsBetter?: boolean;
  /** Rendered as a percentage rather than a score. */
  ratio?: boolean;
  applicable: boolean;
}

/**
 * deltaClass colours a movement.
 *
 * `lowerIsBetter` is per row rather than derived from the sign, because more
 * tests and fewer risks are both good news and a shared rule would colour one of
 * them red.
 */
function deltaClass(row: Row): string {
  if (row.now === null || row.then === null) return 'text-slate-500';
  const delta = row.now - row.then;
  if (delta === 0) return 'text-slate-500';
  const good = row.lowerIsBetter ? delta < 0 : delta > 0;
  return good ? 'text-emerald-400' : 'text-red-400';
}

function buildRows(
  now: HealthResponse,
  then: HistoryRecord,
  confirmedHotspots: number,
): Row[] {
  const rows: Row[] = [
    {
      label: 'Overall score',
      now: now.health.score,
      then: then.score,
      applicable: true,
    },
    {
      label: 'Code health',
      now: metricScore(now, 'code'),
      then: then.code_applicable ? then.code_score : null,
      applicable: now.health.metrics.find((m) => m.key === 'code')?.applicable ?? false,
    },
    {
      label: 'Dependency health',
      now: metricScore(now, 'dependency'),
      then: then.dependency_applicable ? then.dependency_score : null,
      applicable:
        now.health.metrics.find((m) => m.key === 'dependency')?.applicable ?? false,
    },
    {
      label: 'Maintainability (Git)',
      now: metricScore(now, 'git'),
      then: then.git_applicable ? then.git_score : null,
      applicable: now.health.metrics.find((m) => m.key === 'git')?.applicable ?? false,
    },
    {
      label: 'Risks',
      now: now.risk_count,
      then: then.risk_count,
      lowerIsBetter: true,
      applicable: true,
    },
    {
      label: 'Critical risks',
      now: now.critical_risk_count,
      then: then.critical_risk_count,
      lowerIsBetter: true,
      applicable: true,
    },
    {
      label: 'Hotspot candidates',
      now: now.hotspot_count,
      then: then.hotspot_count,
      lowerIsBetter: true,
      applicable: true,
    },
    {
      label: 'Confirmed hotspots',
      // Counted by the /hotspots endpoint rather than /health, which reports
      // only the candidate total, so it arrives as a prop rather than being
      // read off the health response.
      now: confirmedHotspots,
      then: then.confirmed_hotspots,
      lowerIsBetter: true,
      applicable: true,
    },
    {
      label: 'Test file ratio',
      now: now.code.test_file_ratio,
      then: then.test_file_ratio,
      ratio: true,
      applicable: true,
    },
    {
      label: 'Code lines',
      now: now.code.code_lines,
      then: then.code_lines,
      applicable: true,
    },
  ];
  return rows;
}

function metricScore(now: HealthResponse, key: string): number | null {
  const m = now.health.metrics.find((x) => x.key === key);
  return m && m.applicable ? m.score : null;
}

/**
 * CompareView puts the current snapshot next to a run you pick from the
 * recorded history.
 *
 * This compares against a recorded Lensyxe run of the same working tree, which
 * is a different thing from `lensyxe compare`, and the difference matters. The
 * CLI compares two revisions extracted with `git archive`; this compares the
 * live snapshot against what the tool previously measured in this directory.
 * The two disagree whenever the working tree has uncommitted changes, and it
 * says so rather than presenting a delta as if it described a commit.
 *
 * A row whose dimension was not measured on one side shows "not comparable"
 * instead of a number. That case is real rather than theoretical: a repository
 * that only recently became a git repository has no git score in the older run.
 */
export function CompareView({
  current,
  records,
  confirmedHotspots,
}: {
  current: HealthResponse;
  records: HistoryRecord[];
  confirmedHotspots: number;
}) {
  // Default to the most recent prior run, which is the comparison people
  // almost always want, and which the timeline already shows as "the last one".
  const [selected, setSelected] = useState<number>(
    () => (records.length > 0 ? records[records.length - 1].id : -1),
  );

  if (records.length === 0) {
    return (
      <p className="text-sm text-slate-500">
        Nothing to compare against yet. Each run of{' '}
        <code className="text-slate-400">lensyxe analyze</code> is recorded locally,
        so the second one gives this something to compare.
      </p>
    );
  }

  const baseline = records.find((r) => r.id === selected);
  if (!baseline) {
    return (
      <p className="text-sm text-slate-500">
        That recorded run is no longer available. Reload the data.
      </p>
    );
  }

  const rows = buildRows(current, baseline, confirmedHotspots);
  const comparable = rows.filter((r) => r.now !== null && r.then !== null).length;

  return (
    <div>
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <label htmlFor="compare-baseline" className="text-xs uppercase tracking-wider text-slate-500">
          Against
        </label>
        <select
          id="compare-baseline"
          value={String(selected)}
          onChange={(e) => setSelected(Number(e.target.value))}
          className="rounded border border-slate-700 bg-slate-900 px-2 py-1 text-xs text-slate-300"
        >
          {[...records].reverse().map((r) => (
            <option key={r.id} value={r.id}>
              {formatTimestamp(r.recorded_at)} · {r.commit.slice(0, 7) || 'no commit'}
            </option>
          ))}
        </select>
      </div>

      <div className="overflow-x-auto">
        <table className="w-full text-left text-sm">
          <thead>
            <tr className="border-b border-slate-800 text-xs uppercase tracking-wider text-slate-500">
              <th className="py-2 pr-4 font-medium">Figure</th>
              <th className="py-2 pr-4 text-right font-medium">Then</th>
              <th className="py-2 pr-4 text-right font-medium">Now</th>
              <th className="py-2 font-medium">Change</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => {
              const canCompare = r.now !== null && r.then !== null;
              return (
                <tr key={r.label} className="border-b border-slate-800/50">
                  <td className="py-2.5 pr-4 text-slate-300">{r.label}</td>
                  <td className="tabular py-2.5 pr-4 text-right text-slate-500">
                    {canCompare ? render(r.then!, r.ratio) : <span className="text-slate-600">—</span>}
                  </td>
                  <td className="tabular py-2.5 pr-4 text-right text-slate-300">
                    {canCompare ? render(r.now!, r.ratio) : <span className="text-slate-600">not measured</span>}
                  </td>
                  <td className={`tabular py-2.5 ${deltaClass(r)}`}>
                    {canCompare ? signed(r.now! - r.then!) : 'not comparable'}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      <p className="mt-3 text-xs text-slate-600">
        {comparable} of {rows.length} figures are comparable. The rest were not
        measured on both sides, and a difference from a number that was never
        taken is not reported as one. This compares the live working tree against
        a recorded run of it; it is not a diff between two commits, which is what{' '}
        <code className="text-slate-400">lensyxe diff</code> does.
      </p>
    </div>
  );
}

function render(value: number, ratio: boolean | undefined): string {
  return ratio ? formatPct(value) : value.toLocaleString('en-US');
}

/**
 * formatTimestamp renders an RFC3339 instant in the viewer's locale.
 *
 * A recorded_at that does not parse shows the raw string rather than "Invalid
 * Date": a wrong-looking timestamp is a fact about the data, and hiding it
 * behind a placeholder would make a broken record look like a missing one.
 */
function formatTimestamp(raw: string): string {
  const t = new Date(raw);
  if (Number.isNaN(t.getTime())) return raw;
  return t.toLocaleString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}