import { queryOptions } from '@tanstack/react-query';

import { KEEP_MS, KEY, PAGE_ROWS, STALE_MS, base, read, type Source, type WorkloadRef } from './data';

/**
 * The Policy Center's half of `extras/security`: what Kyverno and Gatekeeper
 * wrote, as `GET /policies` and `GET /policies/{engine}/{name}` answer it.
 * Every type mirrors the Go answer.
 */

/** Non-passing results by outcome; `audit` is the failures a policy lets through. */
export interface PolicyCounts {
  fail: number;
  warn: number;
  error: number;
  audit: number;
}

export interface PolicyNamespace {
  namespace: string;
  counts: PolicyCounts;
  resources: number;
}

export interface PolicyRow {
  engine: string;
  name: string;
  /** A Gatekeeper constraint's kind. */
  kind?: string;
  /** Enforce, Audit or Mixed (Kyverno); deny, dryrun or warn (Gatekeeper); "" unknown. */
  mode: string;
  category?: string;
  severity?: string;
  counts: PolicyCounts;
  resources: number;
  /** Gatekeeper's: the violations it wrote, and how many there are. */
  listed?: number;
  violations?: number;
}

export interface PolicyResource {
  namespace?: string;
  kind: string;
  name: string;
  workload?: WorkloadRef;
  counts: PolicyCounts;
  policies: number;
}

export interface PolicyResult {
  engine: string;
  policy: string;
  rule?: string;
  result: 'fail' | 'warn' | 'error';
  severity?: string;
  category?: string;
  message?: string;
  namespace?: string;
  kind: string;
  name: string;
  audit?: boolean;
}

export interface PolicyOverview {
  cluster: string;
  read: string;
  engines: boolean;
  sources: Source[];
  totals: PolicyCounts;
  failing: number;
  namespaces: PolicyNamespace[];
  policies: PolicyRow[];
  policiesTotal: number;
  resources: PolicyResource[];
  resourcesTotal: number;
  notes: string[];
}

export interface PolicyDetail {
  cluster: string;
  read: string;
  sources: Source[];
  policy: PolicyRow | null;
  results: PolicyResult[];
  total: number;
  notes: string[];
}

export interface WorkloadPolicies {
  sources: Source[];
  counts: PolicyCounts;
  results: PolicyResult[];
  total: number;
}

export const policiesPath = (cluster: string, ns: string) =>
  `${base(cluster)}policies${ns === '' ? '' : `?ns=${encodeURIComponent(ns)}`}`;
export const policyPath = (cluster: string, engine: string, name: string) =>
  `${base(cluster)}policies/${encodeURIComponent(engine)}/${encodeURIComponent(name)}`;

export function getPolicies(cluster: string, ns: string, signal?: AbortSignal): Promise<PolicyOverview> {
  return read<PolicyOverview>(
    policiesPath(cluster, ns),
    signal,
    (b) => typeof b.engines === 'boolean' && Array.isArray(b.sources) && Array.isArray(b.policies) && Array.isArray(b.resources),
    'its policies',
  );
}

export function getPolicy(cluster: string, engine: string, name: string, signal?: AbortSignal): Promise<PolicyDetail> {
  return read<PolicyDetail>(policyPath(cluster, engine, name), signal, (b) => Array.isArray(b.results), 'a policy');
}

/** One read per scope while the view is open; no interval. */
export function policiesQuery(cluster: string, ns: string) {
  return queryOptions({
    queryKey: [KEY, 'policies', cluster, ns] as const,
    queryFn: ({ signal }) => getPolicies(cluster, ns, signal),
    staleTime: STALE_MS,
    gcTime: KEEP_MS,
    refetchOnWindowFocus: false,
  });
}

export function policyQuery(cluster: string, engine: string, name: string) {
  return queryOptions({
    queryKey: [KEY, 'policy', cluster, engine, name] as const,
    queryFn: ({ signal }) => getPolicy(cluster, engine, name, signal),
    staleTime: STALE_MS,
    gcTime: KEEP_MS,
    refetchOnWindowFocus: false,
  });
}

/* -------------------------------------------------------------------------- */
/* Filtering                                                                   */
/* -------------------------------------------------------------------------- */

/** The tiles: four filter the rows by outcome; two pick which table shows. */
export type PolicyTile = '' | 'fail' | 'error' | 'warn' | 'audit' | 'policies' | 'resources';
export const POLICY_TILES: readonly PolicyTile[] = ['fail', 'error', 'warn', 'audit', 'policies', 'resources'];

export function policyTileOf(params: URLSearchParams): PolicyTile {
  const t = params.get('pt') ?? '';
  return (POLICY_TILES as readonly string[]).includes(t) ? (t as PolicyTile) : '';
}

const hit = (c: PolicyCounts, tile: PolicyTile): boolean => {
  switch (tile) {
    case 'fail':
      return c.fail > 0;
    case 'error':
      return c.error > 0;
    case 'warn':
      return c.warn > 0;
    case 'audit':
      return c.audit > 0;
    default:
      return true;
  }
};

const has = (text: string, q: string) => q === '' || text.toLowerCase().includes(q);

export function filterPolicies(rows: readonly PolicyRow[], tile: PolicyTile, q: string): PolicyRow[] {
  const needle = q.trim().toLowerCase();
  return rows.filter((r) => hit(r.counts, tile) && has(`${r.engine} ${r.name} ${r.kind ?? ''} ${r.category ?? ''}`, needle));
}

export function filterResources(rows: readonly PolicyResource[], tile: PolicyTile, q: string): PolicyResource[] {
  const needle = q.trim().toLowerCase();
  return rows.filter((r) => hit(r.counts, tile) && has(`${r.namespace ?? ''} ${r.kind} ${r.name}`, needle));
}

export const policyHref = (engine: string, name: string) => `?view=policies&policy=${encodeURIComponent(`${engine}/${name}`)}`;

/** `engine/name` out of the address, split at the first slash only. */
export function policyOf(params: URLSearchParams): { engine: string; name: string } | undefined {
  const raw = params.get('policy') ?? '';
  const at = raw.indexOf('/');
  if (at <= 0 || at === raw.length - 1) return undefined;
  return { engine: raw.slice(0, at), name: raw.slice(at + 1) };
}

export const policyTotal = (c: PolicyCounts) => c.fail + c.warn + c.error;
export { PAGE_ROWS };
