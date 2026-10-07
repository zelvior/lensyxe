import type { HistoryRecord } from 'lib/types';
import { formatScore, scoreColor } from 'lib/format';

/**
 * HealthTimeline plots recorded health scores over time as an SVG line chart.
 *
 * Each x position is one discrete measurement, not an interpolation: the chart
 * connects the recorded points with straight segments and says so, because a
 * smooth curve would imply measurements that were never taken. This matches
 * how `lensyxe history` renders the same data.
 */
export function HealthTimeline({ records }: { records: HistoryRecord[] }) {
  // The API returns oldest-first so the chart can plot it without reversing.
  const points = records.filter((r) => Number.isFinite(r.score));

  if (points.length === 0) {
    return (
      <div className="text-[0.8125rem] leading-relaxed text-slate-400">
        <p className="text-slate-300">No trend yet.</p>
        <p className="mt-1.5">
          Health over time is read from a SQLite database beside the repository,
          not from the analysis itself, so it accumulates one point per run.{' '}
          <code className="font-mono text-slate-300">lensyxe analyze</code>{' '}
          records one.
        </p>
        <p className="mt-1.5">
          A snapshot file carries no history, so this panel stays empty on a
          hosted page however many times you load one.
        </p>
      </div>
    );
  }

  if (points.length === 1) {
    return (
      <div className="text-sm text-slate-400">
        <p className="tabular text-2xl font-semibold text-slate-100">
          {formatScore(points[0].score)}
        </p>
        <p className="mt-1 text-slate-300">
          First snapshot. A trend needs at least two runs.
        </p>
      </div>
    );
  }

  const width = 720;
  const height = 180;
  const padX = 8;
  const padY = 12;

  // The y axis is pinned to 0..100. An auto-fitted axis would magnify a
  // two-point wobble into what looks like a collapse, which is the single most
  // misleading thing a health chart can do.
  const x = (i: number) =>
    padX + (i * (width - padX * 2)) / (points.length - 1);
  const y = (score: number) =>
    padY + (1 - score / 100) * (height - padY * 2);

  const path = points
    .map((p, i) => `${i === 0 ? 'M' : 'L'} ${x(i).toFixed(2)} ${y(p.score).toFixed(2)}`)
    .join(' ');

  const last = points[points.length - 1];
  const first = points[0];
  const change = last.score - first.score;

  const gridValues = [0, 25, 50, 75, 100];

  return (
    <div>
      <svg
        viewBox={`0 0 ${width} ${height}`}
        className="w-full"
        role="img"
        aria-label={`Health score over ${points.length} runs, from ${formatScore(first.score)} to ${formatScore(last.score)}`}
      >
        {gridValues.map((v) => (
          <g key={v}>
            <line
              x1={padX}
              x2={width - padX}
              y1={y(v)}
              y2={y(v)}
              stroke="#1e293b"
              strokeWidth="1"
            />
            <text
              x={padX + 2}
              y={y(v) - 3}
              className="fill-slate-600"
              fontSize="9"
            >
              {v}
            </text>
          </g>
        ))}

        <path d={path} fill="none" stroke={scoreColor(last.score)} strokeWidth="2" />

        {points.map((p, i) => (
          <circle
            key={p.id}
            cx={x(i)}
            cy={y(p.score)}
            r="2.5"
            fill={scoreColor(p.score)}
          >
            <title>
              {new Date(p.recorded_at).toISOString().slice(0, 16).replace('T', ' ')} —{' '}
              {formatScore(p.score)} ({p.grade}), {p.risk_count} risks
            </title>
          </circle>
        ))}
      </svg>
      <div className="mt-2 flex justify-between text-xs text-slate-400">
        <span>
          {points.length} run{points.length === 1 ? '' : 's'}
        </span>
        <span className="tabular">
          {formatScore(first.score)} → {formatScore(last.score)}
          {Math.abs(change) >= 0.5 && (
            <span className={change < 0 ? 'text-red-400' : 'text-emerald-400'}>
              {' '}
              ({change < 0 ? '↓' : '↑'}
              {formatScore(Math.abs(change))})
            </span>
          )}
        </span>
      </div>
    </div>
  );
}