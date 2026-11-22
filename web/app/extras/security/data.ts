import { queryOptions } from '@tanstack/react-query';
import type { PolicyCounts, WorkloadPolicies } from './policyData';
import { ApiError, EnvelopeError, LIST_PAGE, NotJsonError, request } from '@kubetower/ui';

import type { Components, Signature } from './supply';

/**
 * What the routes of `extras/security` answer, typed as the Go writes them,
 * and the pure functions the pages filter with.
 *
 * **Four states for a source, and the types keep them.** A report kind is
 * `read`, `absent` (its CRD is not served), `denied` for this person, or
 * `unreadable`; a read narrowed to the namespaces the person may list is
 * `read` with `complete` false. A workload no report covers carries no
 * `scanned` date, and is never drawn as clean.
 */

export const KEY = 'security';

/** Where the pages live, relative to `/c/:cluster`. The Go routes answer
 *  JSON at `/x/{cluster}/security/…`, a different address. */
export const PAGE_PATH = `x/${KEY}`;

/** `off`: the kind is served and holds no report, so its scanner is off. */
export type State = 'read' | 'absent' | 'denied' | 'unreadable' | 'off';
export type WorkloadSourceKind = 'vulnerabilityreports' | 'configauditreports' | 'exposedsecretreports';
export type ClusterSourceKind = 'clustervulnerabilityreports' | 'clusterconfigauditreports' | 'infraassessmentreports' | 'clusterinfraassessmentreports';
export type RbacSourceKind = 'rbacassessmentreports' | 'clusterrbacassessmentreports';
/** The supply chain's two: the SBOM reports, and the admission's verdict read on the pods. */
export type SupplySourceKind = 'sbomreports' | 'signatures';
export type PolicySourceKind =
  | 'policyreports.wgpolicyk8s.io'
  | 'clusterpolicyreports.wgpolicyk8s.io'
  | 'reports.openreports.io'
  | 'clusterreports.openreports.io'
  | 'constraints.gatekeeper.sh'
  | 'clusterpolicies.kyverno.io'
  | 'policies.kyverno.io'
  | 'validatingpolicies.policies.kyverno.io'
  | 'imagevalidatingpolicies.policies.kyverno.io'
  | 'namespacedvalidatingpolicies.policies.kyverno.io'
  | 'namespacedimagevalidatingpolicies.policies.kyverno.io';
export type SourceKind = WorkloadSourceKind | ClusterSourceKind | RbacSourceKind | PolicySourceKind | SupplySourceKind | 'clustercompliancereports';

export interface Source {
  kind: SourceKind;
  state: State;
  complete: boolean;
  note?: string;
  reports: number;
}

export interface Counts {
  critical: number;
  high: number;
  medium: number;
  low: number;
  unknown: number;
}

export interface WorkloadRef {
  namespace: string;
  /** Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, CronJob, Pod. */
  kind: string;
  name: string;
}

export interface Workload extends WorkloadRef {
  images: string[];
  counts: Counts;
  /** null: no config audit covers it. */
  config: Counts | null;
  /** null: no exposed-secret report covers it. */
  secrets: number | null;
  exposed: boolean;
  exposure?: string;
  /** Absent: no vulnerability report covers it - not scanned, never clean. */
  scanned?: string;
  /** null: no SBOM report covers it. */
  components?: Components | null;
  /** null: no pod of it carries the admission's verdict - unknown, never unsigned. */
  signature?: Signature | null;
}

export interface Overview {
  cluster: string;
  read: string;
  operator: boolean;
  sources: Source[];
  totals: Counts;
  affected: { critical: number; high: number; medium: number };
  imageCount: number;
  exposedCount: number;
  secretCount: number;
  namespaces: { namespace: string; counts: Counts; workloads: number }[];
  images: { image: string; counts: Counts; workloads: number }[];
  workloads: Workload[];
  total: number;
  capped: boolean;
  sbom?: Source;
  signatures?: Source;
  /** Workloads with an image whose verification failed; with every image passed. */
  signatureFailed?: number;
  verified?: number;
}

export type Severity = 'CRITICAL' | 'HIGH' | 'MEDIUM' | 'LOW' | 'UNKNOWN';

export interface Cve {
  id: string;
  severity: Severity;
  score?: number;
  pkg: string;
  installed: string;
  fixed: string;
  title: string;
  link?: string;
  container: string;
}

export interface Container {
  container: string;
  image: string;
  os?: string;
  eosl?: boolean;
  scanned?: string;
  counts: Counts;
}

export interface Check {
  id: string;
  title: string;
  severity: string;
  category?: string;
}

export interface ExposedSecret {
  rule: string;
  title: string;
  category: string;
  severity: string;
  target: string;
}

/** What `/brief` answers about one object of a list: whether a vulnerability
 *  report covers it, and then the workload it was bound to, its counts, its
 *  image and the console's address of its page. */
export interface Brief {
  scanned: boolean;
  workload?: WorkloadRef;
  counts?: Counts;
  image?: string;
  href?: string;
  /** The workload's non-passing policy results, counted; absent without a policy engine. */
  policies?: PolicyCounts;
  signature?: Signature;
}

export interface Detail {
  cluster: string;
  read: string;
  workload: WorkloadRef;
  sources: Source[];
  containers: Container[];
  exposure: string[];
  cves: Cve[];
  cvesTotal: number;
  checks: Check[];
  secrets: ExposedSecret[];
  /** What the policy engines found about the workload; absent without one. */
  policies?: WorkloadPolicies;
  signature?: Signature | null;
}

/* -------------------------------------------------------------------------- */
/* Reading                                                                     */
/* -------------------------------------------------------------------------- */

export class SecurityProblem extends ApiError {
  readonly problem: string;
  constructor(status: number, code: string, message: string, body: unknown) {
    super(status, message, body);
    this.name = 'SecurityProblem';
    this.problem = code;
  }
}

/** The console's gate answers a switched-off extra with plain text. */
const GATE_REFUSED =
  'Security Center is not enabled for this cluster in this console. Turn it on in Settings > Extra; ' +
  "where the console runs inside a cluster, the chart's values must name it and you need the verb use on " +
  'extras.kubetower.dev/security.';

interface ProblemBody {
  code?: unknown;
  message?: unknown;
  detail?: unknown;
}

/** An error as the pages draw it: the route's problem when there is one. */
export function said(error: unknown): unknown {
  if (error instanceof NotJsonError && error.status === 404) {
    return new SecurityProblem(404, 'gate', error.excerpt ?? GATE_REFUSED, undefined);
  }
  if (!(error instanceof ApiError) || error instanceof NotJsonError || error instanceof EnvelopeError) return error;
  const body = error.body as ProblemBody | undefined;
  if (typeof body?.message !== 'string' || body.message === '') return error;
  const detail = typeof body.detail === 'string' && body.detail !== '' ? ` ${body.detail}` : '';
  return new SecurityProblem(error.status, typeof body.code === 'string' ? body.code : '', `${body.message}${detail}`, error.body);
}

export const base = (cluster: string) => `/x/${encodeURIComponent(cluster)}/${KEY}/`;

export function overviewPath(cluster: string, ns: string): string {
  return ns === '' ? base(cluster) : `${base(cluster)}?${new URLSearchParams({ ns }).toString()}`;
}

export function detailPath(cluster: string, w: WorkloadRef): string {
  return `${base(cluster)}workload/${encodeURIComponent(w.namespace)}/${encodeURIComponent(w.kind)}/${encodeURIComponent(w.name)}`;
}

/** The kinds whose panel and list Security Center adds to: the ones that run pods. */
export const PANEL_KINDS = ['pods', 'deployments', 'statefulsets', 'daemonsets', 'replicasets', 'jobs', 'cronjobs'] as const;

/** One object of a list, as `kind` spells it in the console's address (`pods`). */
export function briefPath(cluster: string, kind: string, namespace: string, name: string): string {
  return `${base(cluster)}brief/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}?${new URLSearchParams({ kind }).toString()}`;
}

/** Every row of one list, in one read. */
export function cellsPath(cluster: string, kind: string): string {
  return `${base(cluster)}cells?${new URLSearchParams({ kind }).toString()}`;
}

/** The console's address of the page. */
export const pageHref = (cluster: string) => `/c/${encodeURIComponent(cluster)}/${PAGE_PATH}`;

/** The console's address of a workload's page. */
export function workloadHref(cluster: string, w: WorkloadRef): string {
  return `${pageHref(cluster)}/${encodeURIComponent(w.namespace)}/${encodeURIComponent(w.kind)}/${encodeURIComponent(w.name)}`;
}

const PLURAL: Readonly<Record<string, string>> = {
  Deployment: 'deployments',
  StatefulSet: 'statefulsets',
  DaemonSet: 'daemonsets',
  ReplicaSet: 'replicasets',
  Job: 'jobs',
  CronJob: 'cronjobs',
  Pod: 'pods',
};

/** The console's own panel of the workload, where its kind has a list. */
export function objectHref(cluster: string, w: WorkloadRef): string | undefined {
  const plural = PLURAL[w.kind];
  if (plural === undefined) return undefined;
  return `/c/${encodeURIComponent(cluster)}/${plural}/${encodeURIComponent(w.namespace)}/${encodeURIComponent(w.name)}`;
}

export async function read<T>(path: string, signal: AbortSignal | undefined, check: (body: T) => boolean, what: string): Promise<T> {
  let body: T;
  try {
    body = await request<T>(path, signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (!check(body)) throw new EnvelopeError(`Security Center answered ${what} in a shape this page does not know`, body);
  return body;
}

export function getOverview(cluster: string, ns: string, signal?: AbortSignal): Promise<Overview> {
  return read<Overview>(
    overviewPath(cluster, ns),
    signal,
    (b) => typeof b.operator === 'boolean' && Array.isArray(b.sources) && Array.isArray(b.workloads) && typeof b.totals === 'object',
    'its overview',
  );
}

export function getDetail(cluster: string, w: WorkloadRef, signal?: AbortSignal): Promise<Detail> {
  return read<Detail>(
    detailPath(cluster, w),
    signal,
    (b) => Array.isArray(b.containers) && Array.isArray(b.cves) && Array.isArray(b.checks) && Array.isArray(b.secrets),
    'a workload',
  );
}

/** One page of workloads' distinct failed checks, keyed `namespace/Kind/name`; null where none was read. */
export type ConfigCounts = Readonly<Record<string, Counts | null>>;

export const configKey = (w: WorkloadRef) => `${w.namespace}/${w.kind}/${w.name}`;

export function configPath(cluster: string, rows: readonly WorkloadRef[]): string {
  const q = new URLSearchParams();
  for (const w of rows) q.append('w', configKey(w));
  return `${base(cluster)}config?${q.toString()}`;
}

export function getConfig(cluster: string, rows: readonly WorkloadRef[], signal?: AbortSignal): Promise<ConfigCounts> {
  return read<ConfigCounts>(configPath(cluster, rows), signal, (b) => typeof b === 'object' && b !== null && !Array.isArray(b), 'its config checks');
}

export function getBrief(cluster: string, kind: string, namespace: string, name: string, signal?: AbortSignal): Promise<Brief> {
  return read<Brief>(briefPath(cluster, kind, namespace, name), signal, (b) => typeof b.scanned === 'boolean', 'an object');
}

/** One report about no workload, as its table row tells it. */
export interface FindingRow {
  source: SourceKind;
  kind: string;
  namespace?: string;
  name: string;
  image?: string;
  counts: Counts;
  scanned?: string;
}

/** `GET /cluster`: the reports about the cluster itself, the worst first. */
export interface Findings {
  cluster: string;
  read: string;
  sources: Source[];
  rows: FindingRow[];
  totals: Counts;
  total: number;
  capped: boolean;
  notes: string[];
  byKind: Partial<Record<SourceKind, number>>;
}

export const clusterPath = (cluster: string) => `${base(cluster)}cluster`;
export const rbacPath = (cluster: string) => `${base(cluster)}rbac`;
export const compliancePath = (cluster: string) => `${base(cluster)}compliance`;

/** One control of a standard: its failures, or null for a manual control. */
export interface Control {
  id: string;
  name: string;
  severity: string;
  fail: number | null;
}

export interface ComplianceReport {
  name: string;
  title: string;
  version?: string;
  pass: number;
  fail: number;
  updated?: string;
  controls: Control[];
  controlsTotal: number;
}

/** `GET /compliance`: each standard's controls, from its summary. */
export interface Compliance {
  cluster: string;
  read: string;
  source: Source;
  reports: ComplianceReport[];
}

export function getRbac(cluster: string, signal?: AbortSignal): Promise<Findings> {
  return read<Findings>(rbacPath(cluster), signal, (b) => Array.isArray(b.sources) && Array.isArray(b.rows), 'its RBAC reports');
}

export function getCompliance(cluster: string, signal?: AbortSignal): Promise<Compliance> {
  return read<Compliance>(compliancePath(cluster), signal, (b) => typeof b.source === 'object' && Array.isArray(b.reports), 'its compliance reports');
}

export function getCluster(cluster: string, signal?: AbortSignal): Promise<Findings> {
  return read<Findings>(clusterPath(cluster), signal, (b) => Array.isArray(b.sources) && Array.isArray(b.rows), 'its cluster reports');
}

/** An answer is fresh for a minute; nothing is asked on an interval. */
export const STALE_MS = 60_000;
/** A page left is forgotten after five minutes. */
export const KEEP_MS = 5 * 60_000;

/**
 * One cache entry per scope. **No `refetchInterval`**: a scan moves when
 * trivy-operator rescans, in hours, and a page on a hidden tab must not keep
 * reading. The signal cancels the read when the page goes.
 */
export function overviewQuery(cluster: string, ns: string) {
  return queryOptions({
    queryKey: [KEY, 'overview', cluster, ns] as const,
    queryFn: ({ signal }) => getOverview(cluster, ns, signal),
    staleTime: STALE_MS,
    gcTime: KEEP_MS,
    refetchOnWindowFocus: false,
  });
}

export function detailQuery(cluster: string, w: WorkloadRef) {
  return queryOptions({
    queryKey: [KEY, 'workload', cluster, w.namespace, w.kind, w.name] as const,
    queryFn: ({ signal }) => getDetail(cluster, w, signal),
    staleTime: STALE_MS,
    gcTime: KEEP_MS,
    refetchOnWindowFocus: false,
  });
}

/** The reports about no workload: the cluster's own (`cluster`) or RBAC (`rbac`). */
export type FindingsWhich = 'cluster' | 'rbac';

export function findingsQuery(cluster: string, which: FindingsWhich) {
  return queryOptions({
    queryKey: [KEY, which, cluster] as const,
    queryFn: ({ signal }) => (which === 'rbac' ? getRbac(cluster, signal) : getCluster(cluster, signal)),
    staleTime: STALE_MS,
    gcTime: KEEP_MS,
    refetchOnWindowFocus: false,
  });
}

export const clusterQuery = (cluster: string) => findingsQuery(cluster, 'cluster');
export const rbacQuery = (cluster: string) => findingsQuery(cluster, 'rbac');

export function complianceQuery(cluster: string) {
  return queryOptions({
    queryKey: [KEY, 'compliance', cluster] as const,
    queryFn: ({ signal }) => getCompliance(cluster, signal),
    staleTime: STALE_MS,
    gcTime: KEEP_MS,
    refetchOnWindowFocus: false,
  });
}

/**
 * The distinct failed checks of the rows on screen, at most one page: a
 * report's own summary counts a check once per place it failed, so the
 * overview's figure is replaced by this one, read for those rows alone.
 */
export function configQuery(cluster: string, rows: readonly WorkloadRef[]) {
  return queryOptions({
    queryKey: [KEY, 'config', cluster, rows.map(configKey).join(' ')] as const,
    queryFn: ({ signal }) => getConfig(cluster, rows, signal),
    staleTime: STALE_MS,
    gcTime: KEEP_MS,
    refetchOnWindowFocus: false,
  });
}

export function briefQuery(cluster: string, kind: string, namespace: string, name: string) {
  return queryOptions({
    queryKey: [KEY, 'brief', cluster, kind, namespace, name] as const,
    queryFn: ({ signal }) => getBrief(cluster, kind, namespace, name, signal),
    staleTime: STALE_MS,
    gcTime: KEEP_MS,
    refetchOnWindowFocus: false,
  });
}

/* -------------------------------------------------------------------------- */
/* Filtering                                                                   */
/* -------------------------------------------------------------------------- */

/** Rows drawn at once, in every table. */
export const PAGE_ROWS = LIST_PAGE;

/** The tiles, as the address names them: one at a time. */
export type Tile = 'critical' | 'high' | 'medium' | 'images' | 'exposed' | 'secrets' | 'unsigned' | 'verified' | '';

/** The filters the address carries: `?sev=`, `?images=1`, `?exposed=1`,
 *  `?secrets=1`, `?signature=fail|pass`, `?image=`, `?ns=`, `?q=`. */
export interface Filters {
  tile: Tile;
  image: string;
  q: string;
}

export function tileOf(params: URLSearchParams): Tile {
  const sev = params.get('sev');
  if (sev === 'critical' || sev === 'high' || sev === 'medium') return sev;
  if (params.get('images') === '1') return 'images';
  if (params.get('exposed') === '1') return 'exposed';
  if (params.get('secrets') === '1') return 'secrets';
  if (params.get('signature') === 'fail') return 'unsigned';
  if (params.get('signature') === 'pass') return 'verified';
  return '';
}

/** The address with `tile` pressed, every other tile released. */
export function withTile(params: URLSearchParams, tile: Tile): URLSearchParams {
  const out = new URLSearchParams(params);
  for (const name of ['sev', 'images', 'exposed', 'secrets', 'signature']) out.delete(name);
  if (tile === 'critical' || tile === 'high' || tile === 'medium') out.set('sev', tile);
  else if (tile === 'unsigned') out.set('signature', 'fail');
  else if (tile === 'verified') out.set('signature', 'pass');
  else if (tile !== '') out.set(tile, '1');
  return out;
}

const severe = (c: Counts) => c.critical + c.high > 0;

export function passesTile(w: Workload, tile: Tile): boolean {
  switch (tile) {
    case 'critical':
      return w.counts.critical > 0;
    case 'high':
      return w.counts.high > 0;
    case 'medium':
      return w.counts.medium > 0;
    case 'images':
      return severe(w.counts);
    case 'exposed':
      return w.exposed && severe(w.counts);
    case 'secrets':
      return (w.secrets ?? 0) > 0;
    case 'unsigned':
      return w.signature?.verdict === 'fail';
    case 'verified':
      return w.signature?.verdict === 'pass';
    default:
      return true;
  }
}

/** The workloads the filters keep, in the server's order (worst first). */
export function filterWorkloads(all: readonly Workload[], f: Filters): Workload[] {
  const q = f.q.trim().toLowerCase();
  return all.filter(
    (w) =>
      passesTile(w, f.tile) &&
      (f.image === '' || w.images.includes(f.image)) &&
      (q === '' ||
        w.name.toLowerCase().includes(q) ||
        w.namespace.toLowerCase().includes(q) ||
        w.kind.toLowerCase().includes(q) ||
        w.images.some((i) => i.toLowerCase().includes(q))),
  );
}

export interface CveFilters {
  severity: Severity | '';
  fixable: boolean;
}

export function filterCves(all: readonly Cve[], f: CveFilters): Cve[] {
  if (f.severity === '' && !f.fixable) return [...all];
  return all.filter((c) => (f.severity === '' || c.severity === f.severity) && (!f.fixable || c.fixed !== ''));
}

/** One page of a list, and where it starts. */
export function pageOf<T>(all: readonly T[], page: number): { rows: T[]; page: number; pages: number } {
  const pages = Math.max(1, Math.ceil(all.length / PAGE_ROWS));
  const at = Math.min(Math.max(page, 0), pages - 1);
  return { rows: all.slice(at * PAGE_ROWS, (at + 1) * PAGE_ROWS), page: at, pages };
}

export const total = (c: Counts) => c.critical + c.high + c.medium + c.low + c.unknown;

/** "3m", "2h", "4d": how long ago, from the answer's own clock. */
export function ago(at: string, now: string): string {
  const ms = Date.parse(now) - Date.parse(at);
  if (!Number.isFinite(ms)) return '—';
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86_400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86_400)}d`;
}

export const n = (v: number) => v.toLocaleString('en-US');
