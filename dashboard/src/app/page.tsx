'use client';

import { useOverview } from 'lib/useOverview';
import { HealthGauge, MetricBars, StatTile } from 'components/HealthGauge';
import { HealthTimeline } from 'components/HealthTimeline';
import { HotspotTable } from 'components/HotspotTable';
import { ChurnTable } from 'components/ChurnTable';
import { CompareView } from 'components/CompareView';
import { LanguageBreakdown } from 'components/LanguageBreakdown';
import { RiskList, RiskSummary } from 'components/RiskList';
import { formatPct } from 'lib/format';

/**
 * OverviewPage is a client component.
 *
 * The static export has no server runtime, so the data cannot be fetched during
 * render: it is fetched in the browser from the Go process serving these files.
 * The alternative, embedding a snapshot at build time, would mean the dashboard
 * showed whatever the repository looked like when someone ran `npm run build`,
 * which for a tool whose entire purpose is reporting the current state of your
 * code is worse than showing nothing.
 */
export default function OverviewPage() {
  const { state, reload } = useOverview();

  if (state.status === 'loading') {
    return (
      <div className="card">
        <p className="card-title">Analyzing</p>
        <p className="text-sm text-slate-400">
          Reading the local repository through the Lensyxe API.
        </p>
      </div>
    );
  }

  if (state.status === 'error') {
    return (
      <div className="card">
        <p className="card-title">Unavailable</p>
        <p className="text-sm text-slate-400">
          Could not reach the local Lensyxe API.
        </p>
        <p className="mt-2 font-mono text-xs text-red-400">{state.message}</p>
        <p className="mt-3 text-xs text-slate-600">
          Is the server still running? Press Ctrl+C in the terminal that started
          it and run <code className="text-slate-400">lensyxe serve</code> again.
        </p>
        <button
          type="button"
          onClick={() => void reload()}
          className="mt-4 rounded border border-slate-700 px-3 py-1.5 text-xs text-slate-300 hover:border-slate-500"
        >
          Retry
        </button>
      </div>
    );
  }

  const { health, risks, hotspots, history } = state.data;

  return (
    <div className="space-y-6">
      <div className="flex justify-end">
        <button
          type="button"
          onClick={() => void reload()}
          className="rounded border border-slate-700 px-3 py-1.5 text-xs text-slate-300 hover:border-slate-500"
        >
          Reload data
        </button>
      </div>

      {/* Summary */}
      <section className="grid gap-6 lg:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
        <div className="card">
          <p className="card-title">Overall Health</p>
          <HealthGauge score={health.health.score} grade={health.health.grade} />
          <p className="mt-3 text-sm text-slate-400">{health.health.summary}</p>
          <p className="mt-2 truncate text-xs text-slate-600">
            <span className="font-mono">{health.root}</span> · lensyxe {health.version}
          </p>
        </div>

        <div className="card">
          <p className="card-title">Components</p>
          <MetricBars metrics={health.health.metrics} />
        </div>
      </section>

      {/* Figures */}
      <section className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile
          label="Source files"
          value={String(health.code.source_files)}
          note={`${health.code.files} files scanned, ${health.code.code_lines} code lines`}
        />
        <StatTile
          label="Test file ratio"
          value={formatPct(health.code.test_file_ratio)}
          note={`${health.code.test_files} test files of ${health.code.files} total`}
        />
        <StatTile
          label="Dependencies"
          value={String(health.dependencies.total)}
          note={
            health.dependencies.detected
              ? health.dependencies.locked
                ? `${health.dependencies.direct} direct, lockfile present`
                : `${health.dependencies.direct} direct, no lockfile`
              : 'no manifest detected'
          }
        />
        <StatTile
          label="Git window"
          value={health.git.is_repository ? String(health.git.window_commits) : 'n/a'}
          note={
            health.git.is_repository
              ? `commits by ${health.git.authors} author${
                  health.git.authors === 1 ? '' : 's'
                } · branch ${health.git.branch || 'detached'}`
              : health.git.note ?? 'not a git repository'
          }
        />
      </section>

      {/* Comparison */}
      <section className="card">
        <p className="card-title">Compare against a recorded run</p>
        <CompareView
          current={health}
          records={history.records}
          confirmedHotspots={hotspots.confirmed}
        />
      </section>

      {/* Languages */}
      <section className="card">
        <p className="card-title">Languages</p>
        <LanguageBreakdown languages={health.code.languages} />
      </section>

      {/* Timeline */}
      <section className="card">
        <p className="card-title">Health over time</p>
        <HealthTimeline records={history.records} />
      </section>

      {/* Risks */}
      <section className="card">
        <div className="mb-4 flex flex-wrap items-center justify-between gap-4">
          <p className="card-title mb-0">Top detected risks</p>
          <RiskSummary
            critical={risks.critical}
            high={risks.high}
            medium={risks.medium}
            low={risks.low}
          />
        </div>
        <RiskList risks={risks.risks} />
      </section>

      {/* Hotspots */}
      <section className="card">
        <p className="card-title">Hotspots</p>
        <HotspotTable hotspots={hotspots.hotspots} />
        {hotspots.total > 0 && (
          <p className="mt-3 text-xs text-slate-600">
            {hotspots.confirmed} confirmed of {hotspots.total} candidate
            {hotspots.total === 1 ? '' : 's'}. Confirmation requires size, churn,
            and complexity to cross their thresholds together. Select a row for
            the detail.
          </p>
        )}
      </section>

      {/* Churn */}
      <section className="card">
        <p className="card-title">Churn</p>
        <ChurnTable churn={hotspots.churn} hotspots={hotspots.hotspots} />
      </section>

      <p className="text-xs text-slate-600">
        {history.count} recorded run{history.count === 1 ? '' : 's'} · {risks.total}{' '}
        risk{risks.total === 1 ? '' : 's'} in the current snapshot
      </p>
    </div>
  );
}