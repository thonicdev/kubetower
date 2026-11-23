import { QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, waitFor } from '@testing-library/react';
import { createBrowserRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { KeyboardStack } from '../../keyboard/KeyboardStack';
import { resetKeyboardStack } from '../../keyboard/stack';
import { createQueryClient } from '../../query/client';
import { buildRoutes } from '../../router/routes';
import { ShellProvider } from '../../shell/ShellContext';
import { originLabel, originOf } from '../../shell/settingsOrigin';
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
  it('draws a Back control naming the page, and returns to its exact address', async () => {
    stubFetch(routes);
    const { router } = mountApp(ORIGIN);
    await openFromTopbar(router);

    expect(back()?.getAttribute('title')).toBe(ORIGIN);
    expect(back()?.textContent).toBe('Production › nope');
    expect(back()?.getAttribute('aria-label')).toBe('Back to Production › nope');

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
    expect(originOf({ state: { from: { path: '/c/prod/pods', key: 'k', idx: 3, scroll: -5 } } })).toEqual({
      path: '/c/prod/pods',
      key: 'k',
      idx: 3,
      scroll: 0,
    });
  });

  it('is named by its cluster and page', () => {
    expect(originLabel('/')).toBe('Clusters');
    expect(originLabel('/c/prod')).toBe('prod');
    expect(originLabel('/c/prod/pods?namespace=shop')).toBe('prod › pods');
    expect(originLabel('/c/prod/pods/shop/api-0')).toBe('prod › pods');
    expect(originLabel('/c/arn%3Aaws%3Aeks/x/argocd')).toBe('arn:aws:eks › argocd');
    expect(originLabel('/health')).toBe('health');
    expect(originLabel('/c/prod/pods', (name) => (name === 'prod' ? 'Production' : name))).toBe('Production › pods');
  });
});
