import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { Suspense } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { ClassHistogram, ClassRow, PodAnswer } from './data';
import { HistogramView } from './Histogram';
import JVMTab from './JVMTab';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const POD = '/x/kind/runtime/pod/shop/billing-0';
const HISTOGRAM = `${POD}/app/histogram`;

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

const rows: ClassRow[] = [
  { rank: 1, name: 'int[]', module: 'java.base', instances: 21342, bytes: 1003808, share: 30 },
  { rank: 2, name: 'com.shop.Invoice', instances: 9000, bytes: 500000, share: 15 },
];

const answer: ClassHistogram = {
  cluster: 'kind',
  namespace: 'shop',
  pod: 'billing-0',
  container: 'app',
  state: 'histogram',
  pid: 1,
  pidFrom: 'listed',
  classes: 260,
  instances: 87714,
  bytes: 3339720,
  rows,
  cut: true,
};

function stub(reply: (url: string, init?: RequestInit) => Response): { url: string; method: string; body: unknown }[] {
  const asked: { url: string; method: string; body: unknown }[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      asked.push({ url, method: init?.method ?? 'GET', body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined });
      if (url.endsWith('/tools')) return Promise.resolve(json({ jcmd: true }));
      return Promise.resolve(reply(url, init));
    }),
  );
  return asked;
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

function mount(): void {
  const router = createMemoryRouter(
    [{ path: '/c/:cluster/*', element: <Suspense><JVMTab cluster="kind" kind="pods" namespace="shop" name="billing-0" /></Suspense> }],
    { initialEntries: ['/c/kind/pods'] },
  );
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

describe('the class histogram', () => {
  it('asks for a confirmation that names the full GC, and sends nothing before Run', async () => {
    const asked = stub((url) => (url === POD ? json(pod) : json({}, 599)));
    mount();
    fireEvent.click(await screen.findByText('Class histogram'));
    const confirm = document.querySelector('[data-jvm-histogram-confirm]');
    expect(confirm?.textContent).toContain('Full GC · pauses the application');
    expect(asked.some((a) => a.url === HISTOGRAM)).toBe(false);
    fireEvent.click(within(confirm as HTMLElement).getByText('Cancel'));
    expect(document.querySelector('[data-jvm-histogram-confirm]')).toBeNull();
    expect(asked.some((a) => a.url === HISTOGRAM)).toBe(false);
  });

  it('posts a confirmed request on Run and draws the classes as bars', async () => {
    const asked = stub((url) => (url === POD ? json(pod) : url === HISTOGRAM ? json(answer) : json({}, 599)));
    mount();
    fireEvent.click(await screen.findByText('Class histogram'));
    fireEvent.click(screen.getByText('Run'));
    await waitFor(() => expect(document.querySelector('[data-jvm-histogram-result]')).not.toBeNull());
    const post = asked.find((a) => a.url === HISTOGRAM);
    expect(post).toEqual({ url: HISTOGRAM, method: 'POST', body: { confirmed: true } });
    const list = document.querySelector('[data-jvm-class-rows]') as HTMLElement;
    expect(list.querySelectorAll('li')).toHaveLength(2);
    expect(within(list).getByText('int[]')).toBeTruthy();
    expect(document.querySelector('[data-jvm-histogram-result]')?.textContent).toContain('top 2');
  });

  it('draws the failures the route names, and not as an empty histogram', async () => {
    for (const [status, code, title] of [
      [403, 'exec_forbidden', 'You may not run commands in this pod'],
      [422, 'no_jcmd', 'No jcmd: use a JDK image'],
      [502, 'jcmd_failed', 'The class histogram failed'],
    ] as const) {
      cleanup();
      stub((url) => (url === POD ? json(pod) : url === HISTOGRAM ? json({ code, message: 'said' }, status) : json({}, 599)));
      mount();
      fireEvent.click(await screen.findByText('Class histogram'));
      fireEvent.click(screen.getByText('Run'));
      await waitFor(() => expect(document.querySelector(`[data-jvm-histogram] [data-jvm-failure="${code}"]`)).not.toBeNull());
      expect(document.querySelector('[data-jvm-histogram]')?.textContent).toContain(title);
      expect(document.querySelector('[data-jvm-histogram-result]')).toBeNull();
    }
  });

  it('says a cut output with a pill, and the bars follow the largest row', () => {
    render(<HistogramView histogram={{ ...answer, outputCut: true }} />);
    expect(document.querySelector('[data-pill="warn"]')?.textContent).toBe('output cut');
    const bars = [...document.querySelectorAll<HTMLElement>('[data-jvm-class] > span:nth-child(2) > span:first-child')];
    expect(bars.map((b) => b.style.width)).toEqual(['100%', '50%']);
    expect(bars[0]?.className).toContain('bg-plate-blue');
    expect(bars[1]?.className).toContain('bg-plate-green');
  });
});
