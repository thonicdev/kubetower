import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { Suspense } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { POLL_MS, pageQuery, type Answer, type Cause, type Change, type ObjectGroup, type Section } from './data';
import { EventsPage } from './EventsPage';
import { PAGE_CAUSES } from './parts';

afterEach(() => {
  vi.unstubAllGlobals();
});

/** Event Intelligence's page against the body `events.go` writes. */

const NOW = Date.parse('2026-11-15T12:00:00Z');
const MIN = 60_000;
const at = (minutesAgo: number): string => new Date(NOW - minutesAgo * MIN).toISOString();
const READ: Section = { state: 'read', complete: true };

function stub(body: unknown, status = 200) {
  const fetch = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
    if (!url.startsWith('/x/prod/events/')) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
    return Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }));
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

function mount(entry = '/c/prod/x/events') {
  const router = createMemoryRouter([{ path: '/c/:cluster/x/events', element: <Suspense><EventsPage /></Suspense> }], { initialEntries: [entry] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

/** The route answers cluster-wide, or for the one namespace it is asked. */
function stubByNamespace(whole: Answer, scoped: Record<string, Answer>) {
  const fetch = vi.fn((input: RequestInfo | URL) => {
    const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, 'http://x');
    const body = scoped[url.searchParams.get('namespace') ?? ''] ?? whole;
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }));
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

const figures = () => ['warnings', 'crashLoop', 'oomKilled', 'failedScheduling', 'objectsTouched'].map((k) => tile(k).getAttribute('data-count'));

function group(name: string, over: Partial<ObjectGroup> = {}): ObjectGroup {
  return { kind: 'Pod', namespace: 'shop', name, count: 5, firstSeen: at(20), lastSeen: at(2), message: 'm', ...over };
}

function cause(reason: string, type: string, groups: ObjectGroup[], over: Partial<Cause> = {}): Cause {
  return {
    reason,
    message: `${reason} message`,
    type,
    count: groups.reduce((n, g) => n + g.count, 0),
    objects: groups.length,
    kinds: ['Pod'],
    firstSeen: at(20),
    lastSeen: at(2),
    examples: [],
    groups,
    ...over,
  };
}

const ROLLOUT: Change = { id: 'rollout/shop/api/7', kind: 'rollout', objectKind: 'Deployment', namespace: 'shop', name: 'api', time: at(15), revision: 7, referenced: true };

function answer(causes: Cause[], over: Partial<Answer> = {}): Answer {
  return {
    namespace: '',
    window: '1h',
    since: at(60),
    generatedAt: new Date(NOW).toISOString(),
    tiles: {
      warnings: { count: 10, state: 'read' },
      crashLoop: { count: 1, state: 'read' },
      oomKilled: { count: 0, state: 'read' },
      failedScheduling: { count: 4, state: 'read' },
      objectsTouched: { count: 3, state: 'read' },
    },
    causes,
    changes: [ROLLOUT],
    pods: [{ namespace: 'shop', name: 'api-1', crashLoop: true, oomKilled: false }],
    sources: { events: READ, replicasets: READ, pods: READ, configmaps: READ, nodes: READ },
    truncated: false,
    complete: true,
    ...over,
  };
}

const backoff = cause('BackOff', 'Warning', [group('api-1', { count: 214, firstSeen: at(12), since: ROLLOUT.id }), group('api-2', { firstSeen: at(11), since: ROLLOUT.id })], { since: ROLLOUT.id });
const sched = cause('FailedScheduling', 'Warning', [group('job-1', { namespace: 'batch' })]);
const node = cause('NodeNotReady', 'Warning', [group('node-2', { kind: 'Node', namespace: '' })], { kinds: ['Node'] });
const pulled = cause('Pulled', 'Normal', [group('api-1', { count: 1 })]);

const tile = (key: string): HTMLElement => {
  const found = document.querySelector<HTMLElement>(`[data-tile="${key}"]`);
  if (found === null) throw new Error(`no tile ${key}`);
  return found;
};
const rows = () => [...document.querySelectorAll('[data-cause]')].map((e) => e.getAttribute('data-cause'));
/** A cause's line in the list, once drawn: a correlation card may carry the same reason. */
const findCause = async (name: RegExp): Promise<HTMLElement> => within(await screen.findByRole('region', { name: 'Causes' })).findByRole('button', { name });

describe('Event Intelligence', () => {
  it('says a cause in one line: reason, count, objects, since the change, how long', async () => {
    stub(answer([backoff]));
    mount();
    const line = await findCause(/BackOff/);
    expect(line.textContent).toContain('BackOff × 219');
    expect(line.textContent).toContain('2 pods');
    expect(line.textContent).toContain('since rollout api rev. 7 · 15 min');
  });

  it('files the causes into tiles that filter, and a second press unfilters', async () => {
    stub(answer([backoff, sched, pulled]));
    mount();
    await findCause(/BackOff/);
    expect(tile('failedScheduling').getAttribute('data-count')).toBe('4');
    expect(rows()).toEqual(['BackOff', 'FailedScheduling']);
    fireEvent.click(tile('failedScheduling'));
    await waitFor(() => expect(rows()).toEqual(['FailedScheduling']));
    expect(tile('failedScheduling').getAttribute('aria-pressed')).toBe('true');
    fireEvent.click(tile('failedScheduling'));
    await waitFor(() => expect(rows()).toEqual(['BackOff', 'FailedScheduling']));
    fireEvent.click(tile('warnings'));
    await waitFor(() => expect(rows()).toEqual(['BackOff', 'FailedScheduling']));
  });

  it('filters the crash loop tile by the pods the answer names', async () => {
    stub(answer([backoff, sched]));
    mount();
    await findCause(/BackOff/);
    fireEvent.click(tile('crashLoop'));
    await waitFor(() => expect(rows()).toEqual(['BackOff']));
  });

  it('filters by namespace, type and kind, and recounts what a filter narrowed', async () => {
    stub(answer([backoff, sched, pulled, node]));
    mount();
    await findCause(/BackOff/);
    fireEvent.change(screen.getByLabelText('Type'), { target: { value: 'Normal' } });
    await waitFor(() => expect(rows()).toEqual(['Pulled']));
    fireEvent.change(screen.getByLabelText('Type'), { target: { value: '' } });
    fireEvent.change(screen.getByLabelText('Namespace'), { target: { value: 'batch' } });
    await waitFor(() => expect(rows()).toEqual(['FailedScheduling']));
    fireEvent.change(screen.getByLabelText('Namespace'), { target: { value: '' } });
    fireEvent.change(screen.getByLabelText('Kind'), { target: { value: 'Node' } });
    await waitFor(() => expect(rows()).toEqual(['NodeNotReady']));
  });

  it('places a change before the cells of the group that came after it', async () => {
    stub(answer([backoff]));
    mount();
    await findCause(/BackOff/);
    const change = document.querySelector<HTMLElement>(`[data-change="${ROLLOUT.id}"]`);
    const lane = document.querySelector<HTMLElement>('[data-lane="shop/api-1/BackOff"]');
    expect(change?.closest('[data-changes]')).not.toBeNull();
    expect(lane?.getAttribute('data-since')).toBe(ROLLOUT.id);
    // The first cell with Events is right of the change: cells are 100/40 % wide.
    const firstCell = Number([...(lane?.querySelectorAll('[data-cell][data-count]') ?? [])][0]?.getAttribute('data-cell'));
    expect(Number.parseFloat(change?.style.left ?? 'NaN')).toBeLessThan(((firstCell + 1) * 100) / 40);
  });

  it('names every workload and every change marker', async () => {
    stub(answer([backoff, sched]));
    mount();
    await findCause(/BackOff/);
    const names = [...document.querySelectorAll('[data-workload] [data-cut-name]')].map((e) => e.textContent);
    // Busiest first, then by namespace and name.
    expect(names).toEqual(['api-1', 'job-1', 'api-2']);
    const mark = document.querySelector<HTMLElement>(`[data-change="${ROLLOUT.id}"]`);
    expect(mark?.getAttribute('aria-label')).toBe('rollout api rev.7');
    expect(mark?.getAttribute('title')).toContain('rollout api rev. 7');
  });

  it('draws one row per workload that unfolds to its reasons, each with its total', async () => {
    const cilium = (n: string, count: number) => group(n, { namespace: 'kube-system', workloadKind: 'DaemonSet', workloadName: 'cilium', count });
    stub(answer([cause('BackOff', 'Warning', [cilium('cilium-a', 300), cilium('cilium-b', 200)]), cause('Unhealthy', 'Warning', [cilium('cilium-a', 40)]), sched]));
    mount();
    await findCause(/BackOff/);
    const workloads = () => [...document.querySelectorAll<HTMLElement>('[data-workload]')].map((w) => `${w.getAttribute('data-workload') ?? ''}=${w.lastElementChild?.textContent ?? ''}`);
    expect(workloads()).toEqual(['kube-system/cilium=540', 'batch/job-1=5']);
    const lanes = () => [...document.querySelectorAll('[data-lane]')].map((l) => `${l.getAttribute('data-lane') ?? ''}=${l.lastElementChild?.textContent ?? ''}`);
    // The busiest is open; the others wait for a click.
    expect(lanes()).toEqual(['kube-system/cilium/BackOff=500', 'kube-system/cilium/Unhealthy=40']);
    expect(document.querySelector('[data-lane="kube-system/cilium/BackOff"] [data-lane-name]')?.textContent).toBe('BackOff· 2');
    fireEvent.click(within(document.querySelector('[data-workload="batch/job-1"]') as HTMLElement).getByRole('button'));
    expect(lanes()).toEqual(['kube-system/cilium/BackOff=500', 'kube-system/cilium/Unhealthy=40', 'batch/job-1/FailedScheduling=5']);
    fireEvent.click(within(document.querySelector('[data-workload="kube-system/cilium"]') as HTMLElement).getByRole('button'));
    expect(lanes()).toEqual(['batch/job-1/FailedScheduling=5']);
  });

  it('says on each cell how many Events it holds, when, and that they are spread', async () => {
    stub(answer([cause('BackOff', 'Warning', [group('api-1', { count: 30, firstSeen: at(12), lastSeen: at(2) })])]));
    mount();
    await findCause(/BackOff/);
    const cells = [...document.querySelectorAll<HTMLElement>('[data-lane="shop/api-1/BackOff"] [data-cell][data-count]')];
    expect(cells.length).toBeGreaterThan(1);
    expect(cells.reduce((n, c) => n + Number(c.getAttribute('data-count')), 0)).toBeCloseTo(30, 0);
    expect(cells[0]?.getAttribute('title')).toMatch(/^(<1|(≈ )?\d+) · −\d+ min( \d+ s)? to −\d+ min( \d+ s)?\nSpread evenly from each object’s first to last Event$/);
  });

  it('draws an Event dated after the answer was built in the last cell, not outside the row', async () => {
    stub(answer([cause('BackOff', 'Warning', [group('api-1', { count: 1, firstSeen: at(-1), lastSeen: at(-1) })])]));
    mount();
    await findCause(/BackOff/);
    const cells = [...document.querySelectorAll<HTMLElement>('[data-lane="shop/api-1/BackOff"] [data-cell][data-count]')];
    expect(cells.map((c) => [c.getAttribute('data-cell'), c.getAttribute('data-count')])).toEqual([['39', '1']]);
  });

  it('hides Normal causes behind a toggle that says how many, and the Type filter overrides it', async () => {
    stub(answer([backoff, pulled]));
    mount();
    await findCause(/BackOff/);
    const toggle = document.querySelector<HTMLElement>('[data-normal-toggle]') as HTMLElement;
    expect(toggle.textContent).toBe('Normal1');
    expect(toggle.getAttribute('aria-pressed')).toBe('false');
    expect(rows()).toEqual(['BackOff']);
    fireEvent.click(toggle);
    await waitFor(() => expect(rows()).toEqual(['BackOff', 'Pulled']));
    expect(toggle.getAttribute('aria-pressed')).toBe('true');
    expect(document.querySelector('[data-lane="shop/api-1/Pulled"]')).not.toBeNull();
    fireEvent.click(toggle);
    await waitFor(() => expect(rows()).toEqual(['BackOff']));
    fireEvent.change(screen.getByLabelText('Type'), { target: { value: 'Normal' } });
    await waitFor(() => expect(rows()).toEqual(['Pulled']));
    expect(document.querySelector('[data-normal-toggle]')).toBeNull();
  });

  it('draws nodes that came together as one marker that lists them, and a lone node as its own', async () => {
    const nodes = [0, 1, 2].map((i) => ({ id: `node-added//node-${String(i)}/0`, kind: 'node-added' as const, objectKind: 'Node', name: `node-${String(i)}`, time: at(30 - i * 3), referenced: true }));
    const lone = { id: 'node-notready//node-9/0', kind: 'node-notready' as const, objectKind: 'Node', name: 'node-9', time: at(5), referenced: true };
    stub(answer([backoff], { changes: [ROLLOUT, ...nodes, lone] }));
    mount();
    await findCause(/BackOff/);
    const marks = [...document.querySelectorAll<HTMLElement>('[data-node-group]')];
    expect(marks.map((m) => `${m.getAttribute('aria-label') ?? ''}:${m.getAttribute('data-nodes') ?? ''}`)).toEqual(['3 nodes added:3', 'node node-9 not ready:1']);
    // The answer holds when a node came, not its state now.
    expect(marks[0]?.getAttribute('title')).toBe('3 nodes added\nnode-0 · 30 min\nnode-1 · 27 min\nnode-2 · 24 min');
    expect(marks[1]?.getAttribute('title')).toBe('node node-9 not ready\nnode-9 · 5 min');
    expect(marks.map((m) => m.getAttribute('data-track'))).toEqual(['0', '0']);
    const row = document.querySelector('[data-nodes-row]')?.textContent ?? '';
    expect(row).toContain('+3');
    expect(row).toContain('!1');
    // The rollout stays on the changes row, on its own.
    expect([...document.querySelectorAll('[data-change]')].map((c) => c.getAttribute('data-change'))).toEqual([ROLLOUT.id]);
  });

  it('leads with the correlations, and a click on one lights its rows and its node marker', async () => {
    const nodes = [0, 1, 2].map((i) => ({ id: `node-added//node-${String(i)}/0`, kind: 'node-added' as const, objectKind: 'Node', name: `node-${String(i)}`, time: at(30 - i * 3), referenced: true }));
    const cilium = (n: string, i: number) => group(n, { namespace: 'kube-system', workloadName: 'cilium', count: 100 + i, since: nodes[i]?.id ?? '' });
    const crash = cause('BackOff', 'Warning', [cilium('cilium-a', 0), cilium('cilium-b', 1), cilium('cilium-c', 2)], { since: nodes[0]?.id ?? '' });
    // Another reason on another pod: the card's objects are the lead reason's.
    const probe = cause('Unhealthy', 'Warning', [group('cilium-x', { namespace: 'kube-system', workloadName: 'cilium', count: 5, since: nodes[1]?.id ?? '' })]);
    stub(answer([crash, backoff, sched, probe], { changes: [ROLLOUT, ...nodes] }));
    mount();
    await findCause(/FailedScheduling/);
    const cards = [...document.querySelectorAll<HTMLElement>('[data-correlation]')];
    expect(cards.map((c) => c.textContent)).toEqual(['3 nodes added303BackOff3 objectsUnhealthy 5', 'rollout api rev. 7219BackOff2 objects']);
    // The cards come before the timeline.
    const timeline = document.querySelector('[data-timeline]') as HTMLElement;
    expect((cards[0] as HTMLElement).compareDocumentPosition(timeline) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    fireEvent.click(cards[0] as HTMLElement);
    fireEvent.mouseLeave(cards[0] as HTMLElement);
    const lit = (sel: string, attr: string) => [...document.querySelectorAll<HTMLElement>(`${sel}[data-lit="true"]`)].map((e) => e.getAttribute(attr));
    expect(lit('[data-workload]', 'data-workload')).toEqual(['kube-system/cilium']);
    expect(lit('[data-node-group]', 'data-nodes')).toEqual(['3']);
    expect(document.querySelector('[data-workload="shop/api-1"]')?.getAttribute('data-dim')).toBe('true');
    expect(document.querySelector(`[data-change="${ROLLOUT.id}"]`)?.getAttribute('data-lit')).toBe('false');
    expect(cards[0]?.getAttribute('aria-pressed')).toBe('true');
    // The list follows: what came after the nodes stays, the rest dims.
    expect((await findCause(/FailedScheduling/)).closest('[data-cause]')?.className).toContain('opacity-50');
    expect((await findCause(/BackOff × 303/)).closest('[data-cause]')?.className).not.toContain('opacity-50');
    fireEvent.click(cards[0] as HTMLElement);
    expect(lit('[data-workload]', 'data-workload')).toEqual([]);
  });

  it('says how much of a cut answer the lists hold, in place of truncated', async () => {
    stub(answer([backoff], { truncated: true, tiles: { ...answer([]).tiles, warnings: { count: 2058, state: 'read' } } }));
    mount();
    await findCause(/BackOff/);
    expect(document.querySelector('[data-held]')?.textContent).toBe('219 of 2 058');
    expect(screen.queryByText('truncated')).toBeNull();
  });

  it('shortens a long workload name on the marker and keeps the whole one in the title', async () => {
    const long = 'checkout-payment-gateway-service-blue';
    stub(answer([backoff], { changes: [{ ...ROLLOUT, name: long }] }));
    mount();
    await findCause(/BackOff/);
    const mark = document.querySelector<HTMLElement>(`[data-change="${ROLLOUT.id}"]`);
    expect(mark?.getAttribute('aria-label')).toBe('rollout checkout-payment-gatewa… rev.7');
    expect(mark?.getAttribute('title')).toContain(long);
  });

  it('shows the tiles of the chosen namespace, read from the route for that namespace', async () => {
    const batch = answer([sched], {
      namespace: 'batch',
      tiles: {
        warnings: { count: 4, state: 'read' },
        crashLoop: { count: 0, state: 'read' },
        oomKilled: { count: 0, state: 'read' },
        failedScheduling: { count: 4, state: 'read' },
        objectsTouched: { count: 1, state: 'read' },
      },
    });
    const fetch = stubByNamespace(answer([backoff, sched]), { batch });
    mount();
    await findCause(/BackOff/);
    expect(figures()).toEqual(['10', '1', '0', '4', '3']);
    fireEvent.change(screen.getByLabelText('Namespace'), { target: { value: 'batch' } });
    await waitFor(() => expect(figures()).toEqual(['4', '0', '0', '4', '1']));
    expect(fetch.mock.calls.some(([u]) => String(u).includes('namespace=batch'))).toBe(true);
    fireEvent.change(screen.getByLabelText('Namespace'), { target: { value: '' } });
    await waitFor(() => expect(figures()).toEqual(['10', '1', '0', '4', '3']));
  });

  it('opens filtered from ?namespace= and writes the choice back by replacing the entry', async () => {
    stubByNamespace(answer([backoff, sched]), {});
    const router = mount('/c/prod/x/events?namespace=batch');
    await waitFor(() => expect(rows()).toEqual(['FailedScheduling']));
    expect((screen.getByLabelText('Namespace') as HTMLSelectElement).value).toBe('batch');
    fireEvent.change(screen.getByLabelText('Namespace'), { target: { value: 'shop' } });
    await waitFor(() => expect(router.state.location.search).toBe('?namespace=shop'));
    expect(router.state.historyAction).toBe('REPLACE');
    fireEvent.change(screen.getByLabelText('Namespace'), { target: { value: '' } });
    await waitFor(() => expect(router.state.location.search).toBe(''));
  });

  it('lights the groups after a change on hover and click, dims the rest, and clears on Escape', async () => {
    stub(answer([backoff, sched]));
    mount();
    await findCause(/BackOff/);
    const mark = document.querySelector<HTMLElement>(`[data-change="${ROLLOUT.id}"]`) as HTMLElement;
    const lanes = () => [...document.querySelectorAll<HTMLElement>('[data-workload]')];
    const state = () => lanes().map((l) => `${l.getAttribute('data-workload') ?? ''}:${l.getAttribute('data-lit') === 'true' ? 'lit' : l.getAttribute('data-dim') === 'true' ? 'dim' : 'plain'}`).sort();
    expect(state().every((s) => s.endsWith('plain'))).toBe(true);
    fireEvent.mouseEnter(mark);
    expect(state()).toEqual(['batch/job-1:dim', 'shop/api-1:lit', 'shop/api-2:lit']);
    fireEvent.mouseLeave(mark);
    expect(state().every((s) => s.endsWith('plain'))).toBe(true);
    fireEvent.click(mark);
    fireEvent.mouseLeave(mark);
    expect(state()).toEqual(['batch/job-1:dim', 'shop/api-1:lit', 'shop/api-2:lit']);
    fireEvent.click(mark);
    expect(state().every((s) => s.endsWith('plain'))).toBe(true);
    fireEvent.click(mark);
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(state().every((s) => s.endsWith('plain'))).toBe(true);
  });

  it('lights a cause and its change from a click on its row', async () => {
    stub(answer([backoff, sched]));
    mount();
    await findCause(/BackOff/);
    const causesList = within(screen.getByRole('region', { name: 'Causes' }));
    fireEvent.click(causesList.getByRole('button', { name: /FailedScheduling/ }));
    const lit = [...document.querySelectorAll('[data-workload][data-lit="true"]')].map((l) => l.getAttribute('data-workload'));
    expect(lit).toEqual(['batch/job-1']);
    expect(document.querySelectorAll('[data-workload][data-dim="true"]')).toHaveLength(2);
    fireEvent.click(causesList.getByRole('button', { name: /BackOff/ }));
    const next = [...document.querySelectorAll('[data-workload][data-lit="true"]')].map((l) => l.getAttribute('data-workload'));
    expect(next).toEqual(['shop/api-1', 'shop/api-2']);
    expect(document.querySelector(`[data-change="${ROLLOUT.id}"]`)?.getAttribute('data-lit')).toBe('true');
    fireEvent.click(causesList.getByRole('button', { name: /BackOff/ }));
    expect(document.querySelectorAll('[data-workload][data-dim="true"]')).toHaveLength(0);
  });

  it('starts the axis at the earliest drawn activity, not at the window', async () => {
    stub(answer([backoff, sched]));
    mount();
    await findCause(/BackOff/);
    const section = document.querySelector<HTMLElement>('[data-timeline]') as HTMLElement;
    const start = Number(section.getAttribute('data-axis-start'));
    // The earliest drawn time is a group first seen 20 min ago; the window began 60 min ago.
    expect(start).toBeGreaterThan(NOW - 25 * MIN);
    expect(start).toBeLessThanOrEqual(NOW - 20 * MIN);
    const first = within(section).getAllByText(/^−\d+ min$/)[0];
    expect(first?.textContent).not.toBe('−60 min');
    expect(within(section).getByText('now')).toBeTruthy();
  });

  it('unfolds a cause per object', async () => {
    stub(answer([backoff]));
    mount();
    fireEvent.click(await findCause(/BackOff/));
    expect(document.querySelectorAll('[data-group]')).toHaveLength(2);
    expect(document.querySelector('[data-group="shop/api-1"]')?.textContent).toContain('× 214');
  });

  it('reads a refused source as refused, never as zero', async () => {
    const refused: Section = { state: 'denied', complete: false, note: 'The cluster says you may not make this read.' };
    stub(
      answer([], {
        tiles: {
          warnings: { count: 0, state: 'denied' },
          crashLoop: { count: 0, state: 'failed' },
          oomKilled: { count: 0, state: 'unreviewable' },
          failedScheduling: { count: 0, state: 'denied' },
          objectsTouched: { count: 0, state: 'denied' },
        },
        sources: { events: refused, replicasets: READ, pods: refused, configmaps: READ, nodes: READ },
      }),
    );
    mount();
    await screen.findByText('Events: refused');
    expect(tile('warnings').textContent).toContain('refused');
    expect(tile('warnings').getAttribute('data-count')).toBeNull();
    expect(tile('crashLoop').textContent).toContain('failed');
    expect(document.querySelector('[data-source="Events"]')?.textContent).toBe('Events: refused');
    expect(document.querySelector('[data-none]')).toBeNull();
  });

  it('reads an empty answer over a read source as none', async () => {
    stub(answer([], { changes: [] }));
    mount();
    expect(await screen.findByText('None')).toBeTruthy();
    expect(tile('warnings').textContent).toContain('10');
    expect(document.querySelector('[data-source]')).toBeNull();
  });

  it('says nothing is none when only Normal Events are there, and offers the toggle', async () => {
    stub(answer([pulled]));
    mount();
    await waitFor(() => expect(document.querySelector('[data-normal-toggle]')).not.toBeNull());
    const toggle = document.querySelector<HTMLElement>('[data-normal-toggle]') as HTMLElement;
    expect(toggle.textContent).toBe('Normal1');
    expect(document.querySelector('[data-none]')).toBeNull();
    fireEvent.click(toggle);
    expect(await findCause(/Pulled/)).toBeTruthy();
  });

  it('says a refusal of the whole read in its own words', async () => {
    stub({ code: 'list_forbidden', message: 'The cluster says you may not list events here, so none can be shown.' }, 403);
    mount();
    expect(await screen.findByText(/may not list events here/)).toBeTruthy();
    expect(document.querySelector('[data-none]')).toBeNull();
  });

  it('draws no explanatory sentence', async () => {
    stub(answer([backoff, sched, pulled]));
    mount();
    await findCause(/BackOff/);
    const text = document.getElementById('events')?.textContent ?? '';
    // Labels, figures and short states only: no run of prose.
    expect(text).not.toMatch(/\b(this|which|because|shows|are the)\b/i);
  });

  it('asks the window it is given, and again only on the poll', async () => {
    const fetch = stub(answer([backoff]));
    mount();
    await findCause(/BackOff/);
    fireEvent.click(screen.getByRole('button', { name: '3 h' }));
    await waitFor(() => expect(fetch.mock.calls.some(([u]) => String(u).endsWith('?window=3h'))).toBe(true));
  });

  it('polls every 30 s and only while it is visible', () => {
    const options = pageQuery('prod', '1h');
    expect(options.refetchInterval).toBe(POLL_MS);
    expect(POLL_MS).toBe(30_000);
    expect(options.refetchIntervalInBackground).toBe(false);
  });

  it('draws 2 000 groups and 200 changes within a time ceiling, one screen of causes', async () => {
    const causes: Cause[] = Array.from({ length: 400 }, (_, c) =>
      cause(
        `Reason${String(c)}`,
        c % 3 === 0 ? 'Normal' : 'Warning',
        Array.from({ length: 5 }, (_, i) =>
          group(`pod-${String(c)}-${String(i)}`, { count: 1 + ((c * 7 + i) % 90), firstSeen: at(55 - ((c + i) % 50)), lastSeen: at(2 + (i % 5)) }),
        ),
      ),
    );
    const changes: Change[] = Array.from({ length: 200 }, (_, i) => ({
      id: `rollout/shop/d${String(i)}/1`,
      kind: i % 2 === 0 ? 'rollout' : 'scaling',
      objectKind: 'Deployment',
      namespace: 'shop',
      name: `d${String(i)}`,
      time: at(1 + (i % 58)),
      referenced: true,
    }));
    const fetch = stub(answer(causes, { changes }));
    const started = performance.now();
    mount();
    await findCause(/Reason1 ×/);
    expect(document.querySelectorAll('[data-cause]')).toHaveLength(PAGE_CAUSES);
    expect(document.querySelectorAll('[data-change]')).toHaveLength(200);
    // Normal causes wait behind the toggle: 266 Warning causes of 5 pods each.
    expect(document.querySelectorAll('[data-workload]')).toHaveLength(24);
    expect(document.querySelector('[data-rows-shown]')?.textContent).toBe('24 of 1330');
    fireEvent.click(screen.getByRole('button', { name: '+24' }));
    expect(document.querySelectorAll('[data-workload]')).toHaveLength(48);
    expect(performance.now() - started).toBeLessThan(5000);
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});
