import { cleanup, waitFor } from '@testing-library/react';
import { createMemoryRouter, matchRoutes, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { DetailPage } from '../api';
import { resetKeyboardStack } from '../keyboard/stack';
import { ObjectDetail } from '../pages/object/ObjectDetail';
import { resetMenuMode, resetOpenGroups } from '../shell/menuMemory';
import { resetNavMemory } from '../shell/navMemory';
import { rememberKind, resetViewMemory } from '../shell/viewMemory';
import { mountApp, renderWithClient, stubFetch, type StubReply } from '../testing/harness';
import {
  canonicalClusterAddress,
  clusterAddressMayMove,
  clusterPath,
  clusterSegment,
} from './clusterPath';
// `?raw` and a parse rather than a JSON import: `vite/client` already types
// `?raw`, and a JSON import would need `resolveJsonModule` turned on for the
// whole tree to type-check one test.
import table from './clusterPath.cases.json?raw';

/**
 * A cluster is spelt one way in a console address.
 *
 * `clusterPath.cases.json` is the contract. This file holds the front end to
 * it, and `internal/web/clusterpath_test.go` holds the server to the same file,
 * so the two halves cannot drift apart while each still passes on its own.
 */
interface Case {
  readonly name: string;
  readonly path: string;
}

const cases = JSON.parse(table) as readonly Case[];

function pathOf(name: string): string {
  const found = cases.find((one) => one.name === name);
  if (found === undefined) throw new Error(`clusterPath.cases.json has no case for ${name}`);
  return found.path;
}

describe('clusterPath is the server spelling, byte for byte', () => {
  it('answers every case in the shared table', () => {
    // An empty table would make the loop below pass and say nothing.
    expect(cases.length).toBeGreaterThan(0);
    for (const { name, path } of cases) {
      expect(clusterPath(name), name).toBe(path);
      expect(`/c/${clusterSegment(name)}`, name).toBe(path);
    }
  });

  it('round-trips through the decoding the router applies to :cluster', () => {
    for (const { name } of cases) {
      expect(decodeURIComponent(clusterSegment(name)), name).toBe(name);
    }
  });
});

/**
 * The four links the issue names, for a context carrying `@`, `:` and `/` at
 * once: the rail tile, the page the rail remembers, a node's *Pods* link and
 * the menu's Pods row. Before #94 the first three wrote
 * `/c/team%40arn%3Aaws%3Aeks%3A...%2Fprod` and the menu wrote
 * `/c/team@arn:aws:eks:...:cluster--prod`.
 *
 * The menu's href is the server's: this file draws what the server sends, and
 * `TestTheMenuSpellsAClusterAsTheRailDoes` is where the server is shown to send
 * exactly this address for this context. Here the menu is fed that address and
 * asked the second half of the issue - whether it marks its row current on each
 * address the other three links produce.
 */
const NAME = 'team@arn:aws:eks:eu-west-1:1:cluster/prod';
const HOME = pathOf(NAME);
const PODS = `${HOME}/pods`;
const API = `/api/c/${encodeURIComponent(NAME)}`;

const session = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

const stubs = {
  '/api/session': session,
  '/api/clusters': {
    body: {
      state: 'ok',
      items: [
        { name: NAME, label: 'team prod', state: 'ok', order: 0 },
        { name: 'staging', label: 'staging', state: 'ok', order: 1 },
      ],
    },
  },
  [`${API}/extensions`]: { body: { state: 'empty', items: [] } },
  [`${API}/nav`]: {
    body: {
      state: 'ok',
      data: {
        groups: [
          { key: 'cluster', label: 'Cluster', items: [{ text: 'Overview', href: HOME }] },
          {
            key: 'workloads',
            label: 'Workloads',
            items: [
              { text: 'Pods', href: PODS },
              { text: 'Deployments', href: `${HOME}/deployments` },
            ],
          },
        ],
      },
    },
  },
  '/api/c/staging/extensions': { body: { state: 'empty', items: [] } },
  '/api/c/staging/nav': { body: { state: 'ok', data: { groups: [] } } },
} satisfies Record<string, StubReply>;

function forgetEverything(): void {
  resetKeyboardStack();
  resetOpenGroups();
  resetMenuMode();
  resetNavMemory();
  resetViewMemory();
  localStorage.clear();
}

beforeEach(forgetEverything);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  forgetEverything();
});

function tile(name: string): HTMLElement {
  const found = document.querySelector<HTMLElement>(`#rail [data-cluster="${CSS.escape(name)}"]`);
  if (found === null) throw new Error(`no rail tile for ${name}`);
  return found;
}

function menuRow(text: string): HTMLElement {
  const menu = document.getElementById('menu');
  for (const link of menu?.querySelectorAll('a') ?? []) {
    if (link.textContent === text) return link;
  }
  throw new Error(`no menu row says ${text}`);
}

/** The node's *Pods* link, as `ObjectDetail` draws it for a node of NAME. */
async function nodePodsLink(): Promise<string> {
  const node: DetailPage = {
    kind: 'nodes',
    name: 'ip-10-0-1-7',
    title: 'Node',
    tabs: [],
    sections: [{ key: 'summary', label: 'Summary', state: 'ok', fields: [] }],
  };
  stubFetch({ [`${API}/r/nodes/ip-10-0-1-7`]: { body: { state: 'ok', data: node } } });
  const router = createMemoryRouter(
    [
      {
        path: '/c/:cluster/:kind/:name',
        element: (
          <ObjectDetail
            identity={{ cluster: NAME, kind: 'nodes', name: 'ip-10-0-1-7' }}
            chrome="page"
          />
        ),
      },
    ],
    { initialEntries: [`${HOME}/nodes/ip-10-0-1-7`] },
  );
  const view = renderWithClient(<RouterProvider router={router} />);
  const link = await waitFor(() => {
    const found = view.container.querySelector('[data-section="node-pods"] a');
    if (found === null) throw new Error('no node Pods link yet');
    return found;
  });
  const href = link.getAttribute('href') ?? '';
  cleanup();
  vi.unstubAllGlobals();
  return href;
}

/** The rail tile of NAME seen from the other cluster: not the active tile, so it
 *  links to what it remembers - the page the operator left there. */
async function railRememberedLink(): Promise<string> {
  rememberKind(NAME, 'pods');
  stubFetch(stubs);
  mountApp('/c/staging');
  const href = await waitFor(() => {
    const found = tile(NAME).getAttribute('href');
    if (found === null || found === '' || !found.endsWith('/pods')) {
      throw new Error(`the tile links to ${String(found)} so far`);
    }
    return found;
  });
  cleanup();
  vi.unstubAllGlobals();
  resetViewMemory();
  return href;
}

/** The rail tile of NAME seen from NAME itself: the active tile, which links to
 *  the cluster's own page. Arrived on the node link's address, so the shell is
 *  mounted on one producer's output and asked for another's. */
async function railHomeLink(arrivedOn: string): Promise<string> {
  stubFetch(stubs);
  mountApp(arrivedOn);
  const href = await waitFor(() => {
    const found = tile(NAME).getAttribute('href');
    if (found === null || found.endsWith('/pods')) throw new Error('the tile has not settled');
    return found;
  });
  cleanup();
  vi.unstubAllGlobals();
  return href;
}

describe('every link to a cluster spells it the same way', () => {
  it('the rail tile, its remembered page, the node Pods link and the menu all write one address', async () => {
    const node = await nodePodsLink();
    const remembered = await railRememberedLink();
    const home = await railHomeLink(node);

    // The menu, drawn from the server's answer for this context.
    stubFetch(stubs);
    mountApp(PODS);
    const menu = await waitFor(() => menuRow('Pods').getAttribute('href'));

    expect(home).toBe(HOME);
    expect(new Set([remembered, node.split('?')[0], menu])).toEqual(new Set([PODS]));
    // And the spelling is the readable one: `@` and `:` as they are, `/` escaped
    // so an ARN stays one segment, and no Headlamp `--`.
    expect(PODS).toBe('/c/team@arn:aws:eks:eu-west-1:1:cluster%2Fprod/pods');
  });

  // Arrived at on the address each link actually wrote - not on a copy of the
  // table - as a click, a reload or a bookmark would. The menu's rows are the
  // server's hrefs; `NavLink` compares them with the address as strings, which
  // is why a second spelling marked no row at all.
  it.each([
    ['the rail remembered page', railRememberedLink, 'Pods'],
    ['the node Pods link', nodePodsLink, 'Pods'],
    ['the rail tile', () => nodePodsLink().then(railHomeLink), 'Overview'],
  ])('marks the menu row current on %s', async (_label, produce, current) => {
    const address = await produce();
    stubFetch(stubs);
    mountApp(address);
    await waitFor(() => {
      expect(menuRow(current).getAttribute('aria-current')).toBe('page');
    });
    // And only that row: a prefix match would mark every row under the cluster.
    const marked = document.querySelectorAll('#menu a[aria-current="page"]');
    expect(marked).toHaveLength(1);
  });
});

/**
 * An address saved before #94 carries an old spelling: the front end's
 * `encodeURIComponent` one (`%40`, `%3A`), or the server's Headlamp one (`--`).
 * Both still resolve on the server, but `NavLink` compares strings, so on such
 * an address nothing in the rail or the menu was marked current.
 * `CanonicalCluster` replaces the address with the canonical one.
 */
const OLD_FRONT_END = `/c/${encodeURIComponent(NAME)}`;
const OLD_SERVER = '/c/team@arn:aws:eks:eu-west-1:1:cluster--prod';

describe('canonicalClusterAddress', () => {
  const names = [NAME, 'staging'];

  it('moves both old spellings to the canonical one, keeping the rest of the path', () => {
    expect(canonicalClusterAddress(`${OLD_FRONT_END}/pods/ns/x`, NAME, names)).toBe(`${HOME}/pods/ns/x`);
    expect(canonicalClusterAddress(`${OLD_SERVER}/pods`, 'team@arn:aws:eks:eu-west-1:1:cluster--prod', names)).toBe(
      PODS,
    );
    expect(canonicalClusterAddress(OLD_FRONT_END, NAME, names)).toBe(HOME);
  });

  it('proposes nothing for an address that is canonical, so it cannot loop', () => {
    for (const { name, path } of cases) {
      expect(canonicalClusterAddress(`${path}/pods`, name, [name]), name).toBeUndefined();
    }
  });

  it('proposes nothing for a cluster it cannot resolve', () => {
    expect(canonicalClusterAddress('/c/nowhere%40x/pods', 'nowhere@x', names)).toBeUndefined();
    // The list has not answered: nothing is guessed.
    expect(canonicalClusterAddress(`${OLD_FRONT_END}/pods`, NAME, [])).toBeUndefined();
  });

  it('resolves a name holding a literal %2F to nothing, as the router hands it back', () => {
    // The router turns every `%2F` in a param into `/`, so the context `a%2Fb`
    // (address `/c/a%252Fb`) arrives as `a/b`, which is no listed context.
    expect(canonicalClusterAddress('/c/a%252Fb/pods', 'a/b', ['a%2Fb'])).toBeUndefined();
  });

  it('defensive only: the read-back and dot guards, fed inputs the router never produces', () => {
    // No real `:cluster` holds a literal `%2F`, and a browser resolves `.` and
    // `..` before the router sees them, so neither call below can happen through
    // the router. They pin the guards that would stop a loop if a future router
    // decoded differently. Loop-freedom itself rests on `segment === raw`,
    // which the test over the whole table above holds.
    expect(canonicalClusterAddress('/c/a%25%32Fb/pods', 'a%2Fb', ['a%2Fb'])).toBeUndefined();
    expect(canonicalClusterAddress('/c/%2E%2E/pods', '..', ['..'])).toBeUndefined();
    expect(canonicalClusterAddress('/c/%2E/pods', '.', ['.'])).toBeUndefined();
  });

  it('takes the first listed context on a Headlamp collision, as Manager.Resolve does', () => {
    // `a-/b` and `a/-b` are two contexts, and both are `a---b` in the Headlamp
    // form. `/api/clusters` lists them in `Manager.Contexts()` order, sorted
    // (`-` sorts before `/`), and `Manager.Resolve` takes the first match, so the
    // console must take the first one too.
    const sorted = ['a-/b', 'a/-b'];
    expect([...sorted].sort()).toEqual(sorted);
    expect(canonicalClusterAddress('/c/a---b/pods', 'a---b', sorted)).toBe('/c/a-%2Fb/pods');
  });

  it('reads the Headlamp space as __', () => {
    expect(canonicalClusterAddress('/c/team__prod/pods', 'team__prod', ['team prod'])).toBe(
      '/c/team%20prod/pods',
    );
  });
});

describe('clusterAddressMayMove', () => {
  it('is false on a canonical address whose name holds neither -- nor __', () => {
    for (const { name, path } of cases) {
      if (name.includes('--') || name.includes('__')) continue;
      expect(clusterAddressMayMove(`${path}/pods`, name), name).toBe(false);
    }
  });

  it('is true wherever canonicalClusterAddress could still move the address', () => {
    // The front end spelling: the segment is not the name's own.
    expect(clusterAddressMayMove(`${OLD_FRONT_END}/pods`, NAME)).toBe(true);
    // The Headlamp spellings: only the list can say which context they name.
    expect(clusterAddressMayMove(`${OLD_SERVER}/pods`, 'team@arn:aws:eks:eu-west-1:1:cluster--prod')).toBe(true);
    expect(clusterAddressMayMove('/c/team__prod/pods', 'team__prod')).toBe(true);
    // A canonical name that holds `--` waits too: it may be another context's
    // Headlamp form, and an exact match would win once the list is read.
    expect(clusterAddressMayMove('/c/cluster--prod/pods', 'cluster--prod')).toBe(true);
  });

  it('agrees with canonicalClusterAddress: no redirect is ever possible where it says no', () => {
    const names = [...cases.map((one) => one.name), 'a-/b', 'a/-b', 'team prod'];
    const addresses = [
      ...cases.map((one) => `${one.path}/pods`),
      `${OLD_FRONT_END}/pods`,
      `${OLD_SERVER}/pods`,
      '/c/a---b/pods',
      '/c/team__prod/pods',
    ];
    for (const address of addresses) {
      // The `:cluster` React Router itself hands over for this address, not a
      // hand-written copy of its decoding.
      const param = matchRoutes([{ path: '/c/:cluster/*' }], address)?.[0]?.params.cluster ?? '';
      expect(param, address).not.toBe('');
      if (!clusterAddressMayMove(address, param)) {
        expect(canonicalClusterAddress(address, param, names), address).toBeUndefined();
      }
    }
  });
});

describe('an address in an old spelling is replaced with the canonical one', () => {
  it.each([
    ['the front end spelling', `${OLD_FRONT_END}/pods?search=web#top`],
    ['the Headlamp spelling', `${OLD_SERVER}/pods?search=web#top`],
  ])('on %s, and the rail tile and menu row are then current', async (_label, address) => {
    stubFetch(stubs);
    const { router } = mountApp(address);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe(PODS);
    });
    expect(router.state.location.search).toBe('?search=web');
    expect(router.state.location.hash).toBe('#top');
    // `replace`: the old spelling is not left in the history as a page of its own.
    expect(router.state.historyAction).toBe('REPLACE');
    await waitFor(() => {
      expect(tile(NAME).getAttribute('aria-current')).toBe('page');
      expect(menuRow('Pods').getAttribute('aria-current')).toBe('page');
    });
  });
});
