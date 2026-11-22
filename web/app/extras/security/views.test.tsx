import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Compliance, Counts, Findings } from './data';
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

function mount(path: string) {
  const router = createMemoryRouter([{ path: 'c/:cluster', children: security.routes }], { initialEntries: [`/c/kind/${path}`] });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

const NOW = '2026-11-11T12:00:00Z';
const PAGE = '/x/kind/security/';
const c = (critical: number, high: number, medium: number, low: number): Counts => ({ critical, high, medium, low, unknown: 0 });

const RBAC: Findings = {
  cluster: 'kind',
  read: NOW,
  sources: [
    { kind: 'rbacassessmentreports', state: 'read', complete: true, reports: 1 },
    { kind: 'clusterrbacassessmentreports', state: 'read', complete: true, reports: 1 },
  ],
  rows: [
    { source: 'clusterrbacassessmentreports', kind: 'ClusterRole', name: 'admin', counts: c(2, 1, 0, 0) },
    { source: 'rbacassessmentreports', kind: 'Role', namespace: 'shop', name: 'deployer', counts: c(0, 1, 0, 0) },
  ],
  totals: c(2, 2, 0, 0),
  total: 2,
  capped: false,
  notes: [],
  byKind: { rbacassessmentreports: 1, clusterrbacassessmentreports: 1 },
};

const COMPLIANCE: Compliance = {
  cluster: 'kind',
  read: NOW,
  source: { kind: 'clustercompliancereports', state: 'read', complete: true, reports: 1 },
  reports: [
    {
      name: 'nsa',
      title: 'NSA Kubernetes Hardening Guidance',
      version: '1.0',
      pass: 1,
      fail: 2,
      updated: '2026-11-11T10:00:00Z',
      controlsTotal: 4,
      controls: [
        { id: '2.0', name: 'Privileged', severity: 'HIGH', fail: 2 },
        { id: '1.1', name: 'Immutable file systems', severity: 'LOW', fail: 4 },
        { id: '1.0', name: 'Non-root containers', severity: 'MEDIUM', fail: 0 },
        { id: '9.9', name: 'Manual review', severity: 'MEDIUM', fail: null },
      ],
    },
  ],
};

const controls = () => [...document.querySelectorAll('[data-control]')].map((r) => r.getAttribute('data-control'));

describe('the Security Center views', () => {
  it('reads RBAC only when its view is open, and never the overview there', async () => {
    const asked = stub({ [`${PAGE}rbac`]: RBAC });
    mount('x/security?view=rbac');
    await screen.findByText('admin');
    expect(asked).toEqual([`${PAGE}rbac`]);
    expect(document.querySelector('[data-view="rbac"]')?.getAttribute('aria-pressed')).toBe('true');
    expect([...document.querySelectorAll('[data-finding]')].map((r) => r.getAttribute('data-finding'))).toEqual([
      'clusterrbacassessmentreports/ClusterRole/admin',
      'rbacassessmentreports/Role/deployer',
    ]);
  });

  it('switches views through the address, dropping the other filters', async () => {
    stub({ [`${PAGE}rbac`]: RBAC, [`${PAGE}compliance`]: COMPLIANCE });
    const router = mount('x/security?view=rbac&rr=rbacassessmentreports');
    await screen.findByText('deployer');
    fireEvent.click(document.querySelector('[data-view="compliance"]')!);
    expect(router.state.location.search).toBe('?view=compliance');
    await screen.findByText('NSA Kubernetes Hardening Guidance');
  });

  it('lists the failed controls of each standard, and every control on demand', async () => {
    const asked = stub({ [`${PAGE}compliance`]: COMPLIANCE });
    const router = mount('x/security?view=compliance');
    await screen.findByText('NSA Kubernetes Hardening Guidance');
    expect(controls()).toEqual(['2.0', '1.1']);
    expect(document.querySelector('[data-standard="nsa"] [data-counts]')?.textContent).toBe('1 pass · 2 fail');
    fireEvent.click(document.querySelector('[data-failed-only]')!);
    expect(router.state.location.search).toBe('?view=compliance&cf=all');
    expect(controls()).toEqual(['2.0', '1.1', '1.0', '9.9']);
    expect(document.querySelector('[data-control="9.9"]')?.textContent).toContain('—');
    expect(asked.filter((u) => u === PAGE)).toHaveLength(0);
  });

  it('a compliance scanner that is off is one short state', async () => {
    stub({ [`${PAGE}compliance`]: { ...COMPLIANCE, source: { ...COMPLIANCE.source, state: 'off', reports: 0 }, reports: [] } });
    mount('x/security?view=compliance');
    expect((await screen.findByText('Compliance off')).getAttribute('data-compliance')).toBe('off');
  });
});
