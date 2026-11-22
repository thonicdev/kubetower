import { act, cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DetailAction, DetailPage, ListPage, PodContainer } from '../../api';
import { resetKeyboardStack } from '../../keyboard/stack';
import { resetViewMemory } from '../../shell/viewMemory';
import { mountApp, stubFetch, type StubReply, type StubRoutes } from '../../testing/harness';
import { NO_VERB } from './RowMenu';

/**
 * A list row's context menu, at application altitude.
 *
 * What it does, one test each:
 *
 * - a right-click opens the menu at the pointer; the `⋯`, the menu key and
 *   Shift+F10 open it under the `⋯`;
 * - the header names the object, then the safe entries - Open, Logs, Shell,
 *   Copy name - a separator, then the drawer's verbs, equal to the drawer's for
 *   that object and going through the same plan dialog;
 * - only the irreversible verb, Delete, is red in the menu; the drawer keeps
 *   coloring by `destructive`;
 * - the browser's own menu is suppressed on rows and nowhere else - not on a
 *   link inside a row, from the pointer or the keyboard, and it is never
 *   drawn over the open menu;
 * - one confirmation at a time: a second row's verb waits while a dialog is up;
 * - reading, a failed read and an object with no verb are three sentences.
 *
 * Through the real application, for `ObjectDrawer.test.tsx`'s reason: what is
 * asserted is that the list, the menu, the drawer, the dock and the dialog are
 * wired to each other.
 */

const session = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

const column = {
  visible: true,
  sortable: true,
  secondary: false,
  gauged: false,
  copyable: false,
  prose: false,
  filterable: false,
} as const;

function podRow(name: string): ListPage['rows'][number] {
  return {
    id: `shop-prod/${name}`,
    name,
    namespace: 'shop-prod',
    cells: { name: { text: name }, namespace: { text: 'shop-prod' } },
  };
}

const pods: ListPage = {
  kind: 'pods',
  label: 'Pods',
  namespaced: true,
  columns: [
    { ...column, key: 'name', label: 'Name', order: 0, role: 'name' },
    { ...column, key: 'namespace', label: 'Namespace', order: 1, role: 'namespace' },
  ],
  rows: [
    podRow('storefront-web'),
    podRow('checkout-api'),
    // A row that names no single object: no namespace on a namespaced kind.
    // It opens no drawer, so it has no menu.
    { id: 'orphan', name: 'orphan', cells: { name: { text: 'orphan' } } },
  ],
};

/** The drawer's verbs for this pod, as the server sends them: all three
 *  destructive, Evict refused on this cluster, and Delete alone irreversible. */
const ACTIONS: readonly DetailAction[] = [
  {
    verb: 'evict',
    label: 'Evict',
    permitted: false,
    destructive: true,
    irreversible: false,
    reason: { code: 'verb_denied', message: 'this cluster is marked read-only' },
  },
  { verb: 'restart', label: 'Restart', permitted: true, destructive: true, irreversible: false },
  { verb: 'delete', label: 'Delete', permitted: true, destructive: true, irreversible: true },
];

function running(name: string): PodContainer {
  return { name, image: 'x', init: false, probes: [], status: { state: 'running', ready: true, restarts: 0 } } as PodContainer;
}
function waitingOn(name: string, reason: string): PodContainer {
  return {
    name,
    image: 'x',
    init: false,
    probes: [],
    status: { state: 'waiting', ready: false, restarts: 0, reason },
  } as PodContainer;
}

function detail(
  actions: readonly DetailAction[] | undefined,
  name = 'storefront-web',
  containers?: readonly PodContainer[],
): StubReply {
  const page: DetailPage = {
    kind: 'pods',
    namespace: 'shop-prod',
    name,
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
    ...(actions === undefined ? {} : { actions: [...actions] }),
    ...(containers === undefined ? {} : { containers: [...containers] }),
  };
  return { body: { state: 'ok', data: page } };
}

const OBJECT = '/api/c/prod/r/pods/shop-prod/storefront-web';
const OTHER = '/api/c/prod/r/pods/shop-prod/checkout-api';

const plan: StubReply = {
  body: {
    token: 'plan-9',
    expiresAt: '2999-01-01T00:00:00Z',
    sentence: 'Delete the pod storefront-web in shop-prod.',
    facts: {
      verb: 'delete',
      button: 'Delete it',
      count: 1,
      destructive: true,
      targets: [{ kind: 'pods', namespace: 'shop-prod', name: 'storefront-web' }],
      confirmation: { kind: 'name', expected: 'storefront-web', label: 'Type storefront-web to confirm' },
    },
  },
};

const EXEC = { body: { podExec: true, nodeShell: false, assistant: false } };

/** The Shell's own reads, the list, and whatever the test adds. Returns the
 *  POSTs, by path and body. */
function serve(extra: StubRoutes = {}): { path: string; body: unknown }[] {
  stubFetch({
    '/api/session': session,
    '/api/clusters': { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } },
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/prod/r/pods': { body: { state: 'ok', data: pods } },
    ...extra,
  });
  const stubbed = globalThis.fetch;
  const posted: { path: string; body: unknown }[] = [];
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
    if (init?.method === 'POST') posted.push({ path, body: JSON.parse(String(init.body)) });
    return stubbed(input, init);
  });
  return posted;
}

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
  resetViewMemory();
});

function rowFor(name: string): HTMLElement {
  const cell = [...document.querySelectorAll('tbody td')].find((td) => td.textContent?.trim() === name);
  const row = cell?.closest('tr');
  if (row === null || row === undefined) throw new Error(`no row for ${name}`);
  return row as HTMLElement;
}

async function listed(): Promise<void> {
  await waitFor(() => {
    expect(document.querySelector('tbody td')).not.toBeNull();
  });
}

/** A right-click, as a pointer sends it: button 2, at a point. Returns whether
 *  the browser would still draw its own menu. */
function rightClick(element: Element, at = { clientX: 140, clientY: 90 }): boolean {
  let drawn = true;
  act(() => {
    drawn = fireEvent.contextMenu(element, { ...at, button: 2 });
  });
  return drawn;
}

const menu = (): HTMLElement => screen.getByRole('menu');
const escape = (): void => {
  act(() => {
    fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' });
  });
};

/** The menu's entries, in order, with the separator as `|`. */
function entries(): string[] {
  return [...menu().querySelectorAll('[role="menuitem"], [role="separator"]')].map((one) =>
    one.getAttribute('role') === 'separator' ? '|' : (one.textContent?.trim() ?? ''),
  );
}

/** The verbs: the entries after the separator. */
function verbs(): string[] {
  const all = entries();
  return all.slice(all.indexOf('|') + 1);
}

describe("a list row's menu", () => {
  it('names the object, offers the safe entries, a separator, then the drawer’s verbs', async () => {
    serve({ [OBJECT]: detail(ACTIONS) });
    mountApp('/c/prod/pods');
    await listed();

    expect(rightClick(rowFor('storefront-web'))).toBe(false);
    await waitFor(() => {
      expect(verbs()).toEqual(['Evict', 'Restart', 'Delete']);
    });
    expect(menu().getAttribute('aria-label')).toBe('Actions on shop-prod/storefront-web');
    expect(menu().querySelector('p')?.textContent).toBe('shop-prod/storefront-web');
    // No exec capability answered here, so no Shell: only what the drawer offers.
    expect(entries()).toEqual(['Open', 'Logs', 'Copy name', '|', 'Evict', 'Restart', 'Delete']);

    // Only the irreversible verb is red in the menu, though all three are
    // destructive; a refused verb is there, refused, saying why.
    const red = [...menu().querySelectorAll('[data-destructive]')].map((one) => one.textContent?.trim());
    expect(red).toEqual(['Delete']);
    const evict = screen.getByRole('menuitem', { name: 'Evict' });
    expect(evict.getAttribute('aria-disabled')).toBe('true');
    expect(evict.getAttribute('title')).toBe('this cluster is marked read-only');
  });

  it('opens at the pointer for a right-click, and under the ⋯ for the ⋯, the menu key and Shift+F10', async () => {
    serve({ [OBJECT]: detail(ACTIONS) });
    mountApp('/c/prod/pods');
    await listed();
    const row = rowFor('storefront-web');
    const more = screen.getByRole('button', { name: 'Actions on shop-prod/storefront-web' });
    // Where the ⋯ is, as the browser would lay it out: jsdom lays nothing out.
    more.getBoundingClientRect = () => ({ left: 300, right: 315, top: 60, bottom: 75, width: 15, height: 15, x: 300, y: 60, toJSON: () => ({}) });
    const where = (): string => `${menu().style.left},${menu().style.top}`;

    rightClick(row, { clientX: 140, clientY: 90 });
    expect(where()).toBe('140px,90px');
    escape();

    const placed: string[] = [];
    act(() => {
      fireEvent.click(more);
    });
    placed.push(where());
    escape();
    row.focus();
    act(() => {
      fireEvent.keyDown(row, { key: 'F10', shiftKey: true });
    });
    placed.push(where());
    escape();
    act(() => {
      fireEvent.keyDown(row, { key: 'ContextMenu' });
    });
    placed.push(where());
    escape();
    // The menu key's own contextmenu, as real Chromium sends it: at the focused
    // element's coordinates and not a pointer's button.
    act(() => {
      fireEvent.contextMenu(row, { clientX: 654, clientY: 181, button: 0 });
    });
    placed.push(where());
    expect(placed).toEqual(['300px,75px', '300px,75px', '300px,75px', '300px,75px']);
  });

  it('holds exactly the verbs the drawer draws for the same row, and Open opens that drawer', async () => {
    serve({ [OBJECT]: detail(ACTIONS) });
    mountApp('/c/prod/pods');
    await listed();

    rightClick(rowFor('storefront-web'));
    await waitFor(() => {
      expect(verbs()).toHaveLength(3);
    });
    const fromMenu = verbs();
    expect(document.getElementById('drawer')?.hidden).toBe(true);

    act(() => {
      fireEvent.click(screen.getByRole('menuitem', { name: 'Open' }));
    });
    const drawer = document.getElementById('drawer');
    await waitFor(() => {
      expect(drawer?.querySelector('[aria-label="Actions"]')).not.toBeNull();
    });
    expect(drawer?.hidden).toBe(false);
    const fromDrawer = [...(drawer?.querySelectorAll('[aria-label="Actions"] button') ?? [])].map(
      (button) => button.textContent?.trim() ?? '',
    );
    expect(fromMenu).toEqual(fromDrawer);
    // The drawer keeps coloring by `destructive`, as before: only the row
    // menu reds the irreversible verb alone.
    expect(
      [...(drawer?.querySelectorAll('[aria-label="Actions"] [data-destructive]') ?? [])].map((one) =>
        one.textContent?.trim(),
      ),
    ).toEqual(['Evict', 'Restart', 'Delete']);
  });

  it('previews the chosen verb and opens the same plan dialog the drawer opens', async () => {
    const posted = serve({ [OBJECT]: detail(ACTIONS), '/api/c/prod/actions/preview': plan });
    mountApp('/c/prod/pods');
    await listed();

    rightClick(rowFor('storefront-web'));
    const remove = await screen.findByRole('menuitem', { name: 'Delete' });
    act(() => {
      fireEvent.click(remove);
    });
    await waitFor(() => {
      expect(document.body.textContent).toContain('Delete the pod storefront-web in shop-prod.');
    });
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.body.textContent).toContain('Delete storefront-web?');
    expect(posted).toEqual([
      {
        path: '/api/c/prod/actions/preview',
        body: { verb: 'delete', target: { kind: 'pods', namespace: 'shop-prod', name: 'storefront-web' } },
      },
    ]);
  });

  it('starts nothing on a second row while a confirmation is open, and says so', async () => {
    const posted = serve({
      [OBJECT]: detail(ACTIONS),
      [OTHER]: detail(ACTIONS, 'checkout-api'),
      '/api/c/prod/actions/preview': plan,
    });
    mountApp('/c/prod/pods');
    await listed();

    rightClick(rowFor('storefront-web'));
    const first = await screen.findByRole('menuitem', { name: 'Delete' });
    act(() => {
      fireEvent.click(first);
    });
    await waitFor(() => {
      expect(document.body.textContent).toContain('Delete storefront-web?');
    });

    // The dialog is not modal: a second row's menu can be reached.
    rightClick(rowFor('checkout-api'));
    await waitFor(() => {
      expect(verbs()).toEqual(['Evict', 'Restart', 'Delete']);
    });
    expect(menu().textContent).toContain('Another confirmation is open: finish or cancel it first.');
    const second = screen.getByRole('menuitem', { name: 'Delete' });
    expect(second.getAttribute('aria-disabled')).toBe('true');
    act(() => {
      fireEvent.click(second);
    });
    // Nothing was asked about checkout-api, and the dialog still names, and
    // will still apply to, storefront-web.
    expect(posted).toHaveLength(1);
    expect(document.body.textContent).toContain('Delete storefront-web?');
    expect(document.body.textContent).not.toContain('Delete checkout-api?');
    // The safe entries stay usable.
    expect(screen.getByRole('menuitem', { name: 'Open' }).getAttribute('aria-disabled')).toBeNull();
  });

  it("suppresses the browser's own menu on a row that has one and nowhere else", async () => {
    serve({ [OBJECT]: detail(ACTIONS) });
    mountApp('/c/prod/pods');
    await listed();

    expect(rightClick(rowFor('orphan'))).toBe(true);
    expect(screen.queryByRole('menu')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Actions on orphan' })).toBeNull();
    expect(rightClick(document.querySelector('thead th') as Element)).toBe(true);
    expect(screen.queryByRole('menu')).toBeNull();
    expect(rightClick(rowFor('storefront-web'))).toBe(false);
    expect(menu()).not.toBeNull();

    // And never over the open menu itself, from the pointer or the menu key on
    // one of its items.
    expect(rightClick(menu())).toBe(false);
    let drawn = true;
    act(() => {
      drawn = fireEvent.contextMenu(screen.getByRole('menuitem', { name: 'Open' }), { button: 0 });
    });
    expect(drawn).toBe(false);
    expect(menu()).not.toBeNull();
  });

  it('leaves a link inside a row to the browser, from the pointer and from the keyboard', async () => {
    const linked: ListPage = {
      ...pods,
      columns: [...pods.columns, { ...column, key: 'hosts', label: 'Hosts', order: 2, role: 'other' }],
      rows: [
        {
          ...podRow('storefront-web'),
          cells: {
            ...podRow('storefront-web').cells,
            hosts: { text: 'shop.example.com', parts: [{ text: 'shop.example.com', href: 'https://shop.example.com' }] },
          },
        },
      ],
    };
    serve({ '/api/c/prod/r/pods': { body: { state: 'ok', data: linked } }, [OBJECT]: detail(ACTIONS) });
    mountApp('/c/prod/pods');
    await listed();

    const link = await waitFor(() => {
      const found = document.querySelector<HTMLElement>('tbody a[href="https://shop.example.com"]');
      if (found === null) throw new Error('no link in the row');
      return found;
    });
    expect(rightClick(link)).toBe(true);
    expect(screen.queryByRole('menu')).toBeNull();
    link.focus();
    let prevented = false;
    act(() => {
      prevented = !fireEvent.keyDown(link, { key: 'F10', shiftKey: true });
    });
    expect(prevented).toBe(false);
    act(() => {
      fireEvent.contextMenu(link, { clientX: 400, clientY: 120, button: 0 });
    });
    expect(screen.queryByRole('menu')).toBeNull();
    // The rest of the same row is still the row's.
    expect(rightClick(rowFor('storefront-web'))).toBe(false);
  });

  it('closes on a right-click elsewhere, rather than drawing the browser’s menu over it', async () => {
    serve({ [OBJECT]: detail(ACTIONS) });
    mountApp('/c/prod/pods');
    await listed();

    rightClick(rowFor('storefront-web'));
    const scrim = document.querySelector('button[aria-hidden="true"].fixed');
    if (scrim === null) throw new Error('no scrim under the open menu');
    expect(rightClick(scrim)).toBe(false);
    expect(screen.queryByRole('menu')).toBeNull();
    expect(rightClick(rowFor('storefront-web'))).toBe(false);
    expect(menu()).not.toBeNull();
  });

  it('puts the focus on its first entry, walks with the arrows, and gives the focus back on Escape', async () => {
    serve({ [OBJECT]: detail(ACTIONS) });
    mountApp('/c/prod/pods');
    await listed();

    const row = rowFor('storefront-web');
    row.focus();
    act(() => {
      fireEvent.keyDown(row, { key: 'F10', shiftKey: true });
    });
    await waitFor(() => {
      expect(document.activeElement?.textContent?.trim()).toBe('Open');
    });
    act(() => {
      fireEvent.keyDown(document.activeElement as Element, { key: 'End' });
    });
    await waitFor(() => {
      expect(document.activeElement?.textContent?.trim()).toBe('Delete');
    });
    escape();
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(row);
  });

  it('copies namespace/name and says so', async () => {
    serve({ [OBJECT]: detail(ACTIONS) });
    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal('isSecureContext', true);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    mountApp('/c/prod/pods');
    await listed();

    rightClick(rowFor('storefront-web'));
    act(() => {
      fireEvent.click(screen.getByRole('menuitem', { name: 'Copy name' }));
    });
    expect(writeText).toHaveBeenCalledWith('shop-prod/storefront-web');
    await waitFor(() => {
      expect(document.querySelector('[data-note="copied"]')?.textContent).toBe('Copied shop-prod/storefront-web.');
    });
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('offers Shell only with exec, one entry per container that can take one, refused where none runs', async () => {
    // No exec capability: no Shell, as the drawer draws none.
    serve({ [OBJECT]: detail(ACTIONS, 'storefront-web', [running('app')]) });
    mountApp('/c/prod/pods');
    await listed();
    rightClick(rowFor('storefront-web'));
    await waitFor(() => {
      expect(verbs()).toHaveLength(3);
    });
    expect(entries()).not.toContain('Shell');
    escape();
    cleanup();

    // Exec, one container: Shell.
    serve({ '/api/capabilities': EXEC, [OBJECT]: detail(ACTIONS, 'storefront-web', [running('app')]) });
    mountApp('/c/prod/pods');
    await listed();
    rightClick(rowFor('storefront-web'));
    await waitFor(() => {
      expect(entries().slice(0, 5)).toEqual(['Open', 'Logs', 'Shell', 'Copy name', '|']);
    });
    escape();
    cleanup();

    // Exec, two running and one waiting: the two that can take a shell.
    serve({
      '/api/capabilities': EXEC,
      [OBJECT]: detail(ACTIONS, 'storefront-web', [running('app'), running('sidecar'), waitingOn('init-db', 'CrashLoopBackOff')]),
    });
    mountApp('/c/prod/pods');
    await listed();
    rightClick(rowFor('storefront-web'));
    await waitFor(() => {
      expect(entries()).toContain('Shell: sidecar');
    });
    expect(entries().filter((one) => one.startsWith('Shell'))).toEqual(['Shell: app', 'Shell: sidecar']);
    escape();
    cleanup();

    // Exec, nothing running: Shell refused with the drawer's sentence.
    serve({
      '/api/capabilities': EXEC,
      [OBJECT]: detail(ACTIONS, 'storefront-web', [waitingOn('app', 'ImagePullBackOff')]),
    });
    mountApp('/c/prod/pods');
    await listed();
    rightClick(rowFor('storefront-web'));
    const refused = await screen.findByRole('menuitem', { name: 'Shell' });
    expect(refused.getAttribute('aria-disabled')).toBe('true');
    expect(refused.getAttribute('title')).toContain('ImagePullBackOff');
  });

  /**
   * Under `shared`, `podExec` says only that the build can open a shell in a
   * pod; whether this row's pod gets one is the pod's own answer
   * (`DetailPage.shell`), asked for the person and the console on that pod. A
   * person granted exec in one namespace must still get Shell, which one
   * question about every namespace could not answer.
   */
  it('under the shared profile offers Shell from the pod’s own answer', async () => {
    const SHARED = { body: { mode: 'shared', podExec: true, nodeShell: false, assistant: false } };
    const withShell = (shell: DetailPage['shell']): StubReply => {
      const page = (detail(ACTIONS, 'storefront-web', [running('app')]).body as { data: DetailPage }).data;
      return { body: { state: 'ok', data: shell === undefined ? page : { ...page, shell } } };
    };

    // Allowed in this pod's namespace: Shell.
    serve({ '/api/capabilities': SHARED, [OBJECT]: withShell({ permitted: true }) });
    mountApp('/c/prod/pods');
    await listed();
    rightClick(rowFor('storefront-web'));
    const offered = await screen.findByRole('menuitem', { name: 'Shell' });
    expect(offered.getAttribute('aria-disabled')).not.toBe('true');
    escape();
    cleanup();

    // Not allowed here: no Shell, in a menu whose verbs arrived.
    serve({ '/api/capabilities': SHARED, [OBJECT]: withShell(undefined) });
    mountApp('/c/prod/pods');
    await listed();
    rightClick(rowFor('storefront-web'));
    await waitFor(() => {
      expect(verbs()).toHaveLength(3);
    });
    expect(entries()).not.toContain('Shell');
    escape();
    cleanup();

    // The person may and the console may not: refused with the server's sentence.
    const sentence = 'you may, but this console may not: its ServiceAccount is not granted create on pods/exec';
    serve({
      '/api/capabilities': SHARED,
      [OBJECT]: withShell({ permitted: false, reason: { code: 'verb_denied', message: sentence } }),
    });
    mountApp('/c/prod/pods');
    await listed();
    rightClick(rowFor('storefront-web'));
    const refused = await screen.findByRole('menuitem', { name: 'Shell' });
    expect(refused.getAttribute('aria-disabled')).toBe('true');
    expect(refused.getAttribute('title')).toBe(sentence);
  });

  it('opens Logs in the dock', async () => {
    serve({ [OBJECT]: detail(ACTIONS) });
    // The stream itself is never reached: jsdom has no EventSource, and what is
    // under test is that the menu opened the tab.
    vi.stubGlobal(
      'EventSource',
      class {
        close(): void {}
        onmessage: unknown = null;
        onerror: unknown = null;
      },
    );
    mountApp('/c/prod/pods');
    await listed();

    rightClick(rowFor('storefront-web'));
    act(() => {
      fireEvent.click(screen.getByRole('menuitem', { name: 'Logs' }));
    });
    await waitFor(() => {
      expect(screen.getAllByRole('tab').some((tab) => tab.textContent?.includes('storefront-web'))).toBe(true);
    });
  });

  it('says it is reading, then that the read failed, and never draws an empty menu for it', async () => {
    serve({
      [OBJECT]: {
        body: { state: 'forbidden', reason: { code: 'verb_denied', message: 'pods is forbidden for this account.' } },
      },
    });
    mountApp('/c/prod/pods');
    await listed();

    rightClick(rowFor('storefront-web'));
    expect(menu().textContent).toContain('Reading what can be done to it');
    await waitFor(() => {
      expect(menu().textContent).toContain('Could not read what can be done to it: pods is forbidden for this account.');
    });
    expect(verbs()).toEqual([]);
  });

  it('says so when the object has no verb, which is not a failure', async () => {
    serve({ [OBJECT]: detail(undefined) });
    mountApp('/c/prod/pods');
    await listed();

    rightClick(rowFor('storefront-web'));
    await waitFor(() => {
      expect(menu().textContent).toContain(NO_VERB);
      expect(menu().textContent).not.toContain('Nothing can be done');
    });
    expect(screen.queryByRole('alert')).toBeNull();
  });
});
