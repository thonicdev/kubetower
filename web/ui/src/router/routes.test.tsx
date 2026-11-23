import { act, screen, waitFor } from '@testing-library/react';
import type { RouteObject } from 'react-router';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp, stubFetch } from '../testing/harness';
import { patientWithALoadedRunner } from '../testing/loadedRunner';
import type { Extension } from '../extension';
import { buildRoutes } from './routes';

patientWithALoadedRunner();

/**
 * The Shell survives navigation, and sign-in is outside it.
 *
 * Every route under the Shell now reads the API, so these mount the real
 * application against a stubbed `fetch` rather than a bare router. The identity
 * assertions are unchanged and are still the point: a dock that is re-created by
 * a navigation is a killed PTY, and a test that only asserts the element is
 * present passes for both.
 */
const session = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

function stubEverything(): void {
  stubFetch({
    '/api/session': session,
    '/api/clusters': {
      body: {
        state: 'ok',
        items: [
          { name: 'prod', label: 'prod', state: 'ok' },
          { name: 'staging', label: 'staging', state: 'ok' },
        ],
      },
    },
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/staging/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/c/staging/extensions': { body: { state: 'empty', items: [] } },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
});

describe('the Shell sits outside the Outlet', () => {
  it('keeps the very same dock element across a navigation', async () => {
    stubEverything();
    const { router } = mountApp('/c/prod');

    const before = document.getElementById('dock');
    expect(before).not.toBeNull();

    await act(async () => {
      await router.navigate('/c/staging');
    });

    // Identity, not presence. A re-rendered dock is a killed PTY, and a test
    // that only asserts `toBeInTheDocument` passes for both.
    expect(document.getElementById('dock')).toBe(before);
  });

  it('keeps the rail and the menu across a change of cluster', async () => {
    stubEverything();
    const { router } = mountApp('/c/prod');

    const rail = document.getElementById('rail');
    const menu = document.getElementById('menu');

    await act(async () => {
      await router.navigate('/c/staging');
    });

    expect(document.getElementById('rail')).toBe(rail);
    expect(document.getElementById('menu')).toBe(menu);
  });

  it('renders the menu even where there is no cluster', () => {
    stubEverything();
    mountApp('/');
    // Unconditional by design: a menu that vanished on the pages that do not
    // belong to a cluster would come back only with F5.
    expect(document.getElementById('menu')).not.toBeNull();
  });
});

describe('sign-in is the one route outside the Shell', () => {
  it('draws no rail, no menu, no dock and no drawer', () => {
    stubEverything();
    mountApp('/login');

    for (const id of ['rail', 'menu', 'dock', 'drawer']) {
      expect(document.getElementById(id)).toBeNull();
    }
  });
});

/**
 * The pages arrive as chunks of their own, and the Shell does not wait for them.
 *
 * As one bundle the console is around 900 KiB before the sign-in form, and
 * `chunkReload.ts` - the recovery path for a tab left open across an upgrade -
 * only runs where a dynamic import can 404.
 */
describe('the pages arrive as chunks of their own', () => {
  /** Every route in the tree, flattened, named by the address it is declared
   *  at - a layout route by its parent's, an index route with `(index)`. */
  function flatten(routes: RouteObject[], under = ''): { at: string; route: RouteObject }[] {
    return routes.flatMap((route) => {
      const at =
        route.index === true
          ? `${under}/ (index)`
          : route.path === undefined
            ? `${under} (layout)`
            : `${under}/${route.path}`;
      const below = route.path === undefined ? under : `${under}/${route.path}`;
      return [{ at, route }, ...flatten(route.children ?? [], below)];
    });
  }

  it('marks every page lazy, and neither the Shell, the sign-in view nor the cluster list', () => {
    const all = flatten(buildRoutes([]));
    const lazy = all.filter(({ route }) => route.lazy !== undefined).map(({ at }) => at);
    const eager = all.filter(({ route }) => route.lazy === undefined).map(({ at }) => at);

    expect(lazy).toEqual([
      '/settings/:tab',
      '/c/:cluster/ (index)',
      '/c/:cluster/helm',
      '/c/:cluster/helm/:namespace/:name',
      '/c/:cluster/port-forwards',
      '/c/:cluster/:kind',
      '/c/:cluster/:kind/:name',
      '/c/:cluster/:kind/:namespace/:name',
      '/c/:cluster/x',
      '/c/:cluster/x/:key/*',
    ]);
    // What the first paint needs. The sign-in view is the one page reached
    // with a cold cache; the Shell and the cluster list are what an operator
    // sees the moment the session is accepted. A lazy Shell would withhold
    // the rail, the menu and the dock while a page's chunk is on the wire.
    // The second layout is the one over `/` that opens an in-cluster console
    // on its cluster: it decides before any page is drawn, so it cannot wait
    // on a chunk either. `/health` is a redirect to `/`, and a redirect has
    // nothing to load.
    expect(eager).toEqual(['/login', ' (layout)', ' (layout)', '/ (index)', '/health', '/settings', '/c/:cluster']);
  });

  it('uses the function form of lazy, so a chunk that fails to import is an error and not a blank page', () => {
    // With a `lazy` whose import rejects the way a browser rejects a missing
    // chunk, `lazy: { Component }` makes React Router complete the
    // navigation with `errors=none` and an empty outlet - the per-property
    // promises are awaited inside an empty `catch` in its default data
    // strategy - and `RouteError` never renders. With `lazy: () => …` the
    // rejection is the route's error and the boundary reloads the tab. The
    // recovery path in `chunkReload.ts` exists only in the second shape.
    for (const { at, route } of flatten(buildRoutes([]))) {
      if (route.lazy === undefined) continue;
      expect(typeof route.lazy, at).toBe('function');
    }
  });

  it('carries a fallback on each lazy route, so the Shell is drawn before the chunk lands', () => {
    // React Router withholds the *whole* tree while an initial match is lazy
    // unless a match carries `hydrateFallbackElement`: with none, the root
    // renders null and the console is a blank page until the chunk arrives.
    // With one on the lazy route itself, the Shell renders and only the page
    // slot waits.
    for (const { at, route } of flatten(buildRoutes([]))) {
      if (route.lazy === undefined) continue;
      expect(route.hydrateFallbackElement, at).toBeDefined();
    }
  });

  it('draws the Shell at once and the page when its chunk arrives', async () => {
    stubFetch({
      '/api/session': session,
      '/api/clusters': { body: { state: 'ok', items: [] } },
    });
    mountApp('/settings/general');

    // Synchronous, deliberately: the rail and the menu are on screen before
    // any dynamic import has resolved.
    expect(document.getElementById('menu')).not.toBeNull();
    expect(document.getElementById('page')?.textContent).toContain('Reading this page');

    await waitFor(() => {
      expect(document.getElementById('page')?.textContent).not.toContain('Reading this page');
    });
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Settings');
  });
});

/**
 * `x` is reserved for extensions, and an address under it that nothing claims
 * says so rather than falling through to the object route.
 *
 * Seen in a build carrying extras: `/c/{cluster}/x/{key}` for a key with no page
 * in the SPA was matched as `:kind/:name`, and drew *"x" is not a kind this
 * cluster serves* over a notice that the cluster did not answer. Neither was
 * true - no kind was asked about and no cluster was asked anything.
 */
describe('the reserved x segment', () => {
  // The notice is a lazy chunk, and the first test here used to compile it
  // inside its own `findByText`. On a loaded CI runner that alone ran past the
  // wait. Compiled once here, so the tests time the page and not
  // the transform. The chunk test above deliberately does not do this: there,
  // the chunk arriving late is the subject.
  beforeAll(async () => {
    await import('../pages/extension/NoExtensionPage');
  });

  it('says this build has no page for an extra it does not carry, and blames no cluster', async () => {
    stubEverything();
    mountApp('/c/prod/x/argocd');

    const notice = await screen.findByText(/This console build has no page for the extra "argocd"/);
    expect(notice.closest('[data-state]')?.getAttribute('data-state')).toBe('absent');
    // Its own title: *Not installed* is what an extra says about a cluster
    // missing something, and this is about the console.
    const box = notice.closest('[data-state]');
    expect(box?.textContent).toContain('No such page in this console');
    expect(box?.textContent).not.toContain('Not installed');

    const page = document.getElementById('page')?.textContent ?? '';
    expect(page).not.toMatch(/not a kind/i);
    expect(page).not.toMatch(/did not answer|could not be asked/i);
  });

  it('answers a three-segment address under x too, which an object route would otherwise take', async () => {
    // `/c/:cluster/:kind/:namespace/:name` outranks a bare `x/*` splat, so this
    // address was drawn as an object of kind `x` before the route became
    // `x/:key/*`.
    stubEverything();
    mountApp('/c/prod/x/nosuch/deeper');

    expect(await screen.findByText(/This console build has no page for the extra "nosuch"/)).toBeDefined();
    expect(document.getElementById('page')?.textContent ?? '').not.toMatch(/not a kind/i);
  });

  it('draws a registered extension at its own address, not the notice', async () => {
    stubEverything();
    const extension = {
      key: 'argocd',
      nav: { label: 'Argo CD', path: 'x/argocd' },
      routes: [{ path: 'x/argocd', element: <p>the extension page</p> }],
    };
    mountApp('/c/prod/x/argocd', [extension]);

    expect(await screen.findByText('the extension page')).toBeDefined();
    expect(document.getElementById('page')?.textContent ?? '').not.toMatch(/no page for the extra/);
  });

  it('carries a bookmark under a legacy key to the same place under the current key', async () => {
    stubEverything();
    const extension = {
      key: 'runtime',
      legacyKeys: ['jvm'],
      nav: { label: 'Runtime Inspector', path: 'x/runtime' },
      routes: [
        { path: 'x/runtime', element: <p>the extension page</p> },
        { path: 'x/runtime/sub', element: <p>the sub page</p> },
      ],
    };
    const { router } = mountApp('/c/prod/x/jvm/sub?ns=shop#heap', [extension]);

    expect(await screen.findByText('the sub page')).toBeDefined();
    expect(router.state.location.pathname).toBe('/c/prod/x/runtime/sub');
    expect(router.state.location.search).toBe('?ns=shop');
    expect(router.state.location.hash).toBe('#heap');
  });

  it('says a registered extra has no page at an unknown sub-path, and links its own page', async () => {
    // The extra is in this build, so "this build has no page for the extra"
    // would be false; the address is what is wrong.
    stubEverything();
    const extension = {
      key: 'argocd',
      nav: { label: 'Argo CD', path: 'x/argocd' },
      routes: [{ path: 'x/argocd', element: <p>the extension page</p> }],
    };
    mountApp('/c/prod/x/argocd/typo', [extension]);

    const notice = await screen.findByText(/The extra "Argo CD" has no page at this address/);
    expect(notice.closest('[data-state]')?.getAttribute('data-state')).toBe('absent');
    expect(notice.closest('[data-state]')?.textContent).toContain('No such page in this console');
    expect(document.getElementById('page')?.textContent ?? '').not.toMatch(/no page for the extra|not a kind/);
    expect(screen.getByRole('link', { name: 'Open Argo CD' }).getAttribute('href')).toBe('/c/prod/x/argocd');
  });
});

/**
 * An extension's routes land under `x/<key>` and nowhere else.
 *
 * They are spread into `/c/:cluster` beside the core's pages, where a static
 * path outranks the dynamic `:kind`: an extension route `pods` would not be
 * shadowed by the Pods page, it would replace it. `buildRoutes` refuses that at
 * start-up rather than drawing the wrong page.
 */
describe("an extension's routes stay under x/<key>", () => {
  function extensionWith(routes: RouteObject[], key = 'argocd'): Extension {
    return { key, nav: { label: 'Argo CD', path: `x/${key}` }, routes };
  }

  it('accepts x/<key> itself and any path below it', () => {
    const extension = extensionWith([
      { path: 'x/argocd', element: null },
      { path: 'x/argocd/applications/:namespace/:name', element: null },
      { path: 'x/argocd/*', element: null },
    ]);
    expect(() => buildRoutes([extension])).not.toThrow();
  });

  it.each([
    ['a core page', 'pods'],
    ['a core page under a static name', 'helm'],
    ['the dynamic kind route', ':kind'],
    ['another extension', 'x/kubecron'],
    ['a key that only begins with this one', 'x/argocdx'],
    ['the bare x segment', 'x'],
    ['a dynamic key under x', 'x/:key'],
    ['an absolute address', '/c/:cluster/x/argocd'],
  ])('refuses a route at %s (%s), naming the key', (_what, path) => {
    const extension = extensionWith([
      { path: 'x/argocd', element: null },
      { path, element: null },
    ]);
    expect(() => buildRoutes([extension])).toThrow(/extension "argocd".*must be "x\/argocd" or begin with "x\/argocd\/"/);
  });

  it('refuses a pathless route, whose children would land beside the core pages', () => {
    const extension = extensionWith([{ children: [{ path: 'pods', element: null }] }]);
    expect(() => buildRoutes([extension])).toThrow(/extension "argocd" declares a route at no path/);
  });

  it("refuses an index route with no path, which would replace the cluster's own landing", () => {
    const extension = extensionWith([{ index: true, element: null }]);
    expect(() => buildRoutes([extension])).toThrow(/extension "argocd" declares a route at no path/);
  });

  it('judges an index route by its path: under x/<key> it is accepted, outside it refused', () => {
    expect(() => buildRoutes([extensionWith([{ index: true, path: 'x/argocd', element: null }])])).not.toThrow();
    expect(() => buildRoutes([extensionWith([{ index: true, path: 'pods', element: null }])])).toThrow(
      /extension "argocd" declares a route at "pods"/,
    );
  });

  // `argocd?` is the one that got through a list of banned characters: React
  // Router reads `x/argocd?` as an optional segment, which also answers at the
  // bare `/c/:cluster/x`.
  it.each(['', 'a', 'argo/cd', ':key', '*', 'argocd?', 'argo.cd', 'Argocd', '-argocd', 'argocd-', 'argo cd', 'a'.repeat(41)])(
    'refuses a key that is not one literal segment: %j',
    (key) => {
      expect(() => buildRoutes([extensionWith([{ path: `x/${key}`, element: null }], key)])).toThrow(
        /one literal segment/,
      );
    },
  );

  it('accepts a key at either end of the Go registry pattern', () => {
    for (const key of ['ab', 'kube-cron', 'a'.repeat(40)]) {
      expect(() => buildRoutes([extensionWith([{ path: `x/${key}`, element: null }], key)])).not.toThrow();
    }
  });

  it('refuses two extensions that share a key, rather than keeping one of them silently', () => {
    const first = extensionWith([{ path: 'x/argocd', element: null }]);
    const second = extensionWith([{ path: 'x/argocd/other', element: null }]);
    expect(() => buildRoutes([first, second])).toThrow(/Two extensions share the key "argocd"/);
  });

  it("refuses a legacy key that is another extension's key, or that cannot name a route", () => {
    const first = extensionWith([{ path: 'x/argocd', element: null }]);
    const second = { ...extensionWith([{ path: 'x/delivery', element: null }], 'delivery'), legacyKeys: ['argocd'] };
    expect(() => buildRoutes([first, second])).toThrow(/Two extensions share the key "argocd"/);
    const bad = { ...extensionWith([{ path: 'x/delivery', element: null }], 'delivery'), legacyKeys: ['Old?'] };
    expect(() => buildRoutes([bad])).toThrow(/cannot name a route/);
  });
});
