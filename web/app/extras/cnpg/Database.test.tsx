import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { Database } from './Database';
import type { Rows, Stats, Tables } from './data';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type Replies = Record<string, { status?: number; body: unknown } | ((sent: { method: string; body: string }) => { status?: number; body: unknown })>;

/** What each request sent: its method and its body. */
const sent: { url: string; method: string; body: string }[] = [];

function stub(replies: Replies): string[] {
  const asked: string[] = [];
  sent.length = 0;
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      const one = { url, method: (init?.method ?? 'GET').toUpperCase(), body: typeof init?.body === 'string' ? init.body : '' };
      asked.push(url);
      sent.push(one);
      const answer = replies[url];
      const reply = typeof answer === 'function' ? answer(one) : answer;
      if (reply === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
      return Promise.resolve(new Response(JSON.stringify(reply.body), { status: reply.status ?? 200, headers: { 'Content-Type': 'application/json' } }));
    }),
  );
  return asked;
}

function mount(): void {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <Database cluster="kind" namespace="db" name="pg" />
    </QueryClientProvider>,
  );
}

const BASE = '/x/kind/cnpg/clusters/db/pg';
const STATS: Stats = {
  database: 'shop', verified: false, version: '17.2', connections: 12, maxConnections: 200, cacheHit: 0.99, commits: 5000, rollbacks: 7, deadlocks: 0,
  databases: [{ name: 'shop', bytes: 104857600 }, { name: 'postgres', bytes: 8000000 }],
  replicas: [{ name: 'pg-2', state: 'streaming' }, { name: 'pg-3', state: 'streaming', lagBytes: 2048 }],
};
const TABLES: Tables = {
  database: 'shop', verified: false, schemas: ['public'], complete: true,
  tables: [{ schema: 'public', name: 'orders', kind: 'table', rows: 1200, bytes: 245760 }, { schema: 'public', name: 'v_month', kind: 'view', rows: -1, bytes: 0 }],
};
const ROWS: Rows = {
  schema: 'public', table: 'orders', verified: false, columnsTotal: 5, more: true, key: 'primary', next: 'next-1', start: 0,
  columns: [{ name: 'id', type: 'integer', primary: true }, { name: 'note', type: 'text' }, { name: 'blob', type: 'bytea' }, { name: 'paid', type: 'boolean' }, { name: 'doc', type: 'jsonb' }],
  rows: [
    [{ text: '1' }, { text: 'a'.repeat(512), cut: true }, { bytes: 2048, hex: '00ff' }, { text: 't' }, { text: '{"a":{"b":1}}' }],
    [{ text: '2' }, { null: true }, { null: true }, { text: 'f' }, { null: true }],
  ],
};
const PAGE_2: Rows = { ...withoutNext(ROWS), more: false, start: 100, rows: [[{ text: '101' }, { text: 'z' }, { null: true }, { text: 't' }, { null: true }]] };

function withoutNext(rows: Rows): Rows {
  const copy = { ...rows };
  delete copy.next;
  return copy;
}

async function openOrders(): Promise<void> {
  fireEvent.click(screen.getByRole('button', { name: 'Connect' }));
  fireEvent.click(await waitFor(() => {
    const found = document.querySelector('[data-table="public.orders"]');
    expect(found).not.toBeNull();
    return found as Element;
  }));
  await waitFor(() => expect(document.querySelector('[data-rows="public.orders"] tbody')).not.toBeNull());
}

describe("a CNPG cluster's database", () => {
  it('reads nothing until Connect, then the statistics and the tables, once', async () => {
    const asked = stub({ [`${BASE}/stats`]: { body: STATS }, [`${BASE}/tables`]: { body: TABLES } });
    mount();
    expect(document.querySelector('[data-database="off"]')).not.toBeNull();
    expect(asked).toHaveLength(0);
    fireEvent.click(screen.getByRole('button', { name: 'Connect' }));
    await waitFor(() => expect(document.querySelector('[data-figure="Connections"]')?.textContent).toBe('Connections12 / 200'));
    expect(document.querySelector('[data-figure="Cache hit"]')?.textContent).toBe('Cache hit99.0%');
    expect(document.querySelector('[data-db="shop"]')?.textContent).toContain('100 MiB');
    expect(document.querySelector('[data-replica="pg-2"]')?.textContent).toContain('—');
    expect(document.querySelector('[data-replica="pg-3"]')?.textContent).toContain('2.0 KiB');
    expect(screen.getByText('TLS unverified')).toBeTruthy();
    await waitFor(() => expect(document.querySelector('[data-table="public.orders"]')).not.toBeNull());
    expect(document.querySelector('[data-table="public.v_month"]')?.textContent).toContain('view');
    expect(asked.filter((u) => u.startsWith(BASE))).toEqual([`${BASE}/stats`, `${BASE}/tables`]);
  });

  it("draws a table's rows for their types: cut values, nulls, bytes, booleans, its key", async () => {
    stub({ [`${BASE}/stats`]: { body: STATS }, [`${BASE}/tables`]: { body: TABLES }, [`${BASE}/tables/public/orders`]: { body: ROWS } });
    mount();
    await openOrders();
    const cells = [...document.querySelectorAll('[data-rows] tbody tr:first-child td')].map((td) => td.textContent);
    expect(cells).toEqual(['1', `${'a'.repeat(512)}…`, '2.0 KiB 00ff', 'true', '{"a":{"b":1}}']);
    expect(document.querySelectorAll('[data-rows] tbody tr:nth-child(2) td')[1]?.textContent).toBe('null');
    expect(document.querySelectorAll('[data-rows] tbody tr:nth-child(2) td')[3]?.textContent).toBe('false');
    expect(document.querySelector('[data-rows] thead th')?.textContent).toContain('key');
    expect(screen.getByText('by key')).toBeTruthy();
    expect(screen.getByText('1–2')).toBeTruthy();
  });

  it('asks the next page with its cursor in a POST body, never in the address, and goes back', async () => {
    stub({
      [`${BASE}/stats`]: { body: STATS },
      [`${BASE}/tables`]: { body: TABLES },
      [`${BASE}/tables/public/orders`]: ({ body }) => ({ body: (JSON.parse(body) as { cursor: string }).cursor === 'next-1' ? PAGE_2 : ROWS }),
    });
    mount();
    await openOrders();
    const first = sent.find((s) => s.url === `${BASE}/tables/public/orders`);
    expect(first?.method).toBe('POST');
    expect(JSON.parse(first?.body ?? '{}')).toEqual({ database: 'shop', cursor: '' });
    fireEvent.click(screen.getByRole('button', { name: 'Next page' }));
    await waitFor(() => expect(screen.getByText('101–101')).toBeTruthy());
    const pages = sent.filter((s) => s.url.startsWith(`${BASE}/tables/public/orders`));
    expect(pages.map((s) => s.url)).toEqual([`${BASE}/tables/public/orders`, `${BASE}/tables/public/orders`]);
    expect(JSON.parse(pages[1]?.body ?? '{}')).toEqual({ database: 'shop', cursor: 'next-1' });
    expect((screen.getByRole('button', { name: 'Next page' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole('button', { name: 'Previous page' }));
    await waitFor(() => expect(screen.getByText('1–2')).toBeTruthy());
  });

  it('opens a cell in full, JSON laid out', async () => {
    stub({ [`${BASE}/stats`]: { body: STATS }, [`${BASE}/tables`]: { body: TABLES }, [`${BASE}/tables/public/orders`]: { body: ROWS } });
    mount();
    await openOrders();
    fireEvent.click(document.querySelector('[data-cell="0:4"]') as Element);
    expect(document.querySelector('[data-cell-detail] pre')?.textContent).toBe('{\n  "a": {\n    "b": 1\n  }\n}');
    fireEvent.click(document.querySelector('[data-cell="0:1"]') as Element);
    expect(document.querySelector('[data-cell-detail]')?.textContent).toContain('cut');
    fireEvent.click(screen.getByRole('button', { name: 'Close the value' }));
    expect(document.querySelector('[data-cell-detail]')).toBeNull();
  });

  it('reads another database the user may open from its own list', async () => {
    const other: Tables = { ...TABLES, database: 'reporting', tables: [{ schema: 'public', name: 'daily', kind: 'table', rows: 10, bytes: 8192 }] };
    stub({ [`${BASE}/stats`]: { body: STATS }, [`${BASE}/tables`]: { body: { ...TABLES, databases: ['shop', 'reporting'] } }, [`${BASE}/tables?db=reporting`]: { body: other } });
    mount();
    fireEvent.click(screen.getByRole('button', { name: 'Connect' }));
    const pick = await waitFor(() => screen.getByRole('combobox', { name: 'Database' }));
    fireEvent.change(pick, { target: { value: 'reporting' } });
    await waitFor(() => expect(document.querySelector('[data-table="public.daily"]')).not.toBeNull());
    expect(document.querySelector('[data-table="public.orders"]')).toBeNull();
  });

  it("says a Secret the person may not read as a refusal, not a failure", async () => {
    const refused = { status: 403, body: { code: 'secret_denied', message: 'You may not read the Secret db/pg-app that holds the cluster’s password.' } };
    stub({ [`${BASE}/stats`]: refused, [`${BASE}/tables`]: refused });
    mount();
    fireEvent.click(screen.getByRole('button', { name: 'Connect' }));
    await waitFor(() => expect(document.querySelectorAll('[data-state="refused"][data-code="secret_denied"]')).toHaveLength(2));
  });
});
