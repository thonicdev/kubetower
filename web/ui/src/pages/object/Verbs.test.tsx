import { fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { mountApp, stubFetch, type StubRoutes } from '../../testing/harness';
import { PIN_CHANGED_HOLD_MS } from '../../components/PlanDialog';

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
const OBJECT = '/api/c/prod/r/pods/argo/api-0';

function detail(actions: unknown[]): StubRoutes[string] {
  return {
    body: {
      state: 'ok',
      data: {
        kind: 'pods',
        namespace: 'argo',
        name: 'api-0',
        title: 'Pod',
        tabs: [],
        sections: [],
        actions,
      },
    },
  };
}

function shell(extra: StubRoutes = {}): StubRoutes {
  return {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/capabilities': { body: { mode: 'desktop' } },
    ...extra,
  };
}

function page(): HTMLElement {
  const element = document.getElementById('page');
  if (element === null) throw new Error('#page is missing');
  return element;
}

function control(name: string): HTMLButtonElement {
  const row = page().querySelector('[aria-label="Actions"]');
  if (row === null) throw new Error('no action row');
  const found = [...row.querySelectorAll('button')].find((one) => one.textContent === name);
  if (found === undefined) throw new Error(`no verb called ${name}`);
  return found;
}

function inDialog(name: string): HTMLButtonElement {
  const dialog = document.querySelector('[role="dialog"]');
  if (dialog === null) throw new Error('no dialog');
  const found = [...dialog.querySelectorAll('button')].find((one) => one.textContent === name);
  if (found === undefined) throw new Error(`no control called ${name} in the dialog`);
  return found;
}

/**
 * The verbs half of an object's panel: `DetailPage.actions` is the server's
 * list, and the drawer draws exactly that.
 */
describe('the verbs on an object are the ones the server sent', () => {
  it('draws them, with the destructive one marked', async () => {
    stubFetch(
      shell({
        [OBJECT]: detail([
          { verb: 'restart', label: 'Restart', permitted: true },
          { verb: 'delete', label: 'Delete', permitted: true, destructive: true },
        ]),
      }),
    );
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(page().querySelector('[aria-label="Actions"]')).not.toBeNull();
    });
    expect(control('Restart')).not.toBeNull();
    // The server's answer, not a word list here: a kind-to-verb table on this
    // side is how every list came to be offered every verb. The style is the
    // console's one destructive style - filled, shared with the
    // plan dialog's commit control and the selection bar - so what is asserted
    // is the flag reaching the class, never the verb's name reaching it.
    expect(control('Delete').dataset['destructive']).toBe('');
    expect(control('Delete').className).toContain('bg-bad');
    expect(control('Restart').dataset['destructive']).toBeUndefined();
    expect(control('Restart').className).not.toContain('bg-bad');
  });

  it('intensifies the destructive one on hover and leaves the ordinary one neutral', async () => {
    /*
      Destructive buttons are **already** red at rest: `destructive.ts` draws
      them `border-bad/45 bg-bad/10 text-bad`. So "red on hover" can only mean
      *intensify*: the frame goes to the full `--bad` while the wash goes from a
      tenth to a fifth.

      **Two failures this pins against, and they are opposite.** Taking the fill
      to a flat `bg-bad` would read as *already pressed* on a control whose rest
      state is colored. Giving it the watching arm's `hover:border-accent` would
      paint the console's one destructive control blue.

      And it comes from the flag, never from the word: `destructive` is set on
      the verb by the server, which is its single producer. `Restart` here is
      the control that proves it - an ordinary verb sitting next to a destructive
      one, taking neither red.
    */
    stubFetch(
      shell({
        [OBJECT]: detail([
          { verb: 'restart', label: 'Restart', permitted: true },
          { verb: 'delete', label: 'Delete', permitted: true, destructive: true },
        ]),
      }),
    );
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(page().querySelector('[aria-label="Actions"]')).not.toBeNull();
    });

    const red = control('Delete').className.split(/\s+/);
    // The frame to full `--bad`, the wash from a tenth to a fifth. Both, which
    // is the pair: the wash alone is not enough.
    expect(red).toContain('hover:border-bad');
    expect(red).toContain('hover:bg-bad/20');
    expect(red).toContain('border-bad/45');
    // Not a fill. `hover:bg-bad` with no opacity reads as pressed.
    expect(red).not.toContain('hover:bg-bad');
    expect(red).not.toContain('hover:bg-bad/100');

    const ordinary = control('Restart').className.split(/\s+/);
    // Outside all three arms, and it stays there: the rule is for what watches,
    // for Claude and for what destroys, and Restart is none of them.
    expect(ordinary).toContain('hover:text-fg');
    expect(ordinary).not.toContain('hover:border-bad');
    expect(ordinary).not.toContain('hover:border-accent');
  });

  it('disables a verb the cluster refuses, and carries the reason', async () => {
    stubFetch(
      shell({
        [OBJECT]: detail([
          {
            verb: 'delete',
            label: 'Delete',
            permitted: false,
            destructive: true,
            reason: { code: 'verb_denied', message: 'this cluster is marked read-only' },
          },
        ]),
      }),
    );
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(page().querySelector('[aria-label="Actions"]')).not.toBeNull();
    });
    expect(control('Delete').hasAttribute('disabled')).toBe(true);
    expect(control('Delete').getAttribute('title')).toContain('read-only');
  });

  it('offers a verb the console could not ask about on the desktop, and says the cluster decides', async () => {
    stubFetch(
      shell({
        [OBJECT]: detail([
          {
            verb: 'delete',
            label: 'Delete',
            permitted: true,
            destructive: true,
            reason: {
              code: 'access_unreviewable',
              message: 'the console could not ask the cluster whether you may do this; it is offered, and the cluster decides when you act',
            },
          },
        ]),
      }),
    );
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(page().querySelector('[aria-label="Actions"]')).not.toBeNull();
    });
    expect(control('Delete').hasAttribute('disabled')).toBe(false);
    expect(control('Delete').getAttribute('title')).toContain('the cluster decides');
  });

  it('draws no row at all where the object has no verbs', async () => {
    stubFetch(shell({ [OBJECT]: detail([]) }));
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(page().textContent).toContain('api-0');
    });
    // The namespaces case. An empty row is a strip of chrome that says a
    // user has options here, which they do not.
    expect(page().querySelector('[aria-label="Actions"]')).toBeNull();
  });
});

describe('a verb previews before it acts', () => {
  // A single delete's plan as `PreviewAction` mints it:
  // "Are you sure?", a button with no name on it, nothing
  // typed, no detail - and `forceable`, which draws the "Force delete" box.
  const plan = {
    body: {
      token: 'plan-3',
      expiresAt: '2999-01-01T00:00:00Z',
      sentence: 'Are you sure?',
      facts: {
        verb: 'delete',
        button: 'Delete',
        count: 1,
        destructive: true,
        targets: [{ kind: 'pods', namespace: 'argo', name: 'api-0' }],
        precondition: '41',
        confirmation: { kind: 'acknowledge', expected: 'confirm' },
        forceable: true,
      },
    },
  };

  /** The page with a Delete verb, the plan above, and every POST recorded; an
   *  act answers one done. */
  function deleting(): { path: string; body: unknown }[] {
    const posted: { path: string; body: unknown }[] = [];
    stubFetch(
      shell({
        [OBJECT]: detail([{ verb: 'delete', label: 'Delete', permitted: true, destructive: true }]),
        '/api/c/prod/actions/preview': plan,
      }),
    );
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if (init?.method === 'POST') posted.push({ path, body: JSON.parse(String(init.body)) });
      if (init?.method === 'POST' && path === '/api/c/prod/actions') {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              succeeded: 1,
              failed: 0,
              results: [{ target: { kind: 'pods', namespace: 'argo', name: 'api-0' }, ok: true }],
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } },
          ),
        );
      }
      return stubbed(input, init);
    });
    return posted;
  }

  async function openDelete(): Promise<HTMLElement> {
    mountApp('/c/prod/pods/argo/api-0');
    await waitFor(() => {
      expect(page().querySelector('[aria-label="Actions"]')).not.toBeNull();
    });
    fireEvent.click(control('Delete'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('Are you sure?');
    });
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]');
    if (dialog === null) throw new Error('no dialog');
    return dialog;
  }

  it('sends the object as the target, and writes nothing until the dialog says so', async () => {
    const posted = deleting();
    await openDelete();
    expect(posted.map((one) => one.path)).toEqual(['/api/c/prod/actions/preview']);
    expect(posted[0]?.body).toEqual({
      verb: 'delete',
      target: { kind: 'pods', namespace: 'argo', name: 'api-0' },
    });
  });

  it('asks about one resource with a simple red modal: no typed name, no detail, Delete and Cancel', async () => {
    deleting();
    const dialog = await openDelete();
    // Red, with no bar down the left edge.
    expect(dialog.getAttribute('data-tone')).toBe('danger');
    expect(dialog.className).not.toContain('inset');
    // Nothing to type, nothing folded under Details.
    expect(dialog.querySelector('input[type="text"]')).toBeNull();
    expect(dialog.querySelector('[data-plan-details]')).toBeNull();
    // The button is "Delete" - not "Delete api-0" - and it is live at once.
    const commit = dialog.querySelector<HTMLButtonElement>('[data-commit]');
    expect(commit?.textContent).toBe('Delete');
    expect(commit?.hasAttribute('disabled')).toBe(false);
    expect(inDialog('Cancel')).toBeDefined();
    expect(dialog.textContent).toContain('Force delete');
  });

  it('deletes nothing on Cancel', async () => {
    const posted = deleting();
    await openDelete();
    fireEvent.click(inDialog('Cancel'));
    await waitFor(() => {
      expect(document.querySelector('[role="dialog"]')).toBeNull();
    });
    expect(posted.map((one) => one.path)).toEqual(['/api/c/prod/actions/preview']);
  });

  it('deletes on Delete, with no force unless the box is ticked', async () => {
    const posted = deleting();
    await openDelete();
    fireEvent.click(inDialog('Delete'));
    await waitFor(() => {
      expect(posted.map((one) => one.path)).toContain('/api/c/prod/actions');
    });
    expect(posted.find((one) => one.path === '/api/c/prod/actions')?.body).toEqual({ token: 'plan-3' });
  });

  it('sends force with the act when "Force delete" is ticked', async () => {
    const posted = deleting();
    const dialog = await openDelete();
    const box = dialog.querySelector<HTMLInputElement>('input[type="checkbox"][data-force]');
    if (box === null) throw new Error('no Force delete box');
    fireEvent.click(box);
    expect(box.checked).toBe(true);
    fireEvent.click(inDialog('Delete'));
    await waitFor(() => {
      expect(posted.map((one) => one.path)).toContain('/api/c/prod/actions');
    });
    expect(posted.find((one) => one.path === '/api/c/prod/actions')?.body).toEqual({ token: 'plan-3', force: true });
  });

  /**
   * The object moved between the preview and the click. The server spends the
   * token and answers 409 with the plan as it stands now; the dialog has to
   * show *that*: keeping the old plan would resend a spent token, and the
   * second refusal would be about the token rather than the object.
   */
  it('adopts the fresh plan a 409 carries, and confirms against it next', async () => {
    const cordon = (token: string, precondition: string) => ({
      token,
      expiresAt: '2999-01-01T00:00:00Z',
      sentence: `Cordon nodes node-a at ${precondition}.`,
      facts: {
        verb: 'cordon',
        button: 'Cordon it',
        count: 1,
        destructive: false,
        targets: [{ kind: 'nodes', name: 'node-a' }],
        precondition,
        confirmation: { kind: 'acknowledge' },
      },
    });
    const NODE = '/api/c/prod/r/nodes/-/node-a';
    const posted: { path: string; body: { token?: string } }[] = [];
    stubFetch(
      shell({
        [NODE]: {
          body: {
            state: 'ok',
            data: {
              kind: 'nodes',
              name: 'node-a',
              title: 'Node',
              tabs: [],
              sections: [],
              actions: [{ verb: 'cordon', label: 'Cordon', permitted: true }],
            },
          },
        },
        '/api/c/prod/actions/preview': { body: cordon('plan-7', '7') },
      }),
    );
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if (init?.method === 'POST' && path === '/api/c/prod/actions') {
        const body = JSON.parse(String(init.body)) as { token?: string };
        posted.push({ path, body });
        if (body.token === 'plan-7') {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                status: 409,
                title: 'The plan no longer describes what would happen',
                code: 'plan_changed',
                detail: 'the object changed since the plan was made: it was at resourceVersion 7 and is at 8 now',
                plan: cordon('plan-8', '8'),
              }),
              { status: 409, headers: { 'Content-Type': 'application/problem+json' } },
            ),
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify({ succeeded: 1, failed: 0, results: [] }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          }),
        );
      }
      return stubbed(input, init);
    });

    mountApp('/c/prod/nodes/-/node-a');
    await waitFor(() => {
      expect(page().querySelector('[aria-label="Actions"]')).not.toBeNull();
    });
    fireEvent.click(control('Cordon'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('Cordon nodes node-a at 7.');
    });

    fireEvent.click(inDialog('Cordon it'));
    // The refusal is read, and so is the fresh plan - the sentence about 8, and
    // the precondition the next act will carry.
    await waitFor(() => {
      expect(document.body.textContent).toContain('is at 8 now');
    });
    expect(document.body.textContent).toContain('Cordon nodes node-a at 8.');
    expect(document.body.textContent).not.toContain('Cordon nodes node-a at 7.');

    // A second click straight after the refusal confirms nothing: the pin moved,
    // so the control is held for a moment.
    fireEvent.click(inDialog('Cordon it'));
    expect(posted.map((one) => one.body.token)).toEqual(['plan-7']);
    await waitFor(
      () => {
        expect(inDialog('Cordon it').hasAttribute('disabled')).toBe(false);
      },
      { timeout: PIN_CHANGED_HOLD_MS + 2000 },
    );

    fireEvent.click(inDialog('Cordon it'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('1 done, 0 refused.');
    });
    expect(posted.map((one) => one.body.token)).toEqual(['plan-7', 'plan-8']);
  });
});

/**
 * A drain is two confirmations: the cordon, then the evictions as a plan of
 * their own, carried on the first stage's outcome as `nextPlan`. Closing on the
 * first outcome would leave a sentence - *confirm again to evict* - that no
 * request could answer.
 */
describe('a verb in stages opens the next plan instead of closing', () => {
  const NODE = '/api/c/prod/r/nodes/-/node-a';
  const cordonPlan = {
    token: 'stage-1',
    expiresAt: '2999-01-01T00:00:00Z',
    sentence: 'Drain nodes node-a.',
    facts: {
      verb: 'drain',
      button: 'Cordon and show me the plan',
      count: 1,
      destructive: true,
      targets: [{ kind: 'nodes', name: 'node-a' }],
      precondition: '7',
      confirmation: { kind: 'name', expected: 'node-a', label: 'Type node-a to confirm' },
    },
  };
  const evictPlan = {
    token: 'stage-2',
    expiresAt: '2999-01-01T00:00:00Z',
    sentence: 'Evict 2 pods. This cannot be undone.',
    facts: {
      verb: 'evict',
      // Composed by `evictionFacts` for the set it resolved, not `spec.Button`
      // - which is "Evict it", and "it" here is every pod on a node.
      button: 'Evict 2 pods',
      count: 2,
      destructive: true,
      targets: [
        { kind: 'pods', namespace: 'prod', name: 'api-1' },
        { kind: 'pods', namespace: 'prod', name: 'bare', note: 'nothing owns this pod' },
      ],
      warnings: ['kube-system/fluent-bit-x is left alone: managed by a DaemonSet'],
      confirmation: { kind: 'name', expected: 'node-a', label: 'Type node-a again to evict 2 pods from it' },
    },
  };

  it('draws the first stage, then the pod list, and evicts only on the second confirmation', async () => {
    const posted: { token?: string; confirmed?: string }[] = [];
    stubFetch(
      shell({
        [NODE]: {
          body: {
            state: 'ok',
            data: {
              kind: 'nodes',
              name: 'node-a',
              title: 'Node',
              tabs: [],
              sections: [],
              actions: [{ verb: 'drain', label: 'Drain', permitted: true, destructive: true }],
            },
          },
        },
        '/api/c/prod/actions/preview': { body: cordonPlan },
      }),
    );
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if (init?.method === 'POST' && path === '/api/c/prod/actions') {
        const body = JSON.parse(String(init.body)) as { token?: string; confirmed?: string };
        posted.push(body);
        const outcome =
          body.token === 'stage-1'
            ? {
                succeeded: 1,
                failed: 0,
                results: [{ target: { kind: 'nodes', name: 'node-a' }, ok: true }],
                summary: 'Cordoned node-a. Nothing has been evicted yet: 2 pod(s) would be.',
                nextPlan: evictPlan,
              }
            : {
                succeeded: 2,
                failed: 0,
                results: [
                  { target: { kind: 'pods', namespace: 'prod', name: 'api-1' }, ok: true, message: 'evicted' },
                  { target: { kind: 'pods', namespace: 'prod', name: 'bare' }, ok: true, message: 'evicted' },
                ],
                summary: 'Requested 2 eviction(s) on node-a.',
              };
        return Promise.resolve(
          new Response(JSON.stringify(outcome), { status: 200, headers: { 'Content-Type': 'application/json' } }),
        );
      }
      return stubbed(input, init);
    });

    mountApp('/c/prod/nodes/-/node-a');
    await waitFor(() => {
      expect(page().querySelector('[aria-label="Actions"]')).not.toBeNull();
    });
    fireEvent.click(control('Drain'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('Drain nodes node-a.');
    });

    // The first stage: the node's name, typed.
    fireEvent.change(document.querySelector('input[type="text"]')!, { target: { value: 'node-a' } });
    await waitFor(() => {
      expect(inDialog('Cordon and show me the plan').hasAttribute('disabled')).toBe(false);
    });
    fireEvent.click(inDialog('Cordon and show me the plan'));

    // The dialog did not close: it opened the second stage, with the first
    // stage's sentence above it and the pods by name below.
    await waitFor(() => {
      expect(document.body.textContent).toContain('Evict 2 pods. This cannot be undone.');
    });
    const text = document.body.textContent ?? '';
    expect(text).toContain('Cordoned node-a. Nothing has been evicted yet');
    expect(text).toContain('prod/api-1');
    expect(text).toContain('prod/bare');
    expect(text).toContain('nothing owns this pod');
    expect(text).toContain('fluent-bit-x is left alone');
    expect(text).not.toContain('1 done, 0 refused.');
    // Nothing has been evicted: only the first token was spent.
    expect(posted.map((one) => one.token)).toEqual(['stage-1']);

    // The name typed for the cordon does not stand for the evictions: the box
    // is empty again and the button waits for it.
    expect((document.querySelector('input[type="text"]') as HTMLInputElement).value).toBe('');
    expect(inDialog('Evict 2 pods').hasAttribute('disabled')).toBe(true);
    fireEvent.change(document.querySelector('input[type="text"]')!, { target: { value: 'node-a' } });
    await waitFor(() => {
      expect(inDialog('Evict 2 pods').hasAttribute('disabled')).toBe(false);
    });
    fireEvent.click(inDialog('Evict 2 pods'));

    await waitFor(() => {
      expect(document.body.textContent).toContain('Requested 2 eviction(s) on node-a.');
    });
    expect(posted).toEqual([
      { token: 'stage-1', confirmed: 'node-a' },
      { token: 'stage-2', confirmed: 'node-a' },
    ]);
  });

  /**
   * A node's plan is refused every minute by its kubelet's status, and a
   * refusal must not clear the name the user has typed. The fresh
   * plan a 409 carries is the same confirmation re-read, so what was typed stays
   * and the next click confirms it - while a next *stage* still starts empty
   * (the test above).
   */
  it('keeps the typed name when a 409 replaces the plan with a fresh one', async () => {
    const posted: { token?: string; confirmed?: string }[] = [];
    const fresh = {
      ...cordonPlan,
      token: 'stage-1-fresh',
      facts: {
        ...cordonPlan.facts,
        precondition: 'unschedulable=false; taints=dedicated=gpu:NoSchedule',
        preconditionOn: 'spec.unschedulable and spec.taints',
      },
    };
    stubFetch(
      shell({
        [NODE]: {
          body: {
            state: 'ok',
            data: {
              kind: 'nodes',
              name: 'node-a',
              title: 'Node',
              tabs: [],
              sections: [],
              actions: [{ verb: 'drain', label: 'Drain', permitted: true, destructive: true }],
            },
          },
        },
        '/api/c/prod/actions/preview': { body: cordonPlan },
      }),
    );
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if (init?.method === 'POST' && path === '/api/c/prod/actions') {
        const body = JSON.parse(String(init.body)) as { token?: string; confirmed?: string };
        posted.push(body);
        if (body.token === 'stage-1') {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                status: 409,
                title: 'The plan no longer describes what would happen',
                code: 'plan_changed',
                detail:
                  'the object changed since the plan was made: its spec.unschedulable and spec.taints were ' +
                  'unschedulable=false; taints=none and are unschedulable=false; taints=dedicated=gpu:NoSchedule now',
                plan: fresh,
              }),
              { status: 409, headers: { 'Content-Type': 'application/problem+json' } },
            ),
          );
        }
        return Promise.resolve(
          new Response(
            JSON.stringify({ succeeded: 1, failed: 0, results: [], summary: 'Cordoned node-a.' }),
            { status: 200, headers: { 'Content-Type': 'application/json' } },
          ),
        );
      }
      return stubbed(input, init);
    });

    mountApp('/c/prod/nodes/-/node-a');
    await waitFor(() => {
      expect(page().querySelector('[aria-label="Actions"]')).not.toBeNull();
    });
    fireEvent.click(control('Drain'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('Drain nodes node-a.');
    });
    fireEvent.change(document.querySelector('input[type="text"]')!, { target: { value: 'node-a' } });
    await waitFor(() => {
      expect(inDialog('Cordon and show me the plan').hasAttribute('disabled')).toBe(false);
    });
    fireEvent.click(inDialog('Cordon and show me the plan'));

    // The refusal names the taint, the fresh plan is on screen, and the name
    // typed against the plan it replaced is still in the box.
    await waitFor(() => {
      expect(document.body.textContent).toContain('dedicated=gpu:NoSchedule now');
    });
    expect((document.querySelector('input[type="text"]') as HTMLInputElement).value).toBe('node-a');
    // The pin moved, so the control is held for a moment before the fresh plan
    // can be confirmed: a second click straight
    // after the refusal would confirm a plan nobody had read.
    expect(inDialog('Cordon and show me the plan').hasAttribute('disabled')).toBe(true);
    await waitFor(
      () => {
        expect(inDialog('Cordon and show me the plan').hasAttribute('disabled')).toBe(false);
      },
      { timeout: PIN_CHANGED_HOLD_MS + 2000 },
    );
    // The pin is a glyph, and what it is pinned to is its accessible name and
    // its hover.
    const pinned = document.querySelector('[role="dialog"] [data-pin]');
    expect(pinned?.getAttribute('aria-label')).toContain('spec.unschedulable and spec.taints');

    fireEvent.click(inDialog('Cordon and show me the plan'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('Cordoned node-a.');
    });
    expect(posted).toEqual([
      { token: 'stage-1', confirmed: 'node-a' },
      { token: 'stage-1-fresh', confirmed: 'node-a' },
    ]);
  });

  /**
   * The eviction stage has
   * no pin, so a 409 there means the set moved - a pod landed on the node
   * between the list and the click. The fresh plan names it, the typed node
   * name stays, and the control is held, so a second click cannot evict the
   * new pod before its name has been on screen long enough to be read.
   */
  it('holds the eviction control when a 409 brings a list with a pod that just landed', async () => {
    const posted: { token?: string; confirmed?: string }[] = [];
    const landed = {
      ...evictPlan,
      token: 'stage-2-fresh',
      sentence: 'Evict 3 pods. This cannot be undone.',
      facts: {
        ...evictPlan.facts,
        button: 'Evict 3 pods',
        count: 3,
        targets: [...evictPlan.facts.targets, { kind: 'pods', namespace: 'prod', name: 'just-landed' }],
        confirmation: { kind: 'name', expected: 'node-a', label: 'Type node-a again to evict 3 pods from it' },
      },
    };
    stubFetch(
      shell({
        [NODE]: {
          body: {
            state: 'ok',
            data: {
              kind: 'nodes',
              name: 'node-a',
              title: 'Node',
              tabs: [],
              sections: [],
              actions: [{ verb: 'drain', label: 'Drain', permitted: true, destructive: true }],
            },
          },
        },
        '/api/c/prod/actions/preview': { body: cordonPlan },
      }),
    );
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if (init?.method === 'POST' && path === '/api/c/prod/actions') {
        const body = JSON.parse(String(init.body)) as { token?: string; confirmed?: string };
        posted.push(body);
        const answer =
          body.token === 'stage-1'
            ? {
                status: 200,
                json: {
                  succeeded: 1,
                  failed: 0,
                  results: [{ target: { kind: 'nodes', name: 'node-a' }, ok: true }],
                  summary: 'Cordoned node-a. Nothing has been evicted yet: 2 pod(s) would be.',
                  nextPlan: evictPlan,
                },
              }
            : body.token === 'stage-2'
              ? {
                  status: 409,
                  json: {
                    status: 409,
                    title: 'The plan no longer describes what would happen',
                    code: 'plan_changed',
                    detail: 'the set of pods on node-a changed since the plan was made',
                    plan: landed,
                  },
                }
              : {
                  status: 200,
                  json: { succeeded: 3, failed: 0, results: [], summary: 'Requested 3 eviction(s) on node-a.' },
                };
        return Promise.resolve(
          new Response(JSON.stringify(answer.json), {
            status: answer.status,
            headers: { 'Content-Type': answer.status === 409 ? 'application/problem+json' : 'application/json' },
          }),
        );
      }
      return stubbed(input, init);
    });

    mountApp('/c/prod/nodes/-/node-a');
    await waitFor(() => {
      expect(page().querySelector('[aria-label="Actions"]')).not.toBeNull();
    });
    fireEvent.click(control('Drain'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('Drain nodes node-a.');
    });
    fireEvent.change(document.querySelector('input[type="text"]')!, { target: { value: 'node-a' } });
    await waitFor(() => {
      expect(inDialog('Cordon and show me the plan').hasAttribute('disabled')).toBe(false);
    });
    fireEvent.click(inDialog('Cordon and show me the plan'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('Evict 2 pods. This cannot be undone.');
    });
    fireEvent.change(document.querySelector('input[type="text"]')!, { target: { value: 'node-a' } });
    await waitFor(() => {
      expect(inDialog('Evict 2 pods').hasAttribute('disabled')).toBe(false);
    });
    fireEvent.click(inDialog('Evict 2 pods'));

    // The fresh list is on screen with the pod that landed, the name typed
    // against the old list is still in the box, and the control is held.
    await waitFor(() => {
      expect(document.body.textContent).toContain('prod/just-landed');
    });
    expect((document.querySelector('input[type="text"]') as HTMLInputElement).value).toBe('node-a');
    expect(inDialog('Evict 3 pods').hasAttribute('disabled')).toBe(true);
    // A reflex click during the hold sends nothing.
    fireEvent.click(inDialog('Evict 3 pods'));
    expect(posted.map((one) => one.token)).toEqual(['stage-1', 'stage-2']);

    await waitFor(
      () => {
        expect(inDialog('Evict 3 pods').hasAttribute('disabled')).toBe(false);
      },
      { timeout: PIN_CHANGED_HOLD_MS + 2000 },
    );
    fireEvent.click(inDialog('Evict 3 pods'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('Requested 3 eviction(s) on node-a.');
    });
    expect(posted).toEqual([
      { token: 'stage-1', confirmed: 'node-a' },
      { token: 'stage-2', confirmed: 'node-a' },
      { token: 'stage-2-fresh', confirmed: 'node-a' },
    ]);
  });

  /**
   * The eviction stage leaves pods nothing owns on the
   * node and offers the same drain including them. The control is the
   * server's, and pressing it previews the server's request - from the first
   * stage, with nothing typed.
   */
  it('offers the drain including the pods nothing owns, and previews exactly that', async () => {
    const previews: unknown[] = [];
    const sparing = {
      ...evictPlan,
      facts: {
        ...evictPlan.facts,
        alternative: {
          label: 'Plan again, evicting the 1 pod nothing owns as well',
          request: { verb: 'drain', target: { kind: 'nodes', name: 'node-a' }, includeUnowned: true },
        },
      },
    };
    stubFetch(
      shell({
        [NODE]: {
          body: {
            state: 'ok',
            data: {
              kind: 'nodes',
              name: 'node-a',
              title: 'Node',
              tabs: [],
              sections: [],
              actions: [{ verb: 'drain', label: 'Drain', permitted: true, destructive: true }],
            },
          },
        },
      }),
    );
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if (init?.method === 'POST' && path === '/api/c/prod/actions/preview') {
        previews.push(JSON.parse(String(init.body)));
        return Promise.resolve(
          new Response(JSON.stringify({ ...cordonPlan, token: `stage-1-${previews.length}` }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          }),
        );
      }
      if (init?.method === 'POST' && path === '/api/c/prod/actions') {
        return Promise.resolve(
          new Response(
            JSON.stringify({ succeeded: 1, failed: 0, results: [], summary: 'Cordoned node-a.', nextPlan: sparing }),
            { status: 200, headers: { 'Content-Type': 'application/json' } },
          ),
        );
      }
      return stubbed(input, init);
    });

    mountApp('/c/prod/nodes/-/node-a');
    await waitFor(() => {
      expect(page().querySelector('[aria-label="Actions"]')).not.toBeNull();
    });
    fireEvent.click(control('Drain'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('Drain nodes node-a.');
    });
    fireEvent.change(document.querySelector('input[type="text"]')!, { target: { value: 'node-a' } });
    await waitFor(() => {
      expect(inDialog('Cordon and show me the plan').hasAttribute('disabled')).toBe(false);
    });
    fireEvent.click(inDialog('Cordon and show me the plan'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('Evict 2 pods. This cannot be undone.');
    });

    fireEvent.click(inDialog('Plan again, evicting the 1 pod nothing owns as well'));
    await waitFor(() => {
      expect(previews).toHaveLength(2);
    });
    expect(previews[1]).toEqual({ verb: 'drain', target: { kind: 'nodes', name: 'node-a' }, includeUnowned: true });
    // Back at the first stage, as a new round: nothing typed.
    await waitFor(() => {
      expect(document.body.textContent).toContain('Drain nodes node-a.');
    });
    expect((document.querySelector('input[type="text"]') as HTMLInputElement).value).toBe('');
  });

  /**
   * A node holding no pod a drain evicts by default
   * but one nothing owns has no eviction stage, so the cordon's outcome carries
   * the offer. It is drawn on the outcome, with the server's label, and pressing
   * it previews the server's request from the first stage, nothing typed.
   */
  it('offers the drain including the pods nothing owns on an outcome with no next stage', async () => {
    const previews: unknown[] = [];
    stubFetch(
      shell({
        [NODE]: {
          body: {
            state: 'ok',
            data: {
              kind: 'nodes',
              name: 'node-a',
              title: 'Node',
              tabs: [],
              sections: [],
              actions: [{ verb: 'drain', label: 'Drain', permitted: true, destructive: true }],
            },
          },
        },
      }),
    );
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if (init?.method === 'POST' && path === '/api/c/prod/actions/preview') {
        previews.push(JSON.parse(String(init.body)));
        return Promise.resolve(
          new Response(JSON.stringify({ ...cordonPlan, token: `stage-1-${previews.length}` }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          }),
        );
      }
      if (init?.method === 'POST' && path === '/api/c/prod/actions') {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              succeeded: 1,
              failed: 0,
              results: [],
              summary: 'Cordoned node-a. Nothing has been evicted, and it is not empty: 1 pod(s) nothing owns.',
              alternative: {
                label: 'Plan again, evicting the 1 pod nothing owns as well',
                request: { verb: 'drain', target: { kind: 'nodes', name: 'node-a' }, includeUnowned: true },
              },
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } },
          ),
        );
      }
      return stubbed(input, init);
    });

    mountApp('/c/prod/nodes/-/node-a');
    await waitFor(() => {
      expect(page().querySelector('[aria-label="Actions"]')).not.toBeNull();
    });
    fireEvent.click(control('Drain'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('Drain nodes node-a.');
    });
    fireEvent.change(document.querySelector('input[type="text"]')!, { target: { value: 'node-a' } });
    await waitFor(() => {
      expect(inDialog('Cordon and show me the plan').hasAttribute('disabled')).toBe(false);
    });
    fireEvent.click(inDialog('Cordon and show me the plan'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('it is not empty');
    });

    fireEvent.click(inDialog('Plan again, evicting the 1 pod nothing owns as well'));
    await waitFor(() => {
      expect(previews).toHaveLength(2);
    });
    expect(previews[1]).toEqual({ verb: 'drain', target: { kind: 'nodes', name: 'node-a' }, includeUnowned: true });
    await waitFor(() => {
      expect(document.body.textContent).toContain('Drain nodes node-a.');
    });
    expect(document.body.textContent).not.toContain('it is not empty');
    expect((document.querySelector('input[type="text"]') as HTMLInputElement).value).toBe('');
  });
});
