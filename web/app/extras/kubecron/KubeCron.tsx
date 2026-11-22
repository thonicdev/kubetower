import { useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { Link, useLocation, useParams, useSearchParams } from 'react-router';
import { DataGrid, ListView, Pill, focusRing, formatClock, useDeclarePageFacts, type GridColumn, btnSecondary } from '@kubetower/ui';

import { backHere, boardQuery, type BoardAnswer, type Board, type CronJob, type Outcome } from './data';
import { duration, moment, relative, VERDICT } from './format';
import { STATE_FILTERS, type StateFilter } from './calendar';
import { CollectorHelp, CollectorSentence } from './InstallCollector';
import { PageHeatmap, type Filters } from './PageHeatmap';

/**
 * KubeCron, at `/c/:cluster/x/kubecron`: is anything failing to run that should
 * be running.
 *
 * The question the console's CronJobs list cannot answer. That list shows the
 * schedule and how long ago each job fired; whether that is *late* depends on
 * the expression, so this page leads with the verdict and keeps the raw figures
 * beside it. The verdict stays on this page, whole with the history it is
 * read beside, and is not copied into the console's own lists.
 *
 * Nothing here changes a cluster. Suspend, resume and trigger are the
 * console's, on the CronJob's own drawer, behind its confirmation.
 */
export function KubeCron(): ReactNode {
  const { cluster = '' } = useParams<{ cluster: string }>();
  useDeclarePageFacts({ title: 'KubeCron' });
  const options = boardQuery(cluster);
  const answer = useQuery(options);
  const list = useQuery({ ...options, select: listOf });
  const board = answer.data?.board;
  const [params, setParams] = useSearchParams();
  const state = params.get('state');
  const filters: Filters = {
    ns: params.get('ns') ?? undefined,
    cronJob: params.get('cronjob') ?? undefined,
    state: STATE_FILTERS.includes(state as StateFilter) ? (state as StateFilter) : 'all',
    day: params.get('day') ?? undefined,
    log: params.get('log') ?? undefined,
  };
  const onChange = (changes: Partial<Record<keyof Filters, string | undefined>>) =>
    setParams((p) => {
      const next = new URLSearchParams(p);
      for (const [key, value] of Object.entries(changes)) {
        const name = key === 'cronJob' ? 'cronjob' : key;
        if (value === undefined) next.delete(name);
        else next.set(name, value);
      }
      return next;
    });

  return (
    <div className="flex flex-col gap-4 p-4">
      <header className="flex flex-wrap items-baseline gap-2">
        <h1 className="text-xl font-medium">KubeCron</h1>
        <p className="flex-1 text-xs text-muted">
          {cluster}
          {board === undefined ? null : <> · read {formatClock(board.collectedAt)}</>}
        </p>
        <button
          type="button"
          onClick={() => void answer.refetch()}
          disabled={answer.isFetching}
          className={btnSecondary}
        >
          {answer.isFetching ? 'Reading…' : 'Refresh'}
        </button>
      </header>

      {board === undefined ? null : <Summary board={board} />}
      {board?.complete === false && board.note !== undefined ? <Incomplete note={board.note} /> : null}

      <PageHeatmap cluster={cluster} filters={filters} onChange={onChange} />

      <ListView query={list} what="the CronJobs">
        {(items) => <CronJobs rows={narrowed(items, filters)} cluster={cluster} />}
      </ListView>

      {board === undefined ? null : <CollectorHelp cluster={cluster} state={board.collector} />}
    </div>
  );
}

function listOf(answer: BoardAnswer): BoardAnswer['list'] {
  return answer.list;
}

/** The heatmap's namespace and CronJob filters, applied to the list too. */
function narrowed(rows: readonly CronJob[], f: Filters): readonly CronJob[] {
  return rows.filter((r) => (f.ns === undefined || r.namespace === f.ns) && (f.cronJob === undefined || `${r.namespace}/${r.name}` === f.cronJob));
}

/** What the counts were read from, for the tooltip: the history's span and
 *  how the collector was reached. */
function summaryHint(board: Board): string | undefined {
  const c = board.collector;
  const parts = c.state === 'ok' ? [board.history.detail, c.source] : c.state === 'absent' ? ['No collector: verdicts from the CronJobs themselves'] : [];
  const text = parts.filter((p): p is string => p !== undefined && p !== '').join(' ');
  return text === '' ? undefined : text;
}

/** The counts, read against how much was examined, and where the answer came
 *  from. A collector that could not be read is said here, in its own words. */
function Summary({ board }: { board: Board }): ReactNode {
  const c = board.counts;
  const tiles: { label: string; count: number; tone: string }[] = [
    { label: 'missed', count: c.missed, tone: c.missed > 0 ? 'text-bad' : 'text-muted' },
    { label: 'cannot say', count: c.unknown, tone: c.unknown > 0 ? 'text-warn' : 'text-muted' },
    { label: 'failing (7d)', count: c.failing, tone: c.failing > 0 ? 'text-bad' : 'text-muted' },
    { label: 'running now', count: c.active, tone: 'text-fg' },
    { label: 'suspended', count: c.suspended, tone: 'text-muted' },
    { label: 'CronJobs', count: c.total, tone: 'text-fg' },
  ];
  return (
    <section aria-label="Summary" title={summaryHint(board)} className="rounded-lg border border-line bg-panel p-3">
      <ul className="flex flex-wrap gap-6">
        {tiles.map((t) => (
          <li key={t.label} data-tile={t.label} className="flex items-baseline gap-1.5">
            <span className={`text-2xl font-semibold tabular-nums ${t.tone}`}>{t.count}</span>
            <span className="text-xs text-muted">{t.label}</span>
          </li>
        ))}
      </ul>
      {board.collector.state === 'ok' || board.collector.state === 'absent' ? null : (
        <p data-collector={board.collector.state} className="mt-2 text-xs text-muted">
          <CollectorSentence state={board.collector} after="The verdicts below do not depend on it." />
        </p>
      )}
      {board.retained.ok ? null : <p className="mt-1 text-xs text-warn">{board.retained.detail}</p>}
    </section>
  );
}

function Incomplete({ note }: { note: string }): ReactNode {
  return (
    <div role="status" data-complete="false" className="rounded-lg border border-line bg-panel-2 p-3 text-sm">
      <p className="font-medium text-warn">Incomplete list</p>
      <p className="mt-1 text-muted">{note}</p>
    </div>
  );
}

export function VerdictPill({ cronJob }: { cronJob: CronJob }): ReactNode {
  const v = VERDICT[cronJob.verdict];
  return (
    <span className="flex flex-col gap-0.5">
      <span className="flex items-center gap-1">
        <Pill tone={v.tone} title={v.title}>
          {v.word}
        </Pill>
        {cronJob.history?.failing === true ? (
          <Pill tone="bad" title="The last three finished runs failed. One success clears it.">
            Failing
          </Pill>
        ) : null}
        {cronJob.activeCount > 1 ? (
          <Pill tone="warn" title="More than one run is in flight at once: the job takes longer than its own period.">
            overlapping
          </Pill>
        ) : null}
      </span>
      {cronJob.unresolved !== undefined ? <span className="text-xs text-muted">{cronJob.unresolved}</span> : null}
      {cronJob.lateMs !== undefined ? (
        <span className="text-xs text-bad">due {duration(cronJob.lateMs)} ago</span>
      ) : null}
    </span>
  );
}

/** The collector's window, then how long the job takes. An empty cell means no
 *  collector answered for this CronJob, which is not "no failures". */
export function RunsCell({ cronJob }: { cronJob: CronJob }): ReactNode {
  const h = cronJob.history;
  return (
    <span className="flex flex-col gap-0.5">
      {h === undefined ? <span className="text-muted">—</span> : <OutcomeLine outcome={h} />}
      <span className="flex flex-wrap gap-1 text-xs text-muted">
        {h?.avgDurationMs !== undefined ? <span>avg {duration(h.avgDurationMs)}</span> : null}
        {h?.maxDurationMs !== undefined ? <span>· max {duration(h.maxDurationMs)}</span> : null}
        {cronJob.runs.slice(0, 3).map((r) => (
          <span key={r.name} data-run-class={r.class ?? ''} className={RUN_TONE[r.class ?? ''] ?? 'text-muted'} title={`${r.name}: ${r.status}`}>
            {r.durationMs === undefined ? r.status : duration(r.durationMs)}
          </span>
        ))}
      </span>
    </span>
  );
}

const RUN_TONE: Readonly<Record<string, string>> = { ok: 'text-ok', bad: 'text-bad', warn: 'text-warn' };

function OutcomeLine({ outcome }: { outcome: Outcome }): ReactNode {
  const tone = outcome.failed === 0 ? 'ok' : outcome.failed === outcome.runs ? 'bad' : 'warn';
  return (
    <span className="flex items-center gap-1.5">
      <Pill tone={tone} title="Over the collector's 7-day window. The denominator matters: 4 failures in 4 609 runs is noise, 3 in 5 is broken.">
        {outcome.failed} of {outcome.runs} failed
      </Pill>
      {outcome.lastFailure !== undefined ? (
        <span className="text-xs text-muted">last {relative(outcome.lastFailure)}</span>
      ) : null}
    </span>
  );
}

function columns(cluster: string, back: Record<string, unknown>): readonly GridColumn<CronJob>[] {
  return [
    {
      key: 'name',
      label: 'CronJob',
      wrap: 'wrap',
      measure: (row) => `${row.name} ${row.namespace}`,
      cell: (row) => (
        <span className="flex flex-col">
          <Link
            to={`/c/${encodeURIComponent(cluster)}/x/kubecron/${encodeURIComponent(row.namespace)}/${encodeURIComponent(row.name)}`}
            state={back}
            className={`font-medium underline-offset-2 hover:underline ${focusRing}`}
          >
            {row.name}
          </Link>
          <span className="text-xs text-muted">{row.namespace}</span>
        </span>
      ),
    },
    {
      key: 'schedule',
      label: 'Schedule',
      measure: (row) => `${row.schedule} ${row.timeZone ?? ''}`,
      cell: (row) => (
        <span className="flex flex-col">
          <code className="font-mono text-xs">{row.schedule}</code>
          {row.timeZone === undefined ? null : <span className="text-xs text-muted">{row.timeZone}</span>}
        </span>
      ),
    },
    {
      // "Firing", not "State": the verdict is whether the controller fires on
      // schedule, and a CronJob whose every run fails is still On time. Its
      // outcomes are the Runs column.
      key: 'state',
      label: 'Firing',
      wrap: 'wrap',
      measure: (row) => VERDICT[row.verdict].word,
      cell: (row) => <VerdictPill cronJob={row} />,
    },
    {
      key: 'next',
      label: 'Next',
      measure: () => 'Thu 00:15 in 59m',
      cell: (row) =>
        row.nextAt === undefined ? (
          <span className="text-muted">—</span>
        ) : row.suspended ? (
          <NextWhenSuspended at={row.nextAt} />
        ) : (
          <span className="flex flex-col">
            <span>{moment(row.nextAt)}</span>
            <span className="text-xs text-muted">{relative(row.nextAt)}</span>
          </span>
        ),
    },
    {
      key: 'last',
      label: 'Last fired',
      measure: () => 'Thu 00:15 ok 59m ago',
      cell: (row) => (
        <span className="flex flex-col">
          {row.lastScheduleTime === undefined ? <span className="text-muted">never</span> : <span>{relative(row.lastScheduleTime)}</span>}
          {/* Only when it lags the last firing: a job that fires hourly and last
              succeeded yesterday is failing, not idle. */}
          {row.lastSuccessfulTime !== undefined && row.lastScheduleTime !== undefined && row.lastSuccessfulTime < row.lastScheduleTime ? (
            <span className="text-xs text-muted">ok {relative(row.lastSuccessfulTime)}</span>
          ) : null}
        </span>
      ),
    },
    {
      key: 'runs',
      label: 'Runs',
      wrap: 'wrap',
      measure: () => '10 of 10 failed last 3h ago',
      cell: (row) => <RunsCell cronJob={row} />,
    },
  ];
}

function CronJobs({ rows, cluster }: { rows: readonly CronJob[]; cluster: string }): ReactNode {
  // The CronJob's ← returns here, with the heatmap's filters as they are.
  const back = backHere(useLocation());
  return (
    <DataGrid
      columns={columns(cluster, back)}
      rows={rows}
      rowKey={(row) => `${row.namespace}/${row.name}`}
      label="CronJobs on this cluster, worst first"
      empty={null}
    />
  );
}

/**
 * A suspended CronJob fires nothing, so its next occurrence is not drawn as
 * one: "would be" and muted, kept because somebody about to resume it wants
 * to know when it would fire (scheduled.go computes it on purpose).
 */
export function NextWhenSuspended({ at }: { at: string }): ReactNode {
  return (
    <span className="flex flex-col text-muted" title="spec.suspend is true: nothing fires until it is resumed.">
      <span>none, suspended</span>
      <span className="text-xs">would be {moment(at)}</span>
    </span>
  );
}
