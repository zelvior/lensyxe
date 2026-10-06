import type { LanguageStat } from 'lib/types';
import { formatPct } from 'lib/format';

/**
 * LanguageBreakdown shows the per-language composition of the repository.
 *
 * Every figure here comes from the snapshot the analyzer already produced; none
 * of it is computed in the browser. The bar widths are a share of code lines and
 * nothing else, so they cannot disagree with the line counts printed beside them.
 */
export function LanguageBreakdown({ languages }: { languages: LanguageStat[] }) {
  if (languages.length === 0) {
    return (
      <p className="text-sm text-slate-500">
        No language was detected. Nothing in the tree matched a known extension.
      </p>
    );
  }

  const totalLines = languages.reduce((sum, l) => sum + l.lines, 0);
  const totalTestFiles = languages.reduce((sum, l) => sum + l.test_files, 0);
  const totalFiles = languages.reduce((sum, l) => sum + l.files, 0);

  return (
    <div>
      <div className="overflow-x-auto">
        <table className="w-full text-left text-sm">
          <thead>
            <tr className="border-b border-slate-800 text-xs uppercase tracking-wider text-slate-500">
              <th className="py-2 pr-4 font-medium">Language</th>
              <th className="py-2 pr-4 text-right font-medium">Files</th>
              <th className="py-2 pr-4 text-right font-medium">Code lines</th>
              <th className="py-2 pr-4 text-right font-medium">Share</th>
              <th className="py-2 pr-4 text-right font-medium">Test files</th>
              <th className="py-2 font-medium">Composition</th>
            </tr>
          </thead>
          <tbody>
            {languages.map((l) => {
              const share = totalLines > 0 ? l.lines / totalLines : 0;
              return (
                <tr key={l.name} className="border-b border-slate-800/50 align-middle">
                  <td className="py-2.5 pr-4 font-medium text-slate-200">{l.name}</td>
                  <td className="tabular py-2.5 pr-4 text-right text-slate-400">
                    {l.files}
                  </td>
                  <td className="tabular py-2.5 pr-4 text-right text-slate-300">
                    {l.lines.toLocaleString('en-US')}
                  </td>
                  <td className="tabular py-2.5 pr-4 text-right text-slate-400">
                    {formatPct(share)}
                  </td>
                  <td className="tabular py-2.5 pr-4 text-right text-slate-400">
                    {l.test_files}
                  </td>
                  <td className="w-40 py-2.5">
                    <span
                      className="block h-1.5 rounded bg-slate-700"
                      role="img"
                      aria-label={`${l.name}: ${formatPct(share)} of code lines`}
                    >
                      <span
                        className="block h-1.5 rounded bg-slate-400"
                        style={{ width: `${Math.max(share * 100, share > 0 ? 1 : 0)}%` }}
                      />
                    </span>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <p className="mt-3 text-xs text-slate-600">
        {totalFiles.toLocaleString('en-US')} file{totalFiles === 1 ? '' : 's'} across{' '}
        {languages.length} language{languages.length === 1 ? '' : 's'},{' '}
        {totalLines.toLocaleString('en-US')} code lines. Blank lines and comments are
        excluded from the line counts, the same way the CLI counts them.{' '}
        {totalTestFiles === 0
          ? 'No test files were attributed to any language, so this says nothing about how the repository is tested.'
          : `${totalTestFiles} of those files are test files.`}
      </p>
    </div>
  );
}