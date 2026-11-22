import { render, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DetailPage, Relation } from '../../api';
import { createQueryClient } from '../../query/client';
import { QueryClientProvider } from '@tanstack/react-query';
import { stubFetch } from '../../testing/harness';
import { ObjectDetail } from './ObjectDetail';

/**
 * The graph, the labels, and the three renderings a fact has.
 *
 * Drawn through the real panel rather than through the components: the panel
 * is where a member is quietly not read, and everything here is already
 * computed by `internal/detail`.
 *
 * The assertion that matters most is about a row that is **present and not a
 * link**: *"Worth listing; must not look like a link that failed."* An empty
 * answer and a failed answer drawn differently, at the width of a row, with
 * both wrong answers invisible to the reader: a drawer listing only linkable relations
 * says a pod has three volumes when it has five, and one that links all of them
 * gives two dead links.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

const PATH = '/api/c/prod/r/pods/shop-prod/api-1';

const relations: Relation[] = [
  {
    role: 'Controlled by',
    kind: 'replicasets',
    namespace: 'shop-prod',
    name: 'api-7c9',
    label: 'api-7c9',
    detail: 'Running on node-3',
    linkable: true,
  },
  {
    role: 'Node',
    kind: 'nodes',
    name: 'node-3',
    label: 'node-3',
    linkable: true,
  },
  {
    role: 'Secret',
    kind: 'secrets',
    namespace: 'shop-prod',
    name: 'db-credentials',
    label: 'db-credentials',
    mounts: ['/etc/creds', '+2 more'],
    linkable: true,
  },
  { role: 'Volume', label: 'cache', detail: 'emptyDir', linkable: false },
  { role: 'Selects', kind: 'pods', selector: 'app=api', label: 'app=api', linkable: false },
];

const page: DetailPage = {
  kind: 'pods',
  namespace: 'shop-prod',
  name: 'api-1',
  title: 'Pod',
  tabs: [],
  sections: [
    {
      key: 'summary',
      label: 'Summary',
      state: 'ok',
      fields: [
        { label: 'Status', value: 'Running', tone: 'ok', pill: true, prose: false },
        {
          label: 'PodScheduled',
          value: '0/6 nodes are available: 6 Insufficient cpu.',
          prose: true,
          pill: false,
        },
        { label: 'Node', value: 'node-3', prose: false, pill: false, copy: 'node-3' },
      ],
    },
  ],
  labels: { app: 'api', standalone: '' },
  relations,
  relationsNote: '12 of 40 volumes shown.',
};

async function mount(
  over: Partial<DetailPage> = {},
  chrome: 'drawer' | 'page' = 'drawer',
): Promise<HTMLElement> {
  stubFetch({ [PATH]: { body: { state: 'ok', data: { ...page, ...over } } } });
  const router = createMemoryRouter(
    [
      {
        path: '/c/:cluster/:kind',
        element: (
          <ObjectDetail
            identity={{ cluster: 'prod', kind: 'pods', namespace: 'shop-prod', name: 'api-1' }}
            chrome={chrome}
          />
        ),
      },
      { path: '/health', element: <p>health</p> },
      { path: '*', element: <p>elsewhere</p> },
    ],
    { initialEntries: ['/c/prod/pods'] },
  );
  const view = render(
    <QueryClientProvider client={createQueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  await waitFor(() => {
    expect(view.container.querySelector('[data-section="summary"]')).not.toBeNull();
  });
  return view.container;
}

describe('the relations panel', () => {
  it('lists every relation, linkable or not', async () => {
    // The count is the assertion: dropping the inert ones is the failure
    // that says a pod has three volumes when it has five, and it looks like a
    // shorter list rather than like a bug.
    const container = await mount();
    const rows = container.querySelectorAll('[data-section="relations"] li');
    expect(rows).toHaveLength(relations.length);
  });

  it('draws each relation as a box: an icon, a role, a name, a location', async () => {
    // `.rel` / `.rel-role` / `.rel-name` / `.rel-at`.
    //
    // The assertion is that the four are **separate elements**, because the
    // defect to avoid is one line of text where the role and the name run
    // together in the same size: the row's `textContent` would be identical, so
    // a test reading it could not tell the two renderings apart.
    const container = await mount();
    const rows = [...container.querySelectorAll('[data-section="relations"] li')];

    expect(rows.map((row) => row.querySelector('[data-rel-role]')?.textContent)).toEqual([
      'Controlled by',
      'Node',
      'Secret',
      'Volume',
      'Selects',
    ]);
    // The name, and not the role with it - a name element holding
    // "Controlled byapi-7c9" would be exactly the collapse being fixed.
    expect(rows.map((row) => row.querySelector('[data-rel-name]')?.textContent)).toEqual([
      'api-7c9Running on node-3',
      'node-3',
      'db-credentials/etc/creds+2 more',
      'cacheemptyDir',
      'app=api',
    ]);
  });

  it('draws the location, and nothing at all where the target has no namespace', async () => {
    // `.rel-at`, pushed to the right edge. The **target's** namespace: a Node, a
    // PersistentVolume and a StorageClass are cluster-scoped and carry none, and
    // an empty element at the right edge of those rows would read as a namespace
    // that could not be read rather than as one that does not exist - the
    // same collapse as the inert row, one column over.
    const container = await mount();
    const rows = [...container.querySelectorAll('[data-section="relations"] li')];
    const at = rows.map((row) => row.querySelector('[data-rel-at]'));

    // The ReplicaSet and the Secret. The Node is cluster-scoped; the emptyDir
    // and the selector have no target at all.
    expect(at.map((node) => node?.textContent ?? null)).toEqual([
      'shop-prod',
      null,
      'shop-prod',
      null,
      null,
    ]);
    // And not merely blank: on those three rows there is no element to be blank.
    expect(container.querySelectorAll('[data-rel-at]')).toHaveLength(2);
  });

  it('draws the sprite shape its role maps to, on every row including the inert ones', async () => {
    // A role the map does not know gets the general shape rather than nothing,
    // which `Controlled by` is.
    //
    // The `<use>` reference is what is asserted rather than a rendered glyph:
    // the sprite is mounted by the Shell and this panel renders without one, so
    // jsdom draws nothing at all. Which symbol was asked for is the decision
    // this file owns; whether the sheet is in the document is `Icon.test.tsx`'s.
    const container = await mount();
    const rows = [...container.querySelectorAll('[data-section="relations"] li')];
    expect(rows.map((row) => row.querySelector('use')?.getAttribute('href'))).toEqual([
      '#i-resource',
      '#i-node',
      '#i-secret',
      '#i-volume',
      '#i-resource',
    ]);
  });

  it('draws a relation with nothing behind it inert, with the same role beside it', async () => {
    const container = await mount();
    const inert = [...container.querySelectorAll('[data-section="relations"] [data-inert="true"]')];
    // The emptyDir, and nothing else: the selector is a filtered list,
    // pinned on its own below.
    expect(inert).toHaveLength(1);
    expect(inert.map((node) => node.textContent)).toEqual([expect.stringContaining('Volume')]);
    // No href on either, and the role is still there: same icon, same role, no
    // link that failed.
    for (const node of inert) {
      expect(node.querySelector('a')).toBeNull();
    }
  });

  it('addresses a namespaced target under its namespace and a cluster-scoped one without', async () => {
    // The trap here is the other object's scope rather than this one's: a Node is cluster-scoped, and giving it this pod's
    // namespace produces a URL that passes every validator and answers "not
    // found" for a machine that is right there.
    //
    // On the object's own page the relation is the related object's own route.
    const container = await mount({}, 'page');
    const hrefs = [...container.querySelectorAll('[data-section="relations"] a:not([data-selects])')].map((a) =>
      a.getAttribute('href'),
    );
    expect(hrefs).toEqual([
      '/c/prod/replicasets/shop-prod/api-7c9',
      '/c/prod/nodes/node-3',
      '/c/prod/secrets/shop-prod/db-credentials',
    ]);
  });

  it('addresses the related list from the drawer, narrowed to the namespace where there is one', async () => {
    // From the drawer a relation lands on the related kind's list with the
    // object opened there. The same trap, on a different address: a cluster-scoped target gets no
    // `namespace` parameter at all, not an empty one and not this pod's.
    const container = await mount();
    const hrefs = [...container.querySelectorAll('[data-section="relations"] a:not([data-selects])')].map((a) =>
      a.getAttribute('href'),
    );
    expect(hrefs).toEqual([
      '/c/prod/replicasets?namespace=shop-prod&open=api-7c9',
      '/c/prod/nodes?open=node-3',
      '/c/prod/secrets?namespace=shop-prod&open=db-credentials',
    ]);
  });

  it.each(['drawer', 'page'] as const)(
    "links a selector to the selected kind's list, filtered and narrowed to the namespace (%s)",
    async (chrome) => {
      // On a Deployment, a ReplicaSet or a Job, "Selects app=…" must not be
      // inert, or a controller reaches its owner and never its pods. The server
      // still says `linkable: false` - there is no one object - and the row
      // opens the list the selector describes instead, in its namespace.
      const container = await mount(
        {
          relations: [
            {
              role: 'Selects',
              kind: 'pods',
              namespace: 'shop-prod',
              selector: 'app=api,tier in (web)',
              label: 'app=api,tier in (web)',
              linkable: false,
            },
          ],
        },
        chrome,
      );
      const link = container.querySelector('[data-section="relations"] a[data-selects]');
      expect(link?.getAttribute('href')).toBe(
        '/c/prod/pods?namespace=shop-prod&selector=app%3Dapi%2Ctier+in+%28web%29',
      );
      expect(link?.textContent).toContain('Selects');
      expect(container.querySelector('[data-inert="true"]')).toBeNull();
    },
  );

  it('draws the mount paths, and the cap element with them', async () => {
    // "Which secret is at /etc/creds" is the question the panel is opened
    // with; the `+N more` element is the producer's cap and travels as one of
    // the mounts, so a list that drops it is a list that was cut in silence.
    const container = await mount();
    const mounts = [...container.querySelectorAll('[data-mount]')].map((node) => node.textContent);
    expect(mounts).toEqual(['/etc/creds', '+2 more']);
    // Breakable after each slash and nowhere else: a path that broke anywhere
    // would read `serviceacco` / `unt`. The text is unchanged.
    expect(container.querySelector('[data-mount]')?.querySelectorAll('wbr').length).toBe(2);
  });

  it('says the list was capped', async () => {
    // `DetailSection.state` expresses "this could not be read"; it does not
    // express "this was read and cut", which is why the note is its own member.
    const container = await mount();
    expect(container.querySelector('[data-relations-note]')?.textContent).toBe(
      '12 of 40 volumes shown.',
    );
  });

  it('reads `linkable` and does not re-derive it', async () => {
    // The server's answer wins even where the client could compute a different
    // one. A relation carrying a kind and a name and `linkable: false` is what a
    // kind this cluster does not serve looks like, and a client re-deriving
    // "kind and name are both set" would draw a link to a list that 404s.
    const container = await mount({
      relations: [
        {
          role: 'Config',
          kind: 'configmaps',
          namespace: 'shop-prod',
          name: 'settings',
          label: 'settings',
          linkable: false,
        },
      ],
    });
    expect(container.querySelector('[data-section="relations"] a')).toBeNull();
    expect(container.querySelector('[data-inert="true"]')?.textContent).toContain('settings');
  });

  it('draws nothing at all when there are no relations and nothing was cut', async () => {
    const container = await mount({ relations: [], relationsNote: '' });
    expect(container.querySelector('[data-section="relations"]')).toBeNull();
  });
});

describe('the labels', () => {
  it('draws one chip per label, and a label with no value as the key alone', async () => {
    // Chips rather than a table, because a set is scanned and not read. `app=api` and `standalone` say different things and
    // `standalone=` says a third that is true of neither.
    const container = await mount();
    const chips = [...container.querySelectorAll('[data-chip]')].map((node) => node.textContent);
    expect(chips).toEqual(['app=api', 'standalone']);
  });
});

describe('a fact has three renderings', () => {
  it('draws a status as a badge and a plain value as text', async () => {
    // Collapsing the badge into colored text makes "Running" and a pod's node
    // name look like the same class of thing.
    const container = await mount();
    const pills = [...container.querySelectorAll('[data-pill]')].map((n) => n.textContent);
    expect(pills).toEqual(['Running']);
  });

  it('lets a sentence wrap, by putting the class on the cell', async () => {
    /*
      The mechanism is inheritance: `white-space` inherits into everything inside
      the cell, so `overflow-wrap` on a descendant is inert - it says *where* a
      line may break and `nowrap` says there are none to break.

      So what is asserted is the element the class is on, not that a class exists
      somewhere in the row. jsdom computes no layout, so the geometry cannot be
      measured here; what can be pinned is the element carrying the class.
    */
    const container = await mount();
    const cells = [...container.querySelectorAll('[data-section="summary"] td')];
    const wrapped = cells.filter((cell) => cell.classList.contains('message'));
    expect(wrapped).toHaveLength(1);
    expect(wrapped[0]?.textContent).toContain('Insufficient cpu');

    // And a wrap class on a descendant would be the inert fix: no child of any
    // cell carries one.
    for (const cell of cells) {
      expect(cell.querySelector('.message, .prose, .wrap')).toBeNull();
    }
  });

  it('offers a copy control only on a fact that says what to copy', async () => {
    const container = await mount();
    const controls = container.querySelectorAll('[data-copy-state]');
    expect(controls).toHaveLength(1);
  });
});

/**
 * The three answers the reverse walk can give, and they must render as three.
 *
 * A ConfigMap names nothing, so a panel returning `null` on one reads as *the
 * feature does not exist*. What replaces it must not overcorrect into the other
 * direction: **"nothing uses this" is the commonest answer** and it is the one
 * somebody deletes an object on, while a walk cannot establish it - a ConfigMap picked up by label
 * is named in no object at all.
 */
describe('what the reverse walk found', () => {
  const NOTHING_FOUND =
    'Nothing that was looked at names this ConfigMap. That is not the same as unused: ' +
    'a controller can pick a ConfigMap up by label without naming it in any object at all.';

  it('draws the sentence over a ConfigMap nothing references', async () => {
    const container = await mount({
      relations: [],
      relationsNote: '',
      usedBy: { state: 'empty', reason: { code: 'empty_scope', message: NOTHING_FOUND } },
    });
    const panel = container.querySelector('[data-section="relations"]');
    expect(panel).not.toBeNull();
    const said = container.querySelector('[data-used-by="empty"]');
    expect(said?.textContent).toContain('not the same as unused');
    // The whole string, never a shortened one: the caveat is the half a
    // user has to read, and a client that trimmed it would be deciding on
    // their behalf that it did not matter.
    expect(said?.textContent).toBe(NOTHING_FOUND);
  });

  it('marks a refusal as a refusal rather than as an absence', async () => {
    const container = await mount({
      relations: [],
      relationsNote: '',
      usedBy: {
        state: 'forbidden',
        reason: {
          code: 'verb_denied',
          message: 'Who uses this configmap could not be established: Deployments could not be listed.',
        },
      },
    });
    // The state is on the element, so the two are distinguishable without
    // reading a color - and the empty attribute must not be what matches.
    expect(container.querySelector('[data-used-by="empty"]')).toBeNull();
    const said = container.querySelector('[data-used-by="forbidden"]');
    expect(said?.textContent).toContain('could not be listed');
  });

  it('says nothing of its own when the rows are the answer', async () => {
    const container = await mount({
      relations: [
        {
          role: 'Used by',
          kind: 'deployments',
          namespace: 'shop-prod',
          name: 'api',
          label: 'Deployment/api',
          detail: 'volume conf',
          mounts: ['/etc/api'],
          linkable: true,
        },
      ],
      relationsNote: '',
      usedBy: { state: 'ok' },
    });
    expect(container.querySelector('[data-used-by]')).toBeNull();
    const rows = container.querySelectorAll('[data-section="relations"] li');
    expect(rows).toHaveLength(1);
    expect(rows[0]?.querySelector('[data-rel-role]')?.textContent).toBe('Used by');
    // The cap is a different sentence from the answer and lives where "what was
    // left out" already lives.
    expect(container.querySelector('[data-relations-note]')).toBeNull();
  });

  it('draws the cap and the answer as two separate sentences', async () => {
    const container = await mount({
      relationsNote: '7 more objects use this configmap and are not shown.',
      usedBy: { state: 'ok' },
    });
    expect(container.querySelector('[data-relations-note]')?.textContent).toContain('7 more objects');
    expect(container.querySelector('[data-used-by]')).toBeNull();
  });

  it('draws nothing at all on a kind the walk does not run for', async () => {
    const container = await mount({ relations: [], relationsNote: '' });
    expect(container.querySelector('[data-section="relations"]')).toBeNull();
  });
});
