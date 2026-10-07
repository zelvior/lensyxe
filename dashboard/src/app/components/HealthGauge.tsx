import type { Metric } from 'lib/types';
import { formatScore, scoreColor } from 'lib/format';

/**
 * HealthGauge draws the overall score as an arc.
 *
 * An SVG arc rather than a canvas: it scales crisply, needs no layout
 * measurement, and is deterministic, so the same snapshot always renders the
 * same path. The gauge is a pure function of `score` with no animation, for
 * the same reason the CLI output is static.
 */
export function HealthGauge({ score, grade }: { score: number; grade: string }) {
  const clamped = Math.max(0, Math.min(100, Number.isFinite(score) ? score : 0));

  // A 240-degree sweep starting at the 7 o'clock position.
  const sweep = 240;
  const startAngle = 150;
  const circumference = 2 * Math.PI * 62;

  const arcPath = (pct: number) => {
    const angle = startAngle + (sweep * pct) / 100;
    const rad = (angle * Math.PI) / 180;
    const x = 80 + 62 * Math.cos(rad);
    const y = 80 + 62 * Math.sin(rad);
    return { x, y, large: angle - startAngle > 180 ? 1 : 0 };
  };

  const to = arcPath(clamped);
  const from = arcPath(0);
  const track = `M ${from.x} ${from.y} A 62 62 0 ${from.large} 1 ${to.x} ${to.y}`;

  return (
    <div className="flex items-center gap-6">
      <svg
        viewBox="0 0 160 130"
        className="h-32 w-40 shrink-0"
        role="img"
        aria-label={`Health score ${formatScore(clamped)} out of 100, grade ${grade}`}
      >
        <path
          d={track}
          fill="none"
          stroke="#1e293b"
          strokeWidth="14"
          strokeLinecap="round"
        />
        {clamped > 0 && (
          <path
            d={track}
            fill="none"
            stroke={scoreColor(clamped)}
            strokeWidth="14"
            strokeLinecap="round"
            strokeDasharray={`${(clamped / 100) * circumference * (sweep / 360)} ${circumference}`}
          />
        )}
        <text
          x="80"
          y="76"
          textAnchor="middle"
          className="fill-slate-100 text-[30px] font-semibold"
        >
          {formatScore(clamped)}
        </text>
        <text x="80" y="98" textAnchor="middle" className="fill-slate-500 text-[12px]">
          grade {grade}
        </text>
      </svg>
      <div className="min-w-0">
        <p className="text-sm text-slate-400">{scoreLabel(clamped)}</p>
      </div>
    </div>
  );
}

/** scoreLabel names the band, reusing the Grade thresholds. */
function scoreLabel(score: number): string {
  if (score >= 90) return 'Strong across the measured dimensions.';
  if (score >= 80) return 'Healthy, with room to improve.';
  if (score >= 70) return 'Acceptable; some dimensions need work.';
  if (score >= 60) return 'Below target on one or more dimensions.';
  return 'At risk. The weakest dimension needs attention first.';
}

/** MetricBars renders one proportional bar per scored component. */
export function MetricBars({ metrics }: { metrics: Metric[] }) {
  const applicable = metrics.filter((m) => m.applicable);

  if (applicable.length === 0) {
    return (
      <p className="text-sm text-slate-300">
        No dimensions were applicable to this repository.
      </p>
    );
  }

  return (
    <ul className="space-y-3">
      {applicable.map((m) => (
        <li key={m.key}>
          <div className="flex items-baseline justify-between gap-4">
            <span className="truncate text-sm text-slate-300">{m.label}</span>
            <span className="tabular shrink-0 text-sm text-slate-400">
              {formatScore(m.score)}
              <span className="ml-2 text-xs text-slate-400">
                {Math.round(m.weight * 100)}% weight
              </span>
            </span>
          </div>
          <div
            className="mt-1.5 h-1.5 w-full overflow-hidden rounded-full bg-slate-800"
            role="meter"
            aria-valuenow={Math.round(m.score)}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-label={m.label}
          >
            <div
              className="h-full rounded-full"
              style={{
                width: `${Math.max(0, Math.min(100, m.score))}%`,
                backgroundColor: scoreColor(m.score),
              }}
            />
          </div>
          {m.detail && (
            <p className="mt-1 truncate text-xs text-slate-400">{m.detail}</p>
          )}
        </li>
      ))}
    </ul>
  );
}

/**
 * StatTile is a single labelled figure.
 *
 * `note` carries the one-line reason a figure is what it is, so a number is
 * never presented without the context needed to judge it.
 */
export function StatTile({
  label,
  value,
  note,
}: {
  label: string;
  value: string;
  note?: string;
}) {
  return (
    <div className="card">
      <p className="text-xs uppercase tracking-wider text-slate-300">{label}</p>
      <p className="tabular mt-1.5 text-2xl font-semibold text-slate-100">{value}</p>
      {note && <p className="mt-1 text-xs text-slate-400">{note}</p>}
    </div>
  );
}