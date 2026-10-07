'use client';

import { Fragment, useState } from 'react';
import type { Hotspot } from 'lib/types';
import { formatScore } from 'lib/format';

/**
 * severityTag styles a confirmed hotspot.
 *
 * A confirmed hotspot met size, churn, and complexity together, which is the
 * only case worth a badge. An unconfirmed candidate is deliberately not badged
 * "high" on size alone: a long declarative file changes rarely and breaks
 * little, and the badge is where that nuance would get lost.
 */
function Tag({ confirmed }: { confirmed: boolean }) {
  if (confirmed) {
    return (
      <span className="rounded bg-red-500/15 px-2 py-0.5 text-xs font-medium text-red-300">
        Confirmed
      </span>
    );
  }
  return (
    <span className="rounded bg-slate-700/40 px-2 py-0.5 text-xs font-medium text-slate-400">
      Candidate
    </span>
  );
}

/**
 * classificationBreakdown turns the analyzer's classification string into
 * individually marked factors.
 *
 * The field is a comma-separated list such as "size,churn,complexity,confirmed".
 * Splitting it here rather than inventing four booleans on the wire keeps the
 * JSON contract matching `models.Hotspot` exactly; the analyzer stays the single
 * place that decides which factors apply.
 *
 * A factor the analyzer does not emit is not shown. Guessing at one, or showing
 * it as "not met" when the analyzer simply never measured it, would turn a
 * detail view into a source of claims the engine did not make.
 */
const FACTORS: ReadonlyArray<{ key: string; label: string }> = [
  { key: 'size', label: 'Size' },
  { key: 'churn', label: 'Churn' },
  { key: 'complexity', label: 'Complexity' },
];

function FactorChips({ classification, confirmed }: { classification: string; confirmed: boolean }) {
  const met = new Set(classification.split(',').map((s) => s.trim()));
  return (
    <span className="flex flex-wrap gap-1.5">
      {FACTORS.map((f) => {
        const on = met.has(f.key);
        return (
          <span
            key={f.key}
            className={
              on
                ? 'rounded bg-slate-600/50 px-2 py-0.5 text-xs text-slate-200'
                : 'rounded border border-slate-800 px-2 py-0.5 text-xs text-slate-400'
          }
          title={on ? `${f.label} threshold met` : `${f.label} threshold not met`}
          >
            {f.label}
          </span>
        );
      })}
      {confirmed && (
        <span className="rounded bg-red-500/15 px-2 py-0.5 text-xs text-red-300">
          All three together
        </span>
      )}
    </span>
  );
}

/**
 * HotspotTable lists files ranked by how risky they are to change.
 *
 * Each row expands. The detail is the reason the row exists: the table already
 * showed lines, churn, and complexity as bare numbers, and a number you cannot
 * interpret is not a finding. Expanding shows which thresholds were crossed,
 * what the analyzer's own rationale was, and what a file of this size would
 * normally look like.
 */
export function HotspotTable({ hotspots }: { hotspots: Hotspot[] }) {
  const [open, setOpen] = useState<Set<string>>(() => new Set());

  if (hotspots.length === 0) {
    return (
      <p className="text-sm text-slate-300">
        No hotspot candidates. No file reached the size threshold.
      </p>
    );
  }

  const toggle = (path: string) =>
    setOpen((prev) => {
      const next = new Set(prev);
      if (!next.delete(path)) next.add(path);
      return next;
    });

  const largest = hotspots[0].lines > 0 ? hotspots[0].lines : 1;

  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-sm">
        <thead>
          <tr className="border-b border-slate-800 text-xs uppercase tracking-wider text-slate-300">
            <th scope="col" className="py-2 pr-4 font-medium">
              <span className="sr-only">Expand</span>
            </th>
            <th scope="col" className="py-2 pr-4 font-medium">File</th>
            <th scope="col" className="py-2 pr-4 text-right font-medium">Lines</th>
            <th scope="col" className="py-2 pr-4 text-right font-medium">Churn</th>
            <th scope="col" className="py-2 pr-4 text-right font-medium">Cx</th>
            <th scope="col" className="py-2 font-medium">Severity</th>
          </tr>
        </thead>
        <tbody>
          {hotspots.map((h) => {
            const expanded = open.has(h.path);
            return (
              <Fragment key={h.path}>
                <tr
                  className="cursor-pointer border-b border-slate-800/50 align-top hover:bg-slate-800/20"
                  onClick={() => toggle(h.path)}
                >
                  <td className="w-6 py-2.5 pr-2 text-slate-400">
                    <span aria-hidden="true">{expanded ? '▾' : '▸'}</span>
                  </td>
                  <th
                    scope="row"
                    className="py-2.5 pr-4 text-left font-normal"
                  >
                    <span className="block truncate font-mono text-xs text-slate-200">
                      {h.path}
                    </span>
                    <span className="mt-0.5 block text-xs text-slate-400">{h.rationale}</span>
                  </th>
                  <td className="tabular py-2.5 pr-4 text-right text-slate-400">
                    {h.lines}
                  </td>
                  <td className="tabular py-2.5 pr-4 text-right text-slate-400">
                    {h.churn}
                  </td>
                  <td className="tabular py-2.5 pr-4 text-right text-slate-400">
                    {formatScore(h.complexity)}
                  </td>
                  <td className="py-2.5">
                    <Tag confirmed={h.confirmed} />
                  </td>
                </tr>
                {expanded && (
                  <tr className="border-b border-slate-800/50 bg-slate-900/40">
                    <td />
                    <td colSpan={5} className="py-4 pr-4">
                      <div className="grid gap-4 sm:grid-cols-2">
                        <div>
                          <p className="mb-2 text-xs uppercase tracking-wider text-slate-300">
                            Thresholds crossed
                          </p>
                          <FactorChips
                            classification={h.classification}
                            confirmed={h.confirmed}
                          />
                        </div>
                        <div>
                          <p className="mb-2 text-xs uppercase tracking-wider text-slate-300">
                            Size against the largest candidate
                          </p>
                          <span className="block h-1.5 rounded bg-slate-700">
                            <span
                              className="block h-1.5 rounded bg-slate-400"
                              style={{
                                width: `${Math.max((h.lines / largest) * 100, 1)}%`,
                              }}
                            />
                          </span>
                          <p className="mt-1.5 text-xs text-slate-300">
                            {h.lines.toLocaleString('en-US')} code lines,{' '}
                            {Math.round((h.lines / largest) * 100)}% of the largest
                            candidate{' '}
                            {hotspots[0].lines.toLocaleString('en-US')}.
                            {h.language ? ` Detected as ${h.language}.` : ''}
                          </p>
                        </div>
                      </div>
                      {h.confirmed ? (
                        <p className="mt-3 text-xs text-slate-400">
                          Size, churn, and complexity all crossed their thresholds
                          at once, which is what makes this one a confirmed hotspot
                          rather than a candidate. Size alone would not be enough:
                          a large file that nobody changes is not a risk, and the
                          analyzer will not report it as one.
                        </p>
                      ) : (
                        <p className="mt-3 text-xs text-slate-300">
                          A candidate, not a confirmed hotspot. It has not met the
                          churn and complexity thresholds alongside its size, so
                          changing it is not known to be dangerous. It is listed
                          because the size is already there and may become a
                          problem.
                        </p>
                      )}
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}