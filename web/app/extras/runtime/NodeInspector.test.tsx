import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { nodeQuery } from './data';
import { NodeInspector } from './NodeInspector';

const BASE = '/x/kind/runtime/pod/shop/web-0/app/node';

interface Asked {
  call: string;
  marked: boolean;
}

type Reply = { status?: number; body: unknown; raw?: boolean };

function stub(replies: Record<string, Reply | Reply[]>): Asked[] {
  const asked: Asked[] = [];
  const seen: Record<string, number> = {};
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      const call = `${init?.method ?? 'GET'} ${url}`;
      asked.push({ call, marked: new Headers(init?.headers).get('X-KubeTower-Request') === '1' });
      const entry = replies[call];
      const n = seen[call] ?? 0;
      seen[call] = n + 1;
      const reply = Array.isArray(entry) ? entry[Math.min(n, entry.length - 1)] : entry;
      if (reply === undefined) return Promise.resolve(new Response(`no stub for ${call}`, { status: 599 }));
      if (reply.raw === true) {
        return Promise.resolve(
          new Response(String(reply.body), {
            status: reply.status ?? 200,
            headers: { 'Content-Type': 'application/octet-stream', 'Content-Disposition': 'attachment; filename="web-0-app.heapsnapshot"' },
          }),
        );
      }
      return Promise.resolve(new Response(JSON.stringify(reply.body), { status: reply.status ?? 200, headers: { 'Content-Type': 'application/json' } }));
    }),
  );
  return asked;
}

function mount(element: ReactNode): void {
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{element}</QueryClientProvider>);
}

const pane = <NodeInspector cluster="kind" namespace="shop" name="web-0" container="app" />;
const closed = { port: 9229, source: 'default', started: false, sigusr1Disabled: false, open: false, detail: '/json/list answered 404 Not Found' };
const open = { ...closed, open: true, detail: undefined };

beforeEach(() => {
  vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:x'), revokeObjectURL: vi.fn() }));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("the Node.js tab's inspector", () => {
  it('opens the inspector only after a confirmation, with the risk on the button, and posts with the request header', async () => {
    const asked = stub({
      [`GET ${BASE}`]: { body: closed },
      [`POST ${BASE}/inspector?confirm=confirm`]: { body: { ...open, pid: 7 } },
    });
    mount(pane);
    const button = await screen.findByRole('button', { name: 'Open inspector' });
    expect(button.getAttribute('title')).toMatch(/until the process restarts/);
    for (const name of ['Heap snapshot', 'CPU profile', 'Diagnostic report']) {
      expect((screen.getByRole('button', { name }) as HTMLButtonElement).disabled).toBe(true);
    }
    fireEvent.click(button);
    expect(asked.filter((a) => a.call.startsWith('POST'))).toEqual([]);
    expect(screen.getByText('until restart')).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    await waitFor(() => {
      expect(document.querySelector('[data-node-state="open"]')).not.toBeNull();
    });
    expect(asked.find((a) => a.call.startsWith('POST'))?.marked).toBe(true);
    expect((screen.getByRole('button', { name: 'Diagnostic report' }) as HTMLButtonElement).disabled).toBe(false);
  });

  it('asks which process when there are several', async () => {
    stub({
      [`GET ${BASE}`]: { body: closed },
      [`POST ${BASE}/inspector?confirm=confirm`]: { status: 409, body: { code: 'choose_process', pids: [7, 12], message: 'Choose' } },
      [`POST ${BASE}/inspector?confirm=confirm&pid=12`]: { body: { ...open, pid: 12 } },
    });
    mount(pane);
    fireEvent.click(await screen.findByRole('button', { name: 'Open inspector' }));
    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    fireEvent.click(await screen.findByRole('button', { name: 'pid 12' }));
    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    await waitFor(() => {
      expect(document.querySelector('[data-node-state="open"]')).not.toBeNull();
    });
  });

  it('cannot open a process started with --disable-sigusr1', async () => {
    stub({ [`GET ${BASE}`]: { body: { ...closed, sigusr1Disabled: true } } });
    mount(pane);
    expect(((await screen.findByRole('button', { name: 'Open inspector' })) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText('--disable-sigusr1')).toBeDefined();
  });

  it('reads the report, says how many values were masked, and takes a snapshot only once confirmed', async () => {
    const asked = stub({
      [`GET ${BASE}`]: { body: open },
      [`POST ${BASE}/report`]: {
        body: {
          masked: 2,
          report: { header: { nodejsVersion: 'v22.11.0' }, javascriptHeap: { usedMemory: 1024 }, environmentVariables: { DB_PASSWORD: '••••••' } },
        },
      },
      [`POST ${BASE}/heapsnapshot?confirm=confirm`]: { body: '{"snapshot":{}}', raw: true },
    });
    mount(pane);
    fireEvent.click(await screen.findByRole('button', { name: 'Diagnostic report' }));
    expect(await screen.findByText('Node.js v22.11.0')).toBeDefined();
    expect(screen.getByText('2 masked')).toBeDefined();

    fireEvent.click(screen.getByRole('button', { name: 'Heap snapshot' }));
    expect(asked.some((a) => a.call.includes('heapsnapshot'))).toBe(false);
    expect(screen.getByText('memory ×2')).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: 'Snapshot and download' }));
    await waitFor(() => {
      expect(URL.createObjectURL).toHaveBeenCalled();
    });
  });
});

describe('the Node.js probe', () => {
  it('opens its tunnel once, not again when the window regains focus', () => {
    const options = nodeQuery('kind', 'shop', 'web-0', 'app');
    expect(options.refetchOnWindowFocus).toBe(false);
    expect('refetchInterval' in options).toBe(false);
  });
});
