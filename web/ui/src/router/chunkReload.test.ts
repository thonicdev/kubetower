import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import { createElement } from 'react';
import { createMemoryRouter, RouterProvider, type DataRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  clearChunkReloadGuard,
  clearChunkReloadGuardOnceRendered,
  isChunkLoadFailure,
  reloadForNewBuild,
} from './chunkReload';
import { RouteError } from './RouteError';

/**
 * The recovery path for a tab left open across an upgrade.
 *
 * These are the two things it has to hold: a failed import reaches the boundary
 * and reloads the tab **once**, and a chunk that is genuinely missing - a bad
 * deploy rather than a stale tab - does not reload it forever.
 *
 * Clearing the guard right after `render()` is before any lazy route has
 * resolved, so a tab whose *initial* route fails would reload, clear, fail,
 * reload. The guard is cleared when the router has actually settled with no
 * error.
 *
 * `window.location.reload` cannot be spied on in jsdom - `reload` is
 * unforgeable on `Location` - but `location` itself is configurable on the
 * window, so the whole object is stubbed. Nothing under test reads anything
 * else off it.
 */

/** The wording Chrome uses; Firefox and Safari have their own, tested below. */
const MISSING = 'Failed to fetch dynamically imported module: http://console/assets/Health-Ck3x.js';

function reloads(): number {
  return vi.mocked(window.location.reload).mock.calls.length;
}

/**
 * Wait for the reload count to reach `n`, rather than read it once.
 *
 * `RouteError` draws "Reloading…" at render and calls `reload()` from an
 * effect, which runs after the commit. `findByRole('alert')` resolves on the
 * commit, so a count read on the next line races the effect - and on a loaded
 * runner it loses (`expected +0 to be 1`).
 */
async function reloaded(n: number): Promise<void> {
  await waitFor(() => {
    expect(reloads()).toBe(n);
  });
}

/**
 * Let every pending effect run, then read the count.
 *
 * For the assertions that a reload did **not** happen, where waiting for the
 * count proves nothing - it is already there. The effect has to have had its
 * chance first, or "still 1" is true only because nothing ran yet.
 */
async function settledReloads(): Promise<number> {
  await act(async () => {});
  return reloads();
}

/**
 * A router whose one lazy route imports a chunk that is gone. `/` is static, so
 * a tab starting there renders and settles; `/late` never does.
 *
 * `lazy` is the **function** form, as in `routes.tsx`, and the test in
 * `routes.test.tsx` pins it: React Router awaits the object form's
 * per-property promises inside an empty `catch`, so a rejected
 * `lazy: { Component }` completes the navigation with no error, no Component
 * and an empty outlet - a blank page this boundary never hears about.
 */
function stale(initial: string): DataRouter {
  return createMemoryRouter(
    [
      {
        errorElement: createElement(RouteError),
        children: [
          { path: '/', Component: () => 'home' },
          { path: '/late', lazy: () => Promise.reject(new TypeError(MISSING)) },
        ],
      },
    ],
    { initialEntries: [initial] },
  );
}

/** Mount the way `createApp` does: the guard is released once the router has
 *  settled cleanly, never merely because `render()` returned. */
function mount(router: DataRouter): () => void {
  const release = clearChunkReloadGuardOnceRendered(router);
  render(createElement(RouterProvider, { router }));
  return release;
}

beforeEach(() => {
  sessionStorage.clear();
  vi.stubGlobal('location', { reload: vi.fn() });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  sessionStorage.clear();
});

describe('telling a missing chunk from everything else', () => {
  it('recognizes the three engines, the bundler, and nothing else', () => {
    expect(isChunkLoadFailure(new TypeError(MISSING))).toBe(true);
    expect(isChunkLoadFailure(new TypeError('error loading dynamically imported module'))).toBe(true);
    expect(isChunkLoadFailure(new TypeError('Importing a module script failed.'))).toBe(true);
    // Vite's own wording, for a chunk's stylesheet fetched ahead of it - the
    // terminal chunk carries xterm's CSS, and after an upgrade that is the file
    // that fails first.
    expect(
      isChunkLoadFailure(new Error('Unable to preload CSS for /assets/terminal-Bq2.css')),
    ).toBe(true);
    expect(isChunkLoadFailure(new Error('prod is marked read-only'))).toBe(false);
    // React Router's own `ErrorResponse` for an address no route matches: an
    // object, not an `Error`, and reloading would not change the answer.
    expect(isChunkLoadFailure({ status: 404, statusText: 'Not Found' })).toBe(false);
  });
});

describe('reloading once', () => {
  it('reloads the first time and refuses the second', () => {
    expect(reloadForNewBuild()).toBe(true);
    expect(reloadForNewBuild()).toBe(false);
    expect(reloads()).toBe(1);
  });

  it('is re-armed by a clean render', () => {
    expect(reloadForNewBuild()).toBe(true);
    clearChunkReloadGuard();
    expect(reloadForNewBuild()).toBe(true);
    expect(reloads()).toBe(2);
  });
});

describe('a chunk that no longer exists', () => {
  it('reaches the boundary and reloads the tab once', async () => {
    const release = mount(stale('/late'));

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('This page was updated. Reloading…');
    expect(alert.textContent).toContain(MISSING);
    await reloaded(1);
    release();
  });

  it('does not reload in a loop when the chunk is genuinely missing', async () => {
    // The stale tab: reloads once.
    const first = mount(stale('/late'));
    await screen.findByRole('alert');
    await reloaded(1);
    first();
    cleanup();

    // The tab after that reload, on a build whose chunk is still missing. The
    // router never settles cleanly, so the guard is never released, and the
    // second failure is drawn rather than reloaded - and drawn as what it is,
    // not as "Reloading…" over a reload that was refused.
    const second = mount(stale('/late'));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('one reload did not bring it back');
    expect(alert.textContent).not.toContain('Reloading');
    expect(await settledReloads()).toBe(1);
    second();
  });

  it('gets a reload of its own once a render has settled', async () => {
    // A previous upgrade already spent this tab's one reload.
    expect(reloadForNewBuild()).toBe(true);
    expect(reloads()).toBe(1);

    const router = stale('/');
    const release = mount(router);
    await waitFor(() => {
      expect(screen.getByText('home')).toBeTruthy();
    });
    // Settled with no error: the next missing chunk is a new upgrade.
    expect(sessionStorage.getItem('kubetower.chunk-reloaded')).toBeNull();

    await act(async () => {
      await router.navigate('/late');
    });
    await screen.findByRole('alert');
    await reloaded(2);
    release();
  });
});
