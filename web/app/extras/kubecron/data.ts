import { queryOptions } from '@tanstack/react-query';
import {
  ApiError,
  EnvelopeError,
  NotJsonError,
  request,
  type ListResult,
  type Reason,
  type ValueResult,
} from '@kubetower/ui';

/**
 * What the three routes of `kubecron.go` answer, and what the pages make of it.
 *
 * The routes are extension routes and do not speak the core's envelope, so this
 * module is where their answers become the console's own shapes: a `ListResult`
 * for the CronJobs, a `ValueResult` for one CronJob and for one run. Every
 * branch on a failure is on the route's `code`, never on its sentence.
 *
 * Times are ISO strings and absent when unknown; durations are milliseconds and
 * absent when nobody recorded them. Absent is never zero here: a run that took
 * no time and a run nobody timed are different answers.
 */

export const KEY = 'kubecron';

export type Verdict = 'ok' | 'missed' | 'suspended' | 'unknown';

export interface Availability {
  ok: boolean;
  detail?: string;
}

/** The collector's standing for this request. `absent` is the ordinary case and
 *  the one the page answers with install instructions. */
export interface CollectorState {
  /** `misconfigured` is KubeTower's own settings for the collector, not the
   *  collector: said in its own words, never as "a collector is here". */
  state: 'absent' | 'unusable' | 'misconfigured' | 'ok';
  detail?: string;
  /** How the collector was reached: named by the environment or this
   *  cluster's setting, or found by its label. `source` says it. */
  via?: 'configuration' | 'label';
  source?: string;
  /** The Service the environment or the setting names, when one does. */
  configured?: string;
  /** Every labeled Service, when there are several and none was chosen. */
  candidates?: string[];
  /** Each step, in order: one line each. */
  checks?: CollectorCheck[];
  where?: string;
  observedSince?: string;
  metricsEnabled?: boolean;
  runDays?: number;
  logDays?: number;
}

/** One step of finding and reading the collector: the collector, its
 *  Secret, reaching it, its token. */
export interface CollectorCheck {
  step: 'collector' | 'secret' | 'reach' | 'token';
  state: 'ok' | 'missing' | 'several' | 'denied' | 'failed' | 'refused' | 'skipped';
  detail: string;
  /** What was looked for, namespace/name, and where that came from. */
  value?: string;
  source?: 'environment' | 'setting' | 'default';
}

export interface RetainedRun {
  name: string;
  status: string;
  class?: string;
  startedAt?: string;
  durationMs?: number;
}

export interface Outcome {
  runs: number;
  failed: number;
  lastFailure?: string;
  avgDurationMs?: number;
  maxDurationMs?: number;
  /** The last three finished runs failed: now, not the window. */
  failing?: boolean;
}

export interface CronJob {
  namespace: string;
  name: string;
  schedule: string;
  timeZone?: string;
  suspended: boolean;
  verdict: Verdict;
  unresolved?: string;
  dueAt?: string;
  lateMs?: number;
  nextAt?: string;
  lastScheduleTime?: string;
  lastSuccessfulTime?: string;
  activeCount: number;
  runs: RetainedRun[];
  history?: Outcome;
}

export interface Counts {
  total: number;
  missed: number;
  unknown: number;
  suspended: number;
  active: number;
  failing: number;
  namespaces: number;
}

export interface Board {
  collectedAt: string;
  complete: boolean;
  note?: string;
  retained: Availability;
  history: Availability;
  collector: CollectorState;
  counts: Counts;
}

export interface Day {
  day: string;
  total: number;
  succeeded: number;
  running: number;
  failed: number;
}

export interface Daily {
  windowDays: number;
  observedSince?: string;
  days: Day[];
}

export interface RunRecord {
  id: string;
  pod: string;
  node?: string;
  image?: string;
  trigger: string;
  startedAt: string;
  finishedAt?: string;
  status: 'running' | 'succeeded' | 'failed' | string;
  exitCode?: number;
  retryCount: number;
  logSizeBytes: number;
  durationMs?: number;
  avgCpuMillicores?: number;
  maxCpuMillicores?: number;
  avgMemoryBytes?: number;
  maxMemoryBytes?: number;
}

export interface RunPage {
  runs: RunRecord[];
  nextCursor?: string;
  observedSince?: string;
}

export interface Detail {
  cronJob: CronJob;
  collector: CollectorState;
  daily?: Daily;
  dailyNote?: string;
  runs?: RunPage;
  runsNote?: string;
}

export interface RunLog {
  lines: { at: string; text: string }[];
  sizeBytes: number;
  expired: boolean;
  truncated: boolean;
  running: boolean;
}

export interface Samples {
  samples: { at: string; cpuMillicores: number; memoryBytes: number }[];
  intervalSeconds: number;
  avgCpuMillicores?: number;
  maxCpuMillicores?: number;
  avgMemoryBytes?: number;
  maxMemoryBytes?: number;
  metricsEnabled: boolean;
}

export interface RunAnswer {
  run: RunRecord;
  namespace: string;
  cronJob: string;
  collector: CollectorState;
  log?: RunLog;
  logNote?: string;
  /** Why the log was withheld without asking the collector: `refused` when the
   *  cluster says the person may not `get pods/log` in the run's namespace,
   *  `unreviewed` when that could not be asked. Absent when
   *  the collector was asked, and `logNote` is then its answer. */
  logState?: 'refused' | 'unreviewed';
  samples?: Samples;
  samplesNote?: string;
  /** Whether the run's pod is still in the cluster, for the person; absent when
   *  that could not be asked. */
  podExists?: boolean;
  /** The pod that still exists: the run may name its Job rather than its pod. */
  livePod?: string;
  /** The run's Job while it is in the cluster, for its page in the console;
   *  absent once it is gone. */
  job?: string;
}

/** What `/jobs/…` and `/pods/…` answer (`owner.go`): the CronJob that
 *  started the object, and the collector's runs of it. */
export interface OwnerAnswer {
  cronJob?: { namespace: string; name: string };
  job?: string;
  /** Why there is no CronJob or no run, lower-case, to sit inside a sentence. */
  note?: string;
  collector: CollectorState;
  runs: RunRecord[];
  runsNote?: string;
}

interface Problem {
  code?: unknown;
  message?: unknown;
  detail?: unknown;
}

/** The route's codes that are facts rather than failures to ask. Spelled as
 *  `kubecron.go`'s constants spell them. */
const ABSENT_CODES = ['cronjob_not_found', 'run_not_found', 'no_collector', 'object_not_found'] as const;

const base = (cluster: string) => `/x/${encodeURIComponent(cluster)}/${KEY}/`;

export function boardPath(cluster: string): string {
  return base(cluster);
}

export function detailPath(cluster: string, namespace: string, name: string, before?: string, day?: string): string {
  const path = `${base(cluster)}cronjobs/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
  const query = new URLSearchParams();
  if (before !== undefined && before !== '') query.set('before', before);
  // One day of the calendar, YYYY-MM-DD.
  if (day !== undefined && day !== '') query.set('day', day);
  const text = query.toString();
  return text === '' ? path : `${path}?${text}`;
}

/** The diagnosis the Settings tile draws. */
export function statusPath(cluster: string): string {
  return `${base(cluster)}status`;
}

export async function getStatus(cluster: string, signal?: AbortSignal): Promise<CollectorState> {
  let body: { collector?: CollectorState };
  try {
    body = await request<{ collector?: CollectorState }>(statusPath(cluster), signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (typeof body.collector !== 'object') {
    throw new EnvelopeError('KubeCron answered its status without a collector', body);
  }
  return body.collector;
}

export function runPath(cluster: string, id: string): string {
  return `${base(cluster)}runs/${encodeURIComponent(id)}`;
}

export interface BoardAnswer {
  board: Board;
  list: ListResult<CronJob>;
}

export async function getBoard(cluster: string, signal?: AbortSignal): Promise<BoardAnswer> {
  let body: Board & { cronJobs?: unknown };
  try {
    body = await request<Board & { cronJobs?: unknown }>(boardPath(cluster), signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (!Array.isArray(body.cronJobs) || typeof body.complete !== 'boolean' || typeof body.collector !== 'object') {
    throw new EnvelopeError('KubeCron answered without a cronJobs array, a complete flag and a collector', body);
  }
  const items = body.cronJobs as CronJob[];
  if (items.length > 0) return { board: body, list: { state: 'ok', items } };
  if (!body.complete) {
    return {
      board: body,
      list: {
        state: 'unreadable',
        reason: {
          code: 'truncated',
          message: 'The CronJob list stopped before any was read, so this is not an answer about the cluster.',
          ...(body.note === undefined ? {} : { detail: body.note }),
          scope: { cluster, resource: 'cronjobs', verb: 'list' },
          retryable: true,
        },
      },
    };
  }
  return {
    board: body,
    list: {
      state: 'empty',
      items: [],
      reason: { code: 'empty_scope', message: 'This cluster has no CronJob this console may read.' },
    },
  };
}

export async function getDetail(
  cluster: string,
  namespace: string,
  name: string,
  before: string | undefined,
  signal?: AbortSignal,
  day?: string,
): Promise<ValueResult<Detail>> {
  try {
    const body = await request<Detail>(detailPath(cluster, namespace, name, before, day), signal === undefined ? {} : { signal });
    if (typeof body.cronJob !== 'object' || body.cronJob === null) {
      throw new EnvelopeError('KubeCron answered a CronJob without its cronJob', body);
    }
    return { state: 'ok', data: body };
  } catch (error) {
    const absent = absentOf(error, cluster, 'cronjobs');
    if (absent !== undefined) return absent;
    throw said(error);
  }
}

export async function getRun(cluster: string, id: string, signal?: AbortSignal): Promise<ValueResult<RunAnswer>> {
  try {
    const body = await request<RunAnswer>(runPath(cluster, id), signal === undefined ? {} : { signal });
    if (typeof body.run !== 'object' || body.run === null) {
      throw new EnvelopeError('KubeCron answered a run without its run', body);
    }
    return { state: 'ok', data: body };
  } catch (error) {
    const absent = absentOf(error, cluster, 'runs');
    if (absent !== undefined) return absent;
    throw said(error);
  }
}

/** The object kinds the tab asks about by owner; a CronJob uses its own route. */
export type OwnedKind = 'jobs' | 'pods';

export function ownerPath(cluster: string, kind: OwnedKind, namespace: string, name: string): string {
  return `${base(cluster)}${kind}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

export async function getOwner(
  cluster: string,
  kind: OwnedKind,
  namespace: string,
  name: string,
  signal?: AbortSignal,
): Promise<ValueResult<OwnerAnswer>> {
  try {
    const body = await request<OwnerAnswer>(ownerPath(cluster, kind, namespace, name), signal === undefined ? {} : { signal });
    if (!Array.isArray(body.runs)) {
      throw new EnvelopeError('KubeCron answered an object without its runs', body);
    }
    return { state: 'ok', data: body };
  } catch (error) {
    const absent = absentOf(error, cluster, kind);
    if (absent !== undefined) return absent;
    throw said(error);
  }
}

export function ownerQuery(cluster: string, kind: OwnedKind, namespace: string, name: string) {
  return queryOptions({
    queryKey: [KEY, 'owner', cluster, kind, namespace, name] as const,
    queryFn: ({ signal }) => getOwner(cluster, kind, namespace, name, signal),
  });
}

/** A refusal that is a fact (not found, no collector) is `absent`, with the
 *  route's sentence. Anything else is a failure to ask and stays one. */
function absentOf(error: unknown, cluster: string, resource: string): ValueResult<never> | undefined {
  if (!(error instanceof ApiError) || error instanceof NotJsonError) return undefined;
  const problem = error.body as Problem | undefined;
  const code = problem?.code;
  if (!ABSENT_CODES.some((c) => c === code)) return undefined;
  const reason: Reason = {
    // Only a missing collector is something to install. A missing CronJob,
    // Job, Pod or run is `empty_scope` - nothing of that name this console may
    // read - which `View.tsx` titles "Not found".
    code: code === 'no_collector' ? 'not_installed' : 'empty_scope',
    message: typeof problem?.message === 'string' && problem.message !== '' ? problem.message : 'Not found.',
    scope: { cluster, resource, verb: 'get' },
    retryable: false,
  };
  return { state: 'absent', reason };
}

/**
 * A plain-text 404 at this address is the console's gate, never the route:
 * every refusal `kubecron.go` writes is JSON with a code. Every extra is off
 * until it is turned on, on every profile: in Settings > Extra, whose
 * write is registered with one replica - in a cluster for a person holding
 * `use` on the extra, once the chart's values name it (`internal/api/router.go`).
 * The fallback: the gate's own sentence, the `NotJsonError` excerpt, is shown
 * when there is one.
 */
const GATE_REFUSED =
  'KubeCron is not enabled for this cluster in this console. Turn it on in Settings > Extra; ' +
  "where the console runs inside a cluster, the chart's values must name it and you need the verb use on " +
  'extras.kubetower.dev/kubecron.';

function said(error: unknown): unknown {
  if (error instanceof NotJsonError && error.status === 404) return new ApiError(404, error.excerpt ?? GATE_REFUSED);
  if (!(error instanceof ApiError) || error instanceof NotJsonError || error instanceof EnvelopeError) return error;
  const problem = error.body as Problem | undefined;
  if (typeof problem?.message !== 'string' || problem.message === '') return error;
  const detail = typeof problem.detail === 'string' && problem.detail !== '' ? ` ${problem.detail}` : '';
  return new ApiError(error.status, `${problem.message}${detail}`, error.body);
}

export function boardQuery(cluster: string) {
  return queryOptions({
    queryKey: [KEY, 'board', cluster] as const,
    queryFn: ({ signal }) => getBoard(cluster, signal),
  });
}

export function detailQuery(cluster: string, namespace: string, name: string, before: string | undefined, day?: string) {
  return queryOptions({
    queryKey: [KEY, 'detail', cluster, namespace, name, before ?? '', day ?? ''] as const,
    queryFn: ({ signal }) => getDetail(cluster, namespace, name, before, signal, day),
  });
}

/** The runs on either side of one, in a list read newest first: older is the
 *  next row, newer the one before. Undefined at either end, or when the
 *  run is not in the list read. */
export function neighbours(runs: readonly { id: string }[], id: string): { older?: string; newer?: string } {
  const at = runs.findIndex((r) => r.id === id);
  if (at < 0) return {};
  const out: { older?: string; newer?: string } = {};
  const older = runs[at + 1];
  const newer = at > 0 ? runs[at - 1] : undefined;
  if (older !== undefined) out.older = older.id;
  if (newer !== undefined) out.newer = newer.id;
  return out;
}

/** What `/daily` answers (`heatmap.go`): every visible
 *  CronJob's calendar, and those that could not be read, each with why. */
export interface HeatmapAnswer {
  collector: CollectorState;
  complete: boolean;
  note?: string;
  /** The window asked for, in days, ending today (UTC). */
  windowDays: number;
  /** How far back the collector keeps run records: older is "not recorded". */
  runDays?: number;
  observedSince?: string;
  cronJobs: { namespace: string; name: string; days: Day[] }[];
  unread: { namespace: string; name: string; detail: string }[];
}

/** One run of `/days/{day}`, with the CronJob it belongs to. */
export interface DayRun extends RunRecord {
  namespace: string;
  cronJob: string;
}

/** What `/days/{day}` answers: that day's runs across the CronJobs asked. */
export interface DayAnswer {
  day: string;
  collector: CollectorState;
  runs: DayRun[];
  /** CronJobs that ran more that day than one page holds: only their newest. */
  partial: { namespace: string; name: string }[];
  unread: { namespace: string; name: string; detail: string }[];
}

export function heatmapPath(cluster: string): string {
  return `${base(cluster)}daily`;
}

/** Which CronJobs a day is read across: named, one namespace's, or (empty)
 *  every visible one. */
export interface DayScope {
  cronJobs?: readonly string[];
  namespace?: string;
}

export function dayPath(cluster: string, day: string, scope: DayScope): string {
  const query = new URLSearchParams();
  for (const c of scope.cronJobs ?? []) query.append('cronjob', c);
  if (scope.namespace !== undefined) query.set('namespace', scope.namespace);
  const text = query.toString();
  return `${base(cluster)}days/${encodeURIComponent(day)}${text === '' ? '' : `?${text}`}`;
}

export async function getHeatmap(cluster: string, signal?: AbortSignal): Promise<HeatmapAnswer> {
  let body: HeatmapAnswer;
  try {
    body = await request<HeatmapAnswer>(heatmapPath(cluster), signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (!Array.isArray(body.cronJobs) || !Array.isArray(body.unread) || typeof body.collector !== 'object') {
    throw new EnvelopeError('KubeCron answered its heatmap without cronJobs, unread and a collector', body);
  }
  return body;
}

export async function getDay(cluster: string, day: string, scope: DayScope, signal?: AbortSignal): Promise<DayAnswer> {
  let body: DayAnswer;
  try {
    body = await request<DayAnswer>(dayPath(cluster, day, scope), signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (!Array.isArray(body.runs) || !Array.isArray(body.unread)) {
    throw new EnvelopeError('KubeCron answered a day without its runs', body);
  }
  return body;
}

export function heatmapQuery(cluster: string) {
  return queryOptions({
    queryKey: [KEY, 'heatmap', cluster] as const,
    queryFn: ({ signal }) => getHeatmap(cluster, signal),
  });
}

export function dayQuery(cluster: string, day: string, scope: DayScope) {
  return queryOptions({
    queryKey: [KEY, 'day', cluster, day, scope.namespace ?? '', ...(scope.cronJobs ?? [])] as const,
    queryFn: ({ signal }) => getDay(cluster, day, scope, signal),
  });
}

/** The console's address of a run's log, full-page. Three
 *  segments under the page, so no namespace can be mistaken for it. */
export function logPagePath(cluster: string, id: string): string {
  return `/c/${encodeURIComponent(cluster)}/x/${KEY}/runs/${encodeURIComponent(id)}/log`;
}

/**
 * The history state every way into a run's log carries: the log page offers
 * Back only on an entry the console itself opened. A key alone is not proof -
 * the sign-in page replaces history index 0 with a keyed entry, and Back from
 * there leaves the console.
 */
export const FROM_CONSOLE: Readonly<Record<string, true>> = { 'kubecron.fromConsole': true };

export function openedFromConsole(state: unknown): boolean {
  return typeof state === 'object' && state !== null && (state as Record<string, unknown>)['kubecron.fromConsole'] === true;
}

/** Where a CronJob's page was opened from, carried in its history state. */
const BACK = 'kubecron.back';

/**
 * The state a link into a CronJob's page carries: the address it was followed
 * from, so the page's ← returns there - the KubeCron page with its filters,
 * or an object's tab: a plain arrow back.
 */
export function backHere(location: { pathname: string; search: string }): Record<string, unknown> {
  return { ...FROM_CONSOLE, [BACK]: `${location.pathname}${location.search}` };
}

/** The address a CronJob's page returns to, when the console opened it. */
export function backOf(state: unknown): string | undefined {
  if (typeof state !== 'object' || state === null) return undefined;
  const back = (state as Record<string, unknown>)[BACK];
  // Only an address inside the console: a path, never a scheme or a host.
  return typeof back === 'string' && back.startsWith('/') && !back.startsWith('//') && !back.includes('\\') ? back : undefined;
}

/**
 * Where a CronJob's page goes once the CronJob is gone: back where it was
 * opened from, unless that is the console's own page of this same CronJob,
 * which reads absent too (from the CronJob's KubeCron tab, a Delete would
 * go from one dead page to another). KubeCron
 * otherwise, as when the page was not opened from the console.
 */
export function leaveFor(state: unknown, cluster: string, namespace: string, name: string): string {
  const back = backOf(state);
  return back === undefined || isCronJobObjectPage(back, namespace, name) ? kubeCronPagePath(cluster) : back;
}

/** Whether an address is the console's page of this CronJob: `/c/<cluster>/cronjobs/<namespace>/<name>`. */
function isCronJobObjectPage(address: string, namespace: string, name: string): boolean {
  const path = address.split(/[?#]/)[0] ?? '';
  let parts: string[];
  try {
    parts = path
      .split('/')
      .filter((part) => part !== '')
      .map((part) => decodeURIComponent(part));
  } catch {
    return false;
  }
  if (parts.length !== 5 || parts[0] !== 'c') return false;
  const kind = (parts[2] ?? '').toLowerCase().split('.')[0];
  return (kind === 'cronjobs' || kind === 'cronjob') && parts[3] === namespace && parts[4] === name;
}

/** The console's address of the KubeCron page. */
export function kubeCronPagePath(cluster: string): string {
  return `/c/${encodeURIComponent(cluster)}/x/${KEY}`;
}

/** The console's address of one CronJob's KubeCron page. */
export function cronJobPagePath(cluster: string, namespace: string, name: string): string {
  return `/c/${encodeURIComponent(cluster)}/x/${KEY}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

export function runQuery(cluster: string, id: string) {
  return queryOptions({
    queryKey: [KEY, 'run', cluster, id] as const,
    queryFn: ({ signal }) => getRun(cluster, id, signal),
  });
}
