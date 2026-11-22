import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { Suspense } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { PAGE_ROWS, type Category, type PageAnswer, type Preemption, type PodView, type Row } from './data';
import { PendingPage } from './PendingPage';
import { PodBody } from './PodView';

afterEach(() => {
  vi.unstubAllGlobals();
});

/** Pending's page against the body `pending.go` writes. */

const ROUTE = '/x/prod/pending/';
const NOW = '2026-11-11T14:30:00Z';

function stub(body: unknown, status = 200) {
  const fetch = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
    if (url !== ROUTE) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
    return Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }));
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

function mount(entry = '/c/prod/x/pending') {
  const router = createMemoryRouter([{ path: '/c/:cluster/x/pending', element: <Suspense><PendingPage /></Suspense> }], {
    initialEntries: [entry],
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

function row(name: string, main: Category, over: Partial<Row> = {}): Row {
  return {
    namespace: 'shop',
    name,
    created: '2026-11-11T14:00:00Z',
    stage: main === 'starting' ? 'starting' : 'unschedulable',
    main,
    categories: [main],
    message: `0/3 nodes are available: 3 ${main}.`,
    verdict: { total: 3, reasons: [{ text: `reason ${main}`, nodes: 3, category: main }] },
    closest: { node: 'node-1', failing: 1 },
    request: { cpu: 1000, memory: 2 ** 30 },
    ...over,
  };
}

const READ = { state: 'read', complete: true } as const;

function page(rows: Row[], over: Partial<PageAnswer> = {}): PageAnswer {
  const counts: Partial<Record<Category, number>> = {};
  for (const r of rows) counts[r.main] = (counts[r.main] ?? 0) + 1;
  return {
    cluster: 'prod',
    namespace: '',
    read: NOW,
    pods: READ,
    capped: false,
    total: rows.length,
    counts,
    rows,
    nodes: READ,
    placed: READ,
    bars: [{ name: 'node-1', ready: true, cordoned: false, taints: 0, cpu: { allocatable: 2000, requested: 1500 }, memory: { allocatable: 4 * 2 ** 30, requested: 2 ** 30 }, pods: { allocatable: 110, requested: 3 } }],
    failures: [],
    failuresRead: READ,
    ...over,
  };
}

describe('Pending', () => {
  it('files the pods into tiles that filter the list', async () => {
    stub(page([row('a', 'resources'), row('b', 'resources'), row('c', 'taints')]));
    mount();
    const tile = await screen.findByRole('button', { name: /Taints/ });
    expect(tile.getAttribute('data-count')).toBe('1');
    expect(document.querySelectorAll('[data-row]')).toHaveLength(3);
    fireEvent.click(tile);
    await waitFor(() => expect(document.querySelectorAll('[data-row]')).toHaveLength(1));
    expect(document.querySelector('[data-row="shop/c"]')).not.toBeNull();
    fireEvent.click(screen.getByRole('button', { name: /Taints/ }));
    await waitFor(() => expect(document.querySelectorAll('[data-row]')).toHaveLength(3));
  });

  it('lays a picked pod’s request over the nodes, green where it fits and red where not', async () => {
    stub(page([row('a', 'resources')]));
    mount();
    fireEvent.click(await screen.findByTitle('Lay its request over the nodes'));
    // 1 CPU on a node with 500m free: does not fit; 1Gi of 3Gi free: fits.
    await waitFor(() => expect(document.querySelectorAll('[data-fill="pod"]')).toHaveLength(2));
    const [cpu, mem] = [...document.querySelectorAll('[data-fill="pod"]')];
    expect(cpu?.getAttribute('data-fits')).toBe('false');
    expect(mem?.getAttribute('data-fits')).toBe('true');
  });

  it('says a gated pod with its chip alone, the sentence in its tooltip', async () => {
    const message = 'Held back by its scheduling gates: example.com/quota-check.';
    const gated = row('held', 'gated', { stage: 'gated', message, verdict: { total: 0, reasons: [] } });
    delete gated.closest;
    stub(page([gated]));
    mount();
    const held = await waitFor(() => {
      const el = document.querySelector('[data-row="shop/held"]');
      expect(el).not.toBeNull();
      return el as HTMLElement;
    });
    expect(held.querySelector('[title]')?.getAttribute('title')).toBe(message);
    expect(held.querySelectorAll('p')).toHaveLength(0);
    expect(screen.queryByText(message)).toBeNull();
  });

  it('draws one screen of rows from five thousand, and reads once', async () => {
    const rows = Array.from({ length: 5000 }, (_, i) => row(`p-${String(i).padStart(5, '0')}`, i % 3 === 0 ? 'taints' : 'resources'));
    const fetch = stub(page(rows));
    const started = performance.now();
    mount();
    await screen.findByRole('button', { name: /Taints/ });
    expect(document.querySelectorAll('[data-row]')).toHaveLength(PAGE_ROWS);
    expect(performance.now() - started).toBeLessThan(5000);
    fireEvent.click(screen.getByRole('button', { name: /Show 200 more of 4800/ }));
    await waitFor(() => expect(document.querySelectorAll('[data-row]')).toHaveLength(2 * PAGE_ROWS));
    // Settled: no read loop and no re-render storm asking again.
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it('leads a row with the reason that blocks the pod, the control-plane taint after it', async () => {
    const taint = { text: 'node(s) had untolerated taint {node-role.kubernetes.io/control-plane: }', nodes: 1, category: 'taints' as const };
    const mem = { text: 'Insufficient memory', nodes: 3, category: 'resources' as const };
    stub(page([row('big', 'resources', { verdict: { total: 4, reasons: [taint, mem] } })]));
    mount();
    expect(await screen.findByText('Insufficient memory (3/4 nodes) + 1 more')).toBeTruthy();
  });

  it('opens filtered from ?namespace= and writes the choice back by replacing the entry', async () => {
    stub(page([row('a', 'resources'), row('b', 'resources', { namespace: 'batch' })]));
    const router = mount('/c/prod/x/pending?namespace=batch');
    await waitFor(() => expect(document.querySelectorAll('[data-row]')).toHaveLength(1));
    expect((screen.getByLabelText('Namespace') as HTMLSelectElement).value).toBe('batch');
    fireEvent.change(screen.getByLabelText('Namespace'), { target: { value: 'shop' } });
    await waitFor(() => expect(router.state.location.search).toBe('?namespace=shop'));
    expect(router.state.historyAction).toBe('REPLACE');
    fireEvent.change(screen.getByLabelText('Namespace'), { target: { value: '' } });
    await waitFor(() => expect(router.state.location.search).toBe(''));
  });

  it('tells nothing pending, refused and partial apart', async () => {
    stub(page([]));
    mount();
    expect(await screen.findByText('Nothing pending')).toBeTruthy();
  });

  it('says a refusal in its own words', async () => {
    stub({ code: 'list_forbidden', message: 'The cluster says you may not list pods here, so no pending pod can be shown.' }, 403);
    mount();
    expect(await screen.findByText(/may not list pods here/)).toBeTruthy();
    expect(document.querySelector('[data-nothing-pending]')).toBeNull();
  });

  it('says a narrowed answer is partial', async () => {
    stub(page([], { pods: { state: 'read', complete: false, note: 'Shown only from the namespaces you may list.' } }));
    mount();
    expect(await screen.findByText('Pods: partial')).toBeTruthy();
    expect(screen.getByText('Nothing pending in the namespaces you may read')).toBeTruthy();
  });

  it('shows the workloads a quota refused, with the quota', async () => {
    stub(
      page([], {
        failures: [
          {
            namespace: 'shop', kind: 'ReplicaSet', name: 'api-1', category: 'quota', count: 4, last: NOW,
            message: 'exceeded quota: small, requested: requests.cpu=3, used: requests.cpu=2, limited: requests.cpu=2',
            quota: { name: 'small', resource: 'requests.cpu', requested: '3', used: 2000, hard: 2000, room: false },
          },
        ],
      }),
    );
    mount();
    expect(await screen.findByText(/2 of 2 used · asked 3/)).toBeTruthy();
    expect(screen.getByRole('button', { name: /Cannot create pods/ }).getAttribute('data-count')).toBe('1');
  });
});

describe('one pod', () => {
  const view: PodView = {
    cluster: 'prod', pending: true, phase: 'Pending', read: NOW, row: row('a', 'resources'),
    columns: [
      { key: 'resource:cpu', label: 'CPU', computed: true, scheduler: 0 },
      { key: 'podAffinity', label: 'Pod affinity', computed: false, scheduler: 2 },
    ],
    fits: [{ node: 'node-1', cells: [{ key: 'resource:cpu', ok: false, detail: 'needs 1, 500m free of 2' }, { key: 'podAffinity', ok: null, detail: 'per the scheduler' }], failing: 1, excluded: 0, deficit: 0.25 }],
    nodesTotal: 1, bars: [], wouldTake: ['On node-1, the closest node: CPU needs 1, 500m free of 2.'], claims: [],
    events: [{ type: 'Warning', reason: 'FailedScheduling', message: 'nope', first: '2026-11-11T14:01:00Z', last: '2026-11-11T14:29:00Z', count: 9, source: 'default-scheduler' }],
    eventsCapped: false,
  };

  it('draws a check nothing computes as unknown, never as a pass', () => {
    render(<PodBody view={view} />);
    expect(document.querySelector('[data-cell="resource:cpu"]')?.getAttribute('data-ok')).toBe('false');
    expect(document.querySelector('[data-cell="podAffinity"]')?.getAttribute('data-ok')).toBe('unknown');
    expect(screen.getByText(/CPU needs 1, 500m free of 2/)).toBeTruthy();
    expect(document.querySelectorAll('[data-event]')).toHaveLength(1);
  });

  it('draws pod affinity and spread as an answer once they are computed, and says why when they are not', () => {
    const placed: PodView = {
      ...view,
      columns: [
        { key: 'podAffinity', label: 'Pod affinity', computed: true, scheduler: 2 },
        { key: 'spread', label: 'Spread', computed: false, scheduler: 0, why: 'the pods on the nodes were not read whole' },
      ],
      fits: [
        {
          node: 'node-1', failing: 1, excluded: 1, deficit: 0,
          cells: [
            { key: 'podAffinity', ok: false, detail: 'pod shop/db-0 has an anti-affinity that excludes it on kubernetes.io/hostname=node-1' },
            { key: 'spread', ok: null, detail: 'per the scheduler' },
          ],
        },
        { node: 'node-2', failing: 0, excluded: 0, deficit: 0, cells: [{ key: 'podAffinity', ok: true }, { key: 'spread', ok: null, detail: 'per the scheduler' }] },
      ],
      nodesTotal: 2,
    };
    render(<PodBody view={placed} />);
    const refused = document.querySelector('[data-fit="node-1"] [data-cell="podAffinity"]');
    expect(refused?.getAttribute('data-ok')).toBe('false');
    expect(refused?.getAttribute('title')).toContain('pod shop/db-0 has an anti-affinity');
    expect(document.querySelector('[data-fit="node-2"] [data-cell="podAffinity"]')?.getAttribute('data-ok')).toBe('true');
    expect(document.querySelector('[data-fit="node-1"] [data-cell="spread"]')?.getAttribute('data-ok')).toBe('unknown');
    const headings = [...document.querySelectorAll('[role="columnheader"]')];
    expect(headings.find((h) => h.textContent === 'Pod affinity')?.getAttribute('title')).toBe('Computed from the pods placed on the nodes');
    expect(headings.find((h) => h.textContent?.startsWith('Spread'))?.getAttribute('title')).toBe(
      'Not computed here, the pods on the nodes were not read whole: 0 node(s) refused per the scheduler',
    );
  });

  it('draws the scheduler’s preemption verdict with the counts, and the pod’s priority', () => {
    const withPreemption = (preemption: Preemption): PodView => ({ ...view, row: { ...row('a', 'resources'), preemption } });
    const { unmount } = render(
      <PodBody view={withPreemption({ verdict: 'mixed', total: 5, unhelpful: 3, noVictims: 2, priority: 1000, class: 'standard' })} />,
    );
    const strip = document.querySelector('[data-preemption="mixed"]');
    expect(strip?.querySelector('[data-chip="verdict"]')?.textContent).toContain('Eviction would not help');
    expect(strip?.querySelector('[data-chip="priority"]')?.textContent).toContain('standard · 1000');
    expect(strip?.querySelector('[data-count="unhelpful"]')?.textContent).toContain('3 not helpful');
    expect(strip?.querySelector('[data-count="noVictims"]')?.textContent).toContain('2 no victims');
    unmount();
    // A nominated node: eviction helps there, and the matrix marks the row.
    render(
      <PodBody
        view={{
          ...withPreemption({ verdict: 'nominated', total: 1, unhelpful: 0, noVictims: 0, nominated: 'node-1', priority: 100000, class: 'critical' }),
        }}
      />,
    );
    expect(document.querySelector('[data-chip="verdict"]')?.textContent).toContain('Nominated node-1');
    expect(document.querySelector('[data-fit="node-1"] [data-nominated]')).not.toBeNull();
    expect(document.querySelector('[data-preemption-counts]')).toBeNull();
  });

  it('draws no preemption strip for a pod the scheduler has said nothing about', () => {
    render(<PodBody view={view} />);
    expect(document.querySelector('[data-preemption]')).toBeNull();
  });

  it('says a pod that is no longer pending is not', () => {
    render(<PodBody view={{ ...view, pending: false, phase: 'Running', row: undefined } as unknown as PodView} />);
    expect(document.querySelector('[data-not-pending]')?.textContent).toBe('Not pending · Running');
  });
});
