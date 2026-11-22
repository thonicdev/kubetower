import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { clusterQuery, KEEP_MS, STALE_MS, type Counts, type Findings, type Overview } from './data';
import { security } from './index';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function stub(replies: Record<string, unknown>): string[] {
  const asked: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      asked.push(url);
      const body = replies[url];
      if (body === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
      return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    }),
  );
  return asked;
}

function mount(path = 'x/security') {
  const router = createMemoryRouter([{ path: 'c/:cluster', children: security.routes }], { initialEntries: [`/c/kind/${path}`] });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

const NOW = '2026-11-11T12:00:00Z';
const c = (critical: number, high: number, medium: number, low: number): Counts => ({ critical, high, medium, low, unknown: 0 });

const OVERVIEW: Overview = {
  cluster: 'kind',
  read: NOW,
  operator: true,
  sources: [{ kind: 'vulnerabilityreports', state: 'read', complete: true, reports: 0 }],
  totals: c(0, 0, 0, 0),
  affected: { critical: 0, high: 0, medium: 0 },
  imageCount: 0,
  exposedCount: 0,
  secretCount: 0,
  namespaces: [],
  images: [],
  workloads: [],
  total: 0,
  capped: false,
};

const CLUSTER: Findings = {
  cluster: 'kind',
  read: NOW,
  sources: [
    { kind: 'clustervulnerabilityreports', state: 'read', complete: true, reports: 2 },
    { kind: 'clusterconfigauditreports', state: 'read', complete: true, reports: 1 },
    { kind: 'infraassessmentreports', state: 'off', complete: true, reports: 0 },
    { kind: 'clusterinfraassessmentreports', state: 'denied', complete: false, note: 'You may not list them here.', reports: 0 },
  ],
  rows: [
    { source: 'clustervulnerabilityreports', kind: 'Node', name: 'kubelet', image: 'k8s.io/kubelet:1.30.2', counts: c(1, 4, 0, 0), scanned: '2026-11-11T11:00:00Z' },
    { source: 'clustervulnerabilityreports', kind: 'Pod', namespace: 'kube-system', name: 'kube-apiserver', counts: c(0, 2, 0, 0) },
    { source: 'clusterconfigauditreports', kind: 'ClusterRole', name: 'admin', counts: c(0, 0, 0, 0) },
  ],
  totals: c(1, 6, 0, 0),
  total: 3,
  capped: false,
  notes: [],
  byKind: { clustervulnerabilityreports: 2, clusterconfigauditreports: 1 },
};

const PAGE = '/x/kind/security/';
const findings = () => [...document.querySelectorAll('[data-finding]')].map((r) => r.getAttribute('data-finding'));

describe('the cluster reports', () => {
  it('reads once with the page, with no interval', () => {
    const q = clusterQuery('kind');
    expect(q.staleTime).toBe(STALE_MS);
    expect(q.gcTime).toBe(KEEP_MS);
    expect('refetchInterval' in q).toBe(false);
  });

  it('lists the rows the worst first, one chip per kind that filters through the address', async () => {
    const asked = stub({ [PAGE]: OVERVIEW, [`${PAGE}cluster`]: CLUSTER });
    const router = mount();
    await screen.findByText('kubelet');
    expect(findings()).toEqual(['clustervulnerabilityreports/Node/kubelet', 'clustervulnerabilityreports/Pod/kube-apiserver', 'clusterconfigauditreports/ClusterRole/admin']);
    expect(document.querySelector('[data-finding="clusterconfigauditreports/ClusterRole/admin"] [data-clean]')).not.toBeNull();
    const chip = (k: string) => document.querySelector(`[data-cluster-kind="${k}"]`)!;
    expect(chip('clustervulnerabilityreports').textContent).toBe('Components2');
    expect(chip('infraassessmentreports').textContent).toBe('Infraoff');
    expect((chip('infraassessmentreports') as HTMLButtonElement).disabled).toBe(true);
    expect(chip('clusterinfraassessmentreports').getAttribute('data-state')).toBe('denied');
    expect(chip('clusterinfraassessmentreports').getAttribute('title')).toContain('You may not list them here.');

    fireEvent.click(chip('clusterconfigauditreports'));
    expect(router.state.location.search).toBe('?cr=clusterconfigauditreports');
    expect(findings()).toEqual(['clusterconfigauditreports/ClusterRole/admin']);
    fireEvent.click(chip('clusterconfigauditreports'));
    expect(findings()).toHaveLength(3);
    expect(asked.filter((u) => u === `${PAGE}cluster`)).toHaveLength(1);
  });

  it('draws nothing where the cluster serves none of the kinds', async () => {
    stub({
      [PAGE]: OVERVIEW,
      [`${PAGE}cluster`]: { ...CLUSTER, rows: [], byKind: {}, sources: CLUSTER.sources.map((s) => ({ ...s, state: 'absent', reports: 0 })) },
    });
    mount();
    await waitFor(() => expect(document.querySelector('[data-security-page]')?.getAttribute('aria-busy')).toBe('false'));
    await new Promise((r) => setTimeout(r, 20));
    expect(document.querySelector('[data-cluster-reports]')).toBeNull();
  });
});
