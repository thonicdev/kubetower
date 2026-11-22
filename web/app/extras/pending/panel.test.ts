import { afterEach, describe, expect, it, vi } from 'vitest';

import { pending } from './extension';
import { pendingCells, pendingProbe, pendingShortcut, workloadProbe } from './panel';

afterEach(() => {
  vi.unstubAllGlobals();
});

/** What Pending adds to a pod's panel and to the Pods list. */

function stub(routes: Record<string, { status?: number; body: unknown; text?: boolean }>) {
  const fetch = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? `${input.pathname}${input.search}` : input.url;
    const r = routes[url];
    if (r === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
    if (r.text === true) return Promise.resolve(new Response(String(r.body), { status: r.status ?? 200, headers: { 'Content-Type': 'text/plain' } }));
    return Promise.resolve(new Response(JSON.stringify(r.body), { status: r.status ?? 200, headers: { 'Content-Type': 'application/json' } }));
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

const BRIEF = '/x/prod/pending/brief/shop/api-1';
const POD = '/x/prod/pending/pod/shop/api-1';
const object = { cluster: 'prod', kind: 'pods', namespace: 'shop', name: 'api-1' };
const row = {
  namespace: 'shop', name: 'api-1', created: '2026-11-11T14:00:00Z', stage: 'unschedulable', main: 'resources', categories: ['resources'],
  message: '0/3 nodes are available: 3 Insufficient cpu.', verdict: { total: 3, reasons: [{ text: 'Insufficient cpu', nodes: 3, category: 'resources' }] },
  request: { cpu: 2000 },
};

describe('the panel', () => {
  it('draws the tab on a pending pod only', async () => {
    stub({ [BRIEF]: { body: { pending: true, row } } });
    expect(await pendingProbe(object, new AbortController().signal)).toBe(true);
    stub({ [BRIEF]: { body: { pending: false } } });
    expect(await pendingProbe(object, new AbortController().signal)).toBe(false);
    // Off for this cluster: the console's own 404, not the extra's.
    stub({ [BRIEF]: { status: 404, body: '404 page not found', text: true } });
    expect(await pendingProbe(object, new AbortController().signal)).toBe(false);
    // Could not look: the tab is drawn, and says so.
    stub({ [BRIEF]: { status: 502, body: { code: 'list_failed', message: 'could not list' } } });
    expect(await pendingProbe(object, new AbortController().signal)).toBe(true);
  });

  it('gives the shortcut the reason and the closest node', async () => {
    stub({ [BRIEF]: { body: { pending: true, row } }, [POD]: { body: { pending: true, events: [], fits: [], row: { ...row, closest: { node: 'node-7', failing: 1 } } } } });
    const target = await pendingShortcut(object, new AbortController().signal);
    expect(target).toEqual({ title: 'Resources', badges: ['Pending'], detail: 'closest node-7 · 1 failed', to: '/c/prod/x/pending/shop/api-1' });
  });

  it('keeps the reason when the nodes cannot be read, and draws nothing on a running pod', async () => {
    stub({ [BRIEF]: { body: { pending: true, row } }, [POD]: { status: 403, body: { code: 'list_forbidden', message: 'no' } } });
    expect((await pendingShortcut(object, new AbortController().signal))?.detail).toBe('Insufficient cpu');
    stub({ [BRIEF]: { body: { pending: false } } });
    expect(await pendingShortcut(object, new AbortController().signal)).toBeNull();
  });

  it('asks the column once for the whole list', async () => {
    const fetch = stub({ '/x/prod/pending/cells?kind=pods': { body: { 'shop/api-1': { text: 'Resources', tone: 'bad' } } } });
    const cells = await pendingCells({ cluster: 'prod', kind: 'pods' }, new AbortController().signal);
    expect(cells['shop/api-1']?.text).toBe('Resources');
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it('asks the column once for a workload list too', async () => {
    const fetch = stub({
      '/x/prod/pending/cells?kind=deployments': {
        body: { 'shop/api': { text: '2 pending', tone: 'bad', href: '/c/prod/deployments/shop/api?tab=x.pending.workload' }, 'shop/web': { text: 'Cannot create', tone: 'bad' } },
      },
    });
    const cells = await pendingCells({ cluster: 'prod', kind: 'deployments' }, new AbortController().signal);
    expect(cells['shop/api']?.text).toBe('2 pending');
    expect(cells['shop/web']?.text).toBe('Cannot create');
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it('draws the workload tab while a pod waits or a pod was refused, and not otherwise', async () => {
    const WORKLOAD = '/x/prod/pending/workload/deployments/shop/api';
    const deployment = { cluster: 'prod', kind: 'deployments', namespace: 'shop', name: 'api' };
    const view = { pods: { state: 'read', complete: true }, rows: [], failuresRead: { state: 'read', complete: true } };
    const failure = { namespace: 'shop', kind: 'ReplicaSet', name: 'api-1', category: 'quota', message: 'm', count: 1, last: '2026-11-11T14:00:00Z' };
    stub({ [WORKLOAD]: { body: { ...view, pending: 2, rows: [row] } } });
    expect(await workloadProbe(deployment, new AbortController().signal)).toBe(true);
    stub({ [WORKLOAD]: { body: { ...view, pending: 0, failure } } });
    expect(await workloadProbe(deployment, new AbortController().signal)).toBe(true);
    stub({ [WORKLOAD]: { body: { ...view, pending: 0 } } });
    expect(await workloadProbe(deployment, new AbortController().signal)).toBe(false);
    stub({ [WORKLOAD]: { status: 404, body: '404 page not found', text: true } });
    expect(await workloadProbe(deployment, new AbortController().signal)).toBe(false);
    // Could not look: the tab is drawn, and says so.
    stub({ [WORKLOAD]: { status: 502, body: { code: 'list_failed', message: 'could not list' } } });
    expect(await workloadProbe(deployment, new AbortController().signal)).toBe(true);
  });

  it('is declared where the console draws it', () => {
    expect(pending.tabs?.map((t) => t.kinds)).toEqual([['pods'], ['deployments', 'statefulsets', 'daemonsets', 'jobs']]);
    expect(pending.shortcuts?.map((t) => t.kinds)).toEqual([['pods']]);
    expect(pending.columns?.map((t) => t.kinds)).toEqual([['pods', 'deployments', 'statefulsets', 'daemonsets', 'jobs']]);
  });

  it('has no menu entry, and its tile opens the debug window', () => {
    expect(pending.nav.menu).toBe(false);
    expect(pending.nav.label).toBe('Pod Debugging');
    expect(pending.shortcuts?.[0]?.dialog).toBeDefined();
  });
});
