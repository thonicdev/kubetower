import { queryOptions } from '@tanstack/react-query';
import { ApiError, EnvelopeError, NotJsonError, request } from '@kubetower/ui';

/** What `extras/dns` answers at `/x/{cluster}/dns/stats`, typed as `dns.go` writes it. A null is a figure the window did not hold. */

export const KEY = 'dns';

export const SPANS = ['15m', '1h', '6h'] as const;
export type Span = (typeof SPANS)[number];
export const DEFAULT_SPAN: Span = '1h';

export type State = 'read' | 'no-prometheus' | 'unreadable' | 'empty';

export interface Figures {
  qps: number;
  nxdomain: number | null;
  servfail: number | null;
  p50: number | null;
  p99: number | null;
  cacheHit: number | null;
}

export interface Pod {
  pod: string;
  node?: string;
  figures: Figures | null;
}

export interface Upstream {
  to: string;
  qps: number;
  errQps: number;
  p99: number | null;
  healthFailPs: number;
  answers: boolean;
  /** The zones the Corefile forwards to it, when it was read. */
  zones?: string[];
}

export type Points = [number, number][];

export interface Series {
  step: number;
  qps: Points;
  byRcode: Record<string, Points>;
  p50: Points;
  p99: Points;
  cacheHit: Points;
}

export interface Stats {
  state: State;
  detail?: string;
  span: string;
  namespace?: string;
  version?: string;
  partial?: string;
  readAt?: string;
  now?: Figures;
  series?: Series;
  pods: Pod[];
  upstreams: Upstream[];
}

/** Seconds between two reads of an open page: the span's step, never under 15 s. */
export const POLL_MS: Record<Span, number> = { '15m': 15_000, '1h': 30_000, '6h': 120_000 };

interface Problem {
  message?: string;
  detail?: string;
}

function said(error: unknown): unknown {
  if (error instanceof NotJsonError && error.status === 404) return new ApiError(404, error.excerpt ?? 'DNS is not turned on for this cluster.');
  if (!(error instanceof ApiError) || error instanceof NotJsonError || error instanceof EnvelopeError) return error;
  const problem = error.body as Problem | undefined;
  if (typeof problem?.message !== 'string' || problem.message === '') return error;
  return new ApiError(error.status, problem.message, error.body);
}

/** A read the cluster refused for the person, as against one that failed. */
export function isRefused(error: unknown): boolean {
  if (!(error instanceof ApiError) || error.status !== 403) return false;
  return (error.body as { code?: unknown } | undefined)?.code === 'list_forbidden';
}

export async function getStats(cluster: string, span: Span, signal?: AbortSignal): Promise<Stats> {
  const path = `/x/${encodeURIComponent(cluster)}/${KEY}/stats?${new URLSearchParams({ span }).toString()}`;
  let body: Stats;
  try {
    body = await request<Stats>(path, signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (typeof body.state !== 'string' || !Array.isArray(body.pods) || !Array.isArray(body.upstreams)) {
    throw new EnvelopeError('DNS answered without its state and its pods', body);
  }
  return body;
}

/**
 * One read per cluster and span. Asked again only while the page is on screen:
 * the interval stops in a hidden window and with the view that closes.
 */
export function statsQuery(cluster: string, span: Span) {
  return queryOptions({
    queryKey: [KEY, 'stats', cluster, span] as const,
    queryFn: ({ signal }) => getStats(cluster, span, signal),
    staleTime: POLL_MS[span] / 2,
    refetchInterval: POLL_MS[span],
    refetchIntervalInBackground: false,
  });
}
