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
  /*
   * Only a confirmed hotspot is badged.
   *
   * This used to render a grey "Candidate" pill on every unconfirmed row, which
   * on a typical repository is ten identical pills down a column saying the same
   * thing ten times. The absence of a badge is now the signal that a file is not
   * confirmed, and the one row that is confirmed is the one that draws the eye --
   * which is the entire point of the distinction.
   */
  if (!confirmed) return null;
  return (
    <span className="rounded-sm bg-red-500/15 px-1.5 py-0.5 text-[0.65rem] font-medium uppercase tracking-wide text-red-300">
      confirmed
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
  const [query, setQuery] = useState('');
  const [confirmedOnly, setConfirmedOnly] = useState(false);

  if (hotspots.length === 0) {
    return (
      <p className="text-sm text-slate-300">
        No hotspot candidates. No file reached the size threshold.
      </p>
    );
  }

  /*
   * Filtering is done here rather than in the parent so the counts in the
   * surrounding prose always describe what is on screen. A "10 candidates" note
   * above a filtered list of three is the kind of small lie that makes a table
   * untrustworthy.
   *
   * The match is a plain case-insensitive substring on the path, so `gap`,
   * `internal/gap`, and `GAPI` all find what you would expect. It is not a
   * glob: a filter that silently fails to match a pattern is worse than one
   * whose rules you can hold in your head.
   */
  const needle = query.trim().toLowerCase();
  const visible = hotspots.filter((h) => {
    if (confirmedOnly && !h.confirmed) return false;
    if (needle && !h.path.toLowerCase().includes(needle)) return false;
    return true;
  });

  const toggle = (path: string) =>
    setOpen((prev) => {
      const next = new Set(prev);
      if (!next.delete(path)) next.add(path);
      return next;
    });

  const largest = hotspots[0].lines > 0 ? hotspots[0].lines : 1;
  const confirmedCount = hotspots.filter((h) => h.confirmed).length;

  return (
    <div>
      {/* Controls sit above the table rather than inside the card header, so
          they travel with the thing they filter when the page is read on a
          narrow screen. */}
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <label className="sr-only" htmlFor="hotspot-filter">
          Filter hotspots by path
        </label>
        <input
          id="hotspot-filter"
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Filter by path"
          spellCheck={false}
          autoComplete="off"
          className="min-w-0 flex-1 rounded border border-slate-700 bg-slate-900/60 px-2.5 py-1.5 text-sm text-slate-200 placeholder:text-slate-400 focus:border-sky-500 focus:outline-none"
        />
        <button
          type="button"
          onClick={() => setConfirmedOnly((v) => !v)}
          aria-pressed={confirmedOnly}
          disabled={confirmedCount === 0}
          className={`shrink-0 rounded border px-2.5 py-1.5 text-xs transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-400 disabled:cursor-not-allowed disabled:opacity-40 ${
            confirmedOnly
              ? 'border-red-500/60 bg-red-500/15 text-red-200'
              : 'border-slate-700 text-slate-300 hover:border-slate-500'
          }`}
        >
          Confirmed only ({confirmedCount})
        </button>
        <span className="tabular shrink-0 text-xs text-slate-400">
          {visible.length} of {hotspots.length}
        </span>
      </div>

      {visible.length === 0 ? (
        <p className="py-6 text-sm text-slate-300">
          No hotspot matches {query ? <code className="font-mono text-slate-200">{query}</code> : 'that filter'}.
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full w-full text-left">
            <thead>
              <tr className="border-b border-slate-800">
                <th scope="col" className="w-6 py-2 pr-2">
                  <span className="sr-only">Expand</span>
                </th>
                <th scope="col" className="th">File</th>
                <th scope="col" className="th-num">Lines</th>
                <th scope="col" className="th-num hidden sm:table-cell">Churn</th>
                <th scope="col" className="th-num hidden md:table-cell">Cx</th>
                <th
                  scope="col"
                  className={confirmedCount > 0 ? 'th' : 'th hidden'}
                >
                  Severity
                </th>
              </tr>
            </thead>
            <tbody>
              {visible.map((h) => {
            const expanded = open.has(h.path);
            return (
              <Fragment key={h.path}>
                <tr
                  className="border-b border-slate-800/50 align-top hover:bg-slate-800/20"
                >
                  <td className="w-6 py-2.5 pr-2">
                    {/* A real button rather than a click handler on the row:
                        a row-level onClick is unreachable by keyboard, and an
                        expander that only responds to a mouse is not a control
                        at all. */}
                    <button
                      type="button"
                      onClick={() => toggle(h.path)}
                      aria-expanded={expanded}
                      className="-ml-1 flex h-6 w-6 items-center justify-center rounded text-slate-400 hover:bg-slate-700/60 hover:text-slate-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-sky-400"
                    >
                      <span aria-hidden="true">{expanded ? '▾' : '▸'}</span>
                      <span className="sr-only">
                        {expanded ? 'Hide detail for' : 'Show detail for'} {h.path}
                      </span>
                    </button>
                  </td>
                  <th
                    scope="row"
                    className="max-w-[9rem] py-2.5 pr-4 text-left font-normal sm:max-w-xs lg:max-w-none"
                  >
                    <span className="ident block truncate">
                      {h.path}
                    </span>
                  </th>
                  <td className="td-num">
                    {h.lines}
                  </td>
                  <td className="td-num">
                    {h.churn}
                  </td>
                  <td className="td-num">
                    {formatScore(h.complexity)}
                  </td>
                  {/* Hides with its header. An empty SEVERITY column is dead width, and the
                      colSpan on the detail row below follows it so the layout
                      stays consistent either way. */}
                  <td className={confirmedCount > 0 ? 'py-2.5 text-[0.8125rem]' : 'hidden'}>
                    <Tag confirmed={h.confirmed} />
                  </td>
                </tr>
                {expanded && (
                  <tr className="border-b border-slate-800/50 bg-slate-900/40">
                    <td />
                    <td colSpan={confirmedCount > 0 ? 5 : 4} className="py-4 pr-4">
                      <div className="grid gap-4 sm:grid-cols-2">
                        <div>
                          <p className="mb-2 text-xs uppercase tracking-wider text-slate-300">
                            Thresholds crossed
                          </p>
                          <FactorChips
                            classification={h.classification}
                            confirmed={h.confirmed}
                          />
                          {h.rationale && (
                            <p className="mt-2 text-xs leading-relaxed text-slate-300">
                              {h.rationale}
                            </p>
                          )}
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
      )}
    </div>
  );
}