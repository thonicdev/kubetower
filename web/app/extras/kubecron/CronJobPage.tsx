import { useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router';
import {
  BackArrow,
  ObjectVerbs,
  PageHeader,
  Pill,
  focusRing,
  useDeclarePageFacts,
  btnSecondary,
  TABLE,
  HEAD_ROW,
  ROW,
  headCell,
  bodyCell,
} from '@kubetower/ui';

import { backOf, detailQuery, kubeCronPagePath, leaveFor, neighbours, type Daily, type Detail, type RunRecord } from './data';
import { bytes, duration, moment, momentOn, relative } from './format';
import { CollectorStanding } from './InstallCollector';
import { NextWhenSuspended, RunsCell, VerdictPill } from './KubeCron';
import { Heatmap } from './Heatmap';
import { STATE_FILTERS, grid, knownFrom, sumDays, todayUTC, type StateFilter } from './calendar';
import { InlineLog } from './LogPage';
import { OutcomeFilter, RunTotal, STATE_OF, yearWindow } from './PageHeatmap';
import { LogToggle, RunPanel, STATUS_TONE } from './RunView';
import { View } from './View';

/**
 * One CronJob's history, at `/c/:cluster/x/kubecron/:namespace/:name`: its
 * verdict, a 90-day calendar, its runs page by page, and one run's log and
 * cost (`?run=<id>`). A page and not a panel under the list: a panel opened
 * in place lands under the fold and reads as a control that did nothing.
 */
export function CronJobPage(): ReactNode {
  const { cluster = '', namespace = '', name = '' } = useParams<{ cluster: string; namespace: string; name: string }>();
  useDeclarePageFacts({ title: `KubeCron / ${namespace} / ${name}` });
  const [params, setParams] = useSearchParams();
  const location = useLocation();
  const navigate = useNavigate();
  const before = params.get('before') ?? undefined;
  const runId = params.get('run') ?? undefined;
  // One day of the calendar: the runs of that day only.
  const day = params.get('day') ?? undefined;
  // The run whose log is unfolded beside the runs (or under them, narrow).
  const log = params.get('log') ?? undefined;
  // The page's outcome filter, on the heatmap and on the runs; the namespace
  // and CronJob filters have no place here.
  const stateParam = params.get('state');
  const state: StateFilter = STATE_FILTERS.includes(stateParam as StateFilter) ? (stateParam as StateFilter) : 'all';
  // **No reload on a click**: a day, a page or a cursor is a new query, and without the
  // previous answer kept on screen `View` drew "Loading" in the place of the
  // whole page - header and heatmap included - for the first read of each.
  // Kept for this CronJob only: another CronJob's history is never drawn
  // under this one's name.
  const detail = useQuery({
    ...detailQuery(cluster, namespace, name, before, day),
    placeholderData: (previous, previousQuery) =>
      previousQuery?.queryKey[2] === cluster && previousQuery.queryKey[3] === namespace && previousQuery.queryKey[4] === name ? previous : undefined,
  });

  const setAll = (changes: Readonly<Record<string, string | undefined>>) =>
    setParams(
      (p) => {
        const next = new URLSearchParams(p);
        for (const [key, value] of Object.entries(changes)) {
          if (value === undefined) next.delete(key);
          else next.set(key, value);
        }
        return next;
      },
      // The way back is kept through the page's own clicks.
      { state: location.state },
    );
  const set = (key: string, value: string | undefined) => setAll({ [key]: value });

  return (
    <div className="flex flex-col gap-4 p-4">
      <PageHeader
        heading="h1"
        // Back to where the page was opened from, or to KubeCron when that is not known.
        back={
          <BackArrow
            to={backOf(location.state) ?? kubeCronPagePath(cluster)}
            label={backOf(location.state) === undefined ? 'Back to KubeCron' : 'Back'}
            data-cronjob-back=""
          />
        }
        kind="CronJob"
        name={name}
        namespace={namespace}
        className=""
        actions={
          <span className="flex flex-wrap items-center gap-1">
        <ObjectVerbs
          cluster={cluster}
          kind="cronjobs"
          namespace={namespace}
          name={name}
          onActed={() => void detail.refetch()}
          onGone={() => void navigate(leaveFor(location.state, cluster, namespace, name), { replace: true })}
        />
        <button
          type="button"
          onClick={() => void detail.refetch()}
          disabled={detail.isFetching}
          className={btnSecondary}
        >
          {detail.isFetching ? 'Reading…' : 'Refresh'}
        </button>
          </span>
        }
      />

      <View query={detail} what="this CronJob's history">
        {(d) => (
          <>
            <Head detail={d} />
            <CollectorStanding cluster={cluster} state={d.collector} />
            {d.collector.state === 'ok' ? (
              <>
                <Calendar
                  daily={d.daily}
                  note={d.dailyNote}
                  runDays={d.collector.runDays}
                  state={state}
                  // A log whose row the filter may hide closes, as on the page,
                  // and so does a run in focus the filter hides.
                  onState={(next) => {
                    const focused = d.runs?.runs.find((r) => r.id === runId);
                    const hidden = focused !== undefined && !STATE_OF[next](focused);
                    setAll({ state: next === 'all' ? undefined : next, log: undefined, ...(hidden ? { run: undefined } : {}) });
                  }}
                  picked={day}
                  // A second click on the day, like "All runs", clears it; a
                  // new day starts at its newest run, with nothing open.
                  onPick={(next) => setAll({ day: next === day ? undefined : next, before: undefined, run: undefined, log: undefined })}
                />
                {/* **One run takes the list's place**: the header and the calendar stay, the list of
                    runs goes, and the run is read where the list was rather
                    than in a panel at the bottom of the page. "All runs"
                    brings the list back, on the page it was left on. */}
                {/* **The log opens in the page**: beside the runs on a wide
                    window, under them on a narrow one, the heatmap above at
                    its one size either way. Full-page is the log's own
                    button, and its × comes back to this. */}
                <div className={log === undefined ? 'flex flex-col gap-4' : 'grid gap-4 lg:grid-cols-2 lg:items-start'} data-log-layout={log === undefined ? 'closed' : 'open'}>
                  <div className={`min-w-0 ${detail.isPlaceholderData ? 'opacity-60' : ''}`} aria-busy={detail.isPlaceholderData}>
                    {runId === undefined ? (
                      <Runs
                        detail={d}
                        state={state}
                        day={day}
                        onAllDays={() => setAll({ day: undefined, before: undefined, log: undefined })}
                        selected={runId}
                        // An open log follows the run picked, as it follows the arrows.
                        onSelect={(id) => setAll({ run: id, log: log === undefined ? undefined : id })}
                        log={log}
                        onLog={(id) => set('log', id === log ? undefined : id)}
                        // Another page of runs closes the log: its row may not be on it.
                        onOlder={(cursor) => setAll({ before: cursor, log: undefined })}
                        onNewest={before === undefined ? undefined : () => setAll({ before: undefined, log: undefined })}
                      />
                    ) : (
                      <RunPanel
                        cluster={cluster}
                        id={runId}
                        onClose={() => setAll({ run: undefined, log: undefined })}
                        closeLabel="← All runs"
                        {...neighbours((d.runs?.runs ?? []).filter(STATE_OF[state]), runId)}
                        // The log, when open, follows the arrows to the next run.
                        onOpen={(id) => setAll({ run: id, log: log === undefined ? undefined : id })}
                        logOpen={log === runId}
                        onLog={() => set('log', log === runId ? undefined : runId)}
                      />
                    )}
                  </div>
                  {log === undefined ? null : (
                    <div className="min-w-0 lg:sticky lg:top-4">
                      <InlineLog cluster={cluster} id={log} onClose={() => set('log', undefined)} />
                    </div>
                  )}
                </div>
              </>
            ) : null}
          </>
        )}
      </View>

    </div>
  );
}

function Head({ detail }: { detail: Detail }): ReactNode {
  const c = detail.cronJob;
  return (
    <section className="grid gap-3 rounded-lg border border-line bg-panel p-3 text-sm sm:grid-cols-4">
      <Fact label="Firing">
        <VerdictPill cronJob={c} />
      </Fact>
      <Fact label="Schedule">
        <code className="font-mono text-xs">{c.schedule}</code>
        {c.timeZone === undefined ? null : <span className="ml-1 text-xs text-muted">{c.timeZone}</span>}
      </Fact>
      <Fact label="Next">
        {c.nextAt === undefined ? '—' : c.suspended ? <NextWhenSuspended at={c.nextAt} /> : `${moment(c.nextAt)} (${relative(c.nextAt)})`}
      </Fact>
      <Fact label="Runs, 7 days">
        <RunsCell cronJob={c} />
      </Fact>
    </section>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }): ReactNode {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs uppercase tracking-wide text-muted">{label}</span>
      <span>{children}</span>
    </div>
  );
}

// --- the calendar --------------------------------------------------------------

/**
 * The CronJob's window as a heatmap, GitHub's way: one
 * column a week. **A day before the collector started watching, or older than
 * it keeps runs, is drawn as not recorded, never as a day without runs**:
 * drawing the two alike is a wall of alarms the morning after an install.
 */
function Calendar({
  daily,
  note,
  runDays,
  state,
  onState,
  picked,
  onPick,
}: {
  daily: Daily | undefined;
  note: string | undefined;
  runDays: number | undefined;
  state: StateFilter;
  onState: (state: StateFilter) => void;
  picked: string | undefined;
  onPick: (day: string) => void;
}): ReactNode {
  if (daily === undefined) return <p className="text-sm text-warn">{note ?? 'The calendar could not be read.'}</p>;
  const today = todayUTC();
  const since = daily.observedSince === undefined ? undefined : daily.observedSince.slice(0, 10);
  // **The page's year, at the page's size**: a CronJob's heatmap is drawn
  // as the page's is. The server read `windowDays` of it, the page's own window
  // (`heatmapDays`); a day before that is not recorded, never "no run".
  const g = grid({
    byDay: sumDays([daily]),
    windowDays: yearWindow(today),
    today,
    known: knownFrom(today, daily.observedSince, Math.min(daily.windowDays, runDays ?? daily.windowDays)),
    state,
  });
  return (
    <section aria-label="Calendar" className="flex flex-col gap-3 rounded-lg border border-line bg-panel p-3">
      {/* The page's heatmap, toolbar and all, less what picks a CronJob. */}
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <OutcomeFilter state={state} onChange={onState} />
        <span className="flex-1" />
        <RunTotal grid={g} />
      </div>
      <Heatmap grid={g} picked={picked} onPick={onPick} label="Runs per day" />
      <p className="text-2xs text-muted">
        Last {daily.windowDays} days read
        {since === undefined ? ' · the collector did not say since when it watches' : ` · watched since ${since}`}
      </p>
    </section>
  );
}

// --- the runs --------------------------------------------------------------------

function Runs({
  detail,
  state,
  day,
  onAllDays,
  selected,
  onSelect,
  log,
  onLog,
  onOlder,
  onNewest,
}: {
  detail: Detail;
  state: StateFilter;
  day: string | undefined;
  onAllDays: () => void;
  selected: string | undefined;
  onSelect: (id: string) => void;
  log: string | undefined;
  onLog: (id: string) => void;
  onOlder: (cursor: string) => void;
  onNewest: (() => void) | undefined;
}): ReactNode {
  const page = detail.runs;
  if (page === undefined) return <p className="text-sm text-warn">{detail.runsNote ?? 'The runs could not be read.'}</p>;
  // The column is drawn only when some run on this page carries a sample: a
  // run shorter than one sampling interval has none, and a column of "— / —"
  // is noise.
  // The collector's API has no outcome filter: the filter is applied to the
  // page read, and paging goes on through the runs it hides.
  const runs = page.runs.filter(STATE_OF[state]);
  const sampled = runs.some((r) => r.maxCpuMillicores !== undefined || r.maxMemoryBytes !== undefined);
  return (
    <section aria-label="Runs" className="rounded-lg border border-line bg-panel">
      {day === undefined ? null : (
        <div className="flex items-center gap-2 border-b border-line px-3 py-2 text-sm" data-runs-day={day}>
          <span className="flex-1">Runs of {day} (UTC)</span>
          <button type="button" onClick={onAllDays} className={btnSecondary}>
            All runs
          </button>
        </div>
      )}
      <table className={`${TABLE} w-full`}>
        <thead>
          <tr className={HEAD_ROW}>
            <th className={headCell()}>Started</th>
            <th className={headCell()}>Outcome</th>
            <th className={headCell()}>Duration</th>
            <th className={headCell()}>Exit</th>
            {sampled ? <th className={headCell()}>Peak CPU / memory</th> : null}
            <th className={headCell()}>
              <span className="sr-only">Log</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {runs.length === 0 ? (
            <tr>
              <td colSpan={sampled ? 6 : 5} className="px-2 py-3 text-muted" data-runs-empty={page.runs.length === 0 ? 'none' : 'filtered'}>
                {page.runs.length > 0
                  ? `No ${state} run in this page of runs${day === undefined ? '' : ` of ${day}`}.`
                  : day === undefined
                    ? 'The collector holds no run of this CronJob in this page of its history.'
                    : `No run started on ${day}.`}
              </td>
            </tr>
          ) : (
            runs.map((r) => (
              <tr key={r.id} data-run={r.id} aria-selected={r.id === selected} className={`${ROW} hover:bg-panel-2 ${r.id === log ? 'bg-panel-2' : ''}`}>
                <td className={bodyCell()}>
                  <button type="button" onClick={() => onSelect(r.id)} className={`text-accent underline-offset-2 hover:underline ${focusRing}`}>
                    {momentOn(r.startedAt, day)}
                  </button>
                  {r.trigger === 'manual' ? (
                    <span className="ml-1">
                      <Pill tone="warn" title="Started by hand: says nothing about whether the schedule fired.">
                        manual
                      </Pill>
                    </span>
                  ) : null}
                </td>
                <td className={bodyCell()}>
                  <Pill tone={STATUS_TONE[r.status] ?? ''}>{r.status}</Pill>
                  {r.retryCount > 0 ? <span className="ml-1 text-xs text-muted">{r.retryCount} retries</span> : null}
                </td>
                <td className={`${bodyCell()} tabular-nums`}>{r.durationMs === undefined ? '—' : duration(r.durationMs)}</td>
                <td className={`${bodyCell()} tabular-nums`}>{r.exitCode === undefined ? '—' : r.exitCode}</td>
                {sampled ? <td className={`${bodyCell()} text-muted tabular-nums`}>{peak(r)}</td> : null}
                <td className={`${bodyCell()} text-right`}>
                  <LogToggle id={r.id} open={r.id === log} onToggle={() => onLog(r.id)} />
                </td>
              </tr>
            ))
          )}
        </tbody>
      </table>
      {onNewest === undefined && page.runs.length === 0 ? null : (
        <div className="flex gap-2 border-t border-line p-2">
          {onNewest === undefined ? null : (
            <button type="button" onClick={onNewest} className={btnSecondary}>
              Newest
            </button>
          )}
          {page.nextCursor === undefined || page.nextCursor === '' ? (
            page.runs.length === 0 ? null : <span className="text-xs text-muted">No older runs in the collector's history.</span>
          ) : (
            <button type="button" onClick={() => onOlder(page.nextCursor ?? '')} className={btnSecondary}>
              Older
            </button>
          )}
        </div>
      )}
    </section>
  );
}

function peak(r: RunRecord): string {
  const cpu = r.maxCpuMillicores === undefined ? '—' : `${r.maxCpuMillicores}m`;
  const mem = r.maxMemoryBytes === undefined ? '—' : bytes(r.maxMemoryBytes);
  return `${cpu} / ${mem}`;
}
