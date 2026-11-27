import { queryOptions } from '@tanstack/react-query';
import { ApiError, EnvelopeError, NotJsonError, request } from '@kubetower/ui';

/**
 * What the routes of `extras/cnpg` answer, and what the pages make of it.
 * Every shape here is the Go side's (`cluster.go`, `model.go`, `overview.go`,
 * `pods.go`); a field renamed there is renamed here.
 *
 * Every failure is a problem with a `code`, and the pages branch on the code,
 * never on a sentence: not installed, refused for the person and a read that
 * failed are three different drawings (`kindOf`).
 */

export const KEY = 'cnpg';

/** Where the pages live, relative to `/c/:cluster`. The Go routes answer
 *  JSON at `/x/{cluster}/cnpg/…`, a different address. */
export const PAGE_PATH = `x/${KEY}`;

export type Health = 'healthy' | 'switchover' | 'degraded' | 'failing' | 'progressing';
export type InstanceState = 'healthy' | 'replicating' | 'failed' | 'unknown';

export interface Instance {
  name: string;
  role: 'primary' | 'replica';
  state: InstanceState;
  timeline?: number;
  target?: boolean;
}

export interface Archive {
  state: 'none' | 'ok' | 'failing' | 'pending';
  message?: string;
}

export interface BackupMark {
  name: string;
  phase: string;
  method?: string;
  createdAt?: string;
  startedAt?: string;
  stoppedAt?: string;
  error?: string;
}

export interface BackupState {
  state: 'none' | 'never' | 'failing' | 'late' | 'ok';
  expected: boolean;
  late: boolean;
  lastSuccess?: string;
  lastFailure?: string;
  message?: string;
  scheduled: number;
  recent: BackupMark[];
  total: number;
}

export interface Certificate {
  secret: string;
  expiresAt: string;
}

export interface Certs {
  state: 'ok' | 'warn' | 'bad' | 'unknown';
  earliest?: string;
  list: Certificate[];
}

export interface Summary {
  namespace: string;
  name: string;
  phase: string;
  phaseReason?: string;
  health: Health;
  expected: number;
  ready: number;
  primary?: string;
  target?: string;
  timeline?: number;
  promotedAt?: string;
  instances: Instance[];
  archive: Archive;
  backup: BackupState;
  certs: Certs;
  image?: string;
  nodesUsed?: number;
  poolers: number;
  storageBytes?: number;
  walStorageBytes?: number;
  createdAt?: string;
}

export interface Counts {
  clusters: number;
  healthy: number;
  degraded: number;
  switchover: number;
  archiveFailing: number;
  backupLate: number;
  notBackedUp: number;
  certsSoon: number;
}

/** A secondary read: read, not served here, refused, or failed. */
export interface Source {
  kind: string;
  state: 'read' | 'absent' | 'refused' | 'failed';
  note?: string;
  complete: boolean;
}

export interface Overview {
  now: string;
  complete: boolean;
  note?: string;
  counts: Counts;
  clusters: Summary[];
  sources: Source[];
  staleAfterHours: number;
  certWarnDays: number;
}

export interface PodFacts {
  phase: string;
  ready: boolean;
  restarts: number;
  node?: string;
  startedAt?: string;
  roleLabel?: string;
}

export interface Disk {
  claim: string;
  usedBytes: number;
  capacityBytes: number;
  declaredBytes?: number;
  trustworthy: boolean;
}

export interface Member extends Instance {
  pod?: PodFacts;
  data?: Disk;
  wal?: Disk;
}

export interface Condition {
  type: string;
  status: string;
  reason?: string;
  message?: string;
  at?: string;
}

export interface ClusterEvent {
  type?: string;
  reason?: string;
  message?: string;
  count?: number;
  at?: string;
}

export interface Scheduled {
  name: string;
  schedule: string;
  suspended: boolean;
  method?: string;
  last?: string;
  next?: string;
}

export interface Pooler {
  name: string;
  type?: string;
  instances: number;
  phase?: string;
  reason?: string;
}

export interface Declared {
  kind: string;
  name: string;
  pgName?: string;
  applied?: boolean;
  message?: string;
}

/** One `kubectl` line to copy. The console runs none of them. */
export interface Command {
  act: 'backup' | 'promote' | 'restart' | 'reload';
  instance?: string;
  line: string;
}

export interface Detail {
  now: string;
  summary: Summary;
  members: Member[];
  conditions: Condition[];
  events: ClusterEvent[];
  backups: BackupMark[];
  scheduled: Scheduled[];
  poolers: Pooler[];
  declared: Declared[];
  writeService?: string;
  readService?: string;
  diskNote?: string;
  sources: Source[];
  commands: Command[];
}

export interface PodRoles {
  pods: Record<string, { role: string; cluster: string }>;
  complete: boolean;
  note?: string;
}

export interface PodAnswer {
  pod: string;
  role: string;
  of: Summary;
}

/** How a problem is drawn: the operator is not there, the person may not
 *  read it, or the read failed. */
export type ProblemKind = 'absent' | 'refusal' | 'failure';

const ABSENT = new Set(['kind_unknown', 'kind_not_served', 'unknown_cluster', 'not_found', 'not_instance', 'gate']);
const REFUSED = new Set(['list_forbidden', 'list_unreviewed', 'secret_denied', 'secret_unreviewed', 'tunnel_refused', 'pod_unknown', 'exec_forbidden', 'exec_unreviewed', 'read_only', 'database_denied']);
const ABSENT_SQL = new Set(['no_credential', 'no_such_table', 'no_such_database']);

export class CnpgProblem extends Error {
  readonly status: number;
  readonly code: string;
  readonly kind: ProblemKind;
  readonly detail: string | undefined;

  constructor(status: number, code: string, message: string, detail?: string) {
    super(message);
    this.name = 'CnpgProblem';
    this.status = status;
    this.code = code;
    this.kind = ABSENT.has(code) || ABSENT_SQL.has(code) ? 'absent' : REFUSED.has(code) ? 'refusal' : 'failure';
    this.detail = detail;
  }
}

/** A plain-text 404 at this address is the console's gate, never the route:
 *  every answer `extras/cnpg` writes is JSON. */
const GATE_REFUSED =
  'Postgres (CNPG) is not enabled for this cluster in this console. Turn it on in Settings > Extra; ' +
  "where the console runs inside a cluster, the chart's values must name it and you need the verb use on " +
  'extras.kubetower.dev/cnpg.';

interface ProblemBody {
  code?: unknown;
  message?: unknown;
  detail?: unknown;
}

/** An error as the pages draw it: the route's problem when there is one. */
export function said(error: unknown): unknown {
  if (error instanceof NotJsonError && error.status === 404) {
    return new CnpgProblem(404, 'gate', error.excerpt ?? GATE_REFUSED);
  }
  if (!(error instanceof ApiError) || error instanceof NotJsonError || error instanceof EnvelopeError) return error;
  const body = error.body as ProblemBody | undefined;
  if (typeof body?.code !== 'string' || typeof body.message !== 'string') return error;
  return new CnpgProblem(error.status, body.code, body.message, typeof body.detail === 'string' && body.detail !== '' ? body.detail : undefined);
}

const base = (cluster: string) => `/x/${encodeURIComponent(cluster)}/${KEY}/`;

export const overviewPath = (cluster: string) => base(cluster);
export const clusterPath = (cluster: string, namespace: string, name: string) =>
  `${base(cluster)}clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
export const podRolesPath = (cluster: string) => `${base(cluster)}pods`;
export const podPath = (cluster: string, namespace: string, name: string) =>
  `${base(cluster)}pods/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;

/** The console's address of a cluster's page. */
export const clusterHref = (cluster: string, namespace: string, name: string) =>
  `/c/${encodeURIComponent(cluster)}/${PAGE_PATH}/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;

async function read<T>(path: string, signal: AbortSignal | undefined, check: (body: T) => boolean, what: string, post?: unknown): Promise<T> {
  let body: T;
  try {
    body = await request<T>(path, { ...(signal === undefined ? {} : { signal }), ...(post === undefined ? {} : { method: 'POST', body: post }) });
  } catch (error) {
    throw said(error);
  }
  if (!check(body)) throw new EnvelopeError(`Postgres (CNPG) answered ${what} in a shape this page does not know`, body);
  return body;
}

export function getOverview(cluster: string, signal?: AbortSignal): Promise<Overview> {
  return read<Overview>(overviewPath(cluster), signal, (b) => Array.isArray(b.clusters) && typeof b.counts === 'object', 'its overview');
}

export function getCluster(cluster: string, namespace: string, name: string, signal?: AbortSignal): Promise<Detail> {
  return read<Detail>(clusterPath(cluster, namespace, name), signal, (b) => Array.isArray(b.members) && typeof b.summary === 'object' && Array.isArray(b.commands), 'a cluster');
}

export function getPodRoles(cluster: string, signal?: AbortSignal): Promise<PodRoles> {
  return read<PodRoles>(podRolesPath(cluster), signal, (b) => typeof b.pods === 'object' && b.pods !== null, 'the pod roles');
}

export function getPod(cluster: string, namespace: string, name: string, signal?: AbortSignal): Promise<PodAnswer> {
  return read<PodAnswer>(podPath(cluster, namespace, name), signal, (b) => typeof b.of === 'object' && b.of !== null, 'a pod');
}

/** How often an open page reads again. TanStack Query stops the interval
 *  when the page unmounts, and while the browser tab is hidden
 *  (`refetchIntervalInBackground` is false). */
export const REFRESH_MS = 30_000;

export function overviewQuery(cluster: string) {
  return queryOptions({
    queryKey: [KEY, 'overview', cluster] as const,
    queryFn: ({ signal }) => getOverview(cluster, signal),
    refetchInterval: REFRESH_MS,
    refetchIntervalInBackground: false,
  });
}

export function clusterQuery(cluster: string, namespace: string, name: string) {
  return queryOptions({
    queryKey: [KEY, 'cluster', cluster, namespace, name] as const,
    queryFn: ({ signal }) => getCluster(cluster, namespace, name, signal),
    refetchInterval: REFRESH_MS,
    refetchIntervalInBackground: false,
  });
}

// --- the SQL door (`extras/cnpg/sql.go`) ------------------------------------------

/** `GET …/clusters/{ns}/{name}/stats`: read through the cluster's own
 *  password, once, when the section opens. */
export interface Stats {
  database: string;
  /** Whether the server's certificate was checked against the cluster's CA. */
  verified: boolean;
  version: string;
  startedAt?: string;
  connections: number;
  maxConnections: number;
  cacheHit?: number;
  commits: number;
  rollbacks: number;
  deadlocks: number;
  databases: { name: string; bytes: number }[];
  replicas: { name: string; state: string; lagBytes?: number }[];
}

export interface TableRow {
  schema: string;
  name: string;
  kind: 'table' | 'partitioned' | 'view' | 'materialized' | 'foreign';
  /** Postgres' estimate; -1 before the first ANALYZE. */
  rows: number;
  bytes: number;
}

export interface Tables {
  database: string;
  /** The databases the application user may connect to, `database` among them. */
  databases?: string[];
  verified: boolean;
  schemas: string[];
  tables: TableRow[];
  complete: boolean;
}

export interface Cell {
  null?: boolean;
  text?: string;
  cut?: boolean;
  /** A bytea: its length, and its first bytes in hex. */
  bytes?: number;
  hex?: string;
}

/** How a table's pages follow each other: by its primary key, by ctid for a
 *  table without one, by offset for a view (capped). */
export type PageKey = 'primary' | 'ctid' | 'offset';

export interface Rows {
  schema: string;
  table: string;
  columns: { name: string; type: string; primary?: boolean }[];
  rows: Cell[][];
  verified: boolean;
  columnsTotal: number;
  more: boolean;
  /** How the next page is asked for, and the cursor that asks it (opaque,
   *  sent back in a request's body, never in an address). */
  key?: PageKey;
  next?: string;
  /** The first row's position, from 0. */
  start?: number;
  /** The page stopped short of 100 rows to stay under 4 MiB. */
  bounded?: boolean;
}

const sqlBase = (cluster: string, namespace: string, name: string) => `${clusterPath(cluster, namespace, name)}`;

export function getStats(cluster: string, namespace: string, name: string, signal?: AbortSignal): Promise<Stats> {
  return read<Stats>(`${sqlBase(cluster, namespace, name)}/stats`, signal, (b) => Array.isArray(b.databases), 'its statistics');
}

export function getTables(cluster: string, namespace: string, name: string, database: string, signal?: AbortSignal): Promise<Tables> {
  const query = database === '' ? '' : `?db=${encodeURIComponent(database)}`;
  return read<Tables>(`${sqlBase(cluster, namespace, name)}/tables${query}`, signal, (b) => Array.isArray(b.tables), 'its tables');
}

/** One page of a table. The cursor is the previous page's `next`, sent in the
 *  body: it may hold a primary key's values, which have no place in an
 *  address. */
export function getRows(
  cluster: string,
  namespace: string,
  name: string,
  page: { database: string; schema: string; table: string; cursor: string },
  signal?: AbortSignal,
): Promise<Rows> {
  const path = `${sqlBase(cluster, namespace, name)}/tables/${encodeURIComponent(page.schema)}/${encodeURIComponent(page.table)}`;
  return read<Rows>(path, signal, (b) => Array.isArray(b.rows) && Array.isArray(b.columns), 'a table', { database: page.database, cursor: page.cursor });
}

// --- replication lag (`extras/cnpg/lag.go`) -----------------------------------------

/** One standby the primary streams to. A lag is in seconds and absent when
 *  Postgres reported none: that is not measured, never 0. */
export interface ReplicaLag {
  name: string;
  state: string;
  sync: string;
  write?: number;
  flush?: number;
  replay?: number;
}

/** `GET …/clusters/{ns}/{name}/lag`: one fixed psql query run in the primary,
 *  only when asked. An empty `replicas` is an answer: nobody streams. */
export interface Lag {
  primary: string;
  replicas: ReplicaLag[];
}

export function getLag(cluster: string, namespace: string, name: string, signal?: AbortSignal): Promise<Lag> {
  return read<Lag>(`${clusterPath(cluster, namespace, name)}/lag`, signal, (b) => Array.isArray(b.replicas) && typeof b.primary === 'string', 'its replication lag');
}

/** Read once each, when asked: a tunnel and a Secret read every time, so no
 *  interval, and never again on focus. */
const ONCE = { staleTime: Infinity, refetchOnWindowFocus: false, refetchOnReconnect: false, retry: false } as const;

export function statsQuery(cluster: string, namespace: string, name: string) {
  return queryOptions({ queryKey: [KEY, 'stats', cluster, namespace, name] as const, queryFn: ({ signal }) => getStats(cluster, namespace, name, signal), ...ONCE });
}

export function tablesQuery(cluster: string, namespace: string, name: string, database = '') {
  return queryOptions({
    queryKey: [KEY, 'tables', cluster, namespace, name, database] as const,
    queryFn: ({ signal }) => getTables(cluster, namespace, name, database, signal),
    ...ONCE,
  });
}

export function rowsQuery(cluster: string, namespace: string, name: string, page: { database: string; schema: string; table: string; cursor: string }) {
  return queryOptions({
    queryKey: [KEY, 'rows', cluster, namespace, name, page.database, page.schema, page.table, page.cursor] as const,
    queryFn: ({ signal }) => getRows(cluster, namespace, name, page, signal),
    ...ONCE,
    gcTime: 60_000,
  });
}

/** Disabled until the button is pressed: a lag is an exec in the primary, never run on load. */
export function lagQuery(cluster: string, namespace: string, name: string) {
  return queryOptions({ queryKey: [KEY, 'lag', cluster, namespace, name] as const, queryFn: ({ signal }) => getLag(cluster, namespace, name, signal), ...ONCE });
}
