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

/** HotspotTable lists files ranked by how risky they are to change. */
export function HotspotTable({ hotspots }: { hotspots: Hotspot[] }) {
  if (hotspots.length === 0) {
    return (
      <p className="text-sm text-slate-500">
        No hotspot candidates. No file reached the size threshold.
      </p>
    );
  }

  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-sm">
        <thead>
          <tr className="border-b border-slate-800 text-xs uppercase tracking-wider text-slate-500">
            <th className="py-2 pr-4 font-medium">File</th>
            <th className="py-2 pr-4 text-right font-medium">Lines</th>
            <th className="py-2 pr-4 text-right font-medium">Churn</th>
            <th className="py-2 pr-4 text-right font-medium">Cx</th>
            <th className="py-2 font-medium">Severity</th>
          </tr>
        </thead>
        <tbody>
          {hotspots.map((h) => (
            <tr key={h.path} className="border-b border-slate-800/50 align-top">
              <td className="py-2.5 pr-4">
                <span className="block truncate font-mono text-xs text-slate-200">
                  {h.path}
                </span>
                <span className="mt-0.5 block text-xs text-slate-600">{h.rationale}</span>
              </td>
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
          ))}
        </tbody>
      </table>
    </div>
  );
}