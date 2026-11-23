import { QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, waitFor } from '@testing-library/react';
import { createBrowserRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { KeyboardStack } from '../../keyboard/KeyboardStack';
import { resetKeyboardStack } from '../../keyboard/stack';
import { createQueryClient } from '../../query/client';
import { buildRoutes } from '../../router/routes';
import { ShellProvider } from '../../shell/ShellContext';
import { originOf } from '../../shell/settingsOrigin';
import { mountApp, stubFetch, type StubRoutes } from '../../testing/harness';

/**
 * The way back from Settings to the page it was opened from (#13): a Back
 * control, Escape, one history entry for the whole of Settings, and the page's
 * scroll put back.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
});

const routes: StubRoutes = {
  '/api/session': {
    body: { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' },
  },
  '/api/clusters': {
    body: {
      state: 'ok',
      items: [
        { name: 'prod', label: 'Production', state: 'ok' },
        { name: 'staging', label: 'staging', state: 'ok' },
      ],
    },
  },
  '/api/settings': {
    body: {
      general: { archiveEnabled: false, captureLogs: false, retentionDays: 7 },
      retentionChoices: [{ days: 7, label: '7 days' }],
      archive: { state: 'empty' },
      nodeShell: { enabled: false, available: false, image: '', defaultImage: 'alpine:3.24' },
      info: {
        statePath: '/state',
        statePersistent: true,
        kubeconfigFiles: [],
        listenAddress: '127.0.0.1:5823',
        withoutPassword: true,
        version: 'kubetower 0.1.2',
        development: false,
        repositoryUrl: 'https://github.com/thonicdev/kubetower',
        documentationUrl: 'https://example.com/docs',
      },
    },
  },
  '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
  '/api/c/staging/extensions': { body: { state: 'empty', items: [] } },
};

/** A console page that reads next to nothing: an extra this build does not carry. */
const ORIGIN = '/c/prod/x/nope?namespace=shop&search=api#top';

type AnyRouter = Pick<ReturnType<typeof mountApp>['router'], 'state'>;

function where(router: AnyRouter): string {
  const { pathname, search, hash } = router.state.location;
  return `${pathname}${search}${hash}`;
}

function back(): HTMLButtonElement | null {
  return document.querySelector<HTMLButtonElement>('[data-settings-back]');
}

async function settingsLink(): Promise<HTMLAnchorElement> {
  let link: HTMLAnchorElement | null = null;
  await waitFor(() => {
    link = document.querySelector<HTMLAnchorElement>('a[title="Settings"]');
    expect(link).not.toBeNull();
  });
  return link as unknown as HTMLAnchorElement;
}

/** Open Settings from the top bar, as a person would. */
async function openFromTopbar(router: AnyRouter): Promise<void> {
  fireEvent.click(await settingsLink());
  await waitFor(() => {
    expect(router.state.location.pathname).toBe('/settings/general');
  });
  await waitFor(() => {
    expect(back()).not.toBeNull();
  });
}

describe('Settings opened from a console page', () => {
  it('draws a plain Back control, the address in its tooltip, and returns to that exact address', async () => {
    stubFetch(routes);
    const { router } = mountApp(ORIGIN);
    await openFromTopbar(router);

    expect(back()?.getAttribute('title')).toBe(ORIGIN);
    expect(back()?.textContent).toBe('Back');
    expect(back()?.querySelector('svg use')?.getAttribute('href')).toBe('#i-back');

    fireEvent.click(back() as HTMLButtonElement);
    await waitFor(() => {
      expect(where(router)).toBe(ORIGIN);
    });
  });

  it('keeps one history entry across its tabs, so one Back leaves it', async () => {
    stubFetch(routes);
    const { router } = mountApp(ORIGIN);
    await openFromTopbar(router);

    fireEvent.click(document.querySelector('nav[aria-label="Settings sections"] a[href^="/settings/infos"]') as Element);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/settings/infos');
    });
    expect(router.state.historyAction).toBe('REPLACE');
    // The way back went along with the tab.
    expect(back()?.getAttribute('title')).toBe(ORIGIN);

    await act(async () => {
      await router.navigate(-1);
    });
    await waitFor(() => {
      expect(where(router)).toBe(ORIGIN);
    });
  });

  it('goes back on Escape, unless a field is being typed in', async () => {
    stubFetch(routes);
    const { router } = mountApp(ORIGIN);
    await openFromTopbar(router);

    const field = document.createElement('input');
    document.getElementById('page')?.appendChild(field);
    field.focus();
    fireEvent.keyDown(field, { key: 'Escape' });
    expect(router.state.location.pathname).toBe('/settings/general');
    field.remove();

    fireEvent.keyDown(document.body, { key: 'Escape' });
    await waitFor(() => {
      expect(where(router)).toBe(ORIGIN);
    });
  });

  it('goes back on Escape from a checkbox, which takes no typing', async () => {
    stubFetch(routes);
    const { router } = mountApp(ORIGIN);
    await openFromTopbar(router);
    const box = document.createElement('input');
    box.type = 'checkbox';
    document.getElementById('page')?.appendChild(box);
    box.focus();
    fireEvent.keyDown(box, { key: 'Escape' });
    await waitFor(() => {
      expect(where(router)).toBe(ORIGIN);
    });
    box.remove();
  });

  it('keeps the way back through a reload', async () => {
    stubFetch(routes);
    const { router } = mountApp(ORIGIN);
    await openFromTopbar(router);
    // What a reload reads: the same entry, with its state.
    const { state } = router.state.location;
    await act(async () => {
      await router.navigate('/settings/general', { replace: true, state });
    });
    expect(back()?.getAttribute('title')).toBe(ORIGIN);
  });

  it("puts the page's scroll back once it is left", async () => {
    stubFetch(routes);
    const { router } = mountApp(ORIGIN);
    const region = document.getElementById('page') as HTMLElement;
    // jsdom lays nothing out: a scroll position is held by hand.
    let top = 0;
    Object.defineProperty(region, 'scrollTop', {
      configurable: true,
      get: () => top,
      set: (value: number) => {
        top = value;
      },
    });
    top = 640;
    await openFromTopbar(router);
    top = 0;

    fireEvent.click(back() as HTMLButtonElement);
    await waitFor(() => {
      expect(where(router)).toBe(ORIGIN);
    });
    await waitFor(() => {
      expect(top).toBe(640);
    });
  });
});

describe('every way into Settings carries the way back', () => {
  /** Each entry point's test fails when that entry stops carrying the origin:
   *  Settings then draws no Back control. */
  const capabilities = {
    '/api/capabilities': {
      body: {
        mode: 'desktop',
        localShell: true,
        podExec: true,
        nodeShell: true,
        assistant: false,
        portForward: true,
        preferences: true,
        replicated: false,
        readOnly: { writesRefused: false, detailWritesRefused: false },
      },
    },
  };

  it('from the account menu', async () => {
    // The account menu carries Settings where the top bar holds the session:
    // one cluster, and a console one signs back in to.
    stubFetch({
      ...routes,
      '/api/clusters': { body: { state: 'ok', items: [{ name: 'prod', label: 'Production', state: 'ok' }] } },
      '/api/auth/methods': { body: { password: true, sso: { state: 'off' } } },
    });
    const { router } = mountApp(ORIGIN);
    let trigger: HTMLElement | null = null;
    await waitFor(() => {
      trigger = document.querySelector<HTMLElement>('[title="Signed in as alice"]');
      expect(trigger).not.toBeNull();
    });
    fireEvent.click(trigger as unknown as HTMLElement);
    let item: HTMLElement | undefined;
    await waitFor(() => {
      item = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((one) => one.textContent === 'Settings');
      expect(item).toBeDefined();
    });
    fireEvent.click(item as HTMLElement);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/settings/general');
    });
    await waitFor(() => {
      expect(back()?.getAttribute('title')).toBe(ORIGIN);
    });
  });

  it("from the suggestion banner's See", async () => {
    stubFetch({
      ...routes,
      ...capabilities,
      '/api/c/prod/extensions/suggestions': {
        body: {
          state: 'ok',
          items: [
            { key: 'redis', label: 'Redis viewer', evidence: '2 Redis Services in 1 namespace' },
            { key: 'cnpg', label: 'Postgres (CNPG)', evidence: '3 clusters in 2 namespaces' },
            { key: 'security', label: 'Security Center', evidence: 'reports in 6 namespaces' },
          ],
        },
      },
    });
    const { router } = mountApp(ORIGIN);
    let see: HTMLElement | null = null;
    await waitFor(() => {
      see = document.querySelector<HTMLElement>('[data-suggestion-see]');
      expect(see).not.toBeNull();
    });
    fireEvent.click(see as unknown as HTMLElement);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/settings/extra');
    });
    expect(originOf(router.state.location)?.path).toBe(ORIGIN);
  });

  it("from the rail's tiles while in Settings › Extra, replacing the entry", async () => {
    const listing = {
      body: { state: 'ok', items: [{ key: 'alfa', label: 'Alfa Center', enabled: false, pinned: false, writable: true, settings: [] }] },
    };
    stubFetch({ ...routes, ...capabilities, '/api/c/prod/extensions': listing, '/api/c/staging/extensions': listing });
    const { router } = mountApp(ORIGIN);
    await openFromTopbar(router);
    let tab: HTMLElement | null = null;
    await waitFor(() => {
      tab = document.querySelector<HTMLElement>('nav[aria-label="Settings sections"] a[href^="/settings/extra"]');
      expect(tab).not.toBeNull();
    });
    fireEvent.click(tab as unknown as HTMLElement);
    let tile: HTMLElement | null = null;
    await waitFor(() => {
      tile = document.querySelector<HTMLElement>('a[href="/settings/extra?cluster=staging"]');
      expect(tile).not.toBeNull();
    });
    fireEvent.click(tile as unknown as HTMLElement);
    await waitFor(() => {
      expect(router.state.location.search).toBe('?cluster=staging');
    });
    expect(router.state.historyAction).toBe('REPLACE');
    await waitFor(() => {
      expect(back()?.getAttribute('title')).toBe(ORIGIN);
    });
  });
});

describe('under the browser router', () => {
  it('goes back through the history, so the entry below is the page itself', async () => {
    stubFetch(routes);
    window.history.replaceState(null, '', ORIGIN);
    const router = createBrowserRouter(buildRoutes([]));
    render(
      <QueryClientProvider client={createQueryClient()}>
        <KeyboardStack>
          <ShellProvider extensions={[]}>
            <RouterProvider router={router} />
          </ShellProvider>
        </KeyboardStack>
      </QueryClientProvider>,
    );
    await openFromTopbar(router);
    const below = (window.history.state as { idx: number }).idx - 1;
    fireEvent.click(document.querySelector('nav[aria-label="Settings sections"] a[href^="/settings/infos"]') as Element);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/settings/infos');
    });

    fireEvent.click(back() as HTMLButtonElement);
    await waitFor(() => {
      expect(where(router)).toBe(ORIGIN);
    });
    // A step back, not a new entry on top of Settings.
    expect(router.state.historyAction).toBe('POP');
    expect((window.history.state as { idx: number }).idx).toBe(below);
    router.dispose();
    window.history.replaceState(null, '', '/');
  });
});

describe("the page's scroll after the browser's Back", () => {
  it('is put back too', async () => {
    stubFetch(routes);
    const { router } = mountApp(ORIGIN);
    const region = document.getElementById('page') as HTMLElement;
    let top = 0;
    Object.defineProperty(region, 'scrollTop', {
      configurable: true,
      get: () => top,
      set: (value: number) => {
        top = value;
      },
    });
    top = 480;
    await openFromTopbar(router);
    top = 0;

    await act(async () => {
      await router.navigate(-1);
    });
    await waitFor(() => {
      expect(where(router)).toBe(ORIGIN);
    });
    expect(router.state.historyAction).toBe('POP');
    await waitFor(() => {
      expect(top).toBe(480);
    });
  });
});

describe('Settings opened directly', () => {
  it('draws no Back control', async () => {
    stubFetch(routes);
    mountApp('/settings/general');
    await waitFor(() => {
      expect(document.querySelector('h1')?.textContent).toBe('Settings');
    });
    expect(back()).toBeNull();
  });

  it('keeps the address on Escape', async () => {
    stubFetch(routes);
    const { router } = mountApp('/settings/infos');
    await waitFor(() => {
      expect(document.querySelector('h1')?.textContent).toBe('Settings');
    });
    fireEvent.keyDown(document.body, { key: 'Escape' });
    expect(router.state.location.pathname).toBe('/settings/infos');
  });
});

describe('/settings', () => {
  it('lands on General with the way back it was opened with', async () => {
    stubFetch(routes);
    const { router } = mountApp(ORIGIN);
    fireEvent.click(await settingsLink());
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/settings/general');
    });
    expect(originOf(router.state.location)?.path).toBe(ORIGIN);
  });
});

describe('the origin', () => {
  it('is read defensively from whatever an entry holds', () => {
    expect(originOf({ state: null })).toBeUndefined();
    expect(originOf({ state: { from: { path: 'https://elsewhere.example' } } })).toBeUndefined();
    expect(originOf({ state: { from: { path: '//elsewhere.example/x' } } })).toBeUndefined();
    expect(originOf({ state: { from: { path: '/\\elsewhere.example/x' } } })).toBeUndefined();
    expect(originOf({ state: { from: { path: '/c/prod/pods', key: 'k', idx: 3, scroll: -5 } } })).toEqual({
      path: '/c/prod/pods',
      key: 'k',
      idx: 3,
      scroll: 0,
    });
  });
});
