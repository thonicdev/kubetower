import { render, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DetailPage } from '../../api';
import { createQueryClient } from '../../query/client';
import { QueryClientProvider } from '@tanstack/react-query';
import { stubFetch } from '../../testing/harness';
import { ObjectDetail } from './ObjectDetail';

/**
 * A Pod's addresses on its Summary tile, as `internal/detail` writes them: an
 * address carries `copy` and gets a copy button; an address the pod does not
 * have yet arrives empty and is drawn as "—", with nothing to copy.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

const PATH = '/api/c/prod/r/pods/shop-prod/api-1';

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
        { label: 'Node', value: 'worker-1', prose: false, pill: false },
        { label: 'Pod IPv4', value: '10.244.1.7', prose: false, pill: false, copy: '10.244.1.7' },
        { label: 'Pod IPv6', value: 'fd00:10:244:1::7', prose: false, pill: false, copy: 'fd00:10:244:1::7' },
        { label: 'Node IP', value: '', prose: false, pill: false },
      ],
    },
  ],
  labels: {},
  relations: [],
};

async function mount(chrome: 'drawer' | 'page'): Promise<HTMLElement> {
  stubFetch({ [PATH]: { body: { state: 'ok', data: page } } });
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

function row(container: HTMLElement, label: string): HTMLTableRowElement {
  const rows = [...container.querySelectorAll<HTMLTableRowElement>('[data-section="summary"] tr')];
  const found = rows.find((tr) => tr.querySelector('th')?.textContent === label);
  if (found === undefined) throw new Error(`no ${label} row`);
  return found;
}

describe("a Pod's addresses", () => {
  for (const chrome of ['page', 'drawer'] as const) {
    it(`draws each address with its own copy button, in the ${chrome}`, async () => {
      const container = await mount(chrome);
      for (const [label, ip] of [
        ['Pod IPv4', '10.244.1.7'],
        ['Pod IPv6', 'fd00:10:244:1::7'],
      ] as const) {
        const tr = row(container, label);
        expect(tr.querySelector('td')?.textContent).toContain(ip);
        expect(tr.querySelectorAll('button[aria-label^="Copy this"]')).toHaveLength(1);
      }
    });

    it(`draws an address the pod does not have yet as "—", with no copy button, in the ${chrome}`, async () => {
      const container = await mount(chrome);
      const tr = row(container, 'Node IP');
      expect(tr.querySelector('td')?.textContent).toBe('—');
      expect(tr.querySelector('button')).toBeNull();
    });
  }
});
