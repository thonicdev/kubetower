import { queryOptions } from '@tanstack/react-query';
import { ApiError, EnvelopeError, NotJsonError, request } from '@kubetower/ui';

import { gateUnknownCluster } from '../gate';

/**
 * What the routes of `extras/redis` answer, and what the pages make of it.
 *
 * Every failure is a problem with a `code` the page branches on and a `kind`
 * that says whether it is a refusal (a right or a credential said no), a
 * failure (a read that did not happen) or an absence (it is not there). The
 * three are drawn differently, and nothing here branches on a sentence.
 *
 * **No password is ever in these shapes.** A credential is its provenance -
 * Secret, key, source, pod - and the routes never send a value; the pages
 * never put one in an address either.
 */

export const KEY = 'redis';

export interface Door {
  namespace: string;
  service: string;
  port: number;
  portName?: string;
  /** Why the port was offered: the guess, said on every door. */
  why: string;
  /** What this Service says of the pods behind it (inventory only). */
  role?: Role;
  roleFrom?: RoleSource;
}

/** A member's or a door's role in its deployment. */
export type Role = 'primary' | 'replica' | 'headless' | 'sentinel';

/**
 * What a role was read from, most trusted first: the server's own `INFO
 * replication` (`info`, once probed), the Service's labels and selector, the
 * labels of every pod behind it, its having no cluster IP (`spec`), its name,
 * or the port it was found on (a Sentinel's).
 */
export type RoleSource = 'info' | 'labels' | 'pods' | 'spec' | 'name' | 'port';

/** One actual Redis and every Service that reaches it. */
export interface Backend {
  key: string;
  pods: string[];
  /** A pod, "N pods: …", nothing behind it, or what is behind it was not read. */
  behind: string;
  ready: number;
  reachable: boolean;
  endpointless: boolean;
  doors: Door[];
}

/** One backend inside a deployment, with what it is to the deployment. */
export interface Member extends Backend {
  role?: Role;
  roleFrom?: RoleSource;
  /** Reached on a Sentinel port only: drawn, never opened or probed. */
  sentinel?: boolean;
  workloads: string[];
}

/** What a deployment's members were folded on, most trusted first. */
export type Evidence = 'workload' | 'pods' | 'release' | 'name';

/**
 * One installation of a Redis or a Valkey: every Service and every set of
 * pods that belongs to it. Folded on the evidence it
 * names - never across namespaces, never two primaries.
 */
export interface Deployment {
  key: string;
  name: string;
  /** Empty for a deployment of one member. `name` alone is a hint. */
  evidence: Evidence[];
  release?: string;
  chart?: string;
  workloads: string[];
  members: Member[];
}

/** An ExternalName Service to a Redis outside the cluster: listed, never probed or opened. */
export interface External {
  namespace: string;
  service: string;
  host: string;
  /** Absent when the Service declares no port, or several nothing tells apart. */
  port?: number;
  portName?: string;
  why: string;
}

export interface Inventory {
  scope: string;
  complete: boolean;
  note?: string;
  slicesNote?: string;
  /** The namespaces grouped without their pods, and why. */
  podsNotes?: string[];
  services: number;
  /** The servers or pod sets inside the deployments, Sentinels apart. */
  members: number;
  /** Deployments. */
  redises: number;
  namespaces: { name: string; deployments: Deployment[] }[];
  /** ExternalName Services to a Redis outside the cluster. */
  external?: External[];
}

export interface Server {
  /** As the server reports itself: `Redis` or `Valkey`. */
  engine: string;
  version: string;
  /** `standalone`, `cluster` or `sentinel`. */
  mode: string;
  databases: number;
  /** The server's own `INFO replication` answer. */
  role?: 'primary' | 'replica';
  /** How many replicas a primary has attached right now. */
  replicas?: number;
  note?: string;
}

/** Where a password came from. Never what it is. */
export interface Credential {
  secret: string;
  key: string;
  source: string;
  pod: string;
}

export interface Overview {
  keys: number;
  usedMemory?: string;
  keyspace: { index: number; keys: number; expires: number }[];
}

export interface KeyRow {
  name: string;
  /** The key's bytes, base64 (URL alphabet), when `name` could not carry them. */
  raw?: string;
  type: string;
  ttl: string;
  /** What `MEMORY USAGE` says the key costs, in bytes; absent when not said. */
  memory?: number;
}

export interface Scan {
  match: string;
  cursor: string;
  /** The cursor the server handed back; absent when the scan is done. */
  next?: string;
  done: boolean;
  keys: KeyRow[];
}

export interface KeyDetail {
  name: string;
  raw?: string;
  type: string;
  ttl: string;
  encoding?: string;
  memory?: string;
  length?: number;
  entries: Entry[];
  note?: string;
  /** The token that reads the next page; absent when this page was the last.
   *  Opaque here: it goes back to the route as `page`, as it came. */
  next?: string;
  /** How many elements the server reads to a page. */
  pageSize?: number;
}

/**
 * One element of a key. For a stream, one entry: `field` is its ID and
 * `value` the JSON text of its field/value pairs.
 *
 * `binary`: the element was not UTF-8, and `value` draws its bytes with
 * U+FFFD; `raw` then carries the bytes themselves (standard base64), which is
 * what the page decodes or draws in hex. `clipped`: the element (or a hash
 * field's name) was longer than the server's ceiling and only its start is
 * here; `bytes` is then the whole value's length.
 */
export interface Entry {
  field?: string;
  value: string;
  score?: string;
  binary?: boolean;
  raw?: string;
  clipped?: boolean;
  bytes?: number;
}

export interface InstanceAnswer {
  instance: Door;
  pod?: string;
  db: number;
  server: Server;
  credential?: Credential;
  overview?: Overview;
  scan?: Scan;
  key?: KeyDetail;
}

export interface ServiceAnswer {
  namespace: string;
  service: string;
  deployments: Deployment[];
  /** The namespace was grouped without its pods, and why. */
  podsNotes?: string[];
  /** Set, with no deployment, for an ExternalName Service to a Redis outside the cluster. */
  external?: External;
}

export type ProblemKind = 'refusal' | 'failure' | 'absent';

/** A route's problem, kept whole: the code to branch on, the kind to draw. */
export class RedisProblem extends Error {
  readonly status: number;
  readonly code: string;
  readonly kind: ProblemKind;
  readonly detail: string | undefined;

  constructor(status: number, code: string, kind: ProblemKind, message: string, detail?: string) {
    super(message);
    this.name = 'RedisProblem';
    this.status = status;
    this.code = code;
    this.kind = kind;
    this.detail = detail;
  }
}

/** The codes that mean "this instance wants a password this console could not
 *  use", spelled as `session.go` spells them. */
export const PASSWORD_CODES: readonly string[] = [
  'no_credential',
  'pod_unknown',
  'secret_denied',
  'secret_unreviewed',
  'credential_failed',
  'password_refused',
];

const base = (cluster: string) => `/x/${encodeURIComponent(cluster)}/${KEY}/`;

export function inventoryPath(cluster: string, scope: string): string {
  return scope === '' ? base(cluster) : `${base(cluster)}?${new URLSearchParams({ ns: scope }).toString()}`;
}

function instanceBase(cluster: string, door: Pick<Door, 'namespace' | 'service' | 'port'>): string {
  return `${base(cluster)}instances/${encodeURIComponent(door.namespace)}/${encodeURIComponent(door.service)}/${door.port}`;
}

/** The instance read: the parameters on the allowlist, and nothing else. */
export interface InstanceQuery {
  db: number;
  match: string;
  cursor: string;
}

export function instancePath(cluster: string, door: Pick<Door, 'namespace' | 'service' | 'port'>, q: InstanceQuery): string {
  const query = new URLSearchParams();
  if (q.db !== 0) query.set('db', String(q.db));
  if (q.match !== '') query.set('match', q.match);
  if (q.cursor !== '' && q.cursor !== '0') query.set('cursor', q.cursor);
  const text = query.toString();
  return text === '' ? instanceBase(cluster, door) : `${instanceBase(cluster, door)}?${text}`;
}

export function probePath(cluster: string, door: Pick<Door, 'namespace' | 'service' | 'port'>): string {
  return `${instanceBase(cluster, door)}/probe`;
}

/** A key's read; `page` is the token a previous page handed back, absent for the first. */
export function keyPath(cluster: string, door: Pick<Door, 'namespace' | 'service' | 'port'>, db: number, key: KeyRow, page?: string): string {
  const query = new URLSearchParams();
  if (db !== 0) query.set('db', String(db));
  if (key.raw !== undefined && key.raw !== '') query.set('raw', key.raw);
  else query.set('key', key.name);
  if (page !== undefined && page !== '') query.set('page', page);
  return `${instanceBase(cluster, door)}/key?${query.toString()}`;
}

export function servicePath(cluster: string, namespace: string, name: string): string {
  return `${base(cluster)}services/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

/**
 * A plain-text 404 at this address is the console's gate, never the route:
 * every refusal `extras/redis` writes is JSON with a code. The fallback: the gate's own
 * sentence, the `NotJsonError` excerpt, is shown when there is one.
 */
const GATE_REFUSED =
  'The Redis viewer is not enabled for this cluster in this console. Turn it on in Settings > Extra; ' +
  "where the console runs inside a cluster, the chart's values must name it and you need the verb use on " +
  'extras.kubetower.dev/redis.';

interface ProblemBody {
  code?: unknown;
  kind?: unknown;
  message?: unknown;
  detail?: unknown;
}

/** An error as the pages draw it: the route's problem when there is one. */
export function said(error: unknown): unknown {
  if (error instanceof NotJsonError && error.status === 404) {
    return new RedisProblem(404, 'gate', 'absent', error.excerpt ?? GATE_REFUSED);
  }
  // The gate answers a cluster the kubeconfig does not hold before the route
  // runs, in the console API's words: drawn as the route's own unknown cluster.
  const unknown = gateUnknownCluster(error);
  if (unknown !== undefined) return new RedisProblem(404, 'unknown_cluster', 'absent', unknown);
  if (!(error instanceof ApiError) || error instanceof NotJsonError || error instanceof EnvelopeError) return error;
  const body = error.body as ProblemBody | undefined;
  if (typeof body?.code !== 'string' || typeof body.message !== 'string') return error;
  const kind: ProblemKind = body.kind === 'refusal' || body.kind === 'absent' ? body.kind : 'failure';
  return new RedisProblem(
    error.status,
    body.code,
    kind,
    body.message,
    typeof body.detail === 'string' && body.detail !== '' ? body.detail : undefined,
  );
}

/**
 * The reads that dial - an instance, its probe, a key - are POSTs: each opens
 * a tunnel and may read the password, recorded
 * as the person's reveal, and a GET is what any cross-site link can make a
 * browser send. `request` adds the console's mutation header to a POST.
 */
type Method = 'GET' | 'POST';

async function read<T>(path: string, signal: AbortSignal | undefined, check: (body: T) => boolean, what: string, method: Method = 'GET'): Promise<T> {
  let body: T;
  try {
    body = await request<T>(path, signal === undefined ? { method } : { method, signal });
  } catch (error) {
    throw said(error);
  }
  if (!check(body)) throw new EnvelopeError(`The Redis viewer answered ${what} in a shape this page does not know`, body);
  return body;
}

export function getInventory(cluster: string, scope: string, signal?: AbortSignal): Promise<Inventory> {
  return read<Inventory>(
    inventoryPath(cluster, scope),
    signal,
    (b) => Array.isArray(b.namespaces) && typeof b.complete === 'boolean' && b.namespaces.every((n) => Array.isArray(n.deployments)),
    'its inventory',
  );
}

/**
 * One namespace's inventory, read once for a pod's two tiles (the deployment
 * tile and "not checked", `shortcut.ts`), which are asked side by side: a
 * second read would list the namespace's Services, slices and pods twice, and
 * could answer otherwise than the first and draw both tiles. A read, failed or
 * not, is shared for SHARED_READ_MS; a caller that goes away stops waiting, and
 * the read goes on for the other.
 */
const SHARED_READ_MS = 10_000;
const sharedReads = new Map<string, { at: number; read: Promise<Inventory> }>();

export function sharedInventory(cluster: string, scope: string, signal?: AbortSignal): Promise<Inventory> {
  const key = `${cluster}\u0000${scope}`;
  const now = Date.now();
  let entry = sharedReads.get(key);
  if (entry === undefined || now - entry.at > SHARED_READ_MS) {
    entry = { at: now, read: getInventory(cluster, scope) };
    // Rejected for every caller alike, and never left unhandled.
    entry.read.catch(() => undefined);
    sharedReads.set(key, entry);
  }
  const read = entry.read;
  if (signal === undefined) return read;
  if (signal.aborted) return Promise.reject(new DOMException('The read was canceled', 'AbortError'));
  return new Promise<Inventory>((resolve, reject) => {
    const stop = () => reject(new DOMException('The read was canceled', 'AbortError'));
    signal.addEventListener('abort', stop, { once: true });
    read.then(
      (value) => {
        signal.removeEventListener('abort', stop);
        resolve(value);
      },
      (error: unknown) => {
        signal.removeEventListener('abort', stop);
        reject(error instanceof Error ? error : new Error(String(error)));
      },
    );
  });
}

/** Forgets the shared reads: for tests. */
export function forgetSharedInventory(): void {
  sharedReads.clear();
}

const isInstance = (b: InstanceAnswer) => typeof b.server === 'object' && b.server !== null && typeof b.instance === 'object';

export function getInstance(cluster: string, door: Door | Pick<Door, 'namespace' | 'service' | 'port'>, q: InstanceQuery, signal?: AbortSignal): Promise<InstanceAnswer> {
  return read<InstanceAnswer>(instancePath(cluster, door, q), signal, isInstance, 'an instance', 'POST');
}

export function getProbe(cluster: string, door: Pick<Door, 'namespace' | 'service' | 'port'>, signal?: AbortSignal): Promise<InstanceAnswer> {
  return read<InstanceAnswer>(probePath(cluster, door), signal, isInstance, 'a probe', 'POST');
}

export function getKey(
  cluster: string,
  door: Pick<Door, 'namespace' | 'service' | 'port'>,
  db: number,
  key: KeyRow,
  signal?: AbortSignal,
  page?: string,
): Promise<InstanceAnswer> {
  return read<InstanceAnswer>(keyPath(cluster, door, db, key, page), signal, (b) => isInstance(b) && typeof b.key === 'object', 'a key', 'POST');
}

export function getService(cluster: string, namespace: string, name: string, signal?: AbortSignal): Promise<ServiceAnswer> {
  return read<ServiceAnswer>(servicePath(cluster, namespace, name), signal, (b) => Array.isArray(b.deployments), 'a Service');
}

export function inventoryQuery(cluster: string, scope: string) {
  return queryOptions({
    queryKey: [KEY, 'inventory', cluster, scope] as const,
    queryFn: ({ signal }) => getInventory(cluster, scope, signal),
  });
}

export function instanceQuery(cluster: string, door: Pick<Door, 'namespace' | 'service' | 'port'>, q: InstanceQuery) {
  return queryOptions({
    queryKey: [KEY, 'instance', cluster, door.namespace, door.service, door.port, q.db, q.match, q.cursor] as const,
    queryFn: ({ signal }) => getInstance(cluster, door, q, signal),
    // Each read is a port-forward and a SCAN against somebody's server: never
    // again on its own, only when the person asks.
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
}

export function keyQuery(cluster: string, door: Pick<Door, 'namespace' | 'service' | 'port'>, db: number, key: KeyRow) {
  return queryOptions({
    queryKey: [KEY, 'key', cluster, door.namespace, door.service, door.port, db, key.raw ?? '', key.name] as const,
    queryFn: ({ signal }) => getKey(cluster, door, db, key, signal),
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
}

export function serviceQuery(cluster: string, namespace: string, name: string) {
  return queryOptions({
    queryKey: [KEY, 'service', cluster, namespace, name] as const,
    queryFn: ({ signal }) => getService(cluster, namespace, name, signal),
  });
}

// --- the probes, and what the inventory's tiles count ----------------------

/** One row's probe: not asked, in flight, answered, or refused/failed. */
export type ProbeState =
  | { status: 'running' }
  | { status: 'answered'; answer: InstanceAnswer }
  | { status: 'failed'; error: unknown };

/** The state tiles, in the order they are drawn. */
export const STATES = ['reachable', 'unreachable', 'password', 'unhandled', 'notserving', 'unprobed'] as const;
export type StateTile = (typeof STATES)[number];

/**
 * Where one row puts it among the tiles.
 *
 * **A row with nothing to probe is `notserving`, never `unprobed`**: no pod is
 * behind it, or what is behind it could not be read, so it has no probe control
 * and "not probed yet" would be a count nothing on the page can ever move. A
 * row discovery no longer finds when probed is the same.
 * **An instance that did not answer is `unreachable`, never zero keys**: zero
 * would claim an empty server nobody read. One whose password this console
 * could not use is its own tile: the instance answered, and said no. A Cluster node and a
 * Sentinel answered, and this viewer reads single nodes.
 */
export function stateOf(backend: Pick<Backend, 'reachable'>, probe: ProbeState | undefined): StateTile {
  if (!backend.reachable) return 'notserving';
  if (probe === undefined || probe.status === 'running') return 'unprobed';
  if (probe.status === 'answered') {
    return probe.answer.server.mode === 'cluster' || probe.answer.server.mode === 'sentinel' ? 'unhandled' : 'reachable';
  }
  const error = probe.error;
  if (error instanceof RedisProblem && PASSWORD_CODES.includes(error.code)) return 'password';
  if (error instanceof RedisProblem && error.kind === 'absent') return 'notserving';
  return 'unreachable';
}

/** The engine a probe found, as the server reported itself; none before it answered. */
export function engineOf(probe: ProbeState | undefined): string | undefined {
  return probe?.status === 'answered' ? probe.answer.server.engine : undefined;
}

// --- the roles, and what the probes add to them ------------------------------

/** The members a probe can reach: every member but a Sentinel's. */
export function probeable(deployments: readonly Deployment[]): Member[] {
  return deployments.flatMap((d) => d.members.filter((m) => m.sentinel !== true));
}

/** A role as drawn: what it is, what said so, and what the probe added. */
export interface RoleSaid {
  role: Role | undefined;
  from: RoleSource | undefined;
  /** The pod the probe reached, when the role is the server's own. */
  pod?: string;
  /** What the labels or the name had said, when the server says otherwise. */
  guessed?: Role;
}

/**
 * The role to draw for a member. **The server's own answer wins once it has
 * been asked** (`INFO replication`, on the pod the probe reached): a label is
 * what a chart meant at install, and after a Sentinel failover the pod
 * labeled master may be a replica - so a disagreement is shown, not settled
 * quietly. A headless member stays headless: it is every pod, and the one the
 * probe reached speaks for itself only, which `pod` says.
 */
export function roleOf(member: Pick<Member, 'role' | 'roleFrom'>, probe: ProbeState | undefined): RoleSaid {
  const guess: RoleSaid = { role: member.role, from: member.roleFrom };
  if (probe?.status !== 'answered' || probe.answer.server.role === undefined) return guess;
  const server = probe.answer.server.role;
  const pod = probe.answer.pod;
  if (member.role === 'headless') return { ...guess, ...(pod === undefined ? {} : { pod }) };
  const out: RoleSaid = { role: server, from: 'info', ...(pod === undefined ? {} : { pod }) };
  if (member.role !== undefined && member.role !== server) out.guessed = member.role;
  return out;
}

export const ROLE_SOURCE: Readonly<Record<RoleSource, string>> = {
  info: 'the server (INFO replication)',
  labels: "the Service's labels",
  pods: 'the labels of its pods',
  spec: 'the Service having no cluster IP',
  name: "the Service's name only",
  port: 'the port it was found on',
};

export const EVIDENCE_WORDS: Readonly<Record<Evidence, string>> = {
  workload: 'the workloads owning their pods',
  pods: 'pods they share',
  release: 'their Helm or operator labels',
  name: 'their names',
};
