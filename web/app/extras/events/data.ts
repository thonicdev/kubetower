import { queryOptions } from '@tanstack/react-query';
import { ApiError, EnvelopeError, NotJsonError, request } from '@kubetower/ui';

/**
 * What the routes of `extras/events` answer, typed as the Go writes them
 * (`aggregate.go`, `events.go`, `view.go`).
 *
 * **A source is `read`, `denied`, `unreviewable` or `failed`**, and the types
 * keep it: a tile whose source was not read is a refusal, never a zero, and an
 * empty cause list over a read source is a quiet cluster.
 */

export const KEY = 'events';

export type SourceState = 'read' | 'denied' | 'unreviewable' | 'failed';

export interface Section {
  state: SourceState;
  complete: boolean;
  note?: string;
}

export type ChangeKind = 'rollout' | 'rollback' | 'config' | 'scaling' | 'node-added' | 'node-notready';

export interface Change {
  id: string;
  kind: ChangeKind;
  objectKind: string;
  namespace?: string;
  name: string;
  time: string;
  revision?: number;
  imagesBefore?: string[];
  imagesAfter?: string[];
  manager?: string;
  detail?: string;
  referenced: boolean;
}

export interface ObjectGroup {
  kind: string;
  namespace: string;
  name: string;
  count: number;
  firstSeen: string;
  lastSeen: string;
  workloadKind?: string;
  workloadName?: string;
  node?: string;
  message: string;
  since?: string;
}

export interface Cause {
  reason: string;
  message: string;
  type: string;
  count: number;
  objects: number;
  kinds: string[];
  firstSeen: string;
  lastSeen: string;
  examples: string[];
  since?: string;
  groups: ObjectGroup[];
}

export interface Tile {
  count: number;
  state: SourceState;
}

export interface Tiles {
  warnings: Tile;
  crashLoop: Tile;
  oomKilled: Tile;
  failedScheduling: Tile;
  objectsTouched: Tile;
}

export interface PodState {
  namespace: string;
  name: string;
  node?: string;
  workloadKind?: string;
  workloadName?: string;
  crashLoop: boolean;
  oomKilled: boolean;
}

export interface Sources {
  events: Section;
  replicasets: Section;
  pods: Section;
  configmaps: Section;
  nodes: Section;
}

export interface ObjectRef {
  kind: string;
  namespace: string;
  name: string;
}

export interface Answer {
  cluster?: string;
  namespace: string;
  window: string;
  since: string;
  generatedAt: string;
  object?: ObjectRef;
  tiles: Tiles;
  causes: Cause[];
  changes: Change[];
  pods: PodState[];
  sources: Sources;
  truncated: boolean;
  complete: boolean;
  note?: string;
}

export interface Line {
  kind: string;
  namespace: string;
  name: string;
  warnings: number;
  lastChange: Change | null;
  lastChangeAgeSeconds: number | null;
  generatedAt: string;
  sources: Record<string, Section>;
  complete: boolean;
  note?: string;
}

export const WINDOWS = ['15m', '1h', '3h'] as const;
export type Window = (typeof WINDOWS)[number];
export const DEFAULT_WINDOW: Window = '1h';

/** How often an open view asks again. Only while it is on screen: the
 *  interval stops in a hidden window (`refetchIntervalInBackground: false`)
 *  and with the view that closes. */
export const POLL_MS = 30_000;
export const STALE_MS = 10_000;

/** The kinds that carry the tab and the line, as the console's address spells them. */
export const OBJECT_KINDS: readonly string[] = ['pods', 'deployments', 'statefulsets', 'daemonsets', 'replicasets', 'jobs'];

const base = (cluster: string) => `/x/${encodeURIComponent(cluster)}/${KEY}/`;

export function pagePath(cluster: string, window: string, namespace = ''): string {
  const query = new URLSearchParams({ window });
  if (namespace !== '') query.set('namespace', namespace);
  return `${base(cluster)}?${query.toString()}`;
}

export function objectPathOf(cluster: string, kind: string, namespace: string, name: string, window: string): string {
  const where = [kind, namespace, name].map(encodeURIComponent).join('/');
  return `${base(cluster)}object/${where}?${new URLSearchParams({ window }).toString()}`;
}

export function linePath(cluster: string, kind: string, namespace: string, name: string): string {
  return `${base(cluster)}line/${[kind, namespace, name].map(encodeURIComponent).join('/')}`;
}

const GATE_REFUSED = 'Event Intelligence is not turned on for this cluster.';

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

const isAnswer = (b: Answer) => Array.isArray(b.causes) && Array.isArray(b.changes) && typeof b.tiles?.warnings?.state === 'string';

export function getPage(cluster: string, window: string, signal?: AbortSignal, namespace = ''): Promise<Answer> {
  return read<Answer>(pagePath(cluster, window, namespace), signal, isAnswer, 'Event Intelligence answered without its tiles, its causes and its changes');
}

export function getObject(cluster: string, kind: string, namespace: string, name: string, window: string, signal?: AbortSignal): Promise<Answer> {
  return read<Answer>(objectPathOf(cluster, kind, namespace, name, window), signal, isAnswer, 'Event Intelligence answered this object without its causes and its changes');
}

export function getLine(cluster: string, kind: string, namespace: string, name: string, signal?: AbortSignal): Promise<Line> {
  return read<Line>(
    linePath(cluster, kind, namespace, name),
    signal,
    (b) => typeof b.warnings === 'number' && typeof b.sources === 'object',
    'Event Intelligence answered without its warnings',
  );
}

export function pageQuery(cluster: string, window: string) {
  return queryOptions({
    queryKey: [KEY, 'page', cluster, window] as const,
    queryFn: ({ signal }) => getPage(cluster, window, signal),
    staleTime: STALE_MS,
    refetchInterval: POLL_MS,
    refetchIntervalInBackground: false,
  });
}

/** The page read for one namespace: the route reviews it for the person, and its tiles are that namespace's. */
export function namespaceQuery(cluster: string, window: string, namespace: string) {
  return queryOptions({
    queryKey: [KEY, 'page', cluster, window, namespace] as const,
    queryFn: ({ signal }) => getPage(cluster, window, signal, namespace),
    staleTime: STALE_MS,
    refetchInterval: POLL_MS,
    refetchIntervalInBackground: false,
  });
}

export function objectQuery(cluster: string, kind: string, namespace: string, name: string, window: string) {
  return queryOptions({
    queryKey: [KEY, 'object', cluster, kind, namespace, name, window] as const,
    queryFn: ({ signal }) => getObject(cluster, kind, namespace, name, window, signal),
    staleTime: STALE_MS,
    refetchInterval: POLL_MS,
    refetchIntervalInBackground: false,
  });
}

/** "now", "3 min", "2 h", "4 d": how long ago, from the answer's own clock. */
export function ago(at: string, now: string): string {
  const ms = Date.parse(now) - Date.parse(at);
  if (!Number.isFinite(ms)) return '—';
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return 'now';
  if (s < 3600) return `${Math.floor(s / 60)} min`;
  if (s < 86_400) return `${Math.floor(s / 3600)} h`;
  return `${Math.floor(s / 86_400)} d`;
}
