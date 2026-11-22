import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { Suspense } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { PodAnswer } from './data';
import JVMTab from './JVMTab';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

/** The buttons that run jcmd, greyed when the container has none. */

const POD = '/x/kind/jvm/pod/shop/billing-0';
const TOOLS = `${POD}/app/tools`;
const TITLE = 'No JDK tools in this container';

const pod: PodAnswer = {
  cluster: 'kind',
  namespace: 'shop',
  name: 'billing-0',
  phase: 'Running',
  java: true,
  containers: [
    {
      name: 'app',
      image: 'eclipse-temurin:21-jre',
      java: true,
      hints: [{ source: 'image', reason: 'a Java image' }],
      request: null,
      limit: null,
      heap: null,
      risk: null,
      notes: [],
    },
  ],
};

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const noJcmd = { code: 'no_jcmd', message: 'There is no jcmd in this image.' };

/** Answers the pod and the probe; `probe` is a function so a test can hold it. */
function stub(probe: () => Promise<Response>, inspect?: () => Response): string[] {
  const asked: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      const method = init?.method ?? 'GET';
      asked.push(`${method} ${url}`);
      if (url === POD) return Promise.resolve(json(200, pod));
      if (url === TOOLS) return probe();
      if (method === 'POST' && url === `${POD}/app/inspect` && inspect !== undefined) return Promise.resolve(inspect());
      return Promise.resolve(new Response('no stub', { status: 599 }));
    }),
  );
  return asked;
}

function mount(): void {
  const router = createMemoryRouter(
    [
      {
        path: '/c/:cluster/*',
        element: (
          <Suspense>
            <JVMTab cluster="kind" kind="pods" namespace="shop" name="billing-0" />
          </Suspense>
        ),
      },
    ],
    { initialEntries: ['/c/kind/pods'] },
  );
  render(
    <QueryClientProvider client={new QueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

const jcmdButtons = () => [
  document.querySelector<HTMLButtonElement>('[data-jvm-inspect]'),
  document.querySelector<HTMLButtonElement>('[data-jvm-thread-dump]'),
  document.querySelector<HTMLButtonElement>('[data-jvm-heap-dump]'),
  document.querySelector<HTMLButtonElement>('[data-jvm-histogram-start]'),
  document.querySelector<HTMLButtonElement>('[data-jvm-jfr-record]'),
];

describe('a container with no JDK tools', () => {
  it('greys every jcmd button with a tooltip and adds no sentence', async () => {
    const asked = stub(() => Promise.resolve(json(422, noJcmd)));
    mount();
    await waitFor(() => expect(jcmdButtons().every((b) => b?.disabled === true)).toBe(true));
    for (const b of jcmdButtons()) expect(b?.getAttribute('title')).toBe(TITLE);
    expect(document.body.textContent).not.toMatch(/No JDK tools/);
    expect(asked.filter((a) => a === `GET ${TOOLS}`)).toHaveLength(1);
    // The memory history reads Prometheus, not jcmd: it is not drawn disabled.
    expect(document.querySelector('[data-jvm-history] button:disabled')).toBeNull();
  });

  it('leaves the buttons active when jcmd is there', async () => {
    stub(() => Promise.resolve(json(200, { jcmd: true })));
    mount();
    await screen.findByRole('button', { name: 'Inspect' });
    await waitFor(() => expect(document.querySelector('[data-jvm-inspect]')).not.toBeNull());
    await new Promise((r) => setTimeout(r, 20));
    for (const b of jcmdButtons()) {
      expect(b?.disabled).toBe(false);
      expect(b?.getAttribute('title')).not.toBe(TITLE);
    }
  });

  it.each([
    ['refused', 403, { code: 'exec_forbidden', message: 'no' }],
    ['failed', 502, { code: 'exec_failed', message: 'boom' }],
  ])('leaves the buttons active when the probe is %s: that is not an absence', async (_n, status, body) => {
    const asked = stub(() => Promise.resolve(json(status, body)));
    mount();
    await screen.findByRole('button', { name: 'Inspect' });
    await waitFor(() => expect(asked).toContain(`GET ${TOOLS}`));
    await new Promise((r) => setTimeout(r, 20));
    for (const b of jcmdButtons()) expect(b?.disabled).toBe(false);
  });

  it('leaves the buttons active while the probe is pending', async () => {
    stub(() => new Promise<Response>(() => undefined));
    mount();
    await screen.findByRole('button', { name: 'Inspect' });
    for (const b of jcmdButtons()) expect(b?.disabled).toBe(false);
  });

  it('greys the buttons when an action itself answers no_jcmd', async () => {
    stub(
      () => Promise.resolve(json(502, { code: 'exec_failed', message: 'boom' })),
      () => json(422, noJcmd),
    );
    mount();
    fireEvent.click(await screen.findByRole('button', { name: 'Inspect' }));
    await waitFor(() => expect(document.querySelector<HTMLButtonElement>('[data-jvm-thread-dump]')?.disabled).toBe(true));
    for (const b of jcmdButtons()) expect(b?.getAttribute('title')).toBe(TITLE);
  });
});
