import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp, stubFetch, type StubReply } from '../testing/harness';
import { resetFirstIcons, settleIcon } from './firstIcon';
import { ICON_STYLES, parseIcon, RESET_ICON } from './iconRecipe';
import { PALETTE } from './clusterState';
import { lastKind, rememberKind, rememberOpen, resetViewMemory } from './viewMemory';

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
  resetViewMemory();
  localStorage.clear();
});

function rail(): HTMLElement {
  const element = document.getElementById('rail');
  if (element === null) throw new Error('#rail is missing');
  return element;
}

const session = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

/** How a person signs in here: the shared password, as on the container
 *  profile. Every fixture answers it, so a test about the session tile is a test
 *  of a console that offers a way back in, and not of the fail-open path a
 *  missing route takes (`useSignsBackIn`), which `Shell.test.tsx` covers. */
const methods = { body: { password: true, sso: { state: 'off' } } };

/** What a screen reader hears after a tile's name: its state sentence, through
 *  `aria-describedby` (the name is the cluster's alone). */
function described(tile: HTMLElement | undefined): string | undefined {
  const id = tile?.getAttribute('aria-describedby');
  if (id === null || id === undefined) return undefined;
  return document.getElementById(id)?.textContent ?? undefined;
}

/** The hotbar breaks between tiles, by the heading each one names. */
function seams(): (string | null)[] {
  return Array.from(rail().querySelectorAll('[role="separator"]')).map((one) =>
    one.getAttribute('aria-label'),
  );
}

function tiles(): HTMLElement[] {
  const rail = document.getElementById('rail');
  return rail === null ? [] : Array.from(rail.querySelectorAll<HTMLElement>('[data-cluster]'));
}

/** What a tile says about reaching its editor, where the console keeps preferences. */
const HINT = 'Right-click, F2 or Shift+F10 to change its color and icon.';

describe('the rail keeps a tile for every cluster, whatever its state', () => {
  it('draws a cluster that could not be reached, and says so', async () => {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'prod', label: 'prod', state: 'ok' },
            {
              name: 'edge',
              label: 'edge',
              state: 'unreachable',
              reason: { code: 'cluster_unreachable', message: 'dial tcp: i/o timeout' },
            },
          ],
        },
      },
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    });

    mountApp('/');

    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });

    // Dropping the tile would be this product's founding defect at the smallest
    // possible scale: a rail short by one looks exactly like a rail that was
    // always that long.
    const edge = tiles().find((tile) => tile.dataset['cluster'] === 'edge');
    expect(edge?.dataset['state']).toBe('unreachable');
    expect(edge?.getAttribute('aria-label')).toBe('edge');
    expect(described(edge)).toBe(`could not be reached: dial tcp: i/o timeout. ${HINT}`);
  });

  it('shows the state as a corner badge and keeps the word for a screen reader', async () => {
    /*
      `stateWord()` is not printed beside the badge: truncated at rail width, a
      repeat of the color, and a line on every tile. The word is not lost - it
      is the tile's description, where a screen reader reads it. The tooltip is
      the name alone.
    */
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'prod', label: 'prod', state: 'ok' },
            {
              name: 'edge',
              label: 'edge',
              state: 'unreachable',
              reason: { code: 'cluster_unreachable', message: 'dial tcp: i/o timeout' },
            },
          ],
        },
      },
    });

    mountApp('/');

    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });

    const [prod, edge] = [
      tiles().find((tile) => tile.dataset['cluster'] === 'prod'),
      tiles().find((tile) => tile.dataset['cluster'] === 'edge'),
    ];
    // No printed word from `WORDS` on either tile: the only text a sighted
    // reader sees is the initials. The sentence is in the visually hidden
    // description, which is what a screen reader is given instead.
    const printed = (tile: HTMLElement | undefined): string =>
      Array.from(tile?.querySelectorAll('[aria-hidden="true"]') ?? [])
        .map((one) => one.textContent)
        .join('');
    expect(printed(prod)).not.toContain('reachable');
    expect(printed(edge)).not.toContain('could not be reached');
    // The badge is there, in the corner, in the tone of the state.
    expect(prod?.querySelector('[data-badge="ok"]')?.getAttribute('class')).toContain('bg-ok');
    expect(edge?.querySelector('[data-badge="unreachable"]')?.getAttribute('class')).toContain('bg-bad');
    // The sentence is the description; the tooltip is the name and nothing
    // else.
    expect(described(edge)).toBe(`could not be reached: dial tcp: i/o timeout. ${HINT}`);
    expect(prod?.getAttribute('title')).toBe('prod');
    expect(edge?.getAttribute('title')).toBe('edge');
  });

  it('says the configuration could not be read rather than drawing no tiles', async () => {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': {
        body: {
          state: 'unreadable',
          reason: { code: 'undecodable', message: 'one KUBECONFIG file could not be parsed' },
        },
      },
    });

    mountApp('/');

    // A rail with no tiles is a machine with no clusters configured. This is not
    // that, and the difference is the whole reason the envelope exists.
    const notice = await screen.findAllByText('Could not be read');
    expect(notice.length).toBeGreaterThan(0);
    expect(tiles()).toHaveLength(0);
  });

  it('renders the rail before the clusters have arrived', async () => {
    stubFetch({ '/api/session': session, '/api/auth/methods': methods });
    mountApp('/');
    // Present on the first paint, because it is a sibling of the Outlet rather
    // than something a route draws.
    expect(document.getElementById('rail')).not.toBeNull();
    await waitFor(() => {
      expect(document.getElementById('rail')).not.toBeNull();
    });
  });
});

describe('the rail is the stored arrangement', () => {
  it('hides a hidden tile and orders by the stored order', async () => {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'a', label: 'Alpha', state: 'ok', order: 2 },
            { name: 'b', label: 'Beta', state: 'ok', order: 1 },
            { name: 'c', label: 'Gamma', state: 'ok', hidden: true, order: 0 },
          ],
        },
      },
    });

    mountApp('/');

    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    expect(tiles().map((tile) => tile.dataset['cluster'])).toEqual(['b', 'a']);
  });

  /*
    `Cluster.bar` is the first sort key: without it two hotbars interleave
    into one flat rail in which a tile's neighbors
    are not the tiles it is arranged with. That is a silently wrong rail, which
    is worse than an unsorted one.
  */
  it('keeps each hotbar together and in bar order', async () => {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'b2-second', label: 'B2 second', state: 'ok', bar: 2, order: 1 },
            { name: 'b1-second', label: 'B1 second', state: 'ok', bar: 1, order: 1 },
            { name: 'b2-first', label: 'B2 first', state: 'ok', bar: 2, order: 0 },
            { name: 'b1-first', label: 'B1 first', state: 'ok', bar: 1, order: 0 },
            // On no bar, which `api/openapi.yaml` says "is what `hidden` also
            // says". It sorts after every bar rather than before bar 1.
            { name: 'loose', label: 'Loose', state: 'ok', order: 0 },
          ],
        },
      },
    });

    mountApp('/');

    await waitFor(() => {
      expect(tiles()).toHaveLength(5);
    });
    expect(tiles().map((tile) => tile.dataset['cluster'])).toEqual([
      'b1-first',
      'b1-second',
      'b2-first',
      'b2-second',
      'loose',
    ]);

    // And the seam is visible. Every bar is stacked because there is no switcher
    // yet, and a stack with no break merges two arrangements into a plausible
    // third - the failure family this product is organized around, at rail
    // scale.
    // A rule between bars, named on its `title` and to a reader: the rail is
    // 56px everywhere and a printed "Bar 2" does not fit.
    expect(seams()).toEqual(['Bar 1', 'Bar 2', 'On no bar']);
  });

  it('draws no bar heading on a rail that uses one bar', async () => {
    // "Bar 1" over every tile on a machine with one hotbar answers a question
    // nobody asked, which is how a heading stops being read.
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'a', label: 'Alpha', state: 'ok', bar: 1, order: 0 },
            { name: 'b', label: 'Beta', state: 'ok', bar: 1, order: 1 },
          ],
        },
      },
    });

    mountApp('/');

    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    expect(seams()).toEqual([]);
  });

  it('says a cluster nobody has asked yet is not a cluster that failed', async () => {
    /*
      `pending` is the ordinary state of every tile at start-up: the probe runs
      on its own loop and this list answers out of what is already known. Before
      the state existed it was reported as `unreadable` with `retryable: true` -
      "the least wrong of five" - which asserts that the thing exists and could
      not be read, when nothing was tried.

      A rail that opens amber reads as fourteen broken clusters, and that is the
      support conversation the sixth state was added to prevent.
    */
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'prod', label: 'prod', state: 'ok' },
            {
              name: 'queued',
              label: 'queued',
              state: 'pending',
              reason: { code: 'not_probed_yet', message: 'This cluster has not been probed yet.' },
            },
          ],
        },
      },
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    });

    mountApp('/');

    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });

    const queued = tiles().find((tile) => tile.dataset['cluster'] === 'queued');
    expect(queued?.dataset['state']).toBe('pending');
    expect(described(queued)).toContain('not asked yet');
    // The word it must not borrow, and the two tones it must not wear.
    expect(described(queued)).not.toContain('could not');
    expect(queued?.innerHTML).not.toContain('bg-warn');
    expect(queued?.innerHTML).not.toContain('bg-bad');
  });

  it('shows the nameplate and never puts it in the address', async () => {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': {
        body: {
          state: 'ok',
          // A second cluster, so there is a rail to draw the first one's tile.
          items: [
            { name: 'arn:aws:eks:eu-west-1:1:cluster/prod', label: 'Production', state: 'ok', order: 0 },
            { name: 'staging', label: 'staging', state: 'ok', order: 1 },
          ],
        },
      },
    });

    mountApp('/');

    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    const tile = tiles()[0];
    // Contexts are named in URLs; a nameplate that reached a path would make two
    // clusters indistinguishable to the router. Spelt by `clusterPath`, as the
    // server spells it.
    expect(tile?.getAttribute('href')).toBe('/c/arn:aws:eks:eu-west-1:1:cluster%2Fprod');
    expect(tile?.getAttribute('aria-label')).toBe('Production');
    // The tooltip is the nameplate alone, as the header's crumb shows it:
    // no state, no context name, no hint.
    expect(tile?.getAttribute('title')).toBe('Production');
  });
});

/**
 * Stacking every bar at once would be a degraded form of the product: the
 * rail shows one hotbar, and the switcher writes which.
 */
describe('the rail shows one hotbar, and the switcher chooses which', () => {
  const twoBars = {
    body: {
      state: 'ok',
      items: [
        { name: 'prod', label: 'prod', state: 'ok', bar: 1 },
        { name: 'staging', label: 'staging', state: 'ok', bar: 2 },
      ],
    },
  };

  function preferences(activeBar: number, barCount = 2): StubReply {
    return {
      body: {
        rail: { barCount, activeBar, bars: [['prod'], ['staging']], hidden: [] },
      },
    };
  }

  it('draws the clusters of the bar on screen, and not the others', async () => {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': twoBars,
      '/api/preferences': preferences(1),
    });
    mountApp('/');

    await waitFor(() => {
      expect(tiles().map((tile) => tile.dataset['cluster'])).toEqual(['prod']);
    });
    // The defect: both were drawn, stacked, with a heading between them.
    expect(tiles().map((tile) => tile.dataset['cluster'])).not.toContain('staging');
    expect(rail().querySelector('[aria-label="Hotbar"]')).not.toBeNull();
  });

  it('draws exactly the digits the server says there are', async () => {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': twoBars,
      '/api/preferences': preferences(1, 4),
    });
    mountApp('/');

    await waitFor(() => {
      expect(rail().querySelector('[aria-label="Hotbar"]')).not.toBeNull();
    });
    // `barCount` is a server constant and is read rather than counted off the
    // clusters: a client that assumed a number would be wrong in the direction
    // that hides a bar's worth of clusters.
    const digits = rail().querySelectorAll('[aria-label^="Hotbar "]');
    expect(digits).toHaveLength(4);
  });

  it('writes the chosen bar and re-reads the preferences', async () => {
    const posted: { path: string; body: unknown }[] = [];
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': twoBars,
      '/api/preferences': preferences(1),
      '/api/preferences/rail/bar': { status: 204, body: null },
    });
    // Both already given their first icon, so the bar is the only write.
    settleIcon('prod');
    settleIcon('staging');
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if (init?.method === 'POST') posted.push({ path, body: JSON.parse(String(init.body)) });
      return stubbed(input, init);
    });

    mountApp('/');
    await waitFor(() => {
      expect(rail().querySelector('[aria-label="Hotbar 2"]')).not.toBeNull();
    });
    fireEvent.click(rail().querySelector('[aria-label="Hotbar 2"]')!);

    await waitFor(() => {
      expect(posted).toEqual([{ path: '/api/preferences/rail/bar', body: { bar: 2 } }]);
    });
  });

  /*
    `barCount` is a server constant and is `> 1` on every machine, so a
    switcher conditioned on it would appear for people who never touched
    Arrange. A "barCount is 1" fixture is one the contract cannot produce -
    `RailAssignWrite` is bounded by `barCount` - so the case is every cluster
    on the bar on screen.
  */
  it('draws no switcher while every cluster is on the bar on screen', async () => {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'prod', label: 'prod', state: 'ok', bar: 1 },
            { name: 'staging', label: 'staging', state: 'ok', bar: 1 },
          ],
        },
      },
      // Two bars on offer, as on every machine. Nobody has used the second.
      '/api/preferences': preferences(1, 2),
    });
    mountApp('/');

    await waitFor(() => {
      expect(tiles().map((tile) => tile.dataset['cluster'])).toContain('staging');
    });
    expect(rail().querySelector('[aria-label="Hotbar"]')).toBeNull();
  });

  it('draws the switcher at the foot of the rail once a second bar is populated', async () => {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': twoBars,
      '/api/preferences': preferences(1),
    });
    mountApp('/');

    const switcher = await waitFor(() => {
      const found = rail().querySelector('[aria-label="Hotbar"]');
      expect(found).not.toBeNull();
      return found!;
    });
    // Below the last tile and above Arrange: a control among the controls,
    // rather than a caption over the clusters. Read top-down, digits above the
    // tiles looked like a heading.
    const lastTile = tiles().at(-1)!;
    const arrange = screen.getByRole('button', { name: 'Arrange' });
    expect(lastTile.compareDocumentPosition(switcher) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(switcher.compareDocumentPosition(arrange) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  });

  it('draws no switcher where the preferences have not answered', async () => {
    // No stub for `/api/preferences`, so the harness answers 404 and the rail
    // never learns the active bar. "Another bar than the active one" has no
    // meaning then, and the tiles stack in that window anyway.
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': twoBars,
    });
    mountApp('/');

    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    expect(rail().querySelector('[aria-label="Hotbar"]')).toBeNull();
  });
});

/**
 * A long cluster name on the rail.
 *
 * The tile prints no text, so a name of any length has no line to wrap onto.
 * What has to hold is that the whole
 * name is still reachable: the accessible name, and the tooltip.
 */
describe('a long cluster name never reaches the face of a tile', () => {
  const ARN = 'arn:aws:eks:eu-west-3:123456789012:cluster/a-very-long-production-cluster-name-for-layout';
  const longNames = {
    body: {
      state: 'ok',
      items: [
        { name: ARN, label: ARN, initials: 'A', state: 'ok' },
        { name: 'kubernetes-admin@kubernetes', label: 'kubernetes-admin@kubernetes', initials: 'K', state: 'ok' },
      ],
    },
  };

  it('draws no text on its face, and names the tile with the whole name', async () => {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': longNames,
    });
    mountApp('/');

    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    const arn = tiles().find((tile) => tile.dataset['cluster'] === ARN)!;
    // The face: an icon, and no text at all - neither a fragment of
    // the name nor the initials.
    const face = Array.from(arn.querySelectorAll('[aria-hidden="true"]'))
      .map((one) => one.textContent)
      .join('');
    expect(face).toBe('');
    // The name, whole, where a reader and a pointer reach it.
    // And the initials it shows after it, so a voice user can say what they
    // see (WCAG 2.5.3).
    expect(arn.getAttribute('aria-label')).toBe(`${ARN} (A)`);
    expect(arn.getAttribute('title')).toBe(ARN);
    expect(screen.getByRole('link', { name: `${ARN} (A)` })).toBe(arn);
  });
});

/**
 * Every tile links to the last kind opened on that cluster, which is right for
 * the switch back and would make a click on the tile already on screen a
 * no-op, with no gesture that reaches the dashboard.
 */
describe('a second click on the active tile goes to the overview', () => {
  const twoClusters = {
    body: {
      state: 'ok',
      items: [
        { name: 'prod', label: 'prod', state: 'ok' },
        { name: 'staging', label: 'staging', state: 'ok' },
      ],
    },
  };

  function tile(name: string): HTMLElement {
    const found = tiles().find((one) => one.dataset['cluster'] === name);
    if (found === undefined) throw new Error(`no tile for ${name}`);
    return found;
  }

  it('links the active tile to the bare cluster address and the others to what they remember', async () => {
    rememberKind('prod', 'pods');
    rememberKind('staging', 'deployments');
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': twoClusters,
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    });

    mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    // The tile of the cluster on screen: home, not where they already are.
    expect(tile('prod').getAttribute('href')).toBe('/c/prod');
    // The other tile: back to where they were over there.
    expect(tile('staging').getAttribute('href')).toBe('/c/staging/deployments');
    // And the memory is not touched by the rail: it is read on the next switch
    // back, which is the case the memory is for.
    expect(lastKind('prod')).toBe('pods');
  });

  it('links the other tile to the drawer that was open there, in the ?open= form the list reads', async () => {
    // The tile returns to the kind *and* to the drawer that was open on
    // it - by naming the object in the address, so that the destination page
    // re-opens it for its own cluster. Nothing is carried across: `Drawer.tsx`
    // still empties on the switch.
    rememberKind('prod', 'pods');
    rememberKind('staging', 'pods');
    rememberOpen('staging', { kind: 'pods', namespace: 'shop', name: 'web-1' });
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': twoClusters,
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    });

    mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    // The namespace travels with the name: the list this lands on is not
    // narrowed to one, and a bare name could be several rows.
    expect(tile('staging').getAttribute('href')).toBe('/c/staging/pods?open=shop%2Fweb-1');
  });

  it('links a cluster-scoped object by its name alone', async () => {
    rememberKind('staging', 'nodes');
    rememberOpen('staging', { kind: 'nodes', name: 'worker-2' });
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': twoClusters,
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    });

    mountApp('/c/prod');

    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    expect(tile('staging').getAttribute('href')).toBe('/c/staging/nodes?open=worker-2');
  });

  it('still links the active tile to the bare cluster address while its drawer is remembered', async () => {
    // The same rule: the tile of the cluster on screen is the way to
    // the overview, whatever the memory holds about it.
    rememberKind('prod', 'pods');
    rememberOpen('prod', { kind: 'pods', namespace: 'shop', name: 'web-1' });
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': twoClusters,
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    });

    mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    expect(tile('prod').getAttribute('href')).toBe('/c/prod');
  });

  it('links a tile with nothing remembered to the cluster address, active or not', async () => {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': twoClusters,
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    });

    mountApp('/c/prod');

    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    expect(tile('prod').getAttribute('href')).toBe('/c/prod');
    expect(tile('staging').getAttribute('href')).toBe('/c/staging');
  });
});

/*
  The rail is the compact strip at every width: no *Narrow* control, no
  `kubetower.rail.folded`, no wider rendering with a printed label. These pin
  that each of those is absent rather than hidden, and that what a 56px rail
  has to keep is kept.
*/
describe('the rail is a strip of squares, always 56px, with no fold', () => {
  const twoClusters = {
    body: {
      state: 'ok',
      items: [
        {
          name: 'prod',
          label: 'PRODUCTION-EKS-1',
          initials: 'P1',
          colour: 'amber-pale',
          state: 'unreachable',
          reason: { code: 'cluster_unreachable', message: 'dial tcp: i/o timeout' },
        },
        // A second, so there is a rail: with one cluster `Shell` draws none.
        { name: 'staging', label: 'staging', initials: 'S', colour: 'teal', state: 'ok' },
      ],
    },
  };

  async function mounted(): Promise<void> {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': twoClusters,
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    });
    mountApp('/');
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
  }

  it('draws no fold control and no fold state, at any width', async () => {
    await mounted();

    expect(rail().querySelector('[aria-controls="rail"]')).toBeNull();
    expect(screen.queryByRole('button', { name: /narrow|widen/i })).toBeNull();
    expect(rail().dataset['folded']).toBeUndefined();
    // `w-14` and nothing that widens it: no `sm:` arm and no token. jsdom has
    // no viewport, so this is the class; the pixels are `rail.spec.ts`
    // case 3, at 1280px and at 500px.
    expect(rail().className).toContain('w-14');
    expect(rail().className).not.toMatch(/(^|\s)\w+:w-/);
    expect(rail().className).not.toContain('w-rail');
  });

  it('remembers nothing, because there is nothing to choose', async () => {
    // A browser that folded the old rail still carries the key. It changes
    // nothing, and nothing writes it again.
    localStorage.setItem('kubetower.rail.folded', '0');
    await mounted();
    expect(rail().className).toContain('w-14');
    expect(localStorage.getItem('kubetower.rail.folded')).toBe('0');
  });

  it('draws each tile as an icon with its state in a corner, and its initials only in its name', async () => {
    await mounted();

    const tile = tiles()[0]!;
    expect(tile.getAttribute('href')).toBe('/c/prod');
    expect(tile.className).toContain('size-9');
    // The face is the icon, drawn as SVG, and carries no text.
    await waitFor(() => {
      expect(tile.querySelector('[data-cluster-icon] svg')).not.toBeNull();
    });
    expect(tile.textContent).not.toContain('P1');
    // The badge: in the state's tone, and never the color alone - the
    // description says it too.
    expect(tile.querySelector('[data-badge="unreachable"]')?.getAttribute('class')).toContain('bg-bad');
    expect(tile.getAttribute('aria-label')).toBe('PRODUCTION-EKS-1 (P1)');
    expect(described(tile)).toBe(`could not be reached: dial tcp: i/o timeout. ${HINT}`);
    // And the tooltip is the name alone.
    expect(tile.getAttribute('title')).toBe('PRODUCTION-EKS-1');
  });

  it("draws each default icon on the server's palette key", async () => {
    // `Cluster.colour` is a `ClusterColour` key: the background of the
    // cluster's default icon,
    // painted from the theme's own swatch - never a value from the wire in a
    // class or a style. jsdom has no theme, so the two swatches are set here.
    const root = document.documentElement.style;
    root.setProperty('--color-plate-amber-pale', 'hsl(45 55% 62%)');
    root.setProperty('--color-plate-teal', 'hsl(180 55% 32%)');
    try {
      await mounted();
      const [prod, staging] = [tiles()[0]!, tiles()[1]!];
      const svgOf = async (tile: HTMLElement): Promise<string> =>
        waitFor(() => {
          const svg = tile.querySelector('[data-cluster-icon] svg');
          expect(svg).not.toBeNull();
          return svg!.outerHTML;
        });
      // hsl(45 55% 62%) is d3b969 and hsl(180 55% 32%) is 257e7e.
      expect(await svgOf(prod)).toContain('d3b969');
      expect(await svgOf(staging)).toContain('257e7e');
      // The tile is never a plain color square any more.
      expect(prod.className).not.toContain('bg-plate');
      expect(prod.getAttribute('style')).toBeNull();
    } finally {
      root.removeProperty('--color-plate-amber-pale');
      root.removeProperty('--color-plate-teal');
    }
  });

  it('keeps the hotbar digits and the arrange control reachable', async () => {
    // A 56px
    // column that takes away the way back has taken away more than the width.
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'prod', label: 'prod', state: 'ok', bar: 1 },
            { name: 'staging', label: 'staging', state: 'ok', bar: 2 },
          ],
        },
      },
      '/api/preferences': {
        body: { rail: { barCount: 3, activeBar: 1, bars: [['prod'], ['staging']], hidden: [] } },
      },
    });
    mountApp('/');
    await waitFor(() => {
      expect(rail().querySelector('[aria-label="Hotbar"]')).not.toBeNull();
    });

    expect(rail().querySelectorAll('[aria-label^="Hotbar "]')).toHaveLength(3);
    const arrange = screen.getByRole('button', { name: 'Arrange' });
    expect(arrange.getAttribute('title')).toBe('Arrange the rail');
  });
});

/*
  The foot of the rail is `SessionTile.tsx`, in its narrow form - the identity
  as the `account` glyph with the name on `title` and to a screen reader, and
  `Log out` as its glyph.
*/
describe('the foot of the rail is drawn for 56px', () => {
  // Two, because with one there is no rail to test: `Shell` does not draw it
  // (`onlyCluster`). The second is there to make the column exist, and nothing
  // below is about it.
  const twoClusters = {
    body: {
      state: 'ok',
      items: [
        { name: 'prod', label: 'prod', state: 'ok' },
        { name: 'staging', label: 'staging', state: 'ok' },
      ],
    },
  };

  /** The identity's own paragraph, found by the `title` that carries it. */
  function identity(who: string): HTMLElement {
    const found = rail().querySelector<HTMLElement>(`p[title="${who}"]`);
    if (found === null) throw new Error(`no identity line for ${who}`);
    return found;
  }

  function signOut(): HTMLElement {
    return screen.getByRole('button', { name: 'Log out' });
  }

  async function mounted(who: string, endsAt?: string): Promise<void> {
    stubFetch({
      '/api/session': {
        body: {
          identity: who,
          idleExpiresAt: '2999-01-01T00:00:00Z',
          absoluteExpiresAt: endsAt ?? '2999-01-01T00:00:00Z',
        },
      },
      '/api/auth/methods': methods,
      '/api/clusters': twoClusters,
      '/api/auth/logout': { status: 204 },
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    });
    mountApp('/');
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
  }

  it('draws the identity as a glyph, keeping the name on hover and to a reader', async () => {
    await mounted('alice');

    const line = identity('alice');
    // Nothing printed: the glyph, and no breakpoint that brings a word back.
    expect(line.querySelector('span[aria-hidden="true"]')).toBeNull();
    expect(line.querySelector('use')?.getAttribute('href')).toBe('#i-account');
    expect(line.querySelector('svg')?.getAttribute('class')).not.toContain('sm:');
    // What is drawn is deliberately not a square of initials: one more at the
    // foot of a column of them would read as one more cluster.
    expect(line.getAttribute('title')).toBe('alice');
    expect(line.querySelector('.sr-only')?.textContent).toBe('alice');
  });

  it('keeps Log out one control, its glyph, still saying what it does', async () => {
    await mounted('alice');

    const button = signOut();
    expect(rail().querySelectorAll('button[aria-label="Log out"]')).toHaveLength(1);
    // The account menu's glyph for the same act, and not
    // `external`, the arrow out of a box that means *opens elsewhere*.
    expect(button.querySelector('use')?.getAttribute('href')).toBe('#i-logout');
    expect(button.querySelector('span')).toBeNull();
    expect(button.getAttribute('title')).toBe('Log out');
    expect(button.getAttribute('aria-label')).toBe('Log out');
    // Sign out is called Log out everywhere it is drawn, and so said nowhere.
    expect(document.body.innerHTML).not.toMatch(/sign[ -]?out/i);
  });

  it('still signs out, because a glyph that does nothing is a decoration', async () => {
    // The entry asks that `Log out` stay *reachable*, and an accessible name on
    // a button proves only that it is named.
    await mounted('alice');

    const seen: string[] = [];
    const inner = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const raw =
        typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
      seen.push(`${init?.method ?? 'GET'} ${raw.split('?')[0] ?? raw}`);
      return inner(input, init);
    });

    fireEvent.click(signOut());

    await waitFor(() => {
      expect(seen).toContain('POST /api/auth/logout');
    });
  });

  it('survives an identity that is not five letters long', async () => {
    // `admin` fits at 56px by luck. The glyph is the same size for both.
    const long = 'someone.with.a.long.name@example.org';
    await mounted(long);

    const line = identity(long);
    expect(line.querySelector('use')?.getAttribute('href')).toBe('#i-account');
    expect(line.getAttribute('title')).toBe(long);
    expect(line.querySelector('.sr-only')?.textContent).toBe(long);
  });

  /** The rail's own `status`, not the shell's - `ListView` and the live region
   *  carry that role too, so an unscoped query finds three. */
  function expiry(): HTMLElement | null {
    return Array.from(rail().querySelectorAll<HTMLElement>('[role="status"]')).find((found) =>
      found.textContent?.includes('Session ends in'),
    ) ?? null;
  }

  it('keeps the expiry warning whole, which is the line with no short form', async () => {
    await mounted('alice', new Date(Date.now() + 5 * 60 * 1000).toISOString());

    await waitFor(() => {
      expect(expiry()).not.toBeNull();
    });
    // Still the whole sentence. It wraps inside a 56px column rather than being
    // abbreviated: a warning somebody has to hover to read is not a warning.
    expect(expiry()?.textContent).toContain('Session ends in');
    expect(expiry()?.className).not.toContain('hidden');
  });
});

/*
  The controls at the foot of the rail wear the console's treatments:
  `hover:bg-panel-2` for a control that is a glyph with no word, and no
  printed word at 56px.

  Two things these cannot see, said here rather than left to be assumed. jsdom
  applies no stylesheet, so a `hover:` class is a string in `className` and
  nothing more, and `hidden` moves no pixel - the geometry is `web/measure`'s,
  which has the 500px case. What is pinned here is the *pair*: the
  treatment present and the one it replaces absent, because "some hover class
  exists" passes on a control that carries both.
*/
describe("the rail's foot controls wear the console's treatments", () => {
  // Two, because with one there is no rail to test: `Shell` does not draw it
  // (`onlyCluster`). The second is there to make the column exist, and nothing
  // below is about it.
  const twoClusters = {
    body: {
      state: 'ok',
      items: [
        { name: 'prod', label: 'prod', state: 'ok' },
        { name: 'staging', label: 'staging', state: 'ok' },
      ],
    },
  };

  function arrange(): HTMLElement {
    const found = rail().querySelector<HTMLElement>('button[aria-label="Arrange"]');
    if (found === null) throw new Error('no Arrange control on the rail');
    return found;
  }

  async function mounted(): Promise<void> {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': twoClusters,
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    });
    mountApp('/');
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
  }

  it('gives Arrange the hover a glyph-only control gets, and not the one that paints nothing', async () => {
    await mounted();

    // The pair, both halves. `hover:text-fg` does
    // nothing on a glyph whose fill is not `currentColor`, and asserting only
    // the presence of `hover:bg-panel-2` would pass on a control that carried
    // both.
    expect(arrange().className).toContain('hover:bg-panel-2');
    expect(arrange().className).not.toContain('hover:text-fg');
    // The three hover families - the accent frame for what watches, Claude's color
    // for the mark, the frame to full `--bad` for what destroys - are refused
    // by name: Arrange watches nothing and destroys nothing, and an accent
    // frame would make it look like the tile you are on.
    expect(arrange().className).not.toContain('hover:bg-accent');
    expect(arrange().className).not.toContain('hover:border-accent');
    expect(arrange().className).not.toContain('hover:border-bad');
  });

  it('is its glyph alone, named "Arrange" with the sentence on its title', async () => {
    await mounted();
    expect(arrange().querySelector('span')).toBeNull();
    expect(arrange().querySelector('use')?.getAttribute('href')).toBe('#i-edit');
    expect(arrange().querySelector('svg')?.getAttribute('class')).not.toContain('sm:');
    expect(arrange().getAttribute('aria-label')).toBe('Arrange');
    expect(arrange().getAttribute('title')).toBe('Arrange the rail');
    expect(screen.getByRole('button', { name: 'Arrange' })).toBe(arrange());
  });

  it('opens the arrange mode, because a glyph that does nothing is a decoration', async () => {
    await mounted();

    fireEvent.click(arrange());
    await waitFor(() => {
      expect(screen.getByRole('dialog')).toBeTruthy();
    });
  });

  /*
    Kinds is at the foot, below Arrange. It takes Arrange's shape for Arrange's
    reason - 56px has no room for the word - and Arrange's hover, for the same
    reason.
  */
  it('carries Kinds below Arrange, its glyph alone, with the hover a glyph-only control gets', async () => {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': twoClusters,
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    });
    mountApp('/c/prod');
    // Arrange waits for the capabilities, and Kinds waits with it, so
    // it is never drawn higher up and then pushed down.
    await screen.findByRole('button', { name: 'Arrange' });
    const kinds = await screen.findByRole('button', { name: 'Kinds' });

    expect(rail().contains(kinds)).toBe(true);
    expect(arrange().compareDocumentPosition(kinds) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(kinds.querySelector('span')).toBeNull();
    expect(kinds.querySelector('use')?.getAttribute('href')).toBe('#i-workloads');
    expect(kinds.getAttribute('aria-label')).toBe('Kinds');
    expect(kinds.getAttribute('aria-controls')).toBe('menu');
    expect(kinds.getAttribute('title')).toBe('Hide the kinds column (Ctrl+B)');
    expect(kinds.className).toContain('hover:bg-panel-2');
    expect(kinds.className).not.toContain('hover:text-fg');
    expect(kinds.className).not.toContain('hover:border-accent');
    // The console's ring, as on every control here.
    expect(kinds.className).toContain('focus-visible:outline-offset-2');
  });

  it('draws no Kinds on the rail where no cluster is on screen', async () => {
    await mounted();
    expect(rail().querySelector('button[aria-controls="menu"]')).toBeNull();
  });
});

/*
  Arrange and the bar switcher write preferences, and a console that keeps none -
  the server profile, which serves one cluster, or several
  replicas - says so with `capabilities.preferences` false, the routes not being
  registered. Both arms: an absent control is also what a rail that failed to
  draw its foot looks like.
*/
describe('a rail on a console that keeps no preferences', () => {
  function mountedWith(keeps: boolean): void {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      // Two clusters, because with one the rail is not drawn at all and Arrange
      // is the header's (Shell.test.tsx covers that arm).
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'prod', label: 'prod', state: 'ok', order: 0 },
            { name: 'staging', label: 'staging', state: 'ok', order: 1 },
          ],
        },
      },
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
      '/api/capabilities': {
        body: {
          mode: 'server',
          localShell: false,
          podExec: true,
          nodeShell: false,
          assistant: false,
          portForward: false,
          preferences: keeps,
          readOnly: { writesRefused: false, detailWritesRefused: false },
        },
      },
    });
    mountApp('/');
  }

  it('offers no Arrange, and still draws every tile', async () => {
    mountedWith(false);
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    await waitFor(() => {
      expect(rail().querySelector('button[aria-label="Arrange"]')).toBeNull();
    });
  });

  it('offers Arrange where it keeps them', async () => {
    mountedWith(true);
    await waitFor(() => {
      expect(rail().querySelector('button[aria-label="Arrange"]')).not.toBeNull();
    });
  });

  /** Two clusters on two hotbars, which is what draws the switcher at all. */
  function mountedOnTwoBars(keeps: boolean): void {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'prod', label: 'prod', state: 'ok', bar: 1, order: 0 },
            { name: 'staging', label: 'staging', state: 'ok', bar: 2, order: 0 },
          ],
        },
      },
      '/api/preferences': {
        body: { rail: { barCount: 2, activeBar: 1, bars: [['prod'], ['staging']], hidden: [] } },
      },
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
      '/api/capabilities': {
        body: {
          mode: 'shared',
          localShell: false,
          podExec: true,
          nodeShell: false,
          assistant: false,
          portForward: false,
          preferences: keeps,
          replicated: false,
          readOnly: { writesRefused: false, detailWritesRefused: false },
        },
      },
    });
    mountApp('/');
  }

  it('offers neither Arrange nor the bar switcher, on one replica of the server profile', async () => {
    mountedOnTwoBars(false);
    await waitFor(() => {
      expect(tiles().map((tile) => tile.dataset['cluster'])).toContain('prod');
    });
    // Settled on the capabilities answer, not on the first paint, where both
    // controls are drawn while the member is still unknown.
    await waitFor(() => {
      expect(rail().querySelector('button[aria-label="Arrange"]')).toBeNull();
    });
    expect(rail().querySelector('[aria-label="Hotbar"]')).toBeNull();
    expect(rail().querySelector('[aria-label^="Hotbar "]')).toBeNull();
  });

  it('offers both where it keeps them', async () => {
    mountedOnTwoBars(true);
    await waitFor(() => {
      expect(rail().querySelector('[aria-label="Hotbar"]')).not.toBeNull();
    });
    expect(rail().querySelector('button[aria-label="Arrange"]')).not.toBeNull();
  });
});

/*
  A right-click on
  a tile opens that cluster's nameplate editor beside it - name, initials (up to
  three), the sixteen swatches, Reset - and saving redraws the tile.
*/
describe('right-click on a tile edits that cluster', () => {
  const before = {
    body: {
      state: 'ok',
      items: [
        { name: 'prod', label: 'prod', initials: 'P', colour: 'amber', state: 'ok', bar: 1, order: 0 },
        { name: 'staging', label: 'staging', initials: 'S', colour: 'violet', state: 'ok', bar: 1, order: 1 },
      ],
    },
  };
  function mountedEditable(nameplates: Record<string, unknown> = {}): {
    routes: Record<string, StubReply>;
    posted: { path: string; body: unknown }[];
  } {
    const routes: Record<string, StubReply> = {
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': before,
      '/api/preferences': {
        body: { rail: { barCount: 3, activeBar: 1, bars: [['prod', 'staging']], hidden: [], nameplates } },
      },
      '/api/preferences/rail/nameplate': { status: 204 },
    };
    stubFetch(routes);
    // Both clusters were given their first icon already,
    // so the writes these tests count are the editor's alone.
    settleIcon('prod');
    settleIcon('staging');
    const posted: { path: string; body: unknown }[] = [];
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if (init?.method === 'POST') posted.push({ path, body: JSON.parse(String(init.body)) });
      return stubbed(input, init);
    });
    mountApp('/');
    return { routes, posted };
  }

  function tile(name: string): HTMLElement {
    const found = tiles().find((one) => one.dataset['cluster'] === name);
    if (found === undefined) throw new Error(`no tile for ${name}`);
    return found;
  }

  /** Right-click, then "Customize icon…": the dialog. It draws all 221 style
   *  thumbnails, which in jsdom on a CI runner can take 7 to 9 s, so every
   *  test that opens it carries a 20 s timeout. */
  async function customise(name: string): Promise<HTMLElement> {
    fireEvent.contextMenu(tile(name));
    await screen.findByRole('dialog', { name: new RegExp(name) });
    fireEvent.click(screen.getByRole('button', { name: 'Customize icon…' }));
    return screen.findByRole('dialog', { name: `Icon for ${name}` });
  }

  it('opens a small menu for the tile right-clicked: eight colors, Customize and Reset', async () => {
    mountedEditable();
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });

    // `false` is the event's default prevented: the browser's own menu does
    // not open over this one.
    expect(fireEvent.contextMenu(tile('staging'))).toBe(false);
    const menu = await screen.findByRole('dialog', { name: /staging/ });
    expect(menu.dataset['editor']).toBe('staging');
    // At most eight colors - the eight of
    // the sixteen furthest apart - then Customize icon… and Reset icon. No
    // name field, no icon controls: those are the dialog's.
    const colours = within(menu).getByRole('radiogroup', { name: 'Color for staging' });
    const swatches = within(colours).getAllByRole('radio').map((one) => one.getAttribute('aria-label'));
    expect(swatches).toEqual(['Red', 'Amber', 'Pale lime', 'Green', 'Teal', 'Pale blue', 'Violet', 'Pale pink']);
    expect(within(colours).getByRole('radio', { name: 'Violet' }).getAttribute('aria-checked')).toBe('true');
    expect(within(menu).getByRole('button', { name: 'Customize icon…' })).toBeTruthy();
    expect(within(menu).getByRole('button', { name: 'Reset icon' })).toBeTruthy();
    expect(within(menu).queryByRole('textbox')).toBeNull();
    // Focus starts on Customize, the one thing the menu cannot do in place.
    await waitFor(() => {
      expect(document.activeElement).toBe(within(menu).getByRole('button', { name: 'Customize icon…' }));
    });
  });

  it('writes a color at once from the menu, and moves a stored icon onto it', async () => {
    const icon = '{"v":1,"style":"kt-rings-solo","seed":"x","background":"amber"}';
    const { posted } = mountedEditable({ prod: { icon } });
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    fireEvent.contextMenu(tile('staging'));
    await screen.findByRole('dialog', { name: /staging/ });
    fireEvent.click(screen.getByRole('radio', { name: 'Teal' }));
    await waitFor(() => {
      expect(posted).toEqual([{ path: '/api/preferences/rail/nameplate', body: { cluster: 'staging', colour: 'teal' } }]);
    });
    // The menu stays: a color is tried by looking.
    expect(screen.getByRole('dialog', { name: /staging/ })).toBeTruthy();
    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).toBeNull();
    });

    // A stored icon takes the color as its background, in the same write.
    fireEvent.contextMenu(tile('prod'));
    await screen.findByRole('dialog', { name: /prod/ });
    fireEvent.click(screen.getByRole('radio', { name: 'Red' }));
    await waitFor(() => {
      expect(posted[1]).toEqual({
        path: '/api/preferences/rail/nameplate',
        body: { cluster: 'prod', colour: 'red', icon: '{"v":1,"style":"kt-rings-solo","seed":"x","background":"red"}' },
      });
    });
  });

  it('resets the icon and the color in one write, and leaves the name', async () => {
    const { posted } = mountedEditable({ prod: { name: 'Prod EU', colour: 'amber' } });
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    fireEvent.contextMenu(tile('prod'));
    await screen.findByRole('dialog', { name: /prod/ });
    fireEvent.click(screen.getByRole('button', { name: 'Reset icon' }));
    await waitFor(() => {
      expect(posted).toEqual([{ path: '/api/preferences/rail/nameplate', body: { cluster: 'prod', colour: '', icon: RESET_ICON } }]);
    });
  });

  it('offers clusters, infrastructure, abstract styles and the patterns in the dialog, grouped and filterable', async () => {
    mountedEditable();
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    const dialog = await customise('prod');
    const styles = (): HTMLElement[] =>
      within(within(dialog).getByRole('radiogroup', { name: 'Style for prod' })).getAllByRole('radio');
    expect(styles()).toHaveLength(221);
    expect(within(dialog).getAllByRole('tab').map((one) => one.textContent)).toEqual([
      'All styles (221)',
      'Clusters (20)',
      'Kubernetes (12)',
      'Servers (10)',
      'Network & cloud (9)',
      'Data & storage (9)',
      'Platform (10)',
      'Security (10)',
      'Operations (10)',
      'Delivery (10)',
      'Resources (10)',
      'Actions (10)',
      'Insight (10)',
      'Console (10)',
      'Abstract (40)',
      'Patterns (41)',
    ]);
    fireEvent.click(within(dialog).getByRole('tab', { name: 'Kubernetes (12)' }));
    expect(styles()).toHaveLength(12);
    expect(styles()[0]?.getAttribute('aria-label')).toBe('Pod');
    fireEvent.click(within(dialog).getByRole('tab', { name: 'All styles (221)' }));
    fireEvent.change(within(dialog).getByRole('searchbox'), { target: { value: 'set' } });
    expect(styles().map((one) => one.getAttribute('aria-label'))).toEqual(['Replica set', 'Stateful set', 'Daemon set', 'Settings']);
    // Every thumbnail is drawn, in its own style, with this cluster's seed.
    await waitFor(
      () => {
        for (const one of styles()) expect(one.querySelector('svg'), one.getAttribute('aria-label') ?? '').not.toBeNull();
      },
      { timeout: 10_000 },
    );
  }, 20_000);

  it('saves a style picked from the gallery, reads it back, and the tile draws it', async () => {
    const { routes, posted } = mountedEditable();
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    const dialog = await customise('prod');
    fireEvent.click(within(dialog).getByRole('radio', { name: 'Server' }));
    const icon = '{"v":1,"style":"kt-server","seed":"prod","background":"amber"}';
    routes['/api/preferences'] = {
      body: { rail: { barCount: 3, activeBar: 1, bars: [['prod', 'staging']], hidden: [], nameplates: { prod: { icon } } } },
    };
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => {
      expect(posted).toEqual([{ path: '/api/preferences/rail/nameplate', body: { cluster: 'prod', icon } }]);
    });
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).toBeNull();
    });
    await waitFor(() => {
      expect(tile('prod').querySelector('[data-cluster-icon] svg')).not.toBeNull();
    });
    // Read back: the dialog opens on the stored style.
    const again = await customise('prod');
    expect(within(again).getByRole('radio', { name: 'Server' }).getAttribute('aria-checked')).toBe('true');
    expect(again.querySelector('[data-chosen-style]')?.textContent).toBe('Server');
  }, 20_000);

  it('saves a style seeded from the context name', async () => {
    const { posted } = mountedEditable();
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    const dialog = await customise('prod');
    expect((within(dialog).getByRole('textbox', { name: /^Seed/ }) as HTMLInputElement).value).toBe('prod');
    fireEvent.click(within(dialog).getByRole('radio', { name: 'Rings solo' }));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => {
      expect(posted).toEqual([
        {
          path: '/api/preferences/rail/nameplate',
          body: { cluster: 'prod', icon: '{"v":1,"style":"kt-rings-solo","seed":"prod","background":"amber"}' },
        },
      ]);
    });
  }, 20_000);

  it('seeds a long context name with as much of it as a recipe holds', async () => {
    const long = `arn:aws:eks:eu-central-1:123456789012:cluster/${'x'.repeat(40)}`;
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: long, label: long, initials: 'X', colour: 'red', state: 'ok', bar: 1, order: 0 },
            { name: 'staging', label: 'staging', initials: 'S', colour: 'violet', state: 'ok', bar: 1, order: 1 },
          ],
        },
      },
      '/api/preferences': {
        body: { rail: { barCount: 3, activeBar: 1, bars: [[long, 'staging']], hidden: [], nameplates: {} } },
      },
      '/api/preferences/rail/nameplate': { status: 204 },
    });
    mountApp('/');
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    fireEvent.contextMenu(tile(long));
    await screen.findByRole('dialog');
    fireEvent.click(screen.getByRole('button', { name: 'Customize icon…' }));
    const dialog = await screen.findByRole('dialog', { name: /^Icon for / });
    expect((within(dialog).getByRole('textbox', { name: /^Seed/ }) as HTMLInputElement).value).toBe(long.slice(0, 64));
  }, 20_000);

  it('draws the default icon for a stored recipe it cannot read', async () => {
    mountedEditable({ prod: { icon: '{"v":9,"style":"unknown"}' } });
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    await waitFor(() => {
      expect(tile('prod').querySelector('[data-cluster-icon] svg')).not.toBeNull();
    });
  });

  it('writes nothing when the dialog is saved untouched', async () => {
    const { posted } = mountedEditable({ prod: { icon: '{"v":1,"style":"kt-rings-solo","seed":"x"}' } });
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    const dialog = await customise('prod');
    expect(dialog.querySelector('[data-chosen-style]')?.textContent).toBe('Rings solo');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).toBeNull();
    });
    expect(posted).toEqual([]);
  }, 20_000);

  it('closes on Escape and gives the keyboard back to the tile', async () => {
    mountedEditable();
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    tile('staging').focus();
    fireEvent.contextMenu(tile('staging'));
    await screen.findByRole('dialog', { name: /staging/ });

    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).toBeNull();
    });
    expect(document.activeElement).toBe(tile('staging'));
  });

  it('leaves the browser its menu where the console keeps no preferences', async () => {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': before,
      '/api/capabilities': {
        body: {
          mode: 'shared',
          localShell: false,
          podExec: true,
          nodeShell: false,
          assistant: false,
          portForward: false,
          preferences: false,
          replicated: false,
          readOnly: { writesRefused: false, detailWritesRefused: false },
        },
      },
    });
    mountApp('/');
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    await waitFor(() => {
      expect(rail().querySelector('button[aria-label="Arrange"]')).toBeNull();
    });

    expect(fireEvent.contextMenu(tile('prod'))).toBe(true);
    expect(screen.queryByRole('dialog')).toBeNull();
    // And says nothing about an editor that is not there.
    expect(tile('prod').getAttribute('title')).not.toContain('F2');
  });

  it('does not let a save that lands late close the editor opened since', async () => {
    /*
      Save on prod with the request hung, Escape, open staging, and let prod's
      answer arrive. The success must not close "the editor", which by then is
      staging's.
    */
    mountedEditable();
    let release: (() => void) | undefined;
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method !== 'POST') return stubbed(input, init);
      return new Promise<Response>((resolve) => {
        release = () => {
          resolve(new Response(null, { status: 204 }));
        };
      });
    });
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });

    const dialog = await customise('prod');
    fireEvent.click(within(dialog).getByRole('radio', { name: 'Rings solo' }));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => {
      expect(release).toBeDefined();
    });

    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).toBeNull();
    });
    fireEvent.contextMenu(tile('staging'));
    await screen.findByRole('dialog', { name: /staging/ });

    release?.();
    // The write landed, so the rail is refreshed and it is said...
    await waitFor(() => {
      expect(document.body.textContent).toContain('prod has a new icon.');
    });
    // ...and staging's editor is still the one open.
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(screen.getByRole('dialog', { name: /staging/ })).toBeTruthy();
  }, 20_000);

  it('keeps Save pressable for the keyboard while a save is pending, and does not send it twice', async () => {
    const { posted } = mountedEditable();
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method !== 'POST') return stubbed(input, init);
      // Recorded by the stub underneath, then never answered.
      void stubbed(input, init);
      return new Promise<Response>(() => undefined);
    });
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    const dialog = await customise('prod');
    fireEvent.click(within(dialog).getByRole('radio', { name: 'Rings solo' }));
    const save = within(dialog).getByRole('button', { name: 'Save' }) as HTMLButtonElement;
    save.focus();
    fireEvent.click(save);
    await waitFor(() => {
      expect(save.getAttribute('aria-disabled')).toBe('true');
    });
    // Not `disabled`, which in a browser drops focus out of the editor.
    expect(save.disabled).toBe(false);
    expect(document.activeElement).toBe(save);
    fireEvent.click(save);
    // A beat, so a second request would have started by now.
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(posted).toHaveLength(1);
  }, 20_000);

  it('opens on F2 as well, the rename key a Mac keyboard has', async () => {
    mountedEditable();
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    tile('staging').focus();
    fireEvent.keyDown(tile('staging'), { key: 'F2' });
    const editor = await screen.findByRole('dialog', { name: /staging/ });
    expect(editor.dataset['editor']).toBe('staging');
  });

  it('keeps the keyboard inside the menu, which sits on a scrim', async () => {
    /*
      Tabbing out would reach the menu and the top bar - all behind the scrim,
      where nothing can be pressed. Tab from the last control comes
      back to the first, and Shift+Tab the other way.
    */
    mountedEditable();
    await waitFor(() => {
      expect(tiles()).toHaveLength(2);
    });
    fireEvent.contextMenu(tile('prod'));
    const menu = await screen.findByRole('dialog', { name: /prod/ });
    const first = within(menu).getByRole('radio', { name: 'Red' });
    // Reset is disabled with nothing stored, so Customize is the last stop.
    const last = within(menu).getByRole('button', { name: 'Customize icon…' });
    await waitFor(() => {
      expect(document.activeElement).toBe(last);
    });
    fireEvent.keyDown(last, { key: 'Tab' });
    expect(document.activeElement).toBe(first);
    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true });
    expect(document.activeElement).toBe(last);
    // In between, Tab is the browser's own: nothing is taken from it.
    first.focus();
    expect(fireEvent.keyDown(first, { key: 'Tab' })).toBe(true);
    expect(menu.contains(document.activeElement)).toBe(true);
  });

  it('fits a 320px window, a margin each side', async () => {
    const width = window.innerWidth;
    window.innerWidth = 320;
    try {
      mountedEditable();
      await waitFor(() => {
        expect(tiles()).toHaveLength(2);
      });
      fireEvent.contextMenu(tile('prod'));
      const editor = await screen.findByRole('dialog', { name: /prod/ });
      // jsdom draws nothing, so the arithmetic and the cap are what is read:
      // placed 8px in, and never wider than the window less 16px.
      expect(editor.style.left).toBe('8px');
      expect(editor.className).toContain('max-w-[calc(100vw-var(--spacing)*4)]');
    } finally {
      window.innerWidth = width;
    }
  });
});

describe("a tile's state badge is a shape as well as a color", () => {
  it('draws one shape per kind of state, so no two kinds differ by color alone', async () => {
    stubFetch({
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'a', label: 'a', initials: 'A', colour: 'red', state: 'ok' },
            { name: 'b', label: 'b', initials: 'B', colour: 'red', state: 'pending' },
            { name: 'c', label: 'c', initials: 'C', colour: 'red', state: 'empty' },
            { name: 'd', label: 'd', initials: 'D', colour: 'red', state: 'forbidden' },
            { name: 'e', label: 'e', initials: 'E', colour: 'red', state: 'unreachable' },
          ],
        },
      },
    });
    mountApp('/');
    await waitFor(() => {
      expect(tiles()).toHaveLength(5);
    });
    const shape = (state: string): string => {
      const badge = rail().querySelector(`[data-badge="${state}"]`);
      if (badge === null) throw new Error(`no badge for ${state}`);
      const classes = badge.getAttribute('class') ?? '';
      // The color is taken out: what is left is the shape.
      return classes
        .split(' ')
        .filter((each) => !/^(bg|border)-(ok|accent|muted|warn|bad)$/.test(each))
        .join(' ');
    };
    const shapes = ['ok', 'pending', 'empty', 'forbidden', 'unreachable'].map(shape);
    expect(new Set(shapes).size).toBe(5);
    expect(shape('unreachable')).toContain('rotate-45');
  });
});

describe('a cluster seen with no icon is given one drawn at random', () => {
  const three = {
    body: {
      state: 'ok',
      items: [
        { name: 'prod', label: 'prod', colour: 'amber', state: 'ok', bar: 1, order: 0 },
        { name: 'staging', label: 'staging', colour: 'violet', state: 'ok', bar: 1, order: 1 },
        { name: 'edge', label: 'edge', colour: 'teal', state: 'ok', bar: 1, order: 2 },
      ],
    },
  };
  const stored = '{"v":1,"style":"kt-rings-solo","seed":"x"}';

  function preferences(nameplates: Record<string, unknown>): StubReply {
    return { body: { rail: { barCount: 3, activeBar: 1, bars: [['prod', 'staging', 'edge']], hidden: [], nameplates } } };
  }

  /** The browser's generator, stubbed: each value it hands out is the next of
   *  `faces` (fractions of the full range), so the draws are known in advance. */
  function stubDice(faces: number[]): void {
    let at = 0;
    vi.spyOn(crypto, 'getRandomValues').mockImplementation(<T extends ArrayBufferView | null>(array: T): T => {
      if (array instanceof Uint32Array) {
        for (let index = 0; index < array.length; index += 1) array[index] = Math.floor((faces[at++ % faces.length] ?? 0) * 2 ** 32);
      }
      return array;
    });
  }

  function mounted(
    nameplates: Record<string, unknown>,
    extra: Record<string, StubReply> = {},
  ): { posted: { path: string; body: { cluster?: string; icon?: string } }[] } {
    const plates: Record<string, unknown> = { ...nameplates };
    const routes: Record<string, StubReply> = {
      '/api/session': session,
      '/api/auth/methods': methods,
      '/api/clusters': three,
      '/api/preferences': preferences(nameplates),
      '/api/preferences/rail/nameplate': { status: 204 },
      ...extra,
    };
    stubFetch(routes);
    const posted: { path: string; body: { cluster?: string; icon?: string } }[] = [];
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if (init?.method === 'POST') {
        const body = JSON.parse(String(init.body)) as { cluster?: string; icon?: string; colour?: string };
        posted.push({ path, body });
        // Kept, as the server keeps it: the next read of the preferences has it.
        if (path === '/api/preferences/rail/nameplate' && body.cluster !== undefined) {
          const { cluster, ...change } = body;
          const plate: Record<string, unknown> = { ...(plates[cluster] as Record<string, unknown> | undefined) };
          for (const [key, value] of Object.entries(change)) {
            if (value === '') delete plate[key];
            else plate[key] = value;
          }
          plates[cluster] = plate;
          routes['/api/preferences'] = preferences(plates);
        }
      }
      return stubbed(input, init);
    });
    mountApp('/');
    return { posted };
  }

  function tile(name: string): HTMLElement {
    const found = tiles().find((one) => one.dataset['cluster'] === name);
    if (found === undefined) throw new Error(`no tile for ${name}`);
    return found;
  }

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('stores a random style, color and seed for each new cluster, and leaves a stored icon alone', async () => {
    // Ten faces per cluster - style, color, eight seed characters: staging
    // rolls the first face of each, edge the middle one.
    stubDice([...Array<number>(10).fill(0), ...Array<number>(10).fill(0.5)]);
    const { posted } = mounted({ prod: { icon: stored } });

    await waitFor(() => {
      expect(posted).toHaveLength(2);
    });
    expect(posted.map((one) => [one.path, one.body.cluster])).toEqual([
      ['/api/preferences/rail/nameplate', 'staging'],
      ['/api/preferences/rail/nameplate', 'edge'],
    ]);
    // The icon alone, as a recipe from the dialog is written.
    expect(Object.keys(posted[0]?.body ?? {}).sort()).toEqual(['cluster', 'icon']);
    const staging = parseIcon(posted[0]?.body.icon);
    const edge = parseIcon(posted[1]?.body.icon);
    expect(staging).toEqual({ v: 1, style: ICON_STYLES[0], seed: '00000000', background: PALETTE[0] });
    expect(edge).toEqual({
      v: 1,
      style: ICON_STYLES[Math.floor(ICON_STYLES.length / 2)],
      seed: 'iiiiiiii',
      background: PALETTE[PALETTE.length / 2],
    });

    // Stored, it stays: the preferences read again draw nothing new.
    await new Promise((resolve) => setTimeout(resolve, 100));
    expect(posted).toHaveLength(2);
  });

  it('draws nothing where every cluster has an icon', async () => {
    const { posted } = mounted({ prod: { icon: stored }, staging: { icon: stored }, edge: { icon: stored } });
    await waitFor(() => {
      expect(tiles()).toHaveLength(3);
    });
    await new Promise((resolve) => setTimeout(resolve, 100));
    expect(posted).toEqual([]);
  });

  it('draws nothing where the console keeps no preferences', async () => {
    const { posted } = mounted(
      {},
      {
        '/api/capabilities': {
          body: {
            mode: 'shared',
            localShell: false,
            podExec: true,
            nodeShell: false,
            assistant: false,
            portForward: false,
            preferences: false,
            replicated: false,
            readOnly: { writesRefused: false, detailWritesRefused: false },
          },
        },
      },
    );
    await waitFor(() => {
      expect(tiles()).toHaveLength(3);
    });
    await new Promise((resolve) => setTimeout(resolve, 100));
    expect(posted).toEqual([]);
  });

  it('lets the drawn icon be changed by hand, from what was drawn', async () => {
    stubDice([0.5]);
    const { posted } = mounted({ prod: { icon: stored }, edge: { icon: stored } });
    await waitFor(() => {
      expect(posted).toHaveLength(1);
    });
    const drawn = parseIcon(posted[0]?.body.icon);
    expect(posted[0]?.body.cluster).toBe('staging');
    fireEvent.contextMenu(tile('staging'));
    await screen.findByRole('dialog', { name: /staging/ });
    // The menu reads the stored icon once the preferences are read again: Reset
    // is offered for a cluster with something stored.
    await waitFor(() => {
      expect((screen.getByRole('button', { name: 'Reset icon' }) as HTMLButtonElement).disabled).toBe(false);
    });
    fireEvent.click(screen.getByRole('button', { name: 'Customize icon…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Icon for staging' });
    expect(dialog.querySelector('[data-chosen-style]')?.textContent).not.toBe('Server');
    fireEvent.click(within(dialog).getByRole('radio', { name: 'Server' }));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));

    await waitFor(() => {
      expect(posted).toHaveLength(2);
    });
    expect(posted[1]?.body.cluster).toBe('staging');
    expect(parseIcon(posted[1]?.body.icon)).toEqual({ ...drawn, style: 'kt-server' });
  }, 20_000);

  it('does not draw again for a cluster reset to its default icon', async () => {
    const { posted } = mounted({ prod: { icon: stored }, staging: { icon: stored }, edge: { icon: stored } });
    await waitFor(() => {
      expect(tiles()).toHaveLength(3);
    });
    fireEvent.contextMenu(tile('staging'));
    await screen.findByRole('dialog', { name: /staging/ });
    fireEvent.click(screen.getByRole('button', { name: 'Reset icon' }));
    await waitFor(() => {
      expect(posted).toHaveLength(1);
    });
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).toBeNull();
    });
    await new Promise((resolve) => setTimeout(resolve, 100));
    expect(posted).toEqual([{ path: '/api/preferences/rail/nameplate', body: { cluster: 'staging', colour: '', icon: RESET_ICON } }]);
  });

  it('does not draw an icon after a reload for a cluster reset earlier', async () => {
    resetFirstIcons();
    const { posted } = mounted({ prod: { icon: stored }, staging: { icon: RESET_ICON }, edge: { icon: stored } });
    await waitFor(() => {
      expect(tiles()).toHaveLength(3);
    });
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(posted).toEqual([]);
  });
});
