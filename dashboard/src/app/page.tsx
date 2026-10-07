'use client';

import { useOverview } from 'lib/useOverview';
import { HealthGauge, MetricBars, StatTile, scoreLabel } from 'components/HealthGauge';
import { HealthTimeline } from 'components/HealthTimeline';
import { HotspotTable } from 'components/HotspotTable';
import { ChurnTable } from 'components/ChurnTable';
import { CompareView } from 'components/CompareView';
import { LanguageBreakdown } from 'components/LanguageBreakdown';
import { RiskList, RiskSummary } from 'components/RiskList';
import { SnapshotLoader } from 'components/SnapshotLoader';
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
 *
 * Layout note. The reading order is deliberate and is not the order the
 * analyzer computes things in: score first, because it is the answer; then what
 * to do about it, which is the risks; then the evidence behind the risks; then
 * the composition. Two sections that would render as full-width cards holding a
 * single sentence of "nothing yet" are collapsed into one History card, because
 * an empty card is a hole in the page and two holes read as a broken build.
 */
export default function OverviewPage() {
  const { state, reload, loadSnapshot, backToLoader } = useOverview();

  if (state.status === 'loading') {
    return (
      <div className="flex items-center gap-3 py-16 text-sm text-slate-400">
        <span className="h-2 w-2 animate-pulse rounded-full bg-sky-400" />
        Reading the local repository through the Lensyxe API.
      </div>
    );
  }

  // Static host: no API to talk to, so a snapshot is the only way in. This is
  // not an error state, and it must not be dressed as one.
  if (state.status === 'needs-snapshot') {
    return <SnapshotLoader onLoad={loadSnapshot} />;
  }

  if (state.status === 'error') {
    return (
      <div className="card max-w-2xl">
        <h2 className="text-sm font-semibold text-slate-100">Unavailable</h2>
        <p className="mt-2 text-sm text-slate-300">
          Could not reach the local Lensyxe API.
        </p>
        <p className="mt-2 break-words font-mono text-xs text-red-300">
          {state.message}
        </p>
        <p className="mt-3 text-xs leading-relaxed text-slate-400">
          Is the server still running? Press Ctrl+C in the terminal that started
          it and run <code className="text-slate-300">lensyxe serve</code> again.
        </p>
        <button
          type="button"
          onClick={() => void reload()}
          className="mt-4 rounded border border-slate-700 px-3 py-1.5 text-xs text-slate-300 hover:border-slate-500 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-400"
        >
          Retry
        </button>
      </div>
    );
  }

  const { health, risks, hotspots, history } = state.data;
  const weakest = health.health.metrics
    .filter((m) => m.applicable)
    .reduce((min, m) => (m.score < min.score ? m : min));

  return (
    <div className="space-y-8 sm:space-y-10">
      {/* ---------------------------------------------------- provenance */}
      {/*
        Stated, not implied. A snapshot cannot be refreshed -- there is no
        repository behind it -- so offering "Reload data" would be offering a
        button that cannot do what it says. What it offers instead is a file
        picker, which is honest.
      */}
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        {state.mode === 'snapshot' && state.snapshot ? (
          <p className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-amber-200/90">
            <span className="rounded border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 font-medium uppercase tracking-wide">
              Snapshot
            </span>
            <span className="truncate font-mono text-amber-100/80">
              {state.snapshot.root || 'unknown repository'}
            </span>
            {state.snapshot.generatedAt && (
              <span className="text-amber-200/60">
                {state.snapshot.generatedAt.replace('T', ' ').replace('Z', ' UTC')}
              </span>
            )}
            {state.snapshot.version && (
              <span className="text-amber-200/60">lensyxe {state.snapshot.version}</span>
            )}
          </p>
        ) : (
          <p className="text-xs text-slate-400">
            <span className="mr-1.5 inline-block h-1.5 w-1.5 rounded-full bg-emerald-400 align-middle" />
            Live · re-analyzes on demand from a local repository
          </p>
        )}
        <button
          type="button"
          onClick={() => (state.mode === 'snapshot' ? backToLoader() : void reload())}
          className="shrink-0 self-start rounded border border-slate-700 px-3 py-1.5 text-xs text-slate-300 transition-colors hover:border-slate-500 hover:text-slate-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-400 sm:self-auto"
        >
          {state.mode === 'snapshot' ? 'Load another snapshot' : 'Re-analyze'}
        </button>
      </div>

      {/* --------------------------------------------------------- score */}
      {/*
        The score and the breakdown share one band rather than sitting in two
        cards side by side. At tablet width a 2/3+1/3 split left the gauge card
        two-thirds empty, because the gauge is a fixed 140px and the summary is
        one sentence.
      */}
      <section className="grid gap-6 border-b border-slate-800 pb-8 sm:gap-8 lg:grid-cols-[auto_minmax(0,1fr)] lg:gap-12">
        <div className="flex items-center gap-5 sm:gap-6">
          <HealthGauge score={health.health.score} grade={health.health.grade} />
          <div className="min-w-0 lg:hidden">
            <p className="text-sm font-medium text-slate-200">
              {scoreLabel(health.health.score)}
            </p>
            <p className="mt-1.5 text-xs leading-relaxed text-slate-400">
              Weakest dimension: {weakest.label} at {weakest.score.toFixed(1)}.
            </p>
          </div>
        </div>

        <div className="min-w-0">
          <div className="hidden lg:block">
            <p className="text-base text-slate-200">
              {scoreLabel(health.health.score)}
            </p>
            <p className="mt-1.5 text-sm leading-relaxed text-slate-400">
              {health.health.summary} Weakest dimension:{' '}
              <span className="text-slate-300">{weakest.label}</span> at{' '}
              {weakest.score.toFixed(1)} of 100.
            </p>
          </div>
          <div className="mt-0 lg:mt-5">
            <MetricBars metrics={health.health.metrics} />
          </div>
        </div>
      </section>

      {/* -------------------------------------------------------- figures */}
      {/*
        Four figures on one ruled row rather than four cards. Cards per figure
        made each number a 120px box with a border, which is a lot of chrome for
        a number, and it forced a 2x2 grid at tablet width for no reason.
      */}
      <section className="grid grid-cols-2 gap-x-4 gap-y-5 sm:grid-cols-4 sm:gap-x-6 lg:gap-x-10">
        <StatTile
          label="Source files"
          value={String(health.code.source_files)}
          note={`${health.code.files} scanned · ${health.code.code_lines.toLocaleString('en-US')} code lines`}
        />
        <StatTile
          label="Test files"
          value={formatPct(health.code.test_file_ratio)}
          note={`${health.code.test_files} of ${health.code.files} · ${formatPct(health.code.test_line_ratio)} of lines`}
        />
        <StatTile
          label="Dependencies"
          value={String(health.dependencies.total)}
          note={
            health.dependencies.detected
              ? health.dependencies.locked
                ? `${health.dependencies.direct} direct · locked`
                : `${health.dependencies.direct} direct · no lockfile`
              : 'no manifest detected'
          }
        />
        <StatTile
          label="Git window"
          value={health.git.is_repository ? String(health.git.window_commits) : 'n/a'}
          note={
            health.git.is_repository
              ? `commits · ${health.git.authors} author${health.git.authors === 1 ? '' : 's'} · ${health.git.branch || 'detached'}`
              : (health.git.note ?? 'not a git repository')
          }
        />
      </section>

      {/* ---------------------------------------------------------- risks */}
      <section>
        <div className="mb-3 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
          <h2 className="text-sm font-semibold text-slate-100">Risks</h2>
          <RiskSummary
            critical={risks.critical}
            high={risks.high}
            medium={risks.medium}
            low={risks.low}
          />
        </div>
        <RiskList risks={risks.risks} />
      </section>

      {/* ------------------------------------------------- hotspots/churn */}
      {/*
        Side by side only where there is room for two dense tables. Below xl
        they stack, because two seven-column tables at 50% width is a layout
        that cannot be read at any size.
      */}
      <section className="grid gap-8 xl:grid-cols-2 xl:gap-10">
        <div className="min-w-0">
          <h2 className="mb-3 text-sm font-semibold text-slate-100">Hotspots</h2>
          <HotspotTable hotspots={hotspots.hotspots} />
          {hotspots.total > 0 && (
            <p className="caption measure">
              A file is confirmed only when size, churn and complexity cross
              their thresholds together.
            </p>
          )}
        </div>
        <div className="min-w-0">
          <h2 className="mb-3 text-sm font-semibold text-slate-100">Churn</h2>
          <ChurnTable churn={hotspots.churn} hotspots={hotspots.hotspots} />
        </div>
      </section>

      {/* ------------------------------------------------------ languages */}
      <section className="grid gap-8 lg:grid-cols-[minmax(0,1fr)_minmax(0,2fr)] lg:gap-10">
        <div className="min-w-0">
          <h2 className="mb-3 text-sm font-semibold text-slate-100">History</h2>
          <HealthTimeline records={history.records} />
          {history.records.length > 0 && (
            <div className="mt-6">
              <h3 className="mb-2 text-xs uppercase tracking-wider text-slate-400">
                Compare against a recorded run
              </h3>
              <CompareView
                current={health}
                records={history.records}
                confirmedHotspots={hotspots.confirmed}
              />
            </div>
          )}
        </div>
        <div className="min-w-0">
          <h2 className="mb-3 text-sm font-semibold text-slate-100">Languages</h2>
          <LanguageBreakdown languages={health.code.languages} />
        </div>
      </section>
    </div>
  );
}