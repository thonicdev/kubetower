import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { pprofQuery } from './data';
import { GoPprof } from './GoPprof';

const BASE = '/x/kind/runtime/pod/shop/api-0/app/pprof';

interface Asked {
  call: string;
  marked: boolean;
}

function stub(replies: Record<string, { status?: number; body: unknown; raw?: boolean }>): Asked[] {
  const asked: Asked[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      const call = `${init?.method ?? 'GET'} ${url}`;
      asked.push({ call, marked: new Headers(init?.headers).get('X-KubeTower-Request') === '1' });
      const reply = replies[call];
      if (reply === undefined) return Promise.resolve(new Response(`no stub for ${call}`, { status: 599 }));
      if (reply.raw === true) {
        return Promise.resolve(
          new Response(String(reply.body), {
            status: reply.status ?? 200,
            headers: { 'Content-Type': 'application/octet-stream', 'Content-Disposition': 'attachment; filename="api-0-app.cpu.pb.gz"' },
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

const pane = <GoPprof cluster="kind" namespace="shop" name="api-0" container="app" />;

const dump = {
  total: 4,
  truncated: false,
  raw: 'goroutine 1 [running]:\nmain.main()\n',
  groups: [
    { state: 'chan receive', count: 2, frames: ['main.worker', 'created by main.main'], longest: '12 minutes' },
    { state: 'running', count: 1, frames: ['main.main'] },
    { state: 'select', count: 1, frames: ['net/http.(*persistConn).writeLoop'] },
  ],
};

beforeEach(() => {
  vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:x'), revokeObjectURL: vi.fn() }));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("the Go tab's pprof", () => {
  it('reads the goroutines on a click, grouped, filterable, and posts with the request header', async () => {
    const asked = stub({
      [`GET ${BASE}`]: { body: { port: 6060, source: 'default', reachable: true } },
      [`POST ${BASE}/goroutines`]: { body: dump },
    });
    mount(pane);
    const button = await screen.findByRole('button', { name: 'Goroutines' });
    expect(screen.getByText('pprof :6060').getAttribute('title')).toMatch(/convention/);
    expect(asked.map((a) => a.call)).toEqual([`GET ${BASE}`]);
    fireEvent.click(button);
    await screen.findByText('4 goroutines');
    expect(document.querySelectorAll('[data-goroutine-group]')).toHaveLength(3);
    expect(asked.find((a) => a.call.startsWith('POST'))?.marked).toBe(true);
    fireEvent.change(screen.getByRole('searchbox', { name: 'Filter goroutines' }), { target: { value: 'worker' } });
    expect(document.querySelectorAll('[data-goroutine-group]')).toHaveLength(1);
    expect(screen.getByText('12 minutes')).toBeDefined();
  });

  it('downloads a CPU profile for the seconds picked', async () => {
    const asked = stub({
      [`GET ${BASE}`]: { body: { port: 6061, source: 'port', reachable: true } },
      [`POST ${BASE}/profile?seconds=60`]: { body: 'PROFILE', raw: true },
    });
    mount(pane);
    await screen.findByRole('button', { name: 'CPU profile' });
    fireEvent.change(screen.getByRole('combobox', { name: 'CPU profile seconds' }), { target: { value: '60' } });
    fireEvent.click(screen.getByRole('button', { name: 'CPU profile' }));
    await waitFor(() => {
      expect(asked.some((a) => a.call === `POST ${BASE}/profile?seconds=60` && a.marked)).toBe(true);
    });
    await waitFor(() => {
      expect(URL.createObjectURL).toHaveBeenCalled();
    });
  });

  it('greys every action where pprof does not answer, with why on hover', async () => {
    stub({ [`GET ${BASE}`]: { body: { port: 6060, source: 'default', reachable: false, detail: '/debug/pprof/ answered 404 Not Found' } } });
    mount(pane);
    const goroutinesButton = await screen.findByRole('button', { name: 'Goroutines' });
    for (const name of ['Goroutines', 'Heap profile', 'CPU profile', 'Trace']) {
      expect((screen.getByRole('button', { name }) as HTMLButtonElement).disabled).toBe(true);
    }
    expect(goroutinesButton).toBeDefined();
    expect(document.querySelector('[data-pprof-unreachable] [title]')?.getAttribute('title')).toMatch(/404/);
  });

  it('says why the state could not be read', async () => {
    stub({ [`GET ${BASE}`]: { status: 403, body: { code: 'tunnel_forbidden', message: 'The cluster says you may not port-forward to this pod, so pprof is not reached.' } } });
    mount(pane);
    expect(await screen.findByText(/may not port-forward/)).toBeDefined();
  });
});

describe('the pprof probe', () => {
  it('opens its tunnel once, not again when the window regains focus', () => {
    const options = pprofQuery('kind', 'shop', 'api-0', 'app');
    expect(options.refetchOnWindowFocus).toBe(false);
    expect('refetchInterval' in options).toBe(false);
  });
});
