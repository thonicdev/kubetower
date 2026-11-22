import { QueryClientProvider } from '@tanstack/react-query';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { ExtensionInfo, ListPage, ListResult, Namespace as ApiNamespace } from '../../api';
import { NOTHING_TO_FILTER } from '../../components/FilterMenu';
import { createQueryClient } from '../../query/client';
import { resetColumnMemory } from './columnMemory';
import type { Extension, ExtensionColumn } from '../../extension';
import { ShellProvider, useShell } from '../../shell/ShellContext';
import { resetViewMemory } from '../../shell/viewMemory';
import { stubFetch, type StubReply, type StubRoutes } from '../../testing/harness';
import { settingWanted } from './extensionColumns';
import { setExtraColumnShown } from './extraColumnMemory';
import { namespaceNote } from '../../components/NamespaceMenu';
import { ResourceList } from './ResourceList';

/**
 * The list page, at page altitude.
 *
 * **Altitude is the point.** A test of a predicate is not a test of the page:
 * defects survive when the predicate is covered and its call site is not.
 * Everything below therefore goes through the route, the query, the envelope
 * narrowing and the rendered panel - not through a helper.
 *
 * The flagship assertion is the first one: **no two of the states render the
 * same**. The shortcut that looks identical from the outside (`reason.message`
 * for all four failures) passes every other test in this file.
 */

/*
  Unmount, then unstub, then forget - in that order, and all three.

  **A wiped body is not an unmounted tree.** `document.body.innerHTML = ''`
  takes the DOM away and leaves the React root, its router and its queries
  alive; the tree then answers through whatever `fetch` the *next* test stubs
  and records its address in that test's recording: a test that typed one word
  would then see requests for words it never typed, from a previous test's trees
  still mounted, and the failure looks like a load flake. `testing/setup.ts`
  calls `cleanup` after every test, which closes the gap *between* tests; this
  file also mounts several trees *within* one test, which is why `mount` and
  `mountRecording` unmount before they mount.

  **And `localStorage`, which this file writes without meaning to.** Every
  mount of `ResourceList` calls `rememberKind`, so every test here leaves
  `kubetower.view` behind, for example `{"prod":{"kind":"pods"}}`.
  `resetViewMemory()` rather than `localStorage.clear()` alone, because
  `viewMemory` keeps a module-level snapshot of what it read: clearing the
  storage and not the cache leaves the value in memory for the next test, which
  is the same leak one layer up. The `clear()` stays for anything else a future
  test writes.
*/
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  resetViewMemory();
  localStorage.clear();
});

const page: ListPage = {
  kind: 'pods',
  label: 'Pods',
  namespaced: true,
  /*
    Every column states its five facts, because `Column` requires them and
    because a fixture that left them out would be exercising a
    shape the server cannot send.

    They are facts about the **kind** and not about the cells in front of the
    renderer, which is the whole reason they live on the column:
    `api/openapi.yaml` records an address column painting the word "pending"
    before a controller writes the address, and a usage column painting em dashes
    before its readings arrive - overlapping the column by pixels and staying
    wrong until the page was next drawn. `cpu` is `gauged` here so that the
    reservation, the bar and the sizing are all under test rather than declared.
  */
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
    {
      key: 'cpu',
      label: 'CPU',
      visible: true,
      order: 2,
      sortable: true,
      align: 'end',
      secondary: false,
      gauged: true,
      copyable: false,
      prose: false,
      filterable: false,
      role: 'other',
    },
  ],
  rows: [
    {
      id: 'shop-prod/storefront-web',
      name: 'storefront-web',
      namespace: 'shop-prod',
      cells: {
        name: { text: 'storefront-web' },
        namespace: { text: 'shop-prod' },
        cpu: { text: '120m', sort: 120, gauge: { percent: 42, tone: 'ok' } },
      },
    },
  ],
  /*
    What a pods list is actually offered, and the `scope` is the server's.

    The server answers per kind and names the scope `cluster|kind`. This fixture
    carries both because a page **without** `bulk` is a different page: nil is
    how the server says a kind has no bulk verb, and the client withholds the
    checkboxes on it rather than drawing ticks that lead nowhere.

    Restart is absent deliberately: on a pod, restarting *is* deleting, so a bar
    offering both puts one request behind two words with the gentle-sounding one
    being the destructive one.
  */
  bulk: {
    scope: 'prod|pods',
    verbs: [
      { verb: 'evict', label: 'Evict', permitted: true },
      { verb: 'delete', label: 'Delete', permitted: true },
    ],
  },
};

const ok: StubReply = { body: { state: 'ok', data: page } };
const empty: StubReply = { body: { state: 'ok', data: { ...page, rows: [] } } };

function failed(
  state: string,
  code: string,
  message: string,
  extra: Record<string, unknown> = {},
): StubReply {
  return { body: { state, reason: { code, message, ...extra } } };
}

const unreachable = failed(
  'unreachable',
  'cluster_unreachable',
  'The API server at 10.0.0.1:6443 did not answer within 5s.',
  { retryable: true, scope: { cluster: 'prod', resource: 'pods' } },
);
const forbidden = failed('forbidden', 'verb_denied', 'pods is forbidden for this account.', {
  retryable: false,
  scope: { cluster: 'prod', namespace: 'shop-prod', resource: 'pods', verb: 'list' },
});
const absent = failed('absent', 'no_such_kind', 'This cluster serves no kind called pods.', {
  scope: { cluster: 'prod', resource: 'pods' },
});
const unreadable = failed('unreadable', 'undecodable', 'The list could not be decoded.', {
  scope: { cluster: 'prod', resource: 'pods' },
});

/*
  The sixth, and the one written to be confused with `unreadable`.

  `retryable: true` is set deliberately: a server that sends the flag for a
  cluster nobody has asked yet must not get a retry button out of this page: pressing it cannot make the console's own probe
  loop arrive sooner. The state decides, not the flag.
*/
const pending = failed('pending', 'not_probed_yet', 'This cluster has not been probed yet.', {
  retryable: true,
  scope: { cluster: 'prod', resource: 'pods' },
});

const problem: StubReply = {
  status: 404,
  contentType: 'application/problem+json',
  body: { status: 404, title: 'Not Found', code: 'not_found', detail: 'no such cluster' },
};

/** Comfortable, which is the store's own default and what a console nobody has
 *  touched draws. The page reads it for the row height. */
const preferences: StubReply = {
  body: { rail: { barCount: 1, bars: [], hidden: [], tiles: {} }, columns: { kinds: {} } },
};

const namespaces: StubReply = {
  body: {
    state: 'ok',
    items: [{ name: 'default' }, { name: 'kube-system' }, { name: 'shop-prod' }],
  },
};

/** The in-cluster console's capabilities. */
const SHARED: StubReply = {
  body: {
    mode: 'shared',
    localShell: false,
    podExec: true,
    nodeShell: false,
    assistant: false,
    portForward: false,
    preferences: false,
    readOnly: { writesRefused: false, detailWritesRefused: false },
  },
};

function mount(
  reply: StubReply,
  path = '/c/prod/pods',
  capabilities?: StubReply,
  extra: StubRoutes = {},
  extensions: readonly Extension[] = [],
): void {
  // Whatever this test mounted before, gone before a new stub is installed -
  // or it answers through the new one. See the `afterEach` above.
  cleanup();
  stubFetch({
    '/api/c/prod/r/pods': reply,
    '/api/preferences': preferences,
    '/api/c/prod/namespaces': namespaces,
    ...(capabilities === undefined ? {} : { '/api/capabilities': capabilities }),
    ...extra,
  });
  const router = createMemoryRouter(
    [
      { path: '/c/:cluster/:kind', element: <ResourceList /> },
      { path: '/health', element: <p>the health page</p> },
    ],
    { initialEntries: [path] },
  );
  render(
    // `ShellProvider` and no `Drawer`: the page calls `openDrawer` when a row is
    // clicked, and a provider is what makes that call legal. What the drawer
    // then *draws* is asserted at application altitude in `ObjectDrawer.test.tsx`
    // - mounting a second Drawer here would be a second place the panel is
    // rendered and a second thing to keep in step.
    <QueryClientProvider client={createQueryClient()}>
      <ShellProvider extensions={extensions}>
        <RouterProvider router={router} />
      </ShellProvider>
    </QueryClientProvider>,
  );
}

/** Wait until the page has stopped saying it does not know yet. `Pending`
 *  carries `aria-busy`, and a list still loading has not said there is nothing
 *  in it - which is a sixth thing this page draws and the easiest to confuse
 *  with `empty`. */
async function settled(): Promise<HTMLElement> {
  await waitFor(() => {
    expect(document.querySelector('[aria-busy="true"]')).toBeNull();
  });
  const panel = document.getElementById('panel');
  if (panel === null) throw new Error('no #panel');
  return panel;
}

async function drawnFor(reply: StubReply, path = '/c/prod/pods'): Promise<string> {
  mount(reply, path);
  const panel = await settled();
  const text = panel.textContent ?? '';
  cleanup();
  vi.unstubAllGlobals();
  return text;
}

describe('the six states, drawn', () => {
  it('draws no two of them the same', async () => {
    const drawn = new Map<string, string>();
    drawn.set('ok', await drawnFor(ok));
    drawn.set('empty', await drawnFor(empty));
    drawn.set('unreachable', await drawnFor(unreachable));
    drawn.set('forbidden', await drawnFor(forbidden));
    drawn.set('absent', await drawnFor(absent));
    drawn.set('unreadable', await drawnFor(unreadable));
    // The sixth, and the pairing that matters is `pending` against `unreadable`:
    // one says an answer could not be read, the other that nothing has been
    // asked. Reporting the second as the first is what the state exists to
    // stop, so it is in this matrix rather than tested on its own.
    drawn.set('pending', await drawnFor(pending));
    drawn.set('problem', await drawnFor(problem));

    const names = [...drawn.keys()];
    for (let i = 0; i < names.length; i += 1) {
      for (let j = i + 1; j < names.length; j += 1) {
        const a = names[i] as string;
        const b = names[j] as string;
        expect(
          drawn.get(a),
          `${a} and ${b} draw the same thing, which is the collapse this page exists to undo`,
        ).not.toBe(drawn.get(b));
      }
    }
  });

  it('sends the user somewhere different for each failure', async () => {
    // The four remedies, and they are what the four arms are FOR: retry, ask for
    // a role, install something, look at this console's own health. Four arms
    // with four sentences and one next step would be the same collapse wearing
    // four hats.
    for (const [state, reply] of [
      ['unreachable', unreachable],
      ['forbidden', forbidden],
      ['absent', absent],
      ['unreadable', unreadable],
    ] as const) {
      mount(reply);
      await settled();
      expect(document.querySelector(`[data-remedy="${state}"]`)).not.toBeNull();
      cleanup();
      vi.unstubAllGlobals();
    }
  });

  it('gives pending no remedy and no retry, whatever the reason claims', async () => {
    /*
      The two halves of what makes `pending` the sixth state rather than a
      synonym for one of the five.

      **No remedy.** `Remedy` answers *what does the user do next*, and its
      four sentences are four different places to go. For a cluster nobody has
      asked yet there is nowhere to go: no fault, no role to request, nothing to
      install, and this console's own health page is not where to look either.
      An arm there would be advice invented for a question that has not been put.

      **No retry**, even though this fixture sends `retryable: true`. Pressing anything cannot make
      the server's probe loop arrive sooner, and a control that does nothing is
      what this codebase refuses everywhere it comes up.
    */
    mount(pending);
    await settled();

    expect(document.querySelector('[data-remedy]')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();
    expect(screen.queryByRole('link', { name: /Cluster page/ })).toBeNull();

    // And it says the thing a failure cannot: an answer is coming on its own.
    expect(document.querySelector('[data-state="pending"]')).not.toBeNull();
    expect(document.querySelector('[data-pending-advice="wait"]')).not.toBeNull();
  });

  it('draws the whole of a refusal the server cut to a line, in every profile', async () => {
    // The sentence stops at 200 characters, and the rest of the cluster's words
    // would be nowhere. The server sends them whole as the detail, which the
    // notice draws.
    const whole =
      'deployhooks.demo.example.com is forbidden: User "system:serviceaccount:kubetower:kubetower" ' +
      'cannot list resource "deployhooks" in API group "demo.example.com" at the cluster scope';
    mount(
      failed('forbidden', 'verb_denied', `${whole.slice(0, 120)}…`, {
        detail: whole,
        scope: { cluster: 'prod', resource: 'pods' },
      }),
    );
    await settled();
    expect(document.querySelector('[data-state="forbidden"]')?.textContent).toContain(
      'in API group "demo.example.com" at the cluster scope',
    );
  });

  it('quotes the verb, resource and namespace to ask for when it is forbidden', async () => {
    mount(forbidden);
    await settled();
    expect(screen.getByText('Ask for: list on pods in namespace shop-prod.')).toBeTruthy();
  });

  it('says the fault is this console’s, and links to no Cluster page', async () => {
    // There is no Cluster page: the remedy still says where the fault is, and
    // names no page, in every profile.
    const inCluster = {
      '/api/clusters': { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } },
    };
    for (const profile of [undefined, SHARED]) {
      mount(unreadable, '/c/prod/pods', profile, profile === undefined ? {} : inCluster);
      await settled();
      await waitFor(() => {
        expect(document.querySelector('[data-remedy="unreadable"]')?.textContent).toContain(
          'a fault in this console rather than in the cluster',
        );
      });
      expect(screen.queryByRole('link', { name: /Cluster page/ })).toBeNull();
      expect(document.querySelector('a[href="/health"]')).toBeNull();
    }
  });

  it('renders the server’s own sentence rather than a guess at it', async () => {
    // `reason.code` is what anything branches on; `reason.message` is what is
    // rendered. A client that composes its own sentence has thrown away the one
    // the server wrote about this occurrence.
    mount(unreachable);
    await settled();
    expect(
      screen.getByText('The API server at 10.0.0.1:6443 did not answer within 5s.'),
    ).toBeTruthy();
  });

  it('does not offer a retry the reason says is pointless', async () => {
    // `retryable` drives a button, not a loop. A console that retries
    // `forbidden` on its own writes the same denial into the audit log every
    // few seconds.
    mount(forbidden);
    await settled();
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();

    cleanup();
    vi.unstubAllGlobals();

    mount(unreachable);
    await settled();
    expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy();
  });

  it('keeps the headings when the kind is readable and holds nothing', async () => {
    mount(empty);
    await settled();
    /*
      By the heading's own text and not by its accessible name.

      `/Name/` also matches NAMESPACE, and an anchor at the start of the
      accessible name breaks because the select-all sits in the Name heading: a
      `<th>` containing a checkbox takes the box's label into its own computed
      name, so the heading is announced as "Select every row listed Name". The
      assertion here is about a heading surviving an empty answer, so it reads
      the heading rather than its announcement.
    */
    const headings = [...document.querySelectorAll('th')].map(
      // The sort arrow shares the heading's text, and `·` is the unsorted one.
      (th) => th.textContent?.replace(/[·↑↓]/g, '').trim(),
    );
    expect(headings).toContain('Name');
    expect(headings).toContain('Namespace');
    expect(screen.getByRole('table')).toBeTruthy();
  });

  it('says which question the emptiness answers', async () => {
    // Three claims, and they are not the same claim: the filter matched
    // nothing, this namespace holds none, this kind has none here.
    mount(empty);
    await settled();
    expect(document.querySelector('[data-empty="kind"]')).not.toBeNull();
    expect(document.querySelector('[data-empty="kind"]')?.textContent).not.toContain('answered');
    cleanup();
    vi.unstubAllGlobals();

    mount(empty, '/c/prod/pods?namespace=shop-prod');
    await settled();
    expect(document.querySelector('[data-empty="namespaced"]')).not.toBeNull();
    expect(screen.getByText(/namespace shop-prod/)).toBeTruthy();
    expect(document.querySelector('[data-empty="namespaced"]')?.textContent)
      .not.toContain('answered');
    cleanup();
    vi.unstubAllGlobals();

    mount(empty, '/c/prod/pods?search=nothing-matches-this');
    await settled();
    expect(document.querySelector('[data-empty="filtered"]')).not.toBeNull();
    expect(screen.getByRole('button', { name: 'Clear the filter' })).toBeTruthy();
  });

  it('does not call a list the access review narrowed empty of the whole cluster', async () => {
    // The person may not list pods in every namespace, so
    // the rows are those of the namespaces they may. "No pods on this cluster"
    // would be a claim about namespaces they were not shown.
    const note = 'You may not list pods in every namespace, so this shows only the namespaces where you may.';
    mount({ body: { state: 'ok', data: { ...page, rows: [], accessNote: note } } });
    await settled();
    expect(document.querySelector('[data-empty="kind"]')).toBeNull();
    expect(document.querySelector('[data-empty="narrowed"]')).not.toBeNull();
    expect(document.querySelector('[data-note="access"]')?.textContent).toBe(note);
  });

  it('says a narrowed list is narrowed when it has rows too', async () => {
    const note = 'You may not list pods in every namespace, so this shows only the namespaces where you may.';
    mount({ body: { state: 'ok', data: { ...page, accessNote: note } } });
    await settled();
    expect(document.querySelector('[data-note="access"]')?.textContent).toBe(note);
    cleanup();
    vi.unstubAllGlobals();

    // And a list that is the cluster's whole answer says nothing of the kind.
    mount(ok);
    await settled();
    expect(document.querySelector('[data-note="access"]')).toBeNull();
  });

  it('prefers the server’s sentence on an empty answer over its own', async () => {
    // `Reason` is permitted on a successful arm precisely so that "nothing in
    // this namespace" can say which namespace.
    mount({
      body: {
        state: 'ok',
        reason: { code: 'empty_scope', message: 'The namespace shop-prod does not exist.' },
        data: { ...page, rows: [] },
      },
    });
    await settled();
    expect(screen.getByText('The namespace shop-prod does not exist.')).toBeTruthy();
    expect(document.querySelector('[data-empty="server"]')).not.toBeNull();
  });
});

describe('the panel', () => {
  it('puts the grid where theme.css can reach it', async () => {
    // `#panel > .table-scroll > .kt-grid` is how the grid's fixed layout is
    // scoped to the list. One wrapper between any two of them and the whole
    // rule silently stops applying - to a table that still looks like a table.
    mount(ok);
    await settled();
    expect(document.querySelector('#panel > .table-scroll > table.kt-grid')).not.toBeNull();
  });

  describe('a list of 250 rows, every one listed and the visible ones drawn', () => {
    const many: StubReply = {
      body: {
        state: 'ok',
        data: {
          ...page,
          rows: Array.from({ length: 250 }, (_, i) => {
            const name = `pod-${String(i).padStart(3, '0')}`;
            return {
              id: `shop-prod/${name}`,
              name,
              namespace: 'shop-prod',
              cells: {
                name: { text: name },
                namespace: { text: 'shop-prod' },
                cpu: { text: `${String(i)}m`, sort: i },
              },
            };
          }),
        },
      },
    };
    const ticks = (): number =>
      document.querySelectorAll('tbody input[type="checkbox"]').length;

    it('counts all 250, draws a bounded number and offers no Show more', async () => {
      mount(many);
      await settled();
      expect(screen.getByText('250 listed')).toBeTruthy();
      expect(ticks()).toBeGreaterThan(0);
      expect(ticks()).toBeLessThan(100);
      expect(document.querySelector('[data-more]')).toBeNull();
      expect(document.querySelector('tbody tr[data-spacer]')).not.toBeNull();
    });

    it('select-all picks every listed row, not only the drawn ones, and says how many', async () => {
      mount(many);
      await settled();
      fireEvent.click(screen.getByRole('checkbox', { name: 'Select every row listed' }));
      expect(screen.getByText('250 selected')).toBeTruthy();
      expect(
        [...document.querySelectorAll<HTMLInputElement>('tbody input[type="checkbox"]')].every((box) => box.checked),
      ).toBe(true);
    });

    it('sorts the whole list before the cut', async () => {
      mount(many);
      await settled();
      const first = (): string | undefined =>
        [...document.querySelectorAll('tbody tr:not([data-spacer])')]
          .map((row) => /pod-\d{3}/.exec(row.textContent ?? '')?.[0])
          .find((name) => name !== undefined);
      fireEvent.click(within(document.querySelector('thead') as HTMLElement).getByRole('button', { name: /^CPU/ }));
      const one = first();
      // Whichever way the first press sorts, the first row drawn is an end of
      // the whole 250, never an end of a slice of it.
      expect(['pod-000', 'pod-249']).toContain(one);
      fireEvent.click(within(document.querySelector('thead') as HTMLElement).getByRole('button', { name: /^CPU/ }));
      expect(['pod-000', 'pod-249']).toContain(first());
      expect(first()).not.toBe(one);
    });
  });

  it('draws the rows it was sent', async () => {
    mount(ok);
    await settled();
    expect(screen.getByText('storefront-web')).toBeTruthy();
    expect(screen.getByText('1 listed')).toBeTruthy();
  });
});

/**
 * The column's side of sizing.
 *
 * *A width derived from data that has not arrived is a guess.* The precise
 * statement matters, because the loose one produces a different bug: sizing from
 * the cells **in front of the renderer** is correct. What is forbidden is a width that outlives the answer it was computed
 * from, and the remedy is declarative - the column says what its cells cost,
 * whether or not this render has one to measure.
 *
 * Every assertion below is against the **declaration**, never against the cell.
 */
describe('the column declares what its cells cost', () => {
  it('reserves the gauge’s room on a render with no gauge in it', async () => {
    // The defect, reproduced from its own end: a Nodes list paints em dashes
    // where its usage bars will be, three columns are sized for an em dash, and
    // the readings arrive two seconds later overflowing their column. Here CPU is `gauged` and this answer's rows
    // carry nothing at all - so a column sized from the cells would be at the
    // floor, and one sized from the declaration is not.
    mount({ body: { state: 'ok', data: { ...page, rows: [] } } });
    await settled();

    const cols = Array.from(document.querySelectorAll('colgroup col'));
    // Name, namespace, cpu. **No column for the ticks** - they live in the name's
    // own cell.
    const cpu = cols[2];
    const namespace = cols[1];
    const width = (col: Element | undefined): number =>
      Number.parseInt((col as HTMLElement | undefined)?.style.width ?? '0', 10);

    expect(width(cpu)).toBeGreaterThan(width(namespace));
    // The track and a reading beside it. The exact figure is arithmetic and is
    // allowed to move; that it is a function of the declaration and not of an
    // empty answer is the property under test.
    expect(width(cpu)).toBeGreaterThanOrEqual(120);
  });

  it('draws the bar from the server’s percentage and nothing else', async () => {
    mount(ok);
    await settled();

    // `Gauge` is `{percent, tone}` and arrives already divided, because "the
    // arithmetic that decides what the whole is stays where the sources are
    // rather than being reassembled per client". Nothing here divides anything.
    const gauge = document.querySelector('[data-gauge]');
    expect(gauge?.getAttribute('data-gauge')).toBe('42');

    // And the reading is still the server's text, not a figure this page
    // composed: `Reading.text` is "computed once on the server so that a table
    // cannot report three different units for the same quantity". It is the hover
    // rather than the line; where the server
    // sent no `hint`, the cell's own text is that hover, so it is never lost.
    expect(document.querySelector('[data-measure]')?.getAttribute('title')).toBe('120m');
    expect(screen.getByText('120m').className).toContain('sr-only');
  });

  it('draws the bar and its percentage on one line, and used / total on hover', async () => {
    /*
      The console's one anatomy for a measured quantity
      (`components/Reading.tsx`, parts 1 to 4). A person reads a usage cell
      for pressure; the absolute figure (`1163m`) is the one nobody needs at a
      glance, so the cell draws the bar and its rounded percentage beside it, and
      the quantity moves to the hover as `used / total` - written by the server
      into `Cell.hint`, because `Cell.gauge` arrives already divided and carries
      no whole.

      What is asserted is the property and not the pixels, because jsdom lays
      nothing out: one line (no `flex-col`), the track first and growing, the
      percentage after it in a box of fixed width, and no quantity drawn.
      `measure/table.spec.ts` check 2 measures the geometry.
    */
    mount({
      body: {
        state: 'ok',
        data: {
          ...page,
          rows: [
            {
              ...page.rows[0],
              cells: {
                ...page.rows[0]?.cells,
                cpu: {
                  text: '1163m',
                  sort: 1163,
                  hint: '1.2 / 2 cores allocatable',
                  gauge: { percent: 58.2, tone: 'ok' },
                },
              },
            },
          ],
        },
      },
    });
    await settled();

    const line = document.querySelector('[data-gauge] [data-measure]');
    expect(line?.className).not.toContain('flex-col');
    expect(line?.className).toContain('items-center');
    expect(line?.className).toContain('justify-end');

    // The track first, taking the rest of the line.
    const track = line?.querySelector('[data-gauged]');
    expect(track?.getAttribute('data-percent')).toBe('58');
    expect(track?.parentElement?.className).toContain('flex-1');
    expect(track?.parentElement).toBe(line?.firstElementChild);

    // The percentage beside it, rounded, in tabular numerals and a fixed box -
    // so the bar is the cell less a constant whatever the digits are.
    const share = line?.querySelector('[data-share]');
    expect(share?.textContent).toBe('58%');
    expect(share?.className).toContain('w-10');
    expect(share?.className).toContain('tabular-nums');
    expect(share?.previousElementSibling).toBe(track?.parentElement);

    // No quantity drawn: what is on screen, screen-reader text aside, is the
    // percentage and nothing else.
    const drawn = line?.cloneNode(true) as HTMLElement;
    drawn.querySelectorAll('.sr-only').forEach((node) => {
      node.remove();
    });
    expect(drawn.textContent).toBe('58%');

    // And the quantity on hover, as the server wrote it - on the reading and on
    // its `<td>` alike - and read out after the percentage.
    expect(line?.getAttribute('title')).toBe('1.2 / 2 cores allocatable');
    expect(line?.closest('td')?.getAttribute('title')).toBe('1.2 / 2 cores allocatable');
    expect(line?.querySelector('.sr-only')?.textContent).toBe('1.2 / 2 cores allocatable');
  });

  it('draws no track at all where there is nothing to measure', async () => {
    /*
      theme.css, from a defect report: "an unmeasured cell draws an em dash and
      NO track at all. An empty track reads as 0%, which reads as an idle
      machine, which is the opposite of the truth."

      The column is still `gauged` - that is a fact about the kind - and this
      one cell is `absent`, which is a fact about this object. The first must not
      draw a bar for the second.
    */
    mount({
      body: {
        state: 'ok',
        data: {
          ...page,
          rows: [
            {
              ...page.rows[0],
              cells: {
                ...page.rows[0]?.cells,
                cpu: {
                  text: '',
                  state: 'absent',
                  reason: { code: 'not_installed', message: 'No metrics source on this cluster.' },
                },
              },
            },
          ],
        },
      },
    });
    await settled();

    expect(document.querySelector('[data-gauge]')).toBeNull();
    expect(document.querySelector('[data-gauged]')).toBeNull();
    // No percentage either: `0%` would read as an idle machine just as an empty
    // track does.
    expect(document.querySelector('[data-share]')).toBeNull();
    expect(document.querySelector('[data-cell-state="absent"]')?.textContent).toContain('—');
  });

  it('styles the name column by its declared role and never by its position', async () => {
    // `listData` carries `NameAt` because "after a drag, `first` is whatever was
    // dragged there, and without this an age would be styled as a name on every
    // row". So the role is asserted on the cell that declares it, and the
    // fixture's first column is deliberately not the only one with a role.
    mount(ok);
    await settled();

    const name = document.querySelector('[data-role="name"]');
    expect(name?.textContent).toBe('storefront-web');
    expect(name?.className).toContain('font-medium');
  });

  it('offers Reset only where something was actually arranged', async () => {
    /*
      `secondary` is what separates *the user hid this* from *it is off by
      default*, and `api/openapi.yaml` says in as many words that the difference
      "decides what resetting means".

      Reading `!column.visible` would make any kind declaring one secondary
      column offer Reset for ever - on a fresh install, with nothing stored,
      doing nothing when pressed. A control that is always offered is one
      that stops being read.
    */
    const withSecondary = (visible: boolean): StubReply => ({
      body: {
        state: 'ok',
        data: {
          ...page,
          columns: [
            ...page.columns,
            {
              key: 'node',
              label: 'Node',
              visible,
              order: 3,
              sortable: true,
              secondary: true,
              gauged: false,
              copyable: false,
              prose: false,
              filterable: false,
              role: 'other',
            },
          ],
        },
      },
    });

    /*
      **Reset's condition is not reconstructed from the payload.** Inferring it
      from a column whose visibility differs from its default, or one carrying a
      stored width, cannot see a re-ordering, because the server sends only the
      order it resolved, so a table arranged by dragging would offer no way back;
      and it reads a *default* as an arrangement, because a secondary column that
      is off is off exactly as its descriptor asks. The condition is the store's
      own answer to "is anything saved for this kind", which is
      `columnsCustomised`.

      So the two arms below are the two values of that member, and the first is
      the subtle one: a secondary column drawn **visible** on a console where
      nothing is stored is a kind whose descriptor changed, not a user who
      arranged something.
    */
    mount(withSecondary(false));
    await settled();
    fireEvent.click(screen.getByRole('button', { name: 'More table options' }));
    expect(screen.queryByRole('button', { name: 'Reset columns' })).toBeNull();
    // And the user is told it is a default rather than a decision of theirs.
    fireEvent.click(screen.getByRole('button', { name: 'Columns' }));
    expect(screen.getByText('off by default')).toBeTruthy();

    cleanup();
    vi.unstubAllGlobals();

    mount(withSecondary(true));
    await settled();
    fireEvent.click(screen.getByRole('button', { name: 'More table options' }));
    expect(screen.queryByRole('button', { name: 'Reset columns' })).toBeNull();

    cleanup();
    vi.unstubAllGlobals();

    // The server says something is stored. That is the whole condition.
    mount({
      body: { state: 'ok', data: { ...page, columnsCustomised: true } },
    });
    await settled();
    fireEvent.click(screen.getByRole('button', { name: 'More table options' }));
    expect(screen.getByRole('button', { name: 'Reset columns' })).toBeTruthy();
  });

  it('draws a locked column as an explained fact rather than a toggle that refuses', async () => {
    // `api/openapi.yaml` on `lockedNote`: "the column menu draws it as a fact
    // rather than as a control that refuses; a toggle that does nothing is worse
    // than an explained absence."
    mount({
      body: {
        state: 'ok',
        data: {
          ...page,
          columns: page.columns.map((column) =>
            column.key === 'name'
              ? { ...column, lockedNote: 'Every row is addressed by its name.' }
              : column,
          ),
        },
      },
    });
    await settled();
    fireEvent.click(screen.getByRole('button', { name: 'Columns' }));

    expect(screen.getByText('Every row is addressed by its name.')).toBeTruthy();
    expect(screen.getByRole('checkbox', { name: 'Name' })).toHaveProperty('disabled', true);
  });

  it('makes a namespace cell re-ask this list for that namespace', async () => {
    /*
      `api/openapi.yaml` on `role: 'namespace'`: "a table of four hundred pods
      across thirty namespaces is read by finding one interesting row and then
      wanting its neighbors."

      It re-asks rather than navigating, because the namespace is a parameter
      this page already honors and there is no route that takes one.
    */
    stubFetch({ '/api/c/prod/r/pods': ok });
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ResourceList /> }], {
      initialEntries: ['/c/prod/pods'],
    });
    render(
      <QueryClientProvider client={createQueryClient()}>
        <ShellProvider extensions={[]}>
          <RouterProvider router={router} />
        </ShellProvider>
      </QueryClientProvider>,
    );
    await waitFor(() => {
      expect(document.querySelector('[aria-busy="true"]')).toBeNull();
    });

    fireEvent.click(screen.getByRole('button', { name: 'Show only shop-prod' }));

    await waitFor(() => {
      expect(router.state.location.search).toBe('?namespace=shop-prod');
    });
  });

  it('draws the namespace control in the color of the name, not as a link', async () => {
    /*
      Deliberately not styled as a link at rest: every row has one, so forty
      underlined blue words down a column would read as the most important thing
      on a screen whose subject is the names beside them.

      Asserted on the class list because the look is the whole of the defect
      and jsdom computes no color. Tailwind finds utilities by scanning source
      text, so the class list is the stylesheet's contract too.
    */
    stubFetch({ '/api/c/prod/r/pods': ok });
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ResourceList /> }], {
      initialEntries: ['/c/prod/pods'],
    });
    render(
      <QueryClientProvider client={createQueryClient()}>
        <ShellProvider extensions={[]}>
          <RouterProvider router={router} />
        </ShellProvider>
      </QueryClientProvider>,
    );
    await waitFor(() => {
      expect(document.querySelector('[aria-busy="true"]')).toBeNull();
    });

    const control = screen.getByRole('button', { name: 'Show only shop-prod' });
    const classes = control.className.split(/\s+/);
    expect(classes).toContain('text-fg');
    expect(classes).toContain('cursor-pointer');
    expect(classes).not.toContain('text-accent');
    expect(classes.some((name) => name.includes('underline'))).toBe(false);
    // Discoverable on hover and reachable by keyboard, so plain text does not
    // become hidden text.
    expect(classes.some((name) => name.startsWith('hover:bg-'))).toBe(true);
    expect(classes.some((name) => name.startsWith('focus-visible:outline'))).toBe(true);
    // And still the same control: it declines the row's click and narrows.
    expect(control.hasAttribute('data-no-open')).toBe(true);
    fireEvent.click(control);
    await waitFor(() => {
      expect(router.state.location.search).toBe('?namespace=shop-prod');
    });
  });
});

describe('the filter', () => {
  it('writes the address bar under the parameter’s own name, by replacing', async () => {
    // Two decisions: the parameter key is the one the page reads and not a
    // hard-coded `q`, because at least one box filters under a different name;
    // and it replaces rather than pushes, or Back becomes a backspace key. The
    // box is named `filter` and writes two keys - a plain word is `search`, and `filterText.ts` says so.
    stubFetch({ '/api/c/prod/r/pods': ok });
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ResourceList /> }], {
      initialEntries: ['/c/prod/pods'],
    });
    render(
      <QueryClientProvider client={createQueryClient()}>
        <ShellProvider extensions={[]}>
          <RouterProvider router={router} />
        </ShellProvider>
      </QueryClientProvider>,
    );
    await settled();

    // One box, and it is the one labeled Filter; the label is the way in.
    const box = screen.getByRole('searchbox', { name: /^Filter/ });
    expect(box.getAttribute('name')).toBe('filter');

    fireEvent.change(box, { target: { value: 'web' } });

    await waitFor(() => {
      expect(router.state.location.search).toBe('?search=web');
    });
    // One entry, not four: the address was replaced on each keystroke.
    expect(router.state.historyAction).toBe('REPLACE');
  });
});

/**
 * One box that does everything, at page altitude.
 *
 * A user should not have to know which layer they are typing at, so a label
 * selector and a search share one box. `filterText.test.ts` is the grammar's own table; what is here is the page's
 * side of it - what reaches the request, what the address carries, and that a
 * link carrying the two parameters still lands on the same rows.
 */
describe('the one filter box', () => {
  /** The page at one address, with every request it makes recorded as
   *  `pathname?query` so the test can say which parameter reached the wire. */
  function mountRecording(path: string): {
    asked: string[];
    router: ReturnType<typeof createMemoryRouter>;
  } {
    // Nothing of an earlier mount may still be running when this stub goes in,
    // or its requests are recorded here as if this page had made them.
    cleanup();
    const asked: string[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      const at = new URL(raw, 'http://x');
      asked.push(at.pathname + at.search);
      const body =
        at.pathname === '/api/preferences'
          ? preferences.body
          : at.pathname.endsWith('/namespaces')
            ? namespaces.body
            : ok.body;
      return Promise.resolve(
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    });
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ResourceList /> }], {
      initialEntries: [path],
    });
    render(
      <QueryClientProvider client={createQueryClient()}>
        <ShellProvider extensions={[]}>
          <RouterProvider router={router} />
        </ShellProvider>
      </QueryClientProvider>,
    );
    return { asked, router };
  }

  /**
   * The list requests recorded since `from`, as parsed query strings.
   *
   * **`from` is the point the test's own act begins.** Reading *the first*
   * recorded request that carried the parameter under test would be a test a
   * neighbor can satisfy: a request from another test's tree, arriving before
   * this page had typed anything. Slicing from the keystroke excludes
   * anything recorded before it; asserting over *every* request in the slice,
   * rather than the first that matches, excludes anything after it.
   */
  function listRequests(asked: readonly string[], from = 0): URLSearchParams[] {
    return asked
      .slice(from)
      .filter((one) => one.startsWith('/api/c/prod/r/pods'))
      .map((one) => new URL(one, 'http://x').searchParams);
  }

  it('is rendered once per list', async () => {
    mount(ok);
    await settled();
    expect(screen.getAllByRole('searchbox')).toHaveLength(1);
    /*
      And says what it takes: a search over every column, or a label selector.
      Asserted verbatim on purpose: a column's value is chosen from the filter
      control beside the box, so the wording is the decision, and a placeholder
      that promised a `column:value` grammar would teach a search that finds
      nothing.
    */
    expect(screen.getByRole('searchbox').getAttribute('placeholder')).toBe(
      'search all columns, or app=api',
    );
  });

  it('sends a typed label selector to the cluster, and not as a search', async () => {
    const { asked, router } = mountRecording('/c/prod/pods');
    await settled();

    const typed = asked.length;
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'app=api' } });

    // The address, at once and under the parameter's own name.
    await waitFor(() => {
      expect(router.state.location.search).toBe('?selector=app%3Dapi');
    });
    // The request, once the typing has settled: what the network tab shows.
    await waitFor(() => {
      expect(asked.some((one) => one.includes('?selector=app%3Dapi'))).toBe(true);
    });
    // Every narrowed request this keystroke caused, not the first one that
    // happens to carry a selector - see `listRequests`.
    const narrowed = listRequests(asked, typed).filter((query) => query.has('selector'));
    expect(narrowed.length).toBeGreaterThan(0);
    for (const query of narrowed) {
      expect(query.get('selector')).toBe('app=api');
      expect(query.has('search')).toBe(false);
    }
  });

  it('sends a typed !key to the cluster as a selector', async () => {
    const { asked, router } = mountRecording('/c/prod/pods');
    await settled();

    const typed = asked.length;
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: '!network' } });

    await waitFor(() => {
      expect(router.state.location.search).toBe('?selector=%21network');
    });
    await waitFor(() => {
      expect(listRequests(asked, typed).some((query) => query.get('selector') === '!network')).toBe(true);
    });
    for (const query of listRequests(asked, typed)) expect(query.has('search')).toBe(false);
  });

  it('lists the syntaxes it reads in its tooltip', async () => {
    mount(ok);
    await settled();
    const title = screen.getByRole('searchbox').getAttribute('title') ?? '';
    for (const syntax of ['label keys and values', 'key=value', 'key!=value', 'key in (a,b)', 'key notin (a,b)', '!key']) {
      expect(title).toContain(syntax);
    }
  });

  it('says in one line that a selector the server refused is invalid', async () => {
    mount(
      {
        status: 400,
        body: {
          status: 400,
          title: 'Bad request',
          code: 'invalid_selector',
          detail: 'the label selector "a=b=c" is not valid',
        },
      },
      '/c/prod/pods?selector=a%3Db%3Dc',
    );
    const line = await screen.findByText('Invalid label selector');
    expect(line.getAttribute('title')).toContain('a=b=c');
    // One line, not the generic failure panel, and no rows.
    expect(screen.queryByText(/The request failed/)).toBeNull();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('draws another bad request beside a valid selector as the failure it is', async () => {
    // A search over its bound with a selector set: the selector is fine, and
    // saying otherwise would send the person to fix what is not broken.
    mount(
      { status: 400, body: { status: 400, title: 'Bad request', code: 'invalid_request', detail: 'selector or search is too long' } },
      '/c/prod/pods?selector=app%3Dx&search=x',
    );
    await screen.findByText(/The request failed \(400\)/);
    expect(screen.queryByText('Invalid label selector')).toBeNull();
  });

  it('sends a typed word as a search, and does not ask the cluster for less', async () => {
    const { asked, router } = mountRecording('/c/prod/pods');
    await settled();

    const typed = asked.length;
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'nginx' } });

    await waitFor(() => {
      expect(router.state.location.search).toBe('?search=nginx');
    });
    await waitFor(() => {
      expect(asked.some((one) => one.includes('?search=nginx'))).toBe(true);
    });
    // `search` looks through what came back; the selector is what asks the
    // cluster, and a plain word must never become one. Every searched request
    // recorded after the keystroke, because the first one recorded at all was
    // somebody else's - see `listRequests`.
    const searched = listRequests(asked, typed).filter((query) => query.has('search'));
    expect(searched.length).toBeGreaterThan(0);
    for (const query of searched) {
      expect(query.get('search')).toBe('nginx');
      expect(query.has('selector')).toBe(false);
    }
  });

  it('writes both parameters from one string, in one write', async () => {
    /*
      Two `useUrlParameter` setters called in one handler lose one of the two:
      `useSearchParams`' functional form is closed over the render's own
      params, so the second write starts from an address the first has not
      reached. One string, two parameters, one navigation.
    */
    const { router } = mountRecording('/c/prod/pods');
    await settled();

    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'app=api nginx' } });

    await waitFor(() => {
      const query = new URLSearchParams(router.state.location.search);
      expect(query.get('selector')).toBe('app=api');
      expect(query.get('search')).toBe('nginx');
    });
    expect(router.state.historyAction).toBe('REPLACE');
  });

  it('keeps the typed text verbatim while the address is its own decomposition', async () => {
    /*
      The box is a *composition* of the two parameters, and a controlled input
      that is re-derived from them on every keystroke cannot hold a trailing
      space - `app=api ` decomposes to `app=api`, composes back to `app=api`,
      and the user can never start the second word. `useUrlParameter`'s
      third decision - the value is verbatim - has to survive the split.
    */
    const { router } = mountRecording('/c/prod/pods');
    await settled();
    const box = screen.getByRole<HTMLInputElement>('searchbox');

    fireEvent.change(box, { target: { value: 'app=api ' } });
    await waitFor(() => {
      expect(router.state.location.search).toBe('?selector=app%3Dapi');
    });
    expect(box.value).toBe('app=api ');

    // And the order the user typed is the order the box keeps, even
    // though the address composes the selector first.
    fireEvent.change(box, { target: { value: 'nginx app=api' } });
    await waitFor(() => {
      expect(new URLSearchParams(router.state.location.search).get('search')).toBe('nginx');
    });
    expect(box.value).toBe('nginx app=api');
  });

  it('composes a pasted address carrying both parameters into the one box, on the same rows', async () => {
    const { asked } = mountRecording(
      '/c/prod/pods?selector=app%3Dapi%2Ctier%21%3Dcache&search=nginx',
    );
    await settled();

    // One box, holding both, selector first.
    const box = screen.getByRole<HTMLInputElement>('searchbox');
    expect(box.value).toBe('app=api,tier!=cache nginx');

    // The same request the address always made - and there is no other, which is the claim rather than "the first one said so".
    const requests = listRequests(asked);
    expect(requests.length).toBeGreaterThan(0);
    for (const query of requests) {
      expect(query.get('selector')).toBe('app=api,tier!=cache');
      expect(query.get('search')).toBe('nginx');
    }
    // And the same rows.
    expect(screen.getByText('storefront-web')).toBeTruthy();
  });

  it('empties the box and both parameters when the empty state clears the filter', async () => {
    stubFetch({
      '/api/c/prod/r/pods': empty,
      '/api/preferences': preferences,
      '/api/c/prod/namespaces': namespaces,
    });
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ResourceList /> }], {
      initialEntries: ['/c/prod/pods?selector=app%3Dapi&search=nginx'],
    });
    render(
      <QueryClientProvider client={createQueryClient()}>
        <ShellProvider extensions={[]}>
          <RouterProvider router={router} />
        </ShellProvider>
      </QueryClientProvider>,
    );
    await settled();

    // The sentence quotes the box, not one half of it.
    expect(screen.getByText(/matched “app=api nginx”/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Clear the filter' }));

    await waitFor(() => {
      expect(router.state.location.search).toBe('');
    });
    // The address moved under the box from outside, so the box follows it.
    expect(screen.getByRole<HTMLInputElement>('searchbox').value).toBe('');
  });
});

/**
 * What a cell draws once the server sends what the column declares.
 *
 * Every case states the behavior it pins on the case rather than leaving it to
 * the title.
 */
describe('the cell', () => {
  /** One row, one column, with whatever this case needs put on the cell. */
  function oneCell(cell: Record<string, unknown>, column: Record<string, unknown> = {}): StubReply {
    return {
      body: {
        state: 'ok',
        data: {
          ...page,
          columns: [{ ...page.columns[0], key: 'status', label: 'Status', ...column }],
          rows: [{ id: 'shop/a', name: 'a', namespace: 'shop', cells: { status: cell } }],
        },
      },
    };
  }

  it('draws a graded value as a chip and an ungraded one as text', async () => {
    /*
      A graded cell (Ready, Restarts, Containers, Status) is wrapped in a pill,
      not drawn as bare colored text.

      The second half is what a "chip everything" fix breaks: `toneOf` sends ok,
      warn, bad or nothing, so a cell with no tone was never graded and a chip
      round it would put a border on every name in the table.
    */
    mount(oneCell({ text: 'CrashLoopBackOff', tone: 'bad' }));
    await settled();

    const pill = document.querySelector('[data-pill]');
    expect(pill?.getAttribute('data-pill')).toBe('bad');
    expect(pill?.textContent).toBe('CrashLoopBackOff');
    expect(pill?.className).toContain('rounded-full');
    expect(pill?.className).toContain('border');

    cleanup();
    vi.unstubAllGlobals();

    mount(oneCell({ text: 'CrashLoopBackOff' }));
    const plain = await settled();
    /*
      Asserted positively, on the element the plain path draws, and not merely as
      "no [data-pill]". That negative passes against the defect it exists to
      catch: chipping every cell writes
      `data-pill={undefined}`, React omits an undefined attribute, and the
      selector then finds nothing while every string in the table wears a border.
    */
    const drawn = plain.querySelector('td [data-role]');
    expect(drawn?.textContent).toBe('CrashLoopBackOff');
    expect(drawn?.className).not.toContain('rounded-full');
  });

  it('gives an ungraded value the neutral chip where its column grades something', async () => {
    /*
      An ungraded value rendering as bare text beside graded ones reads as a
      rendering fault. A Restarts column where `7` wears a chip and
      `0` does not looks like the console failed to draw the `0`.

      The rule is about a **column**, not "chip every string in the table". So
      two rows in one column here, one graded and
      one not, and the test above keeps the other half honest: a column with no
      grade anywhere still draws plain text.
    */
    mount({
      body: {
        state: 'ok',
        data: {
          ...page,
          columns: [{ ...page.columns[0], key: 'restarts', label: 'Restarts' }],
          rows: [
            { id: 'shop/a', name: 'a', namespace: 'shop', cells: { restarts: { text: '7', tone: 'warn' } } },
            { id: 'shop/b', name: 'b', namespace: 'shop', cells: { restarts: { text: '0' } } },
          ],
        },
      },
    });
    await settled();

    const pills = [...document.querySelectorAll('[data-pill]')];
    expect(pills.map((node) => node.getAttribute('data-pill'))).toEqual(['warn', 'muted']);
    expect(pills.map((node) => node.textContent)).toEqual(['7', '0']);
    // The same shape on both, which is the whole point of one vocabulary.
    for (const pill of pills) expect(pill.className).toContain('rounded-full');
  });

  it('draws the addressable pieces as exactly the cell text', async () => {
    /*
      The pieces concatenate to `Cell.text`, separators included, and the client
      writes none of its own. Putting a comma back on the client would leave a
      data-level test green — the filter searches one string and the user reads
      another, "and one comma is exactly the size of drift nobody reports and
      nobody can then explain".
    */
    const text = 'a.example.com, b.example.com +2 more';
    mount(
      oneCell({
        text,
        parts: [
          { text: 'a.example.com', href: 'https://a.example.com' },
          { text: ', ' },
          { text: 'b.example.com', href: 'http://b.example.com' },
          { text: ' +2 more' },
        ],
      }),
    );
    const panel = await settled();

    const drawn = panel.querySelector('[data-parts]');
    expect(drawn?.textContent).toBe(text);

    // And it leaves the console safely.
    const first = panel.querySelector('a[data-cell-addr]');
    expect(first?.getAttribute('href')).toBe('https://a.example.com');
    expect(first?.getAttribute('target')).toBe('_blank');
    expect(first?.getAttribute('rel')).toBe('noopener noreferrer');

    // A piece with no href is text beside the ones that have one: `, ` is not an
    // address. Two anchors for four pieces.
    expect(panel.querySelectorAll('a[data-cell-addr]').length).toBe(2);
  });

  it('draws a cell address that is not http or https as text, never as a link', async () => {
    // The host is whoever wrote the Ingress. A scheme the console does
    // not open is drawn as the text it is.
    mount(
      oneCell({
        text: 'a.example.com, javascript:alert(1)',
        parts: [
          { text: 'a.example.com', href: 'https://a.example.com' },
          { text: ', ' },
          { text: 'javascript:alert(1)', href: 'javascript:alert(1)' },
        ],
      }),
    );
    const panel = await settled();
    expect(panel.querySelector('[data-parts]')?.textContent).toBe('a.example.com, javascript:alert(1)');
    const anchors = [...panel.querySelectorAll('a[data-cell-addr]')];
    expect(anchors.map((a) => a.getAttribute('href'))).toEqual(['https://a.example.com']);
  });

  it('puts the cell hint on the cell, and never an empty one', async () => {
    /*
      The node table states a taint *count*, because a column wide enough for two taints pushes
      CPU and Memory off the screen: the number says whether to look, the hover
      says what it is. Without it "the column degrades to a bare number with no
      way to learn what it counts, and nothing fails".

      The second half: a cell with nothing to add must not get
      `title=""`, which hovers as a blank box.
    */
    mount(oneCell({ text: '2', hint: 'node.kubernetes.io/unreachable:NoExecute' }));
    const panel = await settled();

    const cells = Array.from(panel.querySelectorAll('tbody td'));
    const hinted = cells.find((td) => td.getAttribute('title') !== null);
    expect(hinted?.getAttribute('title')).toBe('node.kubernetes.io/unreachable:NoExecute');
    expect(hinted?.textContent).toBe('2');

    cleanup();
    vi.unstubAllGlobals();

    mount(oneCell({ text: '2' }));
    const plain = await settled();
    for (const td of plain.querySelectorAll('tbody td')) {
      expect(td.getAttribute('title')).toBeNull();
    }
  });

  it('offers the whole of a value its column will clip', async () => {
    /*
      A value too long for its column must not spill into the next one.
      `theme.css` clips rather than painting over the neighbor; the
      28-character hover arm is what makes the clipped half recoverable. It offers a hover rather than truncating — the drawn text
      is the whole string either way.
    */
    const long = 'ip-10-0-1-9.eu-west-1.compute.internal';
    expect(long.length).toBeGreaterThan(28);
    mount(oneCell({ text: long }));
    const panel = await settled();

    const cell = Array.from(panel.querySelectorAll('tbody td')).find(
      (td) => td.textContent === long,
    );
    expect(cell?.getAttribute('title')).toBe(long);

    cleanup();
    vi.unstubAllGlobals();

    // Twenty-eight exactly is not longer than twenty-eight.
    const short = 'x'.repeat(28);
    mount(oneCell({ text: short }));
    const tight = await settled();
    const untouched = Array.from(tight.querySelectorAll('tbody td')).find(
      (td) => td.textContent === short,
    );
    expect(untouched?.getAttribute('title')).toBeNull();
  });

  it('offers a copy control where there is something to copy, and not otherwise', async () => {
    /*
      The control is drawn where `Cell.copy` is set. `Column.copyable` is what
      reserves the width, as a function of the declaration, and it is not what
      decides whether a button appears.

      The second arm is the one that matters: `copyableText` sets `Copy` from the
      same string it sets `Text` from, so an absent value is an em dash with no
      `Copy` beside it. Falling back to the text would put a button on every em dash in
      an address column, offering to copy an em dash.
    */
    mount(oneCell({ text: '10.0.0.7', copy: '10.0.0.7' }, { copyable: true }));
    const panel = await settled();
    expect(panel.querySelector('button[aria-label^="Copy this"]')).not.toBeNull();

    cleanup();
    vi.unstubAllGlobals();

    mount(oneCell({ text: '—' }, { copyable: true }));
    const dashed = await settled();
    expect(dashed.querySelector('button[aria-label^="Copy this"]')).toBeNull();
  });
});


/**
 * What the page says about the *selection* rather than about the rows.
 *
 * Members the server sends about the selection, drawn on the panel.
 */
describe('the page', () => {
  it('says a namespace does not exist even while it is drawing rows', async () => {
    /*
      The placement is the whole point: it is a fact about the selection, not about the rows, and **it has to appear even when there
      are rows** — a selection of three where one does not exist gives a short
      table that looks complete. Drawing it in the empty state instead would be
      correct on the case nobody needs it and absent on the case that matters.

      An empty answer and a failed answer are drawn differently, and this is the
      same case for the namespace control: the namespace box can be typed into, so it can be told a name that is not
      one, and "No Pods in namespace X. The cluster answered; there are none
      here" is then a confident claim about what a namespace contains, made about
      something that is not a namespace.

      The sentence is the server's: `namespaceUnknownNote` words it differently
      depending on whether the cluster's namespace list was read, was kept from
      before it stopped answering, or could not be had at all.
    */
    const note = 'There is no namespace called shp on this cluster.';
    mount({
      body: { state: 'ok', data: { ...page, unknownNamespace: note } },
    });
    await settled();

    expect(screen.getByText(note)).toBeTruthy();
    // With rows on screen, which is the half an empty-state arm cannot do.
    expect(screen.getByText('storefront-web')).toBeTruthy();
  });

  it('withholds the checkboxes where the kind offers no bulk verb', async () => {
    /*
      `bulkAffordance` returns nil where `verbsFor` is empty, and that nil is
      load-bearing: it is what keeps checkboxes off a table where ticking a row
      would lead nowhere.

      Drawn as an absent column and not as a disabled box: a control that refuses
      is worse than one that was never offered, which is the same argument
      `lockedNote` is drawn under.
    */
    // `bulk` absent, which is how the server says "this kind offers none" -
    // `bulkAffordance` returns nil rather than an empty verb list.
    const withoutBulk: ListPage = { ...page };
    delete withoutBulk.bulk;
    mount({ body: { state: 'ok', data: withoutBulk } });
    const panel = await settled();

    expect(panel.querySelectorAll('input[type="checkbox"]').length).toBe(0);
    // The columns are the same either way: the ticks never were one, so
    // withholding them changes the count of nothing.
    expect(panel.querySelectorAll('colgroup col').length).toBe(page.columns.length);
    expect(panel.querySelectorAll('thead th').length).toBe(page.columns.length);

    cleanup();
    vi.unstubAllGlobals();

    // The pods list does offer them, so this is a withholding and not an absence
    // - and the table it draws has exactly the same columns.
    mount(ok);
    const pods = await settled();
    expect(pods.querySelectorAll('input[type="checkbox"]').length).toBeGreaterThan(0);
    expect(pods.querySelectorAll('colgroup col').length).toBe(page.columns.length);
  });

  /**
   * Tick a row, then make the server answer again — with the scope the same, and
   * with the scope changed. One route, one component, one selection throughout.
   *
   * The pair is the test. Either half alone proves nothing: a selection that
   * survives could be one nothing ever prunes, and a selection that is dropped
   * could be one any second answer empties. Together they are the rule.
   */
  async function tickThenReadAgain(secondScope: string): Promise<boolean> {
    const answerFor = (search: string) => ({
      state: 'ok',
      data: {
        ...page,
        // The two answers must be told apart on screen, or the wait below cannot
        // know the second has landed: the rows are deliberately identical, so
        // that nothing here can be explained by pruning.
        label: search === '' ? 'Pods' : 'Pods, filtered',
        bulk: { ...page.bulk, scope: search === '' ? 'prod|pods' : secondScope },
      },
    });
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      const at = new URL(url, 'http://x');
      const body =
        at.pathname === '/api/preferences' ? preferences.body : answerFor(at.searchParams.get('search') ?? '');
      return Promise.resolve(
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    });
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ResourceList /> }], {
      initialEntries: ['/c/prod/pods'],
    });
    render(
      <QueryClientProvider client={createQueryClient()}>
        {/* `ShellProvider` because the page calls `openDrawer` on a row click.
            Added when unit 2's drawer and unit 3's column controls met: nine
            tests here mounted without it and every one died on `useShell
            outside <ShellProvider>` before reaching its assertion. */}
        <ShellProvider extensions={[]}>
          <RouterProvider router={router} />
        </ShellProvider>
      </QueryClientProvider>,
    );
    await settled();

    fireEvent.click(screen.getByRole('checkbox', { name: 'Select shop-prod/storefront-web' }));
    expect(screen.getByText('1 selected')).toBeTruthy();

    // A second read of the same list, through the filter, so the row is still
    // listed and pruning has nothing of its own to do.
    fireEvent.change(screen.getByRole('searchbox', { name: /^Filter/ }), {
      target: { value: 'storefront' },
    });
    await waitFor(() => {
      expect(screen.getByRole('heading', { name: 'Pods, filtered' })).toBeTruthy();
    });
    // Still listed, so pruning has nothing of its own to do.
    expect(screen.getByText('storefront-web')).toBeTruthy();
    const survived = screen.queryByText('1 selected') !== null;
    cleanup();
    vi.unstubAllGlobals();
    return survived;
  }

  it('keeps a selection while the server keeps naming the same scope', async () => {
    // A filter narrows the view of one set and is not a scope change, which is
    // why a filter that hides a ticked row does not untick it.
    expect(await tickThenReadAgain('prod|pods')).toBe(true);
  });

  it('discards a selection when the server names a different scope', async () => {
    /*
      `api/openapi.yaml` on `BulkAffordance.scope`: the **server** names it,
      because a client composing it from the route "would not notice a namespace
      selection changing under it". It is a safety property rather than a cache
      key — a set of pod names carried across to another cluster's node list is a
      set of objects that do not exist, and worse, might.

      The route does not move in this test: a scope composed from `cluster` and
      `kind` could not fail it, because nothing observable changes. That is the
      point.
    */
    expect(await tickThenReadAgain('prod|pods@other-selection')).toBe(false);
  });
});

describe('row height', () => {
  /** The panel, with the console's density set either way. */
  function withDensity(density: 'comfortable' | 'compact'): void {
    stubFetch({
      '/api/c/prod/r/pods': ok,
      '/api/preferences': {
        body: { rail: { barCount: 1, bars: [], hidden: [], tiles: {} }, columns: { kinds: {}, density } },
      },
    });
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ResourceList /> }], {
      initialEntries: ['/c/prod/pods'],
    });
    render(
      <QueryClientProvider client={createQueryClient()}>
        {/* `ShellProvider` because the page calls `openDrawer` on a row click.
            Added when unit 2's drawer and unit 3's column controls met: nine
            tests here mounted without it and every one died on `useShell
            outside <ShellProvider>` before reaching its assertion. */}
        <ShellProvider extensions={[]}>
          <RouterProvider router={router} />
        </ShellProvider>
      </QueryClientProvider>,
    );
  }

  it('offers the toggle whether or not anything has been arranged', async () => {
    /*
      Compact rows is always drawn, unlike Reset columns beside it, and the
      asymmetry is the point: the toggle is a preference and there is nothing to
      undo, and **a preference that only appears once you have already changed
      it is one nobody finds**.
    */
    withDensity('comfortable');
    await settled();
    fireEvent.click(screen.getByRole('button', { name: 'More table options' }));

    const toggle = screen.getByRole('button', { name: 'Compact rows' });
    expect(toggle.getAttribute('aria-pressed')).toBe('false');
    // Nothing is stored for this kind, so there is nothing to put back.
    expect(screen.queryByRole('button', { name: 'Reset columns' })).toBeNull();
  });

  it('draws shorter rows when the console says compact, and says so on the control', async () => {
    withDensity('compact');
    const panel = await settled();

    // The state is on the table, so a row's height is a function of the console's
    // preference and not of anything this page decided.
    expect(panel.querySelector('table')?.getAttribute('data-density')).toBe('compact');
    /*
      The second cell, not the first. The first is the select-all column's, whose
      padding is written at a different expression in `DataGrid` — reading it
      would pass against a defect that left every data cell at the comfortable
      height, because the one cell it looked at would be the one still being
      shortened correctly.
    */
    const cell = panel.querySelectorAll('tbody td')[1];
    expect(cell?.className).toContain('py-0.5');
    expect(cell?.className).toContain('leading-tight');

    fireEvent.click(screen.getByRole('button', { name: 'More table options' }));
    // aria-pressed and not only a tick: a mark in a menu is invisible to a reader.
    expect(screen.getByRole('button', { name: 'Compact rows' }).getAttribute('aria-pressed')).toBe(
      'true',
    );
  });

  it('writes the density for the whole console and never for one kind', async () => {
    /*
      `internal/columns/columns.go:758` keeps one value for every table, and
      `ColumnDensityWrite` is `{density}` with no `kind` - a per-kind parameter
      would be the shape of a preference that looks per-table and is not.
    */
    const sent: { url: string; body: unknown }[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      const at = new URL(url, 'http://x');
      if (init?.method === 'POST') {
        sent.push({ url: at.pathname, body: JSON.parse(String(init.body)) });
        return Promise.resolve(new Response(null, { status: 204 }));
      }
      const body =
        at.pathname === '/api/preferences'
          ? { rail: { barCount: 1, bars: [], hidden: [], tiles: {} }, columns: { kinds: {} } }
          : ok.body;
      return Promise.resolve(
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    });
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ResourceList /> }], {
      initialEntries: ['/c/prod/pods'],
    });
    render(
      <QueryClientProvider client={createQueryClient()}>
        {/* `ShellProvider` because the page calls `openDrawer` on a row click.
            Added when unit 2's drawer and unit 3's column controls met: nine
            tests here mounted without it and every one died on `useShell
            outside <ShellProvider>` before reaching its assertion. */}
        <ShellProvider extensions={[]}>
          <RouterProvider router={router} />
        </ShellProvider>
      </QueryClientProvider>,
    );
    await settled();

    fireEvent.click(screen.getByRole('button', { name: 'More table options' }));
    fireEvent.click(screen.getByRole('button', { name: 'Compact rows' }));

    await waitFor(() => {
      expect(sent.length).toBe(1);
    });
    expect(sent[0]?.url).toBe('/api/preferences/columns/density');
    expect(sent[0]?.body).toEqual({ density: 'compact' });
  });
});

/**
 * The namespace selector, at page altitude.
 *
 * `NamespaceMenu.test.tsx` covers the control's own behavior. What is here is
 * everything that is a fact about the *page*: where it sits, whether it is drawn
 * at all, what reaches the request, and the one rule about a cell that would ask
 * for what is already on screen.
 */
describe('the namespace selector', () => {
  it('sits immediately after the heading and before every other control', async () => {
    /*
      The heading says what you are looking at, the namespace says which of them, and
      everything that changes *how* the rows are drawn stays at the far right.
    */
    mount(ok);
    await settled();

    const header = document.querySelector('header');
    const controls = [...(header?.querySelectorAll('button, input') ?? [])];
    const at = (match: (el: Element) => boolean) => controls.findIndex(match);

    const selector = at((el) => el.getAttribute('title') === 'Every namespace');
    const filter = at((el) => el.getAttribute('type') === 'search');
    const columns = at((el) => el.textContent === 'Columns');

    expect(selector).toBeGreaterThanOrEqual(0);
    expect(selector).toBeLessThan(filter);
    expect(selector).toBeLessThan(columns);
    // And after the heading, which keeps the page's name in front of a control.
    expect(header?.firstElementChild?.tagName).toBe('H1');
  });

  it('is withheld on a kind that has no namespaces', async () => {
    /*
      The server applies the same guard to the selector itself: a Node has no
      namespace, so a control narrowing by one cannot change anything about this
      table. `page.namespaced` is the server's answer and the front end does not
      guess it from whether a row happens to carry one.
    */
    mount({ body: { state: 'ok', data: { ...page, namespaced: false } } });
    await settled();
    expect(document.querySelector('[title="Every namespace"]')).toBeNull();
  });

  /**
   * The label selector: accepted by `listResources`, keyed in the query cache
   * and passed through by the Go read, and sent from the filter box.
   */
  it('sends the label selector from the address, unparsed', async () => {
    const asked: string[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      const at = new URL(raw, 'http://x');
      asked.push(at.pathname + at.search);
      const body =
        at.pathname === '/api/preferences'
          ? preferences.body
          : at.pathname.endsWith('/namespaces')
            ? namespaces.body
            : ok.body;
      return Promise.resolve(
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    });
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ResourceList /> }], {
      initialEntries: ['/c/prod/pods?selector=app%3Dapi%2Ctier%21%3Dcache'],
    });
    render(
      <QueryClientProvider client={createQueryClient()}>
        <ShellProvider extensions={[]}>
          <RouterProvider router={router} />
        </ShellProvider>
      </QueryClientProvider>,
    );
    await settled();

    const list = asked.find((one) => one.startsWith('/api/c/prod/r/pods'));
    // Sent as written. `app=api,tier!=cache` is the API server's own grammar and
    // what a user already types into `kubectl -l`; a client that parsed it
    // would be a second implementation of a parser whose errors the server
    // states better.
    expect(list).toContain('selector=app%3Dapi%2Ctier%21%3Dcache');
  });

  it('sends every namespace of the selection, repeated', async () => {
    /*
      The parameter is `explode: true`, so `?namespace=a&namespace=b` is the wire
      form. A server reading the first value only, or a client writing one
      value only, would narrow a selection of two to one and say nothing about
      it — "a client that substitutes one for the other
      shows a fraction of a cluster with no sign that it did".
    */
    const asked: string[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      const at = new URL(raw, 'http://x');
      asked.push(at.pathname + at.search);
      const body =
        at.pathname === '/api/preferences'
          ? preferences.body
          : at.pathname.endsWith('/namespaces')
            ? namespaces.body
            : ok.body;
      return Promise.resolve(
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    });
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ResourceList /> }], {
      initialEntries: ['/c/prod/pods?namespace=shop-prod&namespace=default'],
    });
    render(
      <QueryClientProvider client={createQueryClient()}>
        {/* `ShellProvider` because the page calls `openDrawer` on a row click.
            Added when unit 2's drawer and unit 3's column controls met: nine
            tests here mounted without it and every one died on `useShell
            outside <ShellProvider>` before reaching its assertion. */}
        <ShellProvider extensions={[]}>
          <RouterProvider router={router} />
        </ShellProvider>
      </QueryClientProvider>,
    );
    await settled();

    const list = asked.find((one) => one.startsWith('/api/c/prod/r/pods'));
    expect(list).toBeDefined();
    // Both, and canonical - sorted, so two spellings of one selection are one
    // cache entry and one page, exactly as `parseNamespaces` guarantees.
    expect(list).toBe('/api/c/prod/r/pods?namespace=default&namespace=shop-prod');
  });

  it('writes the selection into the address bar as a repeated parameter', async () => {
    stubFetch({
      '/api/c/prod/r/pods': ok,
      '/api/preferences': preferences,
      '/api/c/prod/namespaces': namespaces,
    });
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ResourceList /> }], {
      initialEntries: ['/c/prod/pods'],
    });
    render(
      <QueryClientProvider client={createQueryClient()}>
        {/* `ShellProvider` because the page calls `openDrawer` on a row click.
            Added when unit 2's drawer and unit 3's column controls met: nine
            tests here mounted without it and every one died on `useShell
            outside <ShellProvider>` before reaching its assertion. */}
        <ShellProvider extensions={[]}>
          <RouterProvider router={router} />
        </ShellProvider>
      </QueryClientProvider>,
    );
    await settled();

    fireEvent.click(screen.getByTitle('Every namespace'));
    const pick = (name: string) => {
      const found = [...document.querySelectorAll('ul button')].find(
        (button) => button.textContent?.includes(name) === true,
      );
      if (found === undefined) throw new Error(`no ${name} entry`);
      fireEvent.click(found);
    };
    pick('kube-system');
    await waitFor(() => {
      expect(router.state.location.search).toBe('?namespace=kube-system');
    });

    /*
      **Two, and written as the parameter repeated.** One value alone cannot tell
      the two spellings apart, so it would pass against a defect that
      comma-joined the selection into a single value. The
      server parses both — `parseNamespaces` splits on commas — so this is about
      the address bar carrying the contract's own `explode: true` form rather
      than a spelling only this client would produce.
    */
    pick('default');
    await waitFor(() => {
      expect(router.state.location.search).toBe('?namespace=default&namespace=kube-system');
    });
    // Replaced, never pushed: a selection built up one namespace at a time must
    // not turn Back into an undo of each tick.
    expect(router.state.historyAction).toBe('REPLACE');
  });

  it('draws a namespace cell as text when it is already the whole selection', async () => {
    /*
      Filtering to what is already the whole of the table changes nothing, and a
      control that cannot change anything is never drawn.
    */
    mount(ok, '/c/prod/pods?namespace=shop-prod');
    const panel = await settled();
    expect(panel.querySelector('[data-role="namespace"]')).not.toBeNull();
    expect(panel.querySelector('button[data-role="namespace"]')).toBeNull();

    cleanup();
    vi.unstubAllGlobals();

    // Two selected: narrowing to one of them is a change, so the control is back.
    mount(ok, '/c/prod/pods?namespace=shop-prod&namespace=default');
    const two = await settled();
    expect(two.querySelector('button[data-role="namespace"]')).not.toBeNull();
  });
});

/**
 * The checkboxes add no column to the table: one select-all and one tick per
 * row, counted against the headings. The ticks are state and the rows are a
 * function of it, so there is no second opinion about the selection to keep in
 * step.
 */
describe('the checkboxes', () => {
  it('add no column to the table, and travel with the name', async () => {
    mount(ok);
    const panel = await settled();

    const columns = page.columns.length;
    expect(panel.querySelectorAll('colgroup col').length).toBe(columns);
    expect(panel.querySelectorAll('thead th').length).toBe(columns);
    expect(panel.querySelectorAll('tbody tr:first-child td').length).toBe(columns);

    // Exactly one select-all, and one tick per row.
    expect(panel.querySelectorAll('input[aria-label="Select every row listed"]').length).toBe(1);
    expect(panel.querySelectorAll('tbody input[type="checkbox"]').length).toBe(page.rows.length);

    /*
      **In the name's cell, not in a cell of its own**. The tick belongs to the
      object rather than to a position, so dragging Name to the far right takes
      the checkboxes with it, and a dedicated leading column would put 34px
      between the tick and the namespace beside it.
    */
    const first = panel.querySelector('tbody tr td');
    expect(first?.querySelector('input[type="checkbox"]')).not.toBeNull();
    expect(first?.textContent).toContain('storefront-web');

    // And the select-all is inside the Name heading rather than beside it.
    const head = panel.querySelector('thead th');
    expect(head?.querySelector('input[type="checkbox"]')).not.toBeNull();
    expect(head?.textContent).toContain('Name');
  });

  it('follows the name when the name is not the first column drawn', async () => {
    /*
      The rule the placement exists for, and the one an "always the first column"
      implementation passes every other test against. `role` says which column is
      the name; the order says where it is drawn. The server keeps the two apart
      deliberately — "moving a column does not make it the name".
    */
    const [name, namespace, cpu] = page.columns;
    if (name === undefined || namespace === undefined || cpu === undefined) {
      throw new Error('fixture');
    }
    mount({
      body: {
        state: 'ok',
        data: {
          ...page,
          columns: [
            { ...namespace, order: 0 },
            { ...cpu, order: 1 },
            { ...name, order: 2 },
          ],
        },
      },
    });
    const panel = await settled();

    const cells = [...(panel.querySelector('tbody tr')?.children ?? [])];
    expect(cells.length).toBe(3);
    // The first cell drawn is the namespace and carries no tick.
    expect(cells[0]?.querySelector('input[type="checkbox"]')).toBeNull();
    // The last is the name, and the tick went with it.
    expect(cells[2]?.querySelector('input[type="checkbox"]')).not.toBeNull();
    expect(cells[2]?.textContent).toContain('storefront-web');
  });
});

describe('arranging the columns', () => {
  /**
   * The last hop: `setColumnSize` and `setColumnPlace` in `endpoints.ts` must
   * have a caller. A capability present at every layer but the last is worse
   * than an absent one, because everything in the tree reports success. So these
   * assert that a gesture reaches the wire, and what it carries.
   */
  function record(): { sent: { url: string; body: unknown }[] } {
    const sent: { url: string; body: unknown }[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      const at = new URL(raw, 'http://x');
      if (init?.method === 'POST') {
        sent.push({ url: at.pathname, body: JSON.parse(String(init.body)) });
        return Promise.resolve(new Response(null, { status: 204 }));
      }
      const body =
        at.pathname === '/api/preferences'
          ? preferences.body
          : at.pathname.endsWith('/namespaces')
            ? namespaces.body
            : ok.body;
      return Promise.resolve(
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    });
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ResourceList /> }], {
      initialEntries: ['/c/prod/pods'],
    });
    render(
      <QueryClientProvider client={createQueryClient()}>
        {/* `ShellProvider` because the page calls `openDrawer` on a row click.
            Added when unit 2's drawer and unit 3's column controls met: nine
            tests here mounted without it and every one died on `useShell
            outside <ShellProvider>` before reaching its assertion. */}
        <ShellProvider extensions={[]}>
          <RouterProvider router={router} />
        </ShellProvider>
      </QueryClientProvider>,
    );
    return { sent };
  }

  it('posts the width of the column that was dragged, and of no other', async () => {
    const { sent } = record();
    await settled();

    const grip = document.querySelector<HTMLElement>('[aria-label="Resize the CPU column"]');
    if (grip === null) throw new Error('no grip');
    fireEvent.keyDown(grip, { key: 'ArrowRight' });

    await waitFor(() => {
      expect(sent.length).toBe(1);
    });
    expect(sent[0]?.url).toBe('/api/preferences/columns/size');
    // One column: sending all of them would freeze every untouched column at
    // whatever pixel it happened to be at.
    expect(sent[0]?.body).toMatchObject({ kind: 'pods', column: 'cpu' });
  });

  it('posts a move as a neighbor and a side, never as an index', async () => {
    const { sent } = record();
    await settled();

    const grip = document.querySelector<HTMLElement>('[aria-label="Resize the Name column"]');
    if (grip === null) throw new Error('no grip');
    fireEvent.keyDown(grip, { key: 'ArrowRight', altKey: true });

    await waitFor(() => {
      expect(sent.length).toBe(1);
    });
    expect(sent[0]?.url).toBe('/api/preferences/columns/place');
    expect(sent[0]?.body).toEqual({
      kind: 'pods',
      column: 'name',
      target: 'namespace',
      edge: 'after',
    });
  });
});

/*
  A console that keeps no preferences - the server profile, or several
  replicas behind one address - registers none of the twelve writes, and
  `capabilities.preferences` says so. On a shared console the server's
  arrangement would be everybody's, so the answer is *per browser*: every control is still drawn, and each gesture is kept in this
  browser's own storage and sends no request. Both arms: the desktop still posts.
*/
describe('a console that keeps no preferences', () => {
  function capabilities(keeps: boolean): StubReply {
    return {
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
    };
  }

  /** Every request the page makes, answered; every POST recorded. */
  function recorded(keeps: boolean): { posts: string[] } {
    const posts: string[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      const at = new URL(raw, 'http://x');
      if (init?.method !== undefined && init.method !== 'GET') {
        posts.push(`${init.method} ${at.pathname}`);
        return Promise.resolve(new Response(null, { status: 404 }));
      }
      const body =
        at.pathname === '/api/preferences'
          ? preferences.body
          : at.pathname === '/api/capabilities'
            ? capabilities(keeps).body
            : at.pathname.endsWith('/namespaces')
              ? namespaces.body
              : ok.body;
      return Promise.resolve(
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    });
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ResourceList /> }], {
      initialEntries: ['/c/prod/pods'],
    });
    render(
      <QueryClientProvider client={createQueryClient()}>
        <ShellProvider extensions={[]}>
          <RouterProvider router={router} />
        </ShellProvider>
      </QueryClientProvider>,
    );
    return { posts };
  }

  /** The stored arrangement, read the way a reload would. */
  function stored(): { density?: string; kinds: Record<string, { widths?: Record<string, number> }> } {
    return JSON.parse(localStorage.getItem('kubetower.columns') ?? '{"kinds":{}}') as {
      density?: string;
      kinds: Record<string, { widths?: Record<string, number> }>;
    };
  }

  function table(): HTMLTableElement {
    const found = document.querySelector<HTMLTableElement>('#panel table.kt-grid');
    if (found === null) throw new Error('no table');
    return found;
  }

  it('still draws every column control, and the list', async () => {
    mount(ok, '/c/prod/pods', capabilities(false));
    await settled();
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'More table options' })).toBeTruthy();
    });
    expect(screen.getByRole('button', { name: 'Columns' })).toBeTruthy();
    expect(document.querySelector('[aria-label^="Resize the"]')).not.toBeNull();
    expect(document.querySelectorAll('#panel tbody tr').length).toBeGreaterThan(0);
  });

  it('keeps a resize in this browser, sends no request, and draws it again after a reload', async () => {
    const { posts } = recorded(false);
    await settled();
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Columns' })).toBeTruthy();
    });
    expect(table().classList.contains('sized')).toBe(false);

    const grip = document.querySelector<HTMLElement>('[aria-label="Resize the CPU column"]');
    if (grip === null) throw new Error('no grip');
    fireEvent.keyDown(grip, { key: 'ArrowRight' });

    await waitFor(() => {
      expect(stored().kinds.pods?.widths?.cpu).toBeGreaterThan(0);
    });
    await waitFor(() => {
      expect(table().classList.contains('sized')).toBe(true);
    });

    // A reload: a fresh tree and a fresh read of storage, nothing in memory.
    cleanup();
    resetColumnMemory();
    const again = recorded(false);
    await settled();
    await waitFor(() => {
      expect(table().classList.contains('sized')).toBe(true);
    });
    expect(posts).toEqual([]);
    expect(again.posts).toEqual([]);
  });

  it('keeps the row height in this browser and sends no request', async () => {
    const { posts } = recorded(false);
    await settled();
    fireEvent.click(await screen.findByRole('button', { name: 'More table options' }));
    fireEvent.click(await screen.findByRole('button', { name: /compact rows/i }));

    await waitFor(() => {
      expect(stored().density).toBe('compact');
    });
    await waitFor(() => {
      expect(table().getAttribute('data-density')).toBe('compact');
    });
    expect(posts).toEqual([]);
  });

  it('ticks a column on and off in this browser, sending no request', async () => {
    const withNode: StubReply = {
      body: {
        state: 'ok',
        data: {
          ...page,
          columns: [
            ...page.columns,
            {
              key: 'node',
              label: 'Node',
              visible: false,
              order: 3,
              sortable: true,
              secondary: true,
              gauged: false,
              copyable: false,
              prose: false,
              filterable: false,
              role: 'other',
            },
          ],
        },
      },
    };
    const posts: string[] = [];
    mount(withNode, '/c/prod/pods', capabilities(false));
    const original = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method !== undefined && init.method !== 'GET') posts.push(String(input));
      return original(input, init);
    });
    await settled();
    const heading = (): Element | undefined =>
      [...document.querySelectorAll('#panel thead th')].find((th) => th.textContent?.includes('Node'));

    const nodeBox = async (): Promise<HTMLElement> => {
      if (screen.queryByRole('checkbox', { name: /^Node/ }) === null) {
        fireEvent.click(await screen.findByRole('button', { name: 'Columns' }));
      }
      return screen.findByRole('checkbox', { name: /^Node/ });
    };
    fireEvent.click(await nodeBox());
    await waitFor(() => {
      expect(heading()).not.toBeUndefined();
    });
    fireEvent.click(await nodeBox());
    await waitFor(() => {
      expect(heading()).toBeUndefined();
    });
    // Back to what the server drew: nothing remembered for this table.
    expect(localStorage.getItem('kubetower.columns') ?? '').not.toContain('pods');
    expect(posts).toEqual([]);
  });

  it('draws a fresh Ingresses list without Address, unchecked, and draws it once checked', async () => {
    /*
      An Ingress's Address is not on the default view: it is unchecked and not
      drawn, and can be checked to show it. The columns are the ones
      `internal/resources/kinds.go` declares for ingresses, keyed as `columnKey`
      keys them, as the server sends them: Address secondary, off, and still
      copyable.
    */
    const column = (
      key: string,
      label: string,
      order: number,
      facts: { secondary?: boolean; copyable?: boolean; filterable?: boolean; role?: 'name' | 'namespace' } = {},
    ): ListPage['columns'][number] => ({
      key,
      label,
      visible: facts.secondary !== true,
      order,
      sortable: true,
      secondary: facts.secondary === true,
      gauged: false,
      copyable: facts.copyable === true,
      prose: false,
      filterable: facts.filterable === true,
      role: facts.role ?? 'other',
    });
    const ingresses: StubReply = {
      body: {
        state: 'ok',
        data: {
          kind: 'ingresses',
          label: 'Ingresses',
          namespaced: true,
          columns: [
            column('name-0', 'Name', 0, { role: 'name' }),
            column('namespace-1', 'Namespace', 1, { role: 'namespace' }),
            column('class-2', 'Class', 2, { filterable: true }),
            column('hosts-3', 'Hosts', 3, { copyable: true }),
            column('address-4', 'Address', 4, { secondary: true, copyable: true }),
            column('age-5', 'Age', 5),
          ],
          rows: [
            {
              id: 'shop-prod/storefront',
              name: 'storefront',
              namespace: 'shop-prod',
              cells: {
                'name-0': { text: 'storefront' },
                'namespace-1': { text: 'shop-prod' },
                'class-2': { text: 'nginx' },
                'hosts-3': { text: 'shop.example.com', copy: 'shop.example.com' },
                'address-4': { text: '203.0.113.10', copy: '203.0.113.10' },
                'age-5': { text: '3d' },
              },
            },
          ],
        } satisfies ListPage,
      },
    };
    const posts: string[] = [];
    mount(ok, '/c/prod/ingresses', capabilities(false), { '/api/c/prod/r/ingresses': ingresses });
    const original = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method !== undefined && init.method !== 'GET') posts.push(String(input));
      return original(input, init);
    });
    await settled();
    const headings = (): string[] =>
      [...document.querySelectorAll('#panel thead th')].map((th) => th.textContent ?? '');
    await waitFor(() => {
      expect(headings().some((text) => text.includes('Hosts'))).toBe(true);
    });

    // Fresh: not drawn, and nothing of it in the rows either.
    expect(headings().some((text) => text.includes('Address'))).toBe(false);
    expect(document.getElementById('panel')?.textContent).not.toContain('203.0.113.10');

    // Unchecked in the columns menu, and said to be a default rather than a
    // choice of the user's.
    fireEvent.click(await screen.findByRole('button', { name: 'Columns' }));
    const box = await screen.findByRole<HTMLInputElement>('checkbox', { name: /^Address/ });
    expect(box.checked).toBe(false);
    expect(box.disabled).toBe(false);
    expect(box.closest('label')?.textContent).toContain('off by default');

    // Checked: drawn, in its place after Hosts, and still copyable.
    fireEvent.click(box);
    await waitFor(() => {
      expect(headings().some((text) => text.includes('Address'))).toBe(true);
    });
    const drawn = headings().map((text) => ['Hosts', 'Address', 'Age'].find((one) => text.includes(one)));
    expect(drawn.filter((one) => one !== undefined)).toEqual(['Hosts', 'Address', 'Age']);
    expect(screen.getByRole('button', { name: 'Copy this address' })).toBeTruthy();

    // Remembered as any column choice is: shown on purpose is `false`, not absent.
    const memory = JSON.parse(localStorage.getItem('kubetower.columns') ?? '{"kinds":{}}') as {
      kinds: Record<string, { hidden?: Record<string, boolean> }>;
    };
    expect(memory.kinds.ingresses?.hidden).toEqual({ 'address-4': false });
    expect(posts).toEqual([]);

    // A reload: a fresh tree and a fresh read of storage, the server still
    // sending Address off. The user's tick wins over the default.
    cleanup();
    resetColumnMemory();
    mount(ok, '/c/prod/ingresses', capabilities(false), { '/api/c/prod/r/ingresses': ingresses });
    await settled();
    await waitFor(() => {
      expect(headings().some((text) => text.includes('Address'))).toBe(true);
    });
  });

  it('posts a resize to the console where it keeps them, and stores nothing here', async () => {
    const { posts } = recorded(true);
    await settled();
    const grip = await waitFor(() => {
      const found = document.querySelector<HTMLElement>('[aria-label="Resize the CPU column"]');
      if (found === null) throw new Error('no grip');
      return found;
    });
    fireEvent.keyDown(grip, { key: 'ArrowRight' });
    await waitFor(() => {
      expect(posts).toContain('POST /api/preferences/columns/size');
    });
    expect(localStorage.getItem('kubetower.columns')).toBeNull();
  });

  it('draws all of them where it keeps them', async () => {
    mount(ok, '/c/prod/pods', capabilities(true));
    await settled();
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'More table options' })).toBeTruthy();
    });
    expect(screen.getByRole('button', { name: 'Columns' })).toBeTruthy();
    expect(document.querySelector('[aria-label^="Resize the"]')).not.toBeNull();
  });
});

describe('a filter typed into the same list', () => {
  /*
    Typing in the pods' search box must not make the window behind it cut out
    and flicker, once per settled word. The filter is in the query key, a new
    key starts empty, and the table would drop to its waiting state until the
    cluster answered. These tests hold every list answer the page asks for until
    the test releases it, so the frame between the two answers is on screen long
    enough to be read.
  */

  function named(...names: string[]): ListPage {
    return {
      ...page,
      rows: names.map((name) => ({
        id: `shop-prod/${name}`,
        name,
        namespace: 'shop-prod',
        cells: {
          name: { text: name },
          namespace: { text: 'shop-prod' },
          cpu: { text: '1m', sort: 1 },
        },
      })),
    };
  }

  interface Held {
    url: URL;
    answer: (data: ListPage) => void;
  }

  /** The page, with each list request either answered at once - where `now`
   *  has an answer for it - or held until the test calls `answer`. */
  function mountHeld(
    path: string,
    now: (asked: URL) => ListPage | undefined,
  ): {
    held: Held[];
    router: ReturnType<typeof createMemoryRouter>;
    client: ReturnType<typeof createQueryClient>;
  } {
    cleanup();
    const held: Held[] = [];
    const respond = (body: unknown, status = 200, type = 'application/json'): Response =>
      new Response(JSON.stringify(body), { status, headers: { 'Content-Type': type } });
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      const at = new URL(raw, 'http://x');
      if (at.pathname === '/api/preferences') return Promise.resolve(respond(preferences.body));
      if (at.pathname.endsWith('/namespaces')) return Promise.resolve(respond(namespaces.body));
      if (at.pathname.includes('/r/')) {
        const answer = now(at);
        if (answer !== undefined) return Promise.resolve(respond({ state: 'ok', data: answer }));
        return new Promise<Response>((resolve) => {
          held.push({
            url: at,
            answer: (data) => {
              resolve(respond({ state: 'ok', data }));
            },
          });
        });
      }
      return Promise.resolve(
        respond(
          { status: 404, title: 'Not Found', code: 'not_found', detail: 'no stub' },
          404,
          'application/problem+json',
        ),
      );
    });
    const router = createMemoryRouter(
      [
        {
          path: '/c/:cluster/:kind',
          element: (
            <>
              <ResourceList />
              <DrawerState />
            </>
          ),
        },
      ],
      { initialEntries: [path] },
    );
    const client = createQueryClient();
    render(
      <QueryClientProvider client={client}>
        <ShellProvider extensions={[]}>
          <RouterProvider router={router} />
        </ShellProvider>
      </QueryClientProvider>,
    );
    return { held, router, client };
  }

  /** Whether the shell holds a drawer: what `?open=` arriving does, read off
   *  the shell rather than off a `Drawer` this file does not mount. */
  function DrawerState(): ReactNode {
    const { drawer } = useShell();
    return <p data-testid="drawer">{drawer === null ? 'empty' : 'open'}</p>;
  }

  /** The held request for one search term, once the page has asked for it. */
  async function heldFor(held: readonly Held[], path: string, search: string): Promise<Held> {
    return waitFor(() => {
      const found = held.find(
        (one) => one.url.pathname === path && (one.url.searchParams.get('search') ?? '') === search,
      );
      if (found === undefined) throw new Error(`nothing asked for ${path}?search=${search}`);
      return found;
    });
  }

  function panelText(): string {
    return document.getElementById('panel')?.textContent ?? '';
  }

  /** The page's own waiting state, inside the panel - not the panel's own
   *  `aria-busy`, which is the standing-in marker. */
  function waiting(): boolean {
    return document.querySelector('#panel [aria-busy="true"]') !== null;
  }

  function filtering(): boolean {
    return document.querySelector('[data-settling="true"]') !== null;
  }

  /** Every state the panel passes through, one entry per DOM change. */
  function watchFrames(): { frames: string[]; stop: () => void } {
    const frames: string[] = [];
    const observer = new MutationObserver(() => {
      const rows = document.querySelectorAll('#panel tbody tr').length;
      frames.push(waiting() ? 'waiting' : rows === 0 ? 'no table' : 'rows');
    });
    observer.observe(document.body, {
      subtree: true,
      childList: true,
      characterData: true,
      attributes: true,
    });
    return {
      frames,
      stop: () => {
        observer.disconnect();
      },
    };
  }

  const allPods = (asked: URL): ListPage | undefined =>
    asked.pathname === '/api/c/prod/r/pods' && asked.search === ''
      ? named('nginx-a', 'nginx-b', 'redis-0')
      : undefined;

  it('keeps the rows on screen while a narrower answer is read, and says it is filtering', async () => {
    const { held, client } = mountHeld('/c/prod/pods', allPods);
    await settled();
    expect(panelText()).toContain('redis-0');
    expect(filtering()).toBe(false);

    const watch = watchFrames();

    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'ngin' } });
    const ngin = await heldFor(held, '/api/c/prod/r/pods', 'ngin');
    // The request is out and has not answered: the frame a user would see.
    // The previous rows, all of them, marked as not yet the answer.
    expect(waiting()).toBe(false);
    expect(panelText()).toContain('nginx-a');
    expect(panelText()).toContain('redis-0');
    expect(filtering()).toBe(true);
    expect(document.getElementById('panel')?.getAttribute('aria-busy')).toBe('true');

    ngin.answer(named('nginx-a', 'nginx-b'));
    await waitFor(() => {
      expect(panelText()).not.toContain('redis-0');
    });
    expect(panelText()).toContain('nginx-b');
    await waitFor(() => {
      expect(filtering()).toBe(false);
    });
    expect(document.getElementById('panel')?.hasAttribute('aria-busy')).toBe(false);

    // And again from a filtered answer to a narrower one.
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'nginx-a' } });
    const narrower = await heldFor(held, '/api/c/prod/r/pods', 'nginx-a');
    expect(waiting()).toBe(false);
    expect(panelText()).toContain('nginx-b');
    expect(filtering()).toBe(true);

    narrower.answer(named('nginx-a'));
    await waitFor(() => {
      expect(panelText()).not.toContain('nginx-b');
    });
    expect(panelText()).toContain('nginx-a');

    watch.stop();
    // Never once, between the first answer and the last, an empty panel or
    // the waiting state.
    expect(watch.frames.length).toBeGreaterThan(0);
    expect(watch.frames.filter((frame) => frame !== 'rows')).toEqual([]);

    // One cache entry per question asked and nothing for the stand-in: a
    // placeholder lives on the observer, never under the new key.
    const keys = client
      .getQueryCache()
      .findAll({ queryKey: ['resources'] })
      .map((query) => query.queryKey);
    expect(keys.map((key) => key[5])).toEqual(['', 'ngin', 'nginx-a']);
  });

  it('names the filter the rows answer, not the one still being read', async () => {
    const { held } = mountHeld('/c/prod/pods', allPods);
    await settled();

    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'qq' } });
    (await heldFor(held, '/api/c/prod/r/pods', 'qq')).answer(named());
    await waitFor(() => {
      expect(document.querySelector('[data-empty="filtered"]')).not.toBeNull();
    });
    expect(panelText()).toContain('“qq”');

    // The empty answer to `qq` stands in while `qx` is read. It must not claim
    // that `qx` matched nothing: nobody has asked the cluster about `qx` yet.
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'qx' } });
    const qx = await heldFor(held, '/api/c/prod/r/pods', 'qx');
    expect(filtering()).toBe(true);
    expect(panelText()).toContain('“qq”');
    expect(panelText()).not.toContain('“qx”');

    qx.answer(named());
    await waitFor(() => {
      expect(panelText()).toContain('“qx”');
    });
  });

  it('starts from the waiting state on another kind, even while a filter was standing in', async () => {
    const { held, router } = mountHeld('/c/prod/pods', allPods);
    await settled();

    // A stand-in is up: the pods' rows, for a filter not answered yet.
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'ngin' } });
    await heldFor(held, '/api/c/prod/r/pods', 'ngin');
    expect(panelText()).toContain('redis-0');

    // Pods under a heading that says Deployments is the defect
    // `router/prefetch.ts` refuses. Nothing of the pods' answer may cross.
    await act(async () => {
      await router.navigate('/c/prod/deployments');
    });
    await waitFor(() => {
      expect(waiting()).toBe(true);
    });
    expect(panelText()).not.toContain('nginx-a');
    expect(panelText()).not.toContain('redis-0');

    // And typing on the new kind before it has answered keeps nothing either:
    // the last list that answered is the pods', and it is not this list.
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'web' } });
    await heldFor(held, '/api/c/prod/r/deployments', 'web');
    expect(waiting()).toBe(true);
    expect(panelText()).not.toContain('nginx-a');
  });

  it('opens a row an arrival names with a filter, once that filter has been answered', async () => {
    // The common arrival: a link from elsewhere carrying `search` and `open`
    // together, onto a list already on screen unfiltered. For the 250 ms the
    // search takes to settle, the answer on screen is the unfiltered one and
    // is not a stand-in yet - and `web-0` is not in it. Saying it is "no
    // longer among the Pods" there is the page contradicting the answer on
    // its way.
    const { held, router } = mountHeld('/c/prod/pods', allPods);
    await settled();
    expect(screen.getByTestId('drawer').textContent).toBe('empty');

    await act(async () => {
      await router.navigate('/c/prod/pods?search=web&open=shop-prod%2Fweb-0');
    });
    const web = await heldFor(held, '/api/c/prod/r/pods', 'web');
    expect(document.querySelector('[data-note="open-gone"]')).toBeNull();
    expect(screen.getByTestId('drawer').textContent).toBe('empty');

    web.answer(named('web-0'));
    await waitFor(() => {
      expect(screen.getByTestId('drawer').textContent).toBe('open');
    });
    expect(document.querySelector('[data-note="open-gone"]')).toBeNull();
  });

  it('goes back to the top when a new filter is answered, and not while it is read', async () => {
    const { held } = mountHeld('/c/prod/pods', allPods);
    const panel = await settled();
    // jsdom lays nothing out, so `scrollTop` is a plain value here: what the
    // page writes is what is read back.
    let top = 0;
    Object.defineProperty(panel, 'scrollTop', {
      configurable: true,
      get: () => top,
      set: (value: number) => {
        top = value;
      },
    });
    top = 400;

    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'ngin' } });
    const ngin = await heldFor(held, '/api/c/prod/r/pods', 'ngin');
    // The stand-in keeps the place the user was reading.
    expect(top).toBe(400);

    ngin.answer(named('nginx-a', 'nginx-b'));
    await waitFor(() => {
      expect(panelText()).not.toContain('redis-0');
    });
    await waitFor(() => {
      expect(top).toBe(0);
    });
  });

  it('starts from the waiting state on another namespace selection or another cluster', async () => {
    const both = (asked: URL): ListPage | undefined =>
      // Prod's unfiltered pods and nothing else: every other question is held.
      asked.search === '' && asked.pathname === '/api/c/prod/r/pods'
        ? named('nginx-a', 'redis-0')
        : undefined;

    const first = mountHeld('/c/prod/pods', both);
    await settled();
    expect(panelText()).toContain('redis-0');
    await act(async () => {
      await first.router.navigate('/c/prod/pods?namespace=kube-system');
    });
    await waitFor(() => {
      expect(first.held.some((one) => one.url.searchParams.get('namespace') === 'kube-system')).toBe(true);
    });
    await waitFor(() => {
      expect(waiting()).toBe(true);
    });
    expect(panelText()).not.toContain('redis-0');

    const second = mountHeld('/c/prod/pods', both);
    await settled();
    expect(panelText()).toContain('redis-0');
    await act(async () => {
      await second.router.navigate('/c/staging/pods?search=ngin');
    });
    await heldFor(second.held, '/api/c/staging/r/pods', 'ngin');
    await waitFor(() => {
      expect(waiting()).toBe(true);
    });
    expect(panelText()).not.toContain('redis-0');
  });
});

/**
 * The filter control.
 *
 * The search box is a plain search over every column again; a column's exact
 * value is chosen from a funnel beside it, listing the columns the server marks
 * `filterable` and the values it offers for each. Filtering stays server-side:
 * the control writes `?filter=<column label>:<value>`, the request carries it,
 * and the page's `filters` member says what the server applied.
 *
 * The stub below plays the server's half honestly: it echoes a filter only when
 * the label names a filterable column of the page, which is
 * `internal/resources/match.go`'s rule, so a test that relied on an echo the
 * server would not send fails here too.
 */
describe('the filter control', () => {
  const column = {
    visible: true,
    sortable: true,
    secondary: false,
    gauged: false,
    copyable: false,
    prose: false,
    filterable: false,
    role: 'other',
  } as const;
  const filterable: ListPage = {
    ...page,
    columns: [
      { ...column, key: 'name-0', label: 'Name', order: 0, role: 'name' },
      { ...column, key: 'namespace-1', label: 'Namespace', order: 1, role: 'namespace' },
      {
        ...column,
        key: 'status-2',
        label: 'Status',
        order: 2,
        filterable: true,
        filterValues: ['Pending', 'Running'],
      },
      {
        ...column,
        key: 'node-3',
        label: 'Node',
        order: 3,
        filterable: true,
        filterValues: ['kubeworker1', 'kubeworker2'],
      },
      { ...column, key: 'cpu-4', label: 'CPU', order: 4, align: 'end' },
    ],
    rows: [
      {
        id: 'shop-prod/storefront-web',
        name: 'storefront-web',
        namespace: 'shop-prod',
        cells: {
          'name-0': { text: 'storefront-web' },
          'namespace-1': { text: 'shop-prod' },
          'status-2': { text: 'Running' },
          'node-3': { text: 'kubeworker1' },
          'cpu-4': { text: '120m' },
        },
      },
    ],
  };

  function answer(at: URL): ListPage {
    const filters = at.searchParams.getAll('filter').flatMap((raw) => {
      const colon = raw.indexOf(':');
      const label = raw.slice(0, colon).trim().toLowerCase();
      const found = filterable.columns.find((one) => one.filterable && one.label.toLowerCase() === label);
      return colon < 0 || found === undefined ? [] : [{ column: found.key, value: raw.slice(colon + 1).trim() }];
    });
    return filters.length === 0 ? filterable : { ...filterable, filters };
  }

  function mountFiltered(path: string): { asked: URL[]; router: ReturnType<typeof createMemoryRouter> } {
    cleanup();
    const asked: URL[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      const at = new URL(raw, 'http://x');
      asked.push(at);
      const body =
        at.pathname === '/api/preferences'
          ? preferences.body
          : at.pathname.endsWith('/namespaces')
            ? namespaces.body
            : { state: 'ok', data: answer(at) };
      return Promise.resolve(
        new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }),
      );
    });
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ResourceList /> }], {
      initialEntries: [path],
    });
    render(
      <QueryClientProvider client={createQueryClient()}>
        <ShellProvider extensions={[]}>
          <RouterProvider router={router} />
        </ShellProvider>
      </QueryClientProvider>,
    );
    return { asked, router };
  }

  const lists = (asked: readonly URL[]) => asked.filter((one) => one.pathname === '/api/c/prod/r/pods');
  const filterParams = (router: ReturnType<typeof createMemoryRouter>) =>
    new URLSearchParams(router.state.location.search).getAll('filter');
  const funnel = () => screen.getByRole('button', { name: /^Filter by column/ });

  it('lists only the columns the server marks filterable, with the values it offers', async () => {
    mountFiltered('/c/prod/pods');
    await settled();
    fireEvent.click(funnel());

    const sections = [...document.querySelectorAll('[data-filter-column]')].map((one) =>
      one.getAttribute('data-filter-column'),
    );
    expect(sections).toEqual(['Status', 'Node']);
    // The panel opens on the columns alone, and the values of one drop down when
    // it is chosen.
    expect(document.querySelectorAll('[data-filter-column] ul')).toHaveLength(0);
    fireEvent.click(within(document.querySelector<HTMLElement>('[data-filter-column="Node"]')!).getByRole('button', { name: 'Node' }));
    const node = document.querySelector('[data-filter-column="Node"]');
    expect([...(node?.querySelectorAll('ul button') ?? [])].map((one) => one.textContent?.trim())).toEqual([
      'kubeworker1',
      'kubeworker2',
    ]);
  });

  // Absent, the funnel would read as missing on Deployments beside Pods. It is
  // drawn inert and says why, and opens nothing. One control, one size.
  it('draws the filter button against the search field, and every toolbar control one height', async () => {
    mountFiltered('/c/prod/pods');
    await settled();
    const field = screen.getByRole('searchbox');
    expect(field.parentElement).toBe(funnel().parentElement?.parentElement);
    expect(field.nextElementSibling).toBe(funnel().parentElement);
    expect(funnel().textContent).toBe('');
    const sized = [
      field,
      funnel(),
      screen.getByRole('button', { name: 'Columns' }),
      screen.getByRole('button', { name: 'More table options' }),
    ];
    for (const one of sized) expect(one.className).toContain('h-8');
    // The joint: the field squares its right corners, the button its left.
    expect(field.className).toContain('rounded-r-none');
    expect(funnel().className).toContain('rounded-l-none');
    expect(funnel().className).toContain('rounded-r-none');
    // Columns follows the funnel with nothing between them, sharing its border,
    // and the settling marker is not between them: it is before the field.
    const columns = screen.getByRole('button', { name: 'Columns' });
    expect(field.parentElement?.parentElement?.nextElementSibling).toBe(columns.parentElement);
    expect(columns.parentElement?.classList.contains('-ml-px')).toBe(true);
    expect(columns.className).toContain('rounded-l-none');
    expect(field.parentElement?.previousElementSibling?.tagName).toBe('LABEL');
    // The icon-only ones are squares, with no side padding to fight the width.
    const square = screen.getByRole('button', { name: 'More table options' });
    expect(square.className).toContain('w-8');
    expect(square.className).not.toContain('px-2');
  });

  it('draws an inert funnel that says why on a list with no filterable column', async () => {
    mount(ok);
    await settled();
    const inert = funnel();
    expect(inert.getAttribute('aria-disabled')).toBe('true');
    expect(inert.title).toBe(NOTHING_TO_FILTER);
    expect(inert.title).not.toContain('search box reads every column');
    fireEvent.click(inert);
    expect(screen.queryByRole('group', { name: 'Filter by column' })).toBeNull();
  });

  // An older link says `ns` and `q`. It narrows the list, and the address is
  // rewritten to the names the page reads.
  it('reads an old `?ns=` and `?q=` as the namespace and the search, and rewrites the address', async () => {
    const { asked, router } = mountFiltered('/c/prod/pods?ns=shop-prod&q=web');
    await waitFor(() => {
      expect(new URLSearchParams(router.state.location.search).toString()).toBe('namespace=shop-prod&search=web');
    });
    await waitFor(() => {
      expect(
        lists(asked).some(
          (one) => one.searchParams.get('namespace') === 'shop-prod' && one.searchParams.get('search') === 'web',
        ),
      ).toBe(true);
    });
    expect((screen.getByRole('searchbox', { name: /^Filter/ }) as HTMLInputElement).value).toBe('web');
  });

  it('says the same short thing on a custom resource list', async () => {
    mount({ body: { state: 'ok', data: { ...page, kind: 'applications.argoproj.io', label: 'Applications' } } });
    await settled();
    expect(funnel().title).toBe(NOTHING_TO_FILTER);
  });

  it('adds a chip and the parameter for a chosen value, and removing the chip removes both', async () => {
    const { asked, router } = mountFiltered('/c/prod/pods');
    await settled();
    fireEvent.click(funnel());
    fireEvent.click(within(document.querySelector<HTMLElement>('[data-filter-column="Node"]')!).getByRole('button', { name: 'Node' }));
    fireEvent.click(screen.getByRole('button', { name: 'kubeworker2' }));

    // The parameter, in the address and in the request: server-side.
    await waitFor(() => {
      expect(filterParams(router)).toEqual(['Node:kubeworker2']);
    });
    await waitFor(() => {
      expect(lists(asked).some((one) => one.searchParams.getAll('filter').includes('Node:kubeworker2'))).toBe(true);
    });
    // The search is untouched: a filter is not a word in the box.
    expect(lists(asked).every((one) => !one.searchParams.has('search'))).toBe(true);
    // The chip.
    const chip = await screen.findByRole('button', { name: 'Remove the filter Node: kubeworker2' });

    // A second value of the same column is either: the menu stays open and
    // still offers kubeworker1, which is the server's `filterValues` - not a
    // list narrowed by its own column's filter.
    fireEvent.click(screen.getByRole('button', { name: 'kubeworker1' }));
    await waitFor(() => {
      expect(filterParams(router)).toEqual(['Node:kubeworker2', 'Node:kubeworker1']);
    });

    fireEvent.click(chip);
    await waitFor(() => {
      expect(filterParams(router)).toEqual(['Node:kubeworker1']);
    });
    expect(screen.queryByRole('button', { name: 'Remove the filter Node: kubeworker2' })).toBeNull();
    await waitFor(() => {
      const last = lists(asked).at(-1);
      expect(last?.searchParams.getAll('filter')).toEqual(['Node:kubeworker1']);
    });
  });

  it("lands on the node panel's link filtered by Node, with a chip saying so", async () => {
    // The href `ObjectDetail.test.tsx` pins for "Show the pods running here".
    const { asked, router } = mountFiltered('/c/prod/pods?filter=Node%3Akubeworker1');
    await settled();
    expect(lists(asked)[0]?.searchParams.getAll('filter')).toEqual(['Node:kubeworker1']);
    expect(screen.getByRole('button', { name: 'Remove the filter Node: kubeworker1' })).not.toBeNull();
    // The box is empty: the filter is not text in it.
    expect(screen.getByRole<HTMLInputElement>('searchbox').value).toBe('');
    expect(filterParams(router)).toEqual(['Node:kubeworker1']);
  });

  it('rewrites an old ?search=node: address into the filter it meant', async () => {
    // Exactly what the node panel's link wrote before the filter control existed.
    const { asked, router } = mountFiltered('/c/prod/pods?search=node%3Akubeworker1');
    await settled();

    await waitFor(() => {
      expect(filterParams(router)).toEqual(['Node:kubeworker1']);
    });
    // The search is gone with it, and the address is replaced, not pushed:
    // Back does not return to the address that found nothing.
    expect(new URLSearchParams(router.state.location.search).get('search')).toBeNull();
    expect(router.state.historyAction).toBe('REPLACE');
    await waitFor(() => {
      const last = lists(asked).at(-1);
      expect(last?.searchParams.getAll('filter')).toEqual(['Node:kubeworker1']);
    });
    expect(screen.getByRole<HTMLInputElement>('searchbox').value).toBe('');
  });

  it('reads any other label:value the same before and after a reload: as a search', async () => {
    // Typed: a search for the text, never a filter.
    const typed = mountFiltered('/c/prod/pods');
    await settled();
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'status:running' } });
    await waitFor(() => {
      expect(new URLSearchParams(typed.router.state.location.search).get('search')).toBe('status:running');
    });
    await settled();
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(filterParams(typed.router)).toEqual([]);

    // The same address arriving - a reload, a shared link: still the search.
    // It must not become the Status filter.
    for (const address of ['/c/prod/pods?search=status%3Arunning', '/c/prod/pods?search=node%3Akubeworker1+nginx']) {
      const { router } = mountFiltered(address);
      await settled();
      await new Promise((resolve) => setTimeout(resolve, 50));
      expect(filterParams(router), address).toEqual([]);
      expect(new URLSearchParams(router.state.location.search).get('search'), address).toBe(
        new URLSearchParams(address.split('?')[1]).get('search'),
      );
    }
  });

  it('leaves a colon that names no filterable column in the search', async () => {
    // A pasted taint, and a column that exists and is not filterable.
    const { router } = mountFiltered('/c/prod/pods?search=control-plane%3ANoSchedule+cpu%3A12m');
    await settled();
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(filterParams(router)).toEqual([]);
    expect(new URLSearchParams(router.state.location.search).get('search')).toBe(
      'control-plane:NoSchedule cpu:12m',
    );
  });

  it('searches for node:kubeworker1 typed into the box, and never turns it into a filter', async () => {
    const { asked, router } = mountFiltered('/c/prod/pods');
    await settled();
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'node:kubeworker1 nginx' } });

    await waitFor(() => {
      expect(lists(asked).some((one) => one.searchParams.get('search') === 'node:kubeworker1 nginx')).toBe(true);
    });
    await settled();
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(filterParams(router)).toEqual([]);
    expect(lists(asked).every((one) => !one.searchParams.has('filter'))).toBe(true);
    expect(screen.getByRole<HTMLInputElement>('searchbox').value).toBe('node:kubeworker1 nginx');
  });

  it('drops a filter the server did not apply, so no chip claims a narrowing that did not happen', async () => {
    const { router } = mountFiltered('/c/prod/pods?filter=Colour%3Ablue&filter=Node%3Akubeworker1');
    await settled();
    await waitFor(() => {
      expect(filterParams(router)).toEqual(['Node:kubeworker1']);
    });
    // Waited for, not read once: the router's state changes before React has
    // drawn from it, and on a loaded CI runner the chips read in between are
    // still the ones the address held a moment before. The Color chip goes; the Node chip stays.
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /Remove the filter Colour/ })).toBeNull();
    });
    expect(screen.getByRole('button', { name: 'Remove the filter Node: kubeworker1' })).not.toBeNull();
  });

  it('says a dropped filter was not applied, until dismissed: an old Controlled by link is not silent', async () => {
    const { router } = mountFiltered('/c/prod/pods?filter=Controlled%20by%3AReplicaSet%2Fapi&filter=Node%3Akubeworker1');
    await settled();
    await waitFor(() => {
      expect(filterParams(router)).toEqual(['Node:kubeworker1']);
    });
    const notice = await screen.findByText(
      'Not a filter on this list: Controlled by: ReplicaSet/api. The list is not filtered by it.',
    );
    expect(notice.closest('[role="status"]')).not.toBeNull();
    // Only what was dropped: the Node filter is applied, and is a chip.
    expect(notice.textContent).not.toContain('kubeworker1');

    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }));
    await waitFor(() => {
      expect(screen.queryByText(/Not a filter on this list/)).toBeNull();
    });
    expect(filterParams(router)).toEqual(['Node:kubeworker1']);
  });

  it('forgets a dropped filter once the list changes, so a clean return to it says nothing', async () => {
    const { router } = mountFiltered('/c/prod/pods?filter=Controlled%20by%3AReplicaSet%2Fapi');
    await settled();
    await screen.findByText(/Not a filter on this list: Controlled by: ReplicaSet\/api/);

    await act(async () => {
      await router.navigate('/c/prod/deployments');
    });
    await settled();
    expect(screen.queryByText(/Not a filter on this list/)).toBeNull();

    await act(async () => {
      await router.navigate('/c/prod/pods');
    });
    await settled();
    expect(router.state.location.search).toBe('');
    expect(screen.queryByText(/Not a filter on this list/)).toBeNull();
  });

  it('says nothing of the kind where every filter was applied', async () => {
    const { router } = mountFiltered('/c/prod/pods?filter=Node%3Akubeworker1');
    await settled();
    expect(filterParams(router)).toEqual(['Node:kubeworker1']);
    await screen.findByRole('button', { name: 'Remove the filter Node: kubeworker1' });
    expect(screen.queryByText(/Not a filter on this list/)).toBeNull();
  });
});


describe("an extension's column", () => {
  function menu(...extras: string[]): StubReply {
    return {
      body: {
        state: 'ok',
        data: { groups: [{ key: 'extra', label: 'Extra', items: extras.map((key) => ({ text: key, href: `/c/prod/x/${key}` })), deferred: false }] },
      },
    };
  }
  function argocd(cells: ExtensionColumn['cells']): Extension {
    return { key: 'argocd', nav: { label: 'Delivery Center', path: 'x/argocd' }, routes: [], columns: [{ key: 'sync', label: 'Argo CD', kinds: ['*'], cells }] };
  }

  it('draws its answer in the row it names, read once for the list', async () => {
    setExtraColumnShown('argocd', 'sync', true);
    const cells = vi.fn((_list: { cluster: string; kind: string }) =>
      Promise.resolve({ 'shop-prod/storefront-web': { text: 'OutOfSync', tone: 'warn', href: '/c/prod/pods/shop-prod/storefront-web?tab=x.argocd.sync' } }),
    );
    mount(ok, '/c/prod/pods', undefined, { '/api/c/prod/nav': menu('argocd') }, [argocd(cells)]);
    expect(await screen.findByRole('columnheader', { name: /Argo CD/ })).toBeTruthy();
    const pill = await screen.findByText('OutOfSync');
    expect(pill.closest('a')?.getAttribute('href')).toBe('/c/prod/pods/shop-prod/storefront-web?tab=x.argocd.sync');
    expect(cells).toHaveBeenCalledTimes(1);
    expect(cells.mock.calls[0]?.[0]).toEqual({ cluster: 'prod', kind: 'pods' });
  });

  it('is absent while the extension is off, and when no row has an answer', async () => {
    setExtraColumnShown('argocd', 'sync', true);
    const cells = vi.fn(() => Promise.resolve({ 'shop-prod/storefront-web': { text: 'Synced', tone: 'ok' } }));
    mount(ok, '/c/prod/pods', undefined, { '/api/c/prod/nav': menu() }, [argocd(cells)]);
    await settled();
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(screen.queryByRole('columnheader', { name: /Argo CD/ })).toBeNull();
    expect(cells).not.toHaveBeenCalled();

    const none = vi.fn(() => Promise.resolve({ 'elsewhere/other': { text: 'Synced', tone: 'ok' } }));
    mount(ok, '/c/prod/pods', undefined, { '/api/c/prod/nav': menu('argocd') }, [argocd(none)]);
    await settled();
    await waitFor(() => {
      expect(none).toHaveBeenCalled();
    });
    expect(screen.queryByRole('columnheader', { name: /Argo CD/ })).toBeNull();
  });

  it('is hidden until it is ticked in Settings > Extra, and its read is never made meanwhile', async () => {
    const cells = vi.fn(() => Promise.resolve({ 'shop-prod/storefront-web': { text: 'Synced', tone: 'ok' } }));
    mount(ok, '/c/prod/pods', undefined, { '/api/c/prod/nav': menu('argocd') }, [argocd(cells)]);
    await settled();
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(screen.queryByRole('columnheader', { name: /Argo CD/ })).toBeNull();
    expect(cells).not.toHaveBeenCalled();

    setExtraColumnShown('argocd', 'sync', true);
    mount(ok, '/c/prod/pods', undefined, { '/api/c/prod/nav': menu('argocd') }, [argocd(cells)]);
    expect(await screen.findByRole('columnheader', { name: /Argo CD/ })).toBeTruthy();
  });

  describe('in the filter control', () => {
    const funnel = () => screen.getByRole('button', { name: /^Filter by column/ });
    const two: StubReply = {
      body: {
        state: 'ok',
        data: {
          ...page,
          columns: [
            ...page.columns,
            { ...page.columns[1]!, key: 'node', label: 'Node', order: 3, filterable: true, filterValues: ['n1', 'n2'], role: 'other' },
            { ...page.columns[1]!, key: 'qos', label: 'QoS', order: 4, filterable: true, filterValues: ['Guaranteed'], visible: false, role: 'other' },
          ],
          rows: [
            ...page.rows,
            { id: 'shop-prod/api', name: 'api', namespace: 'shop-prod', cells: { name: { text: 'api' }, namespace: { text: 'shop-prod' }, cpu: { text: '5m' } } },
          ],
        },
      },
    };
    const sync = (): Promise<Record<string, { text: string }>> =>
      Promise.resolve({ 'shop-prod/storefront-web': { text: 'OutOfSync' }, 'shop-prod/api': { text: 'Synced' } });
    const ext = (): Extension => ({ key: 'argocd', nav: { label: 'Delivery Center', path: 'x/argocd' }, routes: [], columns: [{ key: 'sync', label: 'Argo CD', kinds: ['*'], cells: sync }] });

    it('offers every shown column, the extension one with its values, and no hidden one', async () => {
      setExtraColumnShown('argocd', 'sync', true);
      mount(two, '/c/prod/pods', undefined, { '/api/c/prod/nav': menu('argocd') }, [ext()]);
      await screen.findByRole('columnheader', { name: /Argo CD/ });
      fireEvent.click(funnel());
      const group = await screen.findByRole('group', { name: 'Filter by column' });
      const offered = [...group.querySelectorAll('[data-filter-column]')].map((one) => one.getAttribute('data-filter-column'));
      expect(offered).toEqual(['Node', 'Argo CD']);
    });

    it('does not offer the extension column while it is hidden', async () => {
      mount(two, '/c/prod/pods', undefined, { '/api/c/prod/nav': menu('argocd') }, [ext()]);
      await settled();
      fireEvent.click(funnel());
      const group = await screen.findByRole('group', { name: 'Filter by column' });
      expect([...group.querySelectorAll('[data-filter-column]')].map((one) => one.getAttribute('data-filter-column'))).toEqual(['Node']);
    });

    it('narrows the rows by an extension column, kept in the address', async () => {
      setExtraColumnShown('argocd', 'sync', true);
      mount(two, '/c/prod/pods?filter=Argo%20CD%3ASynced', undefined, { '/api/c/prod/nav': menu('argocd') }, [ext()]);
      await screen.findByRole('columnheader', { name: /Argo CD/ });
      await waitFor(() => {
        expect(screen.queryByText('storefront-web')).toBeNull();
      });
      expect(screen.getByText('api')).toBeTruthy();
      expect(screen.getByRole('button', { name: 'Remove the filter Argo CD: Synced' })).toBeTruthy();
    });
  });

  // Settings → Extra → Delivery Center: "Show the Argo CD column in the lists"
  // The column names the setting; the cluster's listing says whether it is on
  // there.
  function gated(cells: ExtensionColumn['cells']): Extension {
    return { ...argocd(cells), columns: [{ key: 'sync', label: 'Argo CD', kinds: ['*'], cells, setting: 'list-column' }] };
  }
  function listing(value: boolean): StubReply {
    return {
      body: {
        state: 'ok',
        items: [{
          key: 'argocd', label: 'Delivery Center', enabled: true, pinned: false,
          settings: [{ key: 'list-column', label: 'Show the Argo CD column in the lists', value, default: false }],
        }],
      },
    };
  }
  const answer = (): Promise<Readonly<Record<string, { text: string; tone: string }>>> =>
    Promise.resolve({ 'shop-prod/storefront-web': { text: 'Synced', tone: 'ok' } });

  it('is drawn while its setting is on for the cluster', async () => {
    const cells = vi.fn(answer);
    mount(ok, '/c/prod/pods', undefined, { '/api/c/prod/nav': menu('argocd'), '/api/c/prod/extensions': listing(true) }, [gated(cells)]);
    expect(await screen.findByRole('columnheader', { name: /Argo CD/ })).toBeTruthy();
    expect(await screen.findByText('Synced')).toBeTruthy();
  });

  it('is absent, and its read never made, while its setting is off for the cluster', async () => {
    const cells = vi.fn(answer);
    mount(ok, '/c/prod/pods', undefined, { '/api/c/prod/nav': menu('argocd'), '/api/c/prod/extensions': listing(false) }, [gated(cells)]);
    await settled();
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(screen.queryByRole('columnheader', { name: /Argo CD/ })).toBeNull();
    expect(cells).not.toHaveBeenCalled();
  });

  it('is absent when the listing cannot be read: the column is asked for, never assumed', async () => {
    // No stub for the listing: the harness answers 404. The column is off by
    // default, so "could not tell" draws what off draws.
    const cells = vi.fn(answer);
    mount(ok, '/c/prod/pods', undefined, { '/api/c/prod/nav': menu('argocd') }, [gated(cells)]);
    await settled();
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(screen.queryByRole('columnheader', { name: /Argo CD/ })).toBeNull();
    expect(cells).not.toHaveBeenCalled();
  });

  it('is drawn when the listing cannot be read and the column declares its setting on by default', async () => {
    const cells = vi.fn(answer);
    const on: Extension = { ...argocd(cells), columns: [{ key: 'sync', label: 'Argo CD', kinds: ['*'], cells, setting: 'list-column', settingDefault: true }] };
    mount(ok, '/c/prod/pods', undefined, { '/api/c/prod/nav': menu('argocd') }, [on]);
    expect(await screen.findByRole('columnheader', { name: /Argo CD/ })).toBeTruthy();
  });
});

describe('settingWanted', () => {
  const row = (settings?: { key: string; label: string; value: boolean; default: boolean }[]) =>
    ({ key: 'argocd', label: 'Delivery Center', enabled: true, pinned: false, ...(settings === undefined ? {} : { settings }) }) as ExtensionInfo;
  const ok = (items: ExtensionInfo[]): ListResult<ExtensionInfo> => ({ state: 'ok', items });
  const off = [{ key: 'list-column', label: 'x', value: false, default: false }];

  it('waits while the listing is in flight', () => {
    expect(settingWanted(undefined, false, 'argocd', 'list-column')).toBeUndefined();
  });
  it('is the setting where the listing names it', () => {
    expect(settingWanted(ok([row(off)]), false, 'argocd', 'list-column')).toBe(false);
    expect(settingWanted(ok([row([{ ...off[0]!, value: true }])]), false, 'argocd', 'list-column')).toBe(true);
  });
  it('is false where the listing failed, or does not name the setting', () => {
    expect(settingWanted(undefined, true, 'argocd', 'list-column')).toBe(false);
    expect(settingWanted(ok([row()]), false, 'argocd', 'list-column')).toBe(false);
    expect(settingWanted(ok([row([{ ...off[0]!, value: true }])]), false, 'other', 'list-column')).toBe(false);
    expect(settingWanted({ state: 'empty', items: [] } as unknown as ListResult<ExtensionInfo>, false, 'argocd', 'list-column')).toBe(false);
  });
  it("is the column's declared default where the listing cannot say", () => {
    expect(settingWanted(undefined, true, 'argocd', 'list-column', true)).toBe(true);
    expect(settingWanted(ok([row()]), false, 'argocd', 'list-column', true)).toBe(true);
    expect(settingWanted({ state: 'empty', items: [] } as unknown as ListResult<ExtensionInfo>, false, 'argocd', 'list-column', true)).toBe(true);
    // Where the listing names it, its value wins over the declared default.
    expect(settingWanted(ok([row(off)]), false, 'argocd', 'list-column', true)).toBe(false);
  });
});

describe('namespaceNote', () => {
  it('keeps an empty namespace list distinct from one not read yet', () => {
    expect(namespaceNote(undefined)).toBeUndefined();
    const none = { state: 'empty', items: [] } as unknown as ListResult<ApiNamespace>;
    expect(namespaceNote(none)).toBe('No namespaces.');
  });
});
