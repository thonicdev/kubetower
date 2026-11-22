import { useQuery } from '@tanstack/react-query';
import { useMemo, type ReactNode } from 'react';
import { Link, useLocation } from 'react-router';
import { Failure, Pending, Pill, focusRing, focusRingTight, TABLE, ROW, bodyCell } from '@kubetower/ui';

import { backHere, cronJobPagePath, dayQuery, heatmapQuery, type DayRun, type DayScope, type HeatmapAnswer } from './data';
import { bytes, duration, momentOn } from './format';
import { Heatmap } from './Heatmap';
import { InlineLog } from './LogPage';
import { STATE_FILTERS, countFor, grid, knownFrom, sumDays, todayUTC, type Grid, type StateFilter } from './calendar';
import { STATUS_TONE } from './RunView';

/**
 * The KubeCron page's heatmap, drawn as GitHub draws contributions: a year
 * of weeks across the whole width, every CronJob the person may list summed,
 * filtered by namespace, CronJob and outcome. A day opens its runs below; a
 * run unfolds its log beside them (under them, narrow), and that panel's own
 * button opens it full-page.
 *
 * The filters live in the address (`ns`, `cronjob`, `state`, `day`, and `log`
 * for the run whose log is open), so a view can be shared, Back undoes a
 * click, and the full page's × returns to it as it was.
 */

export interface Filters {
  ns: string | undefined;
  cronJob: string | undefined;
  state: StateFilter;
  day: string | undefined;
  /** The run whose log is unfolded beside the day's runs. */
  log?: string | undefined;
}

/** Fifty-two whole weeks back from this one, Monday first, as GitHub draws. */
export function yearWindow(today: string): number {
  const weekday = (new Date(`${today}T00:00:00Z`).getUTCDay() + 6) % 7;
  return 52 * 7 + weekday + 1;
}

/** The calendars the filters keep. A CronJob filter is `namespace/name`. */
export function filtered(answer: HeatmapAnswer, f: Pick<Filters, 'ns' | 'cronJob'>): HeatmapAnswer['cronJobs'] {
  return answer.cronJobs.filter(
    (c) => (f.ns === undefined || c.namespace === f.ns) && (f.cronJob === undefined || `${c.namespace}/${c.name}` === f.cronJob),
  );
}

/** How many names a day's query may carry before it asks by namespace or for
 *  everything instead: a query string of a thousand names is refused by an
 *  ingress before it arrives (414). */
export const MAX_NAMED = 50;

/** What `/days` is asked for: the CronJobs that ran, named while they are few;
 *  past that the namespace filter, or nothing, and the server reads every
 *  visible CronJob. Undefined when nothing ran: no question to ask. */
export function dayScope(calendars: HeatmapAnswer['cronJobs'], f: Filters & { day: string }): DayScope | undefined {
  const ran = ranOn(calendars, f.day, f.state);
  if (ran.length === 0) return undefined;
  if (ran.length <= MAX_NAMED) return { cronJobs: ran };
  return f.ns === undefined ? {} : { namespace: f.ns };
}

/** The calendars the filters keep that could not be read. */
export function unreadIn(answer: HeatmapAnswer, f: Pick<Filters, 'ns' | 'cronJob'>): HeatmapAnswer['unread'] {
  return answer.unread.filter(
    (c) => (f.ns === undefined || c.namespace === f.ns) && (f.cronJob === undefined || `${c.namespace}/${c.name}` === f.cronJob),
  );
}

/** Which of those ran that day under the state filter: the ones `/days` asks. */
export function ranOn(calendars: HeatmapAnswer['cronJobs'], day: string, state: StateFilter): string[] {
  return calendars
    .filter((c) => countFor(c.days.find((d) => d.day === day), state) > 0)
    .map((c) => `${c.namespace}/${c.name}`);
}

export function PageHeatmap({
  cluster,
  filters,
  onChange,
}: {
  cluster: string;
  filters: Filters;
  onChange: (changes: Partial<Record<keyof Filters, string | undefined>>) => void;
}): ReactNode {
  const heat = useQuery(heatmapQuery(cluster));
  if (heat.data === undefined) {
    if (heat.isError) return <Failure error={heat.error} onRetry={() => void heat.refetch()} />;
    return <Pending what="the heatmap" />;
  }
  const answer = heat.data;
  // No collector, or one that cannot be read: the summary and the install
  // block already say so, and there is no history to draw.
  if (answer.collector.state !== 'ok') return null;
  return <Board cluster={cluster} answer={answer} filters={filters} onChange={onChange} />;
}

function Board({
  cluster,
  answer,
  filters,
  onChange,
}: {
  cluster: string;
  answer: HeatmapAnswer;
  filters: Filters;
  onChange: (changes: Partial<Record<keyof Filters, string | undefined>>) => void;
}): ReactNode {
  const today = todayUTC();
  const calendars = useMemo(() => filtered(answer, filters), [answer, filters]);
  const g = useMemo(
    () =>
      grid({
        byDay: sumDays(calendars),
        windowDays: yearWindow(today),
        today,
        // The page draws a year; the server read `windowDays` of it. A day
        // outside that window was not read, so it is not recorded, never "no
        // run".
        known: knownFrom(today, answer.observedSince, answer.windowDays),
        state: filters.state,
      }),
    [calendars, today, answer.observedSince, answer.windowDays, filters.state],
  );
  const unread = unreadIn(answer, filters);
  const namespaces = [...new Set([...answer.cronJobs, ...answer.unread].map((c) => c.namespace))].sort();
  const names = [...answer.cronJobs, ...answer.unread]
    .filter((c) => filters.ns === undefined || c.namespace === filters.ns)
    .map((c) => `${c.namespace}/${c.name}`)
    .sort();
  return (
    <section aria-label="Heatmap" className="flex flex-col gap-3 rounded-lg border border-line bg-panel p-3">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <Select
          label="Namespace"
          value={filters.ns}
          options={namespaces}
          all="All namespaces"
          onChange={(v) => onChange({ ns: v, cronJob: undefined, day: undefined, log: undefined })}
        />
        <Select label="CronJob" value={filters.cronJob} options={names} all="All CronJobs" onChange={(v) => onChange({ cronJob: v, day: undefined, log: undefined })} />
        <OutcomeFilter state={filters.state} onChange={(s) => onChange({ state: s === 'all' ? undefined : s, log: undefined })} />
        <span className="flex-1" />
        <RunTotal grid={g} />
      </div>

      {answer.cronJobs.length === 0 && answer.unread.length === 0 ? (
        <p className="text-sm text-muted">No CronJob on this cluster that this console may read.</p>
      ) : calendars.length === 0 && unread.length > 0 ? (
        <p className="text-sm text-warn" data-heatmap="unread">
          {unread.length === 1
            ? `${unread[0]?.namespace ?? ''}/${unread[0]?.name ?? ''}: its calendar could not be read.`
            : 'None of these calendars could be read.'}
        </p>
      ) : calendars.length === 0 ? (
        <p className="text-sm text-muted" data-heatmap="no-match">
          No calendar matches these filters.
        </p>
      ) : (
        <Heatmap grid={g} picked={filters.day} onPick={(d) => onChange({ day: d === filters.day ? undefined : d, log: undefined })} label="Runs per day" />
      )}
      {unread.length === 0 ? null : (
        <details className="text-xs text-warn" data-heatmap-unread={unread.length}>
          <summary className={`cursor-pointer ${focusRing}`}>
            {unread.length} calendar{unread.length === 1 ? '' : 's'} could not be read and {unread.length === 1 ? 'is' : 'are'} not
            counted
          </summary>
          <ul className="mt-1 flex flex-col gap-0.5">
            {unread.map((u) => (
              <li key={`${u.namespace}/${u.name}`}>
                <span className="font-mono">
                  {u.namespace}/{u.name}
                </span>
                : {u.detail}
              </li>
            ))}
          </ul>
        </details>
      )}
      {answer.complete ? null : <p className="text-xs text-warn">{answer.note ?? 'The CronJob list was not read to its end.'}</p>}

      {filters.day === undefined ? null : (
        <div className={filters.log === undefined ? '' : 'grid gap-3 lg:grid-cols-2 lg:items-start'} data-log-layout={filters.log === undefined ? 'closed' : 'open'}>
          <DayRuns
            cluster={cluster}
            day={filters.day}
            scope={dayScope(calendars, { ...filters, day: filters.day })}
            state={filters.state}
            log={filters.log}
            onLog={(id) => onChange({ log: id === filters.log ? undefined : id })}
          />
          {filters.log === undefined ? null : (
            <div className="min-w-0 lg:sticky lg:top-4">
              <InlineLog cluster={cluster} id={filters.log} onClose={() => onChange({ log: undefined })} />
            </div>
          )}
        </div>
      )}
    </section>
  );
}

function Select({
  label,
  value,
  options,
  all,
  onChange,
}: {
  label: string;
  value: string | undefined;
  options: readonly string[];
  all: string;
  onChange: (value: string | undefined) => void;
}): ReactNode {
  return (
    <select
      aria-label={label}
      value={value ?? ''}
      onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)}
      className={`min-w-0 max-w-full rounded-md border border-line bg-panel px-2 py-1 text-fg sm:max-w-64 ${focusRing}`}
    >
      <option value="">{all}</option>
      {options.map((o) => (
        <option key={o} value={o}>
          {o}
        </option>
      ))}
    </select>
  );
}

/** The outcome filter, the page's and a CronJob's: all, failed, succeeded, running. */
export function OutcomeFilter({ state, onChange }: { state: StateFilter; onChange: (state: StateFilter) => void }): ReactNode {
  return (
    <div role="group" aria-label="Outcome" className="flex max-w-full flex-wrap overflow-hidden rounded-md border border-line">
      {STATE_FILTERS.map((s) => (
        <button
          key={s}
          type="button"
          aria-pressed={state === s}
          onClick={() => onChange(s)}
          className={`px-2 py-1 capitalize ${state === s ? 'bg-panel-2 text-fg' : 'text-muted'} ${focusRingTight}`}
        >
          {s}
        </button>
      ))}
    </div>
  );
}

/** The runs a heatmap counts under its filter, as the toolbar says them. */
export function RunTotal({ grid }: { grid: Grid }): ReactNode {
  const total = grid.weeks.flat().reduce((n, c) => n + (c?.count ?? 0), 0);
  return (
    <span className="text-muted tabular-nums" data-heatmap-total={total}>
      {total} run{total === 1 ? '' : 's'}
    </span>
  );
}

/** Whether a run is one the outcome filter keeps. */
export const STATE_OF: Readonly<Record<StateFilter, (r: Pick<DayRun, 'status'>) => boolean>> = {
  all: () => true,
  failed: (r) => r.status === 'failed',
  succeeded: (r) => r.status === 'succeeded',
  running: (r) => r.status === 'running',
};

/** One day's runs across the CronJobs that ran; a row unfolds the run's log. */
function DayRuns({
  cluster,
  day,
  scope,
  state,
  log,
  onLog,
}: {
  cluster: string;
  day: string;
  scope: DayScope | undefined;
  state: StateFilter;
  log: string | undefined;
  onLog: (id: string) => void;
}): ReactNode {
  const back = backHere(useLocation());
  const query = useQuery({ ...dayQuery(cluster, day, scope ?? {}), enabled: scope !== undefined });
  let body: ReactNode;
  if (scope === undefined) {
    body = <p className="px-3 py-2 text-muted">No run on {day}.</p>;
  } else if (query.data === undefined) {
    body = query.isError ? <Failure error={query.error} onRetry={() => void query.refetch()} /> : <Pending what={`the runs of ${day}`} />;
  } else {
    const runs = query.data.runs.filter(STATE_OF[state]);
    body = (
      <>
        <table className={`${TABLE} w-full`}>
          <tbody>
            {runs.map((r) => (
              <tr
                key={r.id}
                data-run={r.id}
                onClick={() => onLog(r.id)}
                className={`${ROW} cursor-pointer last:border-b-0 hover:bg-panel-2 ${r.id === log ? 'bg-panel-2' : ''}`}
              >
                <td className={`${bodyCell()} tabular-nums`}>
                  <button
                    type="button"
                    aria-pressed={r.id === log}
                    data-log-toggle={r.id}
                    title={r.id === log ? 'Fold the log' : 'Show the log'}
                    onClick={(e) => {
                      e.stopPropagation();
                      onLog(r.id);
                    }}
                    className={`text-accent hover:underline ${focusRing}`}
                  >
                    {momentOn(r.startedAt, day)}
                  </button>
                </td>
                <td className={bodyCell()}>
                  <Link
                    to={cronJobPagePath(cluster, r.namespace, r.cronJob)}
                    state={back}
                    onClick={(e) => e.stopPropagation()}
                    className={`font-mono text-xs hover:underline ${focusRing}`}
                  >
                    {r.namespace}/{r.cronJob}
                  </Link>
                </td>
                <td className={bodyCell()}>
                  <Pill tone={STATUS_TONE[r.status] ?? ''}>{r.status}</Pill>
                </td>
                <td className={`${bodyCell()} text-muted tabular-nums`}>{r.durationMs === undefined ? '—' : duration(r.durationMs)}</td>
                <td className={`${bodyCell()} text-muted tabular-nums`}>{r.exitCode === undefined ? '—' : `exit ${r.exitCode}`}</td>
                <td className={`${bodyCell()} text-muted tabular-nums`} title="Peak CPU · memory">
                  {r.maxCpuMillicores === undefined ? '' : `${r.maxCpuMillicores}m`}
                  {r.maxMemoryBytes === undefined ? '' : ` · ${bytes(r.maxMemoryBytes)}`}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {runs.length === 0 ? <p className="px-3 py-2 text-muted">No {state === 'all' ? '' : `${state} `}run on {day}.</p> : null}
        {query.data.partial.length === 0 ? null : (
          <p className="px-3 py-2 text-xs text-muted">
            Newest runs only for {query.data.partial.map((p) => `${p.namespace}/${p.name}`).join(', ')}:{' '}
            {query.data.partial.length === 1 ? 'its page has' : 'their pages have'} the rest.
          </p>
        )}
        {query.data.unread.length === 0 ? null : (
          <p className="px-3 py-2 text-xs text-warn">
            Not read: {query.data.unread.map((u) => `${u.namespace}/${u.name} (${u.detail})`).join('; ')}
          </p>
        )}
      </>
    );
  }
  return (
    <section aria-label={`Runs of ${day}`} className="rounded-md border border-line" data-day-runs={day}>
      <h2 className="border-b border-line px-3 py-1.5 text-xs font-medium text-muted">{day} (UTC)</h2>
      {body}
    </section>
  );
}
