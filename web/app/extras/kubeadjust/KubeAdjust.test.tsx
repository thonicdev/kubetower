import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { NodeCard, NodeResource, NodeTab, Page, WorkloadCard } from './data';
import { kubeadjust, PAGE_PATH } from './index';

afterEach(() => {
  vi.unstubAllGlobals();
});

/**
 * The page, mounted the way the console mounts it, with a fetch stub standing
 * in for `extras/kubeadjust`. Every body is the shape the Go route writes
 * (`view.go`, `nodes.go`); the Go tests hold what the server says, these hold
 * what the page draws of it.
 */

const ROUTE = '/x/docker-desktop/kubeadjust/';

type Replies = Record<string, Page | NodeTab | { status: number; body: string; contentType?: string }>;

function stub(replies: Replies): { fetch: ReturnType<typeof vi.fn>; signals: Map<string, AbortSignal> } {
  const signals = new Map<string, AbortSignal>();
  const fetch = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
    if (init?.signal) signals.set(url, init.signal);
    const reply = replies[url];
    if (reply === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
    if ('status' in reply && 'body' in reply && typeof reply.body === 'string') {
      return Promise.resolve(new Response(reply.body, { status: reply.status, headers: { 'Content-Type': reply.contentType ?? 'application/json' } }));
    }
    return Promise.resolve(new Response(JSON.stringify(reply), { status: 200, headers: { 'Content-Type': 'application/json' } }));
  });
  vi.stubGlobal('fetch', fetch);
  return { fetch, signals };
}

function mount(path: string): ReturnType<typeof createMemoryRouter> {
  const router = createMemoryRouter([{ path: 'c/:cluster', children: kubeadjust.routes }], {
    initialEntries: [`/c/docker-desktop/${path}`],
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

const NOTES = [
  { ok: true, label: 'Usage', detail: 'metrics-server, sampled now' },
  { ok: true, label: 'Node capacity', detail: '3 node(s)' },
  { ok: false, label: 'Disk', detail: 'no kubelet could be asked for disk usage (nodes/proxy is forbidden)' },
  { ok: false, label: 'History', detail: 'this installation has no Prometheus configured — so every suggestion below rests on a single sample per replica: there is no P95, no burst ratio and no trend' },
];

function page(over: Partial<Page> = {}): Page {
  return {
    cluster: 'docker-desktop',
    namespace: '',
    namespaces: ['data', 'shop'],
    notes: NOTES,
    window: 'no-prometheus',
    ready: true,
    workloads: 4,
    counts: [
      { kind: 'danger', label: 'At risk', count: 1 },
      { kind: 'warning', label: 'Worth looking at', count: 0 },
      { kind: 'overkill', label: 'Over-provisioned', count: 2 },
    ],
    categories: [
      { key: 'cpu', label: 'CPU', count: 1 },
      { key: 'memory', label: 'Memory', count: 2 },
      { key: 'storage', label: 'Storage', count: 0 },
    ],
    groups: [],
    commands: [],
    headline: { scope: 'this cluster', atRisk: 1, worth: 0, over: 2, reclaimCPU: '600m', reclaimMemory: '', reclaimWorkloads: 1, usedOfRequestedCPU: 13, usedOfRequestedMemory: 46 },
    top: [],
    totals: { namespaces: 2, cpuTotal: '540m', cpuAvg: '270m', memTotal: '2.78 GiB', memAvg: '1.39 GiB', source: 'live usage', fromRequests: false },
    namespaceCards: [
      { name: 'shop', pods: 3, workloads: 1, findings: 2, measured: true, band: 'overcommit', worst: 'danger', cpuRatio: '×7.4', memRatio: '×2.1', cpuRequest: '750m', cpuUsed: '100m', memRequest: '1.50 GiB', memUsed: '700 MiB', coverage: '', cpuIdle: '', memIdle: '' },
      { name: 'data', pods: 1, workloads: 1, findings: 0, measured: true, band: 'norequests', worst: '', cpuRatio: 'none set', memRatio: 'none set', cpuRequest: '0m', cpuUsed: '50m', memRequest: '0 B', memUsed: '64 MiB', coverage: '', cpuIdle: '', memIdle: '' },
      { name: 'dark', pods: 2, workloads: 1, findings: 0, measured: false, band: '', worst: '', cpuRatio: '—', memRatio: '—', cpuRequest: '200m', cpuUsed: '', memRequest: '256 MiB', memUsed: '', coverage: '', cpuIdle: '', memIdle: '' },
    ],
    nodeCards: null,
    nodeStates: null,
    cards: null,
    partial: '',
    ...over,
  };
}

function nodeResource(over: Partial<NodeResource> = {}): NodeResource {
  return {
    key: 'cpu',
    label: 'CPU',
    allocatable: '8.00 cores',
    requested: '4.00 cores',
    limited: '12.00 cores',
    used: '1.60 cores',
    requestedPct: 50,
    limitedPct: 150,
    usedPct: 20,
    requestBar: 50,
    limitBar: 100,
    useBar: 20,
    measured: true,
    coverage: '',
    status: 'healthy',
    noAllocatable: false,
    gap: '30 points reserved and not used',
    ...over,
  };
}

function nodeCard(over: Partial<NodeCard> = {}): NodeCard {
  const unmeasured = { used: '', usedPct: null, useBar: 0, measured: false, gap: '' };
  const resources = (measured: boolean) => [
    nodeResource(measured ? {} : unmeasured),
    nodeResource({ key: 'memory', label: 'Memory', allocatable: '16.00 GiB', requested: '8.00 GiB', limited: '8.00 GiB', used: '3.20 GiB', limitedPct: 50, limitBar: 50, gap: '', ...(measured ? {} : unmeasured) }),
    nodeResource({ key: 'ephemeral-storage', label: 'Ephemeral storage', allocatable: '', requested: '0 KiB', limited: '0 KiB', noAllocatable: true, ...unmeasured }),
  ];
  const measured = over.state !== 'none';
  return {
    name: 'worker-1',
    ready: true,
    pods: 34,
    maxPods: 110,
    podsTight: false,
    roles: ['worker'],
    controlPlane: false,
    age: '3d',
    osImage: 'Docker Desktop',
    kernel: '6.6.30-0',
    kubelet: 'v1.31.4',
    pressures: [],
    taints: [],
    resources: resources(measured),
    topPods: [],
    totalPods: 34,
    more: '',
    state: 'healthy',
    ...over,
  };
}

const finding = {
  workload: 'api',
  workloadKind: 'Deployment',
  namespace: 'shop',
  pod: 'api-1',
  podCount: 3,
  container: 'app',
  resource: 'Memory',
  kind: 'danger' as const,
  action: 'Increase limit',
  message: 'Container was OOMKilled — memory limit is too low',
  current: '1.00 GiB',
  suggested: '1.50 GiB',
  appliesToBoth: false,
  warnings: ['2 restarts observed (OOMKilled)'],
  kubectl: 'kubectl set resources deployment/api -c app --limits=memory=1536Mi -n shop',
  scope: 'Deployment api · shop',
  confidence: 'low' as const,
  insufficientData: false,
};

function workload(over: Partial<WorkloadCard> = {}): WorkloadCard {
  const bar = (label: string, measured: boolean) => ({
    label,
    measured,
    status: measured ? ('danger' as const) : ('none' as const),
    mean: measured ? '950 MiB' : '',
    peak: measured ? '1000 MiB' : '',
    request: '512 MiB',
    limit: '1.00 GiB',
    usePct: measured ? 92.8 : 0,
    reqPct: 50,
    peakPct: measured ? 97.7 : 0,
    pct: measured ? 93 : null,
    scale: 'limit',
    headroom: measured ? '24 MiB' : '',
    unbounded: false,
    insufficient: false,
  });
  return {
    workload: 'api',
    kind: 'Deployment',
    namespace: 'shop',
    replicas: 3,
    qos: 'Burstable',
    managed: [],
    worst: 'danger',
    containers: [
      {
        name: 'app',
        restarts: 2,
        crashLooping: false,
        oomKilled: true,
        oomKilledEarlier: false,
        memoryRaised: null,
        cpu: bar('CPU', true),
        memory: bar('Memory', true),
        ephemeral: bar('Ephemeral', false),
        pods: [
          { pod: 'api-1', cpu: '150m', memory: '900 MiB', measured: true },
          { pod: 'api-2', cpu: '', memory: '', measured: false },
        ],
      },
    ],
    findings: [finding],
    volumes: [],
    storageOK: true,
    ...over,
  };
}

describe('KubeAdjust', () => {
  it('is reachable from the menu at x/kubeadjust, under its own label', async () => {
    expect(kubeadjust.key).toBe('kubeadjust');
    expect(kubeadjust.nav).toEqual({ label: 'KubeAdjust', path: 'x/kubeadjust' });
    expect(PAGE_PATH).toBe('x/kubeadjust');
    stub({ [ROUTE]: page() });
    mount(PAGE_PATH);
    expect(await screen.findByRole('heading', { name: 'KubeAdjust', level: 1 })).toBeTruthy();
    expect(document.getElementById('kubeadjust')).not.toBeNull();
  });

  it('says how old a history the console kept is, in words', async () => {
    stub({ [ROUTE]: page({ windowReadAt: new Date(Date.now() - 15 * 60_000).toISOString() }) });
    mount(PAGE_PATH);
    expect(await screen.findByText('History read 15 minutes ago')).toBeTruthy();
  });

  it('draws an unmeasured cluster as unmeasured, never as a well-tuned one', async () => {
    stub({
      [ROUTE]: page({
        ready: false,
        notes: [{ ok: false, label: 'Usage', detail: 'no per-container usage (absent): the cluster has no metrics-server' }, ...NOTES.slice(1)],
        counts: [
          { kind: 'danger', label: 'At risk', count: null },
          { kind: 'warning', label: 'Worth looking at', count: null },
          { kind: 'overkill', label: 'Over-provisioned', count: null },
        ],
        namespaceCards: [],
        totals: null,
      }),
    });
    mount(PAGE_PATH);
    expect(await screen.findByText(/This is not a cluster with nothing to fix/)).toBeTruthy();
    expect(screen.getByText('not measured')).toBeTruthy();
    expect(screen.queryByText(/nothing to change/i)).toBeNull();
    expect(screen.queryByText(/At risk/)).toBeNull();
    // The four notes are drawn, the failing one in the warning tone.
    for (const label of ['Usage', 'Node capacity', 'Disk', 'History']) {
      expect(document.querySelector(`[data-note="${label}"]`)).not.toBeNull();
    }
    expect(document.querySelector('[data-note="Usage"]')?.getAttribute('data-ok')).toBe('false');
  });

  it('keeps the namespace menu outside every panel, so nothing clips it', async () => {
    stub({ [ROUTE]: page() });
    mount(PAGE_PATH);
    await screen.findByRole('link', { name: 'shop on docker-desktop' });
    const menu = screen.getByRole('combobox', { name: 'Namespace' });
    for (let el = menu.parentElement; el !== null; el = el.parentElement) {
      expect(el.className).not.toMatch(/\boverflow-(hidden|auto|clip)\b/);
      expect(el.tagName).not.toBe('SECTION');
    }
    // Every namespace is offered, and choosing one moves the page to it.
    const options = within(menu).getAllByRole('option').map((o) => o.textContent);
    expect(options).toEqual(['All namespaces', 'data', 'shop']);
  });

  it('tables the namespaces worst first, with the three absences told apart', async () => {
    const cards = page().namespaceCards ?? [];
    stub({
      [ROUTE]: page({
        namespaceCards: cards.map((c) => (c.name === 'shop' ? { ...c, cpuIdle: '650m', memIdle: '836 MiB' } : c)),
      }),
    });
    mount(PAGE_PATH);
    const shop = await screen.findByRole('link', { name: 'shop on docker-desktop' });
    expect(shop.getAttribute('href')).toBe('/c/docker-desktop/x/kubeadjust?ns=shop');
    const row = document.querySelector('[data-namespace-card="shop"]') as HTMLElement;
    expect(within(row).getByText('×7.4')).toBeTruthy();
    // What it reserves and leaves idle: the waste, in the unit it is paid in.
    expect(within(row).getByText('650m')).toBeTruthy();
    expect(within(row).getByText('836 MiB')).toBeTruthy();
    // The bands are over-requesting, not "overcommit".
    const words = Array.from(document.querySelectorAll('[data-band-word]')).map((e) => e.textContent);
    expect(words).toEqual(['far over-requested', 'no requests set', 'not measured']);
    const dark = document.querySelector('[data-namespace-card="dark"]') as HTMLElement;
    // Nothing measured: an em dash in each idle cell, never "none" (which
    // says usage meets the requests) and never a zero.
    expect(dark.querySelector('[data-col="cpu-idle"]')?.textContent).toBe('—');
    expect(dark.querySelector('[data-col="mem-idle"]')?.textContent).toBe('—');
    expect(row.querySelector('[data-col="cpu-idle"]')?.textContent).toBe('650m');
  });

  it('reads top-down: a headline, the sources in one line, and only what failed spelled out', async () => {
    stub({ [ROUTE]: page() });
    mount(PAGE_PATH);
    const headline = await screen.findByRole('region', { name: 'In this cluster' });
    expect(within(headline).getByText('At risk')).toBeTruthy();
    expect(document.querySelector('[data-headline-count="danger"]')?.textContent).toContain('1');
    // A severity at zero is drawn as a zero, not left out.
    expect(document.querySelector('[data-headline-count="warning"]')?.textContent).toContain('0');
    expect(within(headline).getByText('600m')).toBeTruthy();
    expect(within(headline).getByText('CPU 13% · memory 46%')).toBeTruthy();
    // The four notes are behind one line; the two that failed are spelled out.
    const failed = Array.from(document.querySelectorAll('[data-note-failed]')).map((e) => e.getAttribute('data-note-failed'));
    expect(failed).toEqual(['Disk', 'History']);
    for (const label of ['Usage', 'Node capacity', 'Disk', 'History']) {
      expect(document.querySelector(`[data-note="${label}"]`)?.closest('details')).not.toBeNull();
    }
  });

  it('says what to act on first, each row leading to its namespace', async () => {
    const { fetch } = stub({
      [ROUTE]: page({ top: [finding] }),
      [`${ROUTE}?ns=shop`]: page({ namespace: 'shop', namespaceCards: null, totals: null, cards: [workload()] }),
    });
    const router = mount(PAGE_PATH);
    const top = await screen.findByRole('region', { name: 'Act on first' });
    const row = within(top).getByText('Increase limit').closest('li') as HTMLElement;
    expect(row.getAttribute('data-top-row')).toBe('danger');
    expect(within(row).getByText('1.50 GiB')).toBeTruthy();
    // Followed, not only read: the namespace's view is asked for and drawn.
    fireEvent.click(within(row).getByRole('link', { name: 'shop' }));
    await waitFor(() => expect(router.state.location.search).toBe('?ns=shop'));
    expect(await screen.findByRole('heading', { name: 'api' })).toBeTruthy();
    expect(fetch.mock.calls.some(([url]) => String(url).endsWith('?ns=shop'))).toBe(true);
  });

  it('labels each finding with how much its figure rests on, and names no value where there is too little', async () => {
    const short = {
      ...finding,
      resource: 'CPU',
      action: 'Reduce request',
      kind: 'overkill' as const,
      message: 'CPU request is 6.7× its P95 usage · 6h of history',
      current: '1.00 cores',
      suggested: '',
      kubectl: '',
      confidence: 'low' as const,
      insufficientData: true,
    };
    stub({
      [`${ROUTE}?ns=shop`]: page({
        namespace: 'shop',
        namespaceCards: null,
        totals: null,
        cards: [workload()],
        groups: [
          { kind: 'danger', label: 'critical', mark: '▲', rows: [{ ...finding, confidence: 'high' }] },
          { kind: 'overkill', label: 'over-provisioned', mark: '▽', rows: [short] },
        ],
        commands: [finding.kubectl],
      }),
    });
    mount(`${PAGE_PATH}?ns=shop`);
    const side = await screen.findByRole('complementary', { name: 'Suggestions' });
    const high = within(side).getByText('Increase limit').closest('li') as HTMLElement;
    expect(high.querySelector('[data-confidence]')?.getAttribute('data-confidence')).toBe('high');
    expect(within(high).getByText('high confidence')).toBeTruthy();
    const withheld = within(side).getByText('Reduce request').closest('li') as HTMLElement;
    expect(withheld.querySelector('[data-confidence]')?.getAttribute('data-confidence')).toBe('insufficient');
    expect(within(withheld).getByText('insufficient data')).toBeTruthy();
    // No value after the arrow is said as such, and no command is drawn.
    expect(within(withheld).getByText('no value')).toBeTruthy();
    expect(withheld.querySelector('code')).toBeNull();
  });

  it('says nothing to act on in words, when there is nothing', async () => {
    stub({ [ROUTE]: page({ top: [] }) });
    mount(PAGE_PATH);
    expect(await screen.findByText('Nothing to change in this cluster.')).toBeTruthy();
  });

  it('says what kind of machine each node is, with its resources as bars and the figures beside them', async () => {
    stub({
      [ROUTE]: page({
        namespaceCards: null,
        totals: null,
        nodeCards: [
          nodeCard({
            name: 'cp-1',
            roles: ['control-plane'],
            controlPlane: true,
            pods: 100,
            podsTight: true,
            pressures: ['disk pressure'],
            taints: [{ text: 'control-plane', effect: 'NoSchedule', title: 'node-role.kubernetes.io/control-plane · NoSchedule' }],
            topPods: [
              {
                name: 'etcd-cp-1',
                title: 'kube-system/etcd-cp-1',
                cpu: { reqPct: 6.3, usePct: 5.3, status: 'overkill', pct: '5%', measured: true, title: 'reserved 500m (6%) · used 420m (5%)' },
                mem: { reqPct: 1, usePct: 0, status: '', pct: '1%', measured: false, title: 'reserved 100 MiB (1%) · no usage was reported for this pod' },
              },
            ],
          }),
        ],
        nodeStates: [
          { key: 'danger', label: 'Near capacity', count: 0 },
          { key: 'warning', label: 'Busy', count: 0 },
          { key: 'healthy', label: 'Balanced', count: 1 },
          { key: 'overkill', label: 'Barely used', count: 0 },
          { key: 'none', label: 'Not measured', count: 0 },
        ],
      }),
    });
    mount(`${PAGE_PATH}?view=nodes`);
    const card = (await screen.findByRole('heading', { name: 'cp-1' })).closest('section') as HTMLElement;
    for (const text of ['control-plane', '100 / 110 pods', '3d', 'v1.31.4', 'kernel 6.6.30-0', 'disk pressure', 'control-plane · NoSchedule', 'etcd-cp-1']) {
      expect(within(card).getAllByText(text).length).toBeGreaterThan(0);
    }
    expect(within(card).getAllByText(/8\.00 cores allocatable/)).toHaveLength(1);
    // No ring: the three bars of the node tab, each figure beside its track.
    expect(card.querySelector('svg')).toBeNull();
    expect(Array.from(card.querySelectorAll('[data-node-resource]')).map((e) => e.getAttribute('data-node-resource'))).toEqual(['cpu', 'memory', 'ephemeral-storage']);
    const cpu = card.querySelector('[data-node-resource="cpu"]') as HTMLElement;
    const figure = cpu.querySelector('[data-figure]') as HTMLElement;
    const track = cpu.querySelector('[data-fill="used"]')?.parentElement as HTMLElement;
    expect(figure.textContent).toBe('20% used');
    expect(track.contains(figure)).toBe(false);
    expect(figure.parentElement).toBe(track.parentElement);
    expect(track.textContent).toBe('');
    // Placed from the server's geometry, not recomputed.
    expect((cpu.querySelector('[data-fill="used"]') as HTMLElement).style.width).toBe('20%');
    expect(cpu.textContent).toContain('30 points reserved and not used');
    // The overcommitment is said beside the bar, unclamped, in the bad tone.
    expect(within(cpu).getByText(/limits 12\.00 cores/).className).toContain('text-bad');
    expect((cpu.querySelector('[data-mark="limits"]') as HTMLElement).className).toContain('bg-bad');
    // An unmeasured pod share is the reservation, muted, with no usage fill.
    const memTrack = card.querySelectorAll('[data-top-pod="etcd-cp-1"] [data-measured]')[1];
    expect(memTrack?.getAttribute('data-measured')).toBe('false');
  });

  it('draws an unmeasured node as its reservation, labeled, with no usage fill', async () => {
    stub({
      [ROUTE]: page({
        namespaceCards: null,
        totals: null,
        nodeCards: [nodeCard({ name: 'quiet', state: 'none' })],
        nodeStates: [
          { key: 'danger', label: 'Near capacity', count: null },
          { key: 'warning', label: 'Busy', count: null },
          { key: 'healthy', label: 'Balanced', count: null },
          { key: 'overkill', label: 'Barely used', count: null },
          { key: 'none', label: 'Not measured', count: 1 },
        ],
      }),
    });
    mount(`${PAGE_PATH}?view=nodes`);
    const card = (await screen.findByRole('heading', { name: 'quiet' })).closest('section') as HTMLElement;
    expect(within(card).getAllByText('50% reserved')).toHaveLength(2);
    expect(card.querySelector('[data-fill="used"]')).toBeNull();
    expect(within(card).getAllByText('usage not measured')).toHaveLength(3);
  });

  it('lines the node cards up with the toolbar: one column of the page, no inset of their own', async () => {
    stub({
      [ROUTE]: page({ namespaceCards: null, totals: null, nodeCards: [nodeCard()], nodeStates: [] }),
    });
    mount(`${PAGE_PATH}?view=nodes`);
    const card = (await screen.findByRole('heading', { name: 'worker-1' })).closest('section') as HTMLElement;
    const toolbar = document.querySelector('[data-toolbar]') as HTMLElement;
    const root = document.getElementById('kubeadjust') as HTMLElement;
    // The card and the toolbar both descend from the page's one padded column,
    // and nothing between the card and that column adds a margin.
    expect(root.contains(toolbar) && root.contains(card)).toBe(true);
    for (let el: HTMLElement | null = card; el !== null && el !== root; el = el.parentElement) {
      expect(el.className).not.toMatch(/(^|\s)-?m[xytrbl]?-\d/);
    }
  });

  describe('the count tiles on the Nodes view', () => {
    const nodes = page({
      namespaceCards: null,
      totals: null,
      nodeCards: [
        nodeCard({ name: 'hot-1', state: 'danger' }),
        nodeCard({ name: 'idle-1', state: 'overkill' }),
        nodeCard({ name: 'idle-2', state: 'overkill' }),
        nodeCard({ name: 'dark-1', state: 'none' }),
      ],
      nodeStates: [
        { key: 'danger', label: 'Near capacity', count: 1 },
        { key: 'warning', label: 'Busy', count: 0 },
        { key: 'healthy', label: 'Balanced', count: 0 },
        { key: 'overkill', label: 'Barely used', count: 2 },
        { key: 'none', label: 'Not measured', count: 1 },
      ],
    });
    const shown = () => Array.from(document.querySelectorAll('[data-node]')).map((e) => e.getAttribute('data-node'));

    it('narrows the list to a tile, and a second click restores it', async () => {
      stub({ [ROUTE]: nodes });
      const router = mount(`${PAGE_PATH}?view=nodes`);
      await screen.findByRole('heading', { name: 'hot-1' });
      expect(shown()).toEqual(['hot-1', 'idle-1', 'idle-2', 'dark-1']);

      const tile = document.querySelector('[data-tile="overkill"]') as HTMLButtonElement;
      fireEvent.click(tile);
      await waitFor(() => expect(shown()).toEqual(['idle-1', 'idle-2']));
      expect(tile.getAttribute('aria-pressed')).toBe('true');
      // The filter is in the address, so a reload keeps it.
      expect(router.state.location.search).toContain('state=overkill');

      fireEvent.click(tile);
      await waitFor(() => expect(shown()).toEqual(['hot-1', 'idle-1', 'idle-2', 'dark-1']));
      expect(tile.getAttribute('aria-pressed')).toBe('false');
    });

    it('narrows to each tile in turn, and All restores the list', async () => {
      stub({ [ROUTE]: nodes });
      const router = mount(`${PAGE_PATH}?view=nodes`);
      await screen.findByRole('heading', { name: 'hot-1' });
      const tile = (key: string) => document.querySelector(`[data-tile="${key}"]`) as HTMLButtonElement;
      const expected: Record<string, string[]> = {
        danger: ['hot-1'],
        overkill: ['idle-1', 'idle-2'],
        none: ['dark-1'],
        warning: [],
        healthy: [],
      };
      for (const [key, names] of Object.entries(expected)) {
        fireEvent.click(tile(key));
        await waitFor(() => expect(router.state.location.search).toContain(`state=${key}`));
        await waitFor(() => expect(shown()).toEqual(names));
        expect(tile(key).getAttribute('aria-pressed')).toBe('true');
        if (names.length === 0) {
          const label = nodes.nodeStates?.find((s) => s.key === key)?.label.toLowerCase() ?? '';
          expect(screen.getByText(`No node is ${label}.`)).toBeTruthy();
        }
        // "All" clears whichever tile is picked.
        fireEvent.click(screen.getByRole('button', { name: 'All' }));
        await waitFor(() => expect(shown()).toEqual(['hot-1', 'idle-1', 'idle-2', 'dark-1']));
        expect(router.state.location.search).not.toContain('state=');
        expect(tile(key).getAttribute('aria-pressed')).toBe('false');
      }
    });

    it('ignores a picked state whose count nobody could establish, and says so', async () => {
      stub({
        [ROUTE]: {
          ...nodes,
          nodeCards: [nodeCard({ name: 'dark-1', state: 'none' }), nodeCard({ name: 'dark-2', state: 'none' })],
          nodeStates: [
            { key: 'danger', label: 'Near capacity', count: null },
            { key: 'warning', label: 'Busy', count: null },
            { key: 'healthy', label: 'Balanced', count: null },
            { key: 'overkill', label: 'Barely used', count: null },
            { key: 'none', label: 'Not measured', count: 2 },
          ],
        },
      });
      // A bookmark from a day the cluster was measured.
      mount(`${PAGE_PATH}?view=nodes&state=danger`);
      await screen.findByRole('heading', { name: 'dark-1' });
      expect(shown()).toEqual(['dark-1', 'dark-2']);
      expect(screen.queryByText(/No node is near capacity/)).toBeNull();
      expect(document.querySelector('[data-filter-ignored="danger"]')?.textContent).toContain('Near capacity');
      expect(document.querySelector('[data-tile="danger"]')?.getAttribute('aria-pressed')).toBe('false');
    });

    it('draws a zero as a zero, and an unmeasured state as unknown rather than zero', async () => {
      stub({
        [ROUTE]: {
          ...nodes,
          nodeCards: [nodeCard({ name: 'dark-1', state: 'none' })],
          nodeStates: [
            { key: 'danger', label: 'Near capacity', count: null },
            { key: 'warning', label: 'Busy', count: null },
            { key: 'healthy', label: 'Balanced', count: null },
            { key: 'overkill', label: 'Barely used', count: null },
            { key: 'none', label: 'Not measured', count: 1 },
          ],
        },
      });
      mount(`${PAGE_PATH}?view=nodes`);
      await screen.findByRole('heading', { name: 'dark-1' });
      const danger = document.querySelector('[data-tile="danger"]') as HTMLButtonElement;
      expect(danger.getAttribute('data-count')).toBe('unknown');
      expect(danger.textContent).toContain('—');
      expect(danger.textContent).not.toContain('0');
      expect(danger.disabled).toBe(true);
      expect(document.querySelector('[data-tile="none"]')?.getAttribute('data-count')).toBe('1');
    });

    it('draws a measured zero as 0', async () => {
      stub({ [ROUTE]: nodes });
      mount(`${PAGE_PATH}?view=nodes`);
      await screen.findByRole('heading', { name: 'hot-1' });
      const busy = document.querySelector('[data-tile="warning"]') as HTMLButtonElement;
      expect(busy.getAttribute('data-count')).toBe('0');
      expect(busy.textContent).toContain('0');
      expect(busy.disabled).toBe(false);
    });
  });

  it('draws one namespace: the card, its bars, its findings and every command in one block', async () => {
    stub({
      [`${ROUTE}?ns=shop`]: page({
        namespace: 'shop',
        namespaceCards: null,
        totals: null,
        cards: [workload()],
        groups: [{ kind: 'danger', label: 'critical', mark: '▲', rows: [finding] }],
        commands: [finding.kubectl],
      }),
    });
    mount(`${PAGE_PATH}?ns=shop`);
    const card = (await screen.findByRole('heading', { name: 'api' })).closest('section') as HTMLElement;
    for (const text of ['3 replicas', 'Burstable', 'OOMKilled', '2 restart(s)']) {
      expect(within(card).getByText(text)).toBeTruthy();
    }
    const memory = card.querySelector('[data-bar="Memory"]') as HTMLElement;
    expect((memory.querySelector('[data-fill]') as HTMLElement).style.width).toBe('92.8%');
    expect(within(memory).getByText('93% of limit')).toBeTruthy();
    expect(memory.textContent).toContain('24 MiB headroom');
    // The ephemeral bar was not measured, so it is not drawn as an empty one.
    expect(card.querySelector('[data-bar="Ephemeral"]')).toBeNull();
    // The per-replica breakdown, and a replica nothing measured says so.
    expect(within(card).getByText('not measured')).toBeTruthy();

    const side = screen.getByRole('complementary', { name: 'Suggestions' });
    expect(within(side).getByText('critical')).toBeTruthy();
    expect(within(side).getByText('Increase limit')).toBeTruthy();
    const block = side.querySelector('[data-commands] pre');
    expect(block?.textContent).toBe(finding.kubectl);
    expect(within(side).getByRole('button', { name: 'Copy all' })).toBeTruthy();
    // A short label, the sentence in its title.
    expect(within(side).queryByText(/KubeTower does not run them/)).toBeNull();
    const note = side.querySelector('[data-restarts]');
    expect(note?.textContent?.trim()).toBe('Restarts pods');
    expect(note?.getAttribute('title')).toMatch(/KubeTower does not run them/);
  });

  it('says on the card what else sizes the workload, and an older OOM kill apart from a recent one', async () => {
    const base = workload();
    const container = { ...base.containers[0]!, oomKilled: false, oomKilledEarlier: true };
    stub({
      [`${ROUTE}?ns=shop`]: page({
        namespace: 'shop',
        namespaceCards: null,
        totals: null,
        cards: [{ ...base, containers: [container], managed: ['HPA api scales this workload on CPU at 60% of the request: usage below the request is that target at work, so no reduction of what it scales on is suggested'] }],
      }),
    });
    mount(`${PAGE_PATH}?ns=shop`);
    const card = (await screen.findByRole('heading', { name: 'api' })).closest('section') as HTMLElement;
    expect(card.querySelector('[data-managed]')?.textContent).toContain('HPA api');
    expect(within(card).getByText('OOMKilled earlier')).toBeTruthy();
    expect(within(card).queryByText('OOMKilled')).toBeNull();
  });

  it('names a memory limit raised after a kill, with the reason no reduction is offered on hover', async () => {
    const base = workload();
    const container = {
      ...base.containers[0]!,
      oomKilled: false,
      restarts: 0,
      memoryRaised: { from: '2 GiB', to: '3 GiB', revision: 4 },
    };
    stub({
      [`${ROUTE}?ns=shop`]: page({
        namespace: 'shop',
        namespaceCards: null,
        totals: null,
        cards: [{ ...base, containers: [container], findings: [], worst: '' }],
      }),
    });
    mount(`${PAGE_PATH}?ns=shop`);
    const card = (await screen.findByRole('heading', { name: 'api' })).closest('section') as HTMLElement;
    expect(within(card).getByText('limit raised from 2 GiB')).toBeTruthy();
    const pill = card.querySelector('[data-memory-raised]') as HTMLElement;
    expect(pill.title).toContain('raised from 2 GiB to 3 GiB at revision 4');
    expect(pill.title).toContain('no reduction of this limit is suggested');
  });

  it('draws no raise pill for a container whose limit was not raised', async () => {
    stub({
      [`${ROUTE}?ns=shop`]: page({ namespace: 'shop', namespaceCards: null, totals: null, cards: [workload()] }),
    });
    mount(`${PAGE_PATH}?ns=shop`);
    const card = (await screen.findByRole('heading', { name: 'api' })).closest('section') as HTMLElement;
    expect(card.querySelector('[data-memory-raised]')).toBeNull();
  });

  it('says an unmeasured volume and a cluster with no disk figures in words, never as an empty disk', async () => {
    stub({
      [`${ROUTE}?ns=data`]: page({
        namespace: 'data',
        namespaceCards: null,
        totals: null,
        cards: [
          workload({
            workload: 'db',
            kind: 'StatefulSet',
            worst: '',
            findings: [],
            volumes: [
              { pod: 'db-0', name: 'data', claim: 'data-db-0', kind: 'pvc', status: 'none', pct: '', used: '', free: '', capacity: '5.00 GiB', usePct: null, measured: false, ceilingWord: 'capacity', pctOf: '', unbounded: false },
            ],
          }),
          workload({ workload: 'api', storageOK: false, volumes: [] }),
        ],
      }),
    });
    mount(`${PAGE_PATH}?ns=data`);
    const db = (await screen.findByRole('heading', { name: 'db' })).closest('section') as HTMLElement;
    const volume = db.querySelector('[data-volume="data"]') as HTMLElement;
    expect(volume.textContent).toContain('usage not measured');
    expect(volume.textContent).toContain('capacity 5.00 GiB');
    expect(volume.querySelector('[data-fill]')).toBeNull();
    const api = screen.getByRole('heading', { name: 'api' }).closest('section') as HTMLElement;
    expect(within(api).getByText(/Disks were not readable on this cluster/)).toBeTruthy();
  });

  it('filters by category through the address, with the counts over everything, and reads nothing again', async () => {
    const cpu = { ...finding, kind: 'overkill' as const, resource: 'CPU', action: 'Reduce request', kubectl: 'kubectl set resources deployment/api -c app --requests=cpu=100m -n shop' };
    const disk = { ...finding, kind: 'overkill' as const, resource: 'PVC', action: 'Expand', kubectl: '' };
    const raise = { ...finding, kind: 'overkill' as const, resource: 'Memory — no limit', action: 'Set limit', kubectl: finding.kubectl };
    const { fetch } = stub({
      [`${ROUTE}?ns=shop`]: page({
        namespace: 'shop',
        namespaceCards: null,
        totals: null,
        cards: [workload()],
        categories: [
          { key: 'cpu', label: 'CPU', count: 1 },
          { key: 'memory', label: 'Memory', count: 2 },
          { key: 'storage', label: 'Storage', count: 1 },
        ],
        groups: [
          { kind: 'danger', label: 'critical', mark: '▲', rows: [finding] },
          { kind: 'overkill', label: 'over-provisioned', mark: '▼', rows: [cpu, raise, disk] },
        ],
        commands: [finding.kubectl, cpu.kubectl],
      }),
    });
    const router = mount(`${PAGE_PATH}?ns=shop`);
    const side = await screen.findByRole('complementary', { name: 'Suggestions' });
    const drawn = () => Array.from(side.querySelectorAll('[data-group]')).map((g) => `${g.getAttribute('data-group')}:${within(g as HTMLElement).getAllByRole('listitem').length}`);
    const block = () => side.querySelector('[data-commands] pre')?.textContent ?? '';
    expect(drawn()).toEqual(['danger:1', 'overkill:3']);

    fireEvent.click(within(side).getByRole('button', { name: /Memory/ }));
    await waitFor(() => expect(router.state.location.search).toBe('?ns=shop&cat=memory'));
    await waitFor(() => expect(document.querySelector('[data-category="memory"]')?.getAttribute('aria-pressed')).toBe('true'));
    // What the server drew under cat=memory: the memory rows in their groups,
    // and their one command once.
    expect(drawn()).toEqual(['danger:1', 'overkill:1']);
    expect(block()).toBe(finding.kubectl);
    // The chips still count everything.
    expect(document.querySelector('[data-category="cpu"]')?.textContent).toContain('1');
    expect(document.querySelector('[data-category="memory"]')?.textContent).toContain('2');
    expect(document.querySelector('[data-category="storage"]')?.textContent).toContain('1');

    fireEvent.click(document.querySelector('[data-category="storage"]') as HTMLElement);
    await waitFor(() => expect(router.state.location.search).toBe('?ns=shop&cat=storage'));
    expect(drawn()).toEqual(['overkill:1']);
    expect(side.querySelector('[data-commands]')).toBeNull();

    fireEvent.click(document.querySelector('[data-category="storage"]') as HTMLElement);
    await waitFor(() => expect(router.state.location.search).toBe('?ns=shop'));
    expect(drawn()).toEqual(['danger:1', 'overkill:3']);
    expect(block()).toBe(`${finding.kubectl}\n${cpu.kubectl}`);
    // Three chips, one read: a filter is the page's, never the cluster's.
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it('says a filter left nothing, apart from a namespace with nothing to change', async () => {
    stub({
      [`${ROUTE}?ns=shop`]: page({
        namespace: 'shop',
        namespaceCards: null,
        totals: null,
        cards: [workload()],
        groups: [{ kind: 'danger', label: 'critical', mark: '▲', rows: [finding] }],
        commands: [finding.kubectl],
      }),
    });
    mount(`${PAGE_PATH}?ns=shop&cat=cpu`);
    const empty = await screen.findByText("Nothing under this filter");
    expect(empty.getAttribute('title')).toMatch(/Other categories may still have findings/);
  });

  it('shows everything for a category it does not know', async () => {
    stub({
      [`${ROUTE}?ns=shop`]: page({
        namespace: 'shop',
        namespaceCards: null,
        totals: null,
        cards: [workload()],
        groups: [{ kind: 'danger', label: 'critical', mark: '▲', rows: [finding] }],
        commands: [finding.kubectl],
      }),
    });
    mount(`${PAGE_PATH}?ns=shop&cat=cpus`);
    const side = await screen.findByRole('complementary', { name: 'Suggestions' });
    expect(side.querySelectorAll('[data-group]')).toHaveLength(1);
    for (const chip of Array.from(side.querySelectorAll('[data-category]'))) expect(chip.getAttribute('aria-pressed')).toBe('false');
  });

  it('drops the category filter when another namespace is chosen', async () => {
    const shop = page({ namespace: 'shop', namespaceCards: null, totals: null, cards: [workload()] });
    const data = page({ namespace: 'data', namespaceCards: null, totals: null, cards: [] });
    stub({ [`${ROUTE}?ns=shop`]: shop, [`${ROUTE}?ns=data`]: data });
    const router = mount(`${PAGE_PATH}?ns=shop&cat=memory`);
    await screen.findByRole('heading', { name: 'api' });
    fireEvent.change(screen.getByRole('combobox', { name: 'Namespace' }), { target: { value: 'data' } });
    await waitFor(() => expect(router.state.location.search).toBe('?ns=data'));
  });

  it('tells a narrowed person the node totals are partial, and places no node in a state', async () => {
    const partial = 'You may list pods in some namespaces only, so every total here counts only the pods in those.';
    const unknownStates = [
      { key: 'danger' as const, label: 'Near capacity', count: null },
      { key: 'warning' as const, label: 'Busy', count: null },
      { key: 'healthy' as const, label: 'Balanced', count: null },
      { key: 'overkill' as const, label: 'Barely used', count: null },
      { key: 'none' as const, label: 'Not measured', count: null },
    ];
    stub({
      [ROUTE]: page({
        namespaceCards: null,
        totals: null,
        partial,
        nodeCards: [nodeCard({ state: 'unknown' })],
        nodeStates: unknownStates,
      }),
    });
    mount(`${PAGE_PATH}?view=nodes`);
    // Said once, outside the collapsed sources, before any tile.
    const said = await screen.findByText(partial, { selector: '[data-partial]', exact: false });
    expect(said.closest('details')).toBeNull();
    expect(screen.getAllByText(partial, { exact: false })).toHaveLength(1);
    const tiles = document.querySelector('[data-tiles]') as HTMLElement;
    expect(said.compareDocumentPosition(tiles) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    // No tile can be picked, none reads as a count, and each says it was
    // withheld - not that nobody could read the nodes.
    for (const tile of Array.from(document.querySelectorAll('[data-tile]'))) {
      expect(tile.getAttribute('data-count')).toBe('unknown');
      expect((tile as HTMLButtonElement).disabled).toBe(true);
      expect(tile.getAttribute('title')).toMatch(/^Withheld: you see only some of the pods/);
    }
  });

  it('says nodes nobody could list apart from a cluster with none', async () => {
    stub({ [ROUTE]: page({ namespaceCards: null, totals: null, nodeCards: null, nodeStates: [] }) });
    mount(`${PAGE_PATH}?view=nodes`);
    expect(await screen.findByText(/No node capacity could be read/)).toBeTruthy();
    expect(screen.queryByText(/answered with no node/)).toBeNull();
  });

  it('switches views through the address from the one answer, reading nothing again', async () => {
    const { fetch } = stub({ [ROUTE]: page({ nodeCards: [nodeCard()], nodeStates: [] }) });
    const router = mount(PAGE_PATH);
    const root = () => document.getElementById('kubeadjust');
    await waitFor(() => expect(document.querySelector('[data-namespace-card="shop"]')).not.toBeNull());
    expect(root()?.getAttribute('data-view')).toBe('overview');
    fireEvent.click(screen.getByRole('link', { name: 'Nodes' }));
    await waitFor(() => expect(router.state.location.search).toBe('?view=nodes'));
    expect(await screen.findByRole('heading', { name: 'worker-1' })).toBeTruthy();
    expect(root()?.getAttribute('data-view')).toBe('nodes');
    fireEvent.click(screen.getByRole('link', { name: 'Namespaces' }));
    await waitFor(() => expect(router.state.location.search).toBe(''));
    await waitFor(() => expect(document.querySelector('[data-namespace-card="shop"]')).not.toBeNull());
    // Two tabs, one read: both views are in the cluster's answer.
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch.mock.calls.map(([url]) => String(url))).toEqual([ROUTE]);
  });

  it('asks the route for the scope alone, whatever view and filter the address holds', async () => {
    const { fetch } = stub({ [`${ROUTE}?ns=shop`]: page({ namespace: 'shop', namespaceCards: null, totals: null, cards: [workload()] }) });
    mount(`${PAGE_PATH}?ns=shop&view=nodes&cat=memory`);
    await screen.findByRole('heading', { name: 'api' });
    expect(document.getElementById('kubeadjust')?.getAttribute('data-view')).toBe('namespace');
    expect(fetch.mock.calls.map(([url]) => String(url))).toEqual([`${ROUTE}?ns=shop`]);
  });

  it('aborts the read in flight when another namespace is asked for', async () => {
    const signals = new Map<string, AbortSignal>();
    const answer = page({ namespace: 'shop', namespaceCards: null, totals: null, cards: [workload()] });
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
        if (init?.signal) signals.set(url, init.signal);
        // The cluster's answer never comes: it is still being read when the
        // operator asks for one namespace.
        if (url === ROUTE) return new Promise<Response>(() => {});
        return Promise.resolve(new Response(JSON.stringify(answer), { status: 200, headers: { 'Content-Type': 'application/json' } }));
      }),
    );
    const router = mount(PAGE_PATH);
    await waitFor(() => expect(signals.get(ROUTE)).toBeDefined());
    await router.navigate('/c/docker-desktop/x/kubeadjust?ns=shop');
    expect(await screen.findByRole('heading', { name: 'api' })).toBeTruthy();
    expect(signals.get(ROUTE)?.aborted).toBe(true);
  });

  it('offers nothing that writes: no form, no apply, only reads and a copy', async () => {
    stub({
      [`${ROUTE}?ns=shop`]: page({
        namespace: 'shop',
        namespaceCards: null,
        totals: null,
        cards: [workload()],
        groups: [{ kind: 'danger', label: 'critical', mark: '▲', rows: [finding] }],
        commands: [finding.kubectl],
      }),
    });
    mount(`${PAGE_PATH}?ns=shop`);
    await screen.findByRole('heading', { name: 'api' });
    expect(document.querySelector('form')).toBeNull();
    const names = screen.getAllByRole('button').map((b) => b.textContent ?? '');
    for (const name of names) {
      expect(name).not.toMatch(/apply|delete|patch|scale|restart|set resources/i);
    }
  });

  it('says the console turned the page off, rather than failing to read', async () => {
    // An empty body has no sentence of the gate's to show, so the page says its own.
    stub({ [ROUTE]: { status: 404, body: '', contentType: 'text/plain' } });
    mount(PAGE_PATH);
    expect(await screen.findByText(/KubeAdjust is not enabled for this cluster/)).toBeTruthy();
  });

  it("shows the gate's own sentence, not the Settings switch, when the gate said why", async () => {
    stub({
      [ROUTE]: {
        status: 404,
        contentType: 'text/plain; charset=utf-8',
        body:
          'KubeAdjust is not enabled on this KubeTower: it is turned off by KT_EXTRAS=-kubeadjust ' +
          'in the environment, which the console cannot change while it runs. ' +
          'Change KT_EXTRAS where the deployment sets it and restart KubeTower.\n',
      },
    });
    mount(PAGE_PATH);
    expect(await screen.findByText(/which the console cannot change while it runs/)).toBeTruthy();
    expect(screen.queryByText(/Turn it on in Settings/)).toBeNull();
  });

  it('says above everything that recommendations are not reliable without Prometheus, with a command to copy', async () => {
    const written: string[] = [];
    vi.stubGlobal('isSecureContext', true);
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: (t: string) => (written.push(t), Promise.resolve()) },
    });
    try {
      stub({ [ROUTE]: page({ window: 'no-prometheus' }) });
      mount(PAGE_PATH);
      const banner = await screen.findByRole('region', { name: 'Prometheus' });
      expect(banner.textContent).toContain('Recommendations are not reliable without Prometheus');
      // Above the headline: met before any figure.
      const headline = screen.getByRole('region', { name: 'In this cluster' });
      expect(banner.compareDocumentPosition(headline) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
      const link = within(banner).getByRole('link', { name: 'How to install it' });
      expect(link.getAttribute('href')).toBe('https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack');
      expect(link.getAttribute('target')).toBe('_blank');
      const command = banner.querySelector('[data-install]')?.textContent ?? '';
      expect(command).toContain('helm repo add prometheus-community https://prometheus-community.github.io/helm-charts');
      expect(command).toContain('helm install prometheus prometheus-community/kube-prometheus-stack -n monitoring --create-namespace');
      fireEvent.click(within(banner).getByRole('button', { name: 'Copy' }));
      await waitFor(() => expect(written).toEqual([command]));
    } finally {
      Reflect.deleteProperty(navigator, 'clipboard');
    }
  });

  it('says nothing about Prometheus when a history was read, nor on a page nobody measured', async () => {
    const withHistory = NOTES.map((n) => (n.label === 'History' ? { ok: true, label: 'History', detail: 'Prometheus, 7d at 15m' } : n));
    stub({
      [ROUTE]: page({ notes: withHistory, window: 'read' }),
      [`${ROUTE}?ns=shop`]: page({ namespace: 'shop', ready: false, headline: null, window: '' }),
    });
    const router = mount(PAGE_PATH);
    await screen.findByRole('region', { name: 'In this cluster' });
    expect(document.querySelector('[data-prometheus-banner]')).toBeNull();
    await router.navigate('/c/docker-desktop/x/kubeadjust?ns=shop');
    await screen.findByText(/Nothing could be measured here/);
    expect(document.querySelector('[data-prometheus-banner]')).toBeNull();
  });

  it('says nothing about installing Prometheus when one is there and its window failed', async () => {
    // The History note fails, and explains why; installing a Prometheus would not fix it.
    const failed = NOTES.map((n) => (n.label === 'History' ? { ok: false, label: 'History', detail: 'connection refused — so every suggestion below rests on a single sample per replica' } : n));
    stub({ [ROUTE]: page({ notes: failed, window: 'unreadable' }) });
    mount(PAGE_PATH);
    await screen.findByRole('region', { name: 'In this cluster' });
    expect(document.querySelector('[data-prometheus-banner]')).toBeNull();
    expect(document.querySelector('[data-note="History"]')).not.toBeNull();
  });

  it('narrows to one node from the address: its workloads, their fixes under How to fix, and a way back', async () => {
    const other = { ...finding, workload: 'web', kind: 'overkill' as const, action: 'Reduce request', resource: 'CPU', warnings: [], kubectl: 'kubectl set resources deployment/web -c app --requests=cpu=100m -n shop' };
    const tab: NodeTab = {
      cluster: 'docker-desktop',
      node: 'worker-1',
      ready: true,
      measured: true,
      notes: NOTES,
      partial: '',
      resources: [],
      pods: 4,
      maxPods: 110,
      about: { kubelet: 'v1.31.4', os: 'Ubuntu', runtime: 'containerd://1.7.24', age: '3d', kernel: '', architecture: '', roles: [], taints: [] },
      findings: [],
      suggestions: [finding, { ...finding, action: 'Set limit', resource: 'CPU', kubectl: '' }, other],
      commands: [finding.kubectl, other.kubectl],
      workloads: 3,
      flagged: 2,
      topWorkloads: [],
      podList: [],
    };
    stub({ [ROUTE]: page({ top: [finding] }), '/x/docker-desktop/kubeadjust/node/worker-1': tab });
    const router = mount(`${PAGE_PATH}?node=worker-1`);
    const list = await screen.findByRole('region', { name: 'Workloads on worker-1' });
    expect(list.querySelector('h2')?.textContent).toBe('2 workloads with a pod on worker-1 to change');
    expect(Array.from(list.querySelectorAll('[data-workload]')).map((e) => e.getAttribute('data-workload'))).toEqual(['api', 'web']);
    // The cluster's own list is not drawn under the filter.
    expect(screen.queryByRole('region', { name: 'Act on first' })).toBeNull();
    // Each workload's commands are under its How to fix, and only there.
    const api = list.querySelector('[data-workload="api"]') as HTMLElement;
    expect(api.querySelectorAll('[data-finding]')).toHaveLength(2);
    const fix = api.querySelector('[data-how-to-fix]') as HTMLDetailsElement;
    expect(fix.open).toBe(false);
    expect(within(fix).getByText('How to fix')).toBeTruthy();
    expect(fix.querySelector('pre')?.textContent).toBe(finding.kubectl);
    expect(Array.from(list.querySelectorAll('code, pre')).filter((e) => e.textContent?.includes('kubectl'))).toHaveLength(2);
    // Visible, and clearable.
    const chip = document.querySelector('[data-node-chip="worker-1"]') as HTMLElement;
    expect(chip.textContent).toContain('Node worker-1');
    fireEvent.click(within(chip).getByRole('button', { name: 'Clear the node filter worker-1' }));
    await waitFor(() => expect(router.state.location.search).toBe(''));
    expect(await screen.findByRole('region', { name: 'Act on first' })).toBeTruthy();
    expect(document.querySelector('[data-node-chip]')).toBeNull();
  });
});
