import { cleanup, fireEvent, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { Extension } from '../extension';
import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp, stubFetch, type StubReply } from '../testing/harness';
import { resetMenuMode, resetOpenGroups } from './menuMemory';
import { resetNavMemory } from './navMemory';

// The snapshot store is wiped on both sides of every test, and the tree is
// unmounted before the wipe: the write into `navMemory` is a passive effect,
// and under the load of the whole suite it can flush after this hook has
// already reset the store - so the next test would read a snapshot nobody in
// it had written, draw it, and with a fresh stamp never
// asked the stub. `cleanup()` first, so nothing of the old tree runs after the
// reset; the `beforeEach` is the guarantee the order does not depend on.
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
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

const clusters = {
  body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] },
};

const anExtra: Extension = {
  key: 'an-extra',
  nav: { label: 'An extra', path: 'x/an-extra' },
  routes: [{ path: 'x/an-extra', element: null }],
};

function menu(): HTMLElement {
  const element = document.getElementById('menu');
  if (element === null) throw new Error('#menu is missing');
  return element;
}

/** Lets the `toggle` events a disclosure queues when it opens or shuts land
 *  before anything is read back. They are tasks, not part of the render, and
 *  an assertion made before they fire proves nothing about them. */
async function settled(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 20));
}

describe('the menu is a cluster read and is drawn as one', () => {
  it('shows the groups discovery returned', async () => {
    stubFetch({
      '/api/session': session,
      '/api/clusters': clusters,
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
      '/api/c/prod/nav': {
        body: {
          state: 'ok',
          data: {
            groups: [
              {
                key: 'workloads',
                label: 'Workloads',
                items: [{ text: 'Pods', href: '/c/prod/r/pods' }],
              },
            ],
          },
        },
      },
    });

    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });
    expect(menu().querySelector('a[href="/c/prod/r/pods"]')).not.toBeNull();
  });

  it('says why the menu is short rather than being quietly short', async () => {
    stubFetch({
      '/api/session': session,
      '/api/clusters': clusters,
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
      '/api/c/prod/nav': {
        body: {
          state: 'forbidden',
          reason: { code: 'verb_denied', message: 'discovery is not permitted for this account' },
        },
      },
    });

    mountApp('/c/prod');

    // "A cluster whose discovery is refused shows its built-in menu and says why
    // the rest is missing, rather than showing a menu that is quietly short."
    await waitFor(() => {
      expect(menu().textContent).toContain('Refused by the cluster');
    });
    expect(menu().textContent).toContain('discovery is not permitted for this account');
  });

  it('carries a group note, which is how a short group explains itself', async () => {
    stubFetch({
      '/api/session': session,
      '/api/clusters': clusters,
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
      '/api/c/prod/nav': {
        body: {
          state: 'ok',
          data: {
            groups: [
              {
                key: 'custom',
                label: 'Custom resources',
                items: [],
                note: 'four CRD groups could not be listed',
              },
            ],
          },
        },
      },
    });

    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('four CRD groups could not be listed');
    });
  });
});

/**
 * The menu draws no *Extras* section from the extensions read: the enveloped
 * read behind one would draw *Could not be asked* into the menu whenever a
 * cluster was slow or unreachable.
 *
 * The claim is kept as a test rather than as a
 * comment, because what has to be caught is the query coming back by accident.
 * The enforcement never lived here — `extension.ts`: a route that is not enabled
 * answers 404 whether or not it was ever drawn.
 */
describe('this version offers no extras, and does not ask about them', () => {
  const nav = { body: { state: 'ok', data: { groups: [] } } };

  it('draws nothing for an extra the build carries, and never reads the enablement list', async () => {
    stubFetch({
      '/api/session': session,
      '/api/clusters': clusters,
      '/api/c/prod/nav': nav,
      // Answers, and enables it. Nothing may draw it, and nothing may ask.
      '/api/c/prod/extensions': {
        body: { state: 'ok', items: [{ key: 'an-extra', label: 'An extra', enabled: true }] },
      },
    });

    const asked: string[] = [];
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      asked.push(String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url));
      return stubbed(input, init);
    });

    mountApp('/c/prod', [anExtra]);

    // Wait for the menu to have been asked for - the one request that is
    // allowed - rather than for a sentence, so that the assertion below is about
    // a menu that has loaded and not about one that has not started.
    await waitFor(() => {
      expect(asked.some((one) => one.endsWith('/api/c/prod/nav'))).toBe(true);
    });
    expect(menu().textContent).not.toContain('An extra');
    expect(menu().textContent).not.toContain('Extras');
    expect(asked.filter((one) => one.includes('/extensions'))).toEqual([]);
  });

  it('says nothing about extras when the cluster cannot be reached', async () => {
    // No stub for /extensions at all: the harness answers 404 for an unstubbed
    // path, so anything that still asked would put a refusal on screen.
    stubFetch({
      '/api/session': session,
      '/api/clusters': clusters,
      '/api/c/prod/nav': nav,
    });

    mountApp('/c/prod', [anExtra]);

    await waitFor(() => {
      expect(document.getElementById('menu')).not.toBeNull();
    });
    expect(menu().textContent).not.toContain('Could not be asked');
    expect(menu().textContent).not.toContain('Not refreshed');
  });
});

/**
 * The element is unconditional; its content is not. On `/health` and
 * `/settings/*` a sentence there would be a 192px band between the rail and a
 * page that has no relation to any cluster. The `<nav>` stays, empty, and `empty:hidden` is
 * what takes it off screen; the page takes the width.
 */
describe('the menu element is unconditional, and empty without a cluster', () => {
  it('exists and holds nothing where there is no cluster at all', async () => {
    stubFetch({ '/api/session': session, '/api/clusters': clusters });
    mountApp('/');
    expect(document.getElementById('menu')).not.toBeNull();
    // Settled, not merely "not yet": the cluster list is the chooser, and the
    // sentence saying so belongs there and nowhere else.
    await waitFor(() => {
      expect(document.body.textContent).toContain('prod');
    });
    expect(menu().childElementCount).toBe(0);
    expect(menu().textContent).toBe('');
  });

  it.each(['/health', '/settings/general'])('holds nothing on %s', async (path) => {
    stubFetch({ '/api/session': session, '/api/clusters': clusters });
    mountApp(path);
    await waitFor(() => {
      expect(document.getElementById('page')?.textContent).not.toBe('');
    });
    expect(menu().childElementCount).toBe(0);
    expect(menu().textContent).toBe('');
  });

  it('holds the groups where there is one', async () => {
    stubFetch({
      '/api/session': session,
      '/api/clusters': clusters,
      '/api/c/prod/nav': {
        body: {
          state: 'ok',
          data: {
            groups: [{ key: 'workloads', label: 'Workloads', items: [{ text: 'Pods', href: '/c/prod/pods' }] }],
          },
        },
      },
    });
    mountApp('/c/prod/pods');
    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });
    expect(menu().childElementCount).toBeGreaterThan(0);
  });
});

/**
 * A group opened by hand stays open when another kind is clicked, and across a
 * reload. `menuMemory.ts` carries the argument; these are the behaviors it
 * buys.
 */
describe('the menu groups collapse, and what a person arranged is remembered', () => {
  const nav = {
    body: {
      state: 'ok',
      data: {
        groups: [
          { key: 'cluster', label: 'Cluster', items: [{ text: 'Nodes', href: '/c/prod/nodes' }] },
          {
            key: 'workloads',
            label: 'Workloads',
            items: [{ text: 'Pods', href: '/c/prod/pods' }],
          },
        ],
      },
    },
  };

  function stub(): void {
    stubFetch({
      '/api/session': session,
      '/api/clusters': clusters,
      '/api/c/prod/nav': nav,
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    });
  }

  function group(key: string): HTMLDetailsElement {
    const element = menu().querySelector<HTMLDetailsElement>(`details[data-group="${key}"]`);
    if (element === null) throw new Error(`no group ${key}`);
    return element;
  }

  it('draws only the group holding the page open on a browser that has never arranged it', async () => {
    // Not every group open, a column of headings with the page somewhere inside.
    stub();
    mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(group('workloads').open).toBe(true);
    });
    await settled();
    expect(group('cluster').open).toBe(false);
    // Drawn, not arranged: nothing is written until a person's own gesture.
    expect(localStorage.getItem('kubetower.menu.open')).toBeNull();
  });

  it('remembers a group closed by hand, across a fresh mount', async () => {
    // Somebody who has opened both, which is the arrangement there is to
    // close one of: an unarranged menu has only the page's group open.
    localStorage.setItem('kubetower.menu.open', '["cluster","workloads"]');
    stub();
    const first = mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });

    const summary = group('workloads').querySelector('summary');
    if (summary === null) throw new Error('the group has no summary');
    fireEvent.click(summary);

    await waitFor(() => {
      expect(group('workloads').open).toBe(false);
    });
    expect(JSON.parse(localStorage.getItem('kubetower.menu.open') ?? 'null')).toStrictEqual([
      'cluster',
    ]);

    first.view.unmount();
    resetOpenGroups();
    cleanup();

    stub();
    mountApp('/c/prod');
    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });

    // The whole point: the arrangement outlives the click.
    expect(group('workloads').open).toBe(false);
    expect(group('cluster').open).toBe(true);
  });

  it('does not follow a fold made in another window, and writes nothing over it', async () => {
    localStorage.setItem('kubetower.menu.open', '["cluster","workloads"]');
    stub();
    mountApp('/c/prod');
    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });
    expect(group('workloads').open).toBe(true);

    // Another window folds Workloads: the browser writes the shared storage and
    // sends `storage` to every other window of the origin, this one included.
    // A fold in one window never moves the menu in another.
    localStorage.setItem('kubetower.menu.open', '["cluster"]');
    window.dispatchEvent(
      new StorageEvent('storage', {
        key: 'kubetower.menu.open',
        newValue: '["cluster"]',
        storageArea: localStorage,
      }),
    );
    await settled();

    expect(group('workloads').open).toBe(true);
    expect(group('cluster').open).toBe(true);
    // And this window, having made no gesture, has not written its own
    // arrangement back over the one the other window saved.
    expect(localStorage.getItem('kubetower.menu.open')).toBe('["cluster"]');
  });

  it('opens the group holding the page being arrived at, whatever was remembered', async () => {
    localStorage.setItem('kubetower.menu.open', '["cluster"]');
    stub();
    const app = mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(group('workloads').open).toBe(true);
    });

    // The group holding the current page is open so that following a link
    // cannot land you somewhere invisible in the menu, and that outranks a
    // remembered arrangement - for the visit. Writing it down would make a
    // visit a setting nobody chose.
    await settled();
    expect(localStorage.getItem('kubetower.menu.open')).toBe('["cluster"]');

    // And the visit ends with the page. Workloads was folded; standing
    // on Pods showed it, and leaving Pods gives the fold back.
    await app.router.navigate('/c/prod/nodes');
    await waitFor(() => {
      expect(group('workloads').open).toBe(false);
    });
    expect(group('cluster').open).toBe(true);
    expect(localStorage.getItem('kubetower.menu.open')).toBe('["cluster"]');
  });

  it('keeps closed the group closed while standing in it', async () => {
    // The visit is what holds it open, so the gesture has to end the visit or
    // the group would spring back on the next render.
    localStorage.setItem('kubetower.menu.open', '["cluster"]');
    stub();
    mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(group('workloads').open).toBe(true);
    });
    await settled();
    const summary = group('workloads').querySelector('summary');
    if (summary === null) throw new Error('the group has no summary');
    fireEvent.click(summary);

    await waitFor(() => {
      expect(group('workloads').open).toBe(false);
    });
    await settled();
    expect(group('workloads').open).toBe(false);
    expect(localStorage.getItem('kubetower.menu.open')).toBe('["cluster"]');
  });

  it('draws the menu when storage refuses to be read at all', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      // What a browser throws when site data is blocked. The catch is untyped, so
      // a plain Error exercises the same branch.
      throw new Error('access to storage is denied by this profile');
    });
    stub();
    mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });
    // The memory is what is lost in a private window. Not the menu, and not
    // the page's own group, which the visit opens with or without storage.
    expect(group('workloads').open).toBe(true);
    vi.restoreAllMocks();
  });
});

/**
 * The custom resource heading: closed, filled when it is opened, and three
 * answers that do not read as each other.
 *
 * A mapping that dropped the flag saying this group's contents are fetched
 * separately, or the nested level where every custom kind actually lives, would
 * leave *Custom Resources* with zero items on every page, and the one condition
 * that fills it inline - being on a custom kind's page - unreachable, because
 * the menu would never offer a link to one: a deadlock that reads exactly like
 * a cluster with no CRDs.
 */
describe('the custom resource heading is deferred, and fills when it is opened', () => {
  const nav = {
    body: {
      state: 'ok',
      data: {
        groups: [
          {
            key: 'workloads',
            label: 'Workloads',
            deferred: false,
            items: [{ text: 'Pods', href: '/c/prod/pods' }],
          },
          { key: 'custom-resources', label: 'Custom Resources', deferred: true, items: [] },
        ],
      },
    },
  };

  const filled = {
    body: {
      state: 'ok',
      data: {
        key: 'custom-resources',
        label: 'Custom Resources',
        deferred: false,
        items: [],
        groups: [
          {
            key: 'cert-manager.io',
            label: 'cert-manager.io',
            deferred: false,
            items: [
              { text: 'Certificates', href: '/c/prod/certificates.cert-manager.io' },
            ],
          },
        ],
      },
    },
  };

  function stub(group: StubReply): void {
    stubFetch({
      '/api/session': session,
      '/api/clusters': clusters,
      '/api/c/prod/nav': nav,
      '/api/c/prod/nav/custom-resources': group,
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    });
  }

  function group(key: string): HTMLDetailsElement {
    const element = menu().querySelector<HTMLDetailsElement>(`details[data-group="${key}"]`);
    if (element === null) throw new Error(`no group ${key}`);
    return element;
  }

  it('draws it shut where every other group is open, and asks for nothing', async () => {
    localStorage.setItem('kubetower.menu.open', '["workloads"]');
    stub(filled);
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });

    // The measurement this whole arrangement exists for: filling the heading
    // lists the cluster's CustomResourceDefinitions - 23.7 MB and 603-668 ms on
    // one production cluster - and it is closed on every page that is not one of
    // its own.
    expect(group('workloads').open).toBe(true);
    expect(group('custom-resources').open).toBe(false);
    expect(menu().textContent).not.toContain('cert-manager.io');
  });

  it('fills it when it is opened', async () => {
    stub(filled);
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });

    const summary = group('custom-resources').querySelector('summary');
    if (summary === null) throw new Error('the heading has no summary');
    fireEvent.click(summary);

    // The kinds, and the API group they are nested under - which is where they
    // live.
    await waitFor(() => {
      expect(menu().textContent).toContain('Certificates');
    });
    expect(menu().textContent).toContain('cert-manager.io');
    expect(
      menu().querySelector('a[href="/c/prod/certificates.cert-manager.io"]'),
    ).not.toBeNull();
  });

  it('draws an API group in the size of its kinds, told apart by color and weight', async () => {
    stub(filled);
    mountApp('/c/prod');
    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });
    const summary = group('custom-resources').querySelector('summary');
    if (summary === null) throw new Error('the heading has no summary');
    fireEvent.click(summary);
    await waitFor(() => {
      expect(menu().textContent).toContain('Certificates');
    });

    const heading = group('cert-manager.io').querySelector('summary');
    const kind = menu().querySelector('a[href="/c/prod/certificates.cert-manager.io"]');
    if (heading === null || kind === null) throw new Error('no group heading or kind');
    expect(heading.classList.contains('text-sm')).toBe(true);
    expect(heading.classList.contains('text-2xs')).toBe(false);
    expect(kind.classList.contains('text-sm')).toBe(true);
    expect(heading.classList.contains('text-muted')).toBe(true);
    expect(heading.classList.contains('font-medium')).toBe(true);
  });

  it('draws an API group as one truncated, titled row with no triangle, toggled by a click on the row', async () => {
    const long = 'a-very-long-api-group-name.subdomain.example.networking.k8s.io';
    const longKind = 'VeryLongCustomResourceDefinitionKindNameThatCannotFit';
    stub({
      body: {
        state: 'ok',
        data: {
          key: 'custom-resources',
          label: 'Custom Resources',
          deferred: false,
          items: [],
          groups: [
            {
              key: long,
              label: long,
              deferred: false,
              items: [{ text: longKind, href: `/c/prod/longs.${long}` }],
            },
          ],
        },
      },
    });
    mountApp('/c/prod');
    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });
    const top = group('custom-resources').querySelector('summary');
    if (top === null) throw new Error('the heading has no summary');
    fireEvent.click(top);
    await waitFor(() => {
      expect(menu().textContent).toContain(longKind);
    });

    const nested = group(long);
    const row = nested.querySelector('summary');
    const name = row?.querySelector('h3');
    if (row === null || name === null || name === undefined) throw new Error('no group row');
    expect(row.querySelector('svg')).toBeNull();
    expect(row.className).toContain('list-none');
    expect(row.getAttribute('title')).toBe(long);
    expect(name.className).toContain('truncate');
    expect(name.className).not.toContain('wrap');
    const kind = menu().querySelector(`a[href="/c/prod/longs.${long}"]`);
    expect(kind?.getAttribute('title')).toBe(longKind);
    expect(kind?.querySelector('span')?.className).toContain('truncate');

    expect((nested as HTMLDetailsElement).open).toBe(false);
    fireEvent.click(row);
    await waitFor(() => {
      expect((group(long) as HTMLDetailsElement).open).toBe(true);
    });
  });

  it('counts the kinds of the opened group as matches under a search', async () => {
    stub(filled);
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });
    const summary = group('custom-resources').querySelector('summary');
    if (summary === null) throw new Error('the heading has no summary');
    fireEvent.click(summary);
    await waitFor(() => {
      expect(menu().textContent).toContain('Certificates');
    });

    const box = menu().querySelector<HTMLInputElement>('#menu-jump');
    if (box === null) throw new Error('the menu has no search box');
    // Matches one kind of the opened group and nothing among the loaded ones.
    fireEvent.change(box, { target: { value: 'certif' } });

    await waitFor(() => {
      expect(menu().textContent).not.toContain('Pods');
    });
    expect(menu().querySelector('a[href="/c/prod/certificates.cert-manager.io"]')).not.toBeNull();
    expect(menu().querySelector('[data-note="no-match"]')).toBeNull();
    expect(menu().textContent).not.toContain('No kind here matches');
  });

  it('arrives open and filled on a custom resource page', async () => {
    stub(filled);
    mountApp('/c/prod/certificates.cert-manager.io');

    // The correctness requirement rather than the optimization: following a link
    // must never land a person on a page whose place in the menu is folded
    // away. No loaded group claims this kind, so the deferred one is where it is.
    await waitFor(() => {
      expect(menu().textContent).toContain('Certificates');
    });
    expect(group('custom-resources').open).toBe(true);
    expect(group('cert-manager.io').open).toBe(true);

    // Open for the visit and not written down. Read after the `toggle`
    // events the two disclosures fire on opening have landed: either one, taken
    // for a gesture, would have committed the heading.
    await settled();
    expect(localStorage.getItem('kubetower.menu.open')).toBeNull();
  });

  it('shuts the heading again when the visit to a custom page ends', async () => {
    // The end of the visit: a custom page, then a built-in one, and the
    // heading is shut again - with nothing in storage saying otherwise, so a
    // reload cannot bring it back either.
    stub(filled);
    const app = mountApp('/c/prod/certificates.cert-manager.io');

    await waitFor(() => {
      expect(menu().textContent).toContain('Certificates');
    });
    expect(group('custom-resources').open).toBe(true);
    await settled();

    await app.router.navigate('/c/prod/pods');
    await waitFor(() => {
      expect(group('custom-resources').open).toBe(false);
    });
    await settled();
    expect(group('custom-resources').open).toBe(false);
    expect(localStorage.getItem('kubetower.menu.open')).toBeNull();
  });

  it('keeps it open after the visit once it has been opened by hand', async () => {
    // The other half: an arrangement is a person's gesture and survives the
    // page. Opened by hand on the overview, then a custom page, then Pods.
    stub(filled);
    const app = mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });
    await settled();
    const summary = group('custom-resources').querySelector('summary');
    if (summary === null) throw new Error('the heading has no summary');
    fireEvent.click(summary);
    await waitFor(() => {
      expect(menu().textContent).toContain('Certificates');
    });
    // Only what was opened: on an unarranged menu nothing else was drawn open,
    // so nothing else rides along.
    expect(JSON.parse(localStorage.getItem('kubetower.menu.open') ?? 'null')).toStrictEqual([
      'custom-resources',
    ]);

    await app.router.navigate('/c/prod/certificates.cert-manager.io');
    await settled();
    await app.router.navigate('/c/prod/pods');
    await settled();
    expect(group('custom-resources').open).toBe(true);
  });

  it('leaves the arrangement alone when an API group inside the heading is toggled', async () => {
    // React carries `toggle` up its own tree although the DOM event does not
    // bubble, so the nested disclosure reaches the heading's handler with the
    // heading's state - "open" - and, during a visit, that could read as a
    // person opening it.
    stub(filled);
    mountApp('/c/prod/certificates.cert-manager.io');

    await waitFor(() => {
      expect(group('cert-manager.io').open).toBe(true);
    });
    await settled();
    const summary = group('cert-manager.io').querySelector('summary');
    if (summary === null) throw new Error('the API group has no summary');
    fireEvent.click(summary);
    await waitFor(() => {
      expect(group('cert-manager.io').open).toBe(false);
    });
    await settled();
    expect(group('custom-resources').open).toBe(true);
    expect(localStorage.getItem('kubetower.menu.open')).toBeNull();
  });

  it('stays shut on a built-in list whose address spells the cluster the other way', async () => {
    // Every group folded by hand, on the Pods list, F5: *Custom Resources* must
    // not come back open. The address carries the cluster with `@` escaped and
    // the menu's hrefs carry it unescaped. On a context named
    // `kubernetes-admin@kubernetes` - kubeadm's default - a whole comparison
    // would match no loaded group, read the page as a custom resource page, and
    // open, fill and write down the heading.
    const cluster = 'kubernetes-admin@kubernetes';
    const api = `/api/c/${encodeURIComponent(cluster)}`;
    stubFetch({
      '/api/session': session,
      '/api/clusters': {
        body: { state: 'ok', items: [{ name: cluster, label: cluster, state: 'ok' }] },
      },
      [`${api}/nav`]: {
        body: {
          state: 'ok',
          data: {
            groups: [
              {
                key: 'workloads',
                label: 'Workloads',
                deferred: false,
                items: [{ text: 'Pods', href: `/c/${cluster}/pods` }],
              },
              { key: 'custom-resources', label: 'Custom Resources', deferred: true, items: [] },
            ],
          },
        },
      },
      [`${api}/nav/custom-resources`]: filled,
      [`${api}/extensions`]: { body: { state: 'empty', items: [] } },
    });
    const asked: string[] = [];
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      asked.push(String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url));
      return stubbed(input, init);
    });
    localStorage.setItem('kubetower.menu.open', '[]');

    mountApp(`/c/${encodeURIComponent(cluster)}/pods`);

    await waitFor(() => {
      expect(group('workloads').open).toBe(true);
    });
    await settled();
    expect(group('custom-resources').open).toBe(false);
    expect(asked.filter((one) => one.includes('/nav/custom-resources'))).toEqual([]);
    expect(localStorage.getItem('kubetower.menu.open')).toBe('[]');
  });

  it('stays shut on a Kubernetes kind the server files under its natural heading', async () => {
    // The Gateway API is served by CRDs, and the server
    // files it under Network rather than Custom Resources - so a Gateway page
    // is claimed by a loaded group, and the deferred heading, with the
    // CustomResourceDefinition listing behind it, is neither opened nor asked
    // for on it.
    const gateways = '/c/prod/gateways.gateway.networking.k8s.io';
    stubFetch({
      '/api/session': session,
      '/api/clusters': clusters,
      '/api/c/prod/nav': {
        body: {
          state: 'ok',
          data: {
            groups: [
              {
                key: 'network',
                label: 'Network',
                deferred: false,
                items: [
                  { text: 'Services', href: '/c/prod/services' },
                  { text: 'Gateways', href: gateways, namespaced: true },
                ],
              },
              { key: 'custom-resources', label: 'Custom Resources', deferred: true, items: [] },
            ],
          },
        },
      },
      '/api/c/prod/nav/custom-resources': filled,
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    });
    const asked: string[] = [];
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      asked.push(String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url));
      return stubbed(input, init);
    });

    mountApp(gateways);

    await waitFor(() => {
      expect(group('network').open).toBe(true);
    });
    await settled();
    expect(group('custom-resources').open).toBe(false);
    expect(asked.filter((one) => one.includes('/nav/custom-resources'))).toEqual([]);

    // Network's shape, as its neighbors draw theirs - not the cube a vendor's
    // custom resource draws.
    const link = menu().querySelector(`a[href^="${gateways}"]`);
    expect(link?.querySelector('use')?.getAttribute('href')).toBe('#i-network');
  });

  it('stays shut on Helm, which is a page under the cluster and not a kind', async () => {
    // `/c/prod/helm` is a kind route by shape and no loaded group claims it;
    // read as "then it is a custom resource page", the heading would open on
    // arrival and fire the discovery read the deferral exists to avoid.
    // Executed here rather than reasoned: no
    // request for the group, the heading shut, the arrangement as it was.
    stub(filled);

    const asked: string[] = [];
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      asked.push(String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url));
      return stubbed(input, init);
    });

    mountApp('/c/prod/helm');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });
    expect(group('custom-resources').open).toBe(false);
    expect(asked.filter((one) => one.includes('/nav/custom-resources'))).toEqual([]);
    expect(localStorage.getItem('kubetower.menu.open')).toBeNull();
  });

  it('says a cluster serves none, rather than drawing nothing', async () => {
    stub({
      body: {
        state: 'ok',
        data: {
          key: 'custom-resources',
          label: 'Custom Resources',
          deferred: false,
          items: [],
          note: 'This cluster serves no custom resource kinds.',
        },
      },
    });
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });
    const summary = group('custom-resources').querySelector('summary');
    if (summary === null) throw new Error('the heading has no summary');
    fireEvent.click(summary);

    await waitFor(() => {
      expect(menu().textContent).toContain('This cluster serves no custom resource kinds.');
    });
  });

  it('says why the kinds could not be listed, which is a different cluster', async () => {
    stub({
      body: {
        state: 'ok',
        data: {
          key: 'custom-resources',
          label: 'Custom Resources',
          deferred: false,
          items: [],
          note: 'listing the custom resource definitions of prod: forbidden',
        },
      },
    });
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });
    const summary = group('custom-resources').querySelector('summary');
    if (summary === null) throw new Error('the heading has no summary');
    fireEvent.click(summary);

    // A cluster that would not answer and a cluster with nothing to say are not
    // the same cluster, and a heading that drew them identically is the founding
    // defect wearing a navigation's clothes.
    await waitFor(() => {
      expect(menu().textContent).toContain(
        'listing the custom resource definitions of prod: forbidden',
      );
    });
    expect(menu().textContent).not.toContain('serves no custom resource kinds');
  });

  // "3 kinds this console may not list are not shown" is drawn first in the
  // group - as the last line under the API groups, in the muted step, it would
  // be missed - in the warning tone the access-narrowed list uses, and it says
  // the read has to be granted.
  const hidden =
    '3 kinds this console may not list are not shown: the identity it reads with has to be granted the read on them.';

  it('draws the kinds it left out first in the group, where it is read, and in the warning tone', async () => {
    stub({
      body: {
        state: 'ok',
        data: {
          key: 'custom-resources',
          label: 'Custom Resources',
          deferred: false,
          items: [],
          accessNote: hidden,
          groups: [
            {
              key: 'argoproj.io',
              label: 'argoproj.io',
              deferred: false,
              items: [{ text: 'Applications', href: '/c/prod/applications.argoproj.io' }],
            },
            {
              key: 'cert-manager.io',
              label: 'cert-manager.io',
              deferred: false,
              items: [{ text: 'Certificates', href: '/c/prod/certificates.cert-manager.io' }],
            },
          ],
        },
      },
    });
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });
    const summary = group('custom-resources').querySelector('summary');
    if (summary === null) throw new Error('the heading has no summary');
    fireEvent.click(summary);

    const note = await waitFor(() => {
      const found = group('custom-resources').querySelector<HTMLElement>('[data-note="access"]');
      if (found === null) throw new Error('no access note in the heading');
      return found;
    });
    expect(note.textContent).toBe(hidden);
    expect(note.textContent).toContain('has to be granted');
    expect(note.className).toContain('text-warn');
    expect(note.className).not.toContain('text-muted');
    // First in the group: directly under its heading, before any API group -
    // not the line after them that a reader scrolls past.
    expect(note.previousElementSibling?.tagName).toBe('SUMMARY');
    const firstSubGroup = group('argoproj.io');
    expect(note.compareDocumentPosition(firstSubGroup) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('keeps the kinds it left out on screen under a search', async () => {
    // When a kind looked for and not found most needs telling apart from one
    // the cluster does not serve.
    stub({
      body: {
        state: 'ok',
        data: {
          key: 'custom-resources',
          label: 'Custom Resources',
          deferred: false,
          items: [],
          accessNote: hidden,
          groups: [
            {
              key: 'argoproj.io',
              label: 'argoproj.io',
              deferred: false,
              items: [{ text: 'Applications', href: '/c/prod/applications.argoproj.io' }],
            },
          ],
        },
      },
    });
    mountApp('/c/prod/applications.argoproj.io');

    await waitFor(() => {
      expect(group('custom-resources').querySelector('[data-note="access"]')).not.toBeNull();
    });
    const box = menu().querySelector<HTMLInputElement>('#menu-jump');
    if (box === null) throw new Error('the menu has no search box');
    fireEvent.change(box, { target: { value: 'appprojects' } });

    await waitFor(() => {
      expect(menu().querySelector('[data-note="no-match"]')).not.toBeNull();
    });
    expect(group('custom-resources').querySelector('[data-note="access"]')?.textContent).toBe(hidden);
  });

  it('draws the envelope failure for a cluster that could not be asked at all', async () => {
    stub({
      body: {
        state: 'unreachable',
        reason: { code: 'cluster_unreachable', message: 'dial tcp 10.0.0.1:443: connection refused' },
      },
    });
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });
    const summary = group('custom-resources').querySelector('summary');
    if (summary === null) throw new Error('the heading has no summary');
    fireEvent.click(summary);

    // The third answer, and it is the envelope's rather than a sentence: it
    // carries a code and whether asking again is worth it, which a note cannot.
    await waitFor(() => {
      expect(menu().textContent).toContain('Could not be asked');
    });
    // And the rest of the menu is untouched. One group failing to fill is not a
    // menu that failed.
    expect(menu().textContent).toContain('Pods');
  });
});

/**
 * The deferred answer survives a reload.
 *
 * The heading is fetched only when opened, and the answer outlives F5: in
 * TanStack's in-memory cache alone, the next opening would pay the full read
 * again (tens of megabytes on a large cluster). What is stored is the menu
 * entries, kilobytes, under `kubetower.nav.<cluster>.<group>`; the query is
 * seeded from it and the five-minute `staleTime` still revalidates in the
 * background. Not an idle prefetch: nothing is asked for on a cluster where the
 * heading was never opened.
 */
describe('the custom resource answer survives a reload', () => {
  const nav = {
    body: {
      state: 'ok',
      data: {
        groups: [
          {
            key: 'workloads',
            label: 'Workloads',
            deferred: false,
            items: [{ text: 'Pods', href: '/c/prod/pods' }],
          },
          { key: 'custom-resources', label: 'Custom Resources', deferred: true, items: [] },
        ],
      },
    },
  };

  const remembered = {
    key: 'custom-resources',
    label: 'Custom Resources',
    deferred: false,
    items: [],
    groups: [
      {
        key: 'cert-manager.io',
        label: 'cert-manager.io',
        deferred: false,
        items: [{ text: 'Certificates', href: '/c/prod/certificates.cert-manager.io' }],
      },
    ],
  };

  const fromServer = {
    body: {
      state: 'ok',
      data: {
        ...remembered,
        groups: [
          {
            key: 'cert-manager.io',
            label: 'cert-manager.io',
            deferred: false,
            items: [
              { text: 'Certificates', href: '/c/prod/certificates.cert-manager.io' },
              { text: 'Issuers', href: '/c/prod/issuers.cert-manager.io' },
            ],
          },
        ],
      },
    },
  };

  const KEY = 'kubetower.nav.prod.custom-resources';

  function pathOf(input: RequestInfo | URL): string {
    return String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
  }

  function forGroup(asked: readonly string[]): string[] {
    return asked.filter((one) => one.includes('/nav/custom-resources'));
  }

  /** The application at the overview, with every request to the group held
   *  until `release` is called - so a test can look at the screen while the
   *  request is in flight, and see whether one was made at all. */
  function mountHeld(): { asked: string[]; release: () => void } {
    stubFetch({
      '/api/session': session,
      '/api/clusters': clusters,
      '/api/c/prod/nav': nav,
      '/api/c/prod/nav/custom-resources': fromServer,
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    });

    const asked: string[] = [];
    let release = (): void => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = pathOf(input);
      asked.push(path);
      if (path.includes('/nav/custom-resources')) await gate;
      return stubbed(input, init);
    });

    mountApp('/c/prod');
    return { asked, release };
  }

  it('draws the heading from the snapshot at once, and asks nothing while it is fresh', async () => {
    localStorage.setItem('kubetower.menu.open', '["workloads","custom-resources"]');
    localStorage.setItem(KEY, JSON.stringify({ at: Date.now(), group: remembered }));
    const { asked } = mountHeld();

    await waitFor(() => {
      expect(menu().textContent).toContain('Certificates');
    });
    expect(menu().textContent).toContain('cert-manager.io');
    expect(menu().querySelector('a[href="/c/prod/certificates.cert-manager.io"]')).not.toBeNull();
    // The whole point: nothing was asked for. The request is what F5 would
    // cost, and the snapshot is younger than the query's five minutes.
    expect(forGroup(asked)).toEqual([]);
    expect(menu().textContent).not.toContain('Reading');
  });

  it('revalidates in the background when the snapshot is older than five minutes', async () => {
    localStorage.setItem('kubetower.menu.open', '["workloads","custom-resources"]');
    localStorage.setItem(KEY, JSON.stringify({ at: Date.now() - 301_000, group: remembered }));
    const { asked, release } = mountHeld();

    // Drawn from the snapshot while the request is in flight - nobody waits on
    // a heading they opened before the reload.
    await waitFor(() => {
      expect(menu().textContent).toContain('Certificates');
    });
    await waitFor(() => {
      expect(forGroup(asked)).toHaveLength(1);
    });
    expect(menu().textContent).not.toContain('Issuers');

    // And the answer replaces it, which is how a newly installed CRD appears
    // without a second F5 - then it is the new snapshot.
    release();
    await waitFor(() => {
      expect(menu().textContent).toContain('Issuers');
    });
    await waitFor(() => {
      const stored = JSON.parse(localStorage.getItem(KEY) ?? 'null') as { at: number; group: unknown };
      expect(stored.group).toStrictEqual(fromServer.body.data);
      expect(stored.at).toBeGreaterThan(Date.now() - 60_000);
    });
  });

  it('writes the answer down when the heading is first filled', async () => {
    localStorage.setItem('kubetower.menu.open', '["workloads","custom-resources"]');
    const { release } = mountHeld();
    release();

    await waitFor(() => {
      expect(menu().textContent).toContain('Issuers');
    });
    await waitFor(() => {
      const stored = JSON.parse(localStorage.getItem(KEY) ?? 'null') as { group: unknown } | null;
      expect(stored?.group).toStrictEqual(fromServer.body.data);
    });
  });

  it('draws a remembered "serves none" as that sentence, never as an empty heading', async () => {
    // The three answers stay three. A snapshot of a cluster with no custom
    // resources carries the server's sentence, and the heading drawn from it
    // says so - an empty heading would read as the founding defect.
    localStorage.setItem('kubetower.menu.open', '["workloads","custom-resources"]');
    localStorage.setItem(
      KEY,
      JSON.stringify({
        at: Date.now(),
        group: {
          key: 'custom-resources',
          label: 'Custom Resources',
          deferred: false,
          items: [],
          note: 'This cluster serves no custom resource kinds.',
        },
      }),
    );
    const { asked } = mountHeld();

    await waitFor(() => {
      expect(menu().textContent).toContain('This cluster serves no custom resource kinds.');
    });
    expect(forGroup(asked)).toEqual([]);
  });

  it('asks nothing on a cluster where the heading is shut, snapshot or not', async () => {
    // Not an idle prefetch. A stale snapshot exists for this cluster, the
    // heading is shut, and it stays a closed disclosure that costs nothing -
    // the case the deferral exists for.
    localStorage.setItem('kubetower.menu.open', '["workloads"]');
    localStorage.setItem(KEY, JSON.stringify({ at: Date.now() - 301_000, group: remembered }));
    const { asked } = mountHeld();

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });
    expect(menu().textContent).not.toContain('Certificates');
    expect(forGroup(asked)).toEqual([]);
  });
});

/**
 * The active row.
 *
 * The assertions are on classes, which is unusual in this file. Worth saying what that
 * can and cannot establish: it pins that the row carries the accent border, the
 * surface and the cut-back radius, and that an inactive row reserves the same
 * two pixels. It does not establish that any of it is visible - jsdom computes
 * no styles and Tailwind is not running here. The screenshot pair is the other
 * half and it is not in this file.
 */
describe('the row you are on reads as one, and not as one step of gray', () => {
  const twoKinds = {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/prod/nav': {
      body: {
        state: 'ok',
        data: {
          groups: [
            {
              key: 'workloads',
              label: 'Workloads',
              items: [
                { text: 'Pods', href: '/c/prod/r/pods' },
                { text: 'Deployments', href: '/c/prod/r/deployments' },
              ],
            },
          ],
        },
      },
    },
  } satisfies Record<string, StubReply>;

  function row(text: string): HTMLElement {
    for (const link of menu().querySelectorAll('a')) {
      if (link.textContent === text) return link;
    }
    throw new Error(`no menu row says ${text}`);
  }

  it('gives the current kind the accent bar and the surface', async () => {
    stubFetch(twoKinds);
    mountApp('/c/prod/r/pods');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });

    const here = row('Pods');
    expect(here.className).toContain('border-accent');
    expect(here.className).toContain('bg-panel-2');
    expect(here.className).toContain('text-fg');
    // The left corners go square so the 2px bar is straight for its whole
    // height rather than bowing away at the top and the bottom.
    expect(here.className).toContain('rounded-r-md');
    expect(here.className).not.toContain('rounded-md ');
    // And `NavLink`'s own announcement, which the mark is the visible half of.
    expect(here.getAttribute('aria-current')).toBe('page');
  });

  it('reserves the same two pixels on every other row, so nothing jogs', async () => {
    stubFetch(twoKinds);
    mountApp('/c/prod/r/pods');

    await waitFor(() => {
      expect(menu().textContent).toContain('Deployments');
    });

    const other = row('Deployments');
    // The border exists on both rows; only its color differs. A `border-l-2`
    // that appeared on the active row alone would push its label 2px right of
    // every other label in the column, and the jog is visible while arrowing
    // down it.
    expect(other.className).toContain('border-l-2');
    expect(other.className).toContain('border-transparent');
    expect(row('Pods').className).toContain('border-l-2');
    expect(other.className).toContain('hover:bg-panel-2');
    expect(other.className).not.toContain('border-accent');
    expect(other.getAttribute('aria-current')).toBeNull();
  });

  it('draws a kind it cannot route to as the same row, dimmed', async () => {
    // Still listed, still inset, still holding the two pixels: a menu short by
    // one line, or ragged by one line, is what `Entry` exists not to be.
    stubFetch({
      ...twoKinds,
      '/api/c/prod/nav': {
        body: {
          state: 'ok',
          data: {
            groups: [
              { key: 'workloads', label: 'Workloads', items: [{ text: 'Widgets', href: '' }] },
            ],
          },
        },
      },
    });
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Widgets');
    });
    expect(menu().querySelector('a[href=""]')).toBeNull();
    const dead = menu().querySelector('span');
    expect(dead?.className).toContain('border-transparent');
    expect(dead?.className).toContain('opacity-60');
  });
});

describe('a group heading says it opens, and says it quietly', () => {
  const nested = {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/prod/nav': {
      body: {
        state: 'ok',
        data: {
          groups: [
            {
              key: 'workloads',
              label: 'Workloads',
              items: [{ text: 'Pods', href: '/c/prod/r/pods' }],
            },
            {
              key: 'custom-resources',
              label: 'Custom Resources',
              items: [],
              groups: [
                {
                  key: 'cert-manager',
                  label: 'cert-manager.io',
                  items: [{ text: 'Issuers', href: '/c/prod/r/issuers.cert-manager.io' }],
                },
              ],
            },
          ],
        },
      },
    },
  } satisfies Record<string, StubReply>;

  function heading(key: string): HTMLElement {
    const summary = menu().querySelector<HTMLElement>(`details[data-group="${key}"] > summary`);
    if (summary === null) throw new Error(`no heading for ${key}`);
    return summary;
  }

  it('carries its own chevron, the native marker having been taken off', async () => {
    stubFetch(nested);
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });

    const top = heading('workloads');
    // Removing the marker without putting something in its place would leave a
    // heading with nothing on it saying that it opens at all.
    expect(top.className).toContain('list-none');
    expect(top.querySelector('use')?.getAttribute('href')).toBe('#i-chevron');
  });

  it('turns the chevron with the disclosure rather than swapping the glyph', async () => {
    // Opened by hand before, since an unarranged menu opens only the
    // page's group and this page is in none.
    localStorage.setItem('kubetower.menu.open', '["workloads"]');
    stubFetch(nested);
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });

    expect(heading('workloads').querySelector('svg')?.getAttribute('class')).toContain('rotate-90');

    // Shut it, the way a person does.
    const details = menu().querySelector<HTMLDetailsElement>('details[data-group="workloads"]');
    if (details === null) throw new Error('no workloads group');
    details.open = false;
    fireEvent(details, new Event('toggle'));

    await waitFor(() => {
      expect(heading('workloads').querySelector('svg')?.getAttribute('class')).not.toContain(
        'rotate-90',
      );
    });
  });

  // The eyebrow is gone from both levels, and the two headings separate by
  // weight and size instead.
  //
  // With an eyebrow, the group headings were too quiet to lead their rows, and
  // the API groups under Custom Resources so wide — uppercase, tracked at .07em,
  // and `gateway.networking.k8s.io` being one unbreakable 25-character word —
  // that the column grew a horizontal scrollbar.
  //
  // So: the heading takes a step up the ladder that `theme.css` already argued
  // for (`text-2xs` is "for badges and counters, not for reading text"), and the
  // API group is a lower-case domain name that may wrap, in the size of its
  // kinds and told apart from them by color and weight.
  it('separates the two headings by weight, with no eyebrow on either', async () => {
    stubFetch(nested);
    mountApp('/c/prod/r/issuers.cert-manager.io');

    await waitFor(() => {
      expect(menu().textContent).toContain('Issuers');
    });

    const top = heading('custom-resources');
    expect(top.className).toContain('text-sm');
    expect(top.className).toContain('font-semibold');
    expect(top.className).not.toContain('uppercase');

    const sub = heading('cert-manager');
    expect(sub.className).toContain('text-sm');
    expect(sub.className).toContain('font-medium');
    expect(sub.className).not.toContain('text-2xs');
    expect(sub.className).not.toContain('uppercase');
    expect(sub.className).not.toContain('tracking-eyebrow');
    // A domain name has nothing to break on: the name is cut by an ellipsis
    // instead, on one line, which is what keeps the scrollbar away.
    expect(sub.querySelector('h3')?.className).toContain('truncate');
  });
});

/**
 * The glyphs, where the menu draws them.
 *
 * Which glyph a kind resolves to is `kindGlyph.test.ts`, against the server's
 * own list. This file asserts only what that one cannot: that the menu asks, and
 * that it asks with the slug out of the href rather than with the label.
 */
describe('every row and every heading carries a shape', () => {
  const grouped = {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/prod/nav': {
      body: {
        state: 'ok',
        data: {
          groups: [
            {
              key: 'workloads',
              label: 'Workloads',
              items: [
                // With the sort `viewMemory` appends, which is most of the menu
                // after an operator has used it once.
                { text: 'Pods', href: '/c/prod/pods?sort=age' },
                { text: 'Cron jobs', href: '/c/prod/cronjobs' },
              ],
            },
            {
              key: 'access-control',
              label: 'Access Control',
              items: [{ text: 'Roles', href: '/c/prod/roles' }],
            },
          ],
        },
      },
    },
  } satisfies Record<string, StubReply>;

  function glyphOf(node: Element | null): string | undefined {
    return node?.querySelector('use')?.getAttribute('href') ?? undefined;
  }

  function row(text: string): HTMLElement {
    for (const link of menu().querySelectorAll('a')) {
      if (link.textContent === text) return link;
    }
    throw new Error(`no menu row says ${text}`);
  }

  it('asks by the slug in the href, with the sort cut off it', async () => {
    stubFetch(grouped);
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });

    // `?sort=age` on the href must not turn Pods into the generic shape - a
    // failure that never errors and reads as a design decision.
    expect(glyphOf(row('Pods'))).toBe('#i-pod');
    expect(glyphOf(row('Cron jobs'))).toBe('#i-cronjob');
    expect(glyphOf(row('Roles'))).toBe('#i-role');
  });

  it('heads each group with the group’s own shape, not its first row’s', async () => {
    stubFetch(grouped);
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });

    const heads = menu().querySelectorAll('details[data-group="workloads"] > summary use');
    // Two: the chevron from step 1, then the group glyph.
    expect([...heads].map((use) => use.getAttribute('href'))).toEqual([
      '#i-chevron',
      '#i-workloads',
    ]);
    expect(glyphOf(menu().querySelector('details[data-group="access-control"] > summary'))).toBe(
      '#i-chevron',
    );
  });

  it('draws the generic shape for a kind it has never heard of, never nothing', async () => {
    stubFetch({
      ...grouped,
      '/api/c/prod/nav': {
        body: {
          state: 'ok',
          data: {
            groups: [
              {
                key: 'custom-resources',
                label: 'Custom Resources',
                items: [],
                groups: [
                  {
                    key: 'example',
                    label: 'example.com',
                    items: [{ text: 'Widgets', href: '/c/prod/widgets.example.com' }],
                  },
                  {
                    key: 'cert-manager',
                    label: 'cert-manager.io',
                    items: [{ text: 'Issuers', href: '/c/prod/issuers.cert-manager.io' }],
                  },
                ],
              },
            ],
          },
        },
      },
    });
    mountApp('/c/prod/widgets.example.com');

    await waitFor(() => {
      expect(menu().textContent).toContain('Widgets');
    });

    // 140 custom kinds on one production cluster. A row with no icon would sit
    // out of line with every built-in row above it.
    expect(glyphOf(row('Widgets'))).toBe('#i-resource');
    // And a vendor this console recognizes gets that vendor's group shape.
    expect(glyphOf(row('Issuers'))).toBe('#i-access');
  });

  it('gives a kind it cannot route to the same icon as one it can', async () => {
    stubFetch({
      ...grouped,
      '/api/c/prod/nav': {
        body: {
          state: 'ok',
          data: {
            groups: [
              { key: 'workloads', label: 'Workloads', items: [{ text: 'Pods', href: '' }] },
            ],
          },
        },
      },
    });
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });

    // No href means no slug, so the generic one - drawn rather than skipped,
    // because a row missing its icon is a row 15px out of line.
    const dead = menu().querySelector('span');
    expect(glyphOf(dead)).toBe('#i-resource');
  });
});

/**
 * The jump box.
 *
 * The keyboard being the namespace box's is the point, so the
 * assertions below are deliberately the same shapes `NamespaceMenu.test.tsx`
 * makes: down and up between the box and the matches, stopping rather than
 * wrapping, and "nothing matched" said rather than a column that has gone blank.
 */
describe('the jump box, which is how a kind is found without reading thirty-six names', () => {
  const twoGroups = {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/prod/nav': {
      body: {
        state: 'ok',
        data: {
          groups: [
            {
              key: 'workloads',
              label: 'Workloads',
              items: [
                { text: 'Pods', href: '/c/prod/pods' },
                { text: 'Deployments', href: '/c/prod/deployments' },
                { text: 'Daemon sets', href: '/c/prod/daemonsets' },
              ],
            },
            {
              key: 'network',
              label: 'Network',
              items: [{ text: 'Services', href: '/c/prod/services' }],
            },
          ],
        },
      },
    },
  } satisfies Record<string, StubReply>;

  function box(): HTMLInputElement {
    const element = menu().querySelector<HTMLInputElement>('#menu-jump');
    if (element === null) throw new Error('no jump box');
    return element;
  }

  function walkable(): string[] {
    return [...menu().querySelectorAll('[data-kind]')].map((row) => row.textContent ?? '');
  }

  async function mounted(at = '/c/prod'): Promise<void> {
    stubFetch(twoGroups);
    mountApp(at);
    await waitFor(() => {
      expect(menu().textContent).toContain('Services');
    });
  }

  it('narrows the rows across every group as it is typed', async () => {
    await mounted();

    // Across both groups, and in the order the column draws them.
    fireEvent.change(box(), { target: { value: 'se' } });
    await waitFor(() => {
      expect(walkable()).toEqual(['Daemon sets', 'Services']);
    });
    expect(menu().textContent).toContain('Network');

    // And the group that has nothing left in it goes with its rows. A heading
    // over an empty disclosure is a group that looks broken, not filtered.
    fireEvent.change(box(), { target: { value: 'dep' } });
    await waitFor(() => {
      expect(walkable()).toEqual(['Deployments']);
    });
    expect(menu().textContent).not.toContain('Network');
  });

  it('narrows in the same render as the keystroke, with nothing to settle', async () => {
    await mounted();

    // Every search box behaves the same, and for a box that filters
    // what is already in hand "the same" is *at once*. No debounce, no
    // waiting state and no marker: there is no answer to wait for, so a
    // marker here would be a promise about nothing. Read without `waitFor`
    // on purpose - the rows must already be narrowed when the change returns.
    fireEvent.change(box(), { target: { value: 'dep' } });
    expect(walkable()).toEqual(['Deployments']);
    expect(menu().querySelector('[data-settling]')).toBeNull();
    expect(menu().querySelector('[aria-busy="true"]')).toBeNull();

    fireEvent.change(box(), { target: { value: 'se' } });
    expect(walkable()).toEqual(['Daemon sets', 'Services']);
  });

  it('says that nothing matched rather than going blank', async () => {
    await mounted();

    fireEvent.change(box(), { target: { value: 'zzz' } });

    await waitFor(() => {
      expect(menu().querySelector('[data-note="no-match"]')).not.toBeNull();
    });
    // Constraint 12 inside a menu: a column that has silently gone empty reads
    // as a cluster with no kinds.
    expect(menu().textContent).toContain('No kind here matches');
    expect(walkable()).toEqual([]);
  });

  it('follows the first match on Enter', async () => {
    await mounted();

    fireEvent.change(box(), { target: { value: 'dep' } });
    await waitFor(() => {
      expect(walkable()).toEqual(['Deployments']);
    });
    fireEvent.keyDown(box(), { key: 'Enter' });

    // The router is a memory router here, so the assertion is what the menu
    // says about where it now is - `NavLink`'s own `aria-current`, which step 1
    // pinned as the visible mark's other half.
    await waitFor(() => {
      const here = menu().querySelector('a[aria-current="page"]');
      expect(here?.getAttribute('data-kind')).toBe('deployments');
    });
  });

  it('walks down into the matches and back up into the box, and stops', async () => {
    await mounted();

    fireEvent.change(box(), { target: { value: 'se' } });
    await waitFor(() => {
      expect(walkable()).toEqual(['Daemon sets', 'Services']);
    });

    const rows = [...menu().querySelectorAll<HTMLElement>('[data-kind]')];
    const [first, second] = rows;
    if (first === undefined || second === undefined) throw new Error('two matches expected');

    fireEvent.keyDown(box(), { key: 'ArrowDown' });
    expect(document.activeElement).toBe(first);

    fireEvent.keyDown(first, { key: 'ArrowDown' });
    expect(document.activeElement).toBe(second);

    // Past the last match it stops - the column scrolls rather than the focus
    // jumping silently back to the top.
    fireEvent.keyDown(second, { key: 'ArrowDown' });
    expect(document.activeElement).toBe(second);

    fireEvent.keyDown(second, { key: 'ArrowUp' });
    expect(document.activeElement).toBe(first);

    // And up from the first match is the way out of the list, not a dead key.
    fireEvent.keyDown(first, { key: 'ArrowUp' });
    expect(document.activeElement).toBe(box());
  });

  it('is focused by / and by Ctrl+K, and declines both while something is typed in', async () => {
    await mounted();

    fireEvent.keyDown(window, { key: '/' });
    expect(document.activeElement).toBe(box());

    box().blur();
    fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    expect(document.activeElement).toBe(box());

    // The guard that matters: `stack.ts` never reads `event.target`, so a bare
    // printable hotkey has to. A slash typed into a text field is a slash.
    box().blur();
    const elsewhere = document.createElement('input');
    document.body.append(elsewhere);
    elsewhere.focus();
    fireEvent.keyDown(elsewhere, { key: '/' });
    expect(document.activeElement).toBe(elsewhere);
    elsewhere.remove();
  });

  it('clears on Escape, and the arrangement is as it was', async () => {
    await mounted();

    fireEvent.change(box(), { target: { value: 'se' } });
    await waitFor(() => {
      expect(walkable()).toEqual(['Daemon sets', 'Services']);
    });

    fireEvent.keyDown(box(), { key: 'Escape' });

    await waitFor(() => {
      expect(box().value).toBe('');
    });
    expect(walkable()).toEqual(['Pods', 'Deployments', 'Daemon sets', 'Services']);
  });

  it('leaves on a second Escape, back to where Ctrl+K found the focus', async () => {
    await mounted();
    const elsewhere = document.createElement('button');
    document.body.append(elsewhere);
    elsewhere.focus();

    fireEvent.keyDown(elsewhere, { key: 'k', ctrlKey: true });
    expect(document.activeElement).toBe(box());
    fireEvent.change(box(), { target: { value: 'dep' } });
    fireEvent.keyDown(box(), { key: 'Escape' });
    await waitFor(() => {
      expect(box().value).toBe('');
    });
    // The first Escape cleared and kept the box; the second leaves it.
    expect(document.activeElement).toBe(box());
    fireEvent.keyDown(box(), { key: 'Escape' });
    expect(document.activeElement).toBe(elsewhere);
    elsewhere.remove();
  });

  it('forgets where Ctrl+K came from once the box is left another way', async () => {
    await mounted();
    const elsewhere = document.createElement('button');
    const later = document.createElement('button');
    document.body.append(elsewhere, later);
    elsewhere.focus();

    fireEvent.keyDown(elsewhere, { key: 'k', ctrlKey: true });
    expect(document.activeElement).toBe(box());
    // Left by the pointer, and come back to by a click rather than the hotkey.
    later.focus();
    box().focus();
    fireEvent.keyDown(box(), { key: 'Escape' });
    // Not back to `elsewhere`, which nothing current asked for.
    expect(document.activeElement).not.toBe(elsewhere);
    expect(document.activeElement).not.toBe(box());
    elsewhere.remove();
    later.remove();
  });

  it('writes nothing into the remembered arrangement while it is narrowing', async () => {
    // A search drives the disclosures, and React setting `open` fires `toggle`
    // exactly as a click does. Without the guard, three keystrokes would rewrite
    // what had been arranged and leave every group open afterwards.
    localStorage.setItem('kubetower.menu.open', '["workloads"]');
    await mounted();

    fireEvent.change(box(), { target: { value: 'serv' } });
    await waitFor(() => {
      expect(walkable()).toEqual(['Services']);
    });

    expect(localStorage.getItem('kubetower.menu.open')).toBe('["workloads"]');
  });
});

describe('the jump box answers to kubectl’s names, and clears after a jump', () => {
  // The menu as the server sends it: each kind's entry carries `aliases`.
  // Pod disruption budgets comes first on purpose - `po` narrows to it too, and
  // Enter must still land on Pods, which `po` names.
  const kinds = {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/prod/nav': {
      body: {
        state: 'ok',
        data: {
          groups: [
            {
              key: 'cluster',
              label: 'Cluster',
              items: [{ text: 'Namespaces', href: '/c/prod/namespaces', aliases: ['namespace', 'namespaces', 'ns'] }],
            },
            {
              key: 'workloads',
              label: 'Workloads',
              items: [
                {
                  text: 'Pod disruption budgets',
                  href: '/c/prod/poddisruptionbudgets',
                  aliases: ['pdb', 'poddisruptionbudget', 'poddisruptionbudgets'],
                },
                { text: 'Pods', href: '/c/prod/pods', aliases: ['po', 'pod', 'pods'] },
                {
                  text: 'Deployments',
                  href: '/c/prod/deployments',
                  aliases: ['deploy', 'deployment', 'deployments'],
                },
              ],
            },
            {
              key: 'network',
              label: 'Network',
              items: [{ text: 'Services', href: '/c/prod/services', aliases: ['service', 'services', 'svc'] }],
            },
            {
              key: 'config',
              label: 'Config',
              items: [
                { text: 'Config maps', href: '/c/prod/configmaps', aliases: ['cm', 'configmap', 'configmaps'] },
                {
                  text: 'Persistent volume claims',
                  href: '/c/prod/persistentvolumeclaims',
                  aliases: ['persistentvolumeclaim', 'persistentvolumeclaims', 'pvc'],
                },
              ],
            },
          ],
        },
      },
    },
  } satisfies Record<string, StubReply>;

  function box(): HTMLInputElement {
    const element = menu().querySelector<HTMLInputElement>('#menu-jump');
    if (element === null) throw new Error('no jump box');
    return element;
  }

  it.each([
    ['svc', 'services'],
    ['deploy', 'deployments'],
    ['po', 'pods'],
    ['cm', 'configmaps'],
    ['ns', 'namespaces'],
    ['pvc', 'persistentvolumeclaims'],
  ])('jumps on %s to %s, and the box is empty after', async (short, kind) => {
    stubFetch(kinds);
    mountApp('/c/prod');
    await waitFor(() => {
      expect(menu().textContent).toContain('Services');
    });

    fireEvent.change(box(), { target: { value: short } });
    // Matched, not "No kind here matches svc."
    expect(menu().querySelector('[data-note="no-match"]')).toBeNull();
    expect(menu().querySelector(`[data-kind="${kind}"]`)).not.toBeNull();

    fireEvent.keyDown(box(), { key: 'Enter' });

    await waitFor(() => {
      const here = menu().querySelector('a[aria-current="page"]');
      expect(here?.getAttribute('data-kind')).toBe(kind);
    });
    // The query is gone, and the column is whole again.
    expect(box().value).toBe('');
    expect(menu().querySelectorAll('[data-kind]')).toHaveLength(7);
  });

  it('clears after a jump made with the pointer too', async () => {
    stubFetch(kinds);
    mountApp('/c/prod');
    await waitFor(() => {
      expect(menu().textContent).toContain('Services');
    });

    fireEvent.change(box(), { target: { value: 'dep' } });
    const row = menu().querySelector<HTMLElement>('[data-kind="deployments"]');
    if (row === null) throw new Error('no Deployments row');
    fireEvent.click(row);

    await waitFor(() => {
      expect(menu().querySelector('a[aria-current="page"]')?.getAttribute('data-kind')).toBe('deployments');
    });
    expect(box().value).toBe('');
  });

  it.each([
    ['Ctrl', { ctrlKey: true }],
    ['Cmd', { metaKey: true }],
    ['Shift', { shiftKey: true }],
    ['middle', { button: 1 }],
  ])('keeps the box after a %s click, which opens the kind elsewhere', async (_, init) => {
    stubFetch(kinds);
    mountApp('/c/prod');
    await waitFor(() => {
      expect(menu().textContent).toContain('Services');
    });

    fireEvent.change(box(), { target: { value: 'dep' } });
    const row = menu().querySelector<HTMLElement>('[data-kind="deployments"]');
    if (row === null) throw new Error('no Deployments row');
    fireEvent.click(row, init);

    // This tab did not move, and what was typed is still wanted here.
    expect(box().value).toBe('dep');
    expect(menu().querySelector('a[aria-current="page"]')).toBeNull();
  });

  it('does not guess between two kinds one name answers to, and offers the built-in one', async () => {
    // `service` is core Services and Knative's Service CRD. The custom
    // resource is listed under its API group, as the server sends it.
    const knative = {
      ...kinds,
      '/api/c/prod/nav': {
        body: {
          state: 'ok',
          data: {
            groups: [
              ...kinds['/api/c/prod/nav'].body.data.groups,
              {
                key: 'custom-resources',
                label: 'Custom Resources',
                items: [],
                groups: [
                  {
                    key: 'serving.knative.dev',
                    label: 'serving.knative.dev',
                    items: [
                      {
                        text: 'Services',
                        href: '/c/prod/services.serving.knative.dev',
                        aliases: ['ksvc', 'service', 'services'],
                      },
                    ],
                  },
                ],
              },
            ],
          },
        },
      },
    } satisfies Record<string, StubReply>;
    stubFetch(knative);
    mountApp('/c/prod');
    await waitFor(() => {
      expect(menu().textContent).toContain('Services');
    });

    fireEvent.change(box(), { target: { value: 'service' } });
    expect(menu().querySelectorAll('[data-kind][data-named]')).toHaveLength(2);
    fireEvent.keyDown(box(), { key: 'Enter' });

    // No jump: the focus is on the built-in kind, and the note says why.
    expect(menu().querySelector('a[aria-current="page"]')).toBeNull();
    expect((document.activeElement as HTMLElement | null)?.getAttribute('data-kind')).toBe('services');
    expect(menu().querySelector('[data-note="several-named"]')?.textContent).toContain('2 kinds answer to service');
    expect(box().value).toBe('service');

    // The next keystroke takes the note away.
    fireEvent.change(box(), { target: { value: 'servic' } });
    expect(menu().querySelector('[data-note="several-named"]')).toBeNull();
  });
});

describe('a search never opens the heading whose opening is a cluster read', () => {
  const withDeferred = {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/prod/nav': {
      body: {
        state: 'ok',
        data: {
          groups: [
            {
              key: 'workloads',
              label: 'Workloads',
              items: [{ text: 'Pods', href: '/c/prod/pods' }],
            },
            { key: 'custom-resources', label: 'Custom Resources', items: [], deferred: true },
          ],
        },
      },
    },
  } satisfies Record<string, StubReply>;

  it('says it cannot be searched while it is shut, and asks nothing', async () => {
    stubFetch(withDeferred);
    const asked: string[] = [];
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      asked.push(typeof input === 'string' ? input : String(input));
      return stubbed(input, init);
    });
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });

    fireEvent.change(box(), { target: { value: 'cert' } });

    await waitFor(() => {
      expect(menu().querySelector('[data-note="unsearched"]')).not.toBeNull();
    });
    expect(menu().textContent).toContain('Open to search custom resources.');
    // 23.7 MB and 603-668 ms on one production cluster. A keystroke must not
    // trigger it, and nothing here did. The occupancy read is the column's own,
    // made on arrival, and is not a group's.
    expect(asked.filter((url) => url.includes('/nav/') && !url.includes('/nav/occupancy'))).toEqual([]);
    // And the no-match line points at it rather than declaring the search over.
    expect(menu().textContent).toContain('Open Custom Resources to search them too.');
  });

  function box(): HTMLInputElement {
    const element = menu().querySelector<HTMLInputElement>('#menu-jump');
    if (element === null) throw new Error('no jump box');
    return element;
  }
});

/**
 * The kinds column is shown or hidden.
 *
 * There is no icons-only fold: icons alone make no sense. Hidden is a column that is not on
 * screen; the *Kinds* button and Ctrl+B bring it back, and `/` shows it and
 * puts the keyboard in the jump box. jsdom computes no layout, so hidden is
 * asserted as the class that produces it; the pixels are `rail.spec.ts` case 4.
 *
 * The fixture has one cluster, so there is no rail and Kinds is in the top bar.
 * Where there is a rail it is at the rail's foot
 * (`Shell.test.tsx`, `Rail.test.tsx`).
 */
describe('the column is shown or hidden, and the choice is the person’s', () => {
  const oneGroup = {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/prod/nav': {
      body: {
        state: 'ok',
        data: {
          groups: [
            {
              key: 'workloads',
              label: 'Workloads',
              items: [
                { text: 'Pods', href: '/c/prod/pods', hint: 'Every pod in this cluster' },
                { text: 'Deployments', href: '/c/prod/deployments' },
              ],
            },
          ],
        },
      },
    },
  } satisfies Record<string, StubReply>;

  function kinds(): HTMLButtonElement {
    const control = document.querySelector<HTMLButtonElement>('button[data-kinds-at="corner"]');
    if (control === null) throw new Error('no Kinds control in the corner');
    return control;
  }

  async function mounted(): Promise<void> {
    stubFetch(oneGroup);
    mountApp('/c/prod');
    await waitFor(() => {
      expect(menu().textContent).toContain('Pods');
    });
    // One cluster, so the rail goes once the list answers, and Kinds with it
    // to the bottom left corner: until then it is at the
    // rail's foot.
    await waitFor(() => {
      expect(document.getElementById('rail')).toBeNull();
    });
  }

  it('is the text menu, 192px, and has no fold of any kind', async () => {
    await mounted();
    expect(menu().className).toContain('w-48');
    expect(menu().className).not.toContain('w-12');
    // The chevron at the foot is gone. The one control left at the foot is the
    // Kinds button, there where no rail carries it.
    expect(menu().querySelectorAll('button[aria-controls="menu"]:not([data-kinds-at="corner"])')).toHaveLength(0);
    expect(menu().querySelector('button[title="Jump to a kind"]')).toBeNull();
    // Names printed, titles the server's hint as they always were.
    const pods = menu().querySelector<HTMLElement>('a[data-kind="pods"]')!;
    expect(pods.textContent).toBe('Pods');
    expect(pods.getAttribute('title')).toBe('Every pod in this cluster');
  });

  it('is hidden and shown again by the Kinds button in the bottom left corner', async () => {
    await mounted();
    const button = kinds();
    // In the corner, not in the top bar, and named by its label as in the rail.
    expect(document.getElementById('topbar')?.contains(button)).toBe(false);
    expect(button.getAttribute('aria-label')).toBe('Kinds');
    expect(button.getAttribute('aria-expanded')).toBe('true');

    fireEvent.click(button);
    await waitFor(() => {
      expect(menu().className).toMatch(/(^|\s)hidden(\s|$)/);
    });
    expect(menu().dataset['hidden']).toBe('');
    expect(kinds().getAttribute('aria-expanded')).toBe('false');
    expect(kinds().getAttribute('title')).toBe('Show the kinds column (Ctrl+B)');
    expect(localStorage.getItem('kubetower.menu.mode')).toBe('hidden');

    fireEvent.click(kinds());
    await waitFor(() => {
      expect(menu().className).not.toMatch(/(^|\s)hidden(\s|$)/);
    });
    expect(localStorage.getItem('kubetower.menu.mode')).toBe('shown');
  });

  it('is hidden and shown by Ctrl+B, and not while the keys are typing', async () => {
    await mounted();
    fireEvent.keyDown(window, { key: 'b', ctrlKey: true });
    await waitFor(() => {
      expect(menu().dataset['hidden']).toBe('');
    });
    fireEvent.keyDown(window, { key: 'B', metaKey: true });
    await waitFor(() => {
      expect(menu().dataset['hidden']).toBeUndefined();
    });

    const box = menu().querySelector<HTMLInputElement>('#menu-jump')!;
    box.focus();
    fireEvent.keyDown(box, { key: 'b', ctrlKey: true });
    expect(menu().dataset['hidden']).toBeUndefined();
  });

  it('survives F5 hidden', async () => {
    await mounted();
    fireEvent.click(kinds());
    await waitFor(() => {
      expect(menu().dataset['hidden']).toBe('');
    });

    // What a reload is, here: the tree goes, the module store goes, and the only
    // thing that crosses is what was written to storage.
    cleanup();
    resetOpenGroups();
    resetMenuMode();
    resetNavMemory();
    stubFetch(oneGroup);
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().dataset['hidden']).toBe('');
    });
    // Waited for, because the column's state is read from storage at once and
    // the button's place is not: until the list answers "one cluster" the rail
    // is drawn, and Kinds is at its foot rather than in the top bar.
    await waitFor(() => {
      expect(kinds().getAttribute('aria-expanded')).toBe('false');
    });
  });

  it('opens on a browser that last had the icons, rather than losing the column', async () => {
    localStorage.setItem('kubetower.menu.folded', '1');
    resetMenuMode();
    await mounted();
    expect(menu().dataset['hidden']).toBeUndefined();
    expect(menu().className).toContain('w-48');
    expect(localStorage.getItem('kubetower.menu.folded')).toBeNull();
  });

  it('shows itself and takes the keyboard into the jump box on /', async () => {
    await mounted();
    fireEvent.click(kinds());
    await waitFor(() => {
      expect(menu().dataset['hidden']).toBe('');
    });

    fireEvent.keyDown(window, { key: '/' });

    await waitFor(() => {
      expect(menu().dataset['hidden']).toBeUndefined();
    });
    await waitFor(() => {
      expect(document.activeElement?.id).toBe('menu-jump');
    });
  });

  it('draws no Kinds button where there is no cluster, and so no column', async () => {
    stubFetch(oneGroup);
    mountApp('/settings/general');
    await waitFor(() => {
      expect(document.getElementById('topbar')?.textContent).toContain('KubeTower');
    });
    expect(document.querySelector('button[aria-controls="menu"]')).toBeNull();
  });
});

/**
 * A cluster whose tile is red must not draw its whole menu, every kind a link,
 * each click landing on a list saying *unreachable* again in its own words; and
 * the dashboard - the one page carrying the refusal and the pointer to Health -
 * must be reachable from the menu, through its Overview entry.
 */
describe('a red cluster’s menu is inert, and says why at the top', () => {
  /** What the server sends: Overview first in the cluster group. */
  const withOverview = {
    body: {
      state: 'ok',
      data: {
        groups: [
          {
            key: 'cluster',
            label: 'Cluster',
            items: [
              { text: 'Overview', href: '/c/prod' },
              { text: 'Nodes', href: '/c/prod/nodes' },
              { text: 'Helm releases', href: '/c/prod/helm' },
            ],
          },
          {
            key: 'workloads',
            label: 'Workloads',
            items: [{ text: 'Pods', href: '/c/prod/pods?sort=age' }],
          },
          {
            key: 'custom-resources',
            label: 'Custom Resources',
            items: [],
            groups: [
              {
                key: 'example',
                label: 'example.com',
                items: [{ text: 'Widgets', href: '/c/prod/widgets.example.com' }],
              },
            ],
          },
        ],
      },
    },
  } satisfies StubReply;

  function clusterIn(state: string, message?: string): StubReply {
    const item: Record<string, unknown> = { name: 'prod', label: 'prod', state };
    if (message !== undefined) item['reason'] = { code: 'cluster_unreachable', message };
    return { body: { state: 'ok', items: [item] } };
  }

  function routes(cluster: StubReply): Record<string, StubReply> {
    return {
      '/api/session': session,
      '/api/clusters': cluster,
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
      '/api/c/prod/nav': withOverview,
    };
  }

  function rows(): { live: string[]; inert: string[] } {
    const live = [...menu().querySelectorAll('a')].map((a) => a.textContent ?? '');
    const inert = [...menu().querySelectorAll('[data-inert]')].map((s) => s.textContent ?? '');
    return { live, inert };
  }

  it('offers Overview first, as a live row with its own shape, on a reachable cluster', async () => {
    stubFetch(routes(clusterIn('ok')));
    mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(menu().textContent).toContain('Overview');
    });
    const first = menu().querySelector('details[data-group="cluster"] a');
    expect(first?.textContent).toBe('Overview');
    expect(first?.getAttribute('href')).toBe('/c/prod');
    // Its own glyph, never the unknown-kind fallback: this is the first row of
    // the menu.
    expect(first?.querySelector('use')?.getAttribute('href')).toBe('#i-overview');
    // And not active on a page under the cluster - `end` on the NavLink. The
    // pods row is the current one.
    expect(first?.getAttribute('aria-current')).toBeNull();
    expect(menu().querySelector('[data-inert]')).toBeNull();
    expect(menu().querySelector('[data-note="cluster-state"]')).toBeNull();
  });

  it('marks Overview as the current page on the dashboard itself', async () => {
    stubFetch(routes(clusterIn('ok')));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Overview');
    });
    const first = menu().querySelector('details[data-group="cluster"] a');
    expect(first?.getAttribute('aria-current')).toBe('page');
    // The cluster group is open on arrival: Overview is the page being arrived
    // at and it lives there.
    expect(menu().querySelector('details[data-group="cluster"]')?.hasAttribute('open')).toBe(true);
  });

  /*
    `unreachable` is not in this table: a cluster the
    probe found unreachable has no menu at all, whatever the verdict's age (the
    test after this one). `forbidden` answers, so its menu stays, inert.
  */
  it.each([['forbidden', 'refused', 'the account may not list anything on this cluster']])(
    'on a %s cluster, every row but Overview is inert and the top line says %s',
    async (state, word, sentence) => {
      stubFetch(routes(clusterIn(state, sentence)));
      mountApp('/c/prod');

      await waitFor(() => {
        expect(menu().textContent).toContain('Widgets');
      });

      // The line at the top: the state in `clusterState.ts`'s own word, and the
      // server's sentence after it.
      const line = menu().querySelector('[data-note="cluster-state"]');
      if (line === null) throw new Error('no state line at the top of the menu');
      expect(line.textContent).toBe(`${word}: ${sentence}`);
      expect(line.getAttribute('data-state')).toBe(state);
      // Above the groups, not among them.
      const firstGroup = menu().querySelector('details');
      if (firstGroup === null) throw new Error('no group was drawn');
      expect(line.compareDocumentPosition(firstGroup) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);

      // The groups are still drawn - a person sees what the console would
      // offer - and every kind, Helm and the custom resource is inert.
      const { live, inert } = rows();
      expect(live).toEqual(['Overview']);
      expect(inert).toEqual(['Nodes', 'Helm releases', 'Pods', 'Widgets']);
      for (const dead of menu().querySelectorAll('[data-inert]')) {
        expect(dead.tagName).toBe('SPAN');
        expect(dead.getAttribute('href')).toBeNull();
        expect(dead.getAttribute('aria-disabled')).toBe('true');
        expect(dead.getAttribute('role')).toBe('link');
        expect(dead.className).toContain('text-muted');
        expect(dead.className).not.toContain('hover:');
        // Not walked by the jump box either: `data-kind` is for rows that route.
        expect(dead.hasAttribute('data-kind')).toBe(false);
      }
      // Overview is the way out, and so is the crumb to the console's home.
      const overview = menu().querySelector('a[href="/c/prod"]');
      expect(overview).not.toBeNull();
      expect(overview?.getAttribute('aria-disabled')).toBeNull();
      expect(document.querySelector('#topbar a[href="/"]')).not.toBeNull();
      // Enter in the jump box follows the first row that routes, which is now
      // the only one.
      expect(menu().querySelector('[data-kind]')?.textContent).toBe('Overview');
    },
  );

  it('draws no menu at all on an unreachable cluster, and does not ask for one', async () => {
    // Not inert rows - no rows.
    // The verdict here carries no `checkedAt`, which is the oldest a verdict
    // can look, and the menu is still not drawn.
    const asked: string[] = [];
    stubFetch(routes(clusterIn('unreachable', 'dial tcp 10.0.0.1:6443: i/o timeout')));
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      asked.push(typeof input === 'string' ? input : String(input));
      return stubbed(input, init);
    });
    mountApp('/c/prod');

    await waitFor(() => {
      expect(asked.some((one) => one.includes('/api/clusters'))).toBe(true);
    });
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(menu().querySelector('[data-group], details, a, [data-note="cluster-state"]')).toBeNull();
    expect(asked.filter((one) => one.includes('/nav'))).toEqual([]);
  });

  it('keeps every link on a cluster that is still being probed', async () => {
    // `pending` is not red. A cluster locked out for the ten seconds its probe
    // takes is a slow cluster made unusable.
    stubFetch(routes(clusterIn('pending', 'this cluster has not been probed yet')));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Widgets');
    });
    const { live, inert } = rows();
    expect(inert).toEqual([]);
    expect(live).toEqual(['Overview', 'Nodes', 'Helm releases', 'Pods', 'Widgets']);
    expect(menu().querySelector('[data-note="cluster-state"]')).toBeNull();
  });

  it('keeps every link on a cluster that answered and refused one read', async () => {
    // `unreadable` is a cluster that is there: its lists can still say which
    // read was refused, so its menu is not locked either.
    stubFetch(routes(clusterIn('unreadable', 'credentials expired')));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Widgets');
    });
    expect(rows().inert).toEqual([]);
    expect(menu().querySelector('[data-note="cluster-state"]')).toBeNull();
  });

  it('says the state in words at the top of the column', async () => {
    // `forbidden` and not `unreachable`: an unreachable
    // cluster draws no menu at all, and this line is the inert menu's.
    stubFetch(routes(clusterIn('forbidden', 'the account may not list anything on this cluster')));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Widgets');
    });
    const line = menu().querySelector('[data-note="cluster-state"]');
    // The column has one width, and the word and the sentence fit in it.
    expect(line?.textContent).toBe('refused: the account may not list anything on this cluster');
    expect(line?.className).toContain('text-warn');
  });
});
