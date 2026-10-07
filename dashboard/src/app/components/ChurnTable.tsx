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
 * The `Hotspot` column joins a churn row to a hotspot of the same path, so a
 * reader can see at a glance which of the churning files are also large. It is
 * derived by exact path match, which is the same identity the analyzer uses to
 * join churn to hotspots. A path present in one list and not the other shows a
 * dash rather than being silently omitted.
 */
export function ChurnTable({
  churn,
  hotspots,
}: {
  churn: ChurnEntry[];
  hotspots: Hotspot[];
}) {
  if (churn.length === 0) {
    return (
      <p className="text-sm text-slate-300">
        No churn was recorded in the window. Either the directory is not a git
        repository, or nothing changed inside it.
      </p>
    );
  }

  const hotspotByPath = new Map(hotspots.map((h) => [h.path, h]));

  /*
   * How many of these rows also appear in the hotspot list.
   *
   * This is very often zero, and that is worth explaining rather than printing
   * ten em-dashes. Both lists are capped independently by the analyzer --
   * HotspotLimit and the churn limit -- so two lists of ten from a repository
   * with hundreds of files will frequently not intersect even when plenty of
   * files are both large and moving. A column that is entirely dashes reads as
   * "nothing here is a hotspot", which is a claim the truncated tables cannot
   * support.
   */
  const overlapCount = churn.filter((c) => hotspotByPath.has(c.path)).length;

  /*
   * Two denominators, and conflating them is what made this column lie.
   *
   * The bar is scaled to the largest row so the shape of the distribution is
   * readable. The figure printed beside it is that row's share of *all* the
   * churn, which is what "share of churn" means to a reader.
   *
   * Dividing both by the top row made every number a percentage of the leader,
   * so a table of ten files summing to 300% read as though it described a
   * partition. The old aria-label admitted it -- "of the top file's churn" --
   * while the column header said something else entirely.
   */
  const total = churn.reduce((sum, c) => sum + c.score, 0);
  const hottest = churn[0].score > 0 ? churn[0].score : 1;

  return (
    <div>
      <div className="overflow-x-auto">
        <table className="w-full text-left text-sm">
          <thead>
            <tr className="border-b border-slate-800 text-[0.65rem] uppercase tracking-wider text-slate-400">
              <th scope="col" className="py-2 pr-4 font-medium">File</th>
              <th scope="col" className="hidden py-2 pr-4 text-right font-medium sm:table-cell">Commits</th>
              <th scope="col" className="py-2 pr-4 text-right font-medium">Added</th>
              <th scope="col" className="hidden py-2 pr-4 text-right font-medium md:table-cell">Deleted</th>
              <th scope="col" className="py-2 pr-4 text-right font-medium">Share</th>
              <th scope="col" className="hidden py-2 pr-4 font-medium lg:table-cell">Relative</th>
              <th scope="col" className="py-2 font-medium">
                {overlapCount > 0 ? 'Hotspot' : <span className="sr-only">Hotspot</span>}
              </th>
            </tr>
          </thead>
          <tbody>
            {churn.map((c) => {
              const hotspot = hotspotByPath.get(c.path);
              const share = total > 0 ? (c.score / total) * 100 : 0;
              const relative = (c.score / hottest) * 100;
              /* The badge is only rendered when at least one row can carry it,
                 so the column never degrades into a column of em-dashes. */
              return (
                <tr key={c.path} className="border-b border-slate-800/50 align-middle">
                  <th
                    scope="row"
                    className="max-w-[8rem] py-2.5 pr-4 text-left font-normal sm:max-w-xs lg:max-w-none"
                  >
                    <span className="block truncate font-mono text-xs text-slate-200">
                      {c.path}
                    </span>
                  </th>
                  <td className="tabular py-2.5 pr-4 text-right text-slate-400">
                    {c.commits}
                  </td>
                  <td className="tabular py-2.5 pr-4 text-right text-slate-300">
                    {c.added.toLocaleString('en-US')}
                  </td>
                  <td className="tabular py-2.5 pr-4 text-right text-slate-400">
                    {c.deleted.toLocaleString('en-US')}
                  </td>
                  <td className="tabular py-2.5 pr-4 text-right text-slate-300">
                    {share >= 0.05 ? `${share.toFixed(1)}%` : '<0.1%'}
                  </td>
                  <td className="hidden w-24 py-2.5 pr-4 lg:table-cell lg:w-28">
                    <span
                      className="block h-1.5 rounded bg-slate-800"
                      role="img"
                      aria-label={`${c.path}: ${relative.toFixed(0)}% of the busiest file's churn`}
                    >
                      <span
                        className="block h-1.5 rounded bg-slate-400"
                        style={{ width: `${Math.max(relative, 1.5)}%` }}
                      />
                    </span>
                  </td>
                  <td className="py-2.5">
                    {overlapCount === 0 ? null : hotspot ? (
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
      <p className="mt-3 text-xs leading-relaxed text-slate-400">
        Movement inside the analysis window.{' '}
        <span className="text-slate-300">Share</span> is this file&rsquo;s portion
        of all churn in the table.{' '}
        <span className="text-slate-300">Relative</span> scales the bars to the
        busiest file so the distribution is readable. A file can be large without
        ever appearing here, and can churn heavily without being large; a hotspot
        needs both.
      </p>
      {overlapCount === 0 && (
        <p className="mt-2 text-xs leading-relaxed text-slate-400">
          <span className="text-slate-300">None of these files is in the hotspot
          list</span> beside it. Both tables show the top ten rows and are capped
          independently, so this is a fact about the truncation rather than about
          the files: it does not mean none of them is a hotspot, only that none
          reached the top ten on both rankings at once.
        </p>
      )}
    </div>
  );
}