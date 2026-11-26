import { queryOptions } from '@tanstack/react-query';
import { ApiError, EnvelopeError, NotJsonError, request } from '@kubetower/ui';

/**
 * What the routes of `extras/storage` answer, typed as the Go writes them
 * (`read.go`, `model.go`, `fill.go`, `snapshots.go`, `routes.go`).
 *
 * **Every figure is the server's**: the fill percentages, their levels, the
 * orphans and the counts. The page places what it is given.
 */

export const KEY = 'storage';
export const PAGE_PATH = `x/${KEY}`;

export type SectionState = 'read' | 'absent' | 'refused' | 'failed';
export type Level = 'ok' | 'warn' | 'bad';
export type FillState = 'measured' | 'shared' | 'unmounted' | 'unread';
export type Mount = 'mounted' | 'unmounted' | 'unknown';
export type Orphan = 'unmounted' | 'workloadGone' | 'released' | 'available' | 'unusedClass';
export type SourceState = 'read' | 'partial' | 'unreadable' | 'none';

export interface Section {
  state: SectionState;
  message?: string;
  detail?: string;
}

export interface Fill {
  state: FillState;
  usedBytes?: number;
  capacityBytes?: number;
  percent?: number;
  level?: Level;
}

export interface Snapshot {
  name: string;
  at?: string;
  ready: boolean;
  class?: string;
  sizeBytes?: number;
}

export interface Claim {
  namespace: string;
  name: string;
  phase: string;
  class?: string;
  volume?: string;
  requestedBytes: number;
  capacityBytes: number;
  accessModes: string[];
  volumeMode?: string;
  reclaim?: string;
  created: string;
  mount: Mount;
  pods: string[];
  morePods?: number;
  workload?: { kind: string; name: string };
  orphan?: Orphan;
  fill: Fill;
  snapshot?: Snapshot;
  snapshots: number;
}

export interface Volume {
  name: string;
  phase: string;
  capacityBytes: number;
  accessModes: string[];
  reclaim: string;
  class?: string;
  driver?: string;
  claim?: string;
  created: string;
  orphan?: Orphan;
}

export interface Class {
  name: string;
  provisioner: string;
  reclaim: string;
  binding: string;
  expansion: boolean;
  default: boolean;
  created: string;
  claims: number;
  volumes: number;
  orphan?: Orphan;
}

export interface FillSource {
  state: SourceState;
  asked: number;
  answered: number;
  message?: string;
  detail?: string;
  /** What gave the figures, in the order asked: the metrics backend
   *  (`prometheus`, `victoriametrics`), then `kubelet` (#91). */
  via?: string[];
}

/** How each source of the fill figures is named. */
export const VIA_WORDS: Readonly<Record<string, string>> = {
  prometheus: 'Prometheus',
  victoriametrics: 'VictoriaMetrics',
  kubelet: 'kubelet',
};

export interface SnapshotSection extends Section {
  total: number;
  ready: number;
  /** -1 in a namespace, and when it could not be read. */
  contents: number;
  classes: { name: string; driver: string; deletionPolicy: string; default: boolean }[];
}

export interface Counts {
  claims: number;
  bound: number;
  pending: number;
  lost: number;
  volumes: number;
  classes: number;
  orphans: number;
  orphanClaims: number;
  orphanVolumes: number;
  orphanClasses: number;
  retainedOrphans: number;
  measured: number;
  /** Claims on a share: measured, with no percentage. */
  shared: number;
  /** Mounted claims no kubelet gave a figure for. */
  unread: number;
  warn: number;
  bad: number;
  snapshotted: number;
  requestedBytes: number;
  provisionedBytes: number;
}

export interface Overview {
  namespace: string;
  now: string;
  complete: boolean;
  notes: string[];
  counts: Counts;
  claims: Claim[];
  shown: number;
  volumes: Volume[];
  volumesRead: Section;
  classes: Class[];
  classesRead: Section;
  mountsRead: Section;
  fill: FillSource;
  snapshots: SnapshotSection;
}

export interface ClaimAnswer {
  now: string;
  claim: Claim;
  volume?: Volume;
  class?: Class;
  fill: FillSource;
  mountsRead: Section;
  snapshots: SnapshotSection;
  history: Snapshot[];
}

export interface PodAnswer {
  now: string;
  claims: Claim[];
  fill: FillSource;
  snapshots: SnapshotSection;
}

/** A refusal or failure the route wrote, with its code. */
export class StorageProblem extends ApiError {
  readonly problem: string;
  readonly detail: string | undefined;
  constructor(status: number, code: string, message: string, detail: string | undefined, body: unknown) {
    super(status, message, body);
    this.name = 'StorageProblem';
    this.problem = code;
    this.detail = detail;
  }
  get refused(): boolean {
    return this.problem === 'list_forbidden' || this.problem === 'gate';
  }
}

/** The console's gate answers a switched-off extra with plain text. */
const GATE_REFUSED = 'Storage Inspector is off for this cluster';

interface Problem {
  code?: unknown;
  message?: unknown;
  detail?: unknown;
}

function said(error: unknown): unknown {
  if (error instanceof NotJsonError && error.status === 404) {
    return new StorageProblem(404, 'gate', GATE_REFUSED, error.excerpt, undefined);
  }
  if (!(error instanceof ApiError) || error instanceof NotJsonError || error instanceof EnvelopeError) return error;
  const problem = error.body as Problem | undefined;
  if (typeof problem?.message !== 'string' || typeof problem.code !== 'string') return error;
  const detail = typeof problem.detail === 'string' && problem.detail !== '' ? problem.detail : undefined;
  return new StorageProblem(error.status, problem.code, problem.message, detail, error.body);
}

function base(cluster: string): string {
  return `/x/${encodeURIComponent(cluster)}/${KEY}/`;
}

export function overviewPath(cluster: string, ns: string): string {
  return ns === '' ? base(cluster) : `${base(cluster)}?${new URLSearchParams({ ns }).toString()}`;
}

async function read<T>(path: string, signal: AbortSignal | undefined): Promise<T> {
  try {
    return await request<T>(path, signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
}

export async function getOverview(cluster: string, ns: string, signal?: AbortSignal): Promise<Overview> {
  const body = await read<Overview>(overviewPath(cluster, ns), signal);
  if (!Array.isArray(body.claims) || typeof body.counts !== 'object') {
    throw new EnvelopeError('Storage Inspector answered without its claims and its counts', body);
  }
  return body;
}

export function claimPath(cluster: string, namespace: string, name: string): string {
  return `${base(cluster)}claim/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

export function podPath(cluster: string, namespace: string, name: string): string {
  return `${base(cluster)}pod/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

/** `null` when the route has no such claim. */
export async function getClaim(cluster: string, namespace: string, name: string, signal?: AbortSignal): Promise<ClaimAnswer | null> {
  try {
    return await read<ClaimAnswer>(claimPath(cluster, namespace, name), signal);
  } catch (error) {
    if (error instanceof StorageProblem && error.problem === 'no_claim') return null;
    throw error;
  }
}

export async function getPod(cluster: string, namespace: string, name: string, signal?: AbortSignal): Promise<PodAnswer> {
  const body = await read<PodAnswer>(podPath(cluster, namespace, name), signal);
  if (!Array.isArray(body.claims)) throw new EnvelopeError('Storage Inspector answered this pod without its claims', body);
  return body;
}

/** An answer is fresh for a minute; nothing is asked on an interval. */
export const STALE_MS = 60_000;
export const KEEP_MS = 5 * 60_000;

export function overviewQuery(cluster: string, ns: string) {
  return queryOptions({
    queryKey: [KEY, cluster, ns] as const,
    queryFn: ({ signal }) => getOverview(cluster, ns, signal),
    staleTime: STALE_MS,
    gcTime: KEEP_MS,
    refetchOnWindowFocus: false,
  });
}

export function claimQuery(cluster: string, namespace: string, name: string) {
  return queryOptions({
    queryKey: [KEY, cluster, 'claim', namespace, name] as const,
    queryFn: ({ signal }) => getClaim(cluster, namespace, name, signal),
    staleTime: STALE_MS,
    gcTime: KEEP_MS,
    refetchOnWindowFocus: false,
  });
}

export function podQuery(cluster: string, namespace: string, name: string) {
  return queryOptions({
    queryKey: [KEY, cluster, 'pod', namespace, name] as const,
    queryFn: ({ signal }) => getPod(cluster, namespace, name, signal),
    staleTime: STALE_MS,
    gcTime: KEEP_MS,
    refetchOnWindowFocus: false,
  });
}

/** The tiles' filters, as `?show=` spells them. */
export const SHOWS = ['all', 'orphans', 'full', 'unmeasured', 'unsnapshotted', 'volumes', 'classes'] as const;
export type Show = (typeof SHOWS)[number];

export function showOf(raw: string | null): Show {
  return (SHOWS as readonly string[]).includes(raw ?? '') ? (raw as Show) : 'all';
}

/**
 * Whether how full the claims are is known: once one claim was measured. A
 * kubelet that answered with no claim's figure - a driver reporting no volume
 * stats - knows nothing either, and nothing is graded on it.
 */
export function fillKnown(data: Pick<Overview, 'counts'>): boolean {
  return data.counts.measured + data.counts.shared > 0;
}

export function passes(c: Claim, show: Show): boolean {
  switch (show) {
    case 'orphans':
      return c.orphan !== undefined;
    case 'full':
      return c.fill.level === 'warn' || c.fill.level === 'bad';
    case 'unmeasured':
      return c.fill.state === 'unread';
    case 'unsnapshotted':
      return c.snapshots === 0;
    default:
      return true;
  }
}

const UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'] as const;

/** Bytes as a person reads them: `20 GiB`, `1.5 TiB`. */
export function bytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B';
  let value = n;
  let unit = 0;
  while (value >= 1024 && unit < UNITS.length - 1) {
    value /= 1024;
    unit++;
  }
  const shown = value >= 10 || unit === 0 ? Math.round(value).toString() : (Math.round(value * 10) / 10).toString();
  return `${shown} ${UNITS[unit] ?? 'B'}`;
}

/** How long ago, from the answer's own clock: `3d`, `5h`. */
export function age(iso: string | undefined, now: string): string {
  if (iso === undefined || iso === '') return '—';
  const ms = Date.parse(now) - Date.parse(iso);
  if (!Number.isFinite(ms)) return '—';
  const minutes = Math.max(0, Math.floor(ms / 60_000));
  if (minutes < 60) return `${String(minutes)}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${String(hours)}h`;
  return `${String(Math.floor(hours / 24))}d`;
}

export const ORPHAN_WORDS: Readonly<Record<Orphan, string>> = {
  unmounted: 'Unmounted',
  workloadGone: 'Workload gone',
  released: 'No claim',
  available: 'Never bound',
  unusedClass: 'Unused',
};

export const ORPHAN_HINTS: Readonly<Record<Orphan, string>> = {
  unmounted: 'No pod mounts this claim',
  workloadGone: 'No pod mounts it; named like a StatefulSet claim, and no StatefulSet here owns it',
  released: 'Its claim was deleted',
  available: 'No claim was ever bound to it',
  unusedClass: 'No claim and no volume uses it',
};

/** An object's address in the console, cluster-scoped when namespace is empty. */
export function objectHref(cluster: string, kind: string, namespace: string, name: string): string {
  const parts = namespace === '' ? [kind, name] : [kind, namespace, name];
  return `/c/${encodeURIComponent(cluster)}/${parts.map((p) => encodeURIComponent(p)).join('/')}`;
}

export function pageHref(cluster: string, show?: Show): string {
  const path = `/c/${encodeURIComponent(cluster)}/${PAGE_PATH}`;
  return show === undefined || show === 'all' ? path : `${path}?${new URLSearchParams({ show }).toString()}`;
}
