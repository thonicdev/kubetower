import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DetailPage, ListPage } from '../../api';
import { resetKeyboardStack } from '../../keyboard/stack';
import { resetViewMemory } from '../../shell/viewMemory';
import { mountApp, stubFetch } from '../../testing/harness';
import { patientWithALoadedRunner } from '../../testing/loadedRunner';

/**
 * An object that is not there any more.
 *
 * A Delete from the object's own drawer closes the drawer, and from its page
 * goes back to its list. An object gone for any other reason reads as gone -
 * "No longer exists" - and never as a kind the cluster does not serve, whose
 * remedy is to install something. And the heading keeps the kind's word: it
 * used to fall back to the URL's slug, "CONFIGMAPS", the moment the object
 * could no longer be read.
 */

// The whole application, a plan dialog and two round trips per test: slow on a
// shared runner, as the other application-altitude files are.
patientWithALoadedRunner();

const session = {
  body: { identity: 'someone', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' },
};

const configmaps: ListPage = {
  kind: 'configmaps',
  label: 'Config maps',
  namespaced: true,
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
  rows: [{ id: 'shop/web', name: 'web', namespace: 'shop', cells: { name: { text: 'web' } } }],
};

const page: DetailPage = {
  kind: 'configmaps',
  namespace: 'shop',
  name: 'web',
  title: 'ConfigMap',
  tabs: [],
  sections: [],
  actions: [{ verb: 'delete', label: 'Delete', permitted: true, destructive: true, irreversible: false }],
};

const OBJECT = '/api/c/prod/r/configmaps/shop/web';

const gone = {
  state: 'absent',
  reason: {
    code: 'no_such_object',
    message: 'configmaps "web" not found',
    scope: { cluster: 'prod', resource: 'configmaps', namespace: 'shop', name: 'web', verb: 'get' },
  },
};

const plan = {
  token: 'plan-1',
  expiresAt: '2999-01-01T00:00:00Z',
  sentence: 'Deletes ConfigMap web.',
  facts: {
    verb: 'delete',
    button: 'Delete',
    count: 1,
    destructive: true,
    targets: [{ kind: 'configmaps', namespace: 'shop', name: 'web' }],
    confirmation: { kind: 'acknowledge', expected: 'confirm' },
    forceable: true,
  },
};

/** The cluster, with the object there until the act deletes it - or, with
 *  `grace`, still there and being deleted, until the test lets it go. */
function cluster(present: { now: boolean; grace?: boolean; deleting?: boolean }): void {
  stubFetch({
    '/api/session': session,
    '/api/clusters': { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } },
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/prod/r/configmaps': { body: { state: 'ok', data: configmaps } },
  });
  const stubbed = globalThis.fetch;
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url).split(
      '?',
    )[0];
    const json = (body: unknown): Promise<Response> =>
      Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    if (path === OBJECT) {
      if (!present.now) return json(gone);
      return json({ state: 'ok', data: present.deleting === true ? { ...page, deleting: true } : page });
    }
    if (init?.method === 'POST' && path === '/api/c/prod/actions/preview') return json(plan);
    if (init?.method === 'POST' && path === '/api/c/prod/actions') {
      if (present.grace === true) present.deleting = true;
      else present.now = false;
      return json({
        succeeded: 1,
        failed: 0,
        results: [{ target: { kind: 'configmaps', namespace: 'shop', name: 'web' }, ok: true }],
      });
    }
    return stubbed(input, init);
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
  resetViewMemory();
});

async function deleteIt(): Promise<void> {
  const verb = await screen.findByRole('button', { name: 'Delete' });
  fireEvent.click(verb);
  const dialog = await screen.findByRole('dialog');
  const commit = dialog.querySelector<HTMLButtonElement>('[data-commit]');
  if (commit === null) throw new Error('no commit button');
  fireEvent.click(commit);
}

/**
 * The outcome is up and says what was done, and the panel is still where it
 * was: it leaves only once the person has read it and closed it. Then Close.
 */
async function readAndCloseTheOutcome(stays: () => void): Promise<void> {
  const close = await waitFor(() => {
    const dialog = document.querySelector('[role="dialog"]');
    const found = [...(dialog?.querySelectorAll('button') ?? [])].find((one) => one.textContent?.trim() === 'Close');
    if (found === undefined) throw new Error('no outcome yet');
    return found;
  });
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain('1 done, 0 refused.');
  stays();
  act(() => {
    fireEvent.click(close);
  });
}

describe('a delete from the object itself', () => {
  it('closes the drawer it was pressed in, once its outcome is closed', async () => {
    cluster({ now: true });
    const { router } = mountApp('/c/prod/configmaps');
    const cell = await waitFor(() => {
      const found = [...document.querySelectorAll('tbody td')].find((td) => td.textContent?.trim() === 'web');
      if (found === undefined) throw new Error('no row yet');
      return found;
    });
    await act(async () => {
      fireEvent.click(cell);
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(document.getElementById('drawer')?.hidden).toBe(false);
    });
    await deleteIt();
    await readAndCloseTheOutcome(() => {
      expect(document.getElementById('drawer')?.hidden).toBe(false);
    });
    await waitFor(() => {
      expect(document.getElementById('drawer')?.hidden).toBe(true);
    });
    expect(router.state.location.pathname).toBe('/c/prod/configmaps');
    expect(document.body.textContent).not.toContain('Not installed');
  });

  it('closes the drawer once the outcome of an accepted delete is closed, while the object is still in its grace period', async () => {
    // A pod: the cluster accepts the delete and keeps the pod, terminating, for
    // its grace period. The drawer does not wait for it to go.
    cluster({ now: true, grace: true });
    mountApp('/c/prod/configmaps');
    const cell = await waitFor(() => {
      const found = [...document.querySelectorAll('tbody td')].find((td) => td.textContent?.trim() === 'web');
      if (found === undefined) throw new Error('no row yet');
      return found;
    });
    await act(async () => {
      fireEvent.click(cell);
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(document.getElementById('drawer')?.hidden).toBe(false);
    });
    await deleteIt();
    await readAndCloseTheOutcome(() => {
      expect(document.getElementById('drawer')?.hidden).toBe(false);
    });
    await waitFor(() => {
      expect(document.getElementById('drawer')?.hidden).toBe(true);
    });
  });

  it('takes the object page back to its list once its outcome is closed', async () => {
    cluster({ now: true });
    const { router } = mountApp('/c/prod/configmaps/shop/web');
    await deleteIt();
    await readAndCloseTheOutcome(() => {
      expect(router.state.location.pathname).toBe('/c/prod/configmaps/shop/web');
    });
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/prod/configmaps');
    });
  });
  it('keeps the outcome up while the object reads as gone behind it', async () => {
    // The re-read after the delete answers the object gone before anyone has
    // closed the outcome: the outcome stays, and the panel says it is gone.
    cluster({ now: true });
    mountApp('/c/prod/configmaps/shop/web');
    await deleteIt();
    await waitFor(() => {
      expect(document.querySelector('[data-state="absent"]')).not.toBeNull();
    });
    await readAndCloseTheOutcome(() => {
      expect(document.querySelector('[data-state="absent"]')).not.toBeNull();
    });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });
});

describe('an object gone for another reason', () => {
  it('is read again while it is being deleted, until it reads as gone', async () => {
    // Opened on an object already on its way out: nothing here acted, so the
    // page stays - and must not stay on the last reading of it.
    const present = { now: true, deleting: true };
    cluster(present);
    mountApp('/c/prod/configmaps/shop/web');
    await screen.findByRole('button', { name: 'Delete' });
    present.now = false;
    const notice = await waitFor(() => {
      const found = document.querySelector<HTMLElement>('[data-state="absent"]');
      if (found === null) throw new Error('not read again yet');
      return found;
    });
    expect(notice.textContent).toContain('No longer exists');
  });

  it('reads as gone in the drawer, and keeps the kind in its heading', async () => {
    cluster({ now: true });
    mountApp('/c/prod/configmaps');
    const row = await waitFor(() => {
      const found = [...document.querySelectorAll('tbody td')].find((td) => td.textContent?.trim() === 'web');
      if (found === undefined) throw new Error('no row yet');
      return found;
    });
    await act(async () => {
      fireEvent.click(row);
      await Promise.resolve();
    });
    const drawer = await waitFor(() => {
      const found = document.getElementById('drawer');
      if (found === null || found.hidden || !(found.textContent ?? '').includes('Delete')) throw new Error('no drawer yet');
      return found;
    });

    // Deleted from the row's own menu, not from the drawer: the drawer did not
    // act, so it stays and says what happened.
    act(() => {
      fireEvent.contextMenu(row, { clientX: 140, clientY: 90, button: 2 });
    });
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog');
    const commit = dialog.querySelector<HTMLButtonElement>('[data-commit]');
    if (commit === null) throw new Error('no commit button');
    fireEvent.click(commit);

    const notice = await waitFor(() => {
      const found = drawer.querySelector<HTMLElement>('[data-state="absent"]');
      if (found === null) throw new Error('no notice yet');
      return found;
    });
    expect(notice.textContent).toContain('No longer exists');
    expect(drawer.hidden).toBe(false);
    expect(drawer.textContent).not.toContain('Not installed');
    expect(drawer.textContent).not.toContain('Nothing on this cluster serves this kind');
    expect(drawer.querySelector('[data-remedy="absent"]')?.textContent).toContain('deleted');
    // The heading's eyebrow is still the kind's own word, not the slug.
    expect(drawer.querySelector('header p')?.textContent).toBe('ConfigMap');
  });
});
