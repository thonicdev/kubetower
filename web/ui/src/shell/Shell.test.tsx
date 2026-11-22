import { focusManager, onlineManager } from '@tanstack/react-query';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { resetLeaving, watchLeaving } from '../auth/leave';
import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp, stubFetch } from '../testing/harness';

afterEach(() => {
  focusManager.setFocused(undefined);
  vi.useRealTimers();
  vi.unstubAllGlobals();
  resetKeyboardStack();
  localStorage.clear();
  resetLeaving();
});

const session = {
  body: {
    identity: 'admin',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

const one = {
  body: { state: 'ok', items: [{ name: 'in-cluster', label: 'in-cluster', state: 'ok' }] },
};
const two = {
  body: {
    state: 'ok',
    items: [
      { name: 'in-cluster', label: 'in-cluster', state: 'ok', order: 0 },
      { name: 'staging', label: 'staging', state: 'ok', order: 1 },
    ],
  },
};

/** How a person signs in here (`SignInMethods`). A password is the container
 *  profile's default; `desktop` is the loopback desktop, with neither a
 *  password nor an identity provider. */
const withPassword = { body: { password: true, sso: { state: 'off' } } };
const withProvider = { body: { password: false, sso: { state: 'ready' } } };
const desktop = { body: { password: false, sso: { state: 'off' } } };

function routes(clusters: unknown, methods: unknown = withPassword): Record<string, unknown> {
  return {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/auth/methods': methods,
    '/api/c/in-cluster/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/c/in-cluster/extensions': { body: { state: 'empty', items: [] } },
  };
}

/** Every read answered and every answer drawn. An absent Log out is only a
 *  finding once the sign-in methods have been asked and their answer rendered:
 *  before that the tile is withheld anyway, and the test would pass on a
 *  console that had not decided yet. */
async function settled(calls: readonly string[]): Promise<void> {
  await waitFor(() => {
    expect(calls).toContain('GET /api/auth/methods');
  });
  await act(() => new Promise((resolve) => setTimeout(resolve, 50)));
}

/** `stubFetch`, and a log of what was asked. */
function logged(stubbed: Record<string, unknown>): string[] {
  const calls: string[] = [];
  stubFetch(stubbed as never);
  const answer = globalThis.fetch;
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
    calls.push(`${init?.method ?? 'GET'} ${path.split('?')[0] ?? path}`);
    return answer(input, init);
  });
  return calls;
}

/** The header's Log out, behind the signed-in name: the name is
 *  found in the top bar, its menu opened, and its Log out returned. */
async function headerSignOut(name = 'admin'): Promise<HTMLElement> {
  const account = await screen.findByRole('button', { name });
  expect(document.getElementById('topbar')?.contains(account)).toBe(true);
  act(() => {
    fireEvent.click(account);
  });
  return screen.getByRole('menuitem', { name: 'Log out' });
}

function kinds(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>('button[aria-controls="menu"]')];
}

function rail(): HTMLElement | null {
  return document.getElementById('rail');
}

/** One route that never answers, over whatever `stubFetch` set up: a read still
 *  in flight, for as long as the test looks. */
function hold(path: string): void {
  const stubbed = globalThis.fetch;
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const asked = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
    if ((asked.split('?')[0] ?? asked) === path) return new Promise<Response>(() => {});
    return stubbed(input, init);
  });
}

/**
 * The rail is for choosing a cluster. With one there is nothing to choose, so it
 * is not drawn - and the condition is **the count**, not the profile, so the
 * desktop with one kubeconfig context loses it too.
 */
describe('the rail, by how many clusters there are', () => {
  it('is not drawn for one cluster, and Log out is still reachable', async () => {
    stubFetch(routes(one) as never);
    mountApp('/c/in-cluster');

    // While the list is in flight the rail is drawn - it is what reports a
    // failure - so wait for the answer rather than for the first Log out.
    await waitFor(() => {
      expect(rail()).toBeNull();
    });
    // The header is the region that holds what the rail held: Log out is in
    // the menu under who is signed in.
    expect(await headerSignOut()).toBeTruthy();
    expect(document.getElementById('topbar')?.textContent).toContain('admin');
  });

  it('signs out from the header when there is no rail', async () => {
    const calls: string[] = [];
    const base = routes(one);
    stubFetch({ ...base, '/api/auth/logout': { status: 204 } } as never);
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      calls.push(`${init?.method ?? 'GET'} ${path}`);
      return stubbed(input, init);
    });
    const left: string[] = [];
    watchLeaving((href) => left.push(href));
    mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(rail()).toBeNull();
    });

    fireEvent.click(await headerSignOut());
    await waitFor(() => {
      expect(left).toEqual(['/auth/login']);
    });
    expect(calls).toContain('POST /api/auth/logout');
  });

  it('draws the one cluster state beside its name, where the tile was', async () => {
    stubFetch(
      routes({
        body: {
          state: 'ok',
          items: [
            {
              name: 'in-cluster',
              label: 'in-cluster',
              state: 'unreachable',
              reason: { code: 'cluster_unreachable', message: 'dial tcp: i/o timeout' },
            },
          ],
        },
      }) as never,
    );
    mountApp('/c/in-cluster');

    await waitFor(() => {
      expect(document.querySelector('#topbar [title="dial tcp: i/o timeout"]')).not.toBeNull();
    });
  });

  it('keeps Arrange reachable from the header, so a hidden cluster can be revealed', async () => {
    // The desktop that hid a cluster and is now down to one kubeconfig context:
    // the rail is gone, and Arrange was only ever at its foot.
    const calls: string[] = [];
    stubFetch({
      ...routes({
        body: {
          state: 'ok',
          items: [{ name: 'in-cluster', label: 'in-cluster', state: 'ok', hidden: true }],
        },
      }),
      '/api/preferences': { body: { state: 'ok', data: {} } },
      '/api/preferences/rail/reveal': { status: 204 },
    } as never);
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      calls.push(`${init?.method ?? 'GET'} ${path}`);
      return stubbed(input, init);
    });
    mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(rail()).toBeNull();
    });

    const arrange = await screen.findByRole('button', { name: 'Arrange' });
    expect(document.getElementById('topbar')?.contains(arrange)).toBe(true);
    fireEvent.click(arrange);
    fireEvent.click(await screen.findByRole('button', { name: 'Reveal' }));

    await waitFor(() => {
      expect(calls).toContain('POST /api/preferences/rail/reveal');
    });
  });

  it('offers no Arrange in the header where the console keeps no preferences', async () => {
    // One cluster under several replicas: the rail is gone for the count, and
    // the header's Arrange is gone for the capability - its writes are not
    // registered there. Log out stays.
    stubFetch({
      ...routes(one),
      '/api/capabilities': {
        body: {
          mode: 'shared',
          localShell: false,
          podExec: false,
          nodeShell: false,
          assistant: false,
          portForward: false,
          preferences: false,
          readOnly: { writesRefused: false, detailWritesRefused: false },
        },
      },
    } as never);
    mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(rail()).toBeNull();
    });
    // The header is drawn and the name holding Log out is in it, so an absent Arrange is the
    // gate and not a header that has not rendered yet.
    await waitFor(() => {
      expect(document.getElementById('topbar')?.contains(screen.getByRole('button', { name: 'admin' }))).toBe(true);
    });
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: 'Arrange' })).toBeNull();
    });
  });

  it('offers no Arrange until the capabilities have said whether its writes exist', async () => {
    // `useKeepsPreferences` reads unknown as "keeps", which is right for
    // drawing and wrong for writing: under the server profile a click in that
    // window would open a dialog whose four writes are not registered.
    stubFetch(routes(one) as never);
    hold('/api/capabilities');
    mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(rail()).toBeNull();
    });
    await waitFor(() => {
      expect(document.getElementById('topbar')?.contains(screen.getByRole('button', { name: 'admin' }))).toBe(true);
    });
    expect(screen.queryByRole('button', { name: 'Arrange' })).toBeNull();
  });

  it('draws no rail for a shared console whose list is still in flight', async () => {
    // The other half. A pending list is not "one cluster", so a cold load of
    // an in-cluster console would draw the rail and then take it away. The server
    // profile serves one cluster, so once the capabilities say `shared`
    // the rail is not drawn for the wait.
    stubFetch({
      ...routes(one),
      '/api/capabilities': {
        body: {
          mode: 'shared',
          localShell: false,
          podExec: true,
          nodeShell: false,
          assistant: false,
          portForward: true,
          preferences: false,
          readOnly: { writesRefused: false, detailWritesRefused: false },
        },
      },
    } as never);
    hold('/api/clusters');
    mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(rail()).toBeNull();
    });
  });

  it('is drawn for two, exactly as before, with Log out at its foot and not twice', async () => {
    stubFetch(routes(two) as never);
    mountApp('/c/in-cluster');

    await waitFor(() => {
      expect(rail()).not.toBeNull();
    });
    const signOuts = await screen.findAllByRole('button', { name: 'Log out' });
    expect(signOuts).toHaveLength(1);
    expect(rail()?.contains(signOuts[0] ?? null)).toBe(true);
    // And Arrange, the other thing the header takes over, is the rail's alone.
    const arranges = await screen.findAllByRole('button', { name: 'Arrange' });
    expect(arranges).toHaveLength(1);
    expect(rail()?.contains(arranges[0] ?? null)).toBe(true);
  });

  it('is drawn while the list is in flight or failed, because the rail is what says so', async () => {
    stubFetch(
      routes({
        body: { state: 'unreadable', reason: { code: 'undecodable', message: 'the kubeconfig would not parse' } },
      }) as never,
    );
    mountApp('/c/in-cluster');
    // Not vacuous: the rail is drawn from the first frame, so what is waited for
    // is the failure drawn *in* it - the answer arrived and the rail stayed.
    await waitFor(() => {
      expect(rail()?.textContent).toContain('Could not be read');
    });
  });

  it('comes back when a second cluster appears, without a reload', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const live: Record<string, unknown> = { ...routes(one) };
    stubFetch(live as never);
    mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(rail()).toBeNull();
    });

    // The same object the stub answers from, so the next poll sees two.
    live['/api/clusters'] = two;
    await vi.advanceTimersByTimeAsync(31_000);

    await waitFor(() => {
      expect(rail()).not.toBeNull();
    });
  });
});

/**
 * Kinds is at the foot of the rail, below
 * Arrange, and in the top bar only where there is no rail; the session is
 * drawn only where the server offers a way to sign back in.
 */
describe('Kinds at the foot of the rail, and no Log out with no way back in', () => {
  it('puts Kinds in the rail below Arrange and above the session, and not in the top bar', async () => {
    stubFetch({ ...routes(two), '/api/preferences': { body: { state: 'ok', data: {} } } } as never);
    mountApp('/c/in-cluster');
    const arrange = await screen.findByRole('button', { name: 'Arrange' });
    const button = await screen.findByRole('button', { name: 'Kinds' });

    expect(kinds()).toEqual([button]);
    expect(rail()?.contains(button)).toBe(true);
    expect(document.getElementById('topbar')?.contains(button)).toBe(false);
    expect(arrange.compareDocumentPosition(button) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    const signOut = await screen.findByRole('button', { name: 'Log out' });
    expect(button.compareDocumentPosition(signOut) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    // Its glyph alone at 56px, with its name and its shortcut kept.
    expect(button.textContent).toBe('');
    expect(button.getAttribute('aria-label')).toBe('Kinds');
    expect(button.getAttribute('title')).toBe('Hide the kinds column (Ctrl+B)');
    expect(button.getAttribute('aria-expanded')).toBe('true');

    fireEvent.click(button);
    await waitFor(() => {
      expect(document.getElementById('menu')?.dataset['hidden']).toBe('');
    });
    expect(button.getAttribute('aria-expanded')).toBe('false');
    expect(button.getAttribute('title')).toBe('Show the kinds column (Ctrl+B)');
  });

  it('puts Kinds in the bottom left corner where there is no rail', async () => {
    stubFetch(routes(one) as never);
    mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(rail()).toBeNull();
    });
    const button = await screen.findByRole('button', { name: 'Kinds' });
    expect(kinds()).toEqual([button]);
    expect(button.dataset['kindsAt']).toBe('corner');
    expect(document.getElementById('topbar')?.contains(button)).toBe(false);
  });

  it('draws no Kinds anywhere off a cluster', async () => {
    const calls = logged(routes(two));
    mountApp('/settings/general');
    await waitFor(() => {
      expect(rail()).not.toBeNull();
    });
    await settled(calls);
    // The rail's foot is drawn - Log out is in it - and Kinds is not.
    expect(rail()?.contains(screen.getByRole('button', { name: 'Log out' }))).toBe(true);
    expect(kinds()).toEqual([]);
  });

  it('draws no session on a desktop with no sign-in method, with a rail', async () => {
    const calls = logged(routes(two, desktop));
    mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(rail()).not.toBeNull();
    });
    const button = await screen.findByRole('button', { name: 'Kinds' });
    await settled(calls);
    expect(screen.queryByRole('button', { name: 'Log out' })).toBeNull();
    expect(document.body.textContent).not.toContain('admin');
    expect(rail()?.contains(button)).toBe(true);
  });

  it('draws no session on a desktop with no sign-in method, and no rail', async () => {
    const calls = logged(routes(one, desktop));
    mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(rail()).toBeNull();
    });
    await settled(calls);
    expect(screen.queryByRole('button', { name: 'Log out' })).toBeNull();
    expect(document.getElementById('topbar')?.textContent).not.toContain('admin');
  });

  it('keeps the session where a password is offered - a desktop with KT_PASSWORD_HASH too', async () => {
    stubFetch(routes(two, withPassword) as never);
    mountApp('/c/in-cluster');
    const signOut = await screen.findByRole('button', { name: 'Log out' });
    expect(rail()?.contains(signOut)).toBe(true);
  });

  it('keeps the session in the header where an identity provider is offered and there is no rail', async () => {
    stubFetch(routes(one, withProvider) as never);
    mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(rail()).toBeNull();
    });
    expect(await headerSignOut()).toBeTruthy();
    expect(document.getElementById('topbar')?.textContent).toContain('admin');
  });

  it('keeps the session where an identity provider is configured and not answering, and does not poll for it', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = logged(routes(two, { body: { password: false, sso: { state: 'unavailable', detail: 'x' } } }));
    mountApp('/c/in-cluster');
    expect(rail()?.contains(await screen.findByRole('button', { name: 'Log out' }))).toBe(true);
    // The sign-in view re-asks every five seconds while the provider is down;
    // the shell's answer cannot change with it, so it asks once.
    const asked = (): number => calls.filter((one) => one === 'GET /api/auth/methods').length;
    expect(asked()).toBe(1);
    await vi.advanceTimersByTimeAsync(21_000);
    expect(asked()).toBe(1);
  });

  it('keeps the session where the methods could not be read: no way out is the worse mistake', async () => {
    const stubbed = routes(two);
    delete stubbed['/api/auth/methods'];
    stubFetch(stubbed as never);
    mountApp('/c/in-cluster');
    expect(rail()?.contains(await screen.findByRole('button', { name: 'Log out' }))).toBe(true);
  });

  it.each([
    ['in the rail', two, 'rail'],
    ['in the header', one, 'topbar'],
  ])(
    'still warns %s that a desktop session is ending, with no Log out: KT_SESSION_MAX can shorten it',
    async (_where, clusters, region) => {
      const ending = {
        body: {
          identity: 'admin',
          idleExpiresAt: '2999-01-01T00:00:00Z',
          absoluteExpiresAt: new Date(Date.now() + 5 * 60_000 - 1_000).toISOString(),
        },
      };
      const calls = logged({ ...routes(clusters, desktop), '/api/session': ending });
      mountApp('/c/in-cluster');
      await settled(calls);
      // Read again on every try: a cold load draws the rail, and the warning
      // in it, until the list says one cluster - and then the header's is a
      // different element (on a loaded CI runner the first one found was the
      // rail's, detached by the time it was looked at).
      await waitFor(() => {
        const found = screen.getAllByText('Session ends in 5 min');
        expect(found).toHaveLength(1);
        expect(document.getElementById(region)?.contains(found[0] ?? null)).toBe(true);
      });
      expect(screen.getByText('Session ends in 5 min').getAttribute('role')).toBe('status');
      expect(screen.queryByRole('button', { name: 'Log out' })).toBeNull();
      expect(document.body.textContent).not.toContain('admin');
    },
  );

  it('says 0 min, never a negative count, for a desktop session whose end has passed', async () => {
    const ended = {
      body: {
        identity: 'admin',
        idleExpiresAt: '2999-01-01T00:00:00Z',
        absoluteExpiresAt: new Date(Date.now() - 2 * 60_000).toISOString(),
      },
    };
    const calls = logged({ ...routes(two, desktop), '/api/session': ended });
    mountApp('/c/in-cluster');
    await settled(calls);
    await waitFor(() => {
      expect(rail()?.textContent).toContain('Session ends in 0 min');
    });
    expect(document.body.textContent).not.toMatch(/Session ends in -/);
  });

  it('draws nothing of the session on a desktop whose session is not ending', async () => {
    const calls = logged(routes(two, desktop));
    mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(rail()).not.toBeNull();
    });
    await settled(calls);
    expect(screen.queryByText(/Session ends/)).toBeNull();
  });

  it('re-asks a methods read that did not reach the server, and takes Log out away on a desktop', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = logged(routes(two, desktop));
    // The first read is lost - the process behind the window restarting.
    let lost = 1;
    failing('/api/auth/methods', () => lost-- > 0);
    mountApp('/c/in-cluster');
    // Failing open while it does not know: Log out is drawn.
    expect(rail()?.contains(await screen.findByRole('button', { name: 'Log out' }))).toBe(true);
    const asked = (): number => calls.filter((one) => one === 'GET /api/auth/methods').length;
    // Every five seconds, and not sooner.
    await vi.advanceTimersByTimeAsync(4_500);
    expect(asked()).toBe(1);
    expect(screen.getByRole('button', { name: 'Log out' })).toBeTruthy();
    await vi.advanceTimersByTimeAsync(1_000);
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: 'Log out' })).toBeNull();
    });
    expect(asked()).toBe(2);
  });

  it('stops re-asking after a minute, and keeps Log out drawn', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = logged(routes(two, desktop));
    failing('/api/auth/methods', () => true);
    mountApp('/c/in-cluster');
    expect(rail()?.contains(await screen.findByRole('button', { name: 'Log out' }))).toBe(true);
    const asked = (): number => calls.filter((one) => one === 'GET /api/auth/methods').length;
    await vi.advanceTimersByTimeAsync(65_000);
    const after = asked();
    expect(after).toBe(12);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(asked()).toBe(after);
    expect(screen.getByRole('button', { name: 'Log out' })).toBeTruthy();
  });

  it('does not re-ask an older server that has no methods route: its 404 is an answer', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const stubbed = routes(two);
    delete stubbed['/api/auth/methods'];
    const calls = logged(stubbed);
    mountApp('/c/in-cluster');
    expect(rail()?.contains(await screen.findByRole('button', { name: 'Log out' }))).toBe(true);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(calls.filter((one) => one === 'GET /api/auth/methods')).toHaveLength(1);
    expect(screen.getByRole('button', { name: 'Log out' })).toBeTruthy();
  });

  it('draws Kinds nowhere until the cluster list has answered, so one cluster does not move it', async () => {
    const calls = logged(routes(one));
    const release = deferred('/api/clusters');
    mountApp('/c/in-cluster');
    // A cold load: the rail is drawn while the list is read (`railDrawn.ts`).
    await waitFor(() => {
      expect(rail()).not.toBeNull();
    });
    await settled(calls);
    expect(kinds()).toEqual([]);

    release();
    await waitFor(() => {
      expect(rail()).toBeNull();
    });
    const button = await screen.findByRole('button', { name: 'Kinds' });
    expect(button.dataset['kindsAt']).toBe('corner');
  });

  it('keeps Kinds drawn on a cluster list that failed, without asking it again in a loop', async () => {
    const stubbed = routes(two);
    delete stubbed['/api/clusters'];
    const calls = logged(stubbed);
    mountApp('/c/in-cluster');
    await settled(calls);
    await screen.findByRole('button', { name: 'Kinds' });
    await act(() => new Promise((resolve) => setTimeout(resolve, 300)));
    // The rail's list, Kinds' own observer of it mounting once, and the
    // shell's: a handful, where drawing Kinds on `!isPending` asked hundreds.
    expect(calls.filter((one) => one === 'GET /api/clusters').length).toBeLessThan(6);
    expect(kinds()).toHaveLength(1);
  });

  it.each(['/api/capabilities', '/api/preferences'])(
    'draws Kinds in the rail only once %s has answered, so Arrange does not push it down',
    async (path) => {
      const calls = logged({ ...routes(two), '/api/preferences': { body: { state: 'ok', data: {} } } });
      const release = deferred(path);
      mountApp('/c/in-cluster');
      await waitFor(() => {
        expect(rail()?.querySelector('[data-cluster]')).not.toBeNull();
      });
      await settled(calls);
      expect(kinds()).toEqual([]);

      release();
      const arrange = await screen.findByRole('button', { name: 'Arrange' });
      const button = await screen.findByRole('button', { name: 'Kinds' });
      expect(rail()?.contains(button)).toBe(true);
      expect(arrange.compareDocumentPosition(button) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    },
  );

  it('keeps Kinds drawn while a capabilities read that failed is asked again', async () => {
    // A failed read with no data goes back to pending when it is asked again -
    // here on a window focus - so Kinds waits on "read once", not on "not
    // pending", or it would vanish for as long as the second ask takes.
    const calls = logged({ ...routes(two), '/api/preferences': { body: { state: 'ok', data: {} } } });
    mountApp('/c/in-cluster');
    await settled(calls);
    await screen.findByRole('button', { name: 'Kinds' });
    let again = 0;
    const answered = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const asked = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
      if ((asked.split('?')[0] ?? asked) !== '/api/capabilities') return answered(input, init);
      again += 1;
      return new Promise<Response>(() => {});
    });
    act(() => {
      focusManager.setFocused(false);
      focusManager.setFocused(true);
    });
    await waitFor(() => {
      expect(again).toBe(1);
    });
    await act(() => new Promise((resolve) => setTimeout(resolve, 50)));
    expect(kinds()).toHaveLength(1);
  });

  it.each(['/api/clusters', '/api/capabilities', '/api/preferences'])(
    'draws Kinds after a bounded wait when %s never answers, so a hung read cannot hide it for good',
    async (path) => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const calls = logged({ ...routes(two), '/api/preferences': { body: { state: 'ok', data: {} } } });
      hold(path);
      mountApp('/c/in-cluster');
      await waitFor(() => {
        expect(rail()).not.toBeNull();
      });
      await settled(calls);
      await vi.advanceTimersByTimeAsync(7_000);
      expect(kinds()).toEqual([]);
      await vi.advanceTimersByTimeAsync(1_500);
      await waitFor(() => {
        expect(kinds()).toHaveLength(1);
      });
      expect(rail()?.contains(kinds()[0] ?? null)).toBe(true);
    },
  );
});

/** What the server answers in a pod (`internal/web/apimount.go`, `mode` under
 *  `inCluster`), and the two profiles that must not change with it. */
function capabilities(mode: 'shared' | 'desktop' | 'container'): { body: unknown } {
  return {
    body: {
      mode,
      localShell: mode === 'desktop',
      podExec: true,
      nodeShell: false,
      assistant: false,
      portForward: mode !== 'shared',
      preferences: mode !== 'shared',
      readOnly: { writesRefused: false, detailWritesRefused: false },
    },
  };
}

/**
 * The in-cluster console opens its cluster straight away, and Kinds is at the
 * bottom left as in every mode. Only under `shared` (with its one cluster) the
 * landing; a desktop or a container with one cluster keeps the chooser. There
 * is no Cluster link or page; `/health` leads to `/`.
 */
describe('the in-cluster console', () => {
  function inCluster(extra: Record<string, unknown> = {}): Record<string, unknown> {
    return { ...routes(one), '/api/capabilities': capabilities('shared'), ...extra };
  }

  it.each(['/', '/health'])('opens on its one cluster from %s, replacing the address', async (from) => {
    stubFetch(inCluster() as never);
    const { router } = mountApp(from);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/in-cluster');
    });
    // Replaced, so Back does not return to a list nobody may pick from.
    expect(router.state.historyAction).toBe('REPLACE');
    expect(document.querySelector('#page a[data-cluster]')).toBeNull();
  });

  it('draws no card while the profile is still unread and the list has said one', async () => {
    // The two reads race. A list that answers first must not be drawn as one
    // card until the capabilities arrive and the console leaves.
    const calls = logged(inCluster());
    const release = deferred('/api/capabilities');
    const { router } = mountApp('/');
    await waitFor(() => {
      expect(calls).toContain('GET /api/clusters');
    });
    // The list has answered one cluster: that, with the profile unread, is the
    // only thing that takes the rail away. Asserting before it would pass on a
    // console that had not heard from either read.
    await waitFor(() => {
      expect(rail()).toBeNull();
    });
    expect(router.state.location.pathname).toBe('/');
    expect(document.querySelector('#page a[data-cluster]')).toBeNull();
    expect(document.getElementById('page')?.textContent).toContain('Reading this page');

    release();
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/in-cluster');
    });
  });

  it('keeps the list and its failure on / when the list could not be read', async () => {
    // An empty answer and a failed one are different answers: with no cluster
    // named there is nowhere to go, and the list is what says why.
    stubFetch(
      inCluster({
        '/api/clusters': {
          body: { state: 'unreadable', reason: { code: 'undecodable', message: 'the kubeconfig would not parse' } },
        },
      }) as never,
    );
    const { router } = mountApp('/');
    await waitFor(() => {
      expect(document.getElementById('page')?.textContent).toContain('Could not be read');
    });
    expect(router.state.location.pathname).toBe('/');
  });

  it('draws no Cluster link and points the KubeTower crumb at the cluster', async () => {
    stubFetch(inCluster() as never);
    mountApp('/c/in-cluster');
    const crumb = await screen.findByRole('link', { name: 'KubeTower' });
    await waitFor(() => {
      expect(crumb.getAttribute('href')).toBe('/c/in-cluster');
    });
    expect(document.querySelector('#topbar a[href="/health"]')).toBeNull();
    // Settings is not part of it: it is under the signed-in name.
    act(() => {
      fireEvent.click(screen.getByRole('button', { name: 'admin' }));
    });
    expect(screen.getByRole('menuitem', { name: 'Settings' })).toBeTruthy();
  });

  it.each(['/', '/health'])('stops waiting on %s for a list that never answers, and draws the list', async (from) => {
    // `request.ts` sets no timeout: without a bound, a hung list would keep the
    // page on its wait for as long as the tab was open. `/health` leads to `/`,
    // and waits the same way there.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    stubFetch(inCluster() as never);
    hold('/api/clusters');
    const { router } = mountApp(from);
    await waitFor(() => {
      expect(rail()).toBeNull();
    });
    expect(document.getElementById('page')?.textContent).toContain('Reading this page');

    await vi.advanceTimersByTimeAsync(7_000);
    expect(document.getElementById('page')?.textContent).toContain('Reading this page');

    await vi.advanceTimersByTimeAsync(1_500);
    expect(await screen.findByRole('heading', { level: 1, name: 'Clusters' })).toBeDefined();
    expect(router.state.location.pathname).toBe('/');
  });

  it('measures the wait once per load, so a page opened after it does not wait again', async () => {
    // A profile that never answers, and the one cluster: every console here
    // waits for the profile before it can tell a chooser from a redirect. Once
    // the load's wait is over, the landing mounted later draws at once rather
    // than starting a wait of its own.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    stubFetch(routes(one) as never);
    hold('/api/capabilities');
    const { router } = mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(rail()).toBeNull();
    });
    await vi.advanceTimersByTimeAsync(8_500);
    // The top bar's Kinds is what the wait was holding back on this page.
    expect(await screen.findByRole('button', { name: 'Kinds' })).toBeDefined();

    await act(async () => {
      await router.navigate('/');
    });
    // Within a second, where a clock started at this mount would hold for eight.
    await waitFor(
      () => {
        expect(document.querySelector('#page a[data-cluster="in-cluster"]')).not.toBeNull();
      },
      { timeout: 1_000 },
    );
  });

  it('does not wait on a list a browser offline has paused', async () => {
    // A paused read is not being asked, so it cannot say one cluster soon: the
    // list page is drawn as on any console, rather than after the load's wait.
    onlineManager.setOnline(false);
    try {
      stubFetch(inCluster() as never);
      mountApp('/');
      expect(await screen.findByRole('heading', { level: 1, name: 'Clusters' }, { timeout: 2_000 })).toBeDefined();
    } finally {
      onlineManager.setOnline(true);
    }
  });

  /** The profile has answered `shared` and the list its one cluster: the crumb
   *  is the cluster's only once both have. Before that, no Kinds is drawn for
   *  another reason (`useKindsAt`'s `none`), and an assertion would prove
   *  nothing about this one. */
  async function inClusterKnown(): Promise<void> {
    const crumb = await screen.findByRole('link', { name: 'KubeTower' });
    await waitFor(() => {
      expect(crumb.getAttribute('href')).toBe('/c/in-cluster');
    });
  }

  // Kinds is at the bottom left on the in-cluster console too, as on the
  // desktop, and the column folds there.
  it('draws Kinds in the bottom left corner, and the column folds', async () => {
    stubFetch(inCluster() as never);
    mountApp('/c/in-cluster');
    await inClusterKnown();
    const button = await screen.findByRole('button', { name: 'Kinds' });
    expect(kinds()).toEqual([button]);
    expect(button.dataset['kindsAt']).toBe('corner');
    expect(document.getElementById('menu')?.dataset['hidden']).toBeUndefined();
    fireEvent.click(button);
    await waitFor(() => {
      expect(document.getElementById('menu')?.dataset['hidden']).toBe('');
    });
  });

  it('folds the column on Ctrl+B, and remembers it', async () => {
    stubFetch(inCluster() as never);
    mountApp('/c/in-cluster');
    await inClusterKnown();
    await screen.findByRole('button', { name: 'Kinds' });
    fireEvent.keyDown(window, { key: 'b', ctrlKey: true });
    await waitFor(() => {
      expect(document.getElementById('menu')?.dataset['hidden']).toBe('');
    });
    expect(localStorage.getItem('kubetower.menu.mode')).toBe('hidden');
  });

  it('leaves Ctrl+B alone until the profile has said which console this is', async () => {
    // In that window a key pressed would store a choice
    // the in-cluster console then ignores.
    const calls = logged(inCluster());
    const release = deferred('/api/capabilities');
    mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(rail()).toBeNull();
    });
    await settled(calls);
    await waitFor(() => {
      expect(document.getElementById('menu-jump')).not.toBeNull();
    });
    expect(fireEvent.keyDown(window, { key: 'b', ctrlKey: true })).toBe(true);
    expect(document.getElementById('menu')?.dataset['hidden']).toBeUndefined();
    expect(localStorage.getItem('kubetower.menu.mode')).not.toBe('hidden');
    release();
    await inClusterKnown();
  });

  it('keeps the column hidden where a hidden one was stored, with the button to bring it back', async () => {
    localStorage.setItem('kubetower.menu.mode', 'hidden');
    stubFetch(inCluster() as never);
    mountApp('/c/in-cluster');
    await inClusterKnown();
    await waitFor(() => {
      expect(document.getElementById('menu')?.dataset['hidden']).toBe('');
    });
    expect((await screen.findByRole('button', { name: 'Kinds' })).getAttribute('aria-expanded')).toBe('false');
  });

  it('draws Kinds in the rail it draws for a list that failed', async () => {
    // A failed list draws the rail that reports it, in this profile as in any.
    // The rail's own Kinds is the profile's to withhold, not the count's.
    stubFetch(
      inCluster({
        '/api/clusters': {
          body: { state: 'unreadable', reason: { code: 'undecodable', message: 'the kubeconfig would not parse' } },
        },
      }) as never,
    );
    mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(rail()?.textContent).toContain('Could not be read');
    });
    // Log out is at the rail's foot, below where Kinds would be, and the rail
    // draws its Kinds once the capabilities and the list have been read.
    await waitFor(() => {
      expect(rail()?.contains(screen.getByRole('button', { name: 'Log out' }))).toBe(true);
    });
    const button = await screen.findByRole('button', { name: 'Kinds' });
    expect(rail()?.contains(button)).toBe(true);
  });

  it('draws Kinds nowhere while the profile is read, then in the corner', async () => {
    // A list that answers before the profile must not draw Kinds in the top bar
    // and take it away. And the column is the same element throughout.
    const calls = logged(inCluster());
    const release = deferred('/api/capabilities');
    mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(rail()).toBeNull();
    });
    await settled(calls);
    const before = document.getElementById('menu');
    expect(kinds()).toEqual([]);

    release();
    await inClusterKnown();
    expect(document.getElementById('menu')).toBe(before);
    expect((await screen.findByRole('button', { name: 'Kinds' })).dataset['kindsAt']).toBe('corner');
  });

  it.each(['desktop', 'container'] as const)(
    'leaves a %s with one cluster its chooser, Kinds in the corner',
    async (mode) => {
      stubFetch({ ...routes(one), '/api/capabilities': capabilities(mode) } as never);
      const { router } = mountApp('/');
      await waitFor(() => {
        expect(document.querySelector('#page a[data-cluster="in-cluster"]')).not.toBeNull();
      });
      expect(router.state.location.pathname).toBe('/');
      expect(screen.getByRole('link', { name: 'KubeTower' }).getAttribute('href')).toBe('/');

      await act(async () => {
        await router.navigate('/c/in-cluster');
      });
      const button = await screen.findByRole('button', { name: 'Kinds' });
      expect(button.dataset['kindsAt']).toBe('corner');
      // And it still hides the column, by the button and by Ctrl+B.
      fireEvent.click(button);
      await waitFor(() => {
        expect(document.getElementById('menu')?.dataset['hidden']).toBe('');
      });
      fireEvent.keyDown(window, { key: 'b', ctrlKey: true });
      await waitFor(() => {
        expect(document.getElementById('menu')?.dataset['hidden']).toBeUndefined();
      });
    },
  );

});

/**
 * There is no Cluster page, and its address lands on the home. On the
 * in-cluster console the home goes on to
 * its one cluster, which the block above covers.
 */
describe('the old Cluster page address', () => {
  it.each(['desktop', 'container'] as const)('leads /health to the cluster list on a %s', async (mode) => {
    // The address lands on the console's home, replaced so Back does not
    // return to it.
    stubFetch({ ...routes(one), '/api/capabilities': capabilities(mode) } as never);
    const { router } = mountApp('/health');
    expect(await screen.findByRole('heading', { level: 1, name: 'Clusters' })).toBeDefined();
    expect(router.state.location.pathname).toBe('/');
    expect(router.state.historyAction).toBe('REPLACE');
    expect(document.querySelector('#topbar a[href="/health"]')).toBeNull();
  });
});

/** One route held until the test lets it through, over whatever is stubbed. */
function deferred(path: string): () => void {
  const stubbed = globalThis.fetch;
  let release: () => void = () => {};
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const asked = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
    if ((asked.split('?')[0] ?? asked) === path) await gate;
    return stubbed(input, init);
  });
  return () => {
    release();
  };
}

/** One route whose request is made - and logged - and then lost while `fails()`
 *  says so: the network error `fetch` rejects with, over whatever is stubbed. */
function failing(path: string, fails: () => boolean): void {
  const stubbed = globalThis.fetch;
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const asked = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
    const answer = await stubbed(input, init);
    if ((asked.split('?')[0] ?? asked) === path && fails()) throw new TypeError('Failed to fetch');
    return answer;
  });
}

/**
 * The menu's deferred headings are remembered per cluster in `localStorage`,
 * and a cluster that left the kubeconfig must be let go of: otherwise a
 * desktop that cycles through short-lived clusters fills the origin's quota,
 * and the dock's own tab list is the next write refused.
 */
describe('what the menu remembers per cluster', () => {
  it('lets go of a cluster that is no longer in the list, and keeps the one that is', async () => {
    const snapshot = JSON.stringify({ at: 1, group: { key: 'custom-resources', label: 'CRDs', items: [] } });
    localStorage.setItem('kubetower.nav.gone.custom-resources', snapshot);
    localStorage.setItem('kubetower.nav.in-cluster.custom-resources', snapshot);
    stubFetch(routes(one) as never);
    mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(localStorage.getItem('kubetower.nav.gone.custom-resources')).toBeNull();
    });
    expect(localStorage.getItem('kubetower.nav.in-cluster.custom-resources')).toBe(snapshot);
  });

  it('lets go of nothing on a list that did not answer', async () => {
    const snapshot = JSON.stringify({ at: 1, group: { key: 'custom-resources', label: 'CRDs', items: [] } });
    localStorage.setItem('kubetower.nav.prod.custom-resources', snapshot);
    stubFetch(
      routes({
        body: { state: 'unreadable', reason: { code: 'undecodable', message: 'the kubeconfig would not parse' } },
      }) as never,
    );
    mountApp('/c/in-cluster');
    await waitFor(() => {
      expect(rail()?.textContent).toContain('Could not be read');
    });
    expect(localStorage.getItem('kubetower.nav.prod.custom-resources')).toBe(snapshot);
  });
});
