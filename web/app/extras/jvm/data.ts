import { queryOptions, skipToken, useQuery, useQueryClient } from '@tanstack/react-query';
import { ApiError, EnvelopeError, NotJsonError, download, request, type Downloaded, type ExtensionCell } from '@kubetower/ui';

/**
 * What the routes of `extras/jvm` answer (`routes.go`), and how the page reads
 * it. Every failure is a problem with a `code` the page branches on, never a
 * sentence it parses.
 *
 * Everything here is read from the pod spec: nothing is run in a container.
 */

export const KEY = 'jvm';

/** How often the open page reads again: the working sets move, the specs
 *  rarely do. */
export const POLL_MS = 30_000;

/** How many container rows are drawn at once; "Show more" adds as many. A
 *  cluster with a thousand Java pods draws a hundred rows, not a thousand
 *  bars. */
export const ROW_STEP = 100;


/** One reason to think a container runs a JVM, and what it was read from. */
export interface Hint {
  source: 'image' | 'command' | 'env';
  reason: string;
}

/** A memory request or limit, in bytes and as the spec writes it. */
export interface Memory {
  bytes: number;
  text: string;
}

/** How the maximum heap was arrived at. */
export type HeapFrom = 'explicit' | 'percentage' | 'default' | 'small' | 'floor' | 'nolimit';

export interface Heap {
  /** Null with no memory limit and no -Xmx: sized on the node. */
  bytes: number | null;
  /** `bytes` as the page prints it (`1.5 GiB`); empty when null. */
  text: string;
  from: HeapFrom;
  source: string;
}

/** `warning` above 75 % of the limit, `error` when an explicit -Xmx reaches it. */
export type RiskLevel = 'ok' | 'warning' | 'error' | 'nolimit' | 'unknown';

export interface Risk {
  level: RiskLevel;
  percent: number | null;
  sentence: string;
}

export interface Container {
  name: string;
  image: string;
  /** False is "not seen from the spec", never "not Java". */
  java: boolean;
  hints: Hint[];
  request: Memory | null;
  limit: Memory | null;
  /** Null for a container nothing says is Java. */
  heap: Heap | null;
  risk: Risk | null;
  notes: string[];
  /** Its memory now, from metrics-server, on the inventory only. Null is "not
   *  measured", and the inventory's `usage` says why; a measured zero is 0.
   *  Optional because the pod route does not read it. */
  workingSet?: Memory | null;
}

/** The workload a pod belongs to. A Deployment is named through its
 *  ReplicaSet's pod-template-hash, and `replicaSet` keeps the set's name. */
export interface Owner {
  kind: string;
  name: string;
  replicaSet?: string;
}

/** How the working sets were read. `read` with a container's `workingSet`
 *  null is a container metrics-server has no figure for; every other state
 *  means none has one, and `detail` says why. */
export type UsageState = 'read' | 'absent' | 'refused' | 'unreachable' | 'forbidden' | 'failed';

export interface Usage {
  state: UsageState;
  detail?: string;
  complete: boolean;
  note?: string;
}

export interface Pod {
  namespace: string;
  name: string;
  phase: string;
  java: boolean;
  containers: Container[];
  /** Null for a pod nothing controls. Optional for an answer that predates it. */
  owner?: Owner | null;
}

/** `GET /`: the probable Java pods of the scope, Java containers only. */
export interface Inventory {
  cluster: string;
  namespace: string;
  /** False when narrowed for the person or cut short: then `note` says why. */
  complete: boolean;
  note?: string;
  /** How many running or pending pods were read. */
  read: number;
  /** How many Java pods the read held before the server's cap (2,000, the
   *  worst first) cut `pods`; `complete` is then false and `note` says so. */
  found?: number;
  pods: Pod[];
  unreadable?: number;
  /** Optional for an answer that predates it: then nothing was measured. */
  usage?: Usage;
}

/** The route's address: `/x/{cluster}/jvm/`, with the scope. */
export function inventoryPath(cluster: string, ns: string): string {
  const base = `/x/${encodeURIComponent(cluster)}/${KEY}/`;
  return ns === '' ? base : `${base}?${new URLSearchParams({ ns }).toString()}`;
}

/**
 * A plain-text 404 at this address is the console's gate, never the route:
 * every refusal the route writes is JSON with a code.
 */
const GATE_REFUSED =
  'The JVM Inspector is not enabled for this cluster in this console. Turn it on in Settings > Extra; ' +
  "where the console runs inside a cluster, the chart's values must name it and you need the verb use on " +
  'extras.kubetower.dev/jvm.';

interface Problem {
  message?: unknown;
  detail?: unknown;
}

/** The route's own sentence in place of the transport's, where it sent one. */
export function said(error: unknown): unknown {
  if (error instanceof NotJsonError && error.status === 404) return new ApiError(404, error.excerpt ?? GATE_REFUSED);
  if (!(error instanceof ApiError) || error instanceof NotJsonError || error instanceof EnvelopeError) return error;
  const problem = error.body as Problem | undefined;
  if (typeof problem?.message !== 'string' || problem.message === '') return error;
  const detail = typeof problem.detail === 'string' && problem.detail !== '' ? ` ${problem.detail}` : '';
  return new ApiError(error.status, `${problem.message}${detail}`, error.body);
}

export async function getInventory(cluster: string, ns: string, signal?: AbortSignal): Promise<Inventory> {
  let body: Inventory;
  try {
    body = await request<Inventory>(inventoryPath(cluster, ns), signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (!Array.isArray(body.pods) || typeof body.complete !== 'boolean' || typeof body.read !== 'number') {
    throw new EnvelopeError('The JVM Inspector answered without its pods, its count and whether it is complete', body);
  }
  return body;
}

export function inventoryQuery(cluster: string, ns: string) {
  return queryOptions({
    queryKey: [KEY, cluster, ns] as const,
    queryFn: ({ signal }) => getInventory(cluster, ns, signal),
    staleTime: 10_000,
  });
}

/** The pill's tone for a risk: the console's grades, nothing invented. */
export function riskTone(level: RiskLevel): string | undefined {
  switch (level) {
    case 'ok':
      return 'ok';
    case 'warning':
      return 'warn';
    case 'error':
      return 'bad';
    default:
      return undefined;
  }
}

/** The pill's word for a risk. */
export function riskWord(risk: Risk): string {
  switch (risk.level) {
    case 'error':
      return 'OOMKill';
    case 'warning':
      return 'Tight';
    case 'ok':
      return 'Room';
    case 'nolimit':
      return 'No limit';
    case 'unknown':
      return 'Unknown';
  }
}

/** Risks from the worst: an OOMKill, a tight heap, then what cannot be graded,
 *  and room last. */
export function severity(level: RiskLevel): number {
  switch (level) {
    case 'error':
      return 4;
    case 'warning':
      return 3;
    case 'unknown':
      return 2;
    case 'nolimit':
      return 1;
    case 'ok':
      return 0;
  }
}

/** A pod's worst Java container's risk; `unknown` for one with none graded. */
export function worstLevel(pod: Pod): RiskLevel {
  let worst: RiskLevel | null = null;
  for (const c of pod.containers) {
    if (c.risk !== null && (worst === null || severity(c.risk.level) > severity(worst))) worst = c.risk.level;
  }
  return worst ?? 'unknown';
}

/**
 * Where each mark of a container's memory bar sits, as a share of the track,
 * 0 to 100. The track is the largest of the limit, the heap, the working set
 * and the request, so nothing is drawn off its end; a heap past the limit
 * pushes the limit line in from the end, which is the picture of an OOMKill.
 * Null is "not drawn": no limit, a heap sized on the node, nothing measured.
 */
export interface HeapBar {
  heap: number | null;
  limit: number | null;
  /** The 75 % line, drawn only with a limit. */
  threshold: number | null;
  workingSet: number | null;
  request: number | null;
}

export function heapBar(c: Container): HeapBar {
  const heap = c.heap?.bytes ?? null;
  const limit = c.limit?.bytes ?? null;
  const used = c.workingSet?.bytes ?? null;
  const request = c.request?.bytes ?? null;
  const scale = Math.max(heap ?? 0, limit ?? 0, used ?? 0, request ?? 0);
  const at = (v: number | null) => (v === null || scale <= 0 ? null : Math.min(100, Math.max(0, (v / scale) * 100)));
  return {
    heap: at(heap),
    limit: at(limit),
    threshold: limit === null ? null : at(limit * 0.75),
    workingSet: at(used),
    request: at(request),
  };
}

/** What the page groups its pods by. */
export type Grouping = 'namespace' | 'workload';

export interface Group {
  key: string;
  /** The namespace, or the workload's kind and name. */
  title: string;
  /** Beside the title: the workload's namespace, or nothing. */
  sub: string;
  worst: RiskLevel;
  pods: Pod[];
}

/**
 * The pods in groups, the worst group first, then by name; a pod nothing
 * controls is a group of its own under workloads.
 */
export function groupPods(pods: readonly Pod[], by: Grouping): Group[] {
  const groups = new Map<string, Group>();
  for (const pod of pods) {
    const owner = pod.owner ?? null;
    const key = by === 'namespace' ? pod.namespace : owner === null ? `${pod.namespace}/Pod/${pod.name}` : `${pod.namespace}/${owner.kind}/${owner.name}`;
    let group = groups.get(key);
    if (group === undefined) {
      group =
        by === 'namespace'
          ? { key, title: pod.namespace, sub: '', worst: 'ok', pods: [] }
          : { key, title: owner === null ? `Pod ${pod.name}` : `${owner.kind} ${owner.name}`, sub: pod.namespace, worst: 'ok', pods: [] };
      groups.set(key, group);
    }
    group.pods.push(pod);
    const level = worstLevel(pod);
    if (group.pods.length === 1 || severity(level) > severity(group.worst)) group.worst = level;
  }
  return [...groups.values()].sort(
    (a, b) => severity(b.worst) - severity(a.worst) || a.title.localeCompare(b.title) || a.sub.localeCompare(b.sub),
  );
}

/** Whether a pod answers a search: its name, namespace, workload, or one of
 *  its containers' names or images, ignoring case. */
export function matches(pod: Pod, search: string): boolean {
  const q = search.trim().toLowerCase();
  if (q === '') return true;
  const words = [pod.name, pod.namespace, pod.owner?.name ?? '', ...pod.containers.flatMap((c) => [c.name, c.image])];
  return words.some((w) => w.toLowerCase().includes(q));
}

/** The kinds whose lists draw the Java column: pods, and the workloads a pod
 *  names as its owner (`column.go`'s `columnKinds`). */
export const COLUMN_KINDS = ['pods', 'deployments', 'statefulsets', 'daemonsets', 'replicasets', 'jobs'] as const;

/** The Java column of a list: one pod list for the whole list, from the spec
 *  alone. A refusal rejects, and the list then draws no column. */
export async function javaColumn(
  list: { cluster: string; kind: string },
  signal: AbortSignal,
): Promise<Readonly<Record<string, ExtensionCell>>> {
  const query = new URLSearchParams({ kind: list.kind });
  return request<Record<string, ExtensionCell>>(`/x/${encodeURIComponent(list.cluster)}/${KEY}/column?${query.toString()}`, { signal });
}

// --- the JVM tab on a pod ------------------------------------------------------
//
// The tab's probe reads `GET /pod/{namespace}/{name}` (the spec alone); the
// inspection and the reveal are POSTs that run jcmd in the container through
// the console's exec door, reviewed for the person and recorded. `request`
// sends the console's request header on a POST.

/** `GET /pod/{namespace}/{name}`: every container of one pod, from its spec. */
export interface PodAnswer extends Pod {
  cluster: string;
}

function podPath(cluster: string, namespace: string, name: string): string {
  return `/x/${encodeURIComponent(cluster)}/${KEY}/pod/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

export async function getPod(cluster: string, namespace: string, name: string, signal?: AbortSignal): Promise<PodAnswer> {
  let body: PodAnswer;
  try {
    body = await request<PodAnswer>(podPath(cluster, namespace, name), signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (!Array.isArray(body.containers) || typeof body.java !== 'boolean') {
    throw new EnvelopeError('The JVM Inspector answered without the pod’s containers', body);
  }
  return body;
}

export function podQuery(cluster: string, namespace: string, name: string) {
  return queryOptions({
    queryKey: [KEY, 'pod', cluster, namespace, name] as const,
    queryFn: ({ signal }) => getPod(cluster, namespace, name, signal),
    // One spec per pod looked at, dropped a minute after nothing shows it:
    // the cache is bounded by the pods open, not by the cluster. Nothing
    // polls it, and a window regaining focus does not read it again.
    staleTime: 30_000,
    gcTime: 60_000,
    refetchOnWindowFocus: false,
  });
}

/** One JVM `jcmd -l` listed: its process id and main class, never its arguments. */
export interface ListedJVM {
  pid: number;
  mainClass: string;
}

/** One diagnostic command of the inspection and how it went. */
export interface Section {
  command: string;
  /** `truncated`: the output was cut at the exec's limit and is not read;
   *  `skipped`: not run, the request gone or its budget spent. */
  state: 'ok' | 'unavailable' | 'not-enabled' | 'failed' | 'truncated' | 'skipped';
  sentence?: string;
  truncated?: boolean;
}

export interface Figure {
  bytes: number;
  text: string;
}

export interface KeyFlag {
  name: string;
  value: string;
  text?: string;
}

export interface Summary {
  vm: string;
  version: string;
  major: number;
  uptime: string;
  gc: string;
  flags: KeyFlag[];
}

/** A command-line token; with `secret`, `text` is what precedes a hidden value. */
export interface Arg {
  text: string;
  secret?: string;
}

export interface PropertyRow {
  key: string;
  value: string;
  masked: boolean;
}

/** One part of the memory bar; `start` and `width` are percentages of the track. */
export interface Segment {
  key: string;
  label: string;
  bytes: number;
  text: string;
  start: number;
  width: number;
  used?: { bytes: number; text: string; width: number };
  unattributed?: boolean;
}

export interface Marker {
  key: 'request' | 'limit' | 'workingSet' | 'heapMax';
  label: string;
  bytes: number;
  text: string;
  at: number;
}

export interface MemoryBar {
  source: 'nmt' | 'jvm';
  track: Figure;
  segments: Segment[];
  markers: Marker[];
  committed: Figure | null;
  workingSet: Figure | null;
  workingSetNote?: string;
  heapUsed: Figure | null;
  heapCommitted: Figure | null;
  heapMax: Figure | null;
  metaspaceUsed: Figure | null;
  metaspaceCommitted: Figure | null;
  codeCacheUsed: Figure | null;
  codeCacheSize: Figure | null;
  /** The heap's used and committed parts, in percent of its maximum; null
   *  without a heap or a maximum. */
  heapGauge: { usedPct: number; committedPct: number } | null;
  nmt?: { name: string; reserved: Figure; committed: Figure }[];
  notes: string[];
}

/** `POST /pod/{namespace}/{name}/{container}/inspect`. */
export interface Inspection {
  cluster: string;
  namespace: string;
  pod: string;
  container: string;
  /** `choose`: several JVMs, listed in `jvms`, and nothing else was run. */
  state: 'inspected' | 'choose';
  jvms?: ListedJVM[];
  pid?: number;
  pidFrom?: 'listed' | 'chosen' | 'pid1';
  inherited?: boolean;
  sections?: Section[];
  summary?: Summary;
  memory?: MemoryBar;
  allFlags?: { name: string; value: string }[];
  commandLine?: { jvmArgs: Arg[]; javaCommand: Arg[] };
  properties?: PropertyRow[];
  /** Each garbage collector's work, from PerfCounter.print; absent when it
   *  was not read. */
  gc?: GCWork;
  notes: string[];
}

/** One garbage collector's work since the JVM started (`perf.go`). */
export interface GCCollector {
  name: string;
  /** Collector 0, the young generation's. */
  young: boolean;
  invocations: number;
  seconds: number;
  /** `seconds` as a percentage of the uptime. */
  share: number;
}

export interface GCWork {
  collectors: GCCollector[];
  policy?: string;
  uptime: number;
  /** The time in every collector, as a percentage of the uptime. */
  share: number;
}

/**
 * A failure of the inspection: the route's code, which the tab branches on,
 * and its sentence.
 */
export class InspectProblem extends Error {
  readonly code: string;
  readonly status: number;
  constructor(code: string, message: string, status: number) {
    super(message);
    this.name = 'InspectProblem';
    this.code = code;
    this.status = status;
  }
}

function problemOf(error: unknown): unknown {
  const shown = said(error);
  if (error instanceof ApiError && !(error instanceof NotJsonError) && !(error instanceof EnvelopeError)) {
    const body = error.body as { code?: unknown } | undefined;
    if (typeof body?.code === 'string' && shown instanceof Error) return new InspectProblem(body.code, shown.message, error.status);
  }
  return shown;
}

/** The tooltip of a button that runs jcmd in a container that has none. */
export const NO_JDK_TITLE = 'No JDK tools in this container';

/** `GET .../{container}/tools`: whether jcmd is in the container. A container
 *  without it answers the `no_jcmd` problem instead. */
export interface Tools {
  jcmd: boolean;
}

export function toolsKey(cluster: string, namespace: string, name: string, container: string) {
  return [KEY, 'tools', cluster, namespace, name, container] as const;
}

/** The probe: runs `jcmd -l` once for a container, then is remembered. */
export function toolsQuery(cluster: string, namespace: string, name: string, container: string) {
  return queryOptions({
    queryKey: toolsKey(cluster, namespace, name, container),
    queryFn: async ({ signal }): Promise<Tools> => {
      try {
        return await request<Tools>(`${podPath(cluster, namespace, name)}/${encodeURIComponent(container)}/tools`, { signal });
      } catch (error) {
        throw problemOf(error);
      }
    },
    staleTime: 5 * 60_000,
    retry: false,
  });
}

/**
 * Whether the container is known to have no JDK tools, and a way to say so
 * when an action finds out first. Only an answer of `no_jcmd` counts: a probe
 * that is pending, refused or failed leaves the buttons as they were.
 */
export function useJdkTools(
  cluster: string,
  namespace: string,
  name: string,
  container: string,
): { noJdk: boolean; offTitle: string; attachOff: string | undefined; markNoJdk: (error?: unknown) => void } {
  const client = useQueryClient();
  const enabled = container !== '';
  const query = useQuery({ ...toolsQuery(cluster, namespace, name, container), enabled });
  // What an action learned: the JVM refuses the attach. Never fetched, only set.
  const attach = useQuery({ queryKey: attachKey(cluster, namespace, name, container), queryFn: skipToken, staleTime: Infinity });
  const attachOff = typeof attach.data === 'string' ? attach.data : undefined;
  const noJdk =
    enabled && (query.data?.jcmd === false || (query.error instanceof InspectProblem && query.error.code === 'no_jcmd'));
  return {
    noJdk: noJdk || attachOff !== undefined,
    offTitle: noJdk ? NO_JDK_TITLE : (attachOff ?? NO_JDK_TITLE),
    attachOff,
    markNoJdk: (error) => {
      if (error instanceof InspectProblem && error.code === 'attach_disabled') {
        client.setQueryData<string>(attachKey(cluster, namespace, name, container), error.message);
        return;
      }
      client.setQueryData<Tools>(toolsKey(cluster, namespace, name, container), { jcmd: false });
    },
  };
}

function attachKey(cluster: string, namespace: string, name: string, container: string) {
  return [KEY, 'attach', cluster, namespace, name, container] as const;
}

/** Whether an action failed because jcmd cannot be used here: no jcmd, or a JVM that refuses the attach. */
export function blocksJcmd(error: unknown): boolean {
  return error instanceof InspectProblem && (error.code === 'no_jcmd' || error.code === 'attach_disabled');
}

/** Whether an action failed because the container has no jcmd. */
export function noJcmd(error: unknown): boolean {
  return error instanceof InspectProblem && error.code === 'no_jcmd';
}

function inspectPath(cluster: string, namespace: string, name: string, container: string): string {
  return `${podPath(cluster, namespace, name)}/${encodeURIComponent(container)}/inspect`;
}

/** Runs the inspection: jcmd in `container`, on the person's click only. */
export async function inspect(
  cluster: string,
  namespace: string,
  name: string,
  container: string,
  pid?: number,
): Promise<Inspection> {
  const path = inspectPath(cluster, namespace, name, container);
  let body: Inspection;
  try {
    body = await request<Inspection>(pid === undefined ? path : `${path}?pid=${String(pid)}`, { method: 'POST' });
  } catch (error) {
    throw problemOf(error);
  }
  if (body.state !== 'inspected' && body.state !== 'choose') {
    throw new EnvelopeError('The JVM Inspector answered without saying what it ran', body);
  }
  return body;
}

/** Reads one masked value again: a property by its key, an option by its name. */
export async function reveal(
  cluster: string,
  namespace: string,
  name: string,
  container: string,
  pid: number,
  kind: 'property' | 'option',
  key: string,
): Promise<string> {
  let body: { value?: unknown };
  try {
    body = await request<{ value?: unknown }>(`${inspectPath(cluster, namespace, name, container)}/reveal`, {
      method: 'POST',
      body: { pid, kind, name: key },
    });
  } catch (error) {
    throw problemOf(error);
  }
  if (typeof body.value !== 'string') throw new EnvelopeError('The JVM Inspector answered without the value', body);
  return body.value;
}

// --- the thread dump (`threads.go`) -------------------------------------------

/** The states a thread is counted under, in the answer's order; `VM` is the
 *  JVM's own threads, which have no Java state. */
export type ThreadState = 'RUNNABLE' | 'BLOCKED' | 'WAITING' | 'TIMED_WAITING' | 'NEW' | 'TERMINATED' | 'VM';

export interface LockRef {
  /** The JVM's words: `locked`, `waiting to lock`, `parking to wait for`, `owns`… */
  kind: string;
  id: string;
  class?: string;
  /** The thread holding it, for a lock a thread waits for. */
  heldBy?: string;
}

export interface Frame {
  at: string;
  locks?: LockRef[];
}

export interface JavaThread {
  name: string;
  id?: number;
  daemon: boolean;
  prio?: number;
  state: ThreadState;
  detail?: string;
  status?: string;
  cpu?: string;
  elapsed?: string;
  frames: Frame[];
  framesCut?: number;
  waitingFor?: LockRef;
  holds?: LockRef[];
}

export interface DeadlockLink {
  thread: string;
  waits: string;
  lock?: string;
  class?: string;
  heldBy: string;
}

export interface Deadlock {
  kind: string;
  cycle: DeadlockLink[];
  cycleCut?: boolean;
}

export interface ThreadDump {
  cluster: string;
  namespace: string;
  pod: string;
  container: string;
  /** `choose`: several JVMs, listed in `jvms`, and nothing was dumped. */
  state: 'dumped' | 'choose';
  jvms?: ListedJVM[];
  pid?: number;
  pidFrom?: 'listed' | 'chosen' | 'pid1';
  inherited?: boolean;
  taken?: string;
  vm?: string;
  total?: number;
  daemon?: number;
  counts?: { state: ThreadState; count: number }[];
  threads?: JavaThread[];
  threadsCut?: boolean;
  framesCut?: number;
  deadlocks?: Deadlock[];
  deadlocksCut?: boolean;
  /** jcmd's output reached the exec's limit: the threads after it are missing. */
  outputCut?: boolean;
  /** What Thread.print printed, for the download. */
  raw?: string;
  notes: string[];
}

/** Takes a thread dump: `Thread.print -l` in `container`, on the person's click only. */
export async function threadDump(
  cluster: string,
  namespace: string,
  name: string,
  container: string,
  pid?: number,
): Promise<ThreadDump> {
  const path = `${podPath(cluster, namespace, name)}/${encodeURIComponent(container)}/threads`;
  let body: ThreadDump;
  try {
    body = await request<ThreadDump>(pid === undefined ? path : `${path}?pid=${String(pid)}`, { method: 'POST' });
  } catch (error) {
    throw problemOf(error);
  }
  if (body.state !== 'dumped' && body.state !== 'choose') {
    throw new EnvelopeError('The JVM Inspector answered without saying what it ran', body);
  }
  return body;
}

// --- the heap dump (`heapdump.go`) ---------------------------------------------

/** The word the route asks for once the person confirmed: `Host.ConfirmToken`. */
export const CONFIRM_WORD = 'confirm';

/** What a heap dump answered: the file, or the JVMs to choose from. */
export type HeapDumpAnswer = { state: 'file'; file: Downloaded } | { state: 'choose'; jvms: { pid: number; mainClass: string }[] };

/** Takes a heap dump of the JVM in `container` and downloads it, once confirmed. */
export async function heapDump(
  cluster: string,
  namespace: string,
  name: string,
  container: string,
  pid?: number,
): Promise<HeapDumpAnswer> {
  const query = new URLSearchParams({ confirm: CONFIRM_WORD });
  if (pid !== undefined) query.set('pid', String(pid));
  const path = `${podPath(cluster, namespace, name)}/${encodeURIComponent(container)}/heapdump?${query.toString()}`;
  try {
    return { state: 'file', file: await download(path, `${name}-${container}.hprof`) };
  } catch (error) {
    if (error instanceof ApiError && error.status === 409) {
      const body = error.body as { state?: unknown; jvms?: unknown } | undefined;
      if (body?.state === 'choose_jvm' && Array.isArray(body.jvms)) {
        return { state: 'choose', jvms: body.jvms as { pid: number; mainClass: string }[] };
      }
    }
    throw problemOf(error);
  }
}

// --- the flight recording (`jfr.go`) -------------------------------------------

/** The seconds a recording may last: the route refuses any other. */
export const RECORDING_SECONDS = [30, 60, 120, 300] as const;
export type RecordingSeconds = (typeof RECORDING_SECONDS)[number];

/** Records the JVM in `container` for `seconds` and downloads the `.jfr`, once confirmed. */
export async function flightRecording(
  cluster: string,
  namespace: string,
  name: string,
  container: string,
  seconds: RecordingSeconds,
  pid?: number,
  signal?: AbortSignal,
): Promise<HeapDumpAnswer> {
  const query = new URLSearchParams({ confirm: CONFIRM_WORD, seconds: String(seconds) });
  if (pid !== undefined) query.set('pid', String(pid));
  const path = `${podPath(cluster, namespace, name)}/${encodeURIComponent(container)}/recording?${query.toString()}`;
  try {
    return { state: 'file', file: await download(path, `${name}-${container}.jfr`, signal === undefined ? {} : { signal }) };
  } catch (error) {
    if (error instanceof ApiError && error.status === 409) {
      const body = error.body as { state?: unknown; jvms?: unknown } | undefined;
      if (body?.state === 'choose_jvm' && Array.isArray(body.jvms)) {
        return { state: 'choose', jvms: body.jvms as { pid: number; mainClass: string }[] };
      }
    }
    throw problemOf(error);
  }
}

// --- a pod's memory history (`history.go`) ------------------------------------

/** `read` has lines; `absent` (no Prometheus), `unreadable` (one there that
 *  could not be asked), `empty` (nothing for this pod) have none and say why;
 *  `pending` is still being read, and is asked again. Never one drawn as another. */
export type HistoryState = 'read' | 'absent' | 'unreadable' | 'empty' | 'pending';

export interface HistoryLine {
  container: string;
  /** [x, y] on a 0..100 box, y from the top: drawn as given, never rescaled. */
  points: [number, number][];
  last: Memory;
  min: Memory;
  max: Memory;
  limit: Memory | null;
  /** The limit's place on the scale (y, from the top); null without a limit. */
  limitAt: number | null;
}

export interface History {
  cluster: string;
  namespace: string;
  pod: string;
  state: HistoryState;
  /** The core's line on the window, for a tooltip. */
  detail?: string;
  /** Set when some of the window could not be read. */
  partial?: string;
  /** The window's span, Unix seconds. */
  from?: number;
  to?: number;
  scale?: Memory;
  ticks?: { y: number; text: string }[];
  series: HistoryLine[];
  /** More containers than are drawn. */
  cut?: boolean;
}

/** How often a history still being read is asked again, and how many times. */
export const HISTORY_RETRY_MS = 5_000;
export const HISTORY_RETRIES = 36;

const HISTORY_STATES: HistoryState[] = ['read', 'absent', 'unreadable', 'empty', 'pending'];

/** `GET /pod/{namespace}/{name}/history`: seven days of the pod's containers' memory. */
export async function getHistory(cluster: string, namespace: string, name: string, signal?: AbortSignal): Promise<History> {
  let body: History;
  try {
    body = await request<History>(`${podPath(cluster, namespace, name)}/history`, signal === undefined ? {} : { signal });
  } catch (error) {
    throw problemOf(error);
  }
  if (!HISTORY_STATES.includes(body.state) || !Array.isArray(body.series)) {
    throw new EnvelopeError('The JVM Inspector answered without saying what Prometheus held', body);
  }
  return body;
}

// --- the class histogram (`histogram.go`) -------------------------------------

/** One class of the histogram, the largest first. `share` is its percentage
 *  of the histogram's total bytes, which the page draws as given. */
export interface ClassRow {
  rank: number;
  /** As Java writes it: `int[]`, `java.lang.String`. */
  name: string;
  /** Empty on JDK 8 and for a class in no module. */
  module?: string;
  instances: number;
  bytes: number;
  share: number;
}

export interface ClassHistogram {
  cluster: string;
  namespace: string;
  pod: string;
  container: string;
  /** `choose`: several JVMs, listed in `jvms`, and nothing was run. */
  state: 'histogram' | 'choose';
  jvms?: ListedJVM[];
  pid?: number;
  pidFrom?: 'listed' | 'chosen' | 'pid1';
  inherited?: boolean;
  /** How many classes the JVM listed; `rows` are the first of them. */
  classes?: number;
  instances?: number;
  bytes?: number;
  rows?: ClassRow[];
  /** More classes than rows. */
  cut?: boolean;
  /** jcmd's output reached the exec's limit: the totals are short. */
  outputCut?: boolean;
}

/**
 * Takes a class histogram: `GC.class_histogram` in `container`, **a full GC**,
 * on the person's confirmed click only. The route refuses a request that does
 * not say it was confirmed.
 */
export async function classHistogram(
  cluster: string,
  namespace: string,
  name: string,
  container: string,
  pid?: number,
): Promise<ClassHistogram> {
  const path = `${podPath(cluster, namespace, name)}/${encodeURIComponent(container)}/histogram`;
  let body: ClassHistogram;
  try {
    body = await request<ClassHistogram>(pid === undefined ? path : `${path}?pid=${String(pid)}`, {
      method: 'POST',
      body: { confirmed: true },
    });
  } catch (error) {
    throw problemOf(error);
  }
  if (body.state !== 'histogram' && body.state !== 'choose') {
    throw new EnvelopeError('The JVM Inspector answered without saying what it ran', body);
  }
  return body;
}
