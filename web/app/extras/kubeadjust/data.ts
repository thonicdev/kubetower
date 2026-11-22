import { queryOptions } from '@tanstack/react-query';
import { ApiError, EnvelopeError, NotJsonError, request } from '@kubetower/ui';

/**
 * What the one route of `extras/kubeadjust` answers, typed as the Go writes it
 * (`view.go`, `nodes.go`).
 *
 * **One answer per scope.** The scope is the namespace, the one thing that
 * changes what the server reads. The whole cluster's answer carries the bodies
 * of both its views, the overview's and the Nodes one, and every suggestion
 * unfiltered; which view is on screen and which category is shown are the
 * page's, drawn from that answer, so switching either reads nothing.
 *
 * **Three states for everything the page draws, and the types keep them.** A
 * view's list is `null` when that view was not built (one namespace's answer
 * builds its own, the cluster's builds the other two), `[]` when it was built
 * and holds nothing. A count is `null` when nothing
 * could be measured. A figure is `''` and `measured` false when nothing
 * reported it. The page draws each of the three differently, and never draws an
 * absence as a zero.
 *
 * Every number on a bar or a gauge is the server's: the page places what it is
 * given and never rescales.
 */

export const KEY = 'kubeadjust';

export type View = 'overview' | 'nodes' | 'namespace';
export type Category = '' | 'cpu' | 'memory' | 'storage';
/** A suggestion's severity, loudest first. */
export type Kind = 'danger' | 'warning' | 'overkill';
/** A bar's or a node's band; `none` is "nothing measured it". */
export type Status = 'danger' | 'warning' | 'overkill' | 'healthy' | 'none';
/**
 * How much a CPU or memory suggestion's figure rests on: `high` is a week of
 * usable Prometheus series, `medium` a day to a week, `low` one metrics-server
 * scrape. `''` on a disk row, which reads a level rather than a distribution.
 */
export type Confidence = 'high' | 'medium' | 'low' | '';
/**
 * The usage window's state, as the core answered it; `''` when none was asked
 * for because nothing could be measured. Anything but `no-prometheus` and `''`
 * means a Prometheus was found. `pending` is a page drawn before its window
 * arrived: the read goes on in the console, and the page asks again
 * (`PENDING_POLL_MS`) until it is read.
 */
export type WindowState = 'read' | 'no-prometheus' | 'unreadable' | 'empty' | 'pending' | '';

export interface Note {
  ok: boolean;
  label: string;
  detail: string;
}

export interface Count {
  kind: Kind;
  label: string;
  count: number | null;
}

export interface CategoryChip {
  key: Exclude<Category, ''>;
  label: string;
  /** Over every suggestion in scope, whichever chip is selected. */
  count: number | null;
}

export interface Row {
  workload: string;
  workloadKind: string;
  namespace: string;
  pod: string;
  podCount: number;
  container: string;
  resource: string;
  kind: Kind;
  action: string;
  message: string;
  current: string;
  suggested: string;
  appliesToBoth: boolean;
  warnings: string[];
  /** Empty when the change cannot be expressed as a command, or no value is recommended. */
  kubectl: string;
  scope: string;
  confidence: Confidence;
  /**
   * Too little usable usage to name a value: `suggested` is '' and there is no
   * command, except on an OOM kill's raise, whose value comes from the limit.
   */
  insufficientData: boolean;
}

export interface Group {
  kind: Kind;
  label: string;
  mark: string;
  rows: Row[];
}

export interface Bar {
  label: string;
  measured: boolean;
  status: Status;
  mean: string;
  peak: string;
  request: string;
  limit: string;
  usePct: number;
  reqPct: number;
  peakPct: number;
  /** The mean as a percentage of the limit; null with no limit or no reading. */
  pct: number | null;
  scale: string;
  headroom: string;
  unbounded: boolean;
  /** Too little usable usage to size this resource from: drawn, and no value recommended. */
  insufficient: boolean;
}

export interface PodFigure {
  pod: string;
  cpu: string;
  memory: string;
  measured: boolean;
}

/** A raise of a memory limit, formatted; `to` is "unlimited" when it removed the limit. */
export interface MemoryRaised {
  from: string;
  to: string;
  revision: number;
}

export interface Container {
  name: string;
  restarts: number;
  crashLooping: boolean;
  oomKilled: boolean;
  /** Killed for memory more than a day ago: no alarm, still no memory reduction. */
  oomKilledEarlier: boolean;
  /**
   * The raise of the memory limit that holds back every reduction of it: the
   * pods killed before that rollout are gone, so their kill is remembered from
   * the workload's kept revisions. Null when no raise does.
   */
  memoryRaised: MemoryRaised | null;
  cpu: Bar;
  memory: Bar;
  ephemeral: Bar;
  pods: PodFigure[];
}

export interface Volume {
  pod: string;
  name: string;
  claim: string;
  kind: 'pvc' | 'emptyDir' | string;
  status: Status;
  pct: string;
  used: string;
  free: string;
  capacity: string;
  /** The fill's width; null when there is no fill to draw. */
  usePct: number | null;
  measured: boolean;
  ceilingWord: string;
  pctOf: string;
  unbounded: boolean;
}

export interface WorkloadCard {
  workload: string;
  kind: string;
  namespace: string;
  replicas: number;
  qos: string;
  /** What else sizes this workload (an HPA, a VPA) and what that withholds. */
  managed: string[];
  worst: Kind | '';
  containers: Container[];
  findings: Row[];
  volumes: Volume[];
  storageOK: boolean;
}

export interface NamespaceCard {
  name: string;
  pods: number;
  workloads: number;
  findings: number;
  measured: boolean;
  band: 'overcommit' | 'high' | 'ok' | 'norequests' | '';
  worst: Kind | '';
  cpuRatio: string;
  memRatio: string;
  cpuRequest: string;
  cpuUsed: string;
  memRequest: string;
  memUsed: string;
  coverage: string;
  /** Requested and not used, over the measured containers; `none`, or '' unmeasured. */
  cpuIdle: string;
  memIdle: string;
}

/** The first line of the page: findings, what reductions give back, use of requests. */
export interface Headline {
  scope: string;
  atRisk: number;
  worth: number;
  over: number;
  /** '' when nothing is given back. Requests only: a limit reserves nothing. */
  reclaimCPU: string;
  reclaimMemory: string;
  reclaimWorkloads: number;
  /** null when there is no request to divide by. */
  usedOfRequestedCPU: number | null;
  usedOfRequestedMemory: number | null;
}

export interface Totals {
  namespaces: number;
  cpuTotal: string;
  cpuAvg: string;
  memTotal: string;
  memAvg: string;
  source: string;
  fromRequests: boolean;
}

export interface PodTrack {
  reqPct: number;
  usePct: number;
  status: Status | '';
  pct: string;
  measured: boolean;
  title: string;
}

export interface NodePod {
  name: string;
  title: string;
  cpu: PodTrack;
  mem: PodTrack;
}

export interface Taint {
  text: string;
  effect: string;
  title: string;
}

export interface NodeCard {
  name: string;
  ready: boolean;
  pods: number;
  maxPods: number;
  podsTight: boolean;
  roles: string[];
  controlPlane: boolean;
  age: string;
  osImage: string;
  kernel: string;
  kubelet: string;
  pressures: string[];
  taints: Taint[];
  /** CPU, memory and ephemeral storage, drawn as the node tab draws them. */
  resources: NodeResource[];
  topPods: NodePod[];
  totalPods: number;
  more: string;
  /** The card's tile: one of the gauge's bands, or `none`. */
  state: Status | 'unknown';
}

export interface NodeState {
  key: Status;
  label: string;
  /** null: nobody could say - no node measured, or none read at all. */
  count: number | null;
}

export interface Page {
  cluster: string;
  /** The scope: '' for the whole cluster. */
  namespace: string;
  namespaces: string[];
  namespacesNote?: string;
  notes: Note[];
  /** Whether a Prometheus was found, and what its window answered. */
  window: WindowState;
  /** When that window was read (RFC 3339): a kept one is older than the page. */
  windowReadAt?: string;
  ready: boolean;
  /** How many workloads were examined; null when nothing was measured. */
  workloads: number | null;
  counts: Count[];
  categories: CategoryChip[];
  /** Every suggestion in scope, by severity; `shownSuggestions` filters them. */
  groups: Group[];
  /** Every command the groups carry, once each, in the order they are drawn. */
  commands: string[];
  /** null when nothing was measured. */
  headline: Headline | null;
  /** What to act on first, for the overview; null when not built. */
  top: Row[] | null;
  totals: Totals | null;
  namespaceCards: NamespaceCard[] | null;
  nodeCards: NodeCard[] | null;
  nodeStates: NodeState[] | null;
  cards: WorkloadCard[] | null;
  /** Said when the pods were narrowed to the namespaces the person may list:
   *  every total counts some of the pods, and no node is placed in a state. */
  partial: string;
}

/**
 * What the address asks for. Only `ns` is the server's question; `view` and
 * `cat` are the page's state, kept in the address so a link and a reload land
 * on the same view and filter.
 */
export interface Ask {
  ns: string;
  view: string;
  cat: string;
}

/** The view to draw: a namespace's answer is drawn as that namespace whatever
 *  the address says, so an answer still on screen while another scope is read
 *  keeps its own view. */
export function viewOf(page: Pick<Page, 'namespace'>, view: string): View {
  if (page.namespace !== '') return 'namespace';
  return view === 'nodes' ? 'nodes' : 'overview';
}

/**
 * The category in force. An unrecognized one is dropped, which shows
 * everything: a typo in a pasted link would otherwise draw an empty panel that
 * reads as a namespace with nothing wrong.
 */
export function categoryIn(cat: string): Category {
  return cat === 'cpu' || cat === 'memory' || cat === 'storage' ? cat : '';
}

/**
 * The filter a finding falls under, as `categoryOf` in `view.go` maps it:
 * storage is ephemeral storage, claims and emptyDirs together.
 */
export function categoryOf(resource: string): Category {
  if (resource.startsWith('CPU')) return 'cpu';
  if (resource.startsWith('Memory')) return 'memory';
  if (resource.startsWith('Ephemeral') || resource === 'PVC' || resource === 'EmptyDir') return 'storage';
  return '';
}

/**
 * The suggestions and the command block under one category: the server's
 * groups kept to it, in its order, empty groups left out, and every command
 * once in the order drawn. No category is everything as answered.
 */
export function shownSuggestions(page: Pick<Page, 'groups' | 'commands'>, cat: Category): { groups: Group[]; commands: string[] } {
  if (cat === '') return { groups: page.groups, commands: page.commands };
  const groups = page.groups
    .map((g) => ({ ...g, rows: g.rows.filter((r) => categoryOf(r.resource) === cat) }))
    .filter((g) => g.rows.length > 0);
  const commands = [...new Set(groups.flatMap((g) => g.rows.map((r) => r.kubectl)).filter((c) => c !== ''))];
  return { groups, commands };
}

/** The route's address: the scope alone, so a view or a chip is never a read. */
export function pagePath(cluster: string, ns: string): string {
  const query = new URLSearchParams();
  if (ns !== '') query.set('ns', ns);
  const text = query.toString();
  const base = `/x/${encodeURIComponent(cluster)}/${KEY}/`;
  return text === '' ? base : `${base}?${text}`;
}

interface Problem {
  code?: unknown;
  message?: unknown;
  detail?: unknown;
}

/**
 * A plain-text 404 at this address is the console's gate, never the route:
 * every refusal the route writes is JSON with a code. The fallback: the gate's own
 * sentence, the `NotJsonError` excerpt, is shown when there is one.
 */
const GATE_REFUSED =
  'KubeAdjust is not enabled for this cluster in this console. Turn it on in Settings > Extra; ' +
  "where the console runs inside a cluster, the chart's values must name it and you need the verb use on " +
  'extras.kubetower.dev/kubeadjust.';

function said(error: unknown): unknown {
  if (error instanceof NotJsonError && error.status === 404) return new ApiError(404, error.excerpt ?? GATE_REFUSED);
  if (!(error instanceof ApiError) || error instanceof NotJsonError || error instanceof EnvelopeError) return error;
  const problem = error.body as Problem | undefined;
  if (typeof problem?.message !== 'string' || problem.message === '') return error;
  const detail = typeof problem.detail === 'string' && problem.detail !== '' ? ` ${problem.detail}` : '';
  return new ApiError(error.status, `${problem.message}${detail}`, error.body);
}

/**
 * One read. `signal` is React Query's: a newer question for this page aborts
 * the one in flight, so an older answer never paints over a newer one.
 */
export async function getPage(cluster: string, ns: string, signal?: AbortSignal): Promise<Page> {
  let body: Page;
  try {
    body = await request<Page>(pagePath(cluster, ns), signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (!Array.isArray(body.notes) || typeof body.ready !== 'boolean' || typeof body.namespace !== 'string') {
    throw new EnvelopeError('KubeAdjust answered without its notes, its ready flag and its scope', body);
  }
  return body;
}

/** One resource of one node, in the node tab (`nodetab.go`). */
export interface NodeResource {
  key: 'cpu' | 'memory' | 'ephemeral-storage' | string;
  label: string;
  /** '' when the node declares none of this resource. */
  allocatable: string;
  requested: string;
  limited: string;
  /** '' when not measured. */
  used: string;
  requestedPct: number;
  limitedPct: number;
  /** null when not measured: an unmeasured node is not an idle one. */
  usedPct: number | null;
  requestBar: number;
  limitBar: number;
  useBar: number;
  measured: boolean;
  coverage: string;
  status: Status | 'unknown' | '';
  noAllocatable: boolean;
  /** "N points reserved and not used"; '' when there is no such gap to say. */
  gap: string;
}

/** What the machine is: five facts drawn, the rest folded. */
export interface NodeAbout {
  kubelet: string;
  os: string;
  /** '' when the Node could not be read again for it. */
  runtime: string;
  age: string;
  kernel: string;
  architecture: string;
  roles: string[];
  taints: Taint[];
}

/** A workload with a pod on the node and something to change. */
export interface NodeWorkload {
  workload: string;
  kind: string;
  namespace: string;
  worst: Kind | '';
  findings: number;
  /** How far its pods here are from what they reserve, in words; '' unmeasured. */
  gap: string;
}

export interface NodeFinding {
  kind: Kind;
  title: string;
  detail: string;
}

export interface NodeTabPod {
  name: string;
  namespace: string;
  workload: string;
  cpuRequest: string;
  cpuLimit: string;
  cpuUsed: string;
  memRequest: string;
  memLimit: string;
  memUsed: string;
  share: number;
  measured: boolean;
  findings: number;
  worst: Kind | '';
}

/** What `GET /node/{name}` answers. */
export interface NodeTab {
  cluster: string;
  node: string;
  ready: boolean;
  measured: boolean;
  notes: Note[];
  /** When the oldest of the node's window reads was made (RFC 3339). */
  windowReadAt?: string;
  /** Said when the totals count only some of the node's pods; '' when whole. */
  partial: string;
  resources: NodeResource[];
  pods: number;
  maxPods: number;
  about: NodeAbout;
  findings: NodeFinding[];
  suggestions: Row[];
  commands: string[];
  /** null when nothing was measured, so no workload could be judged. */
  workloads: number | null;
  /** How many workloads with a pod here carry a suggestion. */
  flagged: number;
  /** The few of them furthest from what they reserve here, the largest gap first. */
  topWorkloads: NodeWorkload[];
  podList: NodeTabPod[];
}

export function nodePath(cluster: string, node: string): string {
  return `/x/${encodeURIComponent(cluster)}/${KEY}/node/${encodeURIComponent(node)}`;
}

export async function getNodeTab(cluster: string, node: string, signal?: AbortSignal): Promise<NodeTab> {
  let body: NodeTab;
  try {
    body = await request<NodeTab>(nodePath(cluster, node), signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (!Array.isArray(body.resources) || !Array.isArray(body.notes)) {
    throw new EnvelopeError('KubeAdjust answered this node without its resources and its notes', body);
  }
  return body;
}

/** What `GET /workload/{kind}/{namespace}/{name}` answers: the page's card for
 *  one Deployment, StatefulSet, DaemonSet or CronJob (`workload.go`). */
export interface WorkloadTab {
  cluster: string;
  namespace: string;
  /** Deployment, StatefulSet, DaemonSet or CronJob. */
  kind: string;
  name: string;
  /** false when nothing could be measured: no card is then not "sized right". */
  ready: boolean;
  window: WindowState;
  /** When that window was read (RFC 3339), as on the page. */
  windowReadAt?: string;
  notes: Note[];
  partial: string;
  /** null when nothing was measured, or when no running pod of it reported usage. */
  card: WorkloadCard | null;
  commands: string[];
}

export function workloadPath(cluster: string, kind: string, namespace: string, name: string): string {
  return `/x/${encodeURIComponent(cluster)}/${KEY}/workload/${encodeURIComponent(kind)}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

export async function getWorkloadTab(cluster: string, kind: string, namespace: string, name: string, signal?: AbortSignal): Promise<WorkloadTab> {
  let body: WorkloadTab;
  try {
    body = await request<WorkloadTab>(workloadPath(cluster, kind, namespace, name), signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (!Array.isArray(body.notes) || !Array.isArray(body.commands)) {
    throw new EnvelopeError('KubeAdjust answered this workload without its notes and its commands', body);
  }
  return body;
}

/**
 * How long an answer is fresh: the console's usage cache holds what each
 * kubelet and metrics-server said for ten seconds (`usageTTL`), so asking again
 * sooner - the default five seconds, on every focus - gets the same figures
 * back for the price of a whole read.
 */
export const STALE_MS = 10_000;

/**
 * How old the usage history drawn is, in words - "History read 12 minutes
 * ago" - or '' when it was read under a minute ago, or its time is unknown.
 * The console keeps a window for up to twenty minutes and serves it as it was,
 * so a figure can be older than the page that draws it.
 */
export function windowAge(readAt: string | undefined, now: number = Date.now()): string {
  if (readAt === undefined || readAt === '') return '';
  const at = Date.parse(readAt);
  if (Number.isNaN(at)) return '';
  const minutes = Math.floor((now - at) / 60_000);
  if (minutes < 1) return '';
  const unit = (n: number, word: string): string => `${String(n)} ${word}${n === 1 ? '' : 's'}`;
  let span = unit(minutes, 'minute');
  if (minutes >= 60 * 24) span = unit(Math.floor(minutes / (60 * 24)), 'day');
  else if (minutes >= 60) span = unit(Math.floor(minutes / 60), 'hour');
  return `History read ${span} ago`;
}

/** How often a page whose window is still being read asks again (each ask also waits up to the server's 1.5 s for it). */
export const PENDING_POLL_MS = 1_500;

export function windowPath(cluster: string, ns: string): string {
  const query = new URLSearchParams();
  if (ns !== '') query.set('ns', ns);
  const text = query.toString();
  const base = `/x/${encodeURIComponent(cluster)}/${KEY}/window`;
  return text === '' ? base : `${base}?${text}`;
}

/** Where the scope's window stands, and nothing else: `GET /window`. */
export async function getWindowState(cluster: string, ns: string, signal?: AbortSignal): Promise<WindowState> {
  let body: { window?: unknown };
  try {
    body = await request<{ window?: unknown }>(windowPath(cluster, ns), signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (typeof body.window !== 'string') throw new EnvelopeError('KubeAdjust answered the window without its state', body);
  return body.window as WindowState;
}

/**
 * While the last answer's window is pending, a poll asks only where the window
 * stands, and keeps that answer while it is still pending: reading the page
 * again would list the pods, every other kind and metrics-server for the same
 * figures. Once the window is no longer pending, or its state could not be
 * read, the page is read again.
 */
async function unlessStillPending<T extends { window: WindowState }>(
  last: T | undefined,
  cluster: string,
  ns: string,
  signal: AbortSignal,
  read: () => Promise<T>,
): Promise<T> {
  if (last?.window === 'pending') {
    let state: WindowState | undefined;
    try {
      state = await getWindowState(cluster, ns, signal);
    } catch (error) {
      if (signal.aborted) throw error;
    }
    if (state === 'pending') return last;
  }
  return read();
}

/**
 * How long an answer no screen draws is kept: coming back to KubeAdjust
 * within it draws the last answer at once and refreshes behind it, instead of
 * a spinner for a whole read. Ten minutes, the console's own window cache TTL.
 */
export const KEEP_MS = 10 * 60_000;

/** Ask again while the answer's window is still being read. */
function pollWhilePending(query: { state: { data: { window: WindowState } | undefined } }): number | false {
  return query.state.data?.window === 'pending' ? PENDING_POLL_MS : false;
}

export function nodeTabQuery(cluster: string, node: string) {
  return queryOptions({
    queryKey: [KEY, cluster, 'node', node] as const,
    queryFn: ({ signal }) => getNodeTab(cluster, node, signal),
    staleTime: STALE_MS,
  });
}

export function workloadTabQuery(cluster: string, kind: string, namespace: string, name: string) {
  return queryOptions({
    queryKey: [KEY, cluster, 'workload', kind, namespace, name] as const,
    queryFn: ({ signal, client, queryKey }) =>
      unlessStillPending(client.getQueryData<WorkloadTab>(queryKey), cluster, namespace, signal, () =>
        getWorkloadTab(cluster, kind, namespace, name, signal),
      ),
    staleTime: STALE_MS,
    gcTime: KEEP_MS,
    refetchInterval: pollWhilePending,
  });
}

/** One cache entry per scope: the view and the category are drawn from it. */
export function pageQuery(cluster: string, ns: string) {
  return queryOptions({
    queryKey: [KEY, cluster, ns] as const,
    queryFn: ({ signal, client, queryKey }) =>
      unlessStillPending(client.getQueryData<Page>(queryKey), cluster, ns, signal, () => getPage(cluster, ns, signal)),
    staleTime: STALE_MS,
    gcTime: KEEP_MS,
    refetchInterval: pollWhilePending,
  });
}
