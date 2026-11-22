import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link, useLocation, useNavigate } from 'react-router';
import { LogsButton, focusRing, focusRingTight, type ExtensionTabProps, btnToggle } from '@kubetower/ui';

import { backHere, cronJobPagePath, detailQuery, ownerQuery, runQuery, type CollectorState, type Detail, type RunRecord } from './data';
import { Heatmap } from './Heatmap';
import { dayOfInstant, grid, knownFrom, sumDays, todayUTC } from './calendar';
import { momentOn } from './format';
import { CollectorStanding, sentence } from './InstallCollector';
import { VerdictPill } from './KubeCron';
import { InlineLog } from './LogPage';
import { RunBody } from './RunView';
import { View } from './View';

/**
 * KubeCron in an object's panel, on a CronJob, a Job or a Pod:
 *
 * - **Every one of them draws the heatmap of the whole CronJob**, not of the
 *   object alone.
 * - **A Pod or a Job** opens on its own day and its own run: exit code, CPU,
 *   memory, and a **Log** button. No log is drawn here; the button opens it
 *   full-page.
 * - **A CronJob** is navigated: pick a day, pick one of its runs.
 *
 * With no collector, the tab says what to install, as the page does. An object
 * no CronJob started says so in a sentence, never as an empty history.
 */
export default function KubeCronTab({ cluster, kind, namespace, name }: ExtensionTabProps): ReactNode {
  if (namespace === undefined || namespace === '') {
    return <p className="text-sm text-muted">KubeCron follows namespaced objects only.</p>;
  }
  const k = kind.toLowerCase();
  if (k === 'cronjobs' || k === 'cronjob') {
    return (
      <div data-kubecron-tab="cronjob">
        <CronJobTile cluster={cluster} namespace={namespace} name={name} />
      </div>
    );
  }
  if (k === 'jobs' || k === 'job') return <OnOwned cluster={cluster} kind="jobs" namespace={namespace} name={name} />;
  return <OnOwned cluster={cluster} kind="pods" namespace={namespace} name={name} />;
}

function OnOwned({
  cluster,
  kind,
  namespace,
  name,
}: {
  cluster: string;
  kind: 'jobs' | 'pods';
  namespace: string;
  name: string;
}): ReactNode {
  const owner = useQuery(ownerQuery(cluster, kind, namespace, name));
  return (
    <View query={owner} what="what KubeCron knows about this object">
      {(o) => {
        if (o.cronJob === undefined) {
          return (
            <p data-kubecron-tab="none" className="text-sm text-muted">
              {sentence(o.note ?? 'no CronJob started this object')}
            </p>
          );
        }
        if (o.collector.state !== 'ok') {
          return (
            <div className="flex flex-col gap-3 text-sm" data-kubecron-tab={kind}>
              <CronJobLink cluster={cluster} namespace={o.cronJob.namespace} name={o.cronJob.name} />
              {/* No history to draw: the pod's own live log is still the console's. */}
              {kind === 'pods' ? (
                <span>
                  <LogsButton cluster={cluster} kind="pods" namespace={namespace} name={name} />
                </span>
              ) : null}
              <Collector cluster={cluster} state={o.collector} />
            </div>
          );
        }
        return (
          <div data-kubecron-tab={kind}>
            <CronJobTile
              cluster={cluster}
              namespace={o.cronJob.namespace}
              name={o.cronJob.name}
              own={o.runs}
              ownNote={o.runs.length === 0 ? sentence(o.runsNote ?? 'the collector holds no run of this object') : undefined}
              ownWord={kind === 'pods' ? 'this pod' : 'this job'}
            />
          </div>
        );
      }}
    </View>
  );
}

/**
 * One CronJob's heatmap, a day's runs, and one run's figures. `own` are the
 * runs of the object the tab is on, newest first: the newest is opened on its
 * day, and each is marked in the day's list.
 */
export function CronJobTile({
  cluster,
  namespace,
  name,
  own = NONE,
  ownNote,
  ownWord = '',
}: {
  cluster: string;
  namespace: string;
  name: string;
  own?: readonly RunRecord[];
  ownNote?: string | undefined;
  ownWord?: string;
}): ReactNode {
  const first = own[0];
  // The day and run picked are kept on this history entry, so Back from a
  // run's log lands on them rather than on the tab's opening state.
  const location = useLocation();
  const navigate = useNavigate();
  const key = `${cluster}/${namespace}/${name}`;
  // The pick on this history entry, or the one left when the log went full
  // page: the drawer's own address is rewritten on the way back, without
  // the entry's state, and the pick would be lost with it.
  const [kept] = useState(() => keptPick(location.state, key) ?? returnTicket(key));
  useEffect(() => {
    tickets.delete(key);
  }, [key]);
  const [day, setDay] = useState<string | undefined>(kept !== undefined ? kept.day : first === undefined ? undefined : dayOfInstant(first.startedAt));
  const [picked, setPicked] = useState<string | undefined>(kept !== undefined ? kept.run : first?.id);
  // The picked run's log, unfolded under it: kept with the pick, so the full
  // page's × lands on it open.
  const [logOpen, setLogOpen] = useState(kept?.log === true);
  const pick = (next: { day: string | undefined; run: string | undefined; log?: boolean }) => {
    setDay(next.day);
    setPicked(next.run);
    setLogOpen(next.log === true);
    const state: unknown = location.state;
    void navigate(
      { pathname: location.pathname, search: location.search, hash: location.hash },
      { replace: true, state: { ...(typeof state === 'object' && state !== null ? state : {}), [KEPT]: { key, ...next } } },
    );
  };
  // The heatmap stays on screen while another day's runs are read.
  const detail = useQuery({ ...detailQuery(cluster, namespace, name, undefined, day), placeholderData: keepPreviousData });
  const ownIds = useMemo(() => new Set(own.map((r) => r.id)), [own]);
  return (
    <View query={detail} what="this CronJob's KubeCron history">
      {(d) => (
        <div className="flex flex-col gap-3 text-sm">
          <div className="flex flex-wrap items-center gap-2">
            <CronJobLink cluster={cluster} namespace={namespace} name={name} />
            <VerdictPill cronJob={d.cronJob} />
          </div>
          <Collector cluster={cluster} state={d.collector} />
          {d.collector.state === 'ok' ? (
            <>
              <TileHeatmap
                detail={d}
                day={day}
                onPick={(next) => pick({ day: next === day ? undefined : next, run: undefined })}
              />
              {ownNote === undefined ? null : <p className="text-xs text-muted">{ownNote}</p>}
              <DayRuns
                detail={d}
                day={day}
                fetching={detail.isPlaceholderData}
                picked={picked}
                onPick={(run) => pick({ day, run, log: logOpen })}
                own={ownIds}
                ownWord={ownWord}
                page={cronJobPagePath(cluster, namespace, name)}
              />
              {picked === undefined ? null : (
                <PickedRun
                  cluster={cluster}
                  id={picked}
                  logOpen={logOpen}
                  onLog={() => pick({ day, run: picked, log: !logOpen })}
                  onFullPage={() => tickets.set(key, { day, run: picked, log: true, at: Date.now() })}
                />
              )}
            </>
          ) : null}
        </div>
      )}
    </View>
  );
}

const NONE: readonly RunRecord[] = [];

/** Where a tab keeps its pick on the history entry. */
const KEPT = 'kubecron.pick';

/**
 * The pick a tab leaves when its log goes full page, taken back by the tab
 * on its way back - once: the next mount of that tab forgets it. Kept in
 * memory, by the object, because the core rewrites a drawer's address on the
 * way back and the history entry's state goes with it.
 */
const tickets = new Map<string, { day: string | undefined; run: string | undefined; log?: true; at: number }>();

/** How long a ticket waits for its way back: a reader who left the full page
 *  some other way is not handed that pick on a later, unrelated visit. */
const TICKET_MS = 120_000;

export function returnTicket(key: string): { day: string | undefined; run: string | undefined; log?: true } | undefined {
  const ticket = tickets.get(key);
  if (ticket === undefined || Date.now() - ticket.at > TICKET_MS) return undefined;
  return { day: ticket.day, run: ticket.run, ...(ticket.log === true ? { log: true } : {}) };
}

/** The pick kept on this history entry for this CronJob, if any. */
export function keptPick(state: unknown, key: string): { day: string | undefined; run: string | undefined; log?: true } | undefined {
  if (typeof state !== 'object' || state === null) return undefined;
  const kept = (state as Record<string, unknown>)[KEPT];
  if (typeof kept !== 'object' || kept === null) return undefined;
  const k = kept as { key?: unknown; day?: unknown; run?: unknown; log?: unknown };
  if (k.key !== key) return undefined;
  return { day: typeof k.day === 'string' ? k.day : undefined, run: typeof k.run === 'string' ? k.run : undefined, ...(k.log === true ? { log: true } : {}) };
}

function CronJobLink({ cluster, namespace, name }: { cluster: string; namespace: string; name: string }): ReactNode {
  return (
    <Link
      to={cronJobPagePath(cluster, namespace, name)}
      state={backHere(useLocation())}
      title="Open in KubeCron"
      className={`font-mono text-xs text-accent hover:underline ${focusRing}`}
    >
      {namespace}/{name}
    </Link>
  );
}

function TileHeatmap({ detail, day, onPick }: { detail: Detail; day: string | undefined; onPick: (day: string) => void }): ReactNode {
  const daily = detail.daily;
  if (daily === undefined) return <p className="text-warn">{detail.dailyNote ?? 'The calendar could not be read.'}</p>;
  const today = todayUTC();
  const g = grid({
    byDay: sumDays([daily]),
    windowDays: daily.windowDays,
    today,
    known: knownFrom(today, daily.observedSince, detail.collector.runDays),
    state: 'all',
  });
  return <Heatmap grid={g} picked={day} onPick={onPick} label={`${detail.cronJob.name}, last ${daily.windowDays} days`} />;
}

const DOT: Readonly<Record<string, string>> = { succeeded: 'bg-ok', failed: 'bg-bad', running: 'bg-warn' };

/** The day's runs, or the newest ones when no day is picked, as chips. */
function DayRuns({
  detail,
  day,
  fetching,
  picked,
  onPick,
  own,
  ownWord,
  page: pageHref,
}: {
  detail: Detail;
  day: string | undefined;
  fetching: boolean;
  picked: string | undefined;
  onPick: (id: string) => void;
  own: ReadonlySet<string>;
  ownWord: string;
  /** The CronJob's own page, where a day too long for one page goes on. */
  page: string;
}): ReactNode {
  const location = useLocation();
  if (fetching) return <p className="text-xs text-muted">Reading {day ?? 'the newest runs'}…</p>;
  const page = detail.runs;
  if (page === undefined) return <p className="text-warn">{detail.runsNote ?? 'The runs could not be read.'}</p>;
  const runs = day === undefined ? page.runs.slice(0, 12) : page.runs;
  if (runs.length === 0) {
    return <p className="text-xs text-muted">{day === undefined ? 'No run recorded yet.' : `No run on ${day}.`}</p>;
  }
  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs text-muted">{day === undefined ? 'Newest' : `${day} (UTC)`}</span>
      <ul aria-label="Runs" className="flex flex-wrap gap-1">
        {runs.map((r) => (
          <li key={r.id} data-run={r.id} data-own={own.has(r.id) ? '' : undefined}>
            <button
              type="button"
              aria-pressed={picked === r.id}
              onClick={() => onPick(r.id)}
              title={`${r.status}${r.exitCode === undefined ? '' : `, exit ${r.exitCode}`}`}
              className={`${btnToggle(picked === r.id)} tabular-nums`}
            >
              <span className={`inline-block size-2 rounded-full ${DOT[r.status] ?? 'bg-muted'}`} />
              {momentOn(r.startedAt, day)}
              {own.has(r.id) && ownWord !== '' ? <span className="text-accent">· {ownWord}</span> : null}
            </button>
          </li>
        ))}
        {day !== undefined && page.nextCursor !== undefined && page.nextCursor !== '' ? (
          <li>
            <Link
              to={`${pageHref}?day=${day}`}
              state={backHere(location)}
              data-more-runs=""
              title="This day has more runs than one page: the rest are on the CronJob's page"
              className={`flex rounded-md px-1.5 py-0.5 text-xs text-accent hover:underline ${focusRingTight}`}
            >
              more…
            </Link>
          </li>
        ) : null}
      </ul>
    </div>
  );
}

/** The picked run, and its log unfolded under it: a tab is too narrow for a
 *  log beside it. */
function PickedRun({
  cluster,
  id,
  logOpen,
  onLog,
  onFullPage,
}: {
  cluster: string;
  id: string;
  logOpen: boolean;
  onLog: () => void;
  onFullPage: () => void;
}): ReactNode {
  const run = useQuery(runQuery(cluster, id));
  return (
    <>
      <section aria-label="One run" className="rounded-lg border border-line bg-panel p-3">
        <View query={run} what="this run">
          {(r) => <RunBody cluster={cluster} answer={r} logOpen={logOpen} onLog={onLog} />}
        </View>
      </section>
      {logOpen ? <InlineLog cluster={cluster} id={id} onClose={onLog} onFullPage={onFullPage} /> : null}
    </>
  );
}

function Collector({ cluster, state }: { cluster: string; state: CollectorState }): ReactNode {
  return <CollectorStanding cluster={cluster} state={state} />;
}
