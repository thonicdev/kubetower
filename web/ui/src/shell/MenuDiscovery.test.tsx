import { cleanup, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { MENU_DISCOVERY_FAILED_MS, MENU_DISCOVERY_PENDING_MS, navRefetchInterval } from '../api/queries';
import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp, stubFetch } from '../testing/harness';
import { resetMenuMode, resetOpenGroups } from './menuMemory';
import { resetNavMemory } from './navMemory';

function forgetEverything(): void {
  resetKeyboardStack();
  resetOpenGroups();
  resetMenuMode();
  resetNavMemory();
  localStorage.clear();
}

beforeEach(forgetEverything);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  forgetEverything();
});

const session = {
  body: { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' },
};
const clusters = { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } };

function navWith(discovery?: { state: string; detail: string }): { body: unknown } {
  return {
    body: {
      state: 'ok',
      data: {
        groups: [
          {
            key: 'cluster',
            label: 'Cluster',
            deferred: false,
            items: [{ text: 'Overview', href: '/c/prod' }],
          },
        ],
        ...(discovery === undefined ? {} : { discovery }),
      },
    },
  };
}

function menu(): HTMLElement {
  const element = document.getElementById('menu');
  if (element === null) throw new Error('#menu is missing');
  return element;
}

function stubNav(nav: { body: unknown }): void {
  stubFetch({
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/prod/nav': nav,
  });
}

describe('the menu says where discovery stands with an icon and no sentence', () => {
  it('draws a spinner with its detail on title and aria-label while discovery runs', async () => {
    const detail = 'Discovering the kinds this cluster serves';
    stubNav(navWith({ state: 'pending', detail }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().querySelector('[data-discovery="pending"]')).not.toBeNull();
    });
    const mark = menu().querySelector('[data-discovery="pending"]');
    expect(mark?.getAttribute('title')).toBe(detail);
    expect(mark?.getAttribute('aria-label')).toBe(detail);
    expect(menu().textContent).not.toContain(detail);
    expect(menu().querySelector('[data-note]')).toBeNull();
  });

  it('draws a warning with its one line on title when discovery failed', async () => {
    const detail = 'Discovery could not read metrics.k8s.io/v1beta1';
    stubNav(navWith({ state: 'failed', detail }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().querySelector('[data-discovery="failed"]')).not.toBeNull();
    });
    const mark = menu().querySelector('[data-discovery="failed"]');
    expect(mark?.getAttribute('title')).toBe(detail);
    expect(mark?.className).toContain('text-warn');
    expect(menu().textContent).not.toContain(detail);
    expect(menu().querySelector('[data-note]')).toBeNull();
  });

  it('draws nothing when discovery is complete', async () => {
    stubNav(navWith());
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Overview');
    });
    expect(menu().querySelector('[data-discovery]')).toBeNull();
  });

  it('asks again by itself while pending, and the icon goes when discovery is done', async () => {
    let calls = 0;
    const answers = [navWith({ state: 'pending', detail: 'Discovering the kinds this cluster serves' }), navWith()];
    const base: Record<string, { body: unknown }> = {
      '/api/session': session,
      '/api/clusters': clusters,
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    };
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
      const key = path.split('?')[0] ?? path;
      const reply = key === '/api/c/prod/nav' ? answers[Math.min(calls++, answers.length - 1)] : base[key];
      if (reply === undefined) return Promise.resolve(new Response(null, { status: 404 }));
      return Promise.resolve(
        new Response(JSON.stringify(reply.body), { status: 200, headers: { 'Content-Type': 'application/json' } }),
      );
    });
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().querySelector('[data-discovery="pending"]')).not.toBeNull();
    });
    await waitFor(
      () => {
        expect(menu().querySelector('[data-discovery]')).toBeNull();
      },
      { timeout: 8_000 },
    );
    expect(calls).toBeGreaterThanOrEqual(2);
  }, 15_000);
});

describe('the menu re-reads itself on the schedule its discovery state asks for', () => {
  const ok = (discovery?: { state: 'pending' | 'failed'; detail: string }) =>
    ({ state: 'ok', data: { groups: [], ...(discovery === undefined ? {} : { discovery }) } }) as never;

  it('polls every 2 s while pending and every 30 s after a failure', () => {
    expect(navRefetchInterval(ok({ state: 'pending', detail: 'x' }))).toBe(MENU_DISCOVERY_PENDING_MS);
    expect(navRefetchInterval(ok({ state: 'failed', detail: 'x' }))).toBe(MENU_DISCOVERY_FAILED_MS);
    expect(MENU_DISCOVERY_PENDING_MS).toBe(2_000);
    expect(MENU_DISCOVERY_FAILED_MS).toBe(30_000);
  });

  it('does not poll when discovery is complete, absent or the read itself failed', () => {
    expect(navRefetchInterval(ok())).toBe(false);
    expect(navRefetchInterval(undefined)).toBe(false);
    expect(navRefetchInterval({ state: 'forbidden', reason: { code: 'x', message: 'y' } } as never)).toBe(false);
  });
});
