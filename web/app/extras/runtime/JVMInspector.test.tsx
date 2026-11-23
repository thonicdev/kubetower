import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Container, Inventory, Pod } from './data';
import { POLL_MS, ROW_STEP, groupPods, heapBar, inventoryPath, javaColumn, matches, riskTone, riskWord, worstLevel } from './data';
import { runtimeInspector as jvm } from './index';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  Reflect.deleteProperty(document, 'visibilityState');
});

/**
 * The page, mounted the way the console mounts it, with a fetch stub standing
 * in for `extras/runtimeinspect`. Every body is the shape the Go route writes.
 */

function stub(status: number, body: unknown, contentType = 'application/json'): string[] {
  const asked: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      asked.push(typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url);
      const text = typeof body === 'string' ? body : JSON.stringify(body);
      return Promise.resolve(new Response(text, { status, headers: { 'Content-Type': contentType } }));
    }),
  );
  return asked;
}

function mount(entry = '/c/docker-desktop/x/runtime'): ReturnType<typeof createMemoryRouter> & { unmount: () => void } {
  const router = createMemoryRouter([{ path: 'c/:cluster', children: jvm.routes }], {
    initialEntries: [entry],
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const { unmount } = render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return Object.assign(router, { unmount });
}

function setVisibility(state: 'visible' | 'hidden'): void {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state });
  document.dispatchEvent(new Event('visibilitychange'));
}

/** n Java pods, one container each, over every grade, in ten namespaces. */
function many(n: number): Pod[] {
  const grades = [roomy('app'), app, { ...roomy('app'), risk: { level: 'warning' as const, percent: 80, sentence: 'tight' } }];
  return Array.from({ length: n }, (_, i) => ({
    namespace: `team-${String(i % 10)}`,
    name: `app-${String(i).padStart(5, '0')}`,
    phase: 'Running',
    java: true,
    containers: [grades[i % 3]!],
    owner: { kind: 'StatefulSet', name: `app-${String(i % 40)}` },
  }));
}

const app: Container = {
  name: 'app',
  image: 'eclipse-temurin:21-jre',
  java: true,
  hints: [{ source: 'image', reason: 'the image “eclipse-temurin:21-jre” is a Java runtime image (eclipse-temurin)' }],
  request: { bytes: 1073741824, text: '1Gi' },
  limit: { bytes: 2147483648, text: '2Gi' },
  heap: { bytes: 2147483648, text: '2.0 GiB', from: 'explicit', source: '“-Xmx2g” in the command line' },
  risk: { level: 'error', percent: 100, sentence: 'The heap may grow to 2.0 GiB, at or above the memory limit.' },
  notes: ['JAVA_OPTS carries “-Xmx4g”, which the JVM does not read.'],
  workingSet: { bytes: 734003200, text: '700.0 MiB' },
};

/** A container with room: 512 MiB of heap under a 1 GiB limit. */
const roomy = (name: string): Container => ({
  ...app,
  name,
  request: null,
  limit: { bytes: 1073741824, text: '1Gi' },
  heap: { bytes: 536870912, text: '512.0 MiB', from: 'explicit', source: '“-Xmx512m” in the command line' },
  risk: { level: 'ok', percent: 50, sentence: 'The heap may grow to 512.0 MiB, 50 % of the memory limit of 1.0 GiB.' },
  notes: [],
  workingSet: null,
});

const fleet: Pod[] = [
  { namespace: 'shop', name: 'billing-0', phase: 'Running', java: true, containers: [app], owner: { kind: 'StatefulSet', name: 'billing' } },
  {
    namespace: 'shop',
    name: 'api-7d4b9-aaaaa',
    phase: 'Running',
    java: true,
    containers: [roomy('api')],
    owner: { kind: 'Deployment', name: 'api', replicaSet: 'api-7d4b9' },
  },
  {
    namespace: 'data',
    name: 'zk-0',
    phase: 'Running',
    java: true,
    containers: [{ ...roomy('zk'), limit: null, heap: { bytes: null, text: '', from: 'nolimit', source: 'No memory limit.' }, risk: { level: 'nolimit', percent: null, sentence: 'No memory limit.' } }],
    owner: null,
  },
];

const inventory = (over: Partial<Inventory> = {}): Inventory => ({
  cluster: 'docker-desktop',
  namespace: '',
  complete: true,
  read: 12,
  pods: [{ namespace: 'shop', name: 'billing-0', phase: 'Running', java: true, containers: [app] }],
  ...over,
});

describe('the JVM Inspector page', () => {
  it('lists each Java container with why, its heap, its limit and its risk', async () => {
    const asked = stub(200, inventory());
    mount();
    const row = await screen.findByText('billing-0');
    expect(row.closest('a')?.getAttribute('href')).toBe('/c/docker-desktop/pods/shop/billing-0');
    expect(screen.getByText('2.0 GiB')).toBeDefined();
    expect(screen.getByText('2Gi')).toBeDefined();
    const pill = screen.getByText('OOMKill · 100 %');
    expect(pill.getAttribute('data-tone')).toBe('bad');
    expect(screen.getByText('note').getAttribute('title')).toMatch(/does not read/);
    // Why Java is a pill per hint, its reason on hover.
    expect(screen.getByText('image').getAttribute('title')).toMatch(/eclipse-temurin\)/);
    expect(screen.getByText('1 of 12 pods')).toBeDefined();
    expect(asked).toEqual([inventoryPath('docker-desktop', '')]);
  });

    // The tiles count pods by their worst container, and each one filters.
  it('counts the pods by risk in tiles that filter', async () => {
    stub(200, inventory({ pods: fleet }));
    const router = mount();
    const all = await screen.findByRole('button', { name: /Java pods/ });
    expect(all.getAttribute('data-count')).toBe('3');
    const tile = (key: string) => document.querySelector(`[data-jvm-tile="${key}"]`);
    expect(tile('ok')?.getAttribute('data-count')).toBe('1');
    expect(tile('warning')?.getAttribute('data-count')).toBe('0');
    expect(tile('error')?.getAttribute('data-count')).toBe('1');
    expect(tile('nolimit')?.getAttribute('data-count')).toBe('1');
    expect(tile('unknown')).toBeNull();
    fireEvent.click(tile('error') as HTMLElement);
    expect(await screen.findByText('billing-0')).toBeDefined();
    expect(screen.queryByText('api-7d4b9-aaaaa')).toBeNull();
    expect(router.state.location.search).toBe('?risk=error');
    fireEvent.click(tile('error') as HTMLElement);
    expect(await screen.findByText('api-7d4b9-aaaaa')).toBeDefined();
  });

  // The bar: the heap, the limit, the 75 % line, the request tick and the
  // working set where measured; "not measured" where it was not.
  it('draws each container’s memory on one bar', async () => {
    stub(200, inventory({ pods: fleet, usage: { state: 'read', complete: true } }));
    mount();
    const billing = await screen.findByText('billing-0');
    const row = billing.closest('tr') as HTMLElement;
    for (const mark of ['limit', 'threshold', 'request', 'working-set']) {
      expect(row.querySelector(`[data-jvm-mark="${mark}"]`), mark).not.toBeNull();
    }
    expect(row.querySelector('[data-jvm-heap]')?.getAttribute('style')).toContain('width: 100%');
    expect(within(row).getByText(/700\.0 MiB \(34 %\)/)).toBeDefined();
    const api = (screen.getByText('api-7d4b9-aaaaa').closest('tr') as HTMLElement);
    expect(api.querySelector('[data-jvm-mark="working-set"]')).toBeNull();
    expect(api.querySelector('[data-jvm-used]')?.getAttribute('data-jvm-used')).toBe('none');
    expect(within(api).getByText(/not measured/)).toBeDefined();
    // No limit: no limit line, no 75 % line, and the heap is sized on the node.
    const zk = (screen.getByText('zk-0').closest('tr') as HTMLElement);
    expect(zk.querySelector('[data-jvm-mark="limit"]')).toBeNull();
    expect(zk.querySelector('[data-jvm-mark="threshold"]')).toBeNull();
    expect(within(zk).getByText(/sized on the node/)).toBeDefined();
  });

  // No working set at all is said once, with why: never drawn as nothing used.
  it('says why no working set is drawn', async () => {
    stub(200, inventory({ usage: { state: 'absent', detail: 'no metrics.k8s.io', complete: false } }));
    mount();
    expect(await screen.findByText('Working set: no metrics-server')).toBeDefined();
    expect(document.querySelector('[data-jvm-usage="absent"]')).not.toBeNull();
  });

  // Grouped by namespace by default, by workload on a toggle; the worst
  // group first.
  it('groups by namespace or by workload', async () => {
    stub(200, inventory({ pods: fleet }));
    const router = mount();
    await screen.findByText('billing-0');
    const groups = () => [...document.querySelectorAll('[data-jvm-group]')].map((g) => g.getAttribute('data-jvm-group'));
    expect(groups()).toEqual(['shop', 'data']);
    fireEvent.click(screen.getByRole('button', { name: 'Workload' }));
    expect(router.state.location.search).toBe('?group=workload');
    expect(groups()).toEqual(['shop/StatefulSet/billing', 'data/Pod/zk-0', 'shop/Deployment/api']);
    expect(screen.getByText('StatefulSet billing')).toBeDefined();
  });

  // The list column's link lands here, scoped to its namespace and searched.
  it('reads the namespace and the search from the address', async () => {
    const asked = stub(200, inventory({ pods: fleet.slice(0, 2), namespace: 'shop' }));
    mount('/c/docker-desktop/x/runtime?ns=shop&q=api');
    expect(await screen.findByText('api-7d4b9-aaaaa')).toBeDefined();
    expect(screen.queryByText('billing-0')).toBeNull();
    expect(asked).toEqual([inventoryPath('docker-desktop', 'shop')]);
    expect(document.querySelector('[data-jvm-scope="shop"]')).not.toBeNull();
  });

  // The page polls while it is open and visible, and never once it is hidden
  // or closed: no timer left reading behind a closed view.
  it('polls only while open and visible', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const asked = stub(200, inventory());
    const view = mount();
    await screen.findByText('billing-0');
    expect(asked).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(POLL_MS + 10);
    expect(asked).toHaveLength(2);
    setVisibility('hidden');
    await vi.advanceTimersByTimeAsync(POLL_MS * 4);
    expect(asked).toHaveLength(2);
    setVisibility('visible');
    await vi.advanceTimersByTimeAsync(POLL_MS + 10);
    const before = asked.length;
    expect(before).toBeGreaterThan(2);
    view.unmount();
    await vi.advanceTimersByTimeAsync(POLL_MS * 4);
    expect(asked).toHaveLength(before);
  });

  // A thousand Java pods draw a bounded number of rows, quickly, and the
  // page stays usable: the tiles count all of them, a filter narrows them,
  // and Show more adds a step at a time.
  it('draws a thousand Java pods a step at a time', async () => {
    stub(200, inventory({ pods: many(1000), read: 5000 }));
    const started = performance.now();
    mount();
    expect(await screen.findByText('1000 of 5000 pods')).toBeDefined();
    const took = performance.now() - started;
    const rows = () => document.querySelectorAll('[data-jvm-container]').length;
    expect(rows()).toBe(ROW_STEP);
    expect(took).toBeLessThan(5000);
    expect(document.querySelector('[data-jvm-tile="all"]')?.getAttribute('data-count')).toBe('1000');
    expect(screen.getByText(`${String(ROW_STEP)} of 1000 containers shown`)).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: `Show ${String(ROW_STEP)} more` }));
    expect(rows()).toBe(2 * ROW_STEP);
    // The worst grade first: OOMKill-bound pods fill the first step.
    fireEvent.click(document.querySelector('[data-jvm-tile="error"]') as HTMLElement);
    expect(document.querySelector('[data-jvm-tile="error"]')?.getAttribute('data-count')).toBe('333');
    expect(rows()).toBe(2 * ROW_STEP);
    expect([...document.querySelectorAll('[data-jvm-container]')].every((r) => r.textContent?.includes('OOMKill'))).toBe(true);
    // A thousand rows in jsdom take seconds on a loaded runner: the bound this
    // test holds is the rows drawn, not the default five-second timeout.
  }, 30_000);

// An empty answer is a count and the limit of what a spec shows, never a
  // bare "nothing here"; a narrowed one says it is narrowed.
  it('says what an empty or a narrowed answer does not show', async () => {
    stub(200, inventory({ pods: [], complete: false, note: 'You may not list pods in every namespace.' }));
    mount();
    expect(await screen.findByText('0 of 12 pods')).toBeDefined();
    expect(document.querySelector('[data-jvm-count]')?.getAttribute('title')).toMatch(/not recognized/);
    expect(screen.getByText(/You may not list pods in every namespace/)).toBeDefined();
    expect(screen.queryByRole('table')).toBeNull();
  });

  // A refusal is the route's own sentence, drawn as a failure.
  it('draws a refusal as the route says it', async () => {
    stub(403, { code: 'list_forbidden', message: 'The cluster says you may not list pods here.' });
    mount();
    expect(await screen.findByRole('alert')).toBeDefined();
    expect(screen.getByText(/may not list pods here/)).toBeDefined();
  });

  // The console's gate answers plain text: off for this cluster.
  it('says the extra is off where the gate answers', async () => {
    stub(404, 'not found', 'text/plain');
    mount();
    expect(await screen.findByRole('alert')).toBeDefined();
  });
});

describe('the risk pill', () => {
  it('takes the console’s grades and nothing invented', () => {
    expect(riskTone('ok')).toBe('ok');
    expect(riskTone('warning')).toBe('warn');
    expect(riskTone('error')).toBe('bad');
    expect(riskTone('nolimit')).toBeUndefined();
    expect(riskTone('unknown')).toBeUndefined();
    expect(riskWord({ level: 'nolimit', percent: null, sentence: '' })).toBe('No limit');
  });

  it('addresses one namespace with ?ns=', () => {
    expect(inventoryPath('a b', 'shop')).toBe('/x/a%20b/runtime/?ns=shop');
  });
});

describe('the page’s arithmetic', () => {
  it('places every mark on a track as long as the largest figure', () => {
    // 2 GiB heap, 2 GiB limit, 1 GiB request, 700 MiB used.
    const bar = heapBar(app);
    expect(bar).toEqual({ heap: 100, limit: 100, threshold: 75, workingSet: (734003200 / 2147483648) * 100, request: 50 });
    // A heap past its limit pushes the limit in from the end.
    const over = heapBar({ ...app, heap: { ...app.heap!, bytes: 4294967296 } });
    expect(over.heap).toBe(100);
    expect(over.limit).toBe(50);
    expect(over.threshold).toBe(37.5);
    // Nothing known draws nothing.
    expect(heapBar({ ...app, heap: null, limit: null, request: null, workingSet: null })).toEqual({
      heap: null,
      limit: null,
      threshold: null,
      workingSet: null,
      request: null,
    });
  });

  it('grades a pod by its worst container and finds it by any of its names', () => {
    const both: Pod = { ...fleet[1]!, containers: [roomy('a'), app] };
    expect(worstLevel(both)).toBe('error');
    expect(worstLevel({ ...both, containers: [] })).toBe('unknown');
    expect(matches(both, 'TEMURIN')).toBe(true);
    expect(matches(both, 'api')).toBe(true);
    expect(matches(both, 'kafka')).toBe(false);
    expect(groupPods(fleet, 'namespace').map((g) => [g.key, g.worst])).toEqual([
      ['shop', 'error'],
      ['data', 'nolimit'],
    ]);
  });
});

describe('the Java column', () => {
  it('is declared on pods and the workloads that own them, one read per list', async () => {
    const column = jvm.columns?.[0];
    expect(column?.label).toBe('Java');
    expect(column?.kinds).toEqual(['pods', 'deployments', 'statefulsets', 'daemonsets', 'replicasets', 'jobs']);
    const asked = stub(200, { 'shop/billing-0': { text: 'Java', tone: 'bad', hint: 'h', href: '/c/k/x/runtime?ns=shop&q=billing-0' } });
    const cells = await javaColumn({ cluster: 'k', kind: 'deployments' }, new AbortController().signal);
    expect(cells['shop/billing-0']?.tone).toBe('bad');
    expect(asked).toEqual(['/x/k/runtime/column?kind=deployments']);
  });
});
