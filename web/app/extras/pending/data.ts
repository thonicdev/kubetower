import { queryOptions } from '@tanstack/react-query';
import { ApiError, EnvelopeError, NotJsonError, request } from '@kubetower/ui';

/**
 * What the routes of `extras/pending` answer, typed as the Go writes them
 * (`pending.go`, `read.go`, `fit.go`).
 *
 * **Three states, and the types keep them.** A section (`pods`, `nodes`,
 * `placed`, …) is `read`, `denied` for this person, `unreviewable`, or
 * `failed`; a read that was narrowed or cut is `read` with `complete` false and
 * a note. "Nothing pending" is a read pods section with no rows - never the
 * same picture as a refusal.
 */

export const KEY = 'pending';

export type Category =
  | 'resources'
  | 'taints'
  | 'nodeAffinity'
  | 'podAffinity'
  | 'spread'
  | 'volumes'
  | 'cordon'
  | 'ports'
  | 'gated'
  | 'noScheduler'
  | 'starting'
  | 'other';

/** Every category, in the order the tiles draw them (the Go `Categories`). */
export const CATEGORIES: readonly Category[] = [
  'resources',
  'taints',
  'nodeAffinity',
  'podAffinity',
  'spread',
  'volumes',
  'cordon',
  'ports',
  'gated',
  'noScheduler',
  'starting',
  'other',
];

export const CATEGORY_LABEL: Readonly<Record<Category, string>> = {
  resources: 'Resources',
  taints: 'Taints',
  nodeAffinity: 'Node selector',
  podAffinity: 'Pod affinity',
  spread: 'Topology spread',
  volumes: 'Volumes',
  cordon: 'Cordoned',
  ports: 'Host ports',
  gated: 'Gated',
  noScheduler: 'No scheduler',
  starting: 'Starting',
  other: 'Other',
};

/** What each tile's hover says the category means. */
export const CATEGORY_HINT: Readonly<Record<Category, string>> = {
  resources: 'No node has enough CPU, memory, storage, devices or pod slots free for the request',
  taints: 'Nodes carry a taint the pod does not tolerate',
  nodeAffinity: "No node matches the pod's node selector or required node affinity",
  podAffinity: "The pod's affinity or anti-affinity rules, or another pod's, rule out the nodes",
  spread: 'Placing the pod would break a topology spread constraint',
  volumes: 'A claim is missing or unbound, or its volume cannot be reached from the nodes',
  cordon: 'The nodes are cordoned',
  ports: 'The host port the pod asks for is taken',
  gated: 'Held back by scheduling gates until a controller removes them',
  noScheduler: 'No scheduler has answered for it',
  starting: 'Placed on a node, but its containers have not started: image, mount, init',
  other: 'Something else, in the scheduler’s own words',
};

export type Stage = 'unschedulable' | 'gated' | 'unseen' | 'starting';
export type SectionState = 'read' | 'denied' | 'unreviewable' | 'failed';

export interface Section {
  state: SectionState;
  complete: boolean;
  note?: string;
}

export interface Reason {
  text: string;
  /** -1: the scheduler gave no count, which means every node. */
  nodes: number;
  category: Category;
}

export interface Verdict {
  total: number;
  reasons: Reason[] | null;
  preemption?: string;
}

export type PreemptionVerdict = 'nominated' | 'waiting' | 'never' | 'unhelpful' | 'noVictims' | 'mixed' | 'unsaid' | 'other';

/**
 * The scheduler's word on evicting lower-priority pods, the node it nominated
 * and the pod's priority. Only `nominated` says eviction helps. A count is `n`
 * nodes, 0 for "not named" and -1 for "named with no count" (every node).
 */
export interface Preemption {
  verdict: PreemptionVerdict;
  total: number;
  unhelpful: number;
  noVictims: number;
  text?: string;
  nominated?: string;
  priority?: number;
  class?: string;
  policy?: string;
}

export interface Row {
  namespace: string;
  name: string;
  created: string;
  stage: Stage;
  main: Category;
  categories: Category[];
  message: string;
  verdict: Verdict;
  since?: string;
  closest?: { node: string; failing: number };
  owner?: { kind: string; name: string };
  preemption?: Preemption;
  /** CPU in millicores, every other resource in its unit. */
  request: Record<string, number>;
  node?: string;
  scheduler?: string;
}

export interface Gauge {
  allocatable: number;
  requested: number;
}

export interface NodeBar {
  name: string;
  ready: boolean;
  cordoned: boolean;
  taints: number;
  cpu: Gauge;
  memory: Gauge;
  pods: Gauge;
}

export interface Quota {
  name: string;
  resource: string;
  requested: string;
  used?: number;
  hard?: number;
  room?: boolean;
}

export interface Failure {
  namespace: string;
  kind: string;
  name: string;
  category: 'quota' | 'policy' | 'other';
  message: string;
  count: number;
  last: string;
  quota?: Quota;
}

export interface PageAnswer {
  cluster: string;
  namespace: string;
  read: string;
  pods: Section;
  capped: boolean;
  total: number;
  counts: Partial<Record<Category, number>>;
  rows: Row[];
  nodes: Section;
  placed: Section;
  bars: NodeBar[];
  failures: Failure[];
  failuresRead: Section;
}

export interface Column {
  key: string;
  label: string;
  computed: boolean;
  scheduler: number;
  /** For a column that is not computed although it could have been: what stopped it. */
  why?: string;
}

export interface Cell {
  key: string;
  /** null: not computed here, the scheduler's word only. */
  ok: boolean | null;
  detail?: string;
}

export interface Fit {
  node: string;
  cells: Cell[];
  failing: number;
  excluded: number;
  deficit: number;
}

export interface TimelineEvent {
  type: string;
  reason: string;
  message: string;
  first: string;
  last: string;
  count: number;
  source: string;
}

export interface PodView {
  cluster: string;
  pending: boolean;
  phase: string;
  read: string;
  row?: Row;
  columns: Column[];
  fits: Fit[];
  nodesTotal: number;
  nodes?: Section;
  placed?: Section;
  bars: NodeBar[];
  wouldTake: string[];
  claims: { claim: string; problem: string }[];
  claimsRead?: Section;
  events: TimelineEvent[];
  eventsRead?: Section;
  eventsCapped: boolean;
}

/** `GET /workload/{kind}/{namespace}/{name}`: one workload's pending pods and, when its controller was refused a pod, the refusal. */
export interface WorkloadView {
  cluster: string;
  kind: string;
  namespace: string;
  name: string;
  read: string;
  pods: Section;
  capped: boolean;
  pending: number;
  rows: Row[];
  failure?: Failure;
  failuresRead: Section;
}

/** The kinds that carry the Pending column and tab, as the address spells them. */
export const WORKLOAD_KINDS: readonly string[] = ['deployments', 'statefulsets', 'daemonsets', 'jobs'];

export interface Brief {
  pending: boolean;
  row?: Row;
}

/** How often an open page asks again. Only while it is on screen: TanStack
 *  Query stops the interval when the window is hidden and when the page goes. */
export const POLL_MS = 30_000;
export const STALE_MS = 10_000;

/** How many rows the list draws per screen. */
export const PAGE_ROWS = 200;

const base = (cluster: string) => `/x/${encodeURIComponent(cluster)}/${KEY}/`;

export function pagePath(cluster: string, ns: string): string {
  return ns === '' ? base(cluster) : `${base(cluster)}?${new URLSearchParams({ ns }).toString()}`;
}

export function podPath(cluster: string, namespace: string, name: string): string {
  return `${base(cluster)}pod/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

export function briefPath(cluster: string, namespace: string, name: string): string {
  return `${base(cluster)}brief/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

export function workloadPath(cluster: string, kind: string, namespace: string, name: string): string {
  return `${base(cluster)}workload/${encodeURIComponent(kind)}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

export function cellsPath(cluster: string, kind: string): string {
  return `${base(cluster)}cells?${new URLSearchParams({ kind }).toString()}`;
}

/** Where the console draws one pod's view. */
export function podHref(cluster: string, namespace: string, name: string): string {
  return `/c/${encodeURIComponent(cluster)}/x/${KEY}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

const GATE_REFUSED = 'Pod Debugging is not turned on for this cluster.';

interface Problem {
  code?: string;
  message?: string;
  detail?: string;
}

/** The route's own sentence, in place of a bare status. */
function said(error: unknown): unknown {
  if (error instanceof NotJsonError && error.status === 404) return new ApiError(404, error.excerpt ?? GATE_REFUSED);
  if (!(error instanceof ApiError) || error instanceof NotJsonError || error instanceof EnvelopeError) return error;
  const problem = error.body as Problem | undefined;
  if (typeof problem?.message !== 'string' || problem.message === '') return error;
  const detail = typeof problem.detail === 'string' && problem.detail !== '' ? ` ${problem.detail}` : '';
  return new ApiError(error.status, `${problem.message}${detail}`, error.body);
}

async function read<T>(path: string, signal: AbortSignal | undefined, check: (body: T) => boolean, what: string): Promise<T> {
  let body: T;
  try {
    body = await request<T>(path, signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (!check(body)) throw new EnvelopeError(what, body);
  return body;
}

export function getPage(cluster: string, ns: string, signal?: AbortSignal): Promise<PageAnswer> {
  return read<PageAnswer>(
    pagePath(cluster, ns),
    signal,
    (b) => Array.isArray(b.rows) && Array.isArray(b.bars) && typeof b.pods?.state === 'string',
    'Pod Debugging answered without its rows, its nodes and its read',
  );
}

export function getPod(cluster: string, namespace: string, name: string, signal?: AbortSignal): Promise<PodView> {
  return read<PodView>(
    podPath(cluster, namespace, name),
    signal,
    (b) => typeof b.pending === 'boolean' && Array.isArray(b.events) && Array.isArray(b.fits),
    'Pod Debugging answered this pod without its fit and its events',
  );
}

export function getBrief(cluster: string, namespace: string, name: string, signal?: AbortSignal): Promise<Brief> {
  return read<Brief>(briefPath(cluster, namespace, name), signal, (b) => typeof b.pending === 'boolean', 'Pod Debugging answered without saying');
}

export function getWorkload(cluster: string, kind: string, namespace: string, name: string, signal?: AbortSignal): Promise<WorkloadView> {
  return read<WorkloadView>(
    workloadPath(cluster, kind, namespace, name),
    signal,
    (b) => Array.isArray(b.rows) && typeof b.pods?.state === 'string' && typeof b.pending === 'number',
    'Pod Debugging answered this workload without its pods',
  );
}

export function workloadQuery(cluster: string, kind: string, namespace: string, name: string) {
  return queryOptions({
    queryKey: [KEY, 'workload', cluster, kind, namespace, name] as const,
    queryFn: ({ signal }) => getWorkload(cluster, kind, namespace, name, signal),
    staleTime: STALE_MS,
    refetchInterval: POLL_MS,
  });
}

export function pageQuery(cluster: string, ns: string) {
  return queryOptions({
    queryKey: [KEY, 'page', cluster, ns] as const,
    queryFn: ({ signal }) => getPage(cluster, ns, signal),
    staleTime: STALE_MS,
    refetchInterval: POLL_MS,
  });
}

export function podQuery(cluster: string, namespace: string, name: string) {
  return queryOptions({
    queryKey: [KEY, 'pod', cluster, namespace, name] as const,
    queryFn: ({ signal }) => getPod(cluster, namespace, name, signal),
    staleTime: STALE_MS,
    refetchInterval: POLL_MS,
  });
}

/** CPU in cores or millicores, bytes in binary units - as the Go writes them. */
export function format(resource: string, value: number): string {
  if (resource === 'cpu') {
    if (value % 1000 === 0) return String(value / 1000);
    if (value >= 1000) return (value / 1000).toFixed(1);
    return `${value}m`;
  }
  if (resource === 'memory' || resource === 'ephemeral-storage' || resource.startsWith('hugepages-')) {
    const units = ['', 'Ki', 'Mi', 'Gi', 'Ti'];
    let v = value;
    let i = 0;
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024;
      i += 1;
    }
    if (i === 0) return String(value);
    return `${v >= 10 || Number.isInteger(v) ? v.toFixed(0) : v.toFixed(1)}${units[i]}`;
  }
  return String(value);
}

/** "3m", "2h", "4d": how long ago, from the answer's own clock. */
export function ago(at: string, now: string): string {
  const ms = Date.parse(now) - Date.parse(at);
  if (!Number.isFinite(ms) || ms < 0) return '—';
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86_400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86_400)}d`;
}
