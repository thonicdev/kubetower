import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Column } from '../../api';
import { resetKeyboardStack } from '../../keyboard/stack';
import { resetColumnMemory } from '../resources/columnMemory';
import { resetNamespaceMemory } from '../../shell/namespaceMemory';
import { mountApp } from '../../testing/harness';

/**
 * The Helm list's filter funnel and Columns menu: the resource lists' own
 * controls, over the columns the Helm answer carries, with the arrangement
 * kept where a resource list keeps it - the console's column store under the
 * kind `helm`, or this browser where the console keeps none.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
  resetNamespaceMemory();
  resetColumnMemory();
  try {
    localStorage.clear();
  } catch {
    // No storage in this environment: nothing to clear.
  }
});

const LABELS = ['Name', 'Namespace', 'Revision', 'Status', 'Chart', 'Chart version', 'App version', 'Updated', 'Description'];

/** The columns as `internal/web` declares them, keyed as `columnKey` keys them. */
function columns(hidden: readonly string[] = []): Column[] {
  return LABELS.map((label, index) => ({
    key: `${label.toLowerCase().replace(/ /g, '-')}-${String(index)}`,
    label,
    visible: !hidden.includes(label),
    order: index,
    width: null,
    sortable: true,
    secondary: false,
    gauged: false,
    copyable: false,
    prose: false,
    role: index === 0 ? 'name' : 'other',
    filterable: label === 'Status' || label === 'Chart',
    ...(index === 0 ? { lockedNote: 'always shown — it is what says which object each row is' } : {}),
  }));
}

function release(name: string, status: string, chart: string): Record<string, unknown> {
  return {
    name,
    namespace: 'default',
    revision: 1,
    chart,
    chartVersion: '1.0.0',
    appVersion: '1.0.0',
    description: 'Install complete',
    status,
    updatedAt: '2026-09-12T08:56:00Z',
    state: 'ok',
  };
}

function helmAnswer(hidden: readonly string[] = [], customised = false): unknown {
  return {
    state: 'ok',
    items: [
      release('ingress', 'failed', 'ingress-nginx'),
      release('cache', 'deployed', 'redis'),
      release('queue', 'deployed', 'rabbitmq'),
    ],
    columns: columns(hidden),
    ...(customised ? { columnsCustomised: true } : {}),
  };
}

const capabilities = (keeps: boolean): unknown => ({
  mode: keeps ? 'local' : 'shared',
  localShell: keeps,
  podExec: true,
  nodeShell: false,
  assistant: false,
  portForward: false,
  preferences: keeps,
  replicated: false,
  readOnly: { writesRefused: false, detailWritesRefused: false },
});

/**
 * Every request answered: the Helm list from `answer()`, which a test may
 * change to stand for what the store now holds. Every write is recorded with
 * its body and answered 204.
 */
function serve(keeps: boolean, answer: () => unknown): { writes: { path: string; body: unknown }[] } {
  const writes: { path: string; body: unknown }[] = [];
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const path = new URL(raw, 'http://x').pathname;
    if (init?.method !== undefined && init.method !== 'GET') {
      writes.push({ path, body: typeof init.body === 'string' ? JSON.parse(init.body) : init.body });
      return Promise.resolve(new Response(null, { status: 204 }));
    }
    const replies: Record<string, unknown> = {
      '/api/session': {
        identity: 'alice',
        idleExpiresAt: '2999-01-01T00:00:00Z',
        absoluteExpiresAt: '2999-01-01T00:00:00Z',
      },
      '/api/clusters': { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] },
      '/api/c/prod/nav': { state: 'ok', data: { groups: [] } },
      '/api/c/prod/extensions': { state: 'empty', items: [] },
      '/api/c/prod/namespaces': { state: 'ok', items: [{ name: 'default' }] },
      '/api/capabilities': capabilities(keeps),
      '/api/c/prod/helm': answer(),
    };
    const body = replies[path];
    if (body === undefined) return Promise.resolve(new Response(null, { status: 404 }));
    return Promise.resolve(
      new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    );
  });
  return { writes };
}

function page(): HTMLElement {
  const element = document.getElementById('page');
  if (element === null) throw new Error('#page is missing');
  return element;
}

function headings(): string[] {
  return [...page().querySelectorAll('thead th [data-label]')].map((label) => (label.textContent ?? '').trim());
}

/** One column's part of the open filter panel. */
function inFilter(column: string): ReturnType<typeof within> {
  const section = document.querySelector<HTMLElement>(`[data-filter-column="${column}"]`);
  if (section === null) throw new Error(`the filter panel offers no ${column}`);
  return within(section);
}

async function listed(): Promise<void> {
  await waitFor(() => {
    expect(page().querySelector('[data-release="default/ingress"]')).not.toBeNull();
  });
}

describe('the Helm list has the resource lists’ funnel', () => {
  it('offers Status and Chart, and narrows the rows to a chosen status', async () => {
    serve(true, () => helmAnswer());
    const { router } = mountApp('/c/prod/helm');
    await listed();
    expect(page().textContent).toContain('3 listed');

    fireEvent.click(screen.getByRole('button', { name: /^Filter by column/ }));
    const offered = [...document.querySelectorAll('[data-filter-column]')].map((each) =>
      each.getAttribute('data-filter-column'),
    );
    expect(offered).toEqual(['Status', 'Chart']);

    fireEvent.click(inFilter('Status').getByRole('button', { name: /^Status/ }));
    fireEvent.click(inFilter('Status').getByRole('button', { name: 'failed' }));

    await waitFor(() => {
      expect(page().textContent).toContain('1 of 3 listed');
    });
    expect(page().querySelector('[data-release="default/cache"]')).toBeNull();
    expect(router.state.location.search).toContain('filter=Status%3Afailed');
    // The choice is a chip, and the chip takes it away.
    fireEvent.click(screen.getByRole('button', { name: 'Remove the filter Status: failed' }));
    await waitFor(() => {
      expect(page().textContent).toContain('3 listed');
    });
  });

  it('keeps the other values of a filtered column on offer, so a second one can be chosen', async () => {
    serve(true, () => helmAnswer());
    mountApp('/c/prod/helm?filter=Chart%3Aredis');
    await waitFor(() => {
      expect(page().textContent).toContain('1 of 3 listed');
    });
    fireEvent.click(screen.getByRole('button', { name: /^Filter by column/ }));
    fireEvent.click(inFilter('Chart').getByRole('button', { name: /^Chart/ }));
    // The other charts stay on offer beside the chosen one.
    expect(inFilter('Chart').getByRole('button', { name: 'redis' }).getAttribute('aria-pressed')).toBe('true');
    fireEvent.click(inFilter('Chart').getByRole('button', { name: 'rabbitmq' }));
    await waitFor(() => {
      expect(page().textContent).toContain('2 of 3 listed');
    });
  });

  it('drops a filter on a column this list cannot be filtered by, and says so', async () => {
    serve(true, () => helmAnswer());
    const { router } = mountApp('/c/prod/helm?filter=Name%3Aingress');
    await listed();
    await waitFor(() => {
      expect(router.state.location.search).not.toContain('filter=');
    });
    expect(page().textContent).toContain('3 listed');
    expect(page().textContent).toContain('Not a filter on this list: Name: ingress.');
  });
});

describe('the Helm list has the resource lists’ Columns menu', () => {
  it('hides a column through the console’s column store, under the kind helm', async () => {
    let stored: readonly string[] = [];
    const { writes } = serve(true, () => helmAnswer(stored, stored.length > 0));
    mountApp('/c/prod/helm');
    await listed();
    expect(headings()).toContain('Description');

    fireEvent.click(screen.getByRole('button', { name: 'Columns' }));
    // Name is a fact, not a toggle that refuses.
    expect(screen.getByRole('checkbox', { name: 'Name' })).toHaveProperty('disabled', true);
    stored = ['Description'];
    fireEvent.click(screen.getByRole('checkbox', { name: 'Description' }));

    await waitFor(() => {
      expect(writes).toEqual([
        {
          path: '/api/preferences/columns/show',
          body: { kind: 'helm', column: 'description-8', visible: false },
        },
      ]);
    });
    // The list is read again and draws what the store holds.
    await waitFor(() => {
      expect(headings()).not.toContain('Description');
    });
    // Something is stored, so the `⋯` offers the way back.
    fireEvent.click(screen.getByRole('button', { name: 'More table options' }));
    expect(screen.getByRole('button', { name: 'Reset columns' })).toBeTruthy();
  });

  it('keeps the arrangement in this browser where the console keeps none', async () => {
    const { writes } = serve(false, () => helmAnswer());
    mountApp('/c/prod/helm');
    await listed();

    fireEvent.click(screen.getByRole('button', { name: 'Columns' }));
    // No gesture is kept anywhere until the capabilities have answered, so the
    // box is pressed until one is: only while it is still ticked.
    await waitFor(() => {
      const box = screen.getByRole<HTMLInputElement>('checkbox', { name: 'Chart version' });
      if (box.checked) fireEvent.click(box);
      expect(headings()).not.toContain('Chart version');
    });
    expect(writes).toEqual([]);
    expect(localStorage.getItem('kubetower.columns') ?? '').toContain('"helm"');
  });

  it('draws the columns of an answer that carries none, with the funnel and Columns', async () => {
    serve(true, () => {
      const answer = helmAnswer() as Record<string, unknown>;
      delete answer.columns;
      return answer;
    });
    mountApp('/c/prod/helm');
    await listed();
    expect(headings()).toEqual(LABELS);
    expect(screen.getByRole('button', { name: 'Columns' })).toBeTruthy();
    expect(screen.getByRole('button', { name: /^Filter by column/ })).toBeTruthy();
  });
});
