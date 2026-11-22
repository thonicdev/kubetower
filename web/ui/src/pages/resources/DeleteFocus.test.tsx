import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DetailAction, DetailPage, ListPage } from '../../api';
import { resetKeyboardStack } from '../../keyboard/stack';
import { resetViewMemory } from '../../shell/viewMemory';
import { mountApp, stubFetch, type StubReply } from '../../testing/harness';

/**
 * Where the keyboard is after a delete, from each of the three places one is
 * made: a row's menu, the selection bar and the drawer. The result dialog holds
 * it on its Close while it is up, and once it is closed it is on a control of
 * the list - never on `<body>`.
 */

const session = {
  body: {
    identity: 'someone',
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

function pods(names: readonly string[]): ListPage {
  return {
    kind: 'pods',
    label: 'Pods',
    namespaced: true,
    columns: [
      { ...column, key: 'name', label: 'Name', order: 0, role: 'name' },
      { ...column, key: 'namespace', label: 'Namespace', order: 1, role: 'namespace' },
    ],
    rows: names.map(podRow),
    bulk: { scope: 'prod|pods', verbs: [{ verb: 'delete', label: 'Delete', permitted: true, destructive: true }] },
  };
}

const ALL = ['storefront-web', 'checkout-api', 'cart-worker'];

const ACTIONS: readonly DetailAction[] = [
  { verb: 'delete', label: 'Delete', permitted: true, destructive: true, irreversible: true },
];

function detail(name: string): StubReply {
  const page: DetailPage = {
    kind: 'pods',
    namespace: 'shop-prod',
    name,
    title: 'Pod',
    tabs: [],
    sections: [],
    actions: [...ACTIONS],
  };
  return { body: { state: 'ok', data: page } };
}

const plan: StubReply = {
  body: {
    token: 'plan-1',
    expiresAt: '2999-01-01T00:00:00Z',
    sentence: 'Delete the pod storefront-web in shop-prod.',
    facts: {
      verb: 'delete',
      button: 'Delete it',
      count: 1,
      destructive: true,
      targets: [{ kind: 'pods', namespace: 'shop-prod', name: 'storefront-web' }],
      confirmation: { kind: 'name', expected: 'storefront-web' },
    },
  },
};

const deleted = {
  succeeded: 1,
  failed: 0,
  summary: 'Deleted Pod storefront-web.',
  results: [{ target: { kind: 'pods', namespace: 'shop-prod', name: 'storefront-web' }, ok: true }],
};

/** The app's reads, with the list answering without the deleted pod once a
 *  delete has been posted: the row the act was opened from is then gone. */
function serve(): void {
  stubFetch({
    '/api/session': session,
    '/api/clusters': { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } },
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/prod/r/pods': { body: { state: 'ok', data: pods(ALL) } },
    '/api/c/prod/r/pods/shop-prod/storefront-web': detail('storefront-web'),
    '/api/c/prod/actions/preview': plan,
    '/api/c/prod/actions': { body: deleted },
    '/api/c/prod/bulk/preview': {
      body: { ...plan.body as object, facts: { ...(plan.body as { facts: object }).facts, confirmation: { kind: 'acknowledge' } } },
    },
    '/api/c/prod/bulk': { body: deleted },
  });
  const stubbed = globalThis.fetch;
  let gone = false;
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const path = new URL(raw, 'http://localhost').pathname;
    if (init?.method === 'POST' && (path === '/api/c/prod/actions' || path === '/api/c/prod/bulk')) gone = true;
    if (gone && path === '/api/c/prod/r/pods') {
      return Promise.resolve(
        new Response(JSON.stringify({ state: 'ok', data: pods(ALL.slice(1)) }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    }
    return stubbed(input, init);
  });
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

function dialog(): HTMLElement {
  const element = document.querySelector('[role="dialog"]');
  if (!(element instanceof HTMLElement)) throw new Error('no dialog');
  return element;
}

function inDialog(text: string): HTMLButtonElement {
  const found = [...dialog().querySelectorAll('button')].find((one) => one.textContent?.trim() === text);
  if (found === undefined) throw new Error(`no ${text} in the dialog`);
  return found;
}

/** The outcome is up: the keyboard is on its Close, inside the dialog. */
async function focusOnTheOutcomesClose(): Promise<void> {
  await waitFor(() => {
    expect(dialog().textContent).toContain('Deleted Pod storefront-web.');
  });
  await waitFor(() => {
    expect(document.activeElement).toBe(inDialog('Close'));
  });
  expect(dialog().contains(document.activeElement)).toBe(true);
}

/** Close the outcome; the keyboard is then on a control of the list. */
async function closeAndFindTheList(): Promise<void> {
  act(() => {
    fireEvent.click(inDialog('Close'));
  });
  expect(document.querySelector('[role="dialog"]')).toBeNull();
  expect(document.activeElement).not.toBe(document.body);
  expect(document.activeElement?.closest('#page tbody')).not.toBeNull();
}

describe('the keyboard after a delete', () => {
  it('from a row menu: on Close, then on the row that took the deleted one’s place', async () => {
    serve();
    mountApp('/c/prod/pods');
    await listed();

    act(() => {
      fireEvent.contextMenu(rowFor('storefront-web'), { clientX: 10, clientY: 10, button: 2 });
    });
    const remove = await screen.findByRole('menuitem', { name: 'Delete' });
    act(() => {
      fireEvent.click(remove);
    });
    await waitFor(() => {
      expect(dialog().textContent).toContain('Delete the pod storefront-web');
    });
    fireEvent.change(dialog().querySelector('input[type="text"]')!, { target: { value: 'storefront-web' } });
    await waitFor(() => {
      expect(inDialog('Delete it').hasAttribute('disabled')).toBe(false);
    });
    act(() => {
      fireEvent.click(inDialog('Delete it'));
    });

    await focusOnTheOutcomesClose();
    // The list has been re-read without the deleted pod by now.
    await waitFor(() => {
      expect([...document.querySelectorAll('tbody td')].some((td) => td.textContent?.trim() === 'storefront-web')).toBe(
        false,
      );
    });
    await closeAndFindTheList();
    expect(document.activeElement?.closest('tr')).toBe(rowFor('checkout-api'));
  });

  it('from the selection bar: on Close, then on the list once the bar has gone', async () => {
    serve();
    mountApp('/c/prod/pods');
    await listed();

    const box = rowFor('storefront-web').querySelector('input[type="checkbox"]');
    if (box === null) throw new Error('no checkbox');
    fireEvent.click(box);
    await waitFor(() => {
      expect(document.getElementById('bulk-bar')).not.toBeNull();
    });
    const bulkDelete = [...(document.getElementById('bulk-bar')?.querySelectorAll('button') ?? [])].find(
      (one) => one.textContent === 'Delete',
    );
    if (bulkDelete === undefined) throw new Error('no Delete on the bar');
    bulkDelete.focus();
    act(() => {
      fireEvent.click(bulkDelete);
    });
    await waitFor(() => {
      expect(inDialog('Delete it').hasAttribute('disabled')).toBe(false);
    });
    act(() => {
      fireEvent.click(inDialog('Delete it'));
    });

    await focusOnTheOutcomesClose();
    await closeAndFindTheList();
    expect(document.getElementById('bulk-bar')).toBeNull();
  });

  it('from the drawer: on Close while the result is up, and never on <body> after it', async () => {
    serve();
    mountApp('/c/prod/pods');
    await listed();

    act(() => {
      fireEvent.click(rowFor('storefront-web'));
    });
    const drawer = document.getElementById('drawer');
    await waitFor(() => {
      expect(drawer?.querySelector('[aria-label="Actions"] button')).not.toBeNull();
    });
    const remove = drawer?.querySelector<HTMLButtonElement>('[aria-label="Actions"] button');
    if (remove === null || remove === undefined) throw new Error('no Delete in the drawer');
    remove.focus();
    act(() => {
      fireEvent.click(remove);
    });
    await waitFor(() => {
      expect(dialog().textContent).toContain('Delete the pod storefront-web');
    });
    fireEvent.change(dialog().querySelector('input[type="text"]')!, { target: { value: 'storefront-web' } });
    await waitFor(() => {
      expect(inDialog('Delete it').hasAttribute('disabled')).toBe(false);
    });
    act(() => {
      fireEvent.click(inDialog('Delete it'));
    });

    await focusOnTheOutcomesClose();
    act(() => {
      fireEvent.click(inDialog('Close'));
    });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    // The drawer's Delete, where the drawer still draws it, or a control of
    // the list where it does not: either way a control, and not the page.
    expect(document.activeElement).not.toBe(document.body);
    const back = document.activeElement;
    expect(back === remove || back?.closest('#page tbody') !== null).toBe(true);
  });
});
