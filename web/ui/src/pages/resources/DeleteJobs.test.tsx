import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { BulkAffordance, DetailAction, DetailPage, ListPage } from '../../api';
import { resetKeyboardStack } from '../../keyboard/stack';
import { resetViewMemory } from '../../shell/viewMemory';
import { mountApp, stubFetch, type StubReply } from '../../testing/harness';

/**
 * Deleting Jobs and CronJobs. On a Jobs list and a CronJobs list, a row's menu
 * and the selection bar offer Delete whenever the server does, and the bar's
 * Delete previews the bulk delete of what is ticked. Nothing on this side keeps a per-kind list of who
 * may be deleted: these hold it.
 */

const session = {
  body: { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' },
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

const DELETE = { verb: 'delete', label: 'Delete', permitted: true, destructive: true } as const;

const KINDS: {
  kind: string;
  label: string;
  title: string;
  name: string;
  actions: DetailAction[];
  bulk: BulkAffordance;
  menu: string[];
  bar: string[];
}[] = [
  {
    kind: 'jobs',
    label: 'Jobs',
    title: 'Job',
    name: 'nightly-29895445',
    actions: [{ ...DELETE, irreversible: true }],
    bulk: { scope: 'prod|jobs', verbs: [DELETE] },
    menu: ['Delete'],
    bar: ['Delete'],
  },
  {
    kind: 'cronjobs',
    label: 'Cron jobs',
    title: 'CronJob',
    name: 'nightly',
    actions: [
      { verb: 'trigger', label: 'Run now', permitted: true, destructive: false, irreversible: false },
      { verb: 'suspend', label: 'Suspend', permitted: true, destructive: false, irreversible: false },
      { verb: 'resume', label: 'Resume', permitted: true, destructive: false, irreversible: false },
      { ...DELETE, irreversible: true },
    ],
    bulk: {
      scope: 'prod|cronjobs',
      verbs: [{ verb: 'suspend', label: 'Suspend', permitted: true }, { verb: 'resume', label: 'Resume', permitted: true }, DELETE],
    },
    menu: ['Run now', 'Suspend', 'Resume', 'Delete'],
    bar: ['Suspend', 'Resume', 'Delete'],
  },
];

function serve(one: (typeof KINDS)[number]): { path: string; body: unknown }[] {
  const list: ListPage = {
    kind: one.kind,
    label: one.label,
    namespaced: true,
    columns: [
      { ...column, key: 'name', label: 'Name', order: 0, role: 'name' },
      { ...column, key: 'namespace', label: 'Namespace', order: 1, role: 'namespace' },
    ],
    rows: [{ id: `batch-runs/${one.name}`, name: one.name, namespace: 'batch-runs', cells: { name: { text: one.name }, namespace: { text: 'batch-runs' } } }],
    bulk: one.bulk,
  };
  const page: DetailPage = { kind: one.kind, namespace: 'batch-runs', name: one.name, title: one.title, tabs: [], sections: [], actions: one.actions };
  const detail: StubReply = { body: { state: 'ok', data: page } };
  stubFetch({
    '/api/session': session,
    '/api/clusters': { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } },
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    [`/api/c/prod/r/${one.kind}`]: { body: { state: 'ok', data: list } },
    [`/api/c/prod/r/${one.kind}/batch-runs/${one.name}`]: detail,
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
  const cell = [...document.querySelectorAll('tbody td')].find((td) => td.textContent?.includes(name));
  const row = cell?.closest('tr');
  if (row === null || row === undefined) throw new Error(`no row for ${name}`);
  return row as HTMLElement;
}

describe.each(KINDS)('a $label list', (one) => {
  it("offers Delete in a row's menu", async () => {
    serve(one);
    mountApp(`/c/prod/${one.kind}`);
    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    act(() => {
      fireEvent.contextMenu(rowFor(one.name), { clientX: 140, clientY: 90, button: 2 });
    });
    await waitFor(() => {
      const items = within(screen.getByRole('menu')).getAllByRole('menuitem').map((item) => item.textContent?.trim());
      expect(items.slice(items.indexOf('Copy name') + 1)).toEqual(one.menu);
    });
    const remove = within(screen.getByRole('menu')).getByRole('menuitem', { name: 'Delete' });
    expect(remove.getAttribute('aria-disabled')).not.toBe('true');
  });

  it('offers Delete on the selection bar, and it previews the delete of what is ticked', async () => {
    const posted = serve(one);
    mountApp(`/c/prod/${one.kind}`);
    await waitFor(() => {
      expect(document.querySelector('tbody td')).not.toBeNull();
    });
    fireEvent.click(screen.getByRole('checkbox', { name: `Select batch-runs/${one.name}` }));
    const bar = document.getElementById('bulk-bar');
    if (bar === null) throw new Error('no selection bar');
    const buttons = within(bar)
      .getAllByRole('button')
      .map((button) => button.textContent?.trim())
      .filter((label) => label !== 'Clear');
    expect(buttons).toEqual(one.bar);
    fireEvent.click(within(bar).getByRole('button', { name: 'Delete' }));
    await waitFor(() => {
      expect(posted.map((one) => one.path)).toContain('/api/c/prod/bulk/preview');
    });
    expect(posted.find((p) => p.path === '/api/c/prod/bulk/preview')?.body).toMatchObject({
      verb: 'delete',
      targets: [{ kind: one.kind, namespace: 'batch-runs', name: one.name }],
    });
  });
});
