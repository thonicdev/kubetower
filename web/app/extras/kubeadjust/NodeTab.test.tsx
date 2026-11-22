import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { Suspense, type ReactNode } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { nodeTabQuery, pageQuery, STALE_MS, type NodeResource, type NodeTab as NodeTabData } from './data';
import { kubeadjust } from './index';
import NodeTab from './NodeTab';

afterEach(() => {
  vi.unstubAllGlobals();
});

/**
 * KubeAdjust in a Node's panel, against the body
 * `nodetab.go` writes.
 */

const ROUTE = '/x/docker-desktop/kubeadjust/node/worker-1';

function stub(body: unknown, status = 200): void {
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
      if (url !== ROUTE) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
      return Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }));
    }),
  );
}

function mountWithRouter(element: ReactNode): ReturnType<typeof createMemoryRouter> {
  const router = createMemoryRouter([{ path: '/c/:cluster/*', element: <Suspense>{element}</Suspense> }], {
    initialEntries: ['/c/docker-desktop/nodes'],
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

function mount(element: ReactNode): void {
  mountWithRouter(element);
}

function resource(over: Partial<NodeResource>): NodeResource {
  return {
    key: 'cpu',
    label: 'CPU',
    allocatable: '8.00 cores',
    requested: '750m',
    limited: '12.00 cores',
    used: '540m',
    requestedPct: 9,
    limitedPct: 150,
    usedPct: 7,
    requestBar: 9.4,
    limitBar: 100,
    useBar: 6.8,
    measured: true,
    coverage: '',
    status: 'overkill',
    noAllocatable: false,
    gap: '',
    ...over,
  };
}

function tab(over: Partial<NodeTabData> = {}): NodeTabData {
  return {
    cluster: 'docker-desktop',
    node: 'worker-1',
    ready: true,
    measured: true,
    partial: '',
    notes: [
      { ok: true, label: 'Usage', detail: 'metrics-server, sampled now' },
      { ok: true, label: 'Node capacity', detail: '1 node(s)' },
      { ok: false, label: 'Disk', detail: 'no kubelet could be asked for disk usage' },
      { ok: true, label: 'History', detail: 'Prometheus, 6h at 2m' },
    ],
    resources: [
      resource({}),
      resource({ key: 'memory', label: 'Memory', allocatable: '16.00 GiB', requested: '1.50 GiB', limited: '3.00 GiB', used: '2.78 GiB', limitedPct: 19, limitBar: 18.8 }),
      resource({ key: 'ephemeral-storage', label: 'Ephemeral storage', allocatable: '', requested: '0 KiB', limited: '0 KiB', used: '', usedPct: null, measured: false, noAllocatable: true }),
    ],
    pods: 3,
    maxPods: 110,
    about: {
      kubelet: 'v1.31.4',
      os: 'Ubuntu 24.04.1 LTS',
      runtime: 'containerd://1.7.24',
      age: '3d',
      kernel: '6.8.0-45-generic',
      architecture: 'amd64',
      roles: ['worker'],
      taints: [{ text: 'gpu=true', effect: 'NoSchedule', title: 'gpu=true:NoSchedule' }],
    },
    findings: [{ kind: 'overkill', title: 'Reserved and not used', detail: '650m of CPU is held by requests and not used.' }],
    suggestions: [
      {
        workload: 'api',
        workloadKind: 'Deployment',
        namespace: 'shop',
        pod: 'api-1',
        podCount: 3,
        container: 'app',
        resource: 'Memory',
        kind: 'danger',
        action: 'Increase limit',
        message: 'Container was OOMKilled — memory limit is too low',
        current: '1.00 GiB',
        suggested: '1.50 GiB',
        appliesToBoth: false,
        warnings: [],
        kubectl: 'kubectl set resources deployment/api -c app --limits=memory=1536Mi -n shop',
        scope: 'Deployment api · shop',
        confidence: 'low',
        insufficientData: false,
      },
    ],
    commands: ['kubectl set resources deployment/api -c app --limits=memory=1536Mi -n shop'],
    workloads: 7,
    flagged: 7,
    topWorkloads: ['etl', 'search', 'web', 'cart', 'reports'].map((name, i) => ({
      workload: name,
      kind: 'Deployment',
      namespace: 'shop',
      worst: i === 0 ? ('danger' as const) : ('overkill' as const),
      findings: 2,
      gap: `CPU ${5 - i}.00 cores idle`,
    })),
    podList: [
      { name: 'api-1', namespace: 'shop', workload: 'Deployment api', cpuRequest: '250m', cpuLimit: '500m', cpuUsed: '150m', memRequest: '512 MiB', memLimit: '1.00 GiB', memUsed: '900 MiB', share: 3, measured: true, findings: 1, worst: 'danger' },
      { name: 'db-0', namespace: 'data', workload: '', cpuRequest: '100m', cpuLimit: 'none', cpuUsed: '', memRequest: '128 MiB', memLimit: 'none', memUsed: '', share: 1, measured: false, findings: 0, worst: '' },
    ],
    ...over,
  };
}

describe('the KubeAdjust node tab', () => {
  it('stays fresh as long as the console holds the figures, so a focus does not read every kubelet again', () => {
    // usageTTL in internal/web/usagecache.go.
    expect(STALE_MS).toBe(10_000);
    expect(nodeTabQuery('docker-desktop', 'worker-1').staleTime).toBe(STALE_MS);
    expect(pageQuery('docker-desktop', '').staleTime).toBe(STALE_MS);
  });

  it('is offered on Nodes, and only there', () => {
    const tabs = (kubeadjust.tabs ?? []).filter((t) => t.kinds.includes('nodes'));
    expect(tabs).toHaveLength(1);
    expect(tabs[0]?.kinds).toEqual(['nodes']);
    expect(tabs[0]?.label).toBe('KubeAdjust');
  });

  it('draws the three resources against allocatable, with limits past it in red', async () => {
    stub(tab());
    mount(<NodeTab cluster="docker-desktop" kind="nodes" namespace={undefined} name="worker-1" />);
    const cpu = (await screen.findByText('CPU')).closest('[data-node-resource]') as HTMLElement;
    expect(cpu.textContent).toContain('requested 750m');
    expect(cpu.textContent).toContain('used 540m');
    expect((cpu.querySelector('[data-fill="used"]') as HTMLElement).style.width).toBe('6.8%');
    expect((cpu.querySelector('[data-mark="limits"]') as HTMLElement).className).toContain('bg-bad');
    // The figure beside the track, not inside it.
    expect(cpu.querySelector('[data-figure]')?.textContent).toBe('7% used');
    expect(cpu.querySelector('[data-fill="used"]')?.parentElement?.contains(cpu.querySelector('[data-figure]'))).toBe(false);
    // Ephemeral storage: no allocatable declared and nothing measured, both said.
    const eph = document.querySelector('[data-node-resource="ephemeral-storage"]') as HTMLElement;
    expect(eph.textContent).toContain('usage not measured');
    expect(eph.textContent).toContain('declares no allocatable');
    expect(eph.querySelector('[data-fill]')).toBeNull();
  });

  it('holds no command, anywhere in the panel', async () => {
    stub(tab());
    mount(<NodeTab cluster="docker-desktop" kind="nodes" namespace={undefined} name="worker-1" />);
    await screen.findByText('About this node');
    const panel = document.querySelector('[data-kubeadjust-tab="node"]') as HTMLElement;
    expect(panel.textContent).not.toContain('kubectl');
    expect(panel.querySelector('[data-commands], pre, code')).toBeNull();
    expect(screen.queryByRole('button', { name: /Copy/ })).toBeNull();
    // Not even the suggestions' own text: the page has them.
    expect(screen.queryByText('Increase limit')).toBeNull();
  });

  it('names at most five workloads, the largest gaps first, and leads to all of them on the page', async () => {
    const many = tab().topWorkloads;
    stub(tab({ topWorkloads: [...many, { ...many[4]!, workload: 'sixth' }] }));
    const router = mountWithRouter(<NodeTab cluster="docker-desktop" kind="nodes" namespace={undefined} name="worker-1" />);
    const section = await screen.findByRole('region', { name: 'Workloads on this node' });
    const rows = Array.from(section.querySelectorAll('[data-node-workload]'));
    expect(rows.map((r) => r.getAttribute('data-node-workload'))).toEqual(['etl', 'search', 'web', 'cart', 'reports']);
    expect(rows[0]?.textContent).toContain('CPU 5.00 cores idle');
    const link = within(section).getByRole('link', { name: 'See the 7 workloads in KubeAdjust →' });
    expect(link.getAttribute('href')).toBe('/c/docker-desktop/x/kubeadjust?node=worker-1');
    fireEvent.click(link);
    await waitFor(() => expect(router.state.location.pathname).toBe('/c/docker-desktop/x/kubeadjust'));
    expect(router.state.location.search).toBe('?node=worker-1');
  });

  it('says what the machine is in five facts, and folds the rest', async () => {
    stub(tab());
    mount(<NodeTab cluster="docker-desktop" kind="nodes" namespace={undefined} name="worker-1" />);
    const about = await screen.findByRole('region', { name: 'About this node' });
    const facts = Array.from(about.querySelectorAll('[data-node-facts] [data-fact]')).map((f) => f.textContent);
    expect(facts).toEqual(['Versionkubelet v1.31.4', 'OSUbuntu 24.04.1 LTS', 'Container runtimecontainerd://1.7.24', 'Age3d', 'Pods3 / 110']);
    // Everything else is behind one closed disclosure.
    const more = about.querySelector('[data-node-more]') as HTMLDetailsElement;
    expect(more.open).toBe(false);
    for (const text of ['6.8.0-45-generic', 'amd64', 'worker', 'gpu=true · NoSchedule', 'Reserved and not used']) {
      expect(within(more).getByText(text)).toBeTruthy();
      expect(within(about.querySelector('[data-node-facts]') as HTMLElement).queryByText(text)).toBeNull();
    }
    expect(within(more).getByText(/More about this node · 1 finding/)).toBeTruthy();
  });

  it('says a fact the node did not report, and folds the pods', async () => {
    stub(tab({ about: { ...tab().about, runtime: '' } }));
    mount(<NodeTab cluster="docker-desktop" kind="nodes" namespace={undefined} name="worker-1" />);
    const runtime = (await screen.findByText('Container runtime')).closest('[data-fact]') as HTMLElement;
    expect(runtime.textContent).toBe('Container runtimenot reported');
    const pods = document.querySelector('[data-node-pods]') as HTMLDetailsElement;
    expect(pods.open).toBe(false);
    const unmeasured = document.querySelector('[data-node-pod="db-0"]') as HTMLElement;
    expect(within(unmeasured).getAllByText('not measured')).toHaveLength(2);
    // The failed note is spelled out; the others are one click away.
    expect(Array.from(document.querySelectorAll('[data-note-failed]')).map((e) => e.getAttribute('data-note-failed'))).toEqual(['Disk']);
    expect(document.querySelector('[data-partial]')).toBeNull();
  });

  it('says the totals are partial for a narrowed person, above the bars, with the sources a click away', async () => {
    stub(
      tab({
        partial: 'You may list pods in some namespaces only, so these totals count only the pods in those.',
        notes: [
          { ok: true, label: 'Usage', detail: 'metrics-server, sampled now' },
          { ok: true, label: 'Node capacity', detail: '1 node(s) · node totals count only the pods in the namespaces you may list' },
          { ok: true, label: 'Disk', detail: 'the kubelet summary API on 1 node(s)' },
          { ok: true, label: 'History', detail: 'Prometheus, 6h at 2m' },
        ],
        findings: [{ kind: 'warning', title: 'Totals over some of its pods', detail: 'Only the pods in the namespaces you may list are counted here.' }],
      }),
    );
    mount(<NodeTab cluster="docker-desktop" kind="nodes" namespace={undefined} name="worker-1" />);
    const partial = await screen.findByText(/these totals count only the pods in those/);
    // Above the bars: a reader meets it before any figure.
    const bars = document.querySelector('[data-node-resource="cpu"]') as HTMLElement;
    expect(partial.compareDocumentPosition(bars) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.getByText(/More about this node · 1 finding/)).toBeTruthy();
    expect(screen.getByText('Totals over some of its pods')).toBeTruthy();
    // Every note is behind the sources line; a click opens it.
    const details = document.querySelector('[data-sources] details') as HTMLDetailsElement;
    expect(details.open).toBe(false);
    fireEvent.click(within(details).getByText('Built from'));
    expect(details.open).toBe(true);
    expect(within(details).getByText(/node totals count only the pods/)).toBeTruthy();
  });

  it('leads to every node in KubeAdjust', async () => {
    stub(tab());
    const router = mountWithRouter(<NodeTab cluster="docker-desktop" kind="nodes" namespace={undefined} name="worker-1" />);
    fireEvent.click(await screen.findByRole('link', { name: 'Every node in KubeAdjust' }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/c/docker-desktop/x/kubeadjust'));
    expect(router.state.location.search).toBe('?view=nodes');
  });

  it('says a node nobody could measure is unjudged, never that it has nothing to change', async () => {
    stub(
      tab({
        measured: false,
        workloads: null,
        suggestions: [],
        commands: [],
        flagged: 0,
        topWorkloads: [],
        resources: [resource({ used: '', usedPct: null, measured: false })],
      }),
    );
    mount(<NodeTab cluster="docker-desktop" kind="nodes" namespace={undefined} name="worker-1" />);
    expect(await screen.findByText(/no workload here could be judged/)).toBeTruthy();
    expect(screen.queryByText(/Nothing to change/)).toBeNull();
    expect(document.querySelector('[data-see-all]')).toBeNull();
    expect(document.querySelector('[data-fill="used"]')).toBeNull();
  });

  it('says a refusal from the route, not an empty tab', async () => {
    stub({ code: 'unknown_node', message: 'No node "worker-1" in this cluster.' }, 404);
    mount(<NodeTab cluster="docker-desktop" kind="nodes" namespace={undefined} name="worker-1" />);
    expect(await screen.findByText(/No node "worker-1" in this cluster/)).toBeTruthy();
  });

  it('says how old the history it rests on is, in words', async () => {
    stub(tab({ windowReadAt: new Date(Date.now() - 3 * 3_600_000).toISOString() }));
    mount(<NodeTab cluster="docker-desktop" kind="nodes" namespace={undefined} name="worker-1" />);
    expect(await screen.findByText('History read 3 hours ago')).toBeTruthy();
  });
});
