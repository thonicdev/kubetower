import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { PythonPanel } from './PythonPanel';

const BASE = '/x/kind/runtime/pod/shop/py-0/app/python';

interface Asked {
  call: string;
  marked: boolean;
}

function stub(replies: Record<string, { status?: number; body: unknown }>): Asked[] {
  const asked: Asked[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      const call = `${init?.method ?? 'GET'} ${url}`;
      asked.push({ call, marked: new Headers(init?.headers).get('X-KubeTower-Request') === '1' });
      const reply = replies[call];
      if (reply === undefined) return Promise.resolve(new Response(`no stub for ${call}`, { status: 599 }));
      const problem = (reply.status ?? 200) >= 400;
      return Promise.resolve(
        new Response(JSON.stringify(reply.body), {
          status: reply.status ?? 200,
          headers: { 'Content-Type': problem ? 'application/problem+json' : 'application/json' },
        }),
      );
    }),
  );
  return asked;
}

function mount(): HTMLElement {
  const { container } = render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <PythonPanel cluster="kind" namespace="shop" name="py-0" container="app" />
    </QueryClientProvider>,
  );
  return container;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const processes = {
  reader: 'sh',
  others: 1,
  truncated: false,
  processes: [
    { pid: 1, ppid: 0, comm: 'gunicorn', executable: '/usr/local/bin/python3.12', version: '3.12', rss: 43008000, peak: 46137344,
      threads: 1, threadNames: ['gunicorn'], started: '2026-03-09T06:12:44Z' },
    { pid: 8, ppid: 1, comm: 'gunicorn', executable: '/usr/local/bin/python3.12', version: '3.12', rss: 195035136, peak: 242221056,
      threads: 3, threadNames: ['gunicorn', 'gunicorn', 'gunicorn'] },
  ],
};

const metrics = {
  port: 8000,
  source: 'port',
  reachable: true,
  metrics: {
    version: '3.12.16',
    implementation: 'CPython',
    gc: [
      { generation: '0', collections: 31, collected: 240, uncollectable: 0 },
      { generation: '2', collections: 0, collected: 0, uncollectable: 12 },
    ],
    rss: 25620480,
    virtual: null,
    cpuSeconds: 0.36,
    openFds: 6,
    maxFds: 1048576,
    startTime: null,
  },
};

describe('the Python panel', () => {
  it('reads the processes and the metrics once, both by POST with the request marker', async () => {
    const asked = stub({ [`POST ${BASE}`]: { body: processes }, [`POST ${BASE}/metrics`]: { body: metrics } });
    const view = mount();

    await waitFor(() => {
      expect(view.querySelectorAll('[data-python-pid]')).toHaveLength(2);
      expect(view.querySelector('[data-python-metrics-read]')).not.toBeNull();
    });
    expect(asked.map((a) => a.call).sort()).toEqual([`POST ${BASE}`, `POST ${BASE}/metrics`]);
    expect(asked.every((a) => a.marked)).toBe(true);

    const first = view.querySelector('[data-python-pid="1"]') as HTMLElement;
    expect(first.textContent).toContain('3.12');
    expect(first.textContent).toContain('gunicorn');
    expect(first.querySelector('[title="/usr/local/bin/python3.12"]')).not.toBeNull();
    // The thread names, grouped, are in the tooltip.
    expect(view.querySelector('[data-python-pid="8"] [title="gunicorn ×3"]')).not.toBeNull();
    // A process with no start time shows a dash, not an invented one.
    expect(view.querySelector('[data-python-pid="8"]')?.textContent).toContain('—');
    expect(view.textContent).toContain('+1 other');

    expect(view.textContent).toContain('CPython 3.12.16');
    expect(view.textContent).toContain('Open files');
    // A series not served is left out, never drawn as zero.
    expect(view.textContent).not.toContain('Virtual');
    expect(view.querySelector('[data-python-gc-generation="2"]')?.textContent).toContain('12');
  });

  it('says there is no Python process in one line', async () => {
    stub({
      [`POST ${BASE}`]: { body: { reader: 'python3', others: 2, truncated: false, processes: [] } },
      [`POST ${BASE}/metrics`]: { body: metrics },
    });
    const view = mount();
    await waitFor(() => {
      expect(view.querySelector('[data-python-none]')).not.toBeNull();
    });
    expect(view.querySelector('[data-python-processes] table')).toBeNull();
  });

  it('draws metrics that are not served as such, with the reason on hover', async () => {
    stub({
      [`POST ${BASE}`]: { body: processes },
      [`POST ${BASE}/metrics`]: {
        body: { port: 8000, source: 'default', reachable: false, detail: 'connection refused', metrics: null },
      },
    });
    const view = mount();
    await waitFor(() => {
      expect(view.querySelector('[data-python-metrics-unreachable]')).not.toBeNull();
    });
    expect(view.querySelector('[data-python-metrics-unreachable] [title="connection refused"]')).not.toBeNull();
    expect(view.querySelector('[data-python-metrics-read]')).toBeNull();
    // The processes still read: one failing half does not blank the other.
    expect(view.querySelectorAll('[data-python-pid]')).toHaveLength(2);
  });

  it('shows a refusal of the process read as a failure, and Refresh asks again', async () => {
    const asked = stub({
      [`POST ${BASE}`]: { status: 403, body: { code: 'read_only', message: 'kind is read-only: no command, tunnel or dump.' } },
      [`POST ${BASE}/metrics`]: { body: metrics },
    });
    const view = mount();
    await waitFor(() => {
      expect(view.querySelector('[data-python-processes]')?.textContent).toContain('read-only');
    });
    const refresh = [...view.querySelectorAll('[data-python-processes] button')].find((b) => b.textContent === 'Refresh') as HTMLButtonElement;
    fireEvent.click(refresh);
    await waitFor(() => {
      expect(asked.filter((a) => a.call === `POST ${BASE}`)).toHaveLength(2);
    });
  });
});
