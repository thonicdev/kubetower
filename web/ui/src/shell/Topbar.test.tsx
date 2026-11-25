import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { resetLeaving, watchLeaving } from '../auth/leave';
import { clusterPath } from '../clusters/clusterPath';
import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp, stubFetch, type StubRoutes } from '../testing/harness';
import { patientWithALoadedRunner } from '../testing/loadedRunner';
import { rememberNamespaces, resetNamespaceMemory } from './namespaceMemory';

patientWithALoadedRunner();

// The three pages these tests land on, compiled before the first test rather
// than inside its wait. Each page is a lazy chunk, and the first test to reach
// one would otherwise pay for transforming its whole module graph inside its
// `findBy*` - seconds in a container held to two CPUs. The routes still import
// them lazily; the module is simply already there when they do. That alone
// does not steady this file under load - the first test still pays for the
// Shell's first mount - so the ceiling above does that work.
beforeAll(async () => {
  await Promise.all([
    import('../pages/overview/Overview'),
    import('../pages/resources/ResourceList'),
    import('../pages/settings/Settings'),
    import('../pages/object/ObjectPage'),
    import('../pages/helm/HelmRelease'),
  ]);
});

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
  localStorage.clear();
  resetNamespaceMemory();
  document.title = '';
  resetLeaving();
});

const ARN = 'arn:aws:eks:eu-west-1:1234:cluster/prod';

const capabilities = {
  mode: 'desktop',
  localShell: true,
  podExec: true,
  nodeShell: true,
  assistant: false,
  portForward: true,
  readOnly: { writesRefused: false, detailWritesRefused: false },
};

/** Three clusters: one renamed, one not, and the short
 *  name the list tests use. The first pair is the whole of the nameplate rule -
 *  a fixture where every cluster is named cannot show that an unnamed one falls
 *  back to its real name rather than to a blank. */
const clusters = {
  state: 'ok',
  items: [
    { name: ARN, label: 'Production', initials: 'PR', state: 'ok' },
    { name: 'docker-desktop', label: 'docker-desktop', initials: 'DD', state: 'ok' },
    { name: 'prod', label: 'prod', initials: 'PR', state: 'ok' },
  ],
};

const podList = {
  state: 'ok',
  data: {
    kind: 'pods',
    label: 'Pods',
    refreshSeconds: 2,
    namespaced: true,
    columns: [{ key: 'name', label: 'Name', role: 'name' }],
    rows: [],
    bulk: { scope: 'kind', verbs: [] },
  },
};

const nav = { state: 'ok', data: { groups: [] } };
const preferences = {
  columns: { density: 'comfortable', byKind: {} },
  rail: { order: [], hotbar: [], hidden: [], nameplates: {} },
};

function stub(extra: StubRoutes = {}): void {
  stubFetch({
    '/api/session': { body: { user: 'operator', expiresAt: '2099-01-01T00:00:00Z' } },
    '/api/capabilities': { body: capabilities },
    '/api/clusters': { body: clusters },
    '/api/preferences': { body: preferences },
    ...extra,
  });
}

/** A cluster page that draws a list, which is the only page that declares a
 *  refresh today. */
function onPodList(extra: StubRoutes = {}): void {
  stub({
    '/api/c/prod/nav': { body: nav },
    '/api/c/prod/r/pods': { body: podList },
    '/api/c/prod/namespaces': { body: { state: 'ok', items: [] } },
    ...extra,
  });
}

function topbar(): HTMLElement {
  const element = document.getElementById('topbar');
  if (element === null) throw new Error('no topbar in the document');
  return element;
}

describe('the breadcrumb', () => {
  it('shows the nameplate and addresses the real context name', async () => {
    stub({ [`/api/c/${ARN}/nav`]: { body: nav } });
    mountApp(`/c/${encodeURIComponent(ARN)}`);

    const link = await screen.findByRole('link', { name: 'Production' });
    // The nameplate is what is read; the real name is what is addressed, and it
    // is on the tooltip so whoever has to type it into kubectl can take it from
    // here rather than from Settings. Its second line: the first says where
    // the link goes.
    expect(link.getAttribute('title')).toBe(`Cluster overview\n${ARN}`);
    // In the one spelling every link to a cluster uses,
    // whichever spelling the address was arrived on.
    expect(link.getAttribute('href')).toBe(clusterPath(ARN));
  });

  it('shows its own tooltip under the pointer, wherever on the name the pointer rests', async () => {
    /*
      A browser shows the innermost `title` above the element under the pointer,
      so the attribute on the link proves nothing if the name drawn inside it
      carries one of its own - which `ClusterName` does everywhere else, and
      which would hide "Cluster overview" and the context name here.
      So this reads what a hover would show: from every element that paints a
      piece of the name, the nearest `title` going up.
    */
    stub({ [`/api/c/${ARN}/nav`]: { body: nav } });
    mountApp(`/c/${encodeURIComponent(ARN)}`);

    const link = await screen.findByRole('link', { name: 'Production' });
    const painted = [...link.querySelectorAll('*')].filter((each) =>
      [...each.childNodes].some((node) => node.nodeType === Node.TEXT_NODE && (node.textContent ?? '') !== ''),
    );
    expect(painted.length).toBeGreaterThan(0);
    for (const each of painted) {
      expect(each.closest('[title]')?.getAttribute('title')).toBe(`Cluster overview\n${ARN}`);
    }
  });

  it('falls back to the real name for a cluster nobody renamed', async () => {
    stub({ '/api/c/docker-desktop/nav': { body: nav } });
    mountApp('/c/docker-desktop');

    // Not blank and not a placeholder. A cluster carries its own name as its
    // label until somebody changes it, so "has a nameplate" is the two
    // differing rather than the label being non-empty.
    const link = await screen.findByRole('link', { name: 'docker-desktop' });
    expect(link.getAttribute('title')).toBe('Cluster overview\ndocker-desktop');
  });

  it('is a link to the cluster overview from any page on it, and looks like one', async () => {
    /*
      This segment is a `Link` to `/c/<cluster>`; drawn `text-fg` with an
      underline on hover only, it would read as a title. So it wears
      the affordance every other in-sentence navigation here wears, and the
      tooltip says where it goes.
    */
    onPodList();
    mountApp('/c/prod/pods');

    const link = await screen.findByRole('link', { name: 'prod' });
    expect(link.getAttribute('href')).toBe('/c/prod');
    expect(link.className).toContain('text-accent');
    expect(link.getAttribute('title')).toBe('Cluster overview\nprod');
  });

  /*
    On an object page the kind and the namespace name the list the object
    belongs to. Each segment that names a place is a link to it, and the
    last - the object, where you are - is not.
  */
  it('links an object’s kind to its list and its namespace to that list narrowed, and not its name', async () => {
    stub({
      '/api/c/docker-desktop/nav': { body: nav },
      '/api/c/docker-desktop/r/pods/default/api-0': {
        body: {
          state: 'ok',
          data: { kind: 'pods', name: 'api-0', namespace: 'default', title: 'Pod', tabs: [], sections: [] },
        },
      },
    });
    mountApp('/c/docker-desktop/pods/default/api-0');

    const crumbs = within(screen.getByRole('navigation', { name: 'Breadcrumb' }));
    const kind = await crumbs.findByRole('link', { name: 'Pod' });
    expect(kind.getAttribute('href')).toBe('/c/docker-desktop/pods');
    expect(kind.className).toContain('text-accent');
    expect(crumbs.getByRole('link', { name: 'default' }).getAttribute('href')).toBe(
      '/c/docker-desktop/pods?namespace=default',
    );
    expect(crumbs.getByText('api-0').closest('a')).toBeNull();
    // Each segment is its own item, in order, after the cluster's.
    const items = crumbs
      .getAllByRole('listitem')
      .map((item) => item.textContent ?? '')
      .filter((text) => text !== '/');
    expect(items.slice(-3)).toEqual(['Pod', 'default', 'api-0']);
    // The label is cut inside the link and nothing around the link clips: an
    // item with `overflow: hidden` (`truncate`) cuts off the focus ring, which
    // is drawn outside the link.
    for (const link of [kind, crumbs.getByRole('link', { name: 'default' })]) {
      expect(link.querySelector('span.truncate')).not.toBeNull();
      expect(link.className).not.toMatch(/truncate|overflow-hidden/);
      expect(link.closest('li')?.className).not.toMatch(/truncate|overflow-hidden/);
    }
  });

  it('carries the remembered namespace on the kind, as the menu does, and not on the namespace', async () => {
    // The kind crumb opens the list as the menu would (namespaceMemory.ts).
    rememberNamespaces('docker-desktop', 'pods', ['kube-system']);
    stub({
      '/api/c/docker-desktop/nav': { body: nav },
      '/api/c/docker-desktop/r/pods/default/api-0': {
        body: {
          state: 'ok',
          data: { kind: 'pods', name: 'api-0', namespace: 'default', title: 'Pod', tabs: [], sections: [] },
        },
      },
    });
    mountApp('/c/docker-desktop/pods/default/api-0');

    const crumbs = within(screen.getByRole('navigation', { name: 'Breadcrumb' }));
    expect((await crumbs.findByRole('link', { name: 'Pod' })).getAttribute('href')).toBe(
      '/c/docker-desktop/pods?namespace=kube-system',
    );
    expect(crumbs.getByRole('link', { name: 'default' }).getAttribute('href')).toBe(
      '/c/docker-desktop/pods?namespace=default',
    );
  });

  it('carries no remembered namespace on a cluster-scoped kind', async () => {
    rememberNamespaces('docker-desktop', 'pods', ['kube-system']);
    stub({
      '/api/c/docker-desktop/nav': { body: nav },
      '/api/c/docker-desktop/r/nodes/worker-1': {
        body: { state: 'ok', data: { kind: 'nodes', name: 'worker-1', title: 'Node', tabs: [], sections: [] } },
      },
    });
    mountApp('/c/docker-desktop/nodes/worker-1');

    const crumbs = within(screen.getByRole('navigation', { name: 'Breadcrumb' }));
    expect((await crumbs.findByRole('link', { name: 'Node' })).getAttribute('href')).toBe('/c/docker-desktop/nodes');
  });

  it('takes a Helm release’s “Helm releases” back to the list as it was filtered', async () => {
    // The same address as the page's own Back, for a page loaded directly.
    stub({ '/api/c/docker-desktop/nav': { body: nav } });
    mountApp('/c/docker-desktop/helm/monitoring/grafana?namespace=monitoring');

    const crumbs = within(screen.getByRole('navigation', { name: 'Breadcrumb' }));
    expect((await crumbs.findByRole('link', { name: 'Helm releases' })).getAttribute('href')).toBe(
      '/c/docker-desktop/helm?namespace=monitoring',
    );
    expect(screen.getByRole('link', { name: 'Back' }).getAttribute('href')).toBe(
      '/c/docker-desktop/helm?namespace=monitoring',
    );
  });

  it('links a cluster-scoped object’s kind and draws no namespace segment', async () => {
    stub({
      '/api/c/docker-desktop/nav': { body: nav },
      '/api/c/docker-desktop/r/nodes/worker-1': {
        body: { state: 'ok', data: { kind: 'nodes', name: 'worker-1', title: 'Node', tabs: [], sections: [] } },
      },
    });
    mountApp('/c/docker-desktop/nodes/worker-1');

    const crumbs = within(screen.getByRole('navigation', { name: 'Breadcrumb' }));
    expect((await crumbs.findByRole('link', { name: 'Node' })).getAttribute('href')).toBe('/c/docker-desktop/nodes');
    expect(crumbs.getByText('worker-1').closest('a')).toBeNull();
    // KubeTower, the cluster and the kind; nothing for a namespace it has not.
    expect(crumbs.getAllByRole('link')).toHaveLength(3);
  });

  it('links a Helm release’s list and its namespace, and not the release', async () => {
    stub({ '/api/c/docker-desktop/nav': { body: nav } });
    mountApp('/c/docker-desktop/helm/monitoring/grafana');

    const crumbs = within(screen.getByRole('navigation', { name: 'Breadcrumb' }));
    expect((await crumbs.findByRole('link', { name: 'Helm releases' })).getAttribute('href')).toBe(
      '/c/docker-desktop/helm',
    );
    expect(crumbs.getByRole('link', { name: 'monitoring' }).getAttribute('href')).toBe(
      '/c/docker-desktop/helm?namespace=monitoring',
    );
    expect(crumbs.getByText('grafana').closest('a')).toBeNull();
  });

  it('draws a page that declares only a title as one segment, not a link', async () => {
    onPodList();
    mountApp('/c/prod/pods');

    const crumbs = within(screen.getByRole('navigation', { name: 'Breadcrumb' }));
    expect((await crumbs.findByText('Pods')).closest('a')).toBeNull();
  });

  it('draws only its last part on a phone, every earlier part and every separator below `sm`', async () => {
    // At 390px five parts cut to one letter each read `K / k / S / c / a`. The
    // layout suite reads the last part whole there (`measure/narrow.spec.ts`);
    // this pins which items give way and which one stays.
    stub({
      '/api/c/docker-desktop/nav': { body: nav },
      '/api/c/docker-desktop/r/pods/default/api-0': {
        body: {
          state: 'ok',
          data: { kind: 'pods', name: 'api-0', namespace: 'default', title: 'Pod', tabs: [], sections: [] },
        },
      },
    });
    mountApp('/c/docker-desktop/pods/default/api-0');

    const crumbs = within(screen.getByRole('navigation', { name: 'Breadcrumb' }));
    await crumbs.findByText('api-0');
    const items = crumbs.getAllByRole('listitem', { hidden: true });
    const last = items.at(-1);
    expect(last?.textContent).toBe('api-0');
    expect(last?.className).not.toContain('max-sm:hidden');
    for (const item of items.slice(0, -1)) expect(item.className, item.textContent ?? '').toContain('max-sm:hidden');
  });

  it('says only KubeTower where no cluster is on screen', async () => {
    stub();
    mountApp('/settings/general');

    await waitFor(() => expect(topbar().textContent).toContain('KubeTower'));
    expect(topbar().textContent).not.toContain('Production');
  });
});

/*
  No "refreshing" dot right of the breadcrumb. A list still refreshes on the
  server's interval; this pins only that nothing in the header says so.
*/
describe('the refresh dot', () => {
  it('is not drawn, on a list that refreshes itself', async () => {
    onPodList();
    mountApp('/c/prod/pods');

    // The page has spoken - its title is in the breadcrumb - so a dot drawn
    // from its facts would be there by now.
    await waitFor(() => expect(topbar().textContent).toContain('Pods'));
    expect(screen.queryByTestId('refreshing')).toBeNull();
    expect(topbar().querySelector('[title*="refreshes every"]')).toBeNull();
    expect(topbar().querySelector('.bg-ok')).toBeNull();
  });
});

describe('the controls', () => {
  it('offers Settings and no Cluster link, from every page, including one with no cluster', async () => {
    // No Cluster link beside Settings: the crumb to the console's home is the
    // way to every cluster. Checked once the capabilities and the list have
    // been read: a link would be withheld until then, so its absence before that
    // would prove nothing.
    stub();
    const stubbed = globalThis.fetch;
    const asked: string[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      asked.push(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      return stubbed(input, init);
    });
    mountApp('/settings/general');

    await waitFor(() => expect(topbar().textContent).toContain('KubeTower'));
    await waitFor(() => {
      expect(asked).toContain('/api/capabilities');
      expect(asked).toContain('/api/clusters');
    });
    // And their answers drawn: the crumb and Settings stand, nothing else.
    await waitFor(() => expect(topbar().querySelector('a[href="/settings"]')).not.toBeNull());
    expect(topbar().querySelector('a[href="/settings"]')).not.toBeNull();
    expect(topbar().querySelector('a[href="/health"]')).toBeNull();
    expect(within(topbar()).queryByRole('link', { name: 'Cluster' })).toBeNull();
    expect(screen.getByRole('link', { name: 'KubeTower' }).getAttribute('href')).toBe('/');
  });

  it('offers no Cluster link in a cluster, and its crumb goes to the cluster rather than to /', async () => {
    // The in-cluster console's crumb leads to its one cluster.
    stub({
      '/api/capabilities': { body: { ...capabilities, mode: 'shared', localShell: false } },
      '/api/clusters': { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', initials: 'PR', state: 'ok' }] } },
      '/api/c/prod/nav': { body: nav },
    });
    mountApp('/c/prod');

    const crumb = await screen.findByRole('link', { name: 'KubeTower' });
    await waitFor(() => expect(crumb.getAttribute('href')).toBe('/c/prod'));
    expect(topbar().querySelector('a[href="/health"]')).toBeNull();
    // Settings is still offered: under the name (below). This
    // stub has no methods route, and a methods read that failed draws the way
    // out - and so the menu Settings is in.
    await waitFor(() => expect(topbar().querySelector('button[aria-haspopup="menu"]')).not.toBeNull());
  });

  it('keeps the crumb to / on a shared console that lists several', async () => {
    // The exception is the profile *and* the one cluster: with three there is a
    // choice again, and the crumb leads to the list.
    stub({ '/api/capabilities': { body: { ...capabilities, mode: 'shared', localShell: false } } });
    mountApp('/settings/general');

    await waitFor(() => expect(screen.getByRole('link', { name: 'KubeTower' }).getAttribute('href')).toBe('/'));
    expect(topbar().querySelector('a[href="/health"]')).toBeNull();
  });

  it('offers a shell on the cluster being looked at', async () => {
    onPodList();
    mountApp('/c/prod/pods');

    const button = await screen.findByRole('button', { name: 'Shell' });
    expect(button.getAttribute('title')).toBe('Open a shell on prod');
  });

  it('draws no shell control where the profile has no local shell', async () => {
    onPodList({ '/api/capabilities': { body: { ...capabilities, localShell: false } } });
    mountApp('/c/prod/pods');

    await waitFor(() => expect(topbar().textContent).toContain('Pods'));
    // Absent, not disabled: a control that is offered and then refused
    // teaches a person that the
    // marking is noise.
    expect(screen.queryByRole('button', { name: 'Shell' })).toBeNull();
  });

  it('carries no Kinds where the rail does: it is at the rail’s foot', async () => {
    // Three clusters, so the rail is drawn, and a cluster page, so Kinds is.
    onPodList();
    mountApp('/c/prod/pods');

    const kinds = await screen.findByRole('button', { name: 'Kinds' });
    expect(document.getElementById('rail')?.contains(kinds)).toBe(true);
    expect(topbar().querySelector('button[aria-controls="menu"]')).toBeNull();
    // The breadcrumb is the top bar's first thing again.
    expect(topbar().firstElementChild?.getAttribute('aria-label')).toBe('Breadcrumb');
  });
});

describe('the window title', () => {
  it('carries a named cluster', async () => {
    stub({ [`/api/c/${ARN}/nav`]: { body: nav } });
    mountApp(`/c/${encodeURIComponent(ARN)}`);

    await waitFor(() => expect(document.title).toContain('Production'));
    expect(document.title).toContain('KubeTower');
  });

  it('leaves an unnamed cluster out of it', async () => {
    stub({ '/api/c/docker-desktop/nav': { body: nav } });
    mountApp('/c/docker-desktop');

    await screen.findByRole('link', { name: 'docker-desktop' });
    // A raw context name is neither short enough for a tab nor the thing anybody
    // scans a taskbar for. Less information, more of it legible.
    // Read once the overview has named itself, so it is no race with that.
    await waitFor(() => expect(document.title).toBe('KubeTower · Overview'));
  });
});

/*
  Where the header draws who is signed in, the name
  is the button and Settings and the way out are behind it; Arrange and the
  session warning stay drawn. Where it draws no name - the loopback desktop - Settings
  stays a plain link and there is no menu.

  Each item carries its glyph before its label, and the way out is called Log out
  wherever it is drawn.
*/
describe('the account menu under the signed-in name', () => {
  const one = { state: 'ok', items: [{ name: 'prod', label: 'prod', initials: 'PR', state: 'ok' }] };
  const server = {
    '/api/session': {
      body: { identity: 'alice@example.com', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' },
    },
    '/api/capabilities': { body: { ...capabilities, mode: 'shared', localShell: false, preferences: false } },
    '/api/clusters': { body: one },
    '/api/auth/methods': { body: { password: false, sso: { state: 'ready' } } },
    '/api/c/prod/nav': { body: nav },
  } satisfies StubRoutes;

  /** The one button in the header that opens a menu, once it is there. */
  async function account(): Promise<HTMLElement> {
    await waitFor(() => expect(topbar().querySelectorAll('button[aria-haspopup="menu"]')).toHaveLength(1));
    const found = topbar().querySelector<HTMLElement>('button[aria-haspopup="menu"]');
    if (found === null) throw new Error('no account button');
    return found;
  }

  it('is one button carrying the identity, and no standalone Settings or Log out remains', async () => {
    stubFetch(server);
    mountApp('/c/prod');

    const button = await screen.findByRole('button', { name: 'alice@example.com' });
    expect(await account()).toBe(button);
    expect(topbar().contains(button)).toBe(true);
    expect(button.textContent).toContain('alice@example.com');
    expect(button.getAttribute('title')).toBe('Signed in as alice@example.com');
    expect(button.getAttribute('aria-expanded')).toBe('false');
    expect(topbar().querySelector('a[href="/settings"]')).toBeNull();
    expect(screen.queryByRole('link', { name: 'Settings' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Log out' })).toBeNull();
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('opens on Settings and Log out, and Escape closes it and gives the focus back to the name', async () => {
    stubFetch(server);
    mountApp('/c/prod');
    const button = await screen.findByRole('button', { name: 'alice@example.com' });

    act(() => {
      fireEvent.click(button);
    });
    const menu = screen.getByRole('menu', { name: 'alice@example.com' });
    const items = [...menu.querySelectorAll('[role="menuitem"]')].map((item) => item.textContent);
    expect(items).toEqual(['Settings', 'Log out']);
    expect(button.getAttribute('aria-expanded')).toBe('true');
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: 'Settings' }));

    act(() => {
      fireEvent.keyDown(window, { key: 'Escape' });
    });
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(button);
  });

  it('draws a glyph before each item - a gear for Settings, a way out for Log out', async () => {
    stubFetch(server);
    mountApp('/c/prod');
    const button = await screen.findByRole('button', { name: 'alice@example.com' });
    act(() => {
      fireEvent.click(button);
    });
    const glyphs = [...screen.getByRole('menu').querySelectorAll('[role="menuitem"]')].map((item) => ({
      label: item.textContent,
      // The glyph is the item's first child, so it is drawn before the label.
      first: item.firstElementChild?.tagName.toLowerCase(),
      href: item.querySelector('svg use')?.getAttribute('href'),
    }));
    expect(glyphs).toEqual([
      { label: 'Settings', first: 'svg', href: '#i-settings' },
      { label: 'Log out', first: 'svg', href: '#i-logout' },
    ]);
    // Both shapes are in the sheet the shell mounts, or a `use` draws nothing.
    expect(document.getElementById('i-settings')?.tagName.toLowerCase()).toBe('symbol');
    expect(document.getElementById('i-logout')?.tagName.toLowerCase()).toBe('symbol');
  });

  it('says Sign out nowhere, in the text, a title or an accessible name', async () => {
    stubFetch(server);
    mountApp('/c/prod');
    const button = await screen.findByRole('button', { name: 'alice@example.com' });
    act(() => {
      fireEvent.click(button);
    });
    screen.getByRole('menuitem', { name: 'Log out' });
    // The whole document, menu open: the words, and every attribute a person
    // hears or hovers.
    expect(document.body.innerHTML).not.toMatch(/sign[ -]?out/i);
  });

  it('goes to Settings from the menu', async () => {
    stubFetch(server);
    const { router } = mountApp('/c/prod');
    const button = await screen.findByRole('button', { name: 'alice@example.com' });
    act(() => {
      fireEvent.click(button);
    });
    fireEvent.click(screen.getByRole('menuitem', { name: 'Settings' }));
    await waitFor(() => expect(router.state.location.pathname).toMatch(/^\/settings/));
  });

  it('signs out from the menu', async () => {
    const calls: string[] = [];
    stubFetch({ ...server, '/api/auth/logout': { status: 204 } });
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
      calls.push(`${init?.method ?? 'GET'} ${path}`);
      return stubbed(input, init);
    });
    const left: string[] = [];
    watchLeaving((href) => left.push(href));
    mountApp('/c/prod');
    const button = await screen.findByRole('button', { name: 'alice@example.com' });
    act(() => {
      fireEvent.click(button);
    });
    fireEvent.click(screen.getByRole('menuitem', { name: 'Log out' }));
    await waitFor(() => expect(left).toEqual(['/auth/login']));
    expect(calls).toContain('POST /api/auth/logout');
  });

  it('keeps Arrange and the session warning drawn beside it, outside the menu', async () => {
    // A console with one cluster that keeps preferences (a desktop with
    // KT_PASSWORD_HASH and one context), whose session ends in five minutes.
    stubFetch({
      ...server,
      '/api/session': {
        body: {
          identity: 'alice@example.com',
          idleExpiresAt: '2999-01-01T00:00:00Z',
          absoluteExpiresAt: new Date(Date.now() + 5 * 60_000 - 1_000).toISOString(),
        },
      },
      '/api/capabilities': { body: capabilities },
      '/api/auth/methods': { body: { password: true, sso: { state: 'off' } } },
      '/api/preferences': { body: preferences },
    });
    mountApp('/c/prod');

    const button = await screen.findByRole('button', { name: 'alice@example.com' });
    const arrange = await screen.findByRole('button', { name: 'Arrange' });
    const warning = await screen.findByText('Session ends in 5 min');
    expect(warning.getAttribute('role')).toBe('status');
    for (const each of [arrange, warning]) {
      expect(topbar().contains(each)).toBe(true);
      expect(button.contains(each)).toBe(false);
      // Left of the name, which is the header's right end.
      expect(each.compareDocumentPosition(button) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    }
    expect(await account()).toBe(button);
    expect(topbar().querySelector('a[href="/settings"]')).toBeNull();
  });

  it('is not drawn on the loopback desktop, which keeps a plain Settings link and draws no name', async () => {
    const calls: string[] = [];
    stubFetch({
      ...server,
      '/api/capabilities': { body: capabilities },
      '/api/auth/methods': { body: { password: false, sso: { state: 'off' } } },
      '/api/preferences': { body: preferences },
    });
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
      calls.push(path);
      return stubbed(input, init);
    });
    mountApp('/c/prod');

    // The header has answered for the one cluster - Arrange is its - and the
    // methods have been read, so an absent menu is the rule and not a wait.
    await screen.findByRole('button', { name: 'Arrange' });
    await waitFor(() => expect(calls).toContain('/api/auth/methods'));
    await act(() => new Promise((resolve) => setTimeout(resolve, 50)));
    expect(topbar().querySelector('a[href^="/settings"]')?.textContent).toBe('Settings');
    expect(topbar().querySelector('button[aria-haspopup="menu"]')).toBeNull();
    expect(screen.queryByRole('menu')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Log out' })).toBeNull();
    expect(topbar().textContent).not.toContain('alice');
  });

  it('keeps a plain Settings link where the rail carries the name', async () => {
    // Three clusters: the name and Log out are at the rail's foot, and the
    // header draws no name to put a menu under.
    stub({ '/api/c/prod/nav': { body: nav }, '/api/auth/methods': { body: { password: true, sso: { state: 'off' } } } });
    mountApp('/c/prod');
    await screen.findByRole('button', { name: 'Log out' });
    expect(document.getElementById('rail')?.contains(screen.getByRole('button', { name: 'Log out' }))).toBe(true);
    expect(topbar().querySelector('a[href="/settings?cluster=prod"]')).not.toBeNull();
    expect(topbar().querySelector('button[aria-haspopup="menu"]')).toBeNull();
  });
});
