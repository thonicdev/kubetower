import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { Suspense, type ReactNode } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Container, PodAnswer, Runtime } from './data';
import { runtimeInspector } from './index';
import { tabFor } from './RuntimeTab';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const POD = '/x/kind/runtime/pod/shop/web-0';

function container(name: string, runtime: Runtime | '', hints: Container['hints']): Container {
  return { name, image: `registry.example.com/${name}:1`, runtime, java: runtime === 'java', hints, request: null, limit: null, heap: null, risk: null, notes: [] };
}

const answer: PodAnswer = {
  cluster: 'kind',
  namespace: 'shop',
  name: 'web-0',
  phase: 'Running',
  java: false,
  runtimes: ['node', 'go'],
  containers: [
    container('web', 'node', [
      { source: 'command', reason: 'the command line starts “node”' },
      { source: 'env', reason: 'NODE_ENV is set' },
    ]),
    container('sidecar', 'go', [{ source: 'env', reason: 'GOMEMLIMIT is set: the Go runtime reads it' }]),
    container('admin', 'node', [{ source: 'annotation', reason: 'kubetower.io/runtime names node' }]),
  ],
  owner: null,
};

function stub(body: unknown): string[] {
  const asked: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
      asked.push(`${init?.method ?? 'GET'} ${url}`);
      if (url !== POD) return Promise.resolve(new Response('no stub', { status: 599 }));
      return Promise.resolve(new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } }));
    }),
  );
  return asked;
}

function mount(element: ReactNode): void {
  const router = createMemoryRouter([{ path: '/c/:cluster/*', element: <Suspense>{element}</Suspense> }], { initialEntries: ['/c/kind/pods'] });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

describe('the runtime tabs', () => {
  const object = { cluster: 'kind', kind: 'pods', namespace: 'shop', name: 'web-0' };
  const signal = new AbortController().signal;

  it('declares a tab per runtime, labelled after it, on pods', () => {
    expect(runtimeInspector.tabs?.map((t) => `${t.key}:${t.label}`)).toEqual(['jvm:JVM', 'go:Go', 'node:Node.js', 'python:Python']);
    expect(runtimeInspector.tabs?.every((t) => t.kinds.length === 1 && t.kinds[0] === 'pods')).toBe(true);
    expect(runtimeInspector.columns?.[0]?.label).toBe('Runtime');
  });

  it("draws each tab where the pod runs its runtime, and the probes asked together read the pod once", async () => {
    const asked = stub(answer);
    const tabs = runtimeInspector.tabs ?? [];
    const drawn = await Promise.all(tabs.map((t) => t.probe?.(object, signal)));
    expect(drawn).toEqual([false, true, true, false]);
    expect(asked).toEqual([`GET ${POD}`]);
    await expect(tabs[1]?.probe?.({ ...object, namespace: undefined }, signal)).resolves.toBe(false);
  });

  it('shows the containers of its runtime, a badge, and each hint by its source with the reason on hover', async () => {
    const asked = stub(answer);
    const Node = tabFor('node');
    mount(<Node cluster="kind" kind="pods" namespace="shop" name="web-0" />);
    await screen.findByText('registry.example.com/web:1');
    const hints = document.querySelectorAll('[data-runtime-hints] [data-pill]');
    expect([...hints].map((h) => `${h.textContent ?? ''}|${h.getAttribute('title') ?? ''}`)).toEqual([
      'command|the command line starts “node”',
      'env|NODE_ENV is set',
    ]);
    expect(screen.getByText('Node.js')).toBeDefined();
    // Only its own runtime's containers are offered, and the go sidecar is not.
    const select = screen.getByRole('combobox');
    expect([...(select as HTMLSelectElement).options].map((o) => o.value)).toEqual(['web', 'admin']);
    fireEvent.change(select, { target: { value: 'admin' } });
    expect(document.querySelector('[data-runtime-container="admin"] [data-pill][title="kubetower.io/runtime names node"]')).not.toBeNull();
    // Nothing but the spec and the inspector's probe, a POST since #82.
    expect(asked.every((a) => a.startsWith('GET ') || /^POST \S+\/node$/.test(a))).toBe(true);
  });

  it('draws no picker for a single container', async () => {
    stub(answer);
    const Go = tabFor('go');
    mount(<Go cluster="kind" kind="pods" namespace="shop" name="web-0" />);
    expect(await screen.findByText('sidecar')).toBeDefined();
    expect(screen.queryByRole('combobox')).toBeNull();
  });
});
