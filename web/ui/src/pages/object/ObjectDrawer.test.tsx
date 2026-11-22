import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DetailPage, ListPage } from '../../api';
import { resetKeyboardStack } from '../../keyboard/stack';
import { rememberNamespaces } from '../../shell/namespaceMemory';
import { lastOpen, rememberKind, rememberOpen, resetViewMemory } from '../../shell/viewMemory';
import { mountApp, stubFetch, type StubRoutes } from '../../testing/harness';

/**
 * Clicking a row, at application altitude.
 *
 * **This is the gesture the whole surface hangs off.** It is tested through the
 * real application - the router, the Shell, the Drawer, the list page and the
 * object panel - because the thing being asserted is that they are wired to each
 * other: a test of a predicate is not a test of the page, and a predicate can be
 * covered while its call site is not.
 *
 * Three properties:
 *
 * - **A click opens the panel and does not navigate.** The table keeps
 *   refreshing behind it, and closing returns you to exactly where you were.
 * - **A control inside the row is not the row's action.** If the name is a link
 *   to the object's page and the row opens the drawer, a click on the name must
 *   not fire both.
 * - **The object has an address of its own**, and which of the two shapes it
 *   takes depends on whether it is namespaced.
 */

const session = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

const pods: ListPage = {
  kind: 'pods',
  label: 'Pods',
  namespaced: true,
  columns: [
    {
      key: 'name',
      label: 'Name',
      visible: true,
      order: 0,
      sortable: true,
      secondary: false,
      gauged: false,
      copyable: false,
      prose: false,
      filterable: false,
      role: 'name',
    },
    {
      key: 'namespace',
      label: 'Namespace',
      visible: true,
      order: 1,
      sortable: true,
      secondary: false,
      gauged: false,
      copyable: false,
      prose: false,
      filterable: false,
      role: 'namespace',
    },
  ],
  rows: [
    {
      id: 'shop-prod/storefront-web',
      name: 'storefront-web',
      namespace: 'shop-prod',
      cells: { name: { text: 'storefront-web' }, namespace: { text: 'shop-prod' } },
    },
  ],
};

/** A namespaced kind whose row does not say which namespace. Nothing can supply
 *  one, so the row opens nothing. */
const podsWithoutNamespace: ListPage = {
  ...pods,
  rows: [{ id: 'storefront-web', name: 'storefront-web', cells: { name: { text: 'storefront-web' } } }],
};

const detail: DetailPage = {
  kind: 'pods',
  namespace: 'shop-prod',
  name: 'storefront-web',
  title: 'Pod',
  tabs: [],
  sections: [
    {
      key: 'summary',
      label: 'Summary',
      state: 'ok',
      fields: [{ label: 'Status', value: 'Running', prose: false, pill: false }],
    },
  ],
};

/**
 * The Shell's own reads, plus whatever the test is about.
 *
 * It returns the list of addresses actually asked for, and one test depends on
 * that rather than on the DOM. `stubFetch` is wrapped rather than replaced so
 * that the `Response` under test is still a real one - the harness says why, and
 * the content-type defect it names is the reason.
 */
function shell(extra: StubRoutes = {}): string[] {
  stubFetch({
    '/api/session': session,
    '/api/clusters': { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } },
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    ...extra,
  });
  const stubbed = globalThis.fetch;
  const asked: string[] = [];
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    asked.push(typeof input === 'string' ? input : String(input));
    return stubbed(input, init);
  });
  return asked;
}

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
  resetViewMemory();
});

/** The row holding a given name, as the user sees it. */
function rowFor(name: string): HTMLElement {
  const cell = [...document.querySelectorAll('tbody td')].find(
    (td) => td.textContent?.trim() === name,
  );
  const row = cell?.closest('tr');
  if (row === null || row === undefined) throw new Error(`no row for ${name}`);
  return row as HTMLElement;
}

describe('clicking a row', () => {
  it('opens the object beside the list, and does not navigate away from it', async () => {
    shell({
      '/api/c/prod/r/pods': { body: { state: 'ok', data: pods } },
      '/api/c/prod/r/pods/shop-prod/storefront-web': { body: { state: 'ok', data: detail } },
    });
    const { router } = mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });

    const drawer = document.getElementById('drawer');
    expect(drawer?.hidden).toBe(true);

    await act(async () => {
      fireEvent.click(rowFor('storefront-web'));
      await Promise.resolve();
    });

    await waitFor(() => {
      expect(document.getElementById('drawer')?.textContent ?? '').toContain('Running');
    });

    // The panel, not a page. The list is still on screen behind it and the
    // address bar has not moved: closing the drawer returns the user to
    // exactly where they were, which is the whole argument for a panel.
    expect(router.state.location.pathname).toBe('/c/prod/pods');
    expect(document.querySelector('tbody td')).not.toBeNull();
    expect(document.getElementById('drawer')?.hidden).toBe(false);
  });

  // The panel's open and close buttons are squares of the toolbar's height.
  it("draws the panel's open and close buttons as squares of the toolbar's height", async () => {
    shell({
      '/api/c/prod/r/pods': { body: { state: 'ok', data: pods } },
      '/api/c/prod/r/pods/shop-prod/storefront-web': { body: { state: 'ok', data: detail } },
    });
    mountApp('/c/prod/pods');
    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    await act(async () => {
      fireEvent.click(rowFor('storefront-web'));
      await Promise.resolve();
    });
    const open = await screen.findByRole('link', { name: 'Open as page' });
    const close = screen.getByRole('button', { name: 'Close' });
    for (const one of [open, close]) {
      expect(one.className).toContain('h-8');
      expect(one.className).toContain('w-8');
      expect(one.className).not.toContain('px-2');
    }
  });

  it('does not open it when the control clicked was the namespace filter', async () => {
    // "Remove this and the namespace link opens the drawer as well as filtering,
    // which looks like the filter being ignored."
    //
    // **What is asserted is that the object was never asked for, not that the
    // drawer is empty - which passes with the guard removed.** Picking a
    // namespace changes the address, `Drawer` empties itself on every
    // navigation, so a drawer that opened *and was then
    // closed by the filter it also fired* looks exactly like one that never
    // opened. The request is the thing that cannot be undone by a later effect.
    const asked = shell({
      '/api/c/prod/r/pods': { body: { state: 'ok', data: pods } },
      '/api/c/prod/r/pods/shop-prod/storefront-web': { body: { state: 'ok', data: detail } },
    });
    mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(document.querySelector('[data-role="namespace"]')).not.toBeNull();
    });

    const control = document.querySelector('[data-role="namespace"]');
    await act(async () => {
      if (control !== null) fireEvent.click(control);
      await Promise.resolve();
    });

    expect(asked.some((path) => path.includes('/r/pods/shop-prod/storefront-web'))).toBe(false);
    expect(document.getElementById('drawer')?.hidden).toBe(true);
  });

  it('opens nothing at all for a row that does not name one object', async () => {
    // An ambiguous identity opens no drawer, because nothing here
    // knows which object the user meant. And the row carries no tabstop, so
    // there is not even an affordance suggesting it would.
    const asked = shell({ '/api/c/prod/r/pods': { body: { state: 'ok', data: podsWithoutNamespace } } });
    mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });

    await act(async () => {
      fireEvent.click(rowFor('storefront-web'));
      await Promise.resolve();
    });

    // Nothing was asked for and nothing was drawn. The row is also not a
    // tabstop: an affordance that does nothing is worse than none.
    expect(asked.some((path) => path.includes('/r/pods/'))).toBe(false);
    expect(rowFor('storefront-web').tabIndex).toBe(-1);
    expect(document.getElementById('drawer')?.hidden).toBe(true);
  });

  it('opens on Enter, so the panel is reachable without a pointer', async () => {
    shell({
      '/api/c/prod/r/pods': { body: { state: 'ok', data: pods } },
      '/api/c/prod/r/pods/shop-prod/storefront-web': { body: { state: 'ok', data: detail } },
    });
    mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });

    const row = rowFor('storefront-web');
    expect(row.tabIndex).toBe(0);

    await act(async () => {
      fireEvent.keyUp(row, { key: 'Enter' });
      await Promise.resolve();
    });

    await waitFor(() => {
      expect(document.getElementById('drawer')?.textContent ?? '').toContain('Running');
    });
  });

  it('empties the drawer when the user navigates', async () => {
    // This property is asserted
    // once here with something actually in the drawer: the object it holds
    // carries the cluster it was opened from, so a verb pressed on what looks
    // like staging would go to production.
    shell({
      '/api/c/prod/r/pods': { body: { state: 'ok', data: pods } },
      '/api/c/prod/r/pods/shop-prod/storefront-web': { body: { state: 'ok', data: detail } },
    });
    const { router } = mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    await act(async () => {
      fireEvent.click(rowFor('storefront-web'));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(document.getElementById('drawer')?.hidden).toBe(false);
    });

    await act(async () => {
      await router.navigate('/c/prod');
    });

    expect(document.getElementById('drawer')?.hidden).toBe(true);
  });
});

/**
 * Clicking another row while the drawer is up is another object, and nothing
 * the previous one was holding comes with it.
 *
 * Without a key on the drawer's content, React would keep the same
 * `ObjectDetail` for the next row: a refused preview under Verbs (and
 * Terminals', and Ports') would stay on screen under the new object, and a node
 * shell's plan with it. The refusal is the observable: it is drawn by the
 * panel's own state, so it is still there exactly when the instance was reused.
 */
describe('re-targeting the drawer', () => {
  it('drops what the previous object was holding', async () => {
    const two: ListPage = {
      ...pods,
      rows: [
        ...pods.rows,
        {
          id: 'shop-prod/checkout-api',
          name: 'checkout-api',
          namespace: 'shop-prod',
          cells: { name: { text: 'checkout-api' }, namespace: { text: 'shop-prod' } },
        },
      ],
    };
    const deletable = (name: string): DetailPage => ({
      ...detail,
      name,
      actions: [{ verb: 'delete', label: 'Delete', permitted: true, destructive: true, irreversible: true }],
    });
    shell({
      '/api/c/prod/r/pods': { body: { state: 'ok', data: two } },
      '/api/c/prod/r/pods/shop-prod/storefront-web': { body: { state: 'ok', data: deletable('storefront-web') } },
      '/api/c/prod/r/pods/shop-prod/checkout-api': { body: { state: 'ok', data: deletable('checkout-api') } },
      '/api/c/prod/actions/preview': {
        status: 403,
        contentType: 'application/problem+json',
        body: { status: 403, title: 'Forbidden', code: 'verb_denied', detail: 'storefront-web may not be deleted' },
      },
    });
    mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    // checkout-api first, so its read is cached when the drawer comes back to
    // it. That is the case that kept the instance: an object still being read
    // draws its loading state in place of the panel's sections, which unmounted
    // Verbs and hid the defect behind a spinner.
    await act(async () => {
      fireEvent.click(rowFor('checkout-api'));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(document.getElementById('drawer')?.textContent ?? '').toContain('checkout-api');
    });
    await act(async () => {
      fireEvent.click(rowFor('storefront-web'));
      await Promise.resolve();
    });
    const verb = await waitFor(() => {
      const found = [...(document.getElementById('drawer')?.querySelectorAll('button') ?? [])].find(
        (one) => one.textContent?.trim() === 'Delete',
      );
      if (found === undefined) throw new Error('no Delete in the drawer yet');
      return found;
    });
    await act(async () => {
      fireEvent.click(verb);
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(document.getElementById('drawer')?.textContent ?? '').toContain('may not be deleted');
    });

    await act(async () => {
      fireEvent.click(rowFor('checkout-api'));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(document.getElementById('drawer')?.textContent ?? '').toContain('checkout-api');
    });
    expect(document.getElementById('drawer')?.textContent ?? '').not.toContain('may not be deleted');
  });
});

describe('the object has an address of its own', () => {
  it('draws a namespaced object at /c/:cluster/:kind/:namespace/:name', async () => {
    shell({ '/api/c/prod/r/pods/shop-prod/storefront-web': { body: { state: 'ok', data: detail } } });
    mountApp('/c/prod/pods/shop-prod/storefront-web');

    await waitFor(() => {
      expect(document.querySelector('[data-object-chrome="page"]')?.textContent ?? '').toContain(
        'Running',
      );
    });
    const panel = document.querySelector('[data-object-chrome="page"]');
    expect(panel?.textContent).toContain('storefront-web');
    // The page is not the drawer, and opening one does not fill the other.
    expect(document.getElementById('drawer')?.hidden).toBe(true);
  });

  it('draws a cluster-scoped object at /c/:cluster/:kind/:name, reading the name as a name', async () => {
    // The failure this refuses is exact: reading this shape as the namespaced
    // one would report a node's name as a namespace - a value that passes
    // every validator on its way to being stated as fact.
    const node: DetailPage = {
      kind: 'nodes',
      name: 'ip-10-0-1-14',
      title: 'Node',
      tabs: [],
      sections: [
        {
          key: 'summary',
          label: 'Summary',
          state: 'ok',
          fields: [{ label: 'Status', value: 'Ready', prose: false, pill: false }],
        },
      ],
    };
    shell({ '/api/c/prod/r/nodes/ip-10-0-1-14': { body: { state: 'ok', data: node } } });
    mountApp('/c/prod/nodes/ip-10-0-1-14');

    // A stub answers only the addresses it is given; a request to
    // `/api/c/prod/r/nodes/ip-10-0-1-14/…` would 404 and this would say `absent`,
    // so waiting for `Ready` is waiting for the cluster-scoped address to have
    // been the one asked.
    await waitFor(() => {
      expect(document.querySelector('[data-object-chrome="page"]')?.textContent ?? '').toContain(
        'Ready',
      );
    });
  });

  it('leaves the list route to the list', async () => {
    // `/c/prod/pods` must not be matched by `:kind/:name` with an empty name, and
    // `/c/prod` must not be matched by `:kind` - both are ranked matches rather
    // than an ordered chain.
    shell({ '/api/c/prod/r/pods': { body: { state: 'ok', data: pods } } });
    mountApp('/c/prod/pods');
    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    expect(document.querySelector('[data-object-chrome]')).toBeNull();
  });
});

/**
 * The drawer must not empty on a *query* change: the object panel writes its
 * chosen tab into the query, and a drawer that emptied on it would vanish the
 * moment any tab was pressed.
 *
 * Written here rather than in `Drawer.test.tsx` because the collision is between
 * two components: the drawer's rule about navigation and this panel's rule about
 * the address. A test of either alone passes.
 */
describe('the drawer survives its own tabs', () => {
  it('stays open when the panel puts the chosen tab in the address', async () => {
    shell({
      '/api/c/prod/r/pods': { body: { state: 'ok', data: pods } },
      '/api/c/prod/r/pods/shop-prod/storefront-web': {
        body: {
          state: 'ok',
          data: { ...detail, tabs: [{ key: 'manifest', label: 'Manifest' }] },
        },
      },
      '/api/c/prod/manifest': { body: { state: 'ok', data: { lines: [] } } },
    });
    const { router } = mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    await act(async () => {
      fireEvent.click(rowFor('storefront-web'));
      await Promise.resolve();
    });

    const tab = await waitFor(() => {
      const found = document.getElementById('object-tab-manifest');
      if (found === null) throw new Error('no Manifest tab');
      return found;
    });

    await act(async () => {
      fireEvent.click(tab);
      await Promise.resolve();
    });

    // The address carries the tab, which is what makes a reload and a pasted
    // link land on what was being read.
    await waitFor(() => {
      expect(router.state.location.search).toContain('tab=manifest');
    });
    // And the drawer is still there: it does not empty on its own tab's change.
    expect(document.getElementById('drawer')?.hidden).toBe(false);
    expect(document.querySelector('[data-object-chrome="drawer"]')).not.toBeNull();
  });

  it('still empties when the user actually goes somewhere else', async () => {
    shell({
      '/api/c/prod/r/pods': { body: { state: 'ok', data: pods } },
      '/api/c/prod/r/pods/shop-prod/storefront-web': { body: { state: 'ok', data: detail } },
      '/api/c/staging/r/pods': { body: { state: 'empty', items: [] } },
    });
    const { router } = mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    await act(async () => {
      fireEvent.click(rowFor('storefront-web'));
      await Promise.resolve();
    });
    expect(document.getElementById('drawer')?.hidden).toBe(false);

    // Otherwise an object from a cluster nobody is looking at any more would
    // stay, with its verbs still on it and the old cluster inside them.
    await act(async () => {
      await router.navigate('/c/staging/pods');
    });

    await waitFor(() => {
      expect(document.getElementById('drawer')?.hidden).toBe(true);
    });
  });
});

describe('a control inside a row is not the row', () => {
  it('does not open the drawer when the user ticks a row', async () => {
    shell({
      '/api/c/prod/r/pods': {
        body: {
          state: 'ok',
          data: { ...pods, bulk: { scope: 'prod|pods', verbs: [{ verb: 'delete', label: 'Delete', permitted: true }] } },
        },
      },
      '/api/c/prod/r/pods/shop-prod/storefront-web': { body: { state: 'ok', data: detail } },
    });
    mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });

    const box = document.querySelector('tbody input[type="checkbox"]');
    expect(box).not.toBeNull();
    await act(async () => {
      fireEvent.click(box!);
      await Promise.resolve();
    });

    // The selection happened...
    await waitFor(() => {
      expect(document.getElementById('bulk-bar')).not.toBeNull();
    });
    // ...and the drawer did not. Building a selection of six pods otherwise
    // opens and replaces the drawer six times, and the sixth one stays open over
    // the list the user is working in.
    expect(document.getElementById('drawer')?.hidden).toBe(true);
  });

  it('does not open the drawer when the user follows an address out of the console', async () => {
    // The anchor carries `data-cell-addr`, which the row's guard reads, so
    // opening an Ingress host in a new tab does not also open the drawer under
    // it.
    //
    // As above, the assertion is that the object was never asked for. jsdom
    // does not navigate, so the tab the anchor opens is not observable here;
    // what is observable is the request the drawer would have made. The
    // anchor's default is canceled at the document, after React's own
    // listener has seen the click, only so that jsdom does not log a
    // "navigation to another Document" it would not perform anyway.
    document.addEventListener('click', (event) => event.preventDefault(), { once: true });
    const hosts: ListPage = {
      ...pods,
      columns: [
        ...pods.columns,
        {
          key: 'hosts',
          label: 'Hosts',
          visible: true,
          order: 2,
          sortable: false,
          secondary: false,
          gauged: false,
          copyable: false,
          prose: false,
          filterable: false,
          role: 'other',
        },
      ],
      rows: [
        {
          ...pods.rows[0]!,
          cells: {
            ...pods.rows[0]!.cells,
            hosts: {
              text: 'shop.example.com',
              parts: [{ text: 'shop.example.com', href: 'https://shop.example.com' }],
            },
          },
        },
      ],
    };
    const asked = shell({
      '/api/c/prod/r/pods': { body: { state: 'ok', data: hosts } },
      '/api/c/prod/r/pods/shop-prod/storefront-web': { body: { state: 'ok', data: detail } },
    });
    mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(document.querySelector('tbody a[data-cell-addr]')).not.toBeNull();
    });

    const address = document.querySelector('tbody a[data-cell-addr]');
    await act(async () => {
      if (address !== null) fireEvent.click(address);
      await Promise.resolve();
    });

    expect(asked.some((path) => path.includes('/r/pods/shop-prod/storefront-web'))).toBe(false);
    expect(document.getElementById('drawer')?.hidden).toBe(true);
  });
});

/**
 * A relation in the drawer does not re-target the drawer: clicking the
 * ReplicaSet under a pod would show the ReplicaSet while the page behind it was
 * still the pod list. The user expects to *be* on the ReplicaSets page, narrowed to the namespace, with that object open - the list
 * gives the context the drawer alone does not. So a relation is a navigation,
 * and `Drawer.tsx`'s rule holds: the drawer empties on the way and is re-opened
 * by the destination page reading `?open=`, never carried across.
 */
/**
 * An open object is part of the address: were the drawer to add only `?tab=`, a
 * reload, Back and a copied link would lose it, and no drawer would lead to the
 * object's own page.
 */
describe('the open object in the address', () => {
  const routes: StubRoutes = {
    '/api/c/prod/r/pods': { body: { state: 'ok', data: pods } },
    '/api/c/prod/r/pods/shop-prod/storefront-web': {
      body: { state: 'ok', data: { ...detail, tabs: [{ key: 'events', label: 'Events' }] } },
    },
  };

  async function clickRow(): Promise<void> {
    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    await act(async () => {
      fireEvent.click(rowFor('storefront-web'));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(document.getElementById('drawer')?.textContent ?? '').toContain('Running');
    });
  }

  it('is written by a row click, with the tab cleared in the same navigation', async () => {
    shell(routes);
    const { router } = mountApp('/c/prod/pods?tab=manifest');
    await clickRow();
    await waitFor(() => {
      expect(router.state.location.search).toBe('?open=shop-prod%2Fstorefront-web');
    });
    expect(router.state.location.pathname).toBe('/c/prod/pods');
  });

  it('is removed when the user closes the drawer, so a reload does not reopen it', async () => {
    shell(routes);
    const { router } = mountApp('/c/prod/pods');
    await clickRow();
    await waitFor(() => {
      expect(router.state.location.search).toContain('open=');
    });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Close' }));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(router.state.location.search).toBe('');
    });
    expect(document.getElementById('drawer')?.hidden).toBe(true);
  });

  it('reopens the drawer on a reload of the address', async () => {
    shell(routes);
    const first = mountApp('/c/prod/pods');
    await clickRow();
    await waitFor(() => {
      expect(first.router.state.location.search).toContain('open=');
    });
    const address = `${first.router.state.location.pathname}${first.router.state.location.search}`;
    first.view.unmount();

    // The address as a reload, or a pasted link, would arrive with it.
    const { router } = mountApp(address);
    await waitFor(() => {
      expect(document.getElementById('drawer')?.textContent ?? '').toContain('Running');
    });
    expect(router.state.location.search).toBe('?open=shop-prod%2Fstorefront-web');
  });

  it('is taken out by a narrower filter, so a reload does not call a filtered object gone', async () => {
    // Open a pod, type a filter that hides it, reload. The address kept `open`, and the reload
    // said "no longer among the Pods" for a pod that was only filtered out -
    // and forgot the rail tile's memory of it.
    shell(routes);
    const first = mountApp('/c/prod/pods');
    await clickRow();
    await waitFor(() => {
      expect(first.router.state.location.search).toContain('open=');
    });

    fireEvent.change(screen.getByRole('searchbox', { name: 'Filter Pods' }), {
      target: { value: 'zzz' },
    });
    await waitFor(() => {
      expect(first.router.state.location.search).toBe('?search=zzz');
    });
    const address = `${first.router.state.location.pathname}${first.router.state.location.search}`;
    first.view.unmount();

    mountApp(address);
    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    expect(document.querySelector('[data-note="open-gone"]')).toBeNull();
    expect(document.body.textContent).not.toContain('no longer among');
    expect(lastOpen('prod')).toEqual({ kind: 'pods', namespace: 'shop-prod', name: 'storefront-web' });
  });

  it('says a filter hides the object named by a pasted link, and keeps the tile memory', async () => {
    // The arrival half: a link carrying `open` and a filter that leaves the
    // object out. Nothing says the object is gone, so nothing forgets it.
    const filteredOut: ListPage = { ...pods, rows: pods.rows.filter((row) => row.name !== 'storefront-web') };
    shell({ '/api/c/prod/r/pods': { body: { state: 'ok', data: filteredOut } } });
    rememberKind('prod', 'pods');
    rememberOpen('prod', { kind: 'pods', namespace: 'shop-prod', name: 'storefront-web' });
    mountApp('/c/prod/pods?open=shop-prod%2Fstorefront-web&search=zzz');

    await waitFor(() => {
      expect(document.querySelector('[data-note="open-gone"]')?.textContent).toBe(
        'shop-prod/storefront-web is not among the Pods this filter shows. Nothing was opened; clear the filter or the namespace selection to find it.',
      );
    });
    expect(lastOpen('prod')).toEqual({ kind: 'pods', namespace: 'shop-prod', name: 'storefront-web' });
  });

  it("says an object outside the person's namespaces is not theirs to see, not that it is gone", async () => {
    // A list narrowed by the access review names the namespaces where the
    // person may list the kind; an address naming another one was never theirs.
    const narrowed: ListPage = {
      ...pods,
      rows: pods.rows.filter((row) => row.namespace !== 'team-b'),
      accessNote: 'You may not list pods in every namespace, so this shows only the namespaces where you may.',
      allowedNamespaces: ['shop-prod'],
    };
    shell({ '/api/c/prod/r/pods': { body: { state: 'ok', data: narrowed } } });
    mountApp('/c/prod/pods?open=team-b%2Fdb-0');

    await waitFor(() => {
      expect(document.querySelector('[data-note="open-gone"]')?.textContent).toBe(
        'team-b/db-0 is in team-b, where you may not list Pods, so it is not shown to you. Nothing was opened.',
      );
    });
    expect(document.body.textContent).not.toContain('no longer among');
  });

  it('still says an object in one of the person\'s own namespaces is gone', async () => {
    const narrowed: ListPage = {
      ...pods,
      rows: pods.rows.filter((row) => row.name !== 'storefront-web'),
      accessNote: 'You may not list pods in every namespace, so this shows only the namespaces where you may.',
      allowedNamespaces: ['shop-prod'],
    };
    shell({ '/api/c/prod/r/pods': { body: { state: 'ok', data: narrowed } } });
    mountApp('/c/prod/pods?open=shop-prod%2Fstorefront-web');

    await waitFor(() => {
      expect(document.querySelector('[data-note="open-gone"]')?.textContent).toContain(
        'shop-prod/storefront-web is no longer among the Pods this cluster listed.',
      );
    });
  });

  it("offers the object's own page from the drawer, on the tab being read", async () => {
    shell(routes);
    const { router } = mountApp('/c/prod/pods');
    await clickRow();
    const link = screen.getByRole('link', { name: 'Open as page' });
    expect(link.getAttribute('href')).toBe('/c/prod/pods/shop-prod/storefront-web');

    await act(async () => {
      fireEvent.click(screen.getByRole('tab', { name: 'Activity' }));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(screen.getByRole('link', { name: 'Open as page' }).getAttribute('href')).toBe(
        '/c/prod/pods/shop-prod/storefront-web?tab=activity',
      );
    });

    await act(async () => {
      fireEvent.click(screen.getByRole('link', { name: 'Open as page' }));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/prod/pods/shop-prod/storefront-web');
    });
    expect(document.querySelector('[data-object-chrome="page"]')).not.toBeNull();
  });
});

/*
  The drawer's header is Open as page and Close,
  nothing else - the minimize control is gone - and the page an object is
  opened as carries a Back of its own, because the desktop window has no
  browser bar to go back with.
*/
describe('the way back from an object opened as a page', () => {
  const routes: StubRoutes = {
    '/api/c/prod/r/pods': { body: { state: 'ok', data: pods } },
    '/api/c/prod/r/pods/shop-prod/storefront-web': {
      body: { state: 'ok', data: { ...detail, tabs: [{ key: 'events', label: 'Events' }] } },
    },
  };

  async function openRowOnActivity(): Promise<void> {
    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    await act(async () => {
      fireEvent.click(rowFor('storefront-web'));
      await Promise.resolve();
    });
    await act(async () => {
      fireEvent.click(await screen.findByRole('tab', { name: 'Activity' }));
      await Promise.resolve();
    });
  }

  function back(): HTMLAnchorElement {
    const found = document.querySelector<HTMLAnchorElement>('[data-object-chrome="page"] header a[data-page-back]');
    if (found === null) throw new Error('the page draws no Back');
    return found;
  }

  it('gives the drawer Open as page and Close, and no minimize control', async () => {
    shell(routes);
    mountApp('/c/prod/pods');
    await openRowOnActivity();
    const header = document.querySelector('#drawer [data-object-chrome="drawer"] > header');
    const controls = [...(header?.querySelectorAll('a, button') ?? [])].map(
      (one) => one.getAttribute('aria-label') ?? one.textContent,
    );
    expect(controls).toEqual(['Open as page', 'Close']);
    expect(screen.queryByRole('button', { name: /minimi[sz]e/i })).toBeNull();
    expect(document.querySelector('[data-drawer-minimise], [data-drawer-restore], [data-minimised]')).toBeNull();
  });

  it('draws Back at the left of the page header, and none in the drawer', async () => {
    shell(routes);
    mountApp('/c/prod/pods/shop-prod/storefront-web');
    await waitFor(() => {
      expect(back().getAttribute('title')).toBe('Back');
    });
    expect(back().closest('header')?.firstElementChild).toBe(back());
    expect(screen.getByRole('link', { name: 'Back' })).toBe(back());
  });

  it('returns from Open as page to the list, with the drawer open on the same object and tab', async () => {
    shell(routes);
    const { router } = mountApp('/c/prod/pods?namespace=shop-prod');
    await openRowOnActivity();
    await act(async () => {
      fireEvent.click(screen.getByRole('link', { name: 'Open as page' }));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/prod/pods/shop-prod/storefront-web');
    });
    expect(back().getAttribute('href')).toBe(
      '/c/prod/pods?namespace=shop-prod&open=shop-prod%2Fstorefront-web&tab=activity',
    );

    await act(async () => {
      fireEvent.click(back());
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(document.querySelector('#drawer [data-object-chrome="drawer"] h2')?.textContent).toBe('storefront-web');
    });
    expect(document.getElementById('drawer')?.hidden).toBe(false);
    expect(router.state.location.pathname).toBe('/c/prod/pods');
    expect(document.querySelector('tbody td')).not.toBeNull();
    await waitFor(() => {
      const query = new URLSearchParams(router.state.location.search);
      expect([query.getAll('namespace'), query.get('open'), query.get('tab')]).toEqual([
        ['shop-prod'],
        'shop-prod/storefront-web',
        'activity',
      ]);
    });
    expect(screen.getByRole('tab', { name: 'Activity' }).getAttribute('aria-selected')).toBe('true');
  });

  it("keeps the way back through the page's own tab clicks, on the tab chosen there", async () => {
    shell(routes);
    const { router } = mountApp('/c/prod/pods');
    await openRowOnActivity();
    await act(async () => {
      fireEvent.click(screen.getByRole('link', { name: 'Open as page' }));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/prod/pods/shop-prod/storefront-web');
    });
    // A tab is a query write on the page's own entry: the entry's state, and
    // with it the way back, survives it - and Back follows the tab chosen on
    // the page, not the one the page was opened on.
    await act(async () => {
      fireEvent.click(await screen.findByRole('tab', { name: 'Details' }));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(router.state.location.search).toBe('');
    });
    expect(back().getAttribute('href')).toBe('/c/prod/pods?open=shop-prod%2Fstorefront-web');
  });

  // Back opens the drawer on the tab last chosen
  // on the page, not the tab the page was opened from.
  it('returns to the drawer on the tab last chosen on the page', async () => {
    shell(routes);
    const { router } = mountApp('/c/prod/pods?namespace=shop-prod');
    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    await act(async () => {
      fireEvent.click(rowFor('storefront-web'));
      await Promise.resolve();
    });
    // Opened as a page from the drawer on Details.
    await act(async () => {
      fireEvent.click(await screen.findByRole('link', { name: 'Open as page' }));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/prod/pods/shop-prod/storefront-web');
    });
    await act(async () => {
      fireEvent.click(await screen.findByRole('tab', { name: 'Activity' }));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(router.state.location.search).toBe('?tab=activity');
    });
    expect(back().getAttribute('href')).toBe(
      '/c/prod/pods?namespace=shop-prod&open=shop-prod%2Fstorefront-web&tab=activity',
    );

    await act(async () => {
      fireEvent.click(back());
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(document.querySelector('#drawer [data-object-chrome="drawer"] h2')?.textContent).toBe('storefront-web');
    });
    expect(router.state.location.pathname).toBe('/c/prod/pods');
    await waitFor(() => {
      expect(new URLSearchParams(router.state.location.search).get('tab')).toBe('activity');
    });
    expect(screen.getByRole('tab', { name: 'Activity' }).getAttribute('aria-selected')).toBe('true');
  });

  it("goes back to the kind's list from a page loaded directly", async () => {
    shell(routes);
    const { router } = mountApp('/c/prod/pods/shop-prod/storefront-web?tab=activity');
    await waitFor(() => {
      expect(back().getAttribute('href')).toBe('/c/prod/pods');
    });
    await act(async () => {
      fireEvent.click(back());
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/prod/pods');
    });
    expect(document.getElementById('drawer')?.hidden).toBe(true);
  });

  it("keeps the cluster's namespace scope on the way back from a page loaded directly", async () => {
    shell(routes);
    rememberNamespaces('prod', 'pods', ['shop-prod', 'shop-stage']);
    mountApp('/c/prod/pods/shop-prod/storefront-web');
    await waitFor(() => {
      expect(back().getAttribute('href')).toBe('/c/prod/pods?namespace=shop-prod&namespace=shop-stage');
    });
  });
});

describe('following a relation from the drawer', () => {
  const withRelation = {
    ...detail,
    relations: [
      {
        role: 'Controlled by',
        kind: 'replicasets',
        namespace: 'shop-prod',
        name: 'storefront-web-7c9',
        label: 'storefront-web-7c9',
        linkable: true,
      },
    ],
  };

  const replicasets: ListPage = {
    ...pods,
    kind: 'replicasets',
    label: 'ReplicaSets',
    rows: [
      {
        id: 'shop-prod/storefront-web-7c9',
        name: 'storefront-web-7c9',
        namespace: 'shop-prod',
        cells: { name: { text: 'storefront-web-7c9' }, namespace: { text: 'shop-prod' } },
      },
    ],
  };

  const replicaset: DetailPage = {
    ...detail,
    kind: 'replicasets',
    name: 'storefront-web-7c9',
    title: 'ReplicaSet',
  };

  function relation(): HTMLElement {
    const found = document
      .getElementById('drawer')
      ?.querySelector('a[href*="storefront-web-7c9"]');
    if (found === null || found === undefined) throw new Error('no relation link');
    return found as HTMLElement;
  }

  function routes(): StubRoutes {
    return {
      '/api/c/prod/r/pods': { body: { state: 'ok', data: pods } },
      '/api/c/prod/r/pods/shop-prod/storefront-web': { body: { state: 'ok', data: withRelation } },
      '/api/c/prod/r/replicasets': { body: { state: 'ok', data: replicasets } },
      '/api/c/prod/r/replicasets/shop-prod/storefront-web-7c9': {
        body: { state: 'ok', data: replicaset },
      },
    };
  }

  async function openPod(): Promise<void> {
    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    await act(async () => {
      fireEvent.click(rowFor('storefront-web'));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(document.getElementById('drawer')?.textContent).toContain('storefront-web-7c9');
    });
  }

  it('goes to the related kind, narrowed to the namespace, with the object open', async () => {
    const asked = shell(routes());
    const { router } = mountApp('/c/prod/pods');
    await openPod();

    // The address the row carries is the destination, in full: the list, the
    // namespace, and the object to open. A middle-click gets the same thing.
    expect(relation().getAttribute('href')).toBe(
      '/c/prod/replicasets?namespace=shop-prod&open=storefront-web-7c9',
    );

    await act(async () => {
      fireEvent.click(relation());
      await Promise.resolve();
    });

    // On the ReplicaSets page, not the pods page with a different drawer.
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/prod/replicasets');
    });
    // Narrowed to the pod's namespace through the list's own parameter - the
    // request says so, which is the thing a DOM assertion cannot.
    await waitFor(() => {
      expect(asked).toContain('/api/c/prod/r/replicasets?namespace=shop-prod');
    });
    // And the ReplicaSet is open beside its own list.
    await waitFor(() => {
      expect(document.getElementById('drawer')?.textContent ?? '').toContain('ReplicaSet');
    });
    expect(document.getElementById('drawer')?.hidden).toBe(false);
    expect(rowFor('storefront-web-7c9')).not.toBeNull();
    expect(router.state.location.search).toContain('namespace=shop-prod');
  });

  it('keeps the open object in the address, in the namespaced form', async () => {
    // It stays once the drawer is up, so a reload, Back and a copied link keep
    // the object being read, in
    // the `<namespace>/<name>` form that names one row whatever the list's
    // namespace selection is, and leaves when the drawer is closed (pinned in
    // `the open object in the address` above).
    shell(routes());
    const { router } = mountApp('/c/prod/pods');
    await openPod();
    await act(async () => {
      fireEvent.click(relation());
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(document.getElementById('drawer')?.textContent ?? '').toContain('ReplicaSet');
    });
    await waitFor(() => {
      expect(router.state.location.search).toBe(
        '?namespace=shop-prod&open=shop-prod%2Fstorefront-web-7c9',
      );
    });
  });

  it('opens the object even when its list was already read', async () => {
    // The warm case, and the one a cold-cache test cannot see. With the list
    // in the cache the page has rows on the commit that arrives - the same
    // commit on which the `Drawer`, the Outlet's later sibling, empties itself
    // for the new path. A drawer opened on that commit is closed on that
    // commit. `ResourceList.tsx` opens on a later one for this reason.
    shell(routes());
    const { router } = mountApp('/c/prod/replicasets?namespace=shop-prod');
    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    await act(async () => {
      await router.navigate('/c/prod/pods');
    });
    await openPod();

    await act(async () => {
      fireEvent.click(relation());
      await Promise.resolve();
    });

    await waitFor(() => {
      expect(document.getElementById('drawer')?.textContent ?? '').toContain('ReplicaSet');
    });
    expect(router.state.location.pathname).toBe('/c/prod/replicasets');
    expect(document.getElementById('drawer')?.hidden).toBe(false);
  });

  it('is undone by Back, which returns to the pod list', async () => {
    shell(routes());
    const { router } = mountApp('/c/prod/pods');
    await openPod();
    await act(async () => {
      fireEvent.click(relation());
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(document.getElementById('drawer')?.textContent ?? '').toContain('ReplicaSet');
    });

    await act(async () => {
      await router.navigate(-1);
    });

    // One step back is the pod list - `open` is written with `replace`, so no
    // entry was added for it - and the pod the user was reading is open
    // again, because its entry kept it.
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/prod/pods');
    });
    expect(router.state.location.search).toBe('?open=shop-prod%2Fstorefront-web');
    await waitFor(() => {
      expect(document.querySelector('[data-object-chrome="drawer"] h2')?.textContent).toBe(
        'storefront-web',
      );
    });
  });

  it('leaves a modified click alone, so ctrl-click still opens a tab', async () => {
    shell(routes());
    const { router } = mountApp('/c/prod/pods');
    await openPod();

    const link = relation();
    const event = new MouseEvent('click', { bubbles: true, cancelable: true, ctrlKey: true });
    await act(async () => {
      link.dispatchEvent(event);
      await Promise.resolve();
    });

    // Not swallowed: the browser is left to do what a ctrl-click does. A console
    // where opening a thing in a second tab does not work is the one thing worse
    // than a console that navigates when it should not.
    expect(event.defaultPrevented).toBe(false);
    expect(router.state.location.pathname).toBe('/c/prod/pods');
  });

  it("stays a link to the related object's own page on the object's own page", async () => {
    // No drawer to open on a page, so the relation is the navigation it always
    // was there: the other object's route.
    shell(routes());
    mountApp('/c/prod/pods/shop-prod/storefront-web');
    await waitFor(() => {
      expect(document.querySelector('[data-object-chrome="page"]')?.textContent ?? '').toContain(
        'storefront-web-7c9',
      );
    });
    const link = document.querySelector('[data-object-chrome="page"] a[href*="storefront-web-7c9"]');
    expect(link?.getAttribute('href')).toBe('/c/prod/replicasets/shop-prod/storefront-web-7c9');
  });
});

/**
 * The other half, read on its own: the list opens the named row's
 * drawer on arrival, whoever wrote the address.
 */
describe('a list arriving with ?open=', () => {
  const replicasets: ListPage = {
    ...pods,
    kind: 'replicasets',
    label: 'ReplicaSets',
    rows: [
      {
        id: 'shop-prod/storefront-web-7c9',
        name: 'storefront-web-7c9',
        namespace: 'shop-prod',
        cells: { name: { text: 'storefront-web-7c9' }, namespace: { text: 'shop-prod' } },
      },
    ],
  };

  it('opens that row and keeps the parameter, in the namespaced form', async () => {
    shell({
      '/api/c/prod/r/replicasets': { body: { state: 'ok', data: replicasets } },
      '/api/c/prod/r/replicasets/shop-prod/storefront-web-7c9': {
        body: {
          state: 'ok',
          data: { ...detail, kind: 'replicasets', name: 'storefront-web-7c9', title: 'ReplicaSet' },
        },
      },
    });
    const { router } = mountApp('/c/prod/replicasets?namespace=shop-prod&open=storefront-web-7c9');

    await waitFor(() => {
      expect(document.getElementById('drawer')?.textContent ?? '').toContain('ReplicaSet');
    });
    await waitFor(() => {
      expect(router.state.location.search).toBe(
        '?namespace=shop-prod&open=shop-prod%2Fstorefront-web-7c9',
      );
    });
  });

  it('opens nothing for a name the list does not hold, and still forgets the parameter', async () => {
    // Not an error and not a guess. The object is not in the list the user
    // is looking at, so a drawer about it would contradict the page behind it;
    // the list itself says the object is not there.
    const asked = shell({ '/api/c/prod/r/replicasets': { body: { state: 'ok', data: replicasets } } });
    const { router } = mountApp('/c/prod/replicasets?namespace=shop-prod&open=gone');

    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    await waitFor(() => {
      expect(router.state.location.search).toBe('?namespace=shop-prod');
    });
    expect(asked.some((path) => path.includes('/r/replicasets/'))).toBe(false);
    expect(document.getElementById('drawer')?.hidden).toBe(true);
  });
});

/**
 * The rail tile returns to the drawer that was open, unless the user closed it.
 *
 * `Drawer.tsx` empties on every navigation and that rule is not weakened here:
 * nothing is carried across clusters. What is added is a memory of *which*
 * object was open on a cluster, so that the rail tile can bring the user
 * back through `?open=` - the arrival mechanism the list already has - and the
 * destination page re-opens its own object, for its own cluster.
 *
 * **The whole design is one distinction, and it is the first thing tested.**
 * The memory is cleared when the user shuts the drawer - the × and Escape -
 * and kept when a navigation empties it. Without that, coming back re-opens a
 * drawer the user deliberately closed, which is worse than forgetting, and
 * it is the one place this can be wrong while every other test stays green.
 */
describe('what the drawer remembers across a cluster switch', () => {
  const two = {
    body: {
      state: 'ok',
      items: [
        { name: 'prod', label: 'prod', state: 'ok' },
        { name: 'staging', label: 'staging', state: 'ok' },
      ],
    },
  };
  const routes: StubRoutes = {
    '/api/clusters': two,
    '/api/c/prod/r/pods': { body: { state: 'ok', data: pods } },
    '/api/c/prod/r/pods/shop-prod/storefront-web': { body: { state: 'ok', data: detail } },
    '/api/c/staging/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/c/staging/extensions': { body: { state: 'empty', items: [] } },
  };
  const opened = { kind: 'pods', namespace: 'shop-prod', name: 'storefront-web' };

  async function openTheRow(): Promise<void> {
    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    await act(async () => {
      fireEvent.click(rowFor('storefront-web'));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(document.getElementById('drawer')?.hidden).toBe(false);
    });
  }

  it('remembers the object a row opened, and keeps it when a navigation empties the drawer', async () => {
    shell(routes);
    const { router } = mountApp('/c/prod/pods');
    await openTheRow();
    expect(lastOpen('prod')).toEqual(opened);

    // The rail's own navigation: another cluster. The drawer is emptied -
    // an object and its verbs must not follow the user to
    // another cluster's page - and the memory is what survives it.
    await act(async () => {
      await router.navigate('/c/staging');
    });
    expect(document.getElementById('drawer')?.hidden).toBe(true);
    expect(lastOpen('prod')).toEqual(opened);
    // And nothing about staging was invented.
    expect(lastOpen('staging')).toBeUndefined();
  });

  it('forgets it when the user closes the drawer with the × button', async () => {
    shell(routes);
    mountApp('/c/prod/pods');
    await openTheRow();
    expect(lastOpen('prod')).toEqual(opened);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Close' }));
      await Promise.resolve();
    });

    expect(document.getElementById('drawer')?.hidden).toBe(true);
    expect(lastOpen('prod')).toBeUndefined();
  });

  it('forgets it when the user closes the drawer with Escape', async () => {
    shell(routes);
    mountApp('/c/prod/pods');
    await openTheRow();

    await act(async () => {
      fireEvent.keyDown(window, { key: 'Escape' });
      await Promise.resolve();
    });

    expect(document.getElementById('drawer')?.hidden).toBe(true);
    expect(lastOpen('prod')).toBeUndefined();
  });

  it('re-opens the remembered object on arrival, from the namespace/name form the tile links', async () => {
    // The tile's href carries `?open=<namespace>/<name>`: the list it lands on
    // is not narrowed to a namespace, so a bare name could match several rows,
    // and the namespaced form names exactly one.
    shell(routes);
    const { router } = mountApp('/c/prod/pods?open=shop-prod%2Fstorefront-web');

    await waitFor(() => {
      expect(document.getElementById('drawer')?.textContent ?? '').toContain('Running');
    });
    // Still in the address once the drawer is up.
    await waitFor(() => {
      expect(router.state.location.search).toBe('?open=shop-prod%2Fstorefront-web');
    });
    // And the arrival re-remembers it, so a further switch returns again.
    expect(lastOpen('prod')).toEqual(opened);
  });

  it('does not match a namespaced form against a row in another namespace', async () => {
    shell(routes);
    mountApp('/c/prod/pods?open=other-ns%2Fstorefront-web');

    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    await waitFor(() => {
      expect(document.querySelector('[data-note="open-gone"]')).not.toBeNull();
    });
    expect(document.getElementById('drawer')?.hidden).toBe(true);
  });

  it('says so when the remembered object is no longer in the list, opens nothing, and forgets it', async () => {
    // The pod was deleted or rescheduled while the user was on the other
    // cluster. The list says the object is not
    // here rather than silently opening nothing - and the memory is cleared,
    // so the next return does not repeat a notice about it.
    rememberKind('prod', 'pods');
    rememberOpen('prod', { kind: 'pods', namespace: 'shop-prod', name: 'storefront-web-old' });
    const asked = shell(routes);
    const { router } = mountApp('/c/prod/pods?open=shop-prod%2Fstorefront-web-old');

    await waitFor(() => {
      expect(document.querySelector('[data-note="open-gone"]')).not.toBeNull();
    });
    const note = document.querySelector('[data-note="open-gone"]')?.textContent ?? '';
    expect(note).toContain('storefront-web-old');
    expect(note).toContain('shop-prod');
    expect(note).toContain('no longer');
    expect(document.getElementById('drawer')?.hidden).toBe(true);
    expect(asked.some((path) => path.includes('/r/pods/'))).toBe(false);
    await waitFor(() => {
      expect(router.state.location.search).toBe('');
    });
    expect(lastOpen('prod')).toBeUndefined();
    // The list itself is drawn under the notice: the notice is about the
    // arrival, not about the list.
    expect(document.querySelector('tbody td')).not.toBeNull();
  });

  it('takes the notice down when a row is opened', async () => {
    shell(routes);
    mountApp('/c/prod/pods?open=shop-prod%2Fstorefront-web-old');
    await waitFor(() => {
      expect(document.querySelector('[data-note="open-gone"]')).not.toBeNull();
    });

    await openTheRow();

    expect(document.querySelector('[data-note="open-gone"]')).toBeNull();
  });
});

/**
 * The tab lives in the address and survives a query-only navigation, so a fresh
 * object must be told to land on its summary: opening pod A on Manifest and then
 * clicking pod B opens B on its summary. An address that *arrives* with a tab -
 * reload, pasted link - still lands on it. Only the row click forgets.
 */
describe('which tab another object lands on', () => {
  const twoPods: ListPage = {
    ...pods,
    rows: [
      ...pods.rows,
      {
        id: 'shop-prod/storefront-worker',
        name: 'storefront-worker',
        namespace: 'shop-prod',
        cells: { name: { text: 'storefront-worker' }, namespace: { text: 'shop-prod' } },
      },
    ],
  };
  const withManifest = { ...detail, tabs: [{ key: 'manifest', label: 'Manifest' }] };

  it('lands on the default tab when another row is opened from a row on Manifest', async () => {
    shell({
      '/api/c/prod/r/pods': { body: { state: 'ok', data: twoPods } },
      '/api/c/prod/r/pods/shop-prod/storefront-web': { body: { state: 'ok', data: withManifest } },
      '/api/c/prod/r/pods/shop-prod/storefront-worker': {
        body: { state: 'ok', data: { ...withManifest, name: 'storefront-worker' } },
      },
      '/api/c/prod/manifest': { body: { state: 'ok', data: { lines: [] } } },
    });
    const { router } = mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    await act(async () => {
      fireEvent.click(rowFor('storefront-web'));
      await Promise.resolve();
    });
    const tab = await waitFor(() => {
      const found = document.getElementById('object-tab-manifest');
      if (found === null) throw new Error('no Manifest tab');
      return found;
    });
    await act(async () => {
      fireEvent.click(tab);
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(router.state.location.search).toContain('tab=manifest');
    });

    await act(async () => {
      fireEvent.click(rowFor('storefront-worker'));
      await Promise.resolve();
    });

    // B, on Details. The address no longer names a tab: the row click is a new
    // object, not a reload of the one being read.
    await waitFor(() => {
      expect(document.getElementById('drawer')?.querySelector('h2')?.textContent).toBe(
        'storefront-worker',
      );
    });
    await waitFor(() => {
      expect(router.state.location.search).not.toContain('tab=');
    });
    expect(document.getElementById('object-tab-details')?.getAttribute('aria-selected')).toBe(
      'true',
    );
    expect(document.getElementById('object-panel-manifest')).toBeNull();
    // Still the list, still the drawer: the row click did not become a page.
    expect(router.state.location.pathname).toBe('/c/prod/pods');
    expect(document.getElementById('drawer')?.hidden).toBe(false);
  });

  it('still honors a tab the address arrived with', async () => {
    // The half that stays: a reload and a pasted link land on what was being
    // read. Asserted on the object's own page, which is what a reload lands on.
    shell({
      '/api/c/prod/r/pods/shop-prod/storefront-web': { body: { state: 'ok', data: withManifest } },
      '/api/c/prod/manifest': { body: { state: 'ok', data: { lines: [] } } },
    });
    mountApp('/c/prod/pods/shop-prod/storefront-web?tab=manifest');

    await waitFor(() => {
      expect(document.getElementById('object-tab-manifest')?.getAttribute('aria-selected')).toBe(
        'true',
      );
    });
    expect(document.getElementById('object-panel-manifest')).not.toBeNull();
  });
});
