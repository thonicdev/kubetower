import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Container, Inspection, Inventory, PodAnswer } from './data';
import { inventoryPath } from './data';
import { jvm } from './index';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

/** The page's side panel: a click on a pod opens that pod's JVM tab. */

const app: Container = {
  name: 'app',
  image: 'eclipse-temurin:21-jre',
  java: true,
  hints: [{ source: 'image', reason: 'a Java image' }],
  request: null,
  limit: { bytes: 1073741824, text: '1Gi' },
  heap: { bytes: 536870912, text: '512.0 MiB', from: 'explicit', source: '-Xmx512m' },
  risk: { level: 'ok', percent: 50, sentence: 'room' },
  notes: [],
  workingSet: null,
};

const pods = ['billing-0', 'api-1'].map((name) => ({
  namespace: 'shop',
  name,
  phase: 'Running',
  java: true,
  containers: [app],
}));

const inventory: Inventory = { cluster: 'kind', namespace: '', complete: true, read: 2, pods };

const podAnswer = (name: string): PodAnswer => ({ cluster: 'kind', namespace: 'shop', name, phase: 'Running', java: true, containers: [app] });

const inspection = (sections: NonNullable<Inspection['sections']>): Inspection => ({
  cluster: 'kind',
  namespace: 'shop',
  pod: 'api-1',
  container: 'app',
  state: 'inspected',
  pid: 1,
  pidFrom: 'listed',
  sections,
  notes: [],
});

function stub(inspect: { status: number; body: unknown } = { status: 200, body: inspection([]) }): string[] {
  const asked: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      asked.push(`${init?.method ?? 'GET'} ${url}`);
      let reply: { status: number; body: unknown } = { status: 599, body: `no stub for ${url}` };
      if (url === inventoryPath('kind', '')) reply = { status: 200, body: inventory };
      else if (url.endsWith('/inspect')) reply = inspect;
      else if (url.endsWith('/tools')) reply = { status: 200, body: { jcmd: true } };
      else {
        const m = /\/jvm\/pod\/shop\/([^/]+)$/.exec(url);
        if (m?.[1] !== undefined) reply = { status: 200, body: podAnswer(m[1]) };
      }
      return Promise.resolve(new Response(JSON.stringify(reply.body), { status: reply.status, headers: { 'Content-Type': 'application/json' } }));
    }),
  );
  return asked;
}

function mount(): void {
  const router = createMemoryRouter([{ path: 'c/:cluster', children: jvm.routes }], { initialEntries: ['/c/kind/x/jvm'] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

const row = (name: string) => document.querySelector(`[data-jvm-container="shop/${name}/app"]`) as HTMLElement;

describe('the pod panel', () => {
  it('opens the JVM tab of the clicked pod, with a brief line and no picture before Inspect', async () => {
    const asked = stub();
    mount();
    await screen.findByText('api-1');
    expect(document.querySelector('[data-jvm-panel]')).toBeNull();
    fireEvent.click(row('api-1'));
    const panel = await waitFor(() => {
      const found = document.querySelector('[data-jvm-panel]');
      if (found === null) throw new Error('no panel');
      return found;
    });
    expect(panel.getAttribute('data-jvm-panel')).toBe('shop/api-1');
    await waitFor(() => expect(panel.querySelector('[data-jvm-inspect]')).not.toBeNull());
    expect(panel.querySelector('[data-jvm-hint]')?.textContent).toBe('Inspect → memory, GC and threads');
    expect(panel.querySelector('img')).toBeNull();
    expect(document.querySelector('[data-jvm-preview]')).toBeNull();
    expect(asked).toContain('GET /x/kind/jvm/pod/shop/api-1');
    expect(asked.some((u) => u.includes('/inspect'))).toBe(false);
    expect(asked.some((u) => u.includes('billing-0') && u.includes('/pod/'))).toBe(false);
  });

  it('stops reading once closed, and closes on Escape', async () => {
    const asked = stub();
    mount();
    await screen.findByText('billing-0');
    fireEvent.click(row('billing-0'));
    await waitFor(() => expect(document.querySelector('[data-jvm-inspect]')).not.toBeNull());
    fireEvent.click(document.querySelector('[data-jvm-panel-close]') as HTMLElement);
    expect(document.querySelector('[data-jvm-panel]')).toBeNull();
    const after = asked.length;
    fireEvent.click(row('api-1'));
    await waitFor(() => expect(document.querySelector('[data-jvm-inspect]')).not.toBeNull());
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(document.querySelector('[data-jvm-panel]')).toBeNull();
    expect(asked.slice(after).every((u) => u.includes('api-1'))).toBe(true);
  });

  it('says in a short line to add a JDK image when there is no jcmd', async () => {
    stub({ status: 422, body: { code: 'no_jcmd', message: 'There is no jcmd in this image: a JRE or a distroless image carries none.' } });
    mount();
    await screen.findByText('api-1');
    fireEvent.click(row('api-1'));
    fireEvent.click(await waitFor(() => {
      const b = document.querySelector('[data-jvm-inspect]');
      if (b === null) throw new Error('no button');
      return b;
    }));
    const failure = await waitFor(() => {
      const f = document.querySelector('[data-jvm-failure="no_jcmd"]');
      if (f === null) throw new Error('no failure');
      return f;
    });
    expect(failure.textContent).toBe('No jcmd: use a JDK image');
    expect(failure.querySelector('p')?.getAttribute('title')).toMatch(/JRE or a distroless/);
  });

  it('says in a short line to add the NMT flag when it is off', async () => {
    stub({
      status: 200,
      body: inspection([{ command: 'VM.native_memory summary', state: 'not-enabled', sentence: 'Native memory tracking is not enabled in this JVM.' }]),
    });
    mount();
    await screen.findByText('api-1');
    fireEvent.click(row('api-1'));
    fireEvent.click(await waitFor(() => {
      const b = document.querySelector('[data-jvm-inspect]');
      if (b === null) throw new Error('no button');
      return b;
    }));
    const line = await waitFor(() => {
      const f = document.querySelector('[data-jvm-section="not-enabled"]');
      if (f === null) throw new Error('no line');
      return f;
    });
    expect(line.textContent).toContain('NMT off: add -XX:NativeMemoryTracking=summary to JAVA_TOOL_OPTIONS');
    expect(line.getAttribute('title')).toMatch(/not enabled/);
  });
});
