import { fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { components } from '../api/schema';
import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp, stubFetch, type StubReply, type StubRoutes } from '../testing/harness';
import {
  chosenNamespaces,
  listedAsNamespaced,
  rememberNamespaces,
  resetNamespaceMemory,
  withNamespaces,
} from './namespaceMemory';
import { resetMenuMode, resetOpenGroups } from './menuMemory';
import { resetNavMemory } from './navMemory';
import { rememberKind, resetViewMemory } from './viewMemory';

type ListPage = components['schemas']['ListPage'];

/**
 * The namespace chosen on one list follows the person to
 * every other namespaced list of the same cluster, from the menu, the jump box
 * and the rail tile, until they choose another or "All namespaces".
 *
 * Every test drives the real application - the shell, the menu, the list - and
 * asserts on the **address** it lands on, because the address is the claim:
 * the picker reads it, the request is built from it, and a link copied from it
 * has to say the same thing.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
  resetOpenGroups();
  resetMenuMode();
  resetNavMemory();
  resetViewMemory();
  resetNamespaceMemory();
  localStorage.clear();
});

const session: StubReply = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

const clusters: StubReply = {
  body: {
    state: 'ok',
    items: [
      { name: 'prod', label: 'prod', state: 'ok' },
      { name: 'staging', label: 'staging', state: 'ok' },
    ],
  },
};

/** The menu as the server sends it: `namespaced` on the namespaced kinds'
 *  rows and on nothing else. */
function nav(cluster: string): StubReply {
  return {
    body: {
      state: 'ok',
      data: {
        groups: [
          {
            key: 'cluster',
            label: 'Cluster',
            items: [
              { text: 'Overview', href: `/c/${cluster}` },
              { text: 'Nodes', href: `/c/${cluster}/nodes` },
            ],
          },
          {
            key: 'workloads',
            label: 'Workloads',
            items: [
              { text: 'Pods', href: `/c/${cluster}/pods`, namespaced: true },
              { text: 'Jobs', href: `/c/${cluster}/jobs`, namespaced: true },
            ],
          },
          {
            key: 'config',
            label: 'Config',
            items: [
              { text: 'Config maps', href: `/c/${cluster}/configmaps`, namespaced: true },
              // A remembered sort the server appended: the namespace joins it
              // rather than replacing it.
              { text: 'Secrets', href: `/c/${cluster}/secrets?sort=age`, namespaced: true },
            ],
          },
        ],
      },
    },
  };
}

function list(kind: string, namespaced: boolean, names: readonly string[] = []): StubReply {
  const page: ListPage = {
    kind,
    label: kind,
    namespaced,
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
    ],
    rows: names.map((name) => ({ id: name, name, cells: { name: { text: name } } })),
  };
  return { body: { state: 'ok', data: page } };
}

/** A node's detail, which carries the *Pods* link the node page builds. */
const node: components['schemas']['DetailPage'] = {
  kind: 'nodes',
  name: 'worker-1',
  title: 'Node',
  tabs: [],
  sections: [
    {
      key: 'summary',
      label: 'Summary',
      state: 'ok',
      fields: [{ label: 'Pods', value: '110 allocatable of 110', prose: false, pill: false }],
    },
  ],
};

/** A dashboard whose workloads card links to the pods list, bare - which is
 *  how the server writes it. */
function overview(cluster: string) {
  return {
    state: 'ok',
    data: {
      cluster,
      workloadsSummary: { text: '429 pods', href: `/c/${cluster}/pods` },
      counts: { state: 'ok', items: [{ kind: 'nodes', label: 'Nodes', count: 1 }] },
      capacity: { state: 'empty', items: [] },
      top: { state: 'absent', reason: { code: 'not_installed', message: 'no metrics backend' } },
      events: { state: 'empty', items: [] },
    },
  };
}

const namespaces: StubReply = {
  body: {
    state: 'ok',
    items: [{ name: 'default' }, { name: 'kube-system' }, { name: 'shop-prod' }],
  },
};

function routes(): StubRoutes {
  const out: Record<string, StubReply> = {
    '/api/session': session,
    '/api/clusters': clusters,
  };
  for (const cluster of ['prod', 'staging']) {
    out[`/api/c/${cluster}/nav`] = nav(cluster);
    out[`/api/c/${cluster}/extensions`] = { body: { state: 'empty', items: [] } };
    out[`/api/c/${cluster}/namespaces`] = namespaces;
    out[`/api/c/${cluster}/r/pods`] = list('pods', true);
    out[`/api/c/${cluster}/r/configmaps`] = list('configmaps', true);
    out[`/api/c/${cluster}/r/secrets`] = list('secrets', true);
    out[`/api/c/${cluster}/r/nodes`] = list('nodes', false, ['worker-1']);
    // Namespaced in the menu, and refused when it is read.
    out[`/api/c/${cluster}/r/jobs`] = {
      body: {
        state: 'forbidden',
        reason: { code: 'verb_denied', message: 'jobs is forbidden for this account.', retryable: false },
      },
    };
    out[`/api/c/${cluster}/r/nodes/worker-1`] = { body: { state: 'ok', data: node } };
    out[`/api/c/${cluster}/overview`] = { body: overview(cluster) };
  }
  return out;
}

function menu(): HTMLElement {
  const element = document.getElementById('menu');
  if (element === null) throw new Error('#menu is missing');
  return element;
}

/** The menu row for a kind, by the slug `data-kind` carries. */
function row(kind: string): HTMLAnchorElement {
  const found = menu().querySelector<HTMLAnchorElement>(`a[data-kind="${kind}"]`);
  if (found === null) throw new Error(`no menu row for ${kind}`);
  return found;
}

function tile(cluster: string): HTMLElement {
  const found = document.querySelector<HTMLElement>(`#rail [data-cluster="${cluster}"]`);
  if (found === null) throw new Error(`no tile for ${cluster}`);
  return found;
}

/** The application at an address, once its menu is drawn and the list on
 *  screen has said whether its kind is namespaced - which is when the list
 *  writes its namespace down. */
async function mounted(at: string) {
  stubFetch(routes());
  const app = mountApp(at);
  await waitFor(() => {
    expect(menu().querySelector('a[data-kind="configmaps"]')).not.toBeNull();
  });
  return app;
}

/** What the namespace picker on the list says, or undefined where the list
 *  draws none - which is how a cluster-scoped kind looks. */
function picker(): string | undefined {
  // Found by what only it says on its `title`: "Every namespace", or the
  // names it shows - other buttons on the page carry a title too.
  const button = [...document.querySelectorAll<HTMLButtonElement>('button[aria-expanded][title]')].find(
    (one) => one.title === 'Every namespace' || one.title === one.textContent,
  );
  return button?.textContent ?? undefined;
}

/** Opens the list's namespace picker and chooses "All namespaces" in it. */
function chooseAllNamespaces(): void {
  const button = [...document.querySelectorAll<HTMLButtonElement>('button[aria-expanded][title]')].find(
    (one) => one.title === 'Every namespace' || one.title === one.textContent,
  );
  if (button === undefined) throw new Error('no namespace picker');
  fireEvent.click(button);
  const panel = document.getElementById(button.getAttribute('aria-controls') ?? '');
  // The entry's own name is its last span; the tick shares the button.
  const all = [...(panel?.querySelectorAll('button') ?? [])].find((one) => {
    const spans = one.querySelectorAll('span');
    return spans[spans.length - 1]?.textContent?.trim() === 'All namespaces';
  });
  if (all === undefined) throw new Error('no "All namespaces" entry');
  fireEvent.click(all);
}

/** Where the router is: the path and its query, as the address bar shows it. */
function address(app: Awaited<ReturnType<typeof mounted>>): string {
  const { pathname, search } = app.router.state.location;
  return `${pathname}${search}`;
}

describe('the menu carries the namespace to the next namespaced list', () => {
  it('opens Config maps at the namespace Pods was filtered to', async () => {
    const app = await mounted('/c/prod/pods?namespace=default');
    await waitFor(() => {
      expect(row('configmaps').getAttribute('href')).toBe('/c/prod/configmaps?namespace=default');
    });

    fireEvent.click(row('configmaps'));

    await waitFor(() => {
      expect(address(app)).toBe('/c/prod/configmaps?namespace=default');
    });
    // The picker reads the same address, so it says `default` and not "All
    // namespaces".
    await waitFor(() => {
      expect(picker()).toBe('default');
    });
  });

  it('keeps the rest of a row’s query - the remembered sort - and adds the namespace to it', async () => {
    await mounted('/c/prod/pods?namespace=default');
    await waitFor(() => {
      expect(row('secrets').getAttribute('href')).toBe('/c/prod/secrets?sort=age&namespace=default');
    });
  });

  it('does the same from the jump box, whose Enter follows the same row', async () => {
    const app = await mounted('/c/prod/pods?namespace=default');
    await waitFor(() => {
      expect(row('configmaps').getAttribute('href')).toContain('namespace=default');
    });

    const box = menu().querySelector<HTMLInputElement>('#menu-jump');
    if (box === null) throw new Error('no jump box');
    fireEvent.change(box, { target: { value: 'config' } });
    await waitFor(() => {
      expect([...menu().querySelectorAll('[data-kind]')].map((one) => one.textContent)).toEqual([
        'Config maps',
      ]);
    });
    fireEvent.keyDown(box, { key: 'Enter' });

    await waitFor(() => {
      expect(address(app)).toBe('/c/prod/configmaps?namespace=default');
    });
  });

  it('carries a selection of several namespaces whole', async () => {
    const app = await mounted('/c/prod/pods?namespace=kube-system&namespace=default');
    await waitFor(() => {
      expect(row('configmaps').getAttribute('href')).toBe(
        '/c/prod/configmaps?namespace=default&namespace=kube-system',
      );
    });

    fireEvent.click(row('configmaps'));

    await waitFor(() => {
      expect(app.router.state.location.pathname).toBe('/c/prod/configmaps');
    });
    expect(new URLSearchParams(app.router.state.location.search).getAll('namespace')).toEqual([
      'default',
      'kube-system',
    ]);
  });

  it('forgets it when "All namespaces" is chosen on the list', async () => {
    const app = await mounted('/c/prod/pods?namespace=default');
    await waitFor(() => {
      expect(row('configmaps').getAttribute('href')).toContain('namespace=default');
    });
    await waitFor(() => {
      expect(picker()).toBe('default');
    });

    chooseAllNamespaces();

    await waitFor(() => {
      expect(address(app)).toBe('/c/prod/pods');
    });
    await waitFor(() => {
      expect(row('configmaps').getAttribute('href')).toBe('/c/prod/configmaps');
    });
    expect(chosenNamespaces('prod')).toEqual([]);
  });

  /*
    A list link another page builds with no namespace opens as it says -
    every namespace - and leaves the choice
    alone. Clicked for real, on the pages that build them.
  */
  it('keeps it across the node page’s Pods link, which names no namespace', async () => {
    const app = await mounted('/c/prod/pods?namespace=default');
    await waitFor(() => {
      expect(row('configmaps').getAttribute('href')).toContain('namespace=default');
    });

    await app.router.navigate('/c/prod/nodes/worker-1');
    const link = await waitFor(() => {
      const found = document.querySelector<HTMLAnchorElement>('[data-section="node-pods"] a');
      if (found === null) throw new Error('no Pods link yet');
      return found;
    });
    fireEvent.click(link);

    // Followed as written: the node's pods, in every namespace.
    await waitFor(() => {
      expect(address(app)).toBe('/c/prod/pods?filter=Node%3Aworker-1');
    });
    await waitFor(() => {
      expect(picker()).toBe('All namespaces');
    });
    // And the choice is where it was: the menu still carries it.
    expect(chosenNamespaces('prod')).toEqual(['default']);
    expect(row('configmaps').getAttribute('href')).toBe('/c/prod/configmaps?namespace=default');
  });

  it('keeps it across an Overview card, which names no namespace', async () => {
    const app = await mounted('/c/prod/pods?namespace=default');
    await waitFor(() => {
      expect(row('configmaps').getAttribute('href')).toContain('namespace=default');
    });

    await app.router.navigate('/c/prod');
    const card = await waitFor(() => {
      const found = [...document.querySelectorAll<HTMLAnchorElement>('#page a')].find(
        (one) => one.textContent === '429 pods',
      );
      if (found === undefined) throw new Error('no workloads card link yet');
      return found;
    });
    fireEvent.click(card);

    await waitFor(() => {
      expect(address(app)).toBe('/c/prod/pods');
    });
    await waitFor(() => {
      expect(picker()).toBe('All namespaces');
    });
    expect(chosenNamespaces('prod')).toEqual(['default']);
    expect(row('configmaps').getAttribute('href')).toBe('/c/prod/configmaps?namespace=default');
  });

  it('remembers an explicit namespace on a kind whose read was refused', async () => {
    // The list cannot say it is namespaced - it was refused - but the menu
    // already has: the address asked for `shop-prod` on a kind that has them.
    const app = await mounted('/c/prod/pods?namespace=default');
    await app.router.navigate('/c/prod/jobs?namespace=shop-prod');
    await waitFor(() => {
      expect(document.body.textContent).toContain('jobs is forbidden for this account.');
    });
    await waitFor(() => {
      expect(chosenNamespaces('prod')).toEqual(['shop-prod']);
    });
    expect(row('configmaps').getAttribute('href')).toBe('/c/prod/configmaps?namespace=shop-prod');
  });

  it('neither applies it to a cluster-scoped kind nor clears it there', async () => {
    const app = await mounted('/c/prod/pods?namespace=default');
    await waitFor(() => {
      expect(row('configmaps').getAttribute('href')).toContain('namespace=default');
    });
    // Nodes is not marked namespaced, so its row is never decorated.
    expect(row('nodes').getAttribute('href')).toBe('/c/prod/nodes');

    fireEvent.click(row('nodes'));
    await waitFor(() => {
      expect(address(app)).toBe('/c/prod/nodes');
    });
    // Arrived, and the list has answered that it is cluster-scoped - it draws
    // no picker - and nothing was cleared.
    await waitFor(() => {
      expect(document.body.textContent).toContain('worker-1');
    });
    expect(picker()).toBeUndefined();
    expect(chosenNamespaces('prod')).toEqual(['default']);
    expect(row('configmaps').getAttribute('href')).toBe('/c/prod/configmaps?namespace=default');

    // Nor does a namespace written into a cluster-scoped kind's address by
    // hand become the choice: Nodes has no namespace to choose.
    await app.router.navigate('/c/prod/nodes?namespace=kube-system');
    // The page has drawn that address and its effects have run: a list that
    // wrote here would have written by now. The rows on screen are the
    // previous visit's, so they cannot be what is waited for.
    await new Promise((resolve) => setTimeout(resolve, 100));
    expect(address(app)).toBe('/c/prod/nodes?namespace=kube-system');
    expect(chosenNamespaces('prod')).toEqual(['default']);

    // And back on a namespaced kind, it is there again.
    fireEvent.click(row('configmaps'));
    await waitFor(() => {
      expect(address(app)).toBe('/c/prod/configmaps?namespace=default');
    });
  });

  it('follows an explicit address as written, and remembers it from then on', async () => {
    const app = await mounted('/c/prod/pods?namespace=default');
    await waitFor(() => {
      expect(row('configmaps').getAttribute('href')).toContain('namespace=default');
    });

    // A pasted link naming another namespace wins over the one carried...
    await app.router.navigate('/c/prod/secrets?namespace=shop-prod');
    expect(address(app)).toBe('/c/prod/secrets?namespace=shop-prod');
    // ...and becomes the choice the menu carries.
    await waitFor(() => {
      expect(row('configmaps').getAttribute('href')).toBe('/c/prod/configmaps?namespace=shop-prod');
    });
  });

  it('keeps one choice per cluster, and never carries one to the other', async () => {
    const app = await mounted('/c/prod/pods?namespace=default');
    await waitFor(() => {
      expect(row('configmaps').getAttribute('href')).toContain('namespace=default');
    });

    await app.router.navigate('/c/staging/pods');
    await waitFor(() => {
      expect(row('configmaps').getAttribute('href')).toBe('/c/staging/configmaps');
    });
    await app.router.navigate('/c/staging/pods?namespace=kube-system', { replace: true });
    await waitFor(() => {
      expect(row('configmaps').getAttribute('href')).toBe(
        '/c/staging/configmaps?namespace=kube-system',
      );
    });

    expect(chosenNamespaces('prod')).toEqual(['default']);
    expect(chosenNamespaces('staging')).toEqual(['kube-system']);
    // The way back to prod is to its own namespace, and not to staging's.
    expect(tile('prod').getAttribute('href')).toBe('/c/prod/pods?namespace=default');

    fireEvent.click(tile('prod'));
    await waitFor(() => {
      expect(address(app)).toBe('/c/prod/pods?namespace=default');
    });
    await waitFor(() => {
      expect(row('configmaps').getAttribute('href')).toBe('/c/prod/configmaps?namespace=default');
    });
  });
});

describe('the rail tile', () => {
  it('carries nothing to a remembered kind not known to be namespaced', async () => {
    // Nodes was the last page on staging, and no list there has answered in
    // this session: the tile cannot know the kind's scope, so it names none.
    rememberNamespaces('staging', 'pods', ['default']);
    rememberKind('staging', 'nodes');
    await mounted('/c/prod/pods');

    expect(listedAsNamespaced('staging', 'nodes')).toBe(false);
    expect(tile('staging').getAttribute('href')).toBe('/c/staging/nodes');
  });
});

describe('withNamespaces', () => {
  it('leaves a link that already names a namespace as it was written', () => {
    expect(withNamespaces('/c/prod/pods?namespace=shop', ['default'])).toBe('/c/prod/pods?namespace=shop');
  });

  it('leaves a link alone when nothing is carried', () => {
    expect(withNamespaces('/c/prod/pods?sort=age', [])).toBe('/c/prod/pods?sort=age');
  });

  it('keeps the query and the fragment it found', () => {
    expect(withNamespaces('/c/prod/pods?open=shop%2Fweb#top', ['a', 'b'])).toBe(
      '/c/prod/pods?open=shop%2Fweb&namespace=a&namespace=b#top',
    );
  });
});

describe('where the browser will not keep it', () => {
  it('still carries it for the life of the page', () => {
    // A private window or blocked site data: every storage call throws. The
    // choice is then the module's alone, and nothing is reported.
    vi.stubGlobal('sessionStorage', {
      getItem: () => {
        throw new Error('blocked');
      },
      setItem: () => {
        throw new Error('blocked');
      },
      removeItem: () => {
        throw new Error('blocked');
      },
    });
    resetNamespaceMemory();

    rememberNamespaces('prod', 'pods', ['default']);

    expect(chosenNamespaces('prod')).toEqual(['default']);
    expect(listedAsNamespaced('prod', 'pods')).toBe(true);
  });
});
