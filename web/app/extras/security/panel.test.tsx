import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { Suspense } from 'react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { PANEL_KINDS, type Counts, type Cve, type Detail } from './data';
import { security } from './extension';
import { countsTitle, securityCells, securityProbe, securityShortcut } from './panel';
import PanelTab, { TAB_CVES } from './PanelTab';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

/** What Security Center adds to the panels and the lists of pods and workloads. */
type Replies = Record<string, { status?: number; body: unknown; text?: boolean }>;

function stub(replies: Replies) {
  const fetch = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? `${input.pathname}${input.search}` : input.url;
    const r = replies[url];
    if (r === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
    if (r.text === true) return Promise.resolve(new Response(String(r.body), { status: r.status ?? 200, headers: { 'Content-Type': 'text/plain' } }));
    return Promise.resolve(new Response(JSON.stringify(r.body), { status: r.status ?? 200, headers: { 'Content-Type': 'application/json' } }));
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

const c = (critical: number, high: number, medium: number, low: number): Counts => ({ critical, high, medium, low, unknown: 0 });
const BRIEF = '/x/prod/security/brief/shop/checkout-api?kind=deployments';
const DETAIL = '/x/prod/security/workload/shop/Deployment/checkout-api';
const object = { cluster: 'prod', kind: 'deployments', namespace: 'shop', name: 'checkout-api' };
const workload = { namespace: 'shop', kind: 'Deployment', name: 'checkout-api' };
const HREF = '/c/prod/x/security/shop/Deployment/checkout-api';
const scanned = { scanned: true, workload, counts: c(3, 12, 20, 4), image: 'registry.example.com/shop/checkout-api:1.14.2', href: HREF };
const signal = () => new AbortController().signal;

function cve(i: number): Cve {
  return {
    id: `CVE-2026-${String(1000 + i)}`,
    severity: i < 3 ? 'CRITICAL' : 'HIGH',
    pkg: `pkg-${String(i)}`,
    installed: '1.0',
    fixed: i % 2 === 0 ? '1.1' : '',
    title: `title ${String(i)}`,
    link: 'https://avd.aquasec.com/nvd/cve',
    container: 'api',
  };
}

function detail(over: Partial<Detail> = {}): Detail {
  const cves = Array.from({ length: 15 }, (_, i) => cve(i));
  return {
    cluster: 'prod',
    read: '2026-11-11T12:00:00Z',
    workload,
    sources: [{ kind: 'vulnerabilityreports', state: 'read', complete: true, reports: 1 }],
    containers: [{ container: 'api', image: scanned.image, scanned: '2026-11-11T10:00:00Z', counts: scanned.counts }],
    exposure: [],
    cves,
    cvesTotal: 39,
    checks: [],
    secrets: [],
    ...over,
  };
}

describe('the probe', () => {
  it('draws the tab on a scanned object only', async () => {
    stub({ [BRIEF]: { body: scanned } });
    expect(await securityProbe(object, signal())).toBe(true);
    stub({ [BRIEF]: { body: { scanned: false } } });
    expect(await securityProbe(object, signal())).toBe(false);
  });

  it('draws no tab behind the gate or on the route 404', async () => {
    stub({ [BRIEF]: { status: 404, body: '404 page not found', text: true } });
    expect(await securityProbe(object, signal())).toBe(false);
    stub({ [BRIEF]: { status: 404, body: { code: 'unknown_cluster', message: 'no such cluster' } } });
    expect(await securityProbe(object, signal())).toBe(false);
  });

  it('draws the tab when the read failed, so it can say so', async () => {
    stub({ [BRIEF]: { status: 502, body: { code: 'list_failed', message: 'could not list' } } });
    expect(await securityProbe(object, signal())).toBe(true);
  });

  it('asks nothing of a cluster-scoped object', async () => {
    const fetch = stub({});
    expect(await securityProbe({ ...object, namespace: undefined }, signal())).toBe(false);
    expect(fetch).not.toHaveBeenCalled();
  });
});

describe('the shortcut', () => {
  it('titles the tile with the counts, the image under it', async () => {
    stub({ [BRIEF]: { body: scanned } });
    expect(await securityShortcut(object, signal())).toEqual({ title: '3 critical · 12 high', badges: ['Trivy'], detail: scanned.image, to: HREF });
  });

  it('says clean, or the worst level below high', () => {
    expect(countsTitle(c(0, 0, 0, 0))).toBe('clean');
    expect(countsTitle(c(0, 4, 2, 0))).toBe('4 high');
    expect(countsTitle(c(0, 0, 2, 9))).toBe('2 medium');
    expect(countsTitle(c(1200, 0, 0, 0))).toBe('1,200 critical');
  });

  it('draws nothing on an object no report covers', async () => {
    stub({ [BRIEF]: { body: { scanned: false } } });
    expect(await securityShortcut(object, signal())).toBeNull();
  });

  it('leads to the workload page when the brief carries no console address', async () => {
    stub({ [BRIEF]: { body: { ...scanned, href: undefined } } });
    expect((await securityShortcut(object, signal()))?.to).toBe(HREF);
  });
});

describe('the column', () => {
  it('asks once for the whole list and passes the cells as they are', async () => {
    const cells = {
      'shop/checkout-api': { text: '3 critical', tone: 'bad', hint: '3 critical · 12 high', href: HREF },
      'shop/mailer': { text: 'clean', tone: 'ok' },
    };
    const fetch = stub({ '/x/prod/security/cells?kind=deployments': { body: cells } });
    expect(await securityCells({ cluster: 'prod', kind: 'deployments' }, signal())).toEqual(cells);
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it('rejects a failed read, so no column is drawn', async () => {
    stub({ '/x/prod/security/cells?kind=pods': { status: 502, body: { code: 'list_failed', message: 'no' } } });
    await expect(securityCells({ cluster: 'prod', kind: 'pods' }, signal())).rejects.toThrow();
  });

  it('is declared on pods and on the six workloads that run them', () => {
    const kinds = [...PANEL_KINDS];
    expect(security.tabs?.map((t) => [...t.kinds])).toEqual([kinds]);
    expect(security.shortcuts?.map((t) => [...t.kinds])).toEqual([kinds]);
    expect(security.columns?.map((t) => [...t.kinds])).toEqual([kinds]);
    expect(kinds).toEqual(['pods', 'deployments', 'statefulsets', 'daemonsets', 'replicasets', 'jobs', 'cronjobs']);
  });
});

function mount(props = object) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <Suspense fallback={null}>
          <PanelTab {...props} />
        </Suspense>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('the tab', () => {
  it('draws the counts and the ten worst CVEs, with the way to the page', async () => {
    stub({ [BRIEF]: { body: scanned }, [DETAIL]: { body: detail() } });
    mount();
    await waitFor(() => expect(document.querySelectorAll('[data-tab-cve]')).toHaveLength(TAB_CVES));
    expect(TAB_CVES).toBe(10);
    const rows = [...document.querySelectorAll('[data-tab-cve]')].map((r) => r.getAttribute('data-tab-cve'));
    expect(rows[0]).toBe('CVE-2026-1000');
    expect(rows[9]).toBe('CVE-2026-1009');
    expect(document.querySelector('[data-counter="critical"] dd')?.textContent).toBe('3');
    expect(document.querySelector('[data-counter="high"] dd')?.textContent).toBe('12');
    expect(document.querySelector('[data-shown]')?.textContent).toBe('10 / 39');
    expect(document.querySelector('[data-sevbar]')).not.toBeNull();
    expect(screen.getByText(scanned.image).getAttribute('title')).toBe(scanned.image);
    expect(document.querySelector('[data-open]')?.getAttribute('href')).toBe(HREF);
  });

  it('draws fewer when the workload has fewer, and None when it has none', async () => {
    stub({ [BRIEF]: { body: scanned }, [DETAIL]: { body: detail({ cves: [cve(0), cve(1)], cvesTotal: 2 }) } });
    mount();
    await waitFor(() => expect(document.querySelectorAll('[data-tab-cve]')).toHaveLength(2));
    cleanup();
    stub({ [BRIEF]: { body: { ...scanned, counts: c(0, 0, 0, 0) } }, [DETAIL]: { body: detail({ cves: [], cvesTotal: 0 }) } });
    mount();
    await waitFor(() => expect(document.querySelector('[data-none]')).not.toBeNull());
  });

  it('says unreadable when the brief could not be read', async () => {
    stub({ [BRIEF]: { status: 502, body: { code: 'list_failed', message: 'could not list vulnerabilityreports' } } });
    mount();
    await waitFor(() => expect(document.querySelector('[data-state="unreadable"]')).not.toBeNull());
    expect(document.querySelector('[data-state]')?.getAttribute('title')).toContain('could not list');
  });

  it('says denied on a refusal, and the source state when the reports were not read', async () => {
    stub({ [BRIEF]: { status: 403, body: { code: 'list_forbidden', message: 'forbidden' } } });
    mount();
    await waitFor(() => expect(document.querySelector('[data-state="denied"]')).not.toBeNull());
    cleanup();
    stub({
      [BRIEF]: { body: scanned },
      [DETAIL]: { body: detail({ sources: [{ kind: 'vulnerabilityreports', state: 'unreadable', complete: false, note: 'timeout', reports: 0 }], cves: [] }) },
    });
    mount();
    await waitFor(() => expect(document.querySelector('[data-state="unreadable"]')).not.toBeNull());
    expect(document.querySelector('[data-counter="critical"]')).not.toBeNull();
  });

  it('says unreadable when the CVEs could not be read, and keeps the counts', async () => {
    stub({ [BRIEF]: { body: scanned }, [DETAIL]: { status: 502, body: { code: 'list_failed', message: 'no' } } });
    mount();
    await waitFor(() => expect(document.querySelector('[data-state="unreadable"]')).not.toBeNull());
    expect(document.querySelector('[data-counter="high"] dd')?.textContent).toBe('12');
  });

  it('says not scanned on an object no report covers', async () => {
    stub({ [BRIEF]: { body: { scanned: false } } });
    mount();
    await waitFor(() => expect(document.querySelector('[data-state="not scanned"]')).not.toBeNull());
  });
});
