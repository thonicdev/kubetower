import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { Suspense } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { History, PodAnswer } from './data';
import { HistoryView } from './History';
import JVMTab from './JVMTab';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

const POD = '/x/kind/runtime/pod/shop/billing-0';
const HISTORY = `${POD}/history`;

const pod: PodAnswer = {
  cluster: 'kind',
  namespace: 'shop',
  name: 'billing-0',
  phase: 'Running',
  java: true,
  owner: null,
  containers: [
    {
      name: 'app',
      image: 'eclipse-temurin:21-jdk',
      java: true,
      hints: [{ source: 'image', reason: 'a Java image' }],
      request: null,
      limit: null,
      heap: null,
      risk: null,
      notes: [],
      workingSet: null,
    },
  ],
};

const mem = (bytes: number, text: string) => ({ bytes, text });

const read: History = {
  cluster: 'kind',
  namespace: 'shop',
  pod: 'billing-0',
  state: 'read',
  detail: 'Prometheus, 7d at 15m',
  from: 1_773_000_000,
  to: 1_773_600_000,
  scale: mem(536870912, '512.0 MiB'),
  ticks: [
    { y: 0, text: '512.0 MiB' },
    { y: 50, text: '256.0 MiB' },
    { y: 100, text: '0 B' },
  ],
  series: [
    {
      container: 'app',
      points: [
        [0, 60],
        [50, 40],
        [100, 50],
      ],
      last: mem(268435456, '256.0 MiB'),
      min: mem(150000000, '143.1 MiB'),
      max: mem(322122547, '307.2 MiB'),
      limit: mem(536870912, '512.0 MiB'),
      limitAt: 0,
    },
  ],
};

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

function stub(history: () => Response): string[] {
  const asked: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      // The jcmd probe is not what these tests count.
      if (url.endsWith('/tools')) return Promise.resolve(json({ jcmd: true }));
      asked.push(url);
      return Promise.resolve(url === POD ? json(pod) : url === HISTORY ? history() : json({}, 599));
    }),
  );
  return asked;
}

function mount(): void {
  const router = createMemoryRouter(
    [{ path: '/c/:cluster/*', element: <Suspense><JVMTab cluster="kind" kind="pods" namespace="shop" name="billing-0" /></Suspense> }],
    { initialEntries: ['/c/kind/pods'] },
  );
  render(
    <QueryClientProvider client={new QueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

describe('the memory history', () => {
  it('reads nothing until its button is clicked, then draws the lines as given', async () => {
    const asked = stub(() => json(read));
    mount();
    const button = await screen.findByText('7 days');
    expect(asked).toEqual([POD]);
    fireEvent.click(button);
    await waitFor(() => expect(document.querySelector('[data-jvm-history-result]')).not.toBeNull());
    expect(asked).toEqual([POD, HISTORY]);
    const line = document.querySelector('[data-jvm-history-line="app"]');
    expect(line?.getAttribute('points')).toBe('0,60 50,40 100,50');
    expect(document.querySelector('[data-jvm-history-limit="app"]')?.getAttribute('y1')).toBe('0');
    expect(document.querySelector('[data-jvm-history-result]')?.textContent).toContain('256.0 MiB now');
  });

  it('draws each state as its own thing, never as an empty chart', () => {
    for (const [state, label, tone] of [
      ['absent', 'No metrics available', 'normal'],
      ['unreadable', 'No metrics available', 'normal'],
      ['empty', 'No series for this pod', 'normal'],
    ] as const) {
      cleanup();
      render(<HistoryView history={{ ...read, state, series: [], detail: 'why' }} />);
      const shown = document.querySelector(`[data-jvm-history-state="${state}"]`);
      expect(shown?.textContent).toBe(label);
      expect(shown?.querySelector('[data-tone]')?.getAttribute('data-tone')).toBe(tone);
      expect(shown?.querySelector('[data-tone]')?.getAttribute('title')).toBe('why');
      expect(document.querySelector('svg')).toBeNull();
    }
  });

  it('says a refusal and a failure by their codes, and a partial read with a pill', async () => {
    stub(() => json({ code: 'list_forbidden', message: 'no' }, 403));
    mount();
    fireEvent.click(await screen.findByText('7 days'));
    await waitFor(() => expect(document.querySelector('[data-jvm-history] [data-jvm-failure="list_forbidden"]')).not.toBeNull());
    expect(document.querySelector('[data-jvm-history]')?.textContent).toContain('You may not list pods here');
    cleanup();
    stub(() => json({ ...read, partial: '2 of 9 batches could not be read' }));
    mount();
    fireEvent.click(await screen.findByText('7 days'));
    await waitFor(() => expect(document.querySelector('[data-jvm-history] [data-pill="warn"]')?.textContent).toBe('partial'));
  });

  it('asks again while the window is still being read', async () => {
    let n = 0;
    const asked = stub(() => (n++ === 0 ? json({ ...read, state: 'pending', series: [] }) : json(read)));
    mount();
    fireEvent.click(await screen.findByText('7 days'));
    await waitFor(() => expect(document.querySelector('[data-jvm-history-state="pending"]')).not.toBeNull());
    await waitFor(() => expect(document.querySelector('[data-jvm-history-result]')).not.toBeNull(), { timeout: 8000 });
    expect(asked.filter((u) => u === HISTORY)).toHaveLength(2);
  }, 12_000);
});
