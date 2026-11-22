import { waitFor, type RenderResult } from '@testing-library/react';
import type { ReactNode } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DetailPage } from '../../api';
import { renderWithClient, stubFetch } from '../../testing/harness';
import type { ObjectIdentity } from './address';
import { ObjectDetail } from './ObjectDetail';

/**
 * The annotations block, on any kind.
 *
 * What is asserted here is the *client's* half, and the split matters: the
 * server withholds `last-applied-configuration` and masks a Secret's values, and
 * this component applies no rule of its own. So there is no test here that the
 * client filters anything — there is a test that it draws **what arrives**, mask
 * and all, because a second treatment in a second language is the pair that will
 * disagree.
 */

// `cleanup()` and the storage teardown are the shared hook's
// (`ui/src/testing/setup.ts`); a wipe here would be a no-op beside it.
afterEach(() => {
  vi.unstubAllGlobals();
});

const identity: ObjectIdentity = {
  cluster: 'prod',
  kind: 'pods',
  namespace: 'shop-prod',
  name: 'storefront-web',
};

const PATH = '/api/c/prod/r/pods/shop-prod/storefront-web';

function renderPanel(ui: ReactNode): RenderResult {
  const router = createMemoryRouter(
    [
      { path: '/c/:cluster/:kind', element: ui },
      { path: '/health', element: <p>the health page</p> },
    ],
    { initialEntries: ['/c/prod/pods'] },
  );
  return renderWithClient(<RouterProvider router={router} />);
}

function pageWith(extra: Partial<DetailPage>): DetailPage {
  return {
    kind: 'pods',
    namespace: 'shop-prod',
    name: 'storefront-web',
    title: 'Pod',
    tabs: [],
    sections: [],
    ...extra,
  };
}

async function drawn(page: DetailPage): Promise<RenderResult> {
  stubFetch({ [PATH]: { body: { state: 'ok', data: page } } });
  const view = renderPanel(<ObjectDetail identity={identity} chrome="drawer" />);
  await waitFor(() => {
    expect(document.querySelector('[aria-busy="true"]')).toBeNull();
  });
  return view;
}

function block(view: RenderResult): HTMLElement {
  const found = view.container.querySelector<HTMLElement>('[data-section="annotations"]');
  if (found === null) throw new Error('there is no annotations block');
  return found;
}

describe('the annotations block', () => {
  it('draws every annotation as the same chip a label gets, keyed and valued', async () => {
    const view = await drawn(
      pageWith({
        sections: [{ key: 'summary', label: 'Summary', state: 'ok', fields: [{ label: 'Status', value: 'Running', prose: false, pill: false }] }],
        annotations: {
          'meta.helm.sh/release-name': 'shop',
          'deployment.kubernetes.io/revision': '7',
        },
      }),
    );

    const chips = [...block(view).querySelectorAll('[data-chip]')].map((one) => one.textContent);
    // Sorted, so two objects with the same annotations read the same way.
    expect(chips).toEqual([
      'deployment.kubernetes.io/revision=7',
      'meta.helm.sh/release-name=shop',
    ]);
    // The same element the labels use, and not a second presentation invented
    // beside it: `data-chip` is the row's own marker.
    expect(block(view).querySelectorAll('[data-chip]').length).toBe(2);
  });

  it('is closed until it is asked for, and says how many there are while closed', async () => {
    const view = await drawn(
      pageWith({
        annotations: { a: '1', b: '2', c: '3' },
      }),
    );

    const disclosure = block(view).querySelector('details');
    expect(disclosure).not.toBeNull();
    // Closed. On anything Helm owns or `kubectl apply` touched this block is the
    // tallest thing on the page, and the facts are why the panel was opened.
    expect(disclosure?.open).toBe(false);
    expect(block(view).querySelector('summary')?.textContent).toBe('Annotations (3)');
  });

  it('draws what the server sent and decides nothing itself', async () => {
    // A Secret's values arrive already masked. A client that re-decided which
    // ones to hide would be a second treatment in a second language.
    const view = await drawn(
      pageWith({
        annotations: { 'rotation/previous-password': '<masked>' },
      }),
    );

    expect(block(view).textContent).toContain('rotation/previous-password=<masked>');
    expect(block(view).textContent).not.toContain('masked because');
  });

  it('draws no sentence about what was withheld, and nothing when nothing is left', async () => {
    // The Secret whose only annotation was `last-applied-configuration`: no
    // chips, no disclosure and no sentence explaining the absence.
    const view = await drawn(pageWith({ annotations: {} }));

    expect(view.container.querySelector('[data-section="annotations"]')).toBeNull();
    expect(view.container.textContent ?? '').not.toContain('last-applied-configuration');
  });

  it('draws nothing at all on an object that carries none', async () => {
    const view = await drawn(
      pageWith({
        sections: [{ key: 'summary', label: 'Summary', state: 'ok', fields: [{ label: 'Status', value: 'Running', prose: false, pill: false }] }],
      }),
    );
    expect(view.container.querySelector('[data-section="annotations"]')).toBeNull();
  });

  it('is enough on its own to keep the panel from saying there is nothing here', async () => {
    // `Body`'s emptiness check enumerates the members by hand and its own
    // comment warns that every member added to the body has to be added there
    // too. An annotations-only object - a ServiceAccount that Helm owns is the
    // ordinary one - rendered "Nothing here" over the one thing it had to say.
    const view = await drawn(pageWith({ annotations: { 'meta.helm.sh/release-name': 'shop' } }));

    expect(view.container.querySelector('[data-section="annotations"]')).not.toBeNull();
    expect(view.container.textContent ?? '').not.toContain('Nothing here');
  });

  it('keeps a value-less key drawn as the key alone, on both rows', async () => {
    // `Chips` is the one home of this rule. `app=api`
    // and `standalone` say different things and `standalone=` says a third that
    // is true of neither - and the rule has one home for both rows.
    const view = await drawn(
      pageWith({
        labels: { standalone: '', app: 'api' },
        annotations: { 'marker.example.com/set': '' },
      }),
    );

    const labels = [...view.container.querySelectorAll('[data-section="labels"] [data-chip]')].map(
      (one) => one.textContent,
    );
    expect(labels).toEqual(['app=api', 'standalone']);
    const annotations = [...block(view).querySelectorAll('[data-chip]')].map((one) => one.textContent);
    expect(annotations).toEqual(['marker.example.com/set']);
  });

  it('links a label chip to this kind\'s list, selected on that label', async () => {
    // #739: a pod labelled network=redis opens the pods carrying it.
    const view = await drawn(pageWith({ labels: { network: 'redis', standalone: '' } }));

    const hrefs = [...view.container.querySelectorAll('[data-section="labels"] a[data-chip]')].map((one) =>
      one.getAttribute('href'),
    );
    expect(hrefs).toEqual([
      '/c/prod/pods?namespace=shop-prod&selector=network%3Dredis',
      '/c/prod/pods?namespace=shop-prod&selector=standalone%3D',
    ]);
    // The annotations stay plain chips.
    expect(view.container.querySelector('[data-section="annotations"] a[data-chip]')).toBeNull();
  });
});
