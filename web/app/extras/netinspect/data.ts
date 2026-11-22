import { queryOptions } from '@tanstack/react-query';
import { ApiError, EnvelopeError, NotJsonError, request, type ExtensionCell, type ExtensionShortcutTarget, type ExtensionTabProps } from '@kubetower/ui';

/**
 * What `extras/netinspect` answers, typed as the Go writes it (`view.go`,
 * `verdict.go`).
 *
 * **Rules, never traffic.** Every answer carries `mode: 'rules'`: it is what
 * the NetworkPolicies would allow, computed from the objects, and the page
 * draws it as such - a dashed edge, "allowed by the rule". Observed traffic is
 * a different source and would be a different mode, drawn solid.
 */

export const KEY = 'netinspect';
export const PAGE_PATH = `x/${KEY}`;

/** The two sources a picture can come from; this version reads the rules only. */
export type Mode = 'rules' | 'observed';

export interface CNI {
  name: string;
  enforces: 'yes' | 'no' | 'unknown';
  detail: string;
}

export interface Unevaluated {
  kind: string;
  group: string;
  /** `at least 1`, or '' when the kind is served and could not be listed. */
  count: string;
}

export interface Summary {
  pods: number;
  isolatedIn: number;
  isolatedOut: number;
  open: number;
  hostNetwork: number;
  policies: number;
  defaultDeny: number;
  idle: number;
}

export interface NSRow {
  name: string;
  pods: number;
  isolatedIn: number;
  isolatedOut: number;
  policies: number;
  defaultDenyIn: boolean;
  defaultDenyOut: boolean;
}

export interface PolicyRow {
  /** NetworkPolicy, CiliumNetworkPolicy, CiliumClusterwideNetworkPolicy, Calico NetworkPolicy,
   *  Calico GlobalNetworkPolicy, AdminNetworkPolicy or BaselineAdminNetworkPolicy. */
  kind: string;
  namespace: string;
  name: string;
  /** '' selects every pod of the namespace. */
  selector: string;
  types: string[];
  selects: number;
  ingress: string[];
  egress: string[];
  broken: string[];
}

export interface PodRow {
  namespace: string;
  name: string;
  workload: string;
  ip: string;
  in: boolean;
  out: boolean;
  hostNetwork: boolean;
  policies: number;
}

export interface PodPage {
  total: number;
  offset: number;
  rows: PodRow[];
}

/** The policy engines the extra evaluates, as the page header and Settings → Extra name them. */
export const ENGINES: readonly string[] = ['Kubernetes NetworkPolicy', 'Cilium', 'Calico'];

export type NodeKind = 'namespace' | 'workload' | 'cidr' | 'anyone' | 'others' | 'entity' | 'unevaluated';

/** A rule's kind: a deny wins over every allow; a pass (Calico, AdminNetworkPolicy)
 *  hands the flow to the next tier or layer and allows nothing; a rule this version
 *  cannot decide (FQDNs, layer-7 rules…) is drawn apart, never as allow or deny. */
export type RuleKind = 'allow' | 'deny' | 'pass' | 'unevaluated';

/** What was read of Calico's policies, or of the AdminNetworkPolicies (`calicopolicy.go`, PolicyRead). */
export interface PolicyRead {
  served: boolean;
  policies: number;
  /** Each construct not evaluated, as "http: 2". */
  notEvaluated: string[];
}

/** What was read of Cilium's policies (`read.go`, CiliumRead). */
export interface CiliumRead {
  served: boolean;
  policies: number;
  hostPolicies: number;
  /** Each construct not evaluated, as "toFQDNs: 2". */
  notEvaluated: string[];
}

export interface GraphNode {
  id: string;
  label: string;
  kind: NodeKind;
  namespace?: string;
  pods: number;
  isolatedIn: number;
  isolatedOut: number;
  internal: number;
}

export interface GraphEdge {
  kind: RuleKind;
  from: string;
  to: string;
  side: 'ingress' | 'egress' | 'both';
  ports: string[];
  policies: string[];
  rules: number;
}

export interface Graph {
  scope: 'cluster' | 'namespace';
  nodes: GraphNode[];
  edges: GraphEdge[];
  folded: number;
  edgesTotal: number;
}

export interface Overview {
  mode: Mode;
  namespace: string;
  complete: boolean;
  notes: string[];
  cni: CNI;
  unevaluated: Unevaluated[];
  summary: Summary;
  namespaces: NSRow[];
  namespacesTotal: number;
  /** Every namespace with a pod or a policy, whatever the scope asked: the menu's choices. */
  choices?: string[];
  policies: PolicyRow[];
  policiesTotal: number;
  pods: PodPage;
  graph: Graph;
  /** One namespace's sides and workloads, under `?ns=` only. */
  view?: NamespaceView;
  cilium: CiliumRead;
  /** Calico's policies and tiers, evaluated beside the NetworkPolicies. */
  calico?: PolicyRead;
  /** AdminNetworkPolicies and the BaselineAdminNetworkPolicy. */
  admin?: PolicyRead;
}

export interface PolicyRef {
  namespace: string;
  name: string;
  /** Set for Calico and AdminNetworkPolicy objects only, as their kind. */
  policyKind?: string;
}

export interface RuleRef extends PolicyRef {
  kind: RuleKind;
  rule: number;
  text: string;
}

export type SideState = 'open' | 'allowed' | 'denied' | 'depends' | 'outside' | 'unevaluated';

export interface Side {
  direction: 'egress' | 'ingress';
  pod: string;
  state: SideState;
  isolating: PolicyRef[];
  decided?: RuleRef;
  reason: string;
  caveats: string[];
}

export interface Verdict {
  mode: Mode;
  answer: 'yes' | 'no' | 'depends' | 'unevaluated';
  summary: string;
  from: string;
  to: string;
  port: number;
  protocol: string;
  egress: Side;
  ingress: Side;
  complete: boolean;
  notes: string[];
  cni: CNI;
  unevaluated: Unevaluated[];
  /** NetworkPolicy YAML that would allow this flow, to copy; '' when none is
   *  offered. Nothing applies it. */
  policy: string;
  /** Set when the destination is a Service (`svc:namespace/name`). */
  service?: ServiceTarget;
}

export interface Backend {
  pod: string;
  /** The port the traffic reaches on this pod. */
  port: number;
  answer: Verdict['answer'];
  egress: Side;
  ingress: Side;
}

export interface ServiceTarget {
  name: string;
  port: number;
  portName?: string;
  protocol: string;
  targetPort: string;
  selector: string;
  backends: Backend[];
  total: number;
  capped: boolean;
}

/** The tiles' filters, as the route spells them. */
export type Show = '' | 'in' | 'out' | 'open' | 'host';

export interface Ask {
  ns: string;
  show: Show;
  q: string;
  offset: number;
}

export function showIn(value: string | null): Show {
  return value === 'in' || value === 'out' || value === 'open' || value === 'host' ? value : '';
}

const base = (cluster: string): string => `/x/${encodeURIComponent(cluster)}/${KEY}/`;

export function overviewPath(cluster: string, ask: Ask): string {
  const query = new URLSearchParams();
  if (ask.ns !== '') query.set('ns', ask.ns);
  if (ask.show !== '') query.set('show', ask.show);
  if (ask.q !== '') query.set('q', ask.q);
  if (ask.offset > 0) query.set('offset', String(ask.offset));
  const text = query.toString();
  return text === '' ? base(cluster) : `${base(cluster)}?${text}`;
}

export interface VerdictAsk {
  from: string;
  to: string;
  port: string;
  protocol: string;
}

export function verdictPath(cluster: string, ask: VerdictAsk): string {
  const query = new URLSearchParams({ from: ask.from, to: ask.to, port: ask.port });
  if (ask.protocol !== '' && ask.protocol !== 'TCP') query.set('protocol', ask.protocol);
  return `${base(cluster)}verdict?${query.toString()}`;
}

interface Problem {
  code?: unknown;
  message?: unknown;
  detail?: unknown;
}

/** A plain-text 404 here is the console's gate, never the route: every
 *  refusal the route writes is JSON with a code. */
const GATE_REFUSED =
  'The Network Inspector is not enabled for this cluster in this console. Turn it on in Settings > Extra; ' +
  "where the console runs inside a cluster, the chart's values must name it and you need the verb use on " +
  'extras.kubetower.dev/netinspect.';

/** The route's own sentence, in place of the bare status. */
export function said(error: unknown): unknown {
  if (error instanceof NotJsonError && error.status === 404) return new ApiError(404, error.excerpt ?? GATE_REFUSED);
  if (!(error instanceof ApiError) || error instanceof NotJsonError || error instanceof EnvelopeError) return error;
  const problem = error.body as Problem | undefined;
  if (typeof problem?.message !== 'string' || problem.message === '') return error;
  const detail = typeof problem.detail === 'string' && problem.detail !== '' ? ` (${problem.detail})` : '';
  return new ApiError(error.status, `${problem.message}${detail}`, error.body);
}

/** The code the route refused with, for the page to tell "you may not read
 *  the rules" from "the rules could not be read". */
export function problemCode(error: unknown): string {
  if (!(error instanceof ApiError)) return '';
  const problem = error.body as Problem | undefined;
  return typeof problem?.code === 'string' ? problem.code : '';
}

export async function getOverview(cluster: string, ask: Ask, signal?: AbortSignal): Promise<Overview> {
  let body: Overview;
  try {
    body = await request<Overview>(overviewPath(cluster, ask), signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (typeof body.summary !== 'object' || !Array.isArray(body.graph?.nodes) || !Array.isArray(body.pods?.rows)) {
    throw new EnvelopeError('The Network Inspector answered without its summary, its graph or its pods', body);
  }
  return body;
}

export async function getVerdict(cluster: string, ask: VerdictAsk, signal?: AbortSignal): Promise<Verdict> {
  let body: Verdict;
  try {
    body = await request<Verdict>(verdictPath(cluster, ask), signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (typeof body.answer !== 'string' || typeof body.egress !== 'object' || typeof body.ingress !== 'object') {
    throw new EnvelopeError('The Network Inspector answered a verdict without its two sides', body);
  }
  return body;
}

/**
 * One read per question; no interval. The rules change rarely, and a page
 * that polled would re-read every pod of the cluster for nothing. The page
 * says when it read them and offers "Check again".
 */
export const overviewQuery = (cluster: string, ask: Ask) =>
  queryOptions({
    queryKey: [KEY, cluster, 'overview', ask.ns, ask.show, ask.q, ask.offset],
    queryFn: ({ signal }) => getOverview(cluster, ask, signal),
    staleTime: 60_000,
    refetchOnWindowFocus: false,
  });

export const verdictQuery = (cluster: string, ask: VerdictAsk) =>
  queryOptions({
    queryKey: [KEY, cluster, 'verdict', ask.from, ask.to, ask.port, ask.protocol],
    queryFn: ({ signal }) => getVerdict(cluster, ask, signal),
    staleTime: 60_000,
    refetchOnWindowFocus: false,
  });

// --- a pod's panel --------------------------------------------------------------

export interface PeerView {
  kind: 'pods' | 'cidr' | 'anyone' | 'entity' | 'unevaluated';
  label: string;
  /** How many pods it designates now (pods peers only). */
  pods: number;
  /** A few of the namespaces those pods are in. */
  namespaces: string[];
}

export interface RuleView extends RuleRef {
  peers: PeerView[];
  ports: string[];
}

export interface Direction {
  isolated: boolean;
  policies: PolicyRef[];
  rules: RuleView[];
}

/** `GET /pods/{namespace}/{name}` (`pod.go`). */
export interface PodAnswer {
  mode: Mode;
  complete: boolean;
  notes: string[];
  cni: CNI;
  unevaluated: Unevaluated[];
  pod: PodRow;
  ingress: Direction;
  egress: Direction;
}

export function podPath(cluster: string, namespace: string, name: string): string {
  return `${base(cluster)}pods/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

export async function getPod(cluster: string, namespace: string, name: string, signal?: AbortSignal): Promise<PodAnswer> {
  let body: PodAnswer;
  try {
    body = await request<PodAnswer>(podPath(cluster, namespace, name), signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (typeof body.pod !== 'object' || typeof body.ingress !== 'object' || typeof body.egress !== 'object') {
    throw new EnvelopeError('The Network Inspector answered a pod without its two directions', body);
  }
  return body;
}

/** The tab's read: once when it opens, no interval. */
export const podQuery = (cluster: string, namespace: string, name: string) =>
  queryOptions({
    queryKey: [KEY, cluster, 'pod', namespace, name],
    queryFn: ({ signal }) => getPod(cluster, namespace, name, signal),
    staleTime: 60_000,
    refetchOnWindowFocus: false,
  });

/** The Pods list's column: `GET /cells`, every pod's isolation keyed
 *  `namespace/name`, one read for the whole list. */
export async function isolationCells(list: { cluster: string; kind: string }, signal: AbortSignal): Promise<Readonly<Record<string, ExtensionCell>>> {
  try {
    return await request<Record<string, ExtensionCell>>(`${base(list.cluster)}cells`, { signal });
  } catch (error) {
    throw said(error);
  }
}

/**
 * The tile on a pod's Details: its two isolations as badges, leading to the
 * pod's own page here (`PodPage`). A rejection draws nothing (the
 * tab says why); a pod the route does not know is no tile.
 */
export async function podShortcut({ cluster, namespace, name }: ExtensionTabProps, signal: AbortSignal): Promise<ExtensionShortcutTarget | null> {
  if (namespace === undefined || namespace === '') return null;
  let answer: PodAnswer;
  try {
    answer = await getPod(cluster, namespace, name, signal);
  } catch (error) {
    if (problemCode(error) === 'no_such_pod' || problemCode(error) === 'pod_finished') return null;
    throw error;
  }
  const p = answer.pod;
  const badges = p.hostNetwork ? ['host network'] : [p.in ? 'in isolated' : 'in open', p.out ? 'out isolated' : 'out open'];
  const policies = new Set([...answer.ingress.policies, ...answer.egress.policies].map((x) => x.name)).size;
  return {
    title: 'Network rules',
    badges,
    detail: policies === 0 ? 'no NetworkPolicy selects it' : `${String(policies)} ${policies === 1 ? 'policy selects' : 'policies select'} it`,
    to: `/c/${encodeURIComponent(cluster)}/${PAGE_PATH}/pods/${encodeURIComponent(p.namespace)}/${encodeURIComponent(p.name)}`,
  };
}

// --- observed traffic (Hubble, Calico) -------------------------------------------

/** Each answer about the flow source; `empty` alone means it was read and saw nothing. */
export type FlowsState = 'read' | 'empty' | 'absent' | 'refused' | 'tls' | 'unreachable';

export interface FlowRow {
  from: string;
  to: string;
  port: number;
  protocol: string;
  verdict: string;
  reason?: string;
  deniedBy: string[];
  count: number;
  last: string;
}

export interface FlowEdge {
  from: string;
  to: string;
  forwarded: number;
  dropped: number;
  ports: string[];
  deniedBy: string[];
}

/** `POST /flows?ns=` (`hubble.go`, `calico.go`). */
export interface Flows {
  mode: 'observed';
  /** Who saw it: Cilium's Hubble Relay or Calico's Goldmane; empty when no source was found. */
  source: 'hubble' | 'calico' | '';
  state: FlowsState;
  detail?: string;
  /** Seconds back from now. */
  window: number;
  read: number;
  /** Flows with an end in a namespace the person may not list were left out. */
  narrowed: boolean;
  truncated: boolean;
  forwarded: number;
  dropped: number;
  rows: FlowRow[];
  edges: FlowEdge[];
}

export function flowsPath(cluster: string, ns: string): string {
  return ns === '' ? `${base(cluster)}flows` : `${base(cluster)}flows?${new URLSearchParams({ ns }).toString()}`;
}

export async function getFlows(cluster: string, ns: string, signal?: AbortSignal): Promise<Flows> {
  let body: Flows;
  try {
    body = await request<Flows>(flowsPath(cluster, ns), signal === undefined ? { method: 'POST' } : { method: 'POST', signal });
  } catch (error) {
    throw said(error);
  }
  if (typeof body.state !== 'string' || !Array.isArray(body.rows) || !Array.isArray(body.edges)) {
    throw new EnvelopeError('The Network Inspector answered flows without their state, rows and edges', body);
  }
  return body;
}

/**
 * Observed traffic: one read when the page opens and on "Check again",
 * never on an interval - each read opens a tunnel to the flow source.
 */
export const flowsQuery = (cluster: string, ns: string) =>
  queryOptions({
    queryKey: [KEY, cluster, 'flows', ns],
    queryFn: ({ signal }) => getFlows(cluster, ns, signal),
    staleTime: 60_000,
    refetchOnWindowFocus: false,
    retry: false,
  });

/** A rule as a lane from a peer to the namespace's workloads it applies to. */
export interface Lane extends RuleView {
  targets: string[];
  pods: number;
}

/** One direction of a namespace (`namespace.go`). */
export interface NamespaceSide {
  /** Workloads no policy isolates in this direction. */
  open: string[];
  openPods: number;
  lanes: Lane[];
  lanesTotal: number;
  /** Workloads isolated with nothing allowed. */
  closed: string[];
}

export interface WorkloadView {
  name: string;
  pods: number;
  isolatedIn: number;
  isolatedOut: number;
  hostNetwork: number;
  from: string[];
  to: string[];
}

export interface NamespaceView {
  ingress: NamespaceSide;
  egress: NamespaceSide;
  workloads: WorkloadView[];
  workloadsTotal: number;
}

// --- Hubble UI and Whisker -------------------------------------------------------

/** The Service a UI is served by, for the console's own port-forward (`uis.go`). */
export interface UITunnel {
  namespace: string;
  service: string;
  port: number;
  portName?: string;
  appProtocol?: string;
}

/** One flow UI: an address configured for it, or the Service a tunnel can reach. */
export interface FlowUI {
  url?: string;
  tunnel?: UITunnel;
}

/** `GET /uis`: only the UIs this cluster can open are there. */
export interface FlowUIs {
  hubble?: FlowUI;
  whisker?: FlowUI;
}

export async function getFlowUIs(cluster: string, signal?: AbortSignal): Promise<FlowUIs> {
  try {
    return await request<FlowUIs>(`${base(cluster)}uis`, signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
}

/** Read once and kept: which UIs exist changes with an install, not a minute. */
export const uisQuery = (cluster: string) =>
  queryOptions({
    queryKey: [KEY, cluster, 'uis'],
    queryFn: ({ signal }) => getFlowUIs(cluster, signal),
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
    retry: false,
  });

/**
 * The address a configured UI opens at. Hubble UI takes the namespace as a
 * `namespace` parameter; Whisker has no filter that covers both ends of a flow
 * (its address takes `source_namespace` and `dest_namespace` apart), so it opens
 * on its general view.
 */
export function configuredHref(kind: 'hubble' | 'whisker', url: string, namespace: string): string {
  if (kind !== 'hubble' || namespace === '') return url;
  try {
    const out = new URL(url);
    out.searchParams.set('namespace', namespace);
    return out.toString();
  } catch {
    return url;
  }
}

/** The path and query a tunnelled UI opens at, after the forward's address. */
export function tunnelSuffix(kind: 'hubble' | 'whisker', namespace: string): string {
  if (kind !== 'hubble' || namespace === '') return '/';
  return `/?${new URLSearchParams({ namespace }).toString()}`;
}
