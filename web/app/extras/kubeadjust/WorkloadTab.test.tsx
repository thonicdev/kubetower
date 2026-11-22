import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, within } from '@testing-library/react';
import { Suspense, type ReactNode } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { STALE_MS, workloadTabQuery, type Row, type WorkloadCard, type WorkloadTab as WorkloadTabData } from './data';
import { kubeadjust } from './index';
import WorkloadTab from './WorkloadTab';

afterEach(() => {
  vi.unstubAllGlobals();
});

/**
 * KubeAdjust in a workload's panel, against the body `workload.go` writes.
 */

const ROUTE = '/x/docker-desktop/kubeadjust/workload/deployments/shop/api';

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

function mount(element: ReactNode): void {
  const router = createMemoryRouter([{ path: '/c/:cluster/*', element: <Suspense>{element}</Suspense> }], {
    initialEntries: ['/c/docker-desktop/deployments'],
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

function openTab(): void {
  mount(<WorkloadTab cluster="docker-desktop" kind="deployments" namespace="shop" name="api" />);
}

const COMMAND = 'kubectl set resources deployment/api -c app --requests=memory=1Gi --limits=memory=1536Mi -n shop';

function row(over: Partial<Row> = {}): Row {
  return {
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
    kubectl: COMMAND,
    scope: 'Deployment api · shop',
    confidence: 'high',
    insufficientData: false,
    ...over,
  };
}

function card(over: Partial<WorkloadCard> = {}): WorkloadCard {
  const bar = (label: string) => ({
    label,
    measured: true,
    status: 'danger' as const,
    mean: '950 MiB',
    peak: '1000 MiB',
    request: '512 MiB',
    limit: '1.00 GiB',
    usePct: 92.8,
    reqPct: 50,
    peakPct: 97.7,
    pct: 93,
    scale: 'limit',
    headroom: '24 MiB',
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
        oomKilled: false,
        oomKilledEarlier: true,
        memoryRaised: { from: '512 MiB', to: '1.00 GiB', revision: 4 },
        cpu: bar('CPU'),
        memory: bar('Memory'),
        ephemeral: { ...bar('Ephemeral'), measured: false },
        pods: [],
      },
    ],
    findings: [row(), row({ resource: 'CPU', kind: 'overkill', action: 'Reduce request', confidence: 'medium', kubectl: '', current: '250m', suggested: '100m' })],
    volumes: [],
    storageOK: true,
    ...over,
  };
}

function tab(over: Partial<WorkloadTabData> = {}): WorkloadTabData {
  return {
    cluster: 'docker-desktop',
    namespace: 'shop',
    kind: 'Deployment',
    name: 'api',
    ready: true,
    window: 'read',
    partial: '',
    notes: [
      { ok: true, label: 'Usage', detail: 'metrics-server, sampled now' },
      { ok: true, label: 'Node capacity', detail: '1 node(s)' },
      { ok: true, label: 'Disk', detail: '1 kubelet' },
      { ok: true, label: 'History', detail: 'Prometheus, 7d' },
    ],
    card: card(),
    commands: [COMMAND],
    ...over,
  };
}

describe('the KubeAdjust workload tab', () => {
  it('is offered on Deployments, StatefulSets, DaemonSets and CronJobs, and no other workload', () => {
    const tabs = (kubeadjust.tabs ?? []).filter((t) => t.key === 'workload');
    expect(tabs).toHaveLength(1);
    expect(tabs[0]?.kinds).toEqual(['deployments', 'statefulsets', 'daemonsets', 'cronjobs']);
    expect(tabs[0]?.label).toBe('KubeAdjust');
    for (const other of ['pods', 'replicasets', 'jobs', 'services']) {
      expect((kubeadjust.tabs ?? []).filter((t) => t.kinds.includes(other))).toEqual([]);
    }
  });

  it('asks the one-workload route, fresh as long as the page', () => {
    expect(workloadTabQuery('docker-desktop', 'deployments', 'shop', 'api').staleTime).toBe(STALE_MS);
  });

  it("draws the page's card: the bars and the kills it remembers", async () => {
    stub(tab());
    openTab();
    const section = (await screen.findByRole('heading', { name: 'api' })).closest('section') as HTMLElement;
    expect(within(section).getByText('OOMKilled earlier')).toBeTruthy();
    expect(within(section).getByText('limit raised from 512 MiB')).toBeTruthy();
    expect(section.querySelector('[data-container="app"]')).not.toBeNull();
  });

  it('lists each recommendation with how sure it is, and the commands once, to copy', async () => {
    stub(tab());
    openTab();
    const list = await screen.findByRole('region', { name: 'Recommendations' });
    expect(list.querySelectorAll('[data-finding]')).toHaveLength(2);
    expect(list.querySelector('[data-confidence="high"]')).not.toBeNull();
    expect(list.querySelector('[data-confidence="medium"]')).not.toBeNull();
    expect(within(list).queryByText(COMMAND)).toBeNull();
    const block = screen.getByRole('region', { name: 'How to apply' });
    expect(within(block).getByText(COMMAND)).toBeTruthy();
    expect(within(block).getByRole('button', { name: /Copy/ })).toBeTruthy();
  });

  it("leads to the workload's namespace on the KubeAdjust page", async () => {
    stub(tab());
    openTab();
    const link = await screen.findByRole('link', { name: /Namespace shop in KubeAdjust/ });
    expect(link.getAttribute('href')).toBe('/c/docker-desktop/x/kubeadjust?ns=shop');
  });

  it('says a workload nobody could measure is unsized, never that it has nothing to change', async () => {
    stub(
      tab({
        ready: false,
        card: null,
        commands: [],
        notes: [{ ok: false, label: 'Usage', detail: 'the cluster has no metrics-server' }, ...tab().notes.slice(1)],
      }),
    );
    openTab();
    expect(await screen.findByText(/could not be sized/)).toBeTruthy();
    expect(screen.queryByText(/Nothing to change/)).toBeNull();
    expect(screen.getAllByText('the cluster has no metrics-server').length).toBeGreaterThan(0);
  });

  it('says a workload with no pod reporting usage has nothing to size yet', async () => {
    stub(tab({ card: null, commands: [] }));
    openTab();
    expect(await screen.findByText(/nothing to size yet/)).toBeTruthy();
    expect(screen.queryByRole('region', { name: 'How to apply' })).toBeNull();
  });

  it('says a workload sized right has nothing to change, with no command block', async () => {
    stub(tab({ card: card({ worst: '', findings: [] }), commands: [] }));
    openTab();
    expect(await screen.findByText(/Nothing to change/)).toBeTruthy();
    expect(screen.queryByRole('region', { name: 'How to apply' })).toBeNull();
  });

  it("says a CronJob's commands change its next runs, and that it is measured only while a Job runs", async () => {
    stub(tab({ kind: 'CronJob', card: null, commands: [] }));
    openTab();
    expect(await screen.findByText(/measured only while one of its Jobs runs/)).toBeTruthy();
  });

  it("says a CronJob's commands apply from its next run, not that they restart pods", async () => {
    stub(tab({ kind: 'CronJob', card: card({ kind: 'CronJob' }) }));
    openTab();
    const block = await screen.findByRole('region', { name: 'How to apply' });
    expect(within(block).queryByText(/from the next one on/)).toBeNull();
    const note = block.querySelector('[data-restarts]');
    expect(note?.textContent?.trim()).toBe('Next runs');
    expect(note?.getAttribute('title')).toMatch(/from the next one on/);
    expect(note?.getAttribute('title')).not.toMatch(/restarts every pod/);
  });

  it('says a refusal from the route, not an empty tab', async () => {
    stub({ code: 'unknown_workload', message: 'No Deployment "api" in namespace "shop".' }, 404);
    openTab();
    expect(await screen.findByText(/No Deployment "api" in namespace "shop"/)).toBeTruthy();
  });

  it('says how old a kept history is, in words, and nothing for one just read', async () => {
    stub(tab({ windowReadAt: new Date(Date.now() - 12 * 60_000).toISOString() }));
    openTab();
    expect(await screen.findByText('History read 12 minutes ago')).toBeTruthy();
  });

  it('says nothing of the age of a history read under a minute ago', async () => {
    stub(tab({ windowReadAt: new Date(Date.now() - 20_000).toISOString() }));
    openTab();
    await screen.findByRole('heading', { name: 'api' });
    expect(document.querySelector('[data-window-age]')).toBeNull();
  });
});
