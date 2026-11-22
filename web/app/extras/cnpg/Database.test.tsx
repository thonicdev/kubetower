import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { Database } from './Database';
import type { Rows, Stats, Tables } from './data';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type Replies = Record<string, { status?: number; body: unknown }>;

function stub(replies: Replies): string[] {
  const asked: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      asked.push(url);
      const reply = replies[url];
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
  schema: 'public', table: 'orders', verified: false, columnsTotal: 3, more: true,
  columns: [{ name: 'id', type: 'integer' }, { name: 'note', type: 'text' }, { name: 'blob', type: 'bytea' }],
  rows: [[{ text: '1' }, { text: 'a'.repeat(512), cut: true }, { bytes: 2048 }], [{ text: '2' }, { null: true }, { null: true }]],
};

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

  it("draws a table's first rows: cut values, nulls, binary sizes", async () => {
    stub({ [`${BASE}/stats`]: { body: STATS }, [`${BASE}/tables`]: { body: TABLES }, [`${BASE}/tables/public/orders`]: { body: ROWS } });
    mount();
    fireEvent.click(screen.getByRole('button', { name: 'Connect' }));
    fireEvent.click(await waitFor(() => {
      const found = document.querySelector('[data-table="public.orders"]');
      expect(found).not.toBeNull();
      return found as Element;
    }));
    await waitFor(() => expect(document.querySelector('[data-rows="public.orders"]')).not.toBeNull());
    const cells = [...document.querySelectorAll('[data-rows] tbody tr:first-child td')].map((td) => td.textContent);
    expect(cells).toEqual(['1', `${'a'.repeat(512)}…`, '2.0 KiB']);
    expect(document.querySelector('[data-rows] [data-cut]')?.getAttribute('title')).toBe(`${'a'.repeat(512)}…`);
    expect(document.querySelectorAll('[data-rows] tbody tr:nth-child(2) td')[1]?.textContent).toBe('null');
    expect(screen.getByText('first 2 rows')).toBeTruthy();
  });

  it("says a Secret the person may not read as a refusal, not a failure", async () => {
    const refused = { status: 403, body: { code: 'secret_denied', message: 'You may not read the Secret db/pg-app that holds the cluster’s password.' } };
    stub({ [`${BASE}/stats`]: refused, [`${BASE}/tables`]: refused });
    mount();
    fireEvent.click(screen.getByRole('button', { name: 'Connect' }));
    await waitFor(() => expect(document.querySelectorAll('[data-state="refused"][data-code="secret_denied"]')).toHaveLength(2));
  });
});
