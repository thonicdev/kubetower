import { cleanup, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp, stubFetch } from '../testing/harness';
import { trail } from '../shell/pageFacts';

/**
 * Every route sets its title and its breadcrumb: a page that declared nothing
 * would leave the window titled "KubeTower" alone and the breadcrumb stopping
 * at the cluster. The topbar draws whatever a page
 * declares (`shell/pageFacts.tsx`), so these pages are asked what they declared,
 * through the whole application, by reading the two places it lands.
 *
 * An unnamed cluster is used on purpose: the window title then reads
 * `KubeTower · <page>` with nothing between, which is the exact string to pin.
 */

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  resetKeyboardStack();
  localStorage.clear();
  document.title = '';
});

const CLUSTER = 'docker-desktop';

function breadcrumb(): HTMLElement {
  return screen.getByRole('navigation', { name: 'Breadcrumb' });
}

/**
 * The window title, and the breadcrumb's last items, one per segment. A page
 * whose title is a path declares it in segments, so the
 * items are the title cut at its slashes unless `segments` says otherwise - a
 * page that declares a plain title draws it as one item, slashes and all.
 */
async function expectTitled(page: string, segments: readonly string[] = page.split(' / ')): Promise<void> {
  await waitFor(() => expect(document.title).toBe(`KubeTower · ${page}`));
  const items = [...breadcrumb().querySelectorAll('li:not([aria-hidden])')].map((li) => li.textContent ?? '');
  expect(items.slice(-segments.length)).toEqual(segments);
}

describe('the title a page declares', () => {
  it.each([
    ['the overview', `/c/${CLUSTER}`, 'Overview', undefined],
    ['the Helm list', `/c/${CLUSTER}/helm`, 'Helm releases', undefined],
    ['a Helm release', `/c/${CLUSTER}/helm/monitoring/grafana`, 'Helm releases / monitoring / grafana', undefined],
    // A plain title: one item.
    ['Settings, General', '/settings/general', 'Settings / General', ['Settings / General']],
  ])('%s is named in the window and in the breadcrumb', async (_what, address, title, segments) => {
    stubFetch({});
    mountApp(address);
    await expectTitled(title, segments);
  });

  it('the object page names the kind, the namespace and the object', async () => {
    stubFetch({
      [`/api/c/${CLUSTER}/r/pods/default/api-0`]: {
        body: {
          state: 'ok',
          data: { kind: 'pods', name: 'api-0', namespace: 'default', title: 'Pod', tabs: [], sections: [] },
        },
      },
    });
    mountApp(`/c/${CLUSTER}/pods/default/api-0`);
    // The Kind as the payload writes it once it has answered; the route's
    // plural before that, which is still the right object.
    await expectTitled('Pod / default / api-0');
  });

  it('a cluster-scoped object has no empty namespace segment', async () => {
    stubFetch({
      [`/api/c/${CLUSTER}/r/nodes/worker-1`]: {
        body: { state: 'ok', data: { kind: 'nodes', name: 'worker-1', title: 'Node', tabs: [], sections: [] } },
      },
    });
    mountApp(`/c/${CLUSTER}/nodes/worker-1`);
    await expectTitled('Node / worker-1');
  });
});

describe('trail', () => {
  it('joins the labels that are present into the title', () => {
    const titleOf = (...labels: (string | undefined)[]): string | null =>
      trail(...labels.map((label) => (label === undefined ? undefined : { label }))).title;
    expect(titleOf('Pod', 'default', 'api-0')).toBe('Pod / default / api-0');
    expect(titleOf('Node', undefined, 'worker-1')).toBe('Node / worker-1');
    expect(titleOf('Node', '', 'worker-1')).toBe('Node / worker-1');
  });

  it('titles the page with the labels and keeps each segment’s place', () => {
    expect(trail({ label: 'Pod', to: '/c/x/pods' }, undefined, { label: '' }, { label: 'api-0' })).toEqual({
      title: 'Pod / api-0',
      trail: [{ label: 'Pod', to: '/c/x/pods' }, { label: 'api-0' }],
    });
  });
});
