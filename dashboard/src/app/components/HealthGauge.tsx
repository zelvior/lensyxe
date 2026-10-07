import type { Metric } from 'lib/types';
import { formatScore, scoreColor } from 'lib/format';

/**
 * HealthGauge draws the overall score as a ring.
 *
 * This replaces an arc built from two endpoint coordinates. That version took
 * its `large-arc-flag` from the *start* point's angle rather than the sweep, so
 * a 240-degree arc was asked to render as a sub-180 one and the renderer picked
 * whichever arc satisfied the flags -- correct only by luck of the geometry.
 *
 * A full ring has no such ambiguity: the drawn length is a fraction of the
 * circumference, which is the one piece of arithmetic here that cannot be
 * misread. It is also a larger target for the number, which is the point of the
 * component.
 *
 * No animation, deliberately. The CLI output is static and a score that grows
 * from zero measures nothing that the final frame does not.
 */
export function HealthGauge({
  score,
  grade,
  size = 'md',
}: {
  score: number;
  grade: string;
  size?: 'sm' | 'md';
}) {
  const clamped = Math.max(0, Math.min(100, Number.isFinite(score) ? score : 0));

  // Radius and centre are derived from one number so the ring, the stroke and
  // the type scale together. viewBox is square and the drawing stays inside it
  // with room for the stroke, which is what stops the ring being clipped.
  const r = 54;
  const centre = 70;
  const circumference = 2 * Math.PI * r;
  const stroke = size === 'sm' ? 10 : 13;

  return (
    <div className="relative shrink-0" style={{ width: centre * 2, height: centre * 2 }}>
      <svg
        viewBox={`0 0 ${centre * 2} ${centre * 2}`}
        className="h-full w-full"
        role="img"
        aria-label={`Health score ${formatScore(clamped)} out of 100, grade ${grade}`}
      >
        {/* Track. Full circle, so it reads as a dial with a defined scale. */}
        <circle
          cx={centre}
          cy={centre}
          r={r}
          fill="none"
          stroke="#1e293b"
          strokeWidth={stroke}
        />
        {clamped > 0 && (
          <circle
            cx={centre}
            cy={centre}
            r={r}
            fill="none"
            stroke={scoreColor(clamped)}
            strokeWidth={stroke}
            strokeLinecap="round"
            /* Rotated so the fill starts at twelve o'clock rather than at the
               SVG origin angle, which is three o'clock. */
            transform={`rotate(-90 ${centre} ${centre})`}
            strokeDasharray={`${(clamped / 100) * circumference} ${circumference}`}
          />
        )}
      </svg>
      {/* Positioned over the SVG rather than inside it: SVG text does not wrap
          and cannot be measured, and this has to hold "100" and "86.8" in the
          same box without the digits shifting as the value changes. */}
      <div className="absolute inset-0 flex flex-col items-center justify-center">
        <span
          className={`tabular font-semibold leading-none text-slate-50 ${
            size === 'sm' ? 'text-2xl' : 'text-4xl'
          }`}
        >
          {formatScore(clamped)}
        </span>
        <span className="mt-1 text-xs uppercase tracking-widest text-slate-400">
          {grade || '—'}
        </span>
      </div>
    </div>
  );
}

/** scoreLabel names the band, reusing the Grade thresholds. */
export function scoreLabel(score: number): string {
  if (score >= 90) return 'Strong across every measured dimension.';
  if (score >= 80) return 'Healthy, with room to improve.';
  if (score >= 70) return 'Acceptable; some dimensions need work.';
  if (score >= 60) return 'Below target on one or more dimensions.';
  return 'At risk. The weakest dimension needs attention first.';
}

/**
 * MetricBars renders one proportional bar per scored component.
 *
 * The bar is scaled to 0-100 rather than to the best component. A set of bars
 * normalised to its own maximum makes a 60 look identical to a 95, which is the
 * one thing a score chart must not do.
 */
export function MetricBars({
  metrics,
  compact = false,
}: {
  metrics: Metric[];
  compact?: boolean;
}) {
  const applicable = metrics.filter((m) => m.applicable);

  if (applicable.length === 0) {
    return (
      <p className="text-sm text-slate-300">
        No dimensions were applicable to this repository.
      </p>
    );
  }

  const weakest = applicable.reduce((min, m) => (m.score < min.score ? m : min));

  return (
    <ul className={compact ? 'space-y-2.5' : 'space-y-3.5'}>
      {applicable.map((m) => {
        const isWeakest = m.key === weakest.key && applicable.length > 1;
        return (
          <li key={m.key}>
            <div className="flex items-baseline justify-between gap-3">
              <span className="flex min-w-0 items-baseline gap-2">
                <span className="truncate text-sm text-slate-200">{m.label}</span>
                {isWeakest && (
                  <span className="hidden shrink-0 text-[0.65rem] uppercase tracking-wider text-amber-300/90 sm:inline">
                    weakest
                  </span>
                )}
              </span>
              <span className="tabular shrink-0 text-sm text-slate-300">
                {formatScore(m.score)}
                <span className="ml-2 text-xs text-slate-400">
                  {Math.round(m.weight * 100)}%
                </span>
              </span>
            </div>
            <div
              className="mt-1.5 h-1.5 w-full overflow-hidden rounded-full bg-slate-800"
              role="meter"
              aria-valuenow={Math.round(m.score)}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-label={`${m.label}: ${formatScore(m.score)} out of 100`}
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
              <p className="mt-1 text-xs leading-snug text-slate-400">{m.detail}</p>
            )}
          </li>
        );
      })}
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
    <div className="min-w-0">
      <p className="text-[0.65rem] uppercase tracking-wider text-slate-400">
        {label}
      </p>
      <p className="tabular mt-1 text-xl font-semibold text-slate-50 sm:text-2xl">
        {value}
      </p>
      {note && <p className="mt-0.5 text-xs leading-snug text-slate-400">{note}</p>}
    </div>
  );
}