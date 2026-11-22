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

describe('Event Intelligence', () => {
  it('says a cause in one line: reason, count, objects, since the change, how long', async () => {
    stub(answer([backoff]));
    mount();
    const line = await screen.findByRole('button', { name: /BackOff/ });
    expect(line.textContent).toContain('BackOff × 219');
    expect(line.textContent).toContain('2 pods');
    expect(line.textContent).toContain('since rollout api rev. 7 · 15 min');
  });

  it('files the causes into tiles that filter, and a second press unfilters', async () => {
    stub(answer([backoff, sched, pulled]));
    mount();
    await screen.findByRole('button', { name: /BackOff/ });
    expect(tile('failedScheduling').getAttribute('data-count')).toBe('4');
    expect(rows()).toEqual(['BackOff', 'FailedScheduling', 'Pulled']);
    fireEvent.click(tile('failedScheduling'));
    await waitFor(() => expect(rows()).toEqual(['FailedScheduling']));
    expect(tile('failedScheduling').getAttribute('aria-pressed')).toBe('true');
    fireEvent.click(tile('failedScheduling'));
    await waitFor(() => expect(rows()).toEqual(['BackOff', 'FailedScheduling', 'Pulled']));
    fireEvent.click(tile('warnings'));
    await waitFor(() => expect(rows()).toEqual(['BackOff', 'FailedScheduling']));
  });

  it('filters the crash loop tile by the pods the answer names', async () => {
    stub(answer([backoff, sched]));
    mount();
    await screen.findByRole('button', { name: /BackOff/ });
    fireEvent.click(tile('crashLoop'));
    await waitFor(() => expect(rows()).toEqual(['BackOff']));
  });

  it('filters by namespace, type and kind, and recounts what a filter narrowed', async () => {
    stub(answer([backoff, sched, pulled, node]));
    mount();
    await screen.findByRole('button', { name: /BackOff/ });
    fireEvent.change(screen.getByLabelText('Type'), { target: { value: 'Normal' } });
    await waitFor(() => expect(rows()).toEqual(['Pulled']));
    fireEvent.change(screen.getByLabelText('Type'), { target: { value: '' } });
    fireEvent.change(screen.getByLabelText('Namespace'), { target: { value: 'batch' } });
    await waitFor(() => expect(rows()).toEqual(['FailedScheduling']));
    fireEvent.change(screen.getByLabelText('Namespace'), { target: { value: '' } });
    fireEvent.change(screen.getByLabelText('Kind'), { target: { value: 'Node' } });
    await waitFor(() => expect(rows()).toEqual(['NodeNotReady']));
  });

  it('places a change before the group that came after it on the timeline', async () => {
    stub(answer([backoff]));
    mount();
    await screen.findByRole('button', { name: /BackOff/ });
    const change = document.querySelector<HTMLElement>(`[data-change="${ROLLOUT.id}"]`);
    const bar = document.querySelector<HTMLElement>('[data-bar="shop/api-1"]');
    expect(change).not.toBeNull();
    expect(bar).not.toBeNull();
    // The change is drawn in the row above the bars, and its left edge precedes the bar's.
    const left = (e: HTMLElement | null) => Number.parseFloat(e?.style.left ?? 'NaN');
    expect(left(change)).toBeLessThan(left(bar));
    expect(bar?.getAttribute('data-since')).toBe(ROLLOUT.id);
    expect(change?.closest('[data-changes]')).not.toBeNull();
    expect(bar?.closest('[data-bars]')).not.toBeNull();
  });

  it('names every lane and labels every change marker', async () => {
    stub(answer([backoff, sched]));
    mount();
    await screen.findByRole('button', { name: /BackOff/ });
    const names = [...document.querySelectorAll('[data-lane-name]')].map((e) => e.textContent);
    expect(names).toEqual(expect.arrayContaining(['api-1 · BackOff', 'api-2 · BackOff', 'job-1 · FailedScheduling']));
    expect(names).toHaveLength(3);
    const mark = document.querySelector<HTMLElement>(`[data-change="${ROLLOUT.id}"]`);
    expect(mark?.textContent).toBe('rollout api rev.7');
    expect(mark?.getAttribute('title')).toContain('rollout api rev. 7');
  });

  it('shortens a long workload name on the marker and keeps the whole one in the title', async () => {
    const long = 'checkout-payment-gateway-service-blue';
    stub(answer([backoff], { changes: [{ ...ROLLOUT, name: long }] }));
    mount();
    await screen.findByRole('button', { name: /BackOff/ });
    const mark = document.querySelector<HTMLElement>(`[data-change="${ROLLOUT.id}"]`);
    expect(mark?.textContent).toBe('rollout checkout-payment-gatewa… rev.7');
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
    await screen.findByRole('button', { name: /BackOff/ });
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
    await screen.findByRole('button', { name: /BackOff/ });
    const mark = document.querySelector<HTMLElement>(`[data-change="${ROLLOUT.id}"]`) as HTMLElement;
    const lanes = () => [...document.querySelectorAll<HTMLElement>('[data-lane]')];
    const state = () => lanes().map((l) => `${l.getAttribute('data-lane') ?? ''}:${l.getAttribute('data-lit') === 'true' ? 'lit' : l.getAttribute('data-dim') === 'true' ? 'dim' : 'plain'}`).sort();
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
    await screen.findByRole('button', { name: /BackOff/ });
    const causesList = within(screen.getByRole('region', { name: 'Causes' }));
    fireEvent.click(causesList.getByRole('button', { name: /FailedScheduling/ }));
    const lit = [...document.querySelectorAll('[data-lane][data-lit="true"]')].map((l) => l.getAttribute('data-lane'));
    expect(lit).toEqual(['batch/job-1']);
    expect(document.querySelectorAll('[data-lane][data-dim="true"]')).toHaveLength(2);
    fireEvent.click(causesList.getByRole('button', { name: /BackOff/ }));
    const next = [...document.querySelectorAll('[data-lane][data-lit="true"]')].map((l) => l.getAttribute('data-lane'));
    expect(next).toEqual(['shop/api-1', 'shop/api-2']);
    expect(document.querySelector(`[data-change="${ROLLOUT.id}"]`)?.getAttribute('data-lit')).toBe('true');
    fireEvent.click(causesList.getByRole('button', { name: /BackOff/ }));
    expect(document.querySelectorAll('[data-lane][data-dim="true"]')).toHaveLength(0);
  });

  it('starts the axis at the earliest drawn activity, not at the window', async () => {
    stub(answer([backoff, sched]));
    mount();
    await screen.findByRole('button', { name: /BackOff/ });
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
    fireEvent.click(await screen.findByRole('button', { name: /BackOff/ }));
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

  it('says a refusal of the whole read in its own words', async () => {
    stub({ code: 'list_forbidden', message: 'The cluster says you may not list events here, so none can be shown.' }, 403);
    mount();
    expect(await screen.findByText(/may not list events here/)).toBeTruthy();
    expect(document.querySelector('[data-none]')).toBeNull();
  });

  it('draws no explanatory sentence', async () => {
    stub(answer([backoff, sched, pulled]));
    mount();
    await screen.findByRole('button', { name: /BackOff/ });
    const text = document.getElementById('events')?.textContent ?? '';
    // Labels, figures and short states only: no run of prose.
    expect(text).not.toMatch(/\b(this|which|because|shows|are the)\b/i);
  });

  it('asks the window it is given, and again only on the poll', async () => {
    const fetch = stub(answer([backoff]));
    mount();
    await screen.findByRole('button', { name: /BackOff/ });
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
    await screen.findByRole('button', { name: /Reason0/ });
    expect(document.querySelectorAll('[data-cause]')).toHaveLength(PAGE_CAUSES);
    expect(document.querySelectorAll('[data-change]')).toHaveLength(200);
    expect(document.querySelectorAll('[data-bar]')).toHaveLength(24);
    expect(document.querySelector('[data-timeline]')?.textContent).toContain('24 of 2000');
    expect(performance.now() - started).toBeLessThan(5000);
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});
