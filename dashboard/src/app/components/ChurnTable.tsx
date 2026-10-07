import type { ChurnEntry, Hotspot } from 'lib/types';

/**
 * ChurnTable lists the files absorbing the most movement in the git window.
 *
 * This data was already being fetched by the dashboard and then dropped on the
 * floor: the hotspots endpoint has always returned a churn array alongside the
 * hotspots, and nothing rendered it. Showing it costs nothing and it is the
 * other half of what a hotspot is -- a hotspot is a file that is large *and*
 * moving, and this is the moving half.
 *
 * The `why` column joins a churn row to a hotspot of the same path, so a reader
 * can see at a glance which of the churning files are also large. It is derived
 * by exact path match, which is the same identity the analyzer uses to join
 * churn to hotspots. A path present in one list and not the other is reported
 * as such rather than silently omitted.
 */
export function ChurnTable({ churn, hotspots }: { churn: ChurnEntry[]; hotspots: Hotspot[] }) {
  if (churn.length === 0) {
    return (
      <p className="text-sm text-slate-300">
        No churn was recorded in the window. Either the directory is not a git
        repository, or nothing changed inside it.
      </p>
    );
  }

  const hotspotByPath = new Map(hotspots.map((h) => [h.path, h]));
  const hottest = churn[0].score > 0 ? churn[0].score : 1;

  return (
    <div>
      <div className="overflow-x-auto">
        <table className="w-full text-left text-sm">
          <thead>
            <tr className="border-b border-slate-800 text-xs uppercase tracking-wider text-slate-300">
              <th scope="col" className="py-2 pr-4 font-medium">File</th>
              <th scope="col" className="py-2 pr-4 text-right font-medium">Commits</th>
              <th scope="col" className="py-2 pr-4 text-right font-medium">Added</th>
              <th scope="col" className="py-2 pr-4 text-right font-medium">Deleted</th>
              <th scope="col" className="py-2 pr-4 font-medium">Share of churn</th>
              <th scope="col" className="py-2 font-medium">Also a hotspot</th>
            </tr>
          </thead>
          <tbody>
            {churn.map((c) => {
              const hotspot = hotspotByPath.get(c.path);
              return (
                <tr key={c.path} className="border-b border-slate-800/50 align-middle">
                  <td className="py-2.5 pr-4">
                    <span className="block truncate font-mono text-xs text-slate-200">
                      {c.path}
                    </span>
                  </td>
                  <td className="tabular py-2.5 pr-4 text-right text-slate-400">
                    {c.commits}
                  </td>
                  <td className="tabular py-2.5 pr-4 text-right text-slate-300">
                    {c.added.toLocaleString('en-US')}
                  </td>
                  <td className="tabular py-2.5 pr-4 text-right text-slate-400">
                    {c.deleted.toLocaleString('en-US')}
                  </td>
                  <td className="w-32 py-2.5">
                    <span
                      className="block h-1.5 rounded bg-slate-700"
                      role="img"
                      aria-label={`${c.path}: ${Math.round((c.score / hottest) * 100)}% of the top file's churn`}
                    >
                      <span
                        className="block h-1.5 rounded bg-slate-400"
                        style={{ width: `${Math.max((c.score / hottest) * 100, 1)}%` }}
                      />
                    </span>
                  </td>
                  <td className="py-2.5">
                    {hotspot ? (
                      <span
                        className={
                          hotspot.confirmed
                            ? 'rounded bg-red-500/15 px-2 py-0.5 text-xs font-medium text-red-300'
                            : 'rounded bg-slate-700/40 px-2 py-0.5 text-xs font-medium text-slate-400'
                        }
                      >
                        {hotspot.confirmed ? 'Confirmed' : 'Candidate'}
                      </span>
                    ) : (
                      <span className="text-xs text-slate-400">—</span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <p className="mt-3 text-xs text-slate-400">
        Movement inside the analysis window. A file can be large without ever
        appearing here, and can churn heavily without being large; a hotspot
        needs both.
      </p>
    </div>
  );
}