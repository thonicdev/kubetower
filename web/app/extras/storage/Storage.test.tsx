import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import type { ReactNode } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  STORAGE_BARE,
  STORAGE_CLAIM,
  STORAGE_OVERVIEW,
  STORAGE_POD,
  STORAGE_SNAPSHOTS_REFUSED,
} from '../../../measure/storageinspector.fixture';
import { bytes, passes, type Claim } from './data';
import ClaimTab from './ClaimTab';
import { storage } from './index';
import PodTab from './PodTab';
import { storageTile } from './tile';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type Reply = { status?: number; body: unknown; contentType?: string };

function stub(replies: Record<string, Reply>): string[] {
  const asked: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      asked.push(url);
      const reply = replies[url];
      if (reply === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
      const body = typeof reply.body === 'string' ? reply.body : JSON.stringify(reply.body);
      return Promise.resolve(new Response(body, { status: reply.status ?? 200, headers: { 'Content-Type': reply.contentType ?? 'application/json' } }));
    }),
  );
  return asked;
}

function withClient(ui: ReactNode, path = '/c/prod'): void {
  const router = createMemoryRouter([{ path: '*', element: ui }], { initialEntries: [path] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

function mount(path = 'x/storage'): void {
  const router = createMemoryRouter([{ path: 'c/:cluster', children: storage.routes }], { initialEntries: [`/c/prod/${path}`] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

const ROUTE = '/x/prod/storage/';

function row(claim: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`[data-claim="${claim}"]`);
  if (el === null) throw new Error(`no row ${claim}`);
  return el;
}

describe('the Storage Inspector page', () => {
  it('draws the tiles from the counts and the claims fullest first', async () => {
    stub({ [ROUTE]: { body: STORAGE_OVERVIEW } });
    mount();
    await screen.findByRole('table');
    const c = STORAGE_OVERVIEW.counts;
    expect(document.querySelector('[data-filter="orphans"]')?.getAttribute('data-count')).toBe(String(c.orphans));
    expect(document.querySelector('[data-filter="full"]')?.getAttribute('data-count')).toBe(String(c.warn + c.bad));
    expect(document.querySelector('[data-filter="full"]')?.getAttribute('data-tone')).toBe('bad');
    expect(document.querySelectorAll('[data-claim]')[0]?.getAttribute('data-claim')).toBe('shop/data-orders-db-0');
  });

  it('colours a fill green under 80 %, amber from 80 and red from 90', async () => {
    stub({ [ROUTE]: { body: STORAGE_OVERVIEW } });
    mount();
    await screen.findByRole('table');
    expect(row('shop/data-orders-db-0').querySelector('[data-fill]')?.getAttribute('data-level')).toBe('bad');
    expect(row('search/data-index-1').querySelector('[data-fill]')?.getAttribute('data-level')).toBe('warn');
    expect(row('billing/ledger').querySelector('[data-fill]')?.getAttribute('data-level')).toBe('ok');
    expect(row('shop/data-orders-db-0').querySelector('.bg-bad')).not.toBeNull();
    expect(row('search/data-index-1').querySelector('.bg-warn')).not.toBeNull();
    expect(row('billing/ledger').querySelector('.bg-ok')).not.toBeNull();
    expect(within(row('shop/data-orders-db-0')).getByText('92%')).toBeTruthy();
  });

  it('never draws a percentage for a claim with no figure', async () => {
    stub({ [ROUTE]: { body: STORAGE_BARE } });
    mount();
    await screen.findByRole('table');
    for (const el of document.querySelectorAll('[data-claim]')) {
      expect(el.querySelector('[role="meter"]')).toBeNull();
      expect(el.textContent).not.toMatch(/\b0%/);
    }
    expect(row('billing/reports').querySelector('[data-fill]')?.getAttribute('data-fill')).toBe('unread');
    expect(row('shop/legacy-uploads').querySelector('[data-fill]')?.getAttribute('data-fill')).toBe('unmounted');
  });

  it('names what gave the fill figures as one badge (#91)', async () => {
    stub({ [ROUTE]: { body: STORAGE_OVERVIEW } });
    mount();
    await screen.findByRole('table');
    const via = document.querySelector('[data-glance="fill"] [data-fill-via]');
    expect(via?.getAttribute('data-fill-via')).toBe('victoriametrics,kubelet');
    expect(via?.textContent).toBe('VictoriaMetrics + kubelet');
    expect(via?.querySelector('[title]')?.getAttribute('title')).toBe('Fill read from VictoriaMetrics and kubelet');
    cleanup();
    // Nothing gave a figure: no badge.
    stub({ [ROUTE]: { body: STORAGE_BARE } });
    mount();
    await screen.findByText('No metrics available');
    expect(document.querySelector('[data-glance="fill"]')).toBeNull();
  });

  it('says No metrics available when no fill could be read, what grants it on hover', async () => {
    stub({ [ROUTE]: { body: STORAGE_BARE } });
    mount();
    const line = await screen.findByText('No metrics available');
    expect(line.closest('[data-source]')?.getAttribute('data-source')).toBe('unreadable');
    expect(line.getAttribute('title')).toContain('Fill unreadable · needs nodes/proxy (chart: rbac.nodeProxy)');
    // Never the red sentence of before #91.
    expect(screen.queryByText('Fill unreadable · needs nodes/proxy (chart: rbac.nodeProxy)')).toBeNull();
    // How many are full is unknown, never 0.
    expect(document.querySelector('[data-filter="full"]')?.getAttribute('data-count')).toBe('-1');
    expect(document.querySelector('[data-filter="full"]')?.textContent).toContain('–');
  });

  it('tells absent snapshot kinds from refused ones', async () => {
    stub({ [ROUTE]: { body: STORAGE_BARE } });
    mount();
    await screen.findByRole('table');
    const absent = document.querySelector('[data-glance="snapshots"] [data-section]');
    expect(absent?.getAttribute('data-section')).toBe('absent');
    expect(absent?.textContent).toBe('Not installed');
    cleanup();

    stub({ [ROUTE]: { body: STORAGE_SNAPSHOTS_REFUSED } });
    mount();
    await screen.findByRole('table');
    const refused = document.querySelector('[data-glance="snapshots"] [data-section]');
    expect(refused?.getAttribute('data-section')).toBe('refused');
    expect(refused?.textContent).toBe('Refused');
    expect(document.querySelector('[data-filter="unsnapshotted"]')?.getAttribute('data-count')).toBe('-1');
  });

  it('lists every orphan by kind, each with its reason and a link to its page', async () => {
    stub({ [ROUTE]: { body: STORAGE_OVERVIEW } });
    mount('x/storage?show=orphans');
    await screen.findByLabelText('Volumes');
    const orphans = document.querySelector('[data-orphans]');
    expect(orphans?.querySelectorAll('[data-claim]')).toHaveLength(3);
    const released = document.querySelector('[data-volume="pvc-5b1e0c2a"]');
    expect(released?.querySelector('[data-orphan]')?.getAttribute('data-orphan')).toBe('released');
    expect(released?.textContent).toContain('Retain');
    expect(released?.querySelector('a')?.getAttribute('href')).toBe('/c/prod/persistentvolumes/pvc-5b1e0c2a');
    expect(document.querySelector('[data-class="st1-cold"] [data-orphan]')?.getAttribute('data-orphan')).toBe('unusedClass');
    expect(row('analytics/data-warehouse-0').querySelector('[data-orphan]')?.getAttribute('data-orphan')).toBe('workloadGone');
    // Read-only: nothing on the page deletes or resizes.
    expect(screen.queryByRole('button', { name: /delete|resize|expand/i })).toBeNull();
  });

  it('filters by a tile and back', async () => {
    stub({ [ROUTE]: { body: STORAGE_OVERVIEW } });
    mount();
    await screen.findByRole('table');
    fireEvent.click(screen.getByRole('button', { name: /≥ 80 %/ }));
    await waitFor(() => expect(document.querySelectorAll('[data-claim]')).toHaveLength(2));
    fireEvent.click(screen.getByRole('button', { name: /≥ 80 %/ }));
    await waitFor(() => expect(document.querySelectorAll('[data-claim]')).toHaveLength(STORAGE_OVERVIEW.claims.length));
  });

  it('answers a refusal as one, and offers no retry', async () => {
    stub({ [ROUTE]: { status: 403, body: { code: 'list_forbidden', message: 'You may not list the PersistentVolumeClaims here.' } } });
    mount();
    const notice = await screen.findByText('Refused');
    expect(notice.closest('[data-problem]')?.getAttribute('data-problem')).toBe('list_forbidden');
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();
  });
});

describe('the filters and the formatting', () => {
  it('pass a claim by what it is', () => {
    const [full, warm, shared, ledger, unread, unmounted] = STORAGE_OVERVIEW.claims as [Claim, Claim, Claim, Claim, Claim, Claim];
    expect(passes(full, 'full') && passes(warm, 'full') && !passes(ledger, 'full') && !passes(shared, 'full')).toBe(true);
    expect(passes(unread, 'unmeasured') && !passes(full, 'unmeasured')).toBe(true);
    expect(passes(unmounted, 'orphans') && !passes(full, 'orphans')).toBe(true);
    expect(passes(shared, 'unsnapshotted') && !passes(full, 'unsnapshotted')).toBe(true);
  });

  it('spells bytes as a person reads them', () => {
    expect(bytes(0)).toBe('0 B');
    expect(bytes(512)).toBe('512 B');
    expect(bytes(1.5 * 1024 ** 3)).toBe('1.5 GiB');
    expect(bytes(20 * 1024 ** 3)).toBe('20 GiB');
  });
});

describe('the Storage tabs', () => {
  it('draw a claim with its fill, volume, class and snapshots newest first', async () => {
    stub({ '/x/prod/storage/claim/shop/data-orders-db-0': { body: STORAGE_CLAIM } });
    withClient(<ClaimTab cluster="prod" kind="persistentvolumeclaims" namespace="shop" name="data-orders-db-0" />);
    await screen.findByLabelText('Snapshots');
    expect(document.querySelector('[data-fill]')?.getAttribute('data-level')).toBe('bad');
    expect(screen.getByText('pvc-0c41f7d9').getAttribute('href')).toBe('/c/prod/persistentvolumes/pvc-0c41f7d9');
    expect([...document.querySelectorAll('[data-snapshot]')].map((el) => el.getAttribute('data-snapshot'))).toEqual([
      'orders-db-0-1120',
      'orders-db-0-1119',
      'orders-db-0-1118',
    ]);
  });

  it("draw a pod's claims, and the pod tab is offered only on a pod that mounts one", async () => {
    stub({
      '/x/prod/storage/pod/shop/orders-db-0': { body: STORAGE_POD },
      '/x/prod/storage/pod/shop/web': { body: { ...STORAGE_POD, claims: [] } },
    });
    withClient(<PodTab cluster="prod" kind="pods" namespace="shop" name="orders-db-0" />);
    await screen.findByLabelText('Claim data-orders-db-0');
    const tab = storage.tabs?.find((t) => t.key === 'pod');
    const signal = new AbortController().signal;
    await expect(tab?.probe?.({ cluster: 'prod', kind: 'pods', namespace: 'shop', name: 'orders-db-0' }, signal)).resolves.toBe(true);
    await expect(tab?.probe?.({ cluster: 'prod', kind: 'pods', namespace: 'shop', name: 'web' }, signal)).resolves.toBe(false);
  });
});

describe('the Overview tile', () => {
  it('grades red with a claim at 90 % and leads to the full claims', async () => {
    stub({ [ROUTE]: { body: STORAGE_OVERVIEW } });
    const tile = await storageTile('prod', new AbortController().signal);
    expect(tile.status).toEqual({ text: '1 ≥ 90 %', tone: 'bad' });
    expect(tile.title).toBe('8 claims');
    expect(tile.to).toBe('/c/prod/x/storage?show=full');
    expect(tile.detail).toBe('Fill partial · some kubelets did not answer');
  });

  it('grades amber on orphans alone, and says why it has no fill', async () => {
    stub({ [ROUTE]: { body: STORAGE_BARE } });
    const tile = await storageTile('prod', new AbortController().signal);
    expect(tile.status?.tone).toBe('warn');
    expect(tile.to).toBe('/c/prod/x/storage?show=orphans');
    expect(tile.detail).toContain('rbac.nodeProxy');
    expect(tile.badges).not.toContain(expect.stringContaining('snapshotted'));
  });

  it('withholds OK while a mounted claim is unread, and says how many', async () => {
    // One claim measured, seven unread, nothing orphaned or full.
    const counts = { ...STORAGE_OVERVIEW.counts, orphans: 0, orphanClaims: 0, orphanVolumes: 0, orphanClasses: 0, warn: 0, bad: 0, measured: 1, shared: 0, unread: 7 };
    const partly = { ...STORAGE_OVERVIEW, counts };
    stub({ [ROUTE]: { body: partly } });
    const tile = await storageTile('prod', new AbortController().signal);
    expect(tile.status).toBeUndefined();
    expect(tile.badges).toContain('7 unread');
    cleanup();
    stub({ [ROUTE]: { body: partly } });
    mount();
    await screen.findByRole('table');
    const full = document.querySelector('[data-filter="full"]');
    expect(full?.textContent).toContain('0+');
    expect(full?.textContent).not.toMatch(/(^|[^0-9])0(?!\+)/);
  });

  it('carries no grade when the kubelets answered and measured no claim', async () => {
    // A driver reporting no volume stats: the source read, every claim unread.
    const counts = { ...STORAGE_BARE.counts, orphans: 0, orphanClaims: 0, orphanVolumes: 0, orphanClasses: 0 };
    const answered = { ...STORAGE_BARE, counts, fill: { state: 'read' as const, asked: 2, answered: 2 } };
    stub({ [ROUTE]: { body: answered } });
    const tile = await storageTile('prod', new AbortController().signal);
    expect(tile.status).toBeUndefined();
    expect(tile.badges).toContain('5 unread');
    cleanup();
    stub({ [ROUTE]: { body: answered } });
    mount();
    await screen.findByRole('table');
    expect(document.querySelector('[data-filter="full"]')?.getAttribute('data-count')).toBe('-1');
  });

  it('carries no grade, never a green OK, when no kubelet was read and nothing is orphaned', async () => {
    const counts = { ...STORAGE_BARE.counts, orphans: 0, orphanClaims: 0, orphanVolumes: 0, orphanClasses: 0 };
    stub({ [ROUTE]: { body: { ...STORAGE_BARE, counts } } });
    const tile = await storageTile('prod', new AbortController().signal);
    expect(tile.status).toBeUndefined();
    expect(tile.badges).toContain('5 unread');
  });
});
