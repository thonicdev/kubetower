import { act, cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { resetLeaving, watchLeaving } from '../auth/leave';
import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp, stubFetch, type StubReply } from '../testing/harness';

// Where Log out leaves for: the server's sign-in page, by a navigation jsdom
// does not perform.
let left: string[] = [];

beforeEach(() => {
  left = [];
  watchLeaving((href) => left.push(href));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  resetKeyboardStack();
  resetLeaving();
});

/*
  A sign-out the session store did not record. The server answers 503
  `session_store_unreachable`, because the session is still live, and keeps the
  cookie so the browser can try again. So the tile stays: leaving for the
  sign-in page would draw a person as signed out who is not. It says so, and the
  same button, once the store answers, signs out.
*/
describe('a sign-out the store did not record', () => {
  const unrecorded: StubReply = {
    status: 503,
    contentType: 'application/problem+json',
    body: { status: 503, title: 'Log-out not recorded', code: 'session_store_unreachable', detail: 'x' },
  };

  const ONE = [{ name: 'prod', label: 'prod', state: 'ok' }];
  const TWO = [...ONE, { name: 'staging', label: 'staging', state: 'ok' }];

  function mounted(logout: StubReply, clusters: readonly object[] = ONE): Record<string, StubReply> {
    const routes: Record<string, StubReply> = {
      '/api/session': {
        body: { identity: 'alice@example.com', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' },
      },
      '/api/clusters': { body: { state: 'ok', items: clusters } },
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
      '/api/auth/methods': { body: { password: false, sso: { state: 'ready' } } },
      '/api/auth/logout': logout,
    };
    stubFetch(routes);
    mountApp('/');
    return routes;
  }

  /** The one-cluster console has answered: the rail, and the tile in it, are
   *  gone, and Log out is the header's. Clicking before this is clicking the
   *  tile of a rail drawn only because the list was still in flight - which is
   *  how this suite once passed while the header said nothing. */
  async function railGone(): Promise<void> {
    await waitFor(() => {
      expect(document.getElementById('rail')).toBeNull();
    });
  }

  /** The header's Log out, in the menu under the signed-in name. */
  async function headerSignOut(): Promise<HTMLElement> {
    const account = await screen.findByRole('button', { name: 'alice@example.com' });
    act(() => {
      fireEvent.click(account);
    });
    return screen.getByRole('menuitem', { name: 'Log out' });
  }

  it('stays on the console saying nothing was signed out, and the same entry tries again', async () => {
    const routes = mounted(unrecorded);
    await railGone();
    fireEvent.click(await headerSignOut());
    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toContain('could not log this session out');
    });
    expect(screen.getByRole('alert').textContent).toContain('log out again in a moment');
    expect(screen.getByRole('alert').textContent).not.toMatch(/sign[ -]?out/i);
    expect(left).toEqual([]);
    // Said in the header, where the eye is once the menu has shut - not inside
    // the menu, which closed as Log out was chosen.
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.getElementById('topbar')?.contains(screen.getByRole('alert'))).toBe(true);

    // The store answers again: the cookie was kept, so the retry is a sign-out.
    routes['/api/auth/logout'] = { status: 204 };
    fireEvent.click(await headerSignOut());
    await waitFor(() => {
      expect(left).toEqual(['/auth/login']);
    });
  });

  it('says so on the rail tile where there are two clusters', async () => {
    mounted(unrecorded, TWO);
    fireEvent.click(await screen.findByRole('button', { name: 'Log out' }));
    expect(document.getElementById('rail')).not.toBeNull();
    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toContain('could not log this session out');
    });
    expect(left).toEqual([]);
  });

  it('says nothing of the kind when the sign-out worked', async () => {
    mounted({ status: 204 });
    await railGone();
    fireEvent.click(await headerSignOut());
    // One sign-in page, the server's: it is the one that says signed out.
    await waitFor(() => {
      expect(left).toEqual(['/auth/login']);
    });
    expect(screen.queryByText(/could not log this session out/)).toBeNull();
  });
});
