import { queryOptions } from '@tanstack/react-query';
import { ApiError, EnvelopeError, NotJsonError, request } from '@kubetower/ui';

/**
 * What the route of `extras/certmanager` answers, typed as the Go writes it
 * (`overview.go`, `model.go`).
 *
 * **Every figure is the server's.** Days left are computed there, at one clock,
 * so the tiles, the strip and the rows say the same number; the page places
 * what it is given and never re-dates a certificate.
 *
 * Three answers stay three: `installed: false` (cert-manager is not in the
 * cluster), a `CertManagerProblem` (refused, or could not be read), and an
 * installed answer with no certificate (empty).
 */

export const KEY = 'certmanager';
export const PAGE_PATH = `x/${KEY}`;

/** Days left, banded. `none` is never issued: no date at all. */
export type Band = 'expired' | 'week' | 'month' | 'later' | 'none';
export type SectionState = 'read' | 'absent' | 'refused' | 'failed';

export interface Certificate {
  namespace: string;
  name: string;
  secretName: string;
  dnsNames: string[];
  moreNames: number;
  issuerKind: string;
  issuerName: string;
  notAfter: string;
  renewalTime: string;
  /** `null` when never issued, which is not "expires today". */
  daysLeft: number | null;
  band: Band;
  ready: string;
  reason: string;
  message: string;
  renewalOverdue: boolean;
  failedAttempts: number;
}

export interface Issuer {
  kind: 'Issuer' | 'ClusterIssuer';
  namespace?: string;
  name: string;
  type: string;
  ready: string;
  reason: string;
  message: string;
  broken: boolean;
}

export interface Acme {
  kind: 'Order' | 'Challenge';
  namespace: string;
  name: string;
  state: string;
  reason: string;
  type: string;
  dnsName: string;
  certificate: string;
}

export interface Counts {
  total: number;
  expired: number;
  week: number;
  month: number;
  notReady: number;
  renewalOverdue: number;
  acmeFailing: number;
  issuersBroken: number;
}

export interface Overview {
  installed: boolean;
  message?: string;
  namespace: string;
  now: string;
  complete: boolean;
  notes: string[];
  counts: Counts;
  ring: { ready: number; notReady: number; unknown: number };
  /** Certificates expiring on each of the next 90 days, today first. */
  strip: number[];
  certificates: Certificate[];
  shown: number;
  issuers: { state: SectionState; message?: string; items: Issuer[]; total: number };
  acme: { state: SectionState; message?: string; items: Acme[]; inFlight: number };
}

/** A refusal or failure the route wrote, with its code. */
export class CertManagerProblem extends ApiError {
  readonly problem: string;
  readonly detail: string | undefined;
  constructor(status: number, code: string, message: string, detail: string | undefined, body: unknown) {
    super(status, message, body);
    this.name = 'CertManagerProblem';
    this.problem = code;
    this.detail = detail;
  }
  /** A refusal for the person, as opposed to a read that failed. */
  get refused(): boolean {
    return this.problem === 'list_forbidden';
  }
}

/** The console's gate answers a switched-off extra with plain text. */
const GATE_REFUSED =
  'Cert Manager is not enabled for this cluster in this console. Turn it on in Settings > Extra; ' +
  "where the console runs inside a cluster, the chart's values must name it and you need the verb use on " +
  'extras.kubetower.dev/certmanager.';

interface Problem {
  code?: unknown;
  message?: unknown;
  detail?: unknown;
}

function said(error: unknown): unknown {
  if (error instanceof NotJsonError && error.status === 404) {
    return new CertManagerProblem(404, 'gate', error.excerpt ?? GATE_REFUSED, undefined, undefined);
  }
  if (!(error instanceof ApiError) || error instanceof NotJsonError || error instanceof EnvelopeError) return error;
  const problem = error.body as Problem | undefined;
  if (typeof problem?.message !== 'string' || typeof problem.code !== 'string') return error;
  const detail = typeof problem.detail === 'string' && problem.detail !== '' ? problem.detail : undefined;
  return new CertManagerProblem(error.status, problem.code, problem.message, detail, error.body);
}

export function overviewPath(cluster: string, ns: string): string {
  const base = `/x/${encodeURIComponent(cluster)}/${KEY}/`;
  return ns === '' ? base : `${base}?${new URLSearchParams({ ns }).toString()}`;
}

export async function getOverview(cluster: string, ns: string, signal?: AbortSignal): Promise<Overview> {
  let body: Overview;
  try {
    body = await request<Overview>(overviewPath(cluster, ns), signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (typeof body.installed !== 'boolean' || !Array.isArray(body.certificates) || !Array.isArray(body.strip)) {
    throw new EnvelopeError('Cert Manager answered without its installed flag, its certificates and its strip', body);
  }
  return body;
}

/** An answer is fresh for a minute; nothing is asked on an interval. */
export const STALE_MS = 60_000;
/** A page left is forgotten after five minutes. */
export const KEEP_MS = 5 * 60_000;

/**
 * One cache entry per scope. **No `refetchInterval`**: a certificate's date
 * moves once in weeks, and a page on a hidden tab must not keep reading.
 */
export function overviewQuery(cluster: string, ns: string) {
  return queryOptions({
    queryKey: [KEY, cluster, ns] as const,
    queryFn: ({ signal }) => getOverview(cluster, ns, signal),
    staleTime: STALE_MS,
    gcTime: KEEP_MS,
    refetchOnWindowFocus: false,
  });
}

/** The filters the tiles apply, as `?show=` spells them. */
export const SHOWS = ['all', 'expired', 'week', 'month', 'notready', 'overdue', 'acme', 'issuers'] as const;
export type Show = (typeof SHOWS)[number];

export function showOf(raw: string | null): Show {
  return (SHOWS as readonly string[]).includes(raw ?? '') ? (raw as Show) : 'all';
}

/** Whether a certificate passes a certificate filter. */
export function passes(c: Certificate, show: Show): boolean {
  switch (show) {
    case 'expired':
      return c.band === 'expired';
    case 'week':
      return c.band === 'week';
    case 'month':
      return c.band === 'week' || c.band === 'month';
    case 'notready':
      return c.ready !== 'True';
    case 'overdue':
      return c.renewalOverdue;
    default:
      return true;
  }
}

export function plural(n: number, word: string): string {
  return `${String(n)} ${word}${n === 1 ? '' : 's'}`;
}

/** A condition's CamelCase reason as plain words: `DoesNotExist` is "Does not
 *  exist". Text that already has spaces is the server's sentence and stays. */
export function reasonWords(reason: string): string {
  if (!/^[A-Za-z0-9]+$/.test(reason)) return reason;
  return reason
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .split(' ')
    .map((w, i) => (i === 0 ? w : w.toLowerCase()))
    .join(' ');
}

/** Days left, as a person reads them. */
export function daysWord(c: Pick<Certificate, 'daysLeft' | 'band'>): string {
  if (c.daysLeft === null) return 'not issued';
  if (c.band === 'expired') {
    // The server counts an expiry under a day ago as -1, two days ago as -3.
    const ago = -c.daysLeft - 1;
    return ago === 0 ? 'expired today' : ago === 1 ? 'expired yesterday' : `expired ${String(ago)} days ago`;
  }
  if (c.daysLeft === 0) return 'expires today';
  return c.daysLeft === 1 ? '1 day left' : `${String(c.daysLeft)} days left`;
}

/** One cell of the console's list column, as the contract reads it. */
export interface Cell {
  text: string;
  tone?: string;
  hint?: string;
  href?: string;
}

/**
 * One link of an issuance chain. `found` is an object that was read; the rest
 * are answers about one that was not: `none` (the kind was read and holds no
 * such object), `refused` (the person may not read it), `failed` (the read
 * failed), `unserved` (the cluster has no such kind), `cut` (the read stopped
 * at its limit).
 */
export type LinkState = 'found' | 'none' | 'refused' | 'failed' | 'unserved' | 'cut';
export type LinkTone = 'ok' | 'warn' | 'bad' | 'neutral';

export interface ChainLink {
  kind: string;
  state: LinkState;
  namespace?: string;
  name?: string;
  tone: LinkTone;
  phase?: string;
  reason?: string;
  message?: string;
  type?: string;
  dnsName?: string;
  created?: string;
  changed?: string;
  conditions?: { type: string; status: string; reason?: string; message?: string }[];
}

/** Certificate, request, order, challenges and issuer, as the server read them. */
export interface Chain {
  request: ChainLink;
  order: ChainLink;
  challenges: ChainLink[];
  moreChallenges: number;
  issuer: ChainLink;
  /** The Secret's name: named, never opened. */
  secret: string;
  /** The kind of the first link that is not ok. */
  stuck?: string;
}

/** The certificates of one Certificate, Secret or Ingress. */
export interface ObjectAnswer {
  now: string;
  certificates: Certificate[];
  chains: Record<string, Chain>;
}

export async function getCells(cluster: string, kind: string, signal?: AbortSignal): Promise<Record<string, Cell>> {
  const path = `/x/${encodeURIComponent(cluster)}/${KEY}/cells?${new URLSearchParams({ kind }).toString()}`;
  try {
    return await request<Record<string, Cell>>(path, signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
}

export function objectPath(cluster: string, kind: string, namespace: string, name: string): string {
  return `/x/${encodeURIComponent(cluster)}/${KEY}/objects/${encodeURIComponent(kind)}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

/** `null` when no certificate cert-manager manages is behind the object. */
export async function getObject(cluster: string, kind: string, namespace: string, name: string, signal?: AbortSignal): Promise<ObjectAnswer | null> {
  let body: ObjectAnswer;
  try {
    body = await request<ObjectAnswer>(objectPath(cluster, kind, namespace, name), signal === undefined ? {} : { signal });
  } catch (error) {
    const problem = said(error);
    if (problem instanceof CertManagerProblem && problem.problem === 'no_certificate') return null;
    throw problem;
  }
  body.chains ??= {};
  if (!Array.isArray(body.certificates)) throw new EnvelopeError('Cert Manager answered this object without its certificates', body);
  return body;
}

export function objectQuery(cluster: string, kind: string, namespace: string, name: string) {
  return queryOptions({
    queryKey: [KEY, cluster, 'object', kind, namespace, name] as const,
    queryFn: ({ signal }) => getObject(cluster, kind, namespace, name, signal),
    staleTime: STALE_MS,
    gcTime: KEEP_MS,
    refetchOnWindowFocus: false,
  });
}

/** The extra's page, on one certificate. */
export function certificateHref(cluster: string, c: Pick<Certificate, 'namespace' | 'name'>): string {
  return `/c/${encodeURIComponent(cluster)}/${PAGE_PATH}?${new URLSearchParams({ cert: `${c.namespace}/${c.name}` }).toString()}`;
}
