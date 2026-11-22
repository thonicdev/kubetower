import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { KEEP_MS, PAGE_ROWS, STALE_MS, type Detail } from './data';
import { security } from './index';
import { policiesQuery, type PolicyCounts, type PolicyDetail, type PolicyOverview, type PolicyResource } from './policyData';

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

function mount(path: string) {
  const router = createMemoryRouter([{ path: 'c/:cluster', children: security.routes }], { initialEntries: [`/c/kind/${path}`] });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

const NOW = '2026-11-13T12:00:00Z';
const BASE = '/x/kind/security/';
const pc = (fail: number, error: number, warn: number, audit = 0): PolicyCounts => ({ fail, error, warn, audit });

const SOURCES = [
  { kind: 'policyreports.wgpolicyk8s.io', state: 'read', complete: true, reports: 3 },
  { kind: 'reports.openreports.io', state: 'absent', complete: false, reports: 0 },
  { kind: 'clusterpolicyreports.wgpolicyk8s.io', state: 'read', complete: true, reports: 0 },
  { kind: 'clusterreports.openreports.io', state: 'absent', complete: false, reports: 0 },
  { kind: 'constraints.gatekeeper.sh', state: 'denied', complete: false, note: 'You may not list them here.', reports: 0 },
  { kind: 'clusterpolicies.kyverno.io', state: 'read', complete: true, reports: 2 },
  { kind: 'policies.kyverno.io', state: 'read', complete: true, reports: 0 },
  { kind: 'validatingpolicies.policies.kyverno.io', state: 'absent', complete: false, reports: 0 },
  { kind: 'imagevalidatingpolicies.policies.kyverno.io', state: 'absent', complete: false, reports: 0 },
  { kind: 'namespacedvalidatingpolicies.policies.kyverno.io', state: 'absent', complete: false, reports: 0 },
  { kind: 'namespacedimagevalidatingpolicies.policies.kyverno.io', state: 'absent', complete: false, reports: 0 },
] as PolicyOverview['sources'];

function overview(resources: PolicyResource[]): PolicyOverview {
  return {
    cluster: 'kind',
    read: NOW,
    engines: true,
    sources: SOURCES,
    totals: pc(3, 1, 2, 2),
    failing: 2,
    namespaces: [
      { namespace: 'shop', counts: pc(3, 0, 1, 2), resources: 2 },
      { namespace: 'data', counts: pc(0, 1, 1), resources: 1 },
    ],
    policies: [
      { engine: 'kyverno', name: 'require-labels', mode: 'Audit', category: 'Best Practices', severity: 'medium', counts: pc(2, 0, 0, 2), resources: 2 },
      { engine: 'gatekeeper', name: 'k8srequiredlabels.must-have-owner', kind: 'K8sRequiredLabels', mode: 'deny', counts: pc(1, 0, 0), resources: 1, listed: 1, violations: 1 },
      { engine: 'kyverno', name: 'disallow-latest', mode: 'Enforce', severity: 'high', counts: pc(0, 1, 2), resources: 2 },
    ],
    policiesTotal: 3,
    resources,
    resourcesTotal: resources.length,
    notes: [],
  };
}

const THREE: PolicyResource[] = [
  { namespace: 'shop', kind: 'Pod', name: 'web-1-a', workload: { namespace: 'shop', kind: 'Deployment', name: 'web' }, counts: pc(2, 0, 0, 2), policies: 2 },
  { namespace: 'shop', kind: 'Deployment', name: 'api', workload: { namespace: 'shop', kind: 'Deployment', name: 'api' }, counts: pc(1, 0, 1), policies: 2 },
  { namespace: 'data', kind: 'StatefulSet', name: 'db', counts: pc(0, 1, 1), policies: 1 },
];

const rows = (attr: string) => [...document.querySelectorAll(`[${attr}]`)].map((r) => r.getAttribute(attr));

describe('the Policies view', () => {
  it('reads once a minute at most, with no interval', () => {
    const q = policiesQuery('kind', '');
    expect(q.staleTime).toBe(STALE_MS);
    expect(q.gcTime).toBe(KEEP_MS);
    expect('refetchInterval' in q).toBe(false);
  });

  it('is read only while its view is open', async () => {
    const asked = stub({ [BASE]: { cluster: 'kind', read: NOW, operator: false, sources: [], workloads: [], totals: {} } });
    mount('x/security');
    await screen.findByText('trivy-operator absent');
    expect(asked.some((u) => u.includes('/policies'))).toBe(false);
  });

  it('draws one short state when no policy engine is served', async () => {
    stub({ [`${BASE}policies`]: { ...overview([]), engines: false } });
    mount('x/security?view=policies');
    expect((await screen.findByText('no policy engine')).getAttribute('data-policies')).toBe('none');
  });

  it('names each source with its state, the reason in the title', async () => {
    stub({ [`${BASE}policies`]: overview(THREE) });
    mount('x/security?view=policies');
    await screen.findByText('require-labels');
    const pill = (k: string) => document.querySelector(`[data-source="${k}"]`);
    expect(pill('constraints.gatekeeper.sh')?.getAttribute('data-state')).toBe('denied');
    expect(pill('constraints.gatekeeper.sh')?.getAttribute('title')).toContain('You may not list them here.');
    expect(pill('policyreports.wgpolicyk8s.io')?.textContent).toBe('PolicyReport');
  });

  it('counts the tiles and filters by one, through the address', async () => {
    stub({ [`${BASE}policies`]: overview(THREE) });
    const router = mount('x/security?view=policies');
    await screen.findByText('require-labels');
    const tile = (t: string) => document.querySelector(`[data-policy-tile="${t}"]`);
    expect(tile('fail')?.getAttribute('data-count')).toBe('3');
    expect(tile('audit')?.getAttribute('data-count')).toBe('2');
    expect(tile('resources')?.getAttribute('data-count')).toBe('2');
    fireEvent.click(tile('error')!);
    expect(router.state.location.search).toBe('?view=policies&pt=error');
    expect(rows('data-policy-row')).toEqual(['kyverno/disallow-latest']);
    expect(rows('data-resource-row')).toEqual(['data/StatefulSet/db']);
    fireEvent.click(tile('policies')!);
    expect(document.querySelector('[data-resource-table]')).toBeNull();
    expect(rows('data-policy-row')).toHaveLength(3);
  });

  it('a namespace bar scopes the read itself', async () => {
    const asked = stub({ [`${BASE}policies`]: overview(THREE), [`${BASE}policies?ns=data`]: overview([THREE[2]!]) });
    const router = mount('x/security?view=policies');
    await screen.findByText('require-labels');
    fireEvent.click(document.querySelector('[data-pns="data"]')!);
    expect(new URLSearchParams(router.state.location.search).get('pns')).toBe('data');
    await waitFor(() => expect(rows('data-resource-row')).toEqual(['data/StatefulSet/db']));
    expect(asked).toContain(`${BASE}policies?ns=data`);
  });

  it('draws 100 of 8 000 resources at a time', async () => {
    const many: PolicyResource[] = Array.from({ length: 8000 }, (_, i) => ({
      namespace: `team-${String(i % 40)}`,
      kind: 'Pod',
      name: `pod-${String(i).padStart(5, '0')}`,
      counts: pc(1, 0, 0),
      policies: 1,
    }));
    stub({ [`${BASE}policies`]: overview(many) });
    mount('x/security?view=policies&pt=resources');
    await waitFor(() => expect(rows('data-resource-row')).toHaveLength(PAGE_ROWS));
    expect(document.querySelector('[data-resource-table]')?.closest('section')?.querySelector('[data-range]')?.textContent).toBe('1–100 / 8,000');
  });

  it('a policy opens its results, the message in the row title', async () => {
    const detail: PolicyDetail = {
      cluster: 'kind',
      read: NOW,
      sources: SOURCES,
      policy: overview([]).policies[0]!,
      results: [
        { engine: 'kyverno', policy: 'require-labels', rule: 'check-labels', result: 'fail', severity: 'medium', message: "label 'owner' is required", namespace: 'shop', kind: 'Pod', name: 'web-1-a', audit: true },
      ],
      total: 1,
      notes: [],
    };
    stub({ [`${BASE}policies`]: overview(THREE), [`${BASE}policies/kyverno/require-labels`]: detail });
    const router = mount('x/security?view=policies');
    fireEvent.click(await screen.findByText('require-labels'));
    expect(new URLSearchParams(router.state.location.search).get('policy')).toBe('kyverno/require-labels');
    const row = await waitFor(() => {
      const r = document.querySelector('[data-policy-result]');
      expect(r).not.toBeNull();
      return r!;
    });
    expect(row.getAttribute('title')).toBe("label 'owner' is required");
    expect(row.textContent).toContain('fail · audit');
  });
});

describe("a workload's policies", () => {
  it('shows a Policies tab only where an engine answered', async () => {
    const detail = (policies?: Detail['policies']): Detail => ({
      cluster: 'kind',
      read: NOW,
      workload: { namespace: 'shop', kind: 'Deployment', name: 'web' },
      sources: [],
      containers: [],
      exposure: [],
      cves: [],
      cvesTotal: 0,
      checks: [],
      secrets: [],
      ...(policies === undefined ? {} : { policies }),
    });
    stub({
      [`${BASE}workload/shop/Deployment/web`]: detail({
        sources: [{ kind: 'policyreports.wgpolicyk8s.io', state: 'read', complete: true, reports: 1 }],
        counts: pc(1, 0, 0),
        results: [{ engine: 'kyverno', policy: 'require-labels', rule: 'check-labels', result: 'fail', kind: 'Pod', name: 'web-1-a', namespace: 'shop' }],
        total: 1,
      }),
      [`${BASE}workload/shop/Deployment/api`]: detail(),
    });
    mount('x/security/shop/Deployment/web?tab=policies');
    await waitFor(() => expect(document.querySelector('[data-tab="policies"]')?.textContent).toBe('Policies 1'));
    expect(rows('data-policy-result')).toEqual(['fail']);
    cleanup();
    mount('x/security/shop/Deployment/api');
    await waitFor(() => expect(document.querySelector('[data-tab="cve"]')).not.toBeNull());
    expect(document.querySelector('[data-tab="policies"]')).toBeNull();
  });
});
