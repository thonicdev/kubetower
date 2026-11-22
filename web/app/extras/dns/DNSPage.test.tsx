import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { Suspense } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { POLL_MS, type Figures, type Pod, type Stats, type Upstream } from './data';
import { DNSPage } from './DNSPage';
import { millis, percent, podBars, rate, stackPaths, upstreamBars } from './model';

afterEach(() => {
  vi.unstubAllGlobals();
});

const fig = (over: Partial<Figures> = {}): Figures => ({ qps: 10, nxdomain: 0.1, servfail: 0, p50: 0.001, p99: 0.02, cacheHit: 0.9, ...over });
const pod = (name: string, figures: Figures | null): Pod => ({ pod: name, node: 'n1', figures });
const up = (to: string, over: Partial<Upstream> = {}): Upstream => ({ to, qps: 5, errQps: 0, p99: 0.03, healthFailPs: 0, answers: true, ...over });

function points(v: (i: number) => number): [number, number][] {
  return Array.from({ length: 10 }, (_, i) => [1000 + i * 15, v(i)] as [number, number]);
}

function stats(over: Partial<Stats> = {}): Stats {
  return {
    state: 'read',
    span: '1h',
    namespace: 'kube-system',
    version: '1.11.1',
    now: fig({ qps: 120, servfail: 0.44, p99: 0.25 }),
    series: { step: 30, qps: points(() => 1), byRcode: { NOERROR: points(() => 5), SERVFAIL: points(() => 2) }, p50: points(() => 0.001), p99: points(() => 0.2), cacheHit: points(() => 0.8) },
    pods: [pod('coredns-a', fig({ servfail: 0.5 })), pod('coredns-b', fig({ servfail: 0.001 })), pod('coredns-c', null)],
    upstreams: [up('8.8.8.8:53'), up('10.0.0.1:53', { answers: false, p99: null, qps: 0, healthFailPs: 3 })],
    ...over,
  };
}

function stub(body: unknown, status = 200) {
  const fetch = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
    if (!url.startsWith('/x/prod/dns/stats')) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
    return Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }));
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

function mount(entry = '/c/prod/x/dns'): void {
  const router = createMemoryRouter([{ path: '/c/:cluster/x/dns', element: <Suspense><DNSPage /></Suspense> }], { initialEntries: [entry] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

const tile = (key: string) => document.querySelector(`[data-tile="${key}"]`) as HTMLElement;
const bars = (title: string) => within(screen.getByLabelText(title));

describe('the tiles', () => {
  it('show the figures, coloured by threshold, with the thresholds only in the title', async () => {
    stub(stats());
    mount();
    await waitFor(() => expect(tile('qps')).toBeTruthy());
    expect(within(tile('qps')).getByText('120')).toBeTruthy();
    expect(within(tile('servfail')).getByText('44 %')).toBeTruthy();
    expect(within(tile('p99')).getByText('250 ms')).toBeTruthy();
    expect(tile('servfail').dataset.tone).toBe('bad');
    expect(tile('p99').dataset.tone).toBe('warn');
    expect(tile('qps').dataset.tone).toBe('none');
    expect(tile('servfail').title).toMatch(/1 %.*5 %/);
  });

  it('read a figure the window did not hold as a dash, never a zero', async () => {
    stub(stats({ now: fig({ nxdomain: null, servfail: null, p99: null, cacheHit: null }) }));
    mount();
    await waitFor(() => expect(tile('nxdomain')).toBeTruthy());
    for (const key of ['nxdomain', 'servfail', 'p99', 'cache']) expect(within(tile(key)).getByText('-')).toBeTruthy();
    expect(tile('servfail').dataset.tone).toBe('none');
  });

  it('filter the bars to what is over the threshold, and let them go when pressed again', async () => {
    stub(stats());
    mount();
    await waitFor(() => expect(tile('servfail')).toBeTruthy());
    expect(bars('CoreDNS pods').getAllByRole('listitem')).toHaveLength(3);
    fireEvent.click(tile('servfail'));
    await waitFor(() => expect(bars('CoreDNS pods').getAllByRole('listitem')).toHaveLength(1));
    expect(bars('CoreDNS pods').getByText('coredns-a')).toBeTruthy();
    // The upstream that never answers is over every threshold.
    expect(bars('Upstreams').getByText('10.0.0.1:53')).toBeTruthy();
    expect(bars('Upstreams').queryByText('8.8.8.8:53')).toBeNull();
    fireEvent.click(tile('servfail'));
    await waitFor(() => expect(bars('CoreDNS pods').getAllByRole('listitem')).toHaveLength(3));
  });

  it('draw a pod with no series as No metrics and a dead upstream as No answers', async () => {
    stub(stats());
    mount();
    await waitFor(() => expect(tile('qps')).toBeTruthy());
    expect(bars('CoreDNS pods').getByText('No metrics')).toBeTruthy();
    expect(bars('Upstreams').getByText('No answers')).toBeTruthy();
  });

  it('give a pod name its whole text on hover, where a narrow column cuts it', async () => {
    stub(stats());
    mount();
    await waitFor(() => expect(tile('qps')).toBeTruthy());
    expect(bars('CoreDNS pods').getByText('coredns-a').getAttribute('title')).toBe('coredns-a');
  });
});

describe('the charts', () => {
  it('label each peak with its value, on one line', async () => {
    stub(stats());
    mount();
    await waitFor(() => expect(document.querySelectorAll('[data-peak]')).toHaveLength(3));
    const peaks = [...document.querySelectorAll('[data-peak]')];
    // The stacked total of the answers (5 + 2), the p99 line, the cache hit.
    expect(peaks.map((p) => p.textContent)).toEqual([rate(7), millis(0.2), percent(0.8)]);
    for (const p of peaks) expect(p.className).toContain('whitespace-nowrap');
  });
});

describe('the states', () => {
  it.each([
    ['no-prometheus', 'Prometheus absent', 'x'],
    ['unreadable', 'Unreadable', 'x'],
  ] as const)('%s reads %s, with the reason in the title and no tiles', async (state, word, detail) => {
    stub({ state, detail, span: '1h', pods: [], upstreams: [] });
    mount();
    const el = await screen.findByText(word);
    expect(el.getAttribute('title')).toBe(detail);
    expect(document.querySelector('[data-tile]')).toBeNull();
  });

  it('reads an empty window with CoreDNS found as No metrics, and with none found as No CoreDNS', async () => {
    stub({ state: 'empty', namespace: 'kube-system', detail: 'd', span: '1h', pods: [], upstreams: [] });
    mount();
    expect(await screen.findByText('No metrics')).toBeTruthy();
    const how = document.querySelector('details[data-enable-metrics]') as HTMLDetailsElement;
    expect(how.open).toBe(false);
    expect(document.querySelector('[data-span]')).toBeNull();
    fireEvent.click(screen.getByText('Enable metrics'));
    expect(how.open).toBe(true);
    expect(how.textContent).toContain('prometheus :9153');
    expect(how.textContent).toContain('kind: ServiceMonitor');
    expect(how.textContent).toContain('job_name: coredns');
    const link = screen.getByText('CoreDNS metrics plugin');
    expect(link.getAttribute('href')).toBe('https://coredns.io/plugins/metrics/');
    expect(link.getAttribute('rel')).toBe('noreferrer');
  });

  it('draws no how-to for a failed read', async () => {
    stub({ state: 'unreadable', detail: 'x', span: '1h', pods: [], upstreams: [] });
    mount();
    await screen.findByText('Unreadable');
    expect(document.querySelector('[data-enable-metrics]')).toBeNull();
  });

  it('reads no CoreDNS pod as No CoreDNS', async () => {
    stub({ state: 'empty', detail: 'no CoreDNS pod was found', span: '1h', pods: [], upstreams: [] });
    mount();
    expect(await screen.findByText('No CoreDNS')).toBeTruthy();
  });

  it('reads a refused read as Refused, the message on hover', async () => {
    stub({ code: 'list_forbidden', message: 'you may not' }, 403);
    mount();
    const word = await screen.findByText('Refused');
    expect(word.getAttribute('title')).toContain('you may not');
    expect(screen.queryByText('Unreadable')).toBeNull();
  });

  it.each([
    [502, { code: 'read_failed', message: 'could not be read' }],
    [403, { code: 'other', message: 'forbidden for another reason' }],
  ])('reads a failed read (%i) as Unreadable, never as Refused', async (status, body) => {
    stub(body, status);
    mount();
    expect((await screen.findByText('Unreadable')).getAttribute('title')).toBe(body.message);
    expect(screen.queryByText('Refused')).toBeNull();
  });

  it('draws an upstream only the Corefile names as a No answers badge, with its zone on hover', async () => {
    const s = stats();
    s.upstreams = [{ to: '192.0.2.1:53', qps: 0, errQps: 0, p99: null, healthFailPs: 0, answers: false, zones: ['failing.test'] }];
    stub(s);
    mount();
    await waitFor(() => expect(tile('qps')).toBeTruthy());
    const badge = bars('Upstreams').getByText('No answers');
    expect(badge.getAttribute('data-pill')).toBe('bad');
    expect(bars('Upstreams').getByTitle(/zone failing\.test/)).toBeTruthy();
  });

  it('draws No metrics in a chart whose series is empty', async () => {
    const s = stats();
    s.series = { step: 30, qps: [], byRcode: {}, p50: [], p99: [], cacheHit: [] };
    stub(s);
    mount();
    await waitFor(() => expect(tile('qps')).toBeTruthy());
    expect(document.querySelectorAll('[data-state="empty"]')).toHaveLength(3);
  });
});

describe('the span', () => {
  it('asks for the one the address names, and again when it is changed', async () => {
    const fetch = stub(stats({ span: '6h' }));
    mount('/c/prod/x/dns?span=6h');
    await waitFor(() => expect(tile('qps')).toBeTruthy());
    expect(String(fetch.mock.calls[0]?.[0])).toContain('span=6h');
    fireEvent.click(document.querySelector('[data-span="15m"]') as HTMLElement);
    await waitFor(() => expect(fetch.mock.calls.some((c) => String(c[0]).includes('span=15m'))).toBe(true));
  });

  it('polls no faster than 15 s', () => {
    expect(Math.min(...Object.values(POLL_MS))).toBeGreaterThanOrEqual(15_000);
  });
});

describe('a large set', () => {
  it('renders 200 pods and 600 upstreams', async () => {
    const pods = Array.from({ length: 200 }, (_, i) => pod(`coredns-${String(i).padStart(3, '0')}`, fig({ qps: i })));
    const upstreams = Array.from({ length: 600 }, (_, i) => up(`10.0.${Math.floor(i / 250)}.${i % 250}:53`));
    stub(stats({ pods, upstreams }));
    mount();
    await waitFor(() => expect(bars('CoreDNS pods').getAllByRole('listitem')).toHaveLength(200));
    expect(bars('Upstreams').getAllByRole('listitem')).toHaveLength(600);
  });
});

describe('the model', () => {
  it('formats shares, latencies and rates, and a missing figure as a dash', () => {
    expect([percent(0.44), percent(0.004), percent(null), millis(0.25), millis(1.5), millis(null), rate(120.4), rate(0.55), rate(null)]).toEqual(['44 %', '0.4 %', '-', '250 ms', '1.5 s', '-', '120', '0.6', '-']);
  });

  it('keeps a pod with no series out of a filter that needs a figure', () => {
    expect(podBars(stats(), 'servfail', true).map((b) => b.id)).toEqual(['coredns-a']);
    expect(upstreamBars(stats(), 'p99', true).map((b) => b.id)).toEqual(['10.0.0.1:53']);
  });

  it('stacks rate series without leaving a gap where a code has no sample', () => {
    const paths = stackPaths({ NOERROR: [[1, 1], [2, 1], [3, 1]], SERVFAIL: [[2, 1]] }, ['NOERROR', 'SERVFAIL']);
    expect(paths.map((p) => p.code)).toEqual(['NOERROR', 'SERVFAIL']);
    expect(paths[1]?.d.startsWith('M')).toBe(true);
  });
});
