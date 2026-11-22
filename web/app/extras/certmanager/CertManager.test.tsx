import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { CERTMANAGER_OVERVIEW } from '../../../measure/certmanager.fixture';
import { daysWord, passes, type Certificate, type Overview } from './data';
import { certmanager } from './index';
import { PAGE_ROWS } from './CertManager';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

/**
 * The page, mounted the way the console mounts it, with a fetch stub standing
 * in for `extras/certmanager`. Every body is the shape the Go route writes.
 */

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
      return Promise.resolve(
        new Response(body, { status: reply.status ?? 200, headers: { 'Content-Type': reply.contentType ?? 'application/json' } }),
      );
    }),
  );
  return asked;
}

function mount(path = 'x/certmanager') {
  const router = createMemoryRouter([{ path: 'c/:cluster', children: certmanager.routes }], {
    initialEntries: [`/c/prod/${path}`],
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

const ROUTE = '/x/prod/certmanager/';

function many(n: number): Overview {
  const base = CERTMANAGER_OVERVIEW.certificates[8] as Certificate;
  const certificates = Array.from({ length: n }, (_, i) => ({ ...base, namespace: 'team', name: `cert-${String(i).padStart(5, '0')}` }));
  return { ...CERTMANAGER_OVERVIEW, certificates, shown: n, counts: { ...CERTMANAGER_OVERVIEW.counts, total: n } };
}

describe('the Cert Manager page', () => {
  it('draws the tiles, the strip, the ring and the soonest first', async () => {
    stub({ [ROUTE]: { body: CERTMANAGER_OVERVIEW } });
    mount();
    await screen.findByRole('list', { name: 'Certificates expiring per day' });
    const c = CERTMANAGER_OVERVIEW.counts;
    expect(document.querySelector('[data-filter="expired"]')?.getAttribute('data-count')).toBe(String(c.expired));
    expect(document.querySelector('[data-filter="month"]')?.getAttribute('data-count')).toBe(String(c.month));
    expect(document.querySelectorAll('[data-day]')).toHaveLength(90);
    expect(document.querySelector('[data-ring] svg')?.getAttribute('aria-label')).toBe(
      `${String(CERTMANAGER_OVERVIEW.ring.ready)} ready, ${String(CERTMANAGER_OVERVIEW.ring.notReady)} not ready, 0 unknown`,
    );
    const rows = document.querySelectorAll('tbody tr');
    expect(rows[0]?.getAttribute('data-cert')).toBe('payments/checkout-api');
    expect(rows[0]?.textContent).toContain('expired 2 days ago');
  });

  it('filters on a tile, and keeps the pressed tile in the address', async () => {
    stub({ [ROUTE]: { body: CERTMANAGER_OVERVIEW } });
    const router = mount();
    const tile = await screen.findByRole('button', { name: /< 7 days/i });
    fireEvent.click(tile);
    await waitFor(() => expect(router.state.location.search).toBe('?show=week'));
    const bands = [...document.querySelectorAll('tbody tr')].map((r) => r.getAttribute('data-band'));
    expect(bands.length).toBe(CERTMANAGER_OVERVIEW.counts.week);
    expect(new Set(bands)).toEqual(new Set(['week']));

    fireEvent.click(screen.getByRole('button', { name: /ACME failing/i }));
    await waitFor(() => expect(document.querySelectorAll('[data-acme]')).toHaveLength(2));
    expect(screen.getByText(/failed to perform self check/)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: /Issuers broken/i }));
    await waitFor(() => expect(document.querySelector('[data-issuer="internal-ca"]')).not.toBeNull());
    expect(screen.getByText(/internal-ca-root/)).toBeTruthy();
  });

  it('says cert-manager is not installed, which is not a failure', async () => {
    stub({ [ROUTE]: { body: { ...CERTMANAGER_OVERVIEW, installed: false, message: 'cert-manager is not installed in this cluster.' } } });
    mount();
    await screen.findByText('cert-manager is not installed');
    expect(document.querySelector('[data-state="absent"]')).not.toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
    expect(document.querySelector('[data-strip]')).toBeNull();
  });

  it('says a refusal as one, not as a failure or an empty cluster', async () => {
    stub({ [ROUTE]: { status: 403, body: { code: 'list_forbidden', message: 'The cluster says you may not list them.' } } });
    mount();
    const notice = await screen.findByText('Refused');
    expect(notice.closest('[data-problem]')?.getAttribute('data-problem')).toBe('list_forbidden');
    expect(document.querySelector('[data-state="empty"]')).toBeNull();
  });

  it('says a read that failed as one', async () => {
    stub({ [ROUTE]: { status: 502, body: { code: 'list_failed', message: 'The Certificates could not be read.' } } });
    mount();
    await screen.findByText('Could not read');
    expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy();
  });

  it("says the console's gate as is when the extra is off", async () => {
    stub({ [ROUTE]: { status: 404, body: 'extension certmanager is not enabled', contentType: 'text/plain' } });
    mount();
    await screen.findByText('Refused');
    expect(screen.getByText('extension certmanager is not enabled')).toBeTruthy();
  });

  it('says empty when cert-manager holds nothing', async () => {
    stub({
      [ROUTE]: {
        body: { ...CERTMANAGER_OVERVIEW, certificates: [], shown: 0, strip: Array(90).fill(0), counts: { ...CERTMANAGER_OVERVIEW.counts, total: 0 } },
      },
    });
    mount();
    await screen.findByText('No certificate');
    expect(document.querySelector('[data-state="empty"]')).not.toBeNull();
  });

  it('says a section refused alone, and keeps the rest drawn', async () => {
    stub({
      [ROUTE]: { body: { ...CERTMANAGER_OVERVIEW, acme: { state: 'refused', message: 'You may not list the Orders.', items: [], inFlight: 0 } } },
    });
    mount('x/certmanager?show=acme');
    const refused = await screen.findByText('Refused');
    expect(refused.getAttribute('title')).toBe('You may not list the Orders.');
    expect(document.querySelector('[data-filter="acme"]')?.textContent).toContain('–');
    expect(document.querySelector('[data-strip]')).not.toBeNull();
  });

  it('counts the broken issuers a namespaced person may read, and says what was left out', async () => {
    const note = 'You may not list clusterissuers.cert-manager.io here, so they are not shown.';
    stub({
      [ROUTE]: {
        body: {
          ...CERTMANAGER_OVERVIEW,
          complete: false,
          notes: [note],
          counts: { ...CERTMANAGER_OVERVIEW.counts, issuersBroken: 2 },
          issuers: { state: 'read', total: 3, items: CERTMANAGER_OVERVIEW.issuers.items.slice(0, 3) },
        },
      },
    });
    mount();
    await screen.findByText(note);
    const tile = document.querySelector('[data-filter="issuers"]');
    expect(tile?.getAttribute('data-count')).toBe('2');
    expect(tile?.textContent).toContain('2+');
  });

  it('draws the broken issuers tile as a dash only when no issuer could be read', async () => {
    stub({
      [ROUTE]: { body: { ...CERTMANAGER_OVERVIEW, issuers: { state: 'refused', message: 'You may not list the Issuers here.', items: [], total: 0 } } },
    });
    mount();
    await screen.findByRole('list', { name: 'Certificates expiring per day' });
    const tile = document.querySelector('[data-filter="issuers"]');
    expect(tile?.getAttribute('data-count')).toBe('-1');
    expect(tile?.textContent).toContain('–');
  });

  it('asks the server once, with the scope, and never on an interval', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const asked = stub({ [`${ROUTE}?ns=shop`]: { body: { ...CERTMANAGER_OVERVIEW, namespace: 'shop' } } });
      mount('x/certmanager?ns=shop');
      await screen.findByRole('list', { name: 'Certificates expiring per day' });
      await vi.advanceTimersByTimeAsync(10 * 60_000);
      expect(asked).toEqual([`${ROUTE}?ns=shop`]);
    } finally {
      vi.useRealTimers();
    }
  });

  // 5 000 rows from the server draw one page of them; Show more adds one more.
  it('draws a bounded number of rows from a large answer', async () => {
    stub({ [ROUTE]: { body: many(5000) } });
    mount();
    await screen.findByRole('list', { name: 'Certificates expiring per day' });
    expect(document.querySelectorAll('tbody tr')).toHaveLength(PAGE_ROWS);
    const more = screen.getByRole('button', { name: `Show ${String(PAGE_ROWS)} more` });
    fireEvent.click(more);
    await waitFor(() => expect(document.querySelectorAll('tbody tr')).toHaveLength(2 * PAGE_ROWS));
    expect(within(document.body).getByText(`${String(2 * PAGE_ROWS)} of 5000`, { exact: false })).toBeTruthy();
  });
});

describe('the helpers', () => {
  it('words days left', () => {
    expect(daysWord({ daysLeft: null, band: 'none' })).toBe('not issued');
    expect(daysWord({ daysLeft: -1, band: 'expired' })).toBe('expired today');
    expect(daysWord({ daysLeft: -2, band: 'expired' })).toBe('expired yesterday');
    expect(daysWord({ daysLeft: 0, band: 'week' })).toBe('expires today');
    expect(daysWord({ daysLeft: 1, band: 'week' })).toBe('1 day left');
    expect(daysWord({ daysLeft: 40, band: 'later' })).toBe('40 days left');
  });

  it('filters the 30 days with the week in them', () => {
    const c = (band: Certificate['band']) => ({ ...(CERTMANAGER_OVERVIEW.certificates[0] as Certificate), band });
    expect(passes(c('week'), 'month')).toBe(true);
    expect(passes(c('month'), 'week')).toBe(false);
    expect(passes(c('expired'), 'month')).toBe(false);
  });
});
