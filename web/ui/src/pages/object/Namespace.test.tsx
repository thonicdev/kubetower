import { fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { mountApp, stubFetch, type StubRoutes } from '../../testing/harness';

/**
 * The Namespace page: a namespace can be deleted from the console, and a
 * Terminating one shows the conditions that say what it is waiting for. The
 * server offers Delete on a namespace and sends every condition as a fact;
 * these pin that the page draws both and that Delete sends
 * the cluster-scoped object - no namespace of its own - to the plan and the act.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

const OBJECT = '/api/c/prod/r/namespaces/kubeadjust';

/** The facts the server sends for a namespace stuck on a finalizer, as a kind
 *  cluster wrote them. */
const terminating = [
  { label: 'Kind', value: 'Namespace' },
  { label: 'Name', value: 'kubeadjust' },
  {
    label: 'Deleting since',
    value: '2026-11-02T21:53:39+01:00 — held by kubernetes (spec)',
    tone: 'bad',
    pill: true,
  },
  { label: 'Phase', value: 'Terminating', tone: 'warn', pill: true },
  {
    label: 'NamespaceDeletionContentFailure',
    value:
      'False — ContentDeleted: All content successfully deleted, may be waiting on finalization' +
      ' · since 2026-11-02T21:53:45+01:00 (6m ago)',
    prose: true,
  },
  {
    label: 'NamespaceContentRemaining',
    value:
      'True — SomeResourcesRemain: Some resources are remaining: configmaps. has 1 resource instances' +
      ' · since 2026-11-02T21:53:45+01:00 (6m ago)',
    tone: 'warn',
    prose: true,
  },
  {
    label: 'NamespaceFinalizersRemaining',
    value:
      'True — SomeFinalizersRemain: Some content in the namespace has finalizers remaining: ' +
      'example.com/block in 1 resource instances · since 2026-11-02T21:53:45+01:00 (6m ago)',
    tone: 'warn',
    prose: true,
  },
];

function routes(extra: Record<string, unknown> = {}): StubRoutes {
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
    '/api/capabilities': { body: { mode: 'desktop' } },
    [OBJECT]: {
      body: {
        state: 'ok',
        data: {
          kind: 'namespaces',
          name: 'kubeadjust',
          title: 'Namespace',
          tabs: [],
          sections: [{ key: 'summary', label: 'Summary', state: 'ok', fields: terminating }],
          actions: [
            { verb: 'delete', label: 'Delete', permitted: true, destructive: true, irreversible: true },
          ],
          ...extra,
        },
      },
    },
    '/api/c/prod/actions/preview': {
      body: {
        token: 'plan-ns',
        expiresAt: '2999-01-01T00:00:00Z',
        sentence: 'Are you sure?',
        facts: {
          verb: 'delete',
          button: 'Delete',
          count: 1,
          destructive: true,
          targets: [{ kind: 'namespaces', name: 'kubeadjust' }],
          precondition: '12',
          confirmation: { kind: 'acknowledge', expected: 'confirm' },
          forceable: true,
        },
      },
    },
  };
}

/** Every POST, recorded; the act answers one done. */
function recordPosts(): { path: string; body: unknown }[] {
  const posted: { path: string; body: unknown }[] = [];
  stubFetch(routes());
  const stubbed = globalThis.fetch;
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(
      typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url,
    );
    if (init?.method === 'POST') posted.push({ path, body: JSON.parse(String(init.body)) });
    if (init?.method === 'POST' && path === '/api/c/prod/actions') {
      return Promise.resolve(
        new Response(
          JSON.stringify({
            succeeded: 1,
            failed: 0,
            results: [{ target: { kind: 'namespaces', name: 'kubeadjust' }, ok: true }],
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
      );
    }
    return stubbed(input, init);
  });
  return posted;
}

function page(): HTMLElement {
  const element = document.getElementById('page');
  if (element === null) throw new Error('#page is missing');
  return element;
}

describe('the Namespace page', () => {
  it('offers Delete, confirms it, and sends the cluster-scoped namespace to plan and act', async () => {
    const posted = recordPosts();
    mountApp('/c/prod/namespaces/kubeadjust');
    await waitFor(() => {
      expect(page().querySelector('[aria-label="Actions"]')).not.toBeNull();
    });
    const verb = [...page().querySelectorAll('[aria-label="Actions"] button')].find(
      (one) => one.textContent === 'Delete',
    );
    if (verb === undefined) throw new Error('no Delete on the namespace page');
    fireEvent.click(verb);

    await waitFor(() => {
      expect(document.querySelector('[role="dialog"]')?.textContent).toContain('Are you sure?');
    });
    expect(posted).toEqual([
      {
        path: '/api/c/prod/actions/preview',
        body: { verb: 'delete', target: { kind: 'namespaces', name: 'kubeadjust' } },
      },
    ]);

    const commit = document.querySelector<HTMLButtonElement>('[role="dialog"] [data-commit]');
    if (commit === null) throw new Error('no commit control in the dialog');
    fireEvent.click(commit);
    await waitFor(() => {
      expect(posted.map((one) => one.path)).toContain('/api/c/prod/actions');
    });
    expect(posted.find((one) => one.path === '/api/c/prod/actions')?.body).toEqual({ token: 'plan-ns' });
  });

  it('lists a Terminating namespace’s conditions: status, reason, message, time', async () => {
    stubFetch(routes());
    mountApp('/c/prod/namespaces/kubeadjust');
    await waitFor(() => {
      expect(page().textContent).toContain('NamespaceFinalizersRemaining');
    });

    const rows = new Map(
      [...page().querySelectorAll('tr')].map((row) => [
        row.querySelector('th')?.textContent ?? '',
        row.querySelector('td'),
      ]),
    );
    for (const [label, parts] of [
      ['Phase', ['Terminating']],
      [
        'NamespaceContentRemaining',
        ['True — SomeResourcesRemain', 'configmaps. has 1 resource instances', 'since 2026-11-02'],
      ],
      [
        'NamespaceFinalizersRemaining',
        ['True — SomeFinalizersRemain', 'example.com/block in 1 resource instances'],
      ],
      ['NamespaceDeletionContentFailure', ['False — ContentDeleted', 'may be waiting on finalization']],
    ] as const) {
      const cell = rows.get(label);
      if (cell === undefined || cell === null) throw new Error(`no row for ${label}`);
      for (const part of parts) expect(cell.textContent).toContain(part);
    }
    // A condition's message wraps; its long label holds one line, capped and
    // cut, whole in its title. The geometry is
    // `facts.spec.ts`'s: jsdom lays nothing out.
    expect(rows.get('NamespaceFinalizersRemaining')?.className.split(' ')).toEqual(
      expect.arrayContaining(['message', 'wrap']),
    );
    const label = [...page().querySelectorAll('th')].find(
      (one) => one.textContent === 'NamespaceFinalizersRemaining',
    );
    expect(label?.className.split(' ')).not.toContain('wrap');
    const span = label?.querySelector('[data-fact-label]');
    expect(span?.getAttribute('title')).toBe('NamespaceFinalizersRemaining');
    expect(span?.className.split(' ')).toEqual(expect.arrayContaining(['truncate', 'max-w-[40cqi]']));
  });
});

/**
 * What bounds the namespace: its ResourceQuotas, used
 * against hard, and its LimitRanges, each a row under Related linked to its own
 * page; nothing where it has none; and the refusal said where a list was
 * refused. The server writes the rows and the sentence; these pin that the page
 * draws each answer and never draws two of them alike.
 */
describe('the Namespace page’s quotas and limit ranges', () => {
  function related(): Element | null {
    return page().querySelector('[data-section="relations"]');
  }

  it('draws each quota and limit range with its figures, linked to its own page', async () => {
    stubFetch(
      routes({
        relations: [
          {
            role: 'Resource quota',
            kind: 'resourcequotas',
            namespace: 'kubeadjust',
            name: 'compute',
            label: 'compute',
            detail: 'requests.cpu 250m/1, pods 2/10',
            linkable: true,
          },
          {
            role: 'Limit range',
            kind: 'limitranges',
            namespace: 'kubeadjust',
            name: 'defaults',
            label: 'defaults',
            detail: 'Container/cpu max=2 req=100m def=500m',
            linkable: true,
          },
        ],
      }),
    );
    mountApp('/c/prod/namespaces/kubeadjust');
    await waitFor(() => {
      expect(related()).not.toBeNull();
    });

    const links = [...(related()?.querySelectorAll('a') ?? [])];
    const quota = links.find((one) => one.getAttribute('href') === '/c/prod/resourcequotas/kubeadjust/compute');
    const range = links.find((one) => one.getAttribute('href') === '/c/prod/limitranges/kubeadjust/defaults');
    if (quota === undefined || range === undefined) {
      throw new Error(`links: ${links.map((one) => one.getAttribute('href') ?? '').join(', ')}`);
    }
    expect(quota.querySelector('[data-rel-role]')?.textContent).toBe('Resource quota');
    expect(quota.textContent).toContain('requests.cpu 250m/1, pods 2/10');
    expect(range.querySelector('[data-rel-role]')?.textContent).toBe('Limit range');
    expect(range.textContent).toContain('Container/cpu max=2 req=100m def=500m');
    // The menu's glyph for both kinds, not the general shape.
    expect(quota.querySelector('use')?.getAttribute('href')).toBe('#i-quota');
    expect(range.querySelector('use')?.getAttribute('href')).toBe('#i-quota');
  });

  it('draws no Related section on a namespace that has neither', async () => {
    stubFetch(routes());
    mountApp('/c/prod/namespaces/kubeadjust');
    await waitFor(() => {
      expect(page().textContent).toContain('NamespaceFinalizersRemaining');
    });
    expect(related()).toBeNull();
  });

  it('says a refused list was refused, with no row', async () => {
    const refused =
      'ResourceQuotas and LimitRanges in this namespace could not be listed: ' +
      'the cluster says you may not list resourcequotas in namespace kubeadjust.';
    stubFetch(routes({ relationsNote: refused }));
    mountApp('/c/prod/namespaces/kubeadjust');
    await waitFor(() => {
      expect(related()).not.toBeNull();
    });
    expect(related()?.querySelector('[data-relations-note]')?.textContent).toBe(refused);
    expect(related()?.querySelectorAll('li')).toHaveLength(0);
  });
});
