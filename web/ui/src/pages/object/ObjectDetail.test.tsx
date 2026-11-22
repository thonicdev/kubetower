import { cleanup, fireEvent, waitFor, type RenderResult } from '@testing-library/react';
import type { ReactNode } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DetailPage } from '../../api';
import { mountApp, renderWithClient, stubFetch, type StubReply } from '../../testing/harness';
import type { ObjectIdentity } from './address';
import { ObjectDetail } from './ObjectDetail';

/**
 * The object panel, at panel altitude.
 *
 * The flagship assertion is the same one the list page carries and for the same
 * reason: **no two of the states render the same**. A single object has no
 * `empty` arm - a single thing that is not there is
 * `absent` - so the set here is `ok`, the four failures, `pending`, and a
 * problem response, which is not one of them at all.
 *
 * `pending` against `unreadable` is the pairing that matters. `unreadable` says
 * a read happened and its answer could not be made sense of: a fault, standing,
 * somebody has to act. `pending` says no read has happened: nothing to fix, an
 * answer is coming. A user told the first goes looking for a cause that
 * does not exist.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

/**
 * A router around the panel, because `Remedy` links to the health page.
 *
 * Not scaffolding for its own sake: the remedy for `absent` and `unreachable` is
 * *"go and look at the console's own health"*, and a `<Link>` outside a router
 * throws. In the application the panel is inside one - the Shell is a route
 * element and the Drawer is inside the Shell - so a bare render would be testing
 * a mounting this product never performs.
 */
function renderPanel(ui: ReactNode): RenderResult {
  const router = createMemoryRouter(
    [
      { path: '/c/:cluster/:kind', element: ui },
      { path: '/health', element: <p>the health page</p> },
    ],
    { initialEntries: ['/c/prod/pods'] },
  );
  return renderWithClient(<RouterProvider router={router} />);
}

const identity: ObjectIdentity = {
  cluster: 'prod',
  kind: 'pods',
  namespace: 'shop-prod',
  name: 'storefront-web',
};

const PATH = '/api/c/prod/r/pods/shop-prod/storefront-web';

const page: DetailPage = {
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
      fields: [
        { label: 'Status', value: 'Running', prose: false, pill: false },
        { label: 'Node', value: 'node-1', prose: false, pill: false },
      ],
    },
  ],
};

const ok: StubReply = { body: { state: 'ok', data: page } };

function failed(state: string, code: string, message: string, extra: Record<string, unknown> = {}): StubReply {
  return { body: { state, reason: { code, message, ...extra } } };
}

const unreachable = failed(
  'unreachable',
  'cluster_unreachable',
  'The API server at 10.0.0.1:6443 did not answer within 5s.',
  { retryable: true, scope: { cluster: 'prod', resource: 'pods', name: 'storefront-web' } },
);
const forbidden = failed('forbidden', 'verb_denied', 'pods is forbidden for this account.', {
  scope: { cluster: 'prod', namespace: 'shop-prod', resource: 'pods', verb: 'get' },
});
const absent = failed('absent', 'no_such_kind', 'This cluster serves no kind called pods.');
const unreadable = failed('unreadable', 'undecodable', 'The object could not be decoded.');
const pending = failed('pending', 'not_probed_yet', 'This cluster has not been probed yet.', {
  retryable: true,
});
const problem: StubReply = {
  status: 404,
  contentType: 'application/problem+json',
  body: { status: 404, title: 'Not Found', code: 'not_found', detail: 'no such cluster' },
};

async function drawnFor(reply: StubReply): Promise<string> {
  stubFetch({ [PATH]: reply });
  const view = renderPanel(<ObjectDetail identity={identity} chrome="drawer" />);
  await waitFor(() => {
    expect(document.querySelector('[aria-busy="true"]')).toBeNull();
  });
  const text = view.container.textContent ?? '';
  cleanup();
  vi.unstubAllGlobals();
  return text;
}

describe('the states an object can be in, drawn', () => {
  it('draws no two of them the same', async () => {
    const drawn = new Map<string, string>();
    drawn.set('ok', await drawnFor(ok));
    drawn.set('unreachable', await drawnFor(unreachable));
    drawn.set('forbidden', await drawnFor(forbidden));
    drawn.set('absent', await drawnFor(absent));
    drawn.set('unreadable', await drawnFor(unreadable));
    drawn.set('pending', await drawnFor(pending));
    drawn.set('problem', await drawnFor(problem));

    const seen = new Map<string, string>();
    for (const [state, text] of drawn) {
      const already = seen.get(text);
      expect(already, `${state} renders exactly as ${already ?? ''}`).toBeUndefined();
      seen.set(text, state);
    }
    expect(seen.size).toBe(7);
  });

  it('says which object it is about even when the object could not be read', async () => {
    // The panel was opened from a row, so the user already knows what they
    // clicked; a notice with no name on it reads as a fault in the console.
    const text = await drawnFor(unreachable);
    expect(text).toContain('storefront-web');
  });

  it('offers a retry on a failure that says another attempt could differ, and never on pending', async () => {
    stubFetch({ [PATH]: unreachable });
    renderPanel(<ObjectDetail identity={identity} chrome="drawer" />);
    await waitFor(() => {
      expect(document.body.textContent ?? '').toContain('did not answer');
    });
    expect(document.body.textContent ?? '').toMatch(/try/i);
    cleanup();
    vi.unstubAllGlobals();

    stubFetch({ [PATH]: pending });
    renderPanel(<ObjectDetail identity={identity} chrome="drawer" />);
    await waitFor(() => {
      expect(document.body.textContent ?? '').toContain('not been probed');
    });
    // `retryable: true` is on that reason deliberately - the server still sends
    // it - and the state is what decides. Pressing anything cannot make the
    // console's own probe loop arrive sooner.
    expect(document.querySelector('[data-state="pending"] button')).toBeNull();
  });
});

describe('what the panel draws', () => {
  it('draws each section under its own heading', async () => {
    stubFetch({ [PATH]: ok });
    const view = renderPanel(<ObjectDetail identity={identity} chrome="drawer" />);
    await waitFor(() => {
      expect(view.container.querySelector('[data-section="summary"]')).not.toBeNull();
    });
    const section = view.container.querySelector('[data-section="summary"]');
    expect(section?.textContent).toContain('Status');
    expect(section?.textContent).toContain('Running');
  });

  it('draws a section that could not be read under its own heading, beside the ones that could', async () => {
    // An empty answer and a failed one are drawn differently, at the width of a
    // block. A drawer whose one section was
    // refused must not look like a drawer whose object has nothing to say, and
    // what was read has to stay on screen beside what was not. The server sends
    // one section today; the arm is written for the contract, which allows any.
    stubFetch({
      [PATH]: {
        body: {
          state: 'ok',
          data: {
            ...page,
            sections: [
              ...page.sections,
              {
                key: 'scheduling',
                label: 'Scheduling',
                state: 'forbidden',
                reason: { code: 'read_denied', message: 'nodes is forbidden for this account.' },
              },
            ],
          },
        },
      },
    });
    const view = renderPanel(<ObjectDetail identity={identity} chrome="drawer" />);
    await waitFor(() => {
      expect(view.container.querySelector('[data-section="scheduling"]')).not.toBeNull();
    });

    const refused = view.container.querySelector('[data-section="scheduling"]');
    expect(refused?.textContent).toContain('Scheduling');
    expect(refused?.textContent).toContain('forbidden for this account');
    // And the section that did answer is still drawn.
    expect(view.container.querySelector('[data-section="summary"]')?.textContent).toContain('Running');
  });

  it('draws what a Secret’s details say about it, and has no arm for a held-back value', async () => {
    // Details does not draw a Secret's keys a second time beside the Data tab.
    // The server sends what is *about* the object - type, key count,
    // immutability - as ordinary facts, and this panel has no rendering for a
    // held-back key: whatever arrives is drawn as a fact.
    stubFetch({
      [PATH]: {
        body: {
          state: 'ok',
          data: {
            ...page,
            kind: 'secrets',
            title: 'Secret',
            sections: [
              {
                key: 'summary',
                label: 'Summary',
                state: 'ok',
                fields: [
                  { label: 'Type', value: 'kubernetes.io/tls', prose: false, pill: false },
                  { label: 'Keys', value: '2', prose: false, pill: false },
                  { label: 'Immutable', value: 'yes', tone: 'warn', prose: false, pill: true },
                ],
              },
            ],
          },
        },
      },
    });
    const view = renderPanel(<ObjectDetail identity={identity} chrome="drawer" />);
    await waitFor(() => {
      expect(view.container.querySelector('[data-section="summary"]')).not.toBeNull();
    });
    const text = view.container.textContent ?? '';
    expect(text).toContain('kubernetes.io/tls');
    expect(text).toContain('Keys');
    expect(text).toContain('Immutable');
    expect(view.container.querySelectorAll('[data-section]')).toHaveLength(1);
    expect(text).not.toContain('held back');
    expect(view.container.querySelector('[data-masked]')).toBeNull();
  });

  it('draws the close control in a drawer and not on a page', async () => {
    stubFetch({ [PATH]: ok });
    const drawer = renderPanel(
      <ObjectDetail identity={identity} chrome="drawer" onClose={() => undefined} />,
    );
    await waitFor(() => {
      expect(drawer.container.querySelector('[data-object-chrome="drawer"]')).not.toBeNull();
    });
    expect(drawer.container.querySelector('button[aria-label="Close"]')).not.toBeNull();
    cleanup();

    const pageView = renderPanel(<ObjectDetail identity={identity} chrome="page" />);
    await waitFor(() => {
      expect(pageView.container.querySelector('[data-object-chrome="page"]')).not.toBeNull();
    });
    // Nothing to close: the browser's own Back is what leaves a page.
    expect(pageView.container.querySelector('button[aria-label="Close"]')).toBeNull();
  });
});

/**
 * **Which address, not which order.** The panel also reads `/api/capabilities`
 * for its terminal row, so the first request is not necessarily the object's.
 * What these are about is the *shape* of the address, and a test that fails when
 * an unrelated read is added is measuring the wrong thing.
 */
describe('which address is asked', () => {
  it('asks the namespaced form for a namespaced object', async () => {
    const asked: string[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      asked.push(typeof input === 'string' ? input : String(input));
      return Promise.resolve(
        new Response(JSON.stringify({ state: 'ok', data: page }), {
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    });
    renderPanel(<ObjectDetail identity={identity} chrome="drawer" />);
    await waitFor(() => {
      expect(asked.length).toBeGreaterThan(0);
    });
    await waitFor(() => {
      expect(asked).toContain('/api/c/prod/r/pods/shop-prod/storefront-web');
    });
    // And the other spelling was never asked for: a namespace defaulted in, or
    // the name read as one, would report a node's name as a namespace.
    expect(asked.some((one) => one.includes('/r/pods/storefront-web'))).toBe(false);
  });

  it('asks the cluster-scoped form for a cluster-scoped object, with the name as the one trailing segment', async () => {
    // The other spelling - `/r/nodes/node-1/` or a namespace defaulted in - is
    // how a node's name would be reported as a namespace.
    const asked: string[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      asked.push(typeof input === 'string' ? input : String(input));
      return Promise.resolve(
        new Response(JSON.stringify({ state: 'ok', data: { ...page, kind: 'nodes', name: 'node-1' } }), {
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    });
    renderPanel(
      <ObjectDetail identity={{ cluster: 'prod', kind: 'nodes', name: 'node-1' }} chrome="page" />,
    );
    await waitFor(() => {
      expect(asked.length).toBeGreaterThan(0);
    });
    await waitFor(() => {
      expect(asked).toContain('/api/c/prod/r/nodes/node-1');
    });
    expect(asked.some((one) => one.includes('/r/nodes/node-1/'))).toBe(false);
  });
});

/**
 * The control row, and which side each kind of control is on.
 *
 * ## Why this is asserted at all
 *
 * If the row were drawn by two arms - one for an object with verbs, one for an
 * object without - they could disagree about where the dock controls live: a
 * spacer pushes Logs and Shell right on a Pod, and on a Namespace there is
 * nothing to push them, so they would sit at the left. Shell would be on the
 * right of one object and on the left of the next and nothing would fail,
 * unless a test read the row's children in order.
 *
 * **That is the test**: the order of the direct children, on an object with
 * verbs and on an object with none. A single arm draws the row, so the two cases
 * cannot drift apart without this failing - which is the property, rather than
 * the left-to-right order itself.
 *
 * The whole application is mounted rather than the panel alone. `Terminals` and
 * `LogsButton` both answer `null` where there is no dock above them, so the
 * panel on its own draws a row with the verbs in it and nothing else - which is
 * exactly the arrangement this is about.
 */
describe('the drawer’s control row', () => {
  const capabilities: StubReply = {
    body: {
      mode: 'desktop',
      localShell: true,
      podExec: true,
      nodeShell: true,
      assistant: true,
      portForward: true,
      readOnly: { writesRefused: false, detailWritesRefused: false, measured: true },
    },
  };

  function appShell(extra: Record<string, StubReply>): Record<string, StubReply> {
    return {
      '/api/session': {
        body: {
          identity: 'alice',
          idleExpiresAt: '2999-01-01T00:00:00Z',
          absoluteExpiresAt: '2999-01-01T00:00:00Z',
        },
      },
      '/api/clusters': { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } },
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/capabilities': capabilities,
      ...extra,
    };
  }

  function detail(extra: Record<string, unknown>): StubReply {
    return { body: { state: 'ok', data: { ...page, tabs: [], sections: [], ...extra } } };
  }

  /** The row itself, by the name the panel puts on it for exactly this. */
  function controlRow(): HTMLElement {
    const found = document.querySelector('[data-object-controls]');
    if (found === null) throw new Error('no control row');
    return found as HTMLElement;
  }

  /** Each direct child of the row, by the name it answers to - the two groups
   *  carry an `aria-label`, Logs is its own word, and the spacer is nothing. */
  function rowOrder(): string[] {
    return [...controlRow().children].map(
      (one) => one.getAttribute('aria-label') ?? one.textContent ?? '',
    );
  }

  /**
   * The row, once the **object** has answered and not merely the capabilities.
   *
   * The two reads are separate and land in the wrong order for a naive wait:
   * `Terminals` is drawn as soon as `/api/capabilities` answers, which, as
   * the pages are chunks of their own, is before the object's own read
   * has even been sent. A test that waited for the terminals alone read the row
   * while the verbs were still out, found two children, and would have passed
   * against a panel that never drew a verb at all.
   *
   * So the wait is on the payload's own word for the kind - `ConfigMap` rather
   * than the route's `configmaps` - which only the object read can supply.
   */
  async function shown(kindLabel: string): Promise<void> {
    await waitFor(() => {
      expect(document.querySelector('[aria-label="Terminals"]')).not.toBeNull();
      expect(document.querySelector('[data-object-chrome]')?.textContent).toContain(kindLabel);
    });
  }

  it('draws the watching controls first and the verbs last on an object that has verbs', async () => {
    stubFetch(
      appShell({
        '/api/c/prod/r/pods/shop-prod/storefront-web': detail({
          containers: [{ name: 'web', image: 'ghcr.io/shop/web:9', init: false }],
          actions: [{ verb: 'delete', label: 'Delete', permitted: true, destructive: true }],
        }),
      }),
    );
    mountApp('/c/prod/pods/shop-prod/storefront-web');
    await shown('Pod');

    // Logs, then the terminals, then the spacer that pushes the destructive end
    // away, then the verbs.
    expect(rowOrder()).toEqual(['Logs', 'Terminals', '', 'Actions']);
  });

  it('does not draw Logs when the object read is refused', async () => {
    stubFetch(appShell({ '/api/c/prod/r/pods/shop-prod/storefront-web': forbidden }));
    mountApp('/c/prod/pods/shop-prod/storefront-web');
    await waitFor(() => {
      expect(document.body.textContent).toContain('forbidden for this account');
    });
    expect(controlRow().textContent).not.toContain('Logs');
  });

  /** A spacing class's step - `gap-x-1` is 1 - for the first prefix the element
   *  carries without a variant, or `undefined` where it carries none of them.
   *  Variants are refused separately, by `variantSpacing`. */
  function step(element: Element, ...prefixes: string[]): number | undefined {
    const classes = element.className.split(/\s+/);
    for (const prefix of prefixes) {
      const found = classes.find(
        (one) => one.startsWith(prefix) && /^\d+(\.\d+)?$/.test(one.slice(prefix.length)),
      );
      if (found !== undefined) return Number(found.slice(prefix.length));
    }
    return undefined;
  }

  /** A class that puts horizontal space between or beside flex items: a gap
   *  between columns (`gap-y-*` is the other axis and is not one), `space-x-*`,
   *  or a margin on either side, logical (`ms-`, `me-`) included. */
  const HORIZONTAL_SPACING = /^-?(gap-x|gap(?!-y)|space-x|m[xlrse]?)-/;

  /** The utility a class applies, without its variants or its `!`:
   *  `sm:gap-x-3` is `gap-x-3`. */
  function utility(one: string): string {
    return one.slice(one.lastIndexOf(':') + 1).replace(/^!|!$/g, '');
  }

  /** The element's horizontal spacing classes that apply only under a variant -
   *  a breakpoint, a state, a container query. `step` reads the unprefixed
   *  class, so one of these would change a gap where this test does not look. */
  function variantSpacing(element: Element): string[] {
    return element.className
      .split(/\s+/)
      .filter((one) => one.includes(':') && HORIZONTAL_SPACING.test(utility(one)));
  }

  /** The element's unprefixed classes matching `pattern`. */
  function carries(element: Element, pattern: RegExp): string[] {
    return element.className.split(/\s+/).filter((one) => pattern.test(one));
  }

  /*
    Logs, Shell and Claude are spaced as one set. Logs is a child of the row and Shell and Claude are children of the
    `Terminals` group, so **two containers' gaps decide one spacing**, and this
    pins them to the same step. jsdom lays nothing out, so it is read off the
    classes; `measure/arm.spec.ts` measures the pixels.

    The second half pins what the fix must not move: the verbs' distance from
    the set, which is the row's gap plus the spacer's own margin on each side.
  */
  it('spaces Logs, Shell and Claude as one set, and keeps the verbs as far away as they were', async () => {
    stubFetch(
      appShell({
        '/api/c/prod/r/pods/shop-prod/storefront-web': detail({
          containers: [{ name: 'web', image: 'ghcr.io/shop/web:9', init: false }],
          actions: [{ verb: 'delete', label: 'Delete', permitted: true, destructive: true }],
        }),
      }),
    );
    mountApp('/c/prod/pods/shop-prod/storefront-web');
    await shown('Pod');

    const row = controlRow();
    const group = row.querySelector(':scope > [aria-label="Terminals"]');
    const logs = [...row.children].find((one) => one.textContent === 'Logs');
    const shellButton = [...(group?.querySelectorAll('button') ?? [])].find(
      (one) => one.textContent === 'Shell',
    );
    const claude = group?.querySelector('[aria-label="Claude"]') ?? null;
    const spacer = row.querySelector(':scope > [data-arm-spacer]');
    if (
      group === null ||
      logs === undefined ||
      shellButton === undefined ||
      claude === null ||
      spacer === null
    ) {
      throw new Error('the row is not Logs, the Terminals group with Shell and Claude, and the spacer');
    }

    const shellWrapper = shellButton.parentElement ?? shellButton;

    // No gap or margin that only applies at a breakpoint or in a state, on any
    // box between the three controls or beside the spacer: `step` reads the
    // unprefixed class, and `sm:gap-x-3` would split the set again above
    // `sm` with the rest of this test still passing.
    for (const one of [row, group, spacer, logs, shellWrapper, shellButton, claude]) {
      expect(variantSpacing(one), `<${one.tagName.toLowerCase()} class="${one.className}">`).toEqual([]);
    }

    // Logs to Shell is the row's gap; Shell to Claude is the group's.
    expect(step(row, 'gap-x-', 'gap-')).toBe(step(group, 'gap-x-', 'gap-'));
    // And nothing else adds to one of the two: no margin on a control or on a
    // box around one, and no `space-x-*` on either container.
    for (const one of [logs, group, shellWrapper, shellButton, claude]) {
      expect(carries(one, /^-?m[xlrse]?-/)).toEqual([]);
    }
    for (const one of [row, group]) {
      expect(carries(one, /^-?space-x-/)).toEqual([]);
    }

    // The verbs' side: 12px. The spacer's margin is
    // `mx-*` and nothing one-sided, so the arithmetic below is all of it.
    expect(carries(spacer, /^-?m[lrse]?-/)).toEqual([]);
    expect((step(row, 'gap-x-', 'gap-') ?? 0) + (step(spacer, 'mx-') ?? 0)).toBe(3);
  });

  it('draws them in the same place on an object that has no verb at all', async () => {
    stubFetch(
      appShell({
        '/api/c/prod/r/configmaps/shop-prod/app-config': detail({
          kind: 'configmaps',
          name: 'app-config',
          title: 'ConfigMap',
          actions: [],
        }),
      }),
    );
    mountApp('/c/prod/configmaps/shop-prod/app-config');
    await shown('ConfigMap');

    // No Logs on a ConfigMap and no verbs, so the terminals are the row - and
    // they are still its first child, which is the whole point:
    // Shell in one place rather than two.
    expect(rowOrder()).toEqual(['Terminals']);
    expect(rowOrder().indexOf('Terminals')).toBe(0);
  });
});

/**
 * A node names the pods on it, by sending the user to the list that already
 * draws them.
 *
 * The shape is a jump rather than a tab, because a tab is a contract change and
 * the Pods list already carries CPU, Memory and Node beside every row. So the
 * whole of this is a link, and the whole of the link is that it is **exact** -
 * `?filter=Node:<name>`, the filter control's parameter, and not
 * `?search=<name>`, which on a cluster with `ip-10-0-1-7` and `ip-10-0-1-70` in
 * it silently answers about both.
 *
 * No count on it, and that is a decision rather than an omission: the number of
 * pods running on a node is a read this page does not make. What it has is
 * *allocatable of capacity* - how many it may hold - and printing that beside
 * the word "pods" would answer a different question in the same sentence.
 */
describe('a node names the pods on it', () => {
  const nodeIdentity: ObjectIdentity = { cluster: 'prod', kind: 'nodes', name: 'ip-10-0-1-7' };
  const NODE_PATH = '/api/c/prod/r/nodes/ip-10-0-1-7';
  const nodePage: DetailPage = {
    kind: 'nodes',
    name: 'ip-10-0-1-7',
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

  it('links into the pods list narrowed to this node, exactly and in every namespace', async () => {
    stubFetch({ [NODE_PATH]: { body: { state: 'ok', data: nodePage } } });
    const view = renderPanel(<ObjectDetail identity={nodeIdentity} chrome="drawer" />);
    await waitFor(() => {
      expect(view.container.querySelector('[data-section="node-pods"]')).not.toBeNull();
    });

    const link = view.container.querySelector('[data-section="node-pods"] a');
    // The Node filter and not a bare search. A link the console composed is
    // not a query the user can be expected to audit, so it cannot be a
    // prefix match - and it names the column by its label, which a column
    // added before Node does not move, where its key would.
    expect(link?.getAttribute('href')).toBe('/c/prod/pods?filter=Node%3Aip-10-0-1-7');
    expect(link?.getAttribute('href')).not.toContain('search=');
    // No `namespace` parameter: the pods on a node are the pods on a node, and
    // a jump that inherited a namespace would under-report without saying so.
    expect(link?.getAttribute('href')).not.toContain('namespace=');
    // No count. Nothing on this payload knows how many pods are running here -
    // `110 allocatable of 110` is how many it may hold - and a figure taken
    // from that would be wrong on every node in use.
    expect(view.container.querySelector('[data-section="node-pods"]')?.textContent).not.toMatch(/\d/);

    cleanup();
    vi.unstubAllGlobals();
  });

  it('puts it on a node and on nothing else', async () => {
    stubFetch({ [PATH]: ok });
    const view = renderPanel(<ObjectDetail identity={identity} chrome="drawer" />);
    await waitFor(() => {
      expect(view.container.querySelector('[data-section="summary"]')).not.toBeNull();
    });
    // A pod has a Node of its own and it is a fact, not a list to jump into.
    expect(view.container.querySelector('[data-section="node-pods"]')).toBeNull();

    cleanup();
    vi.unstubAllGlobals();
  });
});

describe('the tab the empty address means', () => {
  /*
    **It means Details, on every kind, and that is a constant rather than a
    computation.**

    Details is encoded as the *absence* of `tab`, to keep a pod's link short. If
    the landing tab depended on the payload (say a ConfigMap or a Secret landing
    on Data), clicking Details would clear the parameter and the panel would read
    the empty address as "the landing tab", which would be Data: Details would be
    unreachable on exactly those kinds. The keys are drawn inside Details, so
    there is no second landing tab to be wrong about. These tests assert that
    property, and that a click made before the payload has arrived is not moved
    off the tab it asked for, because the answer never depends on the payload.

    **These tests hold the router themselves**, because half of what is being
    asserted is the address: that Details on a ConfigMap writes nothing, and that
    the ordinary case still writes nothing either.
  */
  const cm: ObjectIdentity = {
    cluster: 'prod',
    kind: 'configmaps',
    namespace: 'shop-prod',
    name: 'app-config',
  };
  const CM_PATH = '/api/c/prod/r/configmaps/shop-prod/app-config';

  const podPath = '/api/c/prod/r/pods/shop-prod/storefront-web';
  const podWithManifest: StubReply = {
    body: {
      state: 'ok',
      data: {
        ...page,
        tabs: [{ key: 'manifest', label: 'Manifest', endpoint: '/api/c/prod/manifest', lazy: true }],
      },
    },
  };

  function configMapReply(): StubReply {
    return {
      body: {
        state: 'ok',
        data: {
          kind: 'configmaps',
          namespace: 'shop-prod',
          name: 'app-config',
          title: 'ConfigMap',
          // The address the keys are fetched from, which is what a ConfigMap
          // carries rather than a Data tab.
          data: '/api/c/prod/data?kind=configmaps',
          // What a real server still declares on one. It is here so the tests
          // below have something to wait for that only the *payload* can
          // produce: Details is in the strip from the first frame, so waiting
          // on it would resolve against the loading render and assert nothing.
          tabs: [{ key: 'manifest', label: 'Manifest', endpoint: '/api/c/prod/manifest', lazy: true }],
          sections: [],
        },
      },
    };
  }

  /** The same router `renderPanel` builds, handed back so a test can read the
   *  address it ended on. */
  function renderAddressable(ui: ReactNode): {
    view: RenderResult;
    search: () => string;
  } {
    const router = createMemoryRouter(
      [
        { path: '/c/:cluster/:kind', element: ui },
        { path: '/health', element: <p>the health page</p> },
      ],
      { initialEntries: ['/c/prod/pods'] },
    );
    const view = renderWithClient(<RouterProvider router={router} />);
    return { view, search: () => router.state.location.search };
  }

  function selected(view: RenderResult, name: string): string | null {
    return view.getByRole('tab', { name }).getAttribute('aria-selected');
  }

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it('is Details on a ConfigMap, which declares no tab of its own at all', async () => {
    stubFetch({ [CM_PATH]: configMapReply() });
    const { view, search } = renderAddressable(<ObjectDetail identity={cm} chrome="drawer" />);

    // Wait for the payload rather than asserting on the loading frame: the
    // question is what the *answered* panel lands on.
    await waitFor(() => {
      expect(view.getByRole('tab', { name: 'Details' }).getAttribute('aria-selected')).toBe('true');
    });
    // The strip has no Data in it - the keys are a section of the panel above,
    // not a destination.
    expect(view.queryByRole('tab', { name: 'Data' })).toBeNull();
    // And it landed there without being sent: the address is still bare.
    expect(search()).toBe('');
  });

  it('leaves a Details click on a ConfigMap with a bare address', async () => {
    stubFetch({ [CM_PATH]: configMapReply() });
    const { view, search } = renderAddressable(<ObjectDetail identity={cm} chrome="drawer" />);

    await waitFor(() => {
      expect(view.getByRole('tab', { name: 'Manifest' })).toBeTruthy();
    });
    fireEvent.click(view.getByRole('tab', { name: 'Details' }));

    await waitFor(() => {
      expect(
        selected(view, 'Details'),
        'clicking Details moved the reader somewhere else - the empty address no longer means Details',
      ).toBe('true');
    });
    // Cleared, not named: the empty address means Details on every kind now, so
    // a ConfigMap's link is as short as a pod's.
    expect(search()).toBe('');
  });

  it('holds a Details click made before the object had answered', async () => {
    stubFetch({ [CM_PATH]: configMapReply() });
    const { view } = renderAddressable(<ObjectDetail identity={cm} chrome="drawer" />);

    // No await: the strip is drawn and the payload is not in yet. The arriving
    // payload must not move the reader off the tab they asked for, and it does
    // not, because the landing tab never depends on it.
    fireEvent.click(view.getByRole('tab', { name: 'Details' }));

    await waitFor(() => {
      expect(view.getByRole('tab', { name: 'Manifest' })).toBeTruthy();
    });
    await waitFor(() => {
      expect(
        selected(view, 'Details'),
        'the payload arrived and moved the reader off the tab they had asked for',
      ).toBe('true');
    });
  });

  it('leaves a pod that returns to Details with a bare address', async () => {
    stubFetch({ [podPath]: podWithManifest });
    const { view, search } = renderAddressable(
      <ObjectDetail identity={identity} chrome="drawer" />,
    );

    const manifest = await view.findByRole('tab', { name: 'Manifest' });
    fireEvent.click(manifest);
    await waitFor(() => {
      expect(search()).toBe('?tab=manifest');
    });

    fireEvent.click(view.getByRole('tab', { name: 'Details' }));
    await waitFor(() => {
      expect(selected(view, 'Details')).toBe('true');
    });
    // The whole reason Details is the empty address: a pod's link stays
    // short, and coming back to Details takes the parameter off again.
    expect(search()).toBe('');
  });
});
