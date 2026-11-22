import { fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp, stubFetch, type StubRoutes } from '../testing/harness';

/**
 * The line that offers an extension a cluster gives a reason for:
 * drawn where the server suggests one and the switch can
 * be pressed, nowhere else, and never as an error.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
  window.localStorage.clear();
});

const capabilities = (mode: 'desktop' | 'shared', replicated = false): StubRoutes => ({
  '/api/capabilities': {
    body: {
      mode,
      localShell: true,
      podExec: true,
      nodeShell: true,
      assistant: false,
      portForward: true,
      preferences: mode !== 'shared',
      replicated,
      readOnly: { writesRefused: false, detailWritesRefused: false },
    },
  },
});

const suggestion = {
  body: {
    state: 'ok',
    items: [{ key: 'redis', label: 'Redis viewer', evidence: '2 Redis or Valkey Services in 1 namespace' }],
  },
};

function routes(extra: StubRoutes = {}): StubRoutes {
  return {
    '/api/session': {
      body: { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' },
    },
    '/api/clusters': {
      body: {
        state: 'ok',
        items: [
          { name: 'prod', label: 'prod', state: 'ok' },
          { name: 'staging', label: 'staging', state: 'ok' },
        ],
      },
    },
    ...capabilities('desktop'),
    '/api/c/prod/extensions/suggestions': suggestion,
    ...extra,
  };
}

const banner = (): Element | null => document.querySelector('[data-suggestion="redis"]');

describe('the suggestion line', () => {
  it('says what was found and offers the switch', async () => {
    stubFetch(routes());
    mountApp('/c/prod');

    await waitFor(() => {
      expect(banner()).not.toBeNull();
    });
    // A label and its evidence, no sentence.
    expect(banner()?.querySelector('p')?.textContent).toBe('Redis viewer 2 Redis or Valkey Services in 1 namespace');
    expect(banner()?.querySelector('[data-suggestion-on]')?.textContent).toBe('Turn on');
  });

  it('draws nothing when there is nothing to suggest', async () => {
    stubFetch(routes({ '/api/c/prod/extensions/suggestions': { body: { state: 'empty', items: [] } } }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(document.getElementById('page')).not.toBeNull();
    });
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(document.querySelector('[data-suggestions]')).toBeNull();
  });

  it('draws no banner for an extra the cluster does not run (the Settings tile says so)', async () => {
    stubFetch(
      routes({
        '/api/c/prod/extensions/suggestions': {
          body: { state: 'ok', items: [{ key: 'certmanager', label: 'Cert Manager', evidence: 'cert-manager absent', absent: true }] },
        },
      }),
    );
    mountApp('/c/prod');

    await waitFor(() => {
      expect(document.getElementById('page')).not.toBeNull();
    });
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(document.querySelector('[data-suggestions]')).toBeNull();
  });

  it('draws nothing when the suggestion could not be read, never an error', async () => {
    stubFetch(
      routes({
        '/api/c/prod/extensions/suggestions': {
          status: 500,
          contentType: 'application/problem+json',
          body: { status: 500, title: 'Internal Server Error', code: 'internal', detail: 'boom' },
        },
      }),
    );
    mountApp('/c/prod');

    await waitFor(() => {
      expect(document.getElementById('page')).not.toBeNull();
    });
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(document.querySelector('[data-suggestions]')).toBeNull();
    expect(document.body.textContent).not.toContain('boom');
  });

  it('is drawn under the shared profile too, where the server decides what this person could turn on', async () => {
    // The switch is registered in a cluster with one replica, and the
    // server suggests only what the chart's values allow and the person holds
    // `use` on.
    stubFetch(routes(capabilities('shared')));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(banner()).not.toBeNull();
    });
    expect(banner()?.querySelector('[data-suggestion-on]')?.textContent).toBe('Turn on');
  });

  it('is not asked for where several replicas register no switch', async () => {
    const asked: string[] = [];
    stubFetch(routes(capabilities('shared', true)));
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      asked.push(String(input));
      return stubbed(input, init);
    });
    mountApp('/c/prod');

    await waitFor(() => {
      expect(asked.some((path) => path.startsWith('/api/capabilities'))).toBe(true);
    });
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(asked.some((path) => path.includes('/suggestions'))).toBe(false);
    expect(banner()).toBeNull();
  });

  it('turns the extension on here and opens its page', async () => {
    const writes: { path: string; body: unknown }[] = [];
    stubFetch(
      routes({
        '/api/c/prod/extensions/redis': { status: 204 },
        '/api/c/prod/nav': {
          body: {
            state: 'ok',
            data: {
              groups: [
                {
                  key: 'extra',
                  label: 'Extra',
                  deferred: false,
                  // Another extra drawn under the same words first: the
                  // page is found by its address, built from the key.
                  items: [
                    { text: 'Redis viewer', href: '/c/prod/x/other-redis-ui' },
                    { text: 'Redis viewer', href: '/c/prod/x/redis' },
                  ],
                },
              ],
            },
          },
        },
      }),
    );
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'PUT') writes.push({ path: String(input), body: JSON.parse(String(init.body)) });
      return stubbed(input, init);
    });
    const { router } = mountApp('/c/prod');

    await waitFor(() => {
      expect(banner()).not.toBeNull();
    });
    const on = banner()?.querySelector('[data-suggestion-on]');
    if (!(on instanceof HTMLButtonElement)) throw new Error('no Turn on button');
    fireEvent.click(on);

    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/prod/x/redis');
    });
    expect(writes).toEqual([{ path: '/api/c/prod/extensions/redis', body: { enabled: true } }]);
  });

  it('"Not now" hides it for this cluster, and only this one', async () => {
    stubFetch(routes({ '/api/c/staging/extensions/suggestions': suggestion }));
    const { router } = mountApp('/c/prod');

    await waitFor(() => {
      expect(banner()).not.toBeNull();
    });
    const later = banner()?.querySelector('[data-suggestion-dismiss]');
    if (!(later instanceof HTMLButtonElement)) throw new Error('no Not now button');
    fireEvent.click(later);
    expect(banner()).toBeNull();

    await router.navigate('/c/staging');
    await waitFor(() => {
      expect(banner()).not.toBeNull();
    });
    await router.navigate('/c/prod');
    // `navigate` settles once the router is at the address; React draws the
    // page after it, in a transition, and until then the page on screen is
    // still staging's, with staging's line. A fixed sleep read it at whatever
    // point a loaded runner had reached, so wait for prod's page to be the one
    // drawn - its tile in the rail is the current one - and read then.
    await waitFor(() => {
      expect(document.querySelector('[data-cluster="prod"][aria-current="page"]')).not.toBeNull();
    });
    expect(banner()).toBeNull();
    // And it stays gone: a "Not now" forgotten a moment after arriving would
    // bring the line back, which prod's first frame alone does not show.
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(banner()).toBeNull();
  });

  describe('several at once', () => {
    const many = (count: number): StubRoutes => ({
      '/api/c/prod/extensions/suggestions': {
        body: {
          state: 'ok',
          items: ['a', 'b', 'c', 'd'].slice(0, count).map((key) => ({ key, label: `Extra ${key}`, evidence: `${key} found` })),
        },
      },
    });
    const single = (key: string): Element | null => document.querySelector(`[data-suggestion="${key}"]`);
    const group = (): Element | null => document.querySelector('[data-suggestion-group]');
    const dismiss = (where: Element | null): void => {
      const later = where?.querySelector('[data-suggestion-dismiss]');
      if (!(later instanceof HTMLButtonElement)) throw new Error('no Not now button');
      fireEvent.click(later);
    };

    it('keeps one line each for two', async () => {
      stubFetch(routes(many(2)));
      mountApp('/c/prod');
      await waitFor(() => {
        expect(single('a')).not.toBeNull();
      });
      expect(single('b')).not.toBeNull();
      expect(group()).toBeNull();
    });

    it('folds three into one line that links to Settings > Extra for the cluster', async () => {
      stubFetch(routes(many(3)));
      mountApp('/c/prod');
      await waitFor(() => {
        expect(group()).not.toBeNull();
      });
      expect(group()?.querySelector('p')?.textContent).toBe('3 extras Extra a · Extra b · Extra c');
      expect(single('a')).toBeNull();
      expect(group()?.querySelector('[data-suggestion-see]')?.getAttribute('href')).toBe('/settings/extra?cluster=prod');
    });

    it('"Not now" on the group dismisses those extras for this cluster, and a later one comes back alone', async () => {
      stubFetch(routes(many(3)));
      const { view } = mountApp('/c/prod');
      await waitFor(() => {
        expect(group()).not.toBeNull();
      });
      dismiss(group());
      expect(group()).toBeNull();
      expect(document.querySelector('[data-suggestions]')).toBeNull();
      expect(JSON.parse(window.localStorage.getItem('kubetower.suggestions.dismissed') ?? '[]')).toEqual([
        'prod/a',
        'prod/b',
        'prod/c',
      ]);
      view.unmount();

      // Reloaded with a fourth extra suggested: only it is drawn.
      stubFetch(routes(many(4)));
      mountApp('/c/prod');
      await waitFor(() => {
        expect(single('d')).not.toBeNull();
      });
      expect(single('a')).toBeNull();
      expect(group()).toBeNull();
    });
  });
});
