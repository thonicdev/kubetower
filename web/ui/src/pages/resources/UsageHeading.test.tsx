import { QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { ListPage } from '../../api';
import { createQueryClient } from '../../query/client';
import { ShellProvider } from '../../shell/ShellContext';
import { resetViewMemory } from '../../shell/viewMemory';
import { ResourceList } from './ResourceList';

/**
 * Why a table's usage columns are empty, at page altitude: the route, the
 * query, the envelope and the rendered page.
 *
 * The server's answer has two states - a first read still on its way
 * (`pending`) and a read that gave nothing (`unmeasured`). Neither draws a line
 * above the table, because the dashes already say there is no figure. The two
 * answers stay apart one hover away: the server's sentence is the `title` of
 * each empty usage heading. These pin that the line is gone, that the heading
 * carries the sentence for each state, and that a filled column carries none.
 */

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  resetViewMemory();
  localStorage.clear();
});

const LIST = '/api/c/prod/r/nodes';

function nodes(extra: Partial<ListPage>, cpu: string): ListPage {
  return {
    kind: 'nodes',
    label: 'Nodes',
    namespaced: false,
    refreshSeconds: 1,
    columns: [
      { key: 'name', label: 'Name', visible: true, order: 0, sortable: true, secondary: false, gauged: false, copyable: false, prose: false, filterable: false, role: 'name' },
      { key: 'cpu', label: 'CPU', visible: true, order: 1, sortable: true, align: 'end', secondary: false, gauged: true, copyable: false, prose: false, filterable: false, role: 'other' },
    ],
    rows: [
      {
        id: 'desktop-control-plane',
        name: 'desktop-control-plane',
        cells: { name: { text: 'desktop-control-plane' }, cpu: { text: cpu } },
      },
    ],
    count: 1,
    ...extra,
  };
}

const pending = nodes(
  {
    measurementState: 'pending',
    measurementNote: 'Reading the measurements for this kind; the usage columns fill in when that read returns.',
  },
  '—',
);
const unmeasured = nodes(
  {
    measurementState: 'unmeasured',
    measurementNote: 'No measurements for this kind right now, so the usage columns are empty.',
  },
  '—',
);
const measured = nodes({}, '230m');

/** Answers the list with each page in turn, and keeps answering with the last. */
function serve(...pages: ListPage[]): void {
  let asked = 0;
  vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
    const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
    const key = path.split('?')[0] ?? path;
    let body: unknown;
    if (key === LIST) {
      body = { state: 'ok', data: pages[Math.min(asked, pages.length - 1)] };
      asked += 1;
    } else if (key === '/api/preferences') {
      body = { rail: { barCount: 1, bars: [], hidden: [], tiles: {} }, columns: { kinds: {} } };
    } else if (key === '/api/c/prod/namespaces') {
      body = { state: 'ok', items: [{ name: 'default' }] };
    } else {
      return Promise.resolve(
        new Response(JSON.stringify({ status: 404, title: 'Not Found', code: 'not_found' }), {
          status: 404,
          headers: { 'Content-Type': 'application/problem+json' },
        }),
      );
    }
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }));
  });
}

function mount(): void {
  const router = createMemoryRouter(
    [
      { path: '/c/:cluster/:kind', element: <ResourceList /> },
      { path: '/health', element: <p>the health page</p> },
    ],
    { initialEntries: ['/c/prod/nodes'] },
  );
  render(
    <QueryClientProvider client={createQueryClient()}>
      <ShellProvider extensions={[]}>
        <RouterProvider router={router} />
      </ShellProvider>
    </QueryClientProvider>,
  );
}

function note(): HTMLElement | null {
  const found = document.querySelector('[data-measurement]');
  return found instanceof HTMLElement ? found : null;
}

/** The label inside a column heading, which is what carries the hover. */
function heading(label: string): HTMLElement | undefined {
  return [...document.querySelectorAll('th span')].find(
    (one): one is HTMLElement => one instanceof HTMLElement && one.textContent === label && one.children.length === 0,
  );
}

describe('why the usage columns are empty', () => {
  it('draws no note above the table for a first read on its way, and puts its sentence on the heading', async () => {
    serve(pending);
    mount();

    await waitFor(() => {
      expect(heading('CPU')?.title).toBe(
        'Reading the measurements for this kind; the usage columns fill in when that read returns.',
      );
    });
    expect(note()).toBeNull();
    expect(document.body.textContent).not.toContain('when that read returns');
    // The name column is not a usage column, however it reads.
    expect(heading('Name')?.title ?? '').toBe('');
  });

  it('draws no note for a read that gave nothing, and puts its sentence on the heading', async () => {
    serve(unmeasured);
    mount();

    await waitFor(() => {
      expect(heading('CPU')?.title).toBe('No measurements for this kind right now, so the usage columns are empty.');
    });
    expect(note()).toBeNull();
    expect(document.body.textContent).not.toContain('No measurements for this kind');
    expect(document.querySelector('a[href="/health"]')).toBeNull();
  });

  it('drops the hover when the gauges arrive', async () => {
    serve(pending, measured);
    mount();

    await waitFor(
      () => {
        expect(screen.getByText('230m')).toBeTruthy();
      },
      { timeout: 4_000 },
    );
    expect(heading('CPU')?.title ?? '').toBe('');
    expect(note()).toBeNull();
  });
});
