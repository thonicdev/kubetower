import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ShellProvider } from '../../../ui/src/shell/ShellContext';
import { Shortcuts } from '../../../ui/src/pages/object/Shortcuts';
import type { Figures, Pod, Stats } from './data';
import { dns } from './extension';
import { isCoreDNS, lineFrom } from './panel';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const fig = (over: Partial<Figures> = {}): Figures => ({ qps: 412, nxdomain: 0.031, servfail: 0.002, p50: 0.001, p99: 0.004, cacheHit: 0.87, ...over });
const pod = (name: string, figures: Figures | null): Pod => ({ pod: name, figures });
function stats(over: Partial<Stats> = {}): Stats {
  return { state: 'read', span: '15m', namespace: 'kube-system', now: fig(), pods: [pod('coredns-a', fig({ qps: 200, servfail: 0.06 })), pod('coredns-b', null)], upstreams: [], ...over };
}

const STATS = '/x/prod/dns/stats?span=15m';
const menu = { state: 'ok', data: { groups: [{ key: 'extra', label: 'Extra', items: [{ text: 'DNS', href: '/c/prod/x/dns' }], deferred: false }] } };
const dnsLabels = { 'k8s-app': 'kube-dns' };

function stub(replies: Record<string, unknown>) {
  const fetch = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? `${input.pathname}${input.search}` : input.url;
    if (!(url in replies)) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
    return Promise.resolve(new Response(JSON.stringify(replies[url]), { status: 200, headers: { 'Content-Type': 'application/json' } }));
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

function mountPanel(kind: string, name: string, labels: Record<string, string>, nav: unknown = menu) {
  const fetch = stub({ '/api/c/prod/nav': nav, [STATS]: stats() });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <ShellProvider extensions={[dns]}>
          <Shortcuts identity={{ cluster: 'prod', kind, namespace: 'kube-system', name }} marks={{ labels, annotations: {} }} tabs={[]} onTab={vi.fn()} />
        </ShellProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return fetch;
}

const statsCalls = (fetch: ReturnType<typeof stub>) => fetch.mock.calls.filter(([u]) => String(u).includes('/dns/stats')).length;
const row = () => document.querySelector('[data-shortcut="x.dns.line"]') as HTMLElement | null;

describe('the line', () => {
  it('says the cluster figures on the Service, graded by the page thresholds', () => {
    const t = lineFrom('prod', 'services', 'kube-dns', stats({ now: fig({ p99: 0.25 }) }));
    expect(t.title).toBe('412 q/s');
    expect(t.detail).toBe('NXDOMAIN 3.1 % · SERVFAIL 0.2 % · p99 250 ms · cache 87 %');
    expect(t.status).toMatchObject({ text: 'p99', tone: 'warn' });
    expect(t.to).toBe('/c/prod/x/dns');
  });

  it('is quiet-toned when nothing crosses a threshold', () => {
    expect(lineFrom('prod', 'services', 'kube-dns', stats()).status).toBeUndefined();
  });

  it("says a pod's own figures, red above 5 % SERVFAIL", () => {
    const t = lineFrom('prod', 'pods', 'coredns-a', stats());
    expect(t.title).toBe('200 q/s');
    expect(t.status).toMatchObject({ text: 'SERVFAIL', tone: 'bad' });
  });

  it('shows "-" for a figure the window did not hold', () => {
    const t = lineFrom('prod', 'services', 'kube-dns', stats({ now: fig({ p99: null, cacheHit: null }) }));
    expect(t.detail).toContain('p99 - · cache -');
  });

  it('says a short state, with the reason in the hover', () => {
    expect(lineFrom('prod', 'pods', 'coredns-b', stats())).toMatchObject({ title: 'No metrics' });
    expect(lineFrom('prod', 'pods', 'coredns-zz', stats()).title).toBe('No metrics');
    const absent = lineFrom('prod', 'services', 'kube-dns', stats({ state: 'no-prometheus', detail: 'no Prometheus found' }));
    expect(absent.title).toBe('Prometheus absent');
    expect(absent.status?.hint).toBe('no Prometheus found');
  });

  it('matches CoreDNS by label, not by name', () => {
    expect(isCoreDNS({ labels: { 'k8s-app': 'kube-dns' }, annotations: {} })).toBe(true);
    expect(isCoreDNS({ labels: { 'app.kubernetes.io/name': 'coredns' }, annotations: {} })).toBe(true);
    expect(isCoreDNS({ labels: {}, annotations: {} })).toBe(false);
  });
});

describe('the panel', () => {
  it('reads once, at 15 minutes, when the panel opens on the Service', async () => {
    const fetch = mountPanel('services', 'kube-dns', dnsLabels);
    await waitFor(() => expect(row()?.textContent).toContain('412 q/s'));
    expect(statsCalls(fetch)).toBe(1);
  });

  it("shows a CoreDNS pod's own figures", async () => {
    mountPanel('pods', 'coredns-a', dnsLabels);
    await waitFor(() => expect(row()?.textContent).toContain('200 q/s'));
  });

  it('asks nothing and draws nothing on an object that is not CoreDNS', async () => {
    const fetch = mountPanel('services', 'web', { app: 'web' });
    await new Promise((resolve) => setTimeout(resolve, 100));
    expect(statsCalls(fetch)).toBe(0);
    expect(row()).toBeNull();
  });

  it('asks nothing and draws nothing while the extra is off for the cluster', async () => {
    const fetch = mountPanel('services', 'kube-dns', dnsLabels, { state: 'ok', data: { groups: [] } });
    await new Promise((resolve) => setTimeout(resolve, 100));
    expect(statsCalls(fetch)).toBe(0);
    expect(row()).toBeNull();
  });

  it('is declared with no interval of its own, and nothing is read while no panel is shown', () => {
    expect(dns.shortcuts?.[0]).not.toHaveProperty('refetchInterval');
    const fetch = stub({});
    expect(statsCalls(fetch)).toBe(0);
  });
});
