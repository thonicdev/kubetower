import { act, fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { mountApp, stubFetch, type StubRoutes } from '../testing/harness';

afterEach(() => {
  vi.unstubAllGlobals();
});

const session = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

const clusters = { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } };

const pods = {
  kind: 'pods',
  label: 'Pods',
  namespaced: true,
  columns: [
    { key: 'name', label: 'Name', visible: true, order: 0, sortable: true, role: 'name' },
  ],
  rows: [
    { id: 'argo/api-0', name: 'api-0', namespace: 'argo', cells: { name: { text: 'api-0' } } },
    { id: 'argo/api-1', name: 'api-1', namespace: 'argo', cells: { name: { text: 'api-1' } } },
  ],
  // What `bulkAffordance` sends: a verb list already answered permitted or not,
  // per kind and per cluster.
  bulk: {
    scope: 'prod|pods',
    verbs: [
      { verb: 'delete', label: 'Delete', permitted: true },
      {
        verb: 'evict',
        label: 'Evict',
        permitted: false,
        reason: { code: 'read_denied', message: 'this cluster is read-only' },
      },
    ],
  },
};

function shell(extra: StubRoutes = {}): StubRoutes {
  return {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/capabilities': { body: { mode: 'desktop' } },
    '/api/c/prod/r/pods': { body: { state: 'ok', data: pods } },
    ...extra,
  };
}

function bar(): HTMLElement {
  const element = document.getElementById('bulk-bar');
  if (element === null) throw new Error('#bulk-bar is missing');
  return element;
}

/** A control on the bar itself, never the dialog's.
 *
 *  The bar's `Delete` and the dialog's "Delete 1 pod" differ today, but a
 *  lookup that took the first in document order would be ambiguous the day
 *  they did not; scoping each lookup says which surface is being asserted
 *  about. */
function control(name: string): HTMLButtonElement {
  const found = [...bar().querySelectorAll('button')].find((one) => one.textContent === name);
  if (found === undefined) throw new Error(`no control called ${name} on the bar`);
  return found;
}

/** Every control on the bar, in the order they are drawn. */
function controls(): string[] {
  return [...bar().querySelectorAll('button')].map((one) => one.textContent ?? '');
}

function inDialog(name: string): HTMLButtonElement {
  const dialog = document.querySelector('[role="dialog"]');
  if (dialog === null) throw new Error('no dialog');
  const found = [...dialog.querySelectorAll('button')].find((one) => one.textContent === name);
  if (found === undefined) throw new Error(`no control called ${name} in the dialog`);
  return found;
}

async function tickTheFirstRow(): Promise<void> {
  await waitFor(() => {
    expect(document.querySelector('tbody td')).not.toBeNull();
  });
  const box = document.querySelector('tbody input[type="checkbox"]');
  if (box === null) throw new Error('no row checkbox');
  fireEvent.click(box);
  await waitFor(() => {
    expect(document.getElementById('bulk-bar')).not.toBeNull();
  });
}

/**
 * Ticking rows offers the verbs, not only Clear: the server sends the verb
 * list - `ListPage.bulk`, per kind, each verb already answered permitted or not
 * - and the act is bound to a plan by a token.
 */
describe('the selection bar covers no row', () => {
  it('gives the panel room after its last row while rows are ticked', async () => {
    // The bar floats over the panel's foot so that a tick moves nothing; until
    // this it sat over the last row with no way to scroll that row clear.
    stubFetch(shell());
    mountApp('/c/prod/pods');
    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    expect(document.getElementById('panel')?.className).not.toContain('pb-16');
    await tickTheFirstRow();
    expect(document.getElementById('panel')?.className).toContain('pb-16');
  });
});

describe('the selection bar offers what the server offered', () => {
  it('draws a button per verb, and never one it invented', async () => {
    stubFetch(shell());
    mountApp('/c/prod/pods');
    await tickTheFirstRow();

    expect(bar().textContent).toContain('Delete');
    expect(bar().textContent).toContain('Evict');
    // Nothing else. A verb list derived from the kind is how every list came to
    // be offered every verb - Cordon on a Secrets table.
    expect(bar().textContent).not.toContain('Restart');
    expect(bar().textContent).not.toContain('Drain');
  });

  it('disables a verb the server refused, and carries its reason', async () => {
    stubFetch(shell());
    mountApp('/c/prod/pods');
    await tickTheFirstRow();

    const evict = control('Evict');
    // Disabled, not hidden: an operator looking for it learns why rather than
    // wondering whether they misremembered.
    expect(evict.hasAttribute('disabled')).toBe(true);
    expect(evict.getAttribute('title')).toContain('this cluster is read-only');
  });
});

describe('a verb previews before it acts, and the act is bound to the plan', () => {
  const plan = {
    body: {
      token: 'plan-9',
      expiresAt: '2999-01-01T00:00:00Z',
      sentence: 'Delete 1 pod in argo.',
      facts: {
        verb: 'delete',
        // What `PreviewBulk` composes: `spec.Label`, the count the server
        // resolved and `api.KindWord`. `delete` is what goes on the wire.
        button: 'Delete 1 pod',
        count: 1,
        destructive: true,
        irreversible: true,
        targets: [{ kind: 'pods', namespace: 'argo', name: 'api-0' }],
        confirmation: { kind: 'count', expected: '1', label: 'Type 1 to confirm' },
      },
    },
  };

  it('posts the selection to the preview, and nothing else', async () => {
    const posted: { path: string; body: unknown }[] = [];
    stubFetch(shell({ '/api/c/prod/bulk/preview': plan }));
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if (init?.method === 'POST') posted.push({ path, body: JSON.parse(String(init.body)) });
      return stubbed(input, init);
    });

    mountApp('/c/prod/pods');
    await tickTheFirstRow();
    fireEvent.click(control('Delete'));

    await waitFor(() => {
      expect(document.body.textContent).toContain('Delete 1 pod in argo.');
    });
    expect(posted.map((one) => one.path)).toEqual(['/api/c/prod/bulk/preview']);
    // The target is built from the row, never by splitting the id: `Row.id` is
    // the server's and its shape is not this client's to know.
    expect(posted[0]?.body).toEqual({
      verb: 'delete',
      kind: 'pods',
      targets: [{ kind: 'pods', namespace: 'argo', name: 'api-0' }],
    });
  });

  it('refuses to act until the gesture the plan asked for is made', async () => {
    stubFetch(shell({ '/api/c/prod/bulk/preview': plan }));
    mountApp('/c/prod/pods');
    await tickTheFirstRow();
    fireEvent.click(control('Delete'));

    await waitFor(() => {
      expect(document.body.textContent).toContain('Delete 1 pod in argo.');
    });
    // `confirmation.kind: count` - the number of targets, typed back. It is the
    // figure that is wrong when the set has changed under the operator.
    expect(inDialog('Delete 1 pod').hasAttribute('disabled')).toBe(true);

    const field = document.querySelector('input[type="text"]');
    fireEvent.change(field!, { target: { value: '1' } });
    await waitFor(() => {
      expect(inDialog('Delete 1 pod').hasAttribute('disabled')).toBe(false);
    });
  });

  it('sends the token and the gesture, and reports what came back', async () => {
    const posted: { path: string; body: unknown }[] = [];
    stubFetch(
      shell({
        '/api/c/prod/bulk/preview': plan,
        '/api/c/prod/bulk': {
          body: {
            succeeded: 1,
            failed: 1,
            summary: '1 deleted, 1 refused.',
            results: [
              { target: { kind: 'pods', namespace: 'argo', name: 'api-0' }, ok: true },
              {
                target: { kind: 'pods', namespace: 'argo', name: 'api-1' },
                ok: false,
                message: 'a disruption budget refused it',
              },
            ],
          },
        },
      }),
    );
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if (init?.method === 'POST') posted.push({ path, body: JSON.parse(String(init.body)) });
      return stubbed(input, init);
    });

    mountApp('/c/prod/pods');
    await tickTheFirstRow();
    fireEvent.click(control('Delete'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('Delete 1 pod in argo.');
    });
    fireEvent.change(document.querySelector('input[type="text"]')!, { target: { value: '1' } });
    fireEvent.click(inDialog('Delete 1 pod'));

    // Partial success is the normal case and has to say which half.
    await waitFor(() => {
      expect(document.body.textContent).toContain('1 deleted, 1 refused.');
    });
    expect(document.body.textContent).toContain('a disruption budget refused it');

    // The token and the gesture, and **not the selection again**: the facts the
    // operator agreed to were re-derived server-side when the plan was made.
    expect(posted[1]?.path).toBe('/api/c/prod/bulk');
    expect(posted[1]?.body).toEqual({ token: 'plan-9', confirmed: '1' });
  });
});

/**
 * The red comes from the flag and never from the word.
 *
 * `BulkAffordance.verbs` carries `destructive`, as `DetailAction` does -
 * without it **Delete, Restart and Clear would be drawn identically**, on the
 * control that acts on twelve objects at once.
 *
 * The two rows below are the assertion that matters. A bar that colored by
 * string would redden `delete-snapshot` - a custom resource's ordinary verb -
 * and leave a destructive `recycle` looking like Clear, and neither mistake is
 * visible until the day it is expensive.
 */
describe('a destructive verb is drawn destructive, from the flag', () => {
  const misleading = {
    ...pods,
    bulk: {
      scope: 'prod|widgets',
      verbs: [
        // Reads as a delete, is not one.
        { verb: 'delete-snapshot', label: 'Delete snapshot', permitted: true },
        // Reads as housekeeping, destroys something.
        { verb: 'recycle', label: 'Recycle', permitted: true, destructive: true },
      ],
    },
  };

  it('reddens the flagged verb and leaves the one that only sounds like one alone', async () => {
    stubFetch(shell({ '/api/c/prod/r/pods': { body: { state: 'ok', data: misleading } } }));
    mountApp('/c/prod/pods');
    await tickTheFirstRow();

    expect(control('Recycle').dataset['destructive']).toBe('');
    expect(control('Recycle').className).toContain('bg-bad');
    expect(control('Delete snapshot').dataset['destructive']).toBeUndefined();
    expect(control('Delete snapshot').className).not.toContain('bg-bad');
  });

  it('intensifies the flagged verb on hover and takes the other two nowhere near it', async () => {
    /*
      The same assertion `Verbs.test.tsx` makes on the drawer - deliberately,
      because the red comes from one constant and a change made in one file must
      be visible in both. What would otherwise pass here is a bar whose hover was
      written by hand beside `destructive`, a second list.

      `Recycle` is flagged and does not sound it; `Delete snapshot` sounds it and
      is not. The hover follows the flag, as the rest state already does.
    */
    stubFetch(shell({ '/api/c/prod/r/pods': { body: { state: 'ok', data: misleading } } }));
    mountApp('/c/prod/pods');
    await tickTheFirstRow();

    const red = control('Recycle').className.split(/\s+/);
    expect(red).toContain('hover:border-bad');
    expect(red).toContain('hover:bg-bad/20');
    // Not a flat fill: on a control already drawn in red at rest, that reads as
    // pressed rather than as hovered.
    expect(red).not.toContain('hover:bg-bad');

    const sounds = control('Delete snapshot').className.split(/\s+/);
    expect(sounds).not.toContain('hover:border-bad');
    expect(sounds).toContain('hover:text-fg');
  });

  it('leaves Clear as the ordinary control it is', async () => {
    stubFetch(shell({ '/api/c/prod/r/pods': { body: { state: 'ok', data: misleading } } }));
    mountApp('/c/prod/pods');
    await tickTheFirstRow();

    expect(control('Clear').className).not.toContain('bg-bad');
  });

  /**
   * And an ordinary control is not a control with no hover at all.
   *
   * The two verb buttons beside Clear each answer the pointer - the flagged one
   * in red, the ordinary one with `hover:text-fg`. On a bar whose every other
   * control lights up, the one that does not reads as disabled.
   *
   * **`hover:text-fg` and not `hover:border-accent`.** The accent frame is
   * `hover.ts`'s `watching`: `border-accent` already means *this is the one you
   * are on*. Clear acts on the selection rather than on the cluster - which is
   * why it is first - so it takes the console's neutral treatment.
   */
  it('answers the pointer on Clear, in the console’s neutral treatment', async () => {
    stubFetch(shell({ '/api/c/prod/r/pods': { body: { state: 'ok', data: misleading } } }));
    mountApp('/c/prod/pods');
    await tickTheFirstRow();

    const classes = control('Clear').className.split(/\s+/);
    expect(classes).toContain('hover:text-fg');
    expect(classes).not.toContain('hover:border-accent');
    expect(classes).not.toContain('hover:border-bad');
  });
});

/**
 * The order: the way out, then the verbs, non-destructive before destructive.
 *
 * Written as a rule and not as a list, because "Clear, Evict, Delete" is right
 * for Pods and says nothing about a ConfigMap list, which
 * offers Delete alone. Bulk acts on everything ticked, so a Delete that sits third
 * on one kind and first on another is a hazard muscle memory walks into.
 *
 * Clear is at the front because it is **not a verb**: it acts on the selection
 * and never on the cluster, and drawn last it put the cheapest control on the far
 * side of the most expensive one.
 */
describe('the bar is ordered: the way out, then the verbs by severity', () => {
  /** What `verbsFor` sends for a pod list: Restart dropped, Evict then Delete,
   *  both of them destructive. */
  const podBar = {
    ...pods,
    bulk: {
      scope: 'prod|pods',
      verbs: [
        { verb: 'evict', label: 'Evict', permitted: true, destructive: true },
        { verb: 'delete', label: 'Delete', permitted: true, destructive: true },
      ],
    },
  };

  /** A kind whose verb set is different - one verb, and it is the destructive
   *  one. The case that shows the rule is a rule rather than a three-item list. */
  const oneVerbBar = {
    ...pods,
    bulk: {
      scope: 'prod|configmaps',
      verbs: [{ verb: 'delete', label: 'Delete', permitted: true, destructive: true }],
    },
  };

  it('draws Clear first, then the verbs the server sent, in the order it sent them', async () => {
    stubFetch(shell({ '/api/c/prod/r/pods': { body: { state: 'ok', data: podBar } } }));
    mountApp('/c/prod/pods');
    await tickTheFirstRow();

    // Evict before Delete is the server's judgment and **cannot** come from the
    // flag: both carry it. A stable sort is what leaves it alone.
    expect(controls()).toEqual(['Clear', 'Evict', 'Delete']);
  });

  it('holds where the verb set is different, which is what makes it a rule', async () => {
    stubFetch(shell({ '/api/c/prod/r/pods': { body: { state: 'ok', data: oneVerbBar } } }));
    mountApp('/c/prod/pods');
    await tickTheFirstRow();

    expect(controls()).toEqual(['Clear', 'Delete']);
  });

  /**
   * The safety net, and the assertion that says what it is keyed on.
   *
   * `recycle` is flagged and reads as housekeeping; `delete-snapshot` is a custom
   * resource's ordinary verb and reads as a delete. A sort keyed on the label
   * would put them in exactly the wrong order, and it would agree with this
   * fixture's server order while doing it - which is why the server sends them
   * the wrong way round here.
   */
  it('puts an ordinary verb before a destructive one, keyed on the flag and not the word', async () => {
    const misordered = {
      ...pods,
      bulk: {
        scope: 'prod|widgets',
        verbs: [
          { verb: 'recycle', label: 'Recycle', permitted: true, destructive: true },
          { verb: 'delete-snapshot', label: 'Delete snapshot', permitted: true },
        ],
      },
    };
    stubFetch(shell({ '/api/c/prod/r/pods': { body: { state: 'ok', data: misordered } } }));
    mountApp('/c/prod/pods');
    await tickTheFirstRow();

    expect(controls()).toEqual(['Clear', 'Delete snapshot', 'Recycle']);
  });
});

/**
 * What a bulk verb leaves behind in the cache.
 *
 * The drawer and the list are two reads of the same object and only one of them
 * refreshes on its own. `resourceListQuery` carries the kind's interval - two
 * seconds on a live kind - so the row is gone from the table within a tick
 * whatever anybody invalidates. `resourceDetailQuery` carries **no**
 * `refetchInterval`, deliberately: a drawer is a thing being read, and the
 * object is not re-read under a reader's eyes. So the object's own read is
 * stale until something says so, and nothing else will.
 *
 * `Verbs.tsx` says the whole of it on the single-object path: *"the object's
 * own read is what the drawer draws, and the kind's list is where the row
 * lives. A verb that removed the object leaves the first answering `absent`,
 * which is a sentence the panel knows how to say."* The bar has to invalidate
 * both too - the operator's gesture is the ordinary one: tick a row, open it,
 * delete the selection from the bar.
 *
 * **The assertion is about the cache and not about the layout.** What is counted
 * is the object's own read happening a second time; what is read back is the
 * sentence the panel says when the answer is `absent`. Neither is a measurement,
 * which in jsdom would be a measurement of zero.
 */
describe('a bulk verb invalidates the objects it acted on', () => {
  const OBJECT = '/api/c/prod/r/pods/argo/api-0';

  const plan = {
    body: {
      token: 'plan-9',
      expiresAt: '2999-01-01T00:00:00Z',
      sentence: 'Delete 1 pod in argo.',
      facts: {
        verb: 'delete',
        button: 'Delete 1 pod',
        count: 1,
        destructive: true,
        irreversible: true,
        targets: [{ kind: 'pods', namespace: 'argo', name: 'api-0' }],
        confirmation: { kind: 'count', expected: '1', label: 'Type 1 to confirm' },
      },
    },
  };

  /** One target, and the server's answer about which one - `results` is per
   *  target always, and is what `apireads.go` builds from the set it resolved
   *  out of the token rather than from what this client ticked. */
  const done = {
    body: {
      succeeded: 1,
      failed: 0,
      summary: '1 deleted.',
      results: [{ target: { kind: 'pods', namespace: 'argo', name: 'api-0' }, ok: true }],
    },
  };

  const detail = {
    kind: 'pods',
    namespace: 'argo',
    name: 'api-0',
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

  /** What the cluster answers once the pod has gone. A value result has no
   *  `empty` arm: a single thing that is not there is `absent`. */
  const gone = {
    state: 'absent',
    reason: { code: 'no_such_object', message: 'This pod is no longer there.' },
  };

  /**
   * The bar's own routes, plus an object read that answers differently once the
   * bulk act has been posted - which is what a cluster does.
   *
   * It returns the addresses the object's own read was asked at, and the count
   * of them is the assertion: one on opening the drawer, and a second one only
   * if something invalidated it.
   */
  function cluster(): string[] {
    const asked: string[] = [];
    let deleted = false;
    stubFetch(shell({ '/api/c/prod/bulk/preview': plan, '/api/c/prod/bulk': done }));
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const address = String(
        typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url,
      );
      const path = address.split('?')[0] ?? address;
      if (init?.method === 'POST' && path === '/api/c/prod/bulk') deleted = true;
      if (path !== OBJECT) return stubbed(input, init);
      asked.push(path);
      return Promise.resolve(
        new Response(JSON.stringify(deleted ? gone : { state: 'ok', data: detail }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    });
    return asked;
  }

  /** The row's own drawer, opened the way the operator opens it: by clicking the
   *  row. The tick is a control inside it and is not the row's action. */
  async function openTheFirstRow(): Promise<void> {
    const cell = [...document.querySelectorAll('tbody td')].find(
      (one) => one.textContent?.trim() === 'api-0',
    );
    const row = cell?.closest('tr');
    if (row === null || row === undefined) throw new Error('no row for api-0');
    await act(async () => {
      fireEvent.click(row);
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(document.getElementById('drawer')?.textContent ?? '').toContain('Running');
    });
  }

  async function deleteTheSelection(): Promise<void> {
    fireEvent.click(control('Delete'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('Delete 1 pod in argo.');
    });
    fireEvent.change(document.querySelector('input[type="text"]')!, { target: { value: '1' } });
    fireEvent.click(inDialog('Delete 1 pod'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('1 deleted.');
    });
  }

  it('re-reads the object a drawer is drawing, which the kind’s list does not cover', async () => {
    const asked = cluster();
    mountApp('/c/prod/pods');
    await tickTheFirstRow();
    await openTheFirstRow();
    expect(asked.length).toBe(1);

    await deleteTheSelection();

    // The object's own key, and it is not under the kind's: `resource` is
    // `['resource', …]` and `resourceKind` is `['resources', …]`, so
    // invalidating the list reaches neither the drawer's read nor the object
    // route's. Two reads, or the panel is still drawing the pod.
    await waitFor(() => {
      expect(asked.length).toBe(2);
    });
  });

  it('leaves the panel saying the object is gone, rather than offering verbs on it', async () => {
    cluster();
    mountApp('/c/prod/pods');
    await tickTheFirstRow();
    await openTheFirstRow();
    await deleteTheSelection();

    await waitFor(() => {
      expect(document.getElementById('drawer')?.textContent ?? '').toContain(
        'This pod is no longer there.',
      );
    });
    // And not the object it was drawing a moment ago.
    expect(document.getElementById('drawer')?.textContent ?? '').not.toContain('Running');
  });
});

describe('a Terminating namespace is offered no Delete', () => {
  const list = (phase: string) => ({
    kind: 'namespaces',
    label: 'Namespaces',
    namespaced: false,
    columns: [
      { key: 'name-0', label: 'Name', visible: true, order: 0, sortable: true, role: 'name' },
      { key: 'status-1', label: 'Status', visible: true, order: 1, sortable: true },
    ],
    // The keys are the server's columnKey(title, index): TestTheNamespacesList-
    // CarriesThePhaseUnderTheStatusColumnKey pins `status-1` from a real object.
    rows: [{ id: 'shop', name: 'shop', cells: { 'name-0': { text: 'shop' }, 'status-1': { text: phase } } }],
    bulk: { scope: 'prod|namespaces', verbs: [{ verb: 'delete', label: 'Delete', permitted: true }] },
  });

  async function tick(phase: string): Promise<void> {
    stubFetch(shell({ '/api/c/prod/r/namespaces': { body: { state: 'ok', data: list(phase) } } }));
    mountApp('/c/prod/namespaces');
    await tickTheFirstRow();
  }

  it('still offers Delete on an Active one', async () => {
    await tick('Active');
    expect(bar().textContent).toContain('Delete');
  });

  it('withholds Delete while a Terminating one is ticked', async () => {
    await tick('Terminating');
    expect(bar().textContent).toContain('1 selected');
    expect(bar().textContent).not.toContain('Delete');
  });
});
