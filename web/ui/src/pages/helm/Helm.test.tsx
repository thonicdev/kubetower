import { act, fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../../keyboard/stack';
import { chosenNamespaces, rememberNamespaces, resetNamespaceMemory } from '../../shell/namespaceMemory';
import { mountApp, stubFetch, type StubReply, type StubRoutes } from '../../testing/harness';

/**
 * The two Helm pages: the releases list and one release's page.
 *
 * These are the behaviors that are about the page rather than about the view
 * model (the view-model half is covered by `internal/api/helmmembers_test.go`
 * and by `internal/helm`'s own tests):
 *
 *   - the chart version and the app version are both drawn and named apart;
 *   - the four absences reach the page as four sentences, and **none of them
 *     draws the releases table**;
 *   - a failed upgrade's row says which revision is running;
 *   - the sentence columns are the cells that may wrap;
 *   - the revision history draws each revision's own chart version;
 *   - an unreadable revision keeps its row and says why;
 *   - no descriptive sentence on either page or in the drawer;
 *   - nothing on either page writes;
 *   - a release's address is its own page, and the way back keeps the selection;
 *   - an unknown status is not painted as broken;
 *   - a time Helm never recorded draws an em dash and not 1970.
 *
 * That nothing on either page writes is asserted as the absence of any form or
 * mutating control, which is the property rather than a spelling.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
  resetNamespaceMemory();
});

const session = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

const clusters = { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } };

function shell(extra: StubRoutes): StubRoutes {
  return {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    ...extra,
  };
}

function page(): HTMLElement {
  const element = document.getElementById('page');
  if (element === null) throw new Error('#page is missing');
  return element;
}

function drawer(): HTMLElement {
  const element = document.getElementById('drawer');
  if (element === null) throw new Error('#drawer is missing');
  return element;
}

/** The release page's Back, at the left of its header. */
function pageBack(): HTMLAnchorElement {
  const found = page().querySelector<HTMLAnchorElement>('[data-object-chrome="page"] header a[data-page-back]');
  if (found === null) throw new Error('no way back');
  return found;
}

const namespaceList: StubReply = {
  body: { state: 'ok', items: [{ name: 'default' }, { name: 'kube-system' }] },
};

/** Opens the list's namespace picker and clicks one of its entries - a
 *  namespace, or "All namespaces". */
async function pick(entry: string): Promise<void> {
  const button = [...page().querySelectorAll<HTMLButtonElement>('button[aria-expanded][title]')].find(
    (one) => one.title === 'Every namespace' || one.title === one.textContent,
  );
  if (button === undefined) throw new Error('no namespace picker');
  if (button.getAttribute('aria-expanded') !== 'true') fireEvent.click(button);
  const choice = await waitFor(() => {
    const panel = document.getElementById(button.getAttribute('aria-controls') ?? '');
    // The entry's own name is its last span; the tick shares the button.
    const found = [...(panel?.querySelectorAll('button') ?? [])].find((one) => {
      const spans = one.querySelectorAll('span');
      return spans[spans.length - 1]?.textContent?.trim() === entry;
    });
    if (found === undefined) throw new Error(`no "${entry}" in the namespace picker`);
    return found;
  });
  fireEvent.click(choice);
}

/** Click the row of one release and wait for its panel in the drawer. */
async function openInDrawer(release: string): Promise<HTMLElement> {
  const row = await waitFor(() => {
    const found = page().querySelector(`[data-release="${release}"]`)?.closest('tr');
    if (found === null || found === undefined) throw new Error(`no row for ${release}`);
    return found;
  });
  fireEvent.click(row);
  return waitFor(() => {
    const panel = drawer().querySelector<HTMLElement>(`[data-object-chrome="drawer"][data-helm-release="${release}"]`);
    if (panel === null) throw new Error(`no drawer panel for ${release}`);
    return panel;
  });
}

/**
 * A failed upgrade, where the current and the running revision differ.
 *
 * Revision 41 is current and `failed`; revision 40 is the one Helm marks
 * deployed. A row reporting only "41, failed" invites the conclusion that
 * nothing is running.
 */
const releases: StubReply = {
  body: {
    state: 'ok',
    items: [
      {
        name: 'ingress',
        namespace: 'default',
        revision: 41,
        deployedRevision: 40,
        chart: 'ingress-nginx',
        chartVersion: '4.11.2',
        appVersion: '1.11.2',
        deprecated: true,
        description: 'Upgrade "ingress" failed: timed out waiting for the condition',
        note: 'Running: revision 40 is the one Helm marks deployed',
        status: 'failed',
        updatedAt: '2026-09-12T08:56:00Z',
        state: 'ok',
      },
      {
        // Listed and marked, never dropped: a release that exists and cannot be
        // read is not a release that does not exist.
        name: 'sh.helm.release.v1.broken.v1',
        namespace: 'argocd',
        state: 'unreadable',
        reason: { code: 'undecodable', message: 'unexpected EOF' },
      },
    ],
  },
};

const releaseDetail: StubReply = {
  body: {
    state: 'ok',
    data: {
      release: {
        name: 'ingress',
        namespace: 'default',
        revision: 41,
        deployedRevision: 40,
        chart: 'ingress-nginx',
        chartVersion: '4.11.2',
        appVersion: '1.11.2',
        deprecated: true,
        description: 'Upgrade "ingress" failed: timed out waiting for the condition',
        note: 'Running: revision 40 is the one Helm marks deployed',
        status: 'failed',
        updatedAt: '2026-09-12T08:56:00Z',
        state: 'ok',
      },
      revisions: [
        {
          revision: 41,
          status: 'failed',
          tone: 'bad',
          updatedAt: '2026-09-12T08:56:00Z',
          appVersion: '1.11.2',
          description: 'Upgrade "ingress" failed',
          mark: 'current',
          // No chartVersion, and the note is why.
          note: "this revision's stored payload would not decode",
        },
        {
          revision: 40,
          status: 'deployed',
          tone: 'ok',
          updatedAt: '2026-09-10T08:00:00Z',
          chartVersion: '4.11.1',
          appVersion: '1.11.1',
          description: 'Upgrade complete',
          mark: 'deployed',
        },
      ],
      historyMax: 2,
      valuesSupplied: true,
      notesRendered: true,
    },
  },
};

describe('the releases list', () => {
  it('says the list is narrowed to the namespaces where the person may list Secrets', async () => {
    const note =
      'You may not list Secrets in every namespace, and a Helm release is stored as one, so this shows the releases of the namespaces where you may.';
    const body = releases.body as Record<string, unknown>;
    stubFetch(shell({ '/api/c/prod/helm': { body: { ...body, accessNote: note } } }));
    mountApp('/c/prod/helm');

    await waitFor(() => {
      expect(page().textContent).toContain('ingress-nginx');
    });
    expect(document.querySelector('[data-note="access"]')?.textContent).toBe(note);
  });

  it('says a narrowed list with no release is about the namespaces the person may list', async () => {
    stubFetch(
      shell({
        '/api/c/prod/helm': {
          body: { state: 'empty', items: [], accessNote: 'You may not list Secrets in every namespace.' },
        },
      }),
    );
    mountApp('/c/prod/helm');

    await waitFor(() => {
      expect(document.querySelector('[data-empty="helm"]')?.textContent).toBe(
        'No Helm release in the namespaces where you may list Secrets.',
      );
    });
  });

  it('draws the chart version and the app version, named apart', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases }));
    mountApp('/c/prod/helm');

    await waitFor(() => {
      expect(page().textContent).toContain('ingress-nginx');
    });
    const text = page().textContent ?? '';

    // Two columns and not one. They are different numbers, and a single column
    // labeled "Version" is a question rather than an answer.
    expect(text).toContain('Chart version');
    expect(text).toContain('App version');
    expect(text).toContain('4.11.2');
    expect(text).toContain('1.11.2');
  });

  it('says which revision is running on a failed upgrade', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases }));
    mountApp('/c/prod/helm');

    await waitFor(() => {
      expect(page().textContent).toContain('failed');
    });
    const text = page().textContent ?? '';

    // Both facts on the row. "41, failed" alone reads as nothing running;
    // "40, deployed" alone would hide the failure.
    expect(text).toContain('41');
    expect(text).toContain('Running: revision 40 is the one Helm marks deployed');
  });

  it('marks a deprecated chart', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases }));
    mountApp('/c/prod/helm');

    await waitFor(() => {
      expect(page().textContent).toContain('deprecated');
    });
  });

  it('lists an undecodable release rather than dropping it', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases }));
    mountApp('/c/prod/helm');

    await waitFor(() => {
      expect(page().textContent).toContain('sh.helm.release.v1.broken.v1');
    });
    const text = page().textContent ?? '';
    // Named, and with the reason. Dropping it silently is how a console reports
    // nine releases on a cluster that has ten.
    expect(text).toContain('unexpected EOF');
    expect(text).toContain('could not be read');

    // And it opens nothing: there is no panel for a release that would not
    // read, so its row is no tabstop and no control.
    const row = page().querySelector('[data-release="argocd/sh.helm.release.v1.broken.v1"]')?.closest('tr');
    expect(row).not.toBeNull();
    expect(row?.hasAttribute('tabindex')).toBe(false);
    expect(row?.querySelector('a')).toBeNull();
    // The readable one is the control.
    const readable = page().querySelector('[data-release="default/ingress"]')?.closest('tr');
    expect(readable?.getAttribute('tabindex')).toBe('0');
  });

  it('marks the sentence columns as the cells that may wrap', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases }));
    mountApp('/c/prod/helm');

    await waitFor(() => {
      expect(page().textContent).toContain('ingress-nginx');
    });

    // The mechanism is inheritance: `white-space` reaches
    // every element inside the cell, so an `overflow-wrap` on a span in there is
    // inert. The class goes on the cell.
    const description = [...page().querySelectorAll('td')].find((cell) =>
      (cell.textContent ?? '').includes('timed out waiting for the condition'),
    );
    expect(description?.classList.contains('prose')).toBe(true);

    const status = [...page().querySelectorAll('td')].find((cell) =>
      (cell.textContent ?? '').includes('Running: revision 40'),
    );
    expect(status?.classList.contains('message')).toBe(true);
  });

  it('scrolls rather than clipping a table wider than its panel', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases }));
    mountApp('/c/prod/helm');

    await waitFor(() => {
      expect(page().querySelector('table')).not.toBeNull();
    });
    for (const table of page().querySelectorAll('table')) {
      expect(table.classList.contains('kt-grid')).toBe(true);
      expect(table.parentElement?.classList.contains('table-scroll')).toBe(true);
    }
  });
});

describe('the list is a resource list', () => {
  it('draws the shared grid, in the panel, with its columns', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases }));
    mountApp('/c/prod/helm');

    const table = await waitFor(() => {
      const found = page().querySelector<HTMLTableElement>('table[aria-label="Helm releases"]');
      if (found === null) throw new Error('no grid');
      return found;
    });
    // `DataGrid`'s own markup: the class, the scroll box, and the panel
    // the grid's fixed layout is scoped to.
    expect(table.classList.contains('kt-grid')).toBe(true);
    expect(table.querySelector('colgroup')).not.toBeNull();
    expect(table.parentElement?.classList.contains('table-scroll')).toBe(true);
    expect(table.parentElement?.parentElement?.id).toBe('panel');
    // The columns, in order, with the grid's sortable headings.
    const headings = [...table.querySelectorAll('thead th [data-label]')].map((label) => (label.textContent ?? '').trim());
    expect(headings).toEqual([
      'Name',
      'Namespace',
      'Revision',
      'Status',
      'Chart',
      'Chart version',
      'App version',
      'Updated',
      'Description',
    ]);
    // The resource list's head: heading, count, search box.
    expect(page().querySelector('h1')?.textContent).toBe('Helm releases');
    expect(page().textContent).toContain('2 listed');
    expect(page().querySelector('input[name="search"]')).not.toBeNull();
  });

  it('narrows the rows by the search box, and says so when none match', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases }));
    mountApp('/c/prod/helm?search=nginx');

    await waitFor(() => {
      expect(page().textContent).toContain('1 of 2 listed');
    });
    expect(page().querySelector('[data-release="default/ingress"]')).not.toBeNull();
    expect(page().querySelector('[data-release="argocd/sh.helm.release.v1.broken.v1"]')).toBeNull();
  });

  it('opens a release in the drawer and stays on the list', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases, '/api/c/prod/helm/default/ingress': releaseDetail }));
    const { router } = mountApp('/c/prod/helm');

    const panel = await openInDrawer('default/ingress');
    await waitFor(() => {
      expect(panel.textContent).toContain('Revisions');
    });
    expect(panel.querySelector('h2')?.textContent).toBe('ingress');
    expect(panel.querySelector('[data-fact="status"]')?.textContent).toContain('Running: revision 40');
    // No navigation: the list is still the page behind the drawer.
    expect(router.state.location.pathname).toBe('/c/prod/helm');
    expect(page().querySelector('table[aria-label="Helm releases"]')).not.toBeNull();
  });

  it('opens the release full size from the drawer, keeping the selection', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases, '/api/c/prod/helm/default/ingress': releaseDetail }));
    const { router } = mountApp('/c/prod/helm?namespace=default');

    const panel = await openInDrawer('default/ingress');
    const link = panel.querySelector<HTMLAnchorElement>('a[aria-label="Open as page"]');
    expect(link?.getAttribute('href')).toBe('/c/prod/helm/default/ingress?namespace=default');
    fireEvent.click(link as HTMLAnchorElement);

    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/prod/helm/default/ingress');
    });
    await waitFor(() => {
      expect(page().querySelector('[data-object-chrome="page"] h1')?.textContent).toBe('ingress');
    });
    // The page is the same panel: its facts and its history.
    expect(page().querySelector('[data-revision="41"]')).not.toBeNull();
    // And the drawer emptied on the way, as on every navigation.
    await waitFor(() => {
      expect(drawer().querySelector('[data-object-chrome="drawer"]')).toBeNull();
    });
  });

  // No minimize control on the release's panel, and Back on the page it is
  // opened as.
  it('gives the drawer Open as page and Close, and no minimize control', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases, '/api/c/prod/helm/default/ingress': releaseDetail }));
    mountApp('/c/prod/helm');

    const panel = await openInDrawer('default/ingress');
    const controls = [...panel.querySelectorAll(':scope > header a, :scope > header button')].map(
      (one) => one.getAttribute('aria-label') ?? one.textContent,
    );
    expect(controls).toEqual(['Open as page', 'Close']);
    expect(panel.querySelector('[data-drawer-minimise]')).toBeNull();
    expect(panel.querySelector('[data-page-back]')).toBeNull();
  });

  it('returns from Open as page to the list, with the release open in the drawer again', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases, '/api/c/prod/helm/default/ingress': releaseDetail }));
    const { router } = mountApp('/c/prod/helm?namespace=default');

    const panel = await openInDrawer('default/ingress');
    fireEvent.click(panel.querySelector<HTMLAnchorElement>('a[aria-label="Open as page"]') as HTMLAnchorElement);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/prod/helm/default/ingress');
    });
    const back = await waitFor(() => pageBack());
    expect(back.closest('header')?.firstElementChild).toBe(back);
    expect(back.getAttribute('href')).toBe('/c/prod/helm?namespace=default&open=default%2Fingress');

    fireEvent.click(back);
    await waitFor(() => {
      expect(drawer().querySelector('[data-object-chrome="drawer"][data-helm-release="default/ingress"]')).not.toBeNull();
    });
    expect(router.state.location.pathname).toBe('/c/prod/helm');
    // The arrival takes `open` out of the address once it is read; the
    // selection stays.
    await waitFor(() => {
      expect(router.state.location.search).toBe('?namespace=default');
    });
    expect(page().querySelector('table[aria-label="Helm releases"]')).not.toBeNull();
    expect(drawer().hidden).toBe(false);
  });

  it('goes back to the releases list from a page loaded directly', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases, '/api/c/prod/helm/default/ingress': releaseDetail }));
    const { router } = mountApp('/c/prod/helm/default/ingress');

    const back = await waitFor(() => pageBack());
    expect(back.getAttribute('href')).toBe('/c/prod/helm');
    fireEvent.click(back);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/prod/helm');
    });
    expect(drawer().hidden).toBe(true);
  });

  it('carries the selection as it is now, not as it was when the drawer opened', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases, '/api/c/prod/helm/default/ingress': releaseDetail }));
    const { router } = mountApp('/c/prod/helm?namespace=default');

    const panel = await openInDrawer('default/ingress');
    await act(async () => {
      await router.navigate('/c/prod/helm?namespace=default&namespace=kube-system');
    });
    await waitFor(() => {
      expect(panel.querySelector('a[aria-label="Open as page"]')?.getAttribute('href')).toBe(
        '/c/prod/helm/default/ingress?namespace=default&namespace=kube-system',
      );
    });
  });

  it('reads every namespace of the repeated parameter on the page', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases, '/api/c/prod/helm/default/ingress': releaseDetail }));
    mountApp('/c/prod/helm/default/ingress?namespace=argocd&namespace=ingress');

    const back = await waitFor(() => pageBack());
    expect(back.getAttribute('href')).toBe('/c/prod/helm?namespace=argocd&namespace=ingress');
  });

  it('sorts by a column heading: ascending, descending, then the order it came in', async () => {
    const three: StubReply = {
      body: {
        state: 'ok',
        items: [
          { name: 'web', namespace: 'apps', revision: 3, status: 'deployed', state: 'ok' },
          { name: 'api', namespace: 'apps', revision: 12, status: 'deployed', state: 'ok' },
          { name: 'db', namespace: 'data', revision: 1, status: 'deployed', state: 'ok' },
        ],
      },
    };
    stubFetch(shell({ '/api/c/prod/helm': three }));
    mountApp('/c/prod/helm');

    const order = (): string[] =>
      [...page().querySelectorAll('tbody [data-release]')].map((each) => each.textContent ?? '');
    await waitFor(() => {
      expect(order()).toEqual(['web', 'api', 'db']);
    });
    const sortBy = (label: string): void => {
      const heading = [...page().querySelectorAll('thead th button')].find(
        (each) => each.querySelector('[data-label]')?.textContent === label,
      );
      if (heading === undefined) throw new Error(`no sort control for ${label}`);
      fireEvent.click(heading);
    };

    sortBy('Name');
    expect(order()).toEqual(['api', 'db', 'web']);
    sortBy('Name');
    expect(order()).toEqual(['web', 'db', 'api']);
    sortBy('Name');
    expect(order()).toEqual(['web', 'api', 'db']);
    // A number sorts as a number: 12 after 3.
    sortBy('Revision');
    expect(order()).toEqual(['db', 'web', 'api']);
  });

  it('leaves the Updated column out of the search, which draws an age and not the instant', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases }));
    mountApp('/c/prod/helm?search=2026-09-12');

    await waitFor(() => {
      expect(page().textContent).toContain('0 of 2 listed');
    });
    expect(page().querySelector('[data-empty="filtered"]')).not.toBeNull();
  });

  it('remembers the namespace chosen here for the next list', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases }));
    mountApp('/c/prod/helm?namespace=kube-system&namespace=default');

    await waitFor(() => {
      expect(chosenNamespaces('prod')).toEqual(['default', 'kube-system']);
    });
  });

  /*
    The picker's own write, which the address-driven case above does not reach.
    A namespace picked is remembered from either side -
    the picker writes it, and the address it writes names it - but "All
    namespaces" is recorded by the picker alone: an address that names none
    cannot be told from a bare link, and changes nothing.
  */
  it('remembers a namespace picked here, and forgets it on "All namespaces"', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases, '/api/c/prod/namespaces': namespaceList }));
    const { router } = mountApp('/c/prod/helm');
    await waitFor(() => {
      expect(page().textContent).toContain('2 listed');
    });

    await pick('kube-system');
    await waitFor(() => {
      expect(router.state.location.search).toBe('?namespace=kube-system');
    });
    expect(chosenNamespaces('prod')).toEqual(['kube-system']);

    await pick('All namespaces');
    await waitFor(() => {
      expect(router.state.location.search).toBe('');
    });
    expect(chosenNamespaces('prod')).toEqual([]);
  });

  it('is opened from the menu at the namespace chosen on another list', async () => {
    // Chosen on Pods, earlier in the session. Helm's own address names none,
    // which leaves the choice as it was.
    rememberNamespaces('prod', 'pods', ['default']);
    stubFetch(
      shell({
        '/api/c/prod/nav': {
          body: {
            state: 'ok',
            data: {
              groups: [
                {
                  key: 'cluster',
                  label: 'Cluster',
                  items: [
                    { text: 'Overview', href: '/c/prod' },
                    { text: 'Helm releases', href: '/c/prod/helm', namespaced: true },
                  ],
                },
              ],
            },
          },
        },
        '/api/c/prod/helm': releases,
      }),
    );
    mountApp('/c/prod/helm');

    await waitFor(() => {
      const link = document.querySelector<HTMLAnchorElement>('#menu a[data-kind="helm"]');
      expect(link?.getAttribute('href')).toBe('/c/prod/helm?namespace=default');
    });
  });

  it('draws one heading level under the next, in the drawer and on the page', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases, '/api/c/prod/helm/default/ingress': releaseDetail }));
    const { router } = mountApp('/c/prod/helm');

    const panel = await openInDrawer('default/ingress');
    await waitFor(() => {
      expect(panel.textContent).toContain('Revisions');
    });
    // The drawer sits beside the list's `h1`: the release is an `h2`, its
    // sections `h3`.
    expect([...panel.querySelectorAll('h2')].map((each) => each.textContent)).toEqual(['ingress']);
    expect([...panel.querySelectorAll('h3')].map((each) => each.textContent)).toEqual([
      'Revisions',
      'Notes',
      'Values',
    ]);

    await act(async () => {
      await router.navigate('/c/prod/helm/default/ingress');
    });
    await waitFor(() => {
      expect(page().querySelector('[data-object-chrome="page"] h1')?.textContent).toBe('ingress');
    });
    await waitFor(() => {
      expect([...page().querySelectorAll('h2')].map((each) => each.textContent)).toEqual([
        'Revisions',
        'Notes',
        'Values',
      ]);
    });
  });

  it('closes the drawer with its close control', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases, '/api/c/prod/helm/default/ingress': releaseDetail }));
    mountApp('/c/prod/helm');

    const panel = await openInDrawer('default/ingress');
    fireEvent.click(panel.querySelector('button[aria-label="Close"]') as Element);
    await waitFor(() => {
      expect(drawer().querySelector('[data-object-chrome="drawer"]')).toBeNull();
    });
  });

  it('reveals the values from the drawer, recorded against the revision', async () => {
    const asked: string[] = [];
    stubFetch(
      shell({
        '/api/c/prod/helm': releases,
        '/api/c/prod/helm/default/ingress': releaseDetail,
        '/api/c/prod/helm/default/ingress/values': {
          body: { state: 'ok', data: { revision: 41, values: 'controller:\n  replicaCount: 2\n' } },
        },
        '/api/c/prod/helm/default/ingress/notes': {
          body: { state: 'ok', data: { revision: 41, notes: 'The admin password is s3cr3t-admin-pw' } },
        },
      }),
    );
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      asked.push(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url);
      return stubbed(input, init);
    });
    mountApp('/c/prod/helm');

    const panel = await openInDrawer('default/ingress');
    const values = await waitFor(() => {
      const found = panel.querySelector('[data-reveal-values]');
      if (found === null) throw new Error('no reveal control');
      return found;
    });
    expect(asked.filter((each) => each.includes('/values'))).toHaveLength(0);
    fireEvent.click(values);
    await waitFor(() => {
      expect(panel.textContent).toContain('replicaCount: 2');
    });
    fireEvent.click(panel.querySelector('[data-reveal-notes]') as Element);
    await waitFor(() => {
      expect(panel.textContent).toContain('s3cr3t-admin-pw');
    });
    expect(asked.filter((each) => each.includes('/values') || each.includes('/notes'))).toEqual([
      '/api/c/prod/helm/default/ingress/values?revision=41',
      '/api/c/prod/helm/default/ingress/notes?revision=41',
    ]);
  });
});

describe('the four absences reach the page as four sentences', () => {
  const arms = [
    {
      what: 'unreachable',
      reply: {
        state: 'unreachable',
        reason: {
          code: 'cluster_unreachable',
          message: 'prod could not be asked: i/o timeout',
          retryable: true,
        },
      },
      says: 'could not be asked',
    },
    {
      what: 'unreadable',
      reply: {
        state: 'unreadable',
        reason: { code: 'read_denied', message: 'This console may not list Secrets on prod' },
      },
      says: 'may not list Secrets',
    },
    {
      what: 'undecodable',
      reply: {
        state: 'unreadable',
        reason: {
          code: 'undecodable',
          message: '3 release Secrets were found and not one of them could be read.',
        },
      },
      says: 'not one of them could be read',
    },
    {
      what: 'empty',
      reply: { state: 'empty', items: [] },
      says: 'no Helm release',
    },
  ];

  for (const arm of arms) {
    it(`says its own sentence for ${arm.what}, and draws no table`, async () => {
      stubFetch(shell({ '/api/c/prod/helm': { body: arm.reply } }));
      mountApp('/c/prod/helm');

      await waitFor(() => {
        expect((page().textContent ?? '').toLowerCase()).toContain(arm.says.toLowerCase());
      });

      // **What must never be drawn on an absence is the releases table.** A
      // cluster that could not be read, drawn as a cluster with nothing
      // installed, would be a failed answer shown as an empty one; an empty table
      // here would say this cluster installs nothing with Helm - which is the
      // most reassuring possible way to be wrong.
      expect(page().querySelector('table[aria-label="Helm releases"]')).toBeNull();
    });
  }

  it('names the selection in the empty sentence and adds nothing', async () => {
    stubFetch(shell({ '/api/c/prod/helm': { body: { state: 'empty', items: [] } } }));
    mountApp('/c/prod/helm?namespace=argocd,ingress');

    await waitFor(() => {
      expect(page().textContent).toContain('No Helm release in argocd, ingress');
    });
    // It names the selection and adds no sentence about what the answer covers.
    expect(page().textContent).not.toContain('not about the cluster');
  });
});

describe('the namespace selection is repeatable', () => {
  it('sends one parameter per namespace, not a joined string', async () => {
    const asked: string[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      asked.push(url);
      const path = url.split('?')[0] ?? url;
      const bodies: Record<string, unknown> = {
        '/api/session': session.body,
        '/api/clusters': clusters.body,
        '/api/c/prod/nav': { state: 'ok', data: { groups: [] } },
        '/api/c/prod/extensions': { state: 'empty', items: [] },
        '/api/c/prod/helm': releases.body,
      };
      const body = bodies[path];
      if (body === undefined) {
        return Promise.resolve(new Response(null, { status: 404 }));
      }
      return Promise.resolve(
        new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } }),
      );
    });

    mountApp('/c/prod/helm?namespace=argocd,ingress');

    await waitFor(() => {
      expect(asked.some((url) => url.startsWith('/api/c/prod/helm'))).toBe(true);
    });

    // `?namespace=argocd&namespace=ingress`, and never `?namespace=argocd,ingress`:
    // the server reads a repeated parameter, and a joined string would arrive as
    // one namespace named "argocd,ingress" that does not exist.
    const helm = asked.find((url) => url.startsWith('/api/c/prod/helm')) ?? '';
    expect(helm).toContain('namespace=argocd');
    expect(helm).toContain('namespace=ingress');
    expect(helm).not.toContain('argocd%2Cingress');
  });
});

describe('a release has a page of its own', () => {
  it('draws the history, which is what the page is for', async () => {
    stubFetch(
      shell({
        '/api/c/prod/helm': releases,
        '/api/c/prod/helm/default/ingress': releaseDetail,
      }),
    );
    mountApp('/c/prod/helm/default/ingress');

    await waitFor(() => {
      expect(page().textContent).toContain('Revisions');
    });
    const text = page().textContent ?? '';

    // Every revision Helm kept, superseded ones included, newest first.
    expect(page().querySelector('[data-revision="41"]')).not.toBeNull();
    expect(page().querySelector('[data-revision="40"]')).not.toBeNull();

    // Each revision's own chart version, which is how an upgrade is read off
    // this table.
    expect(text).toContain('4.11.1');

    // How far back it is possible to look, rather than a reader inferring it
    // from a list that stops.
    expect(text).toContain('2 kept');
  });

  it('marks the current revision and the running one apart', async () => {
    stubFetch(
      shell({
        '/api/c/prod/helm': releases,
        '/api/c/prod/helm/default/ingress': releaseDetail,
      }),
    );
    mountApp('/c/prod/helm/default/ingress');

    await waitFor(() => {
      expect(page().querySelector('[data-mark="current"]')).not.toBeNull();
    });

    // Two facts that usually coincide and diverge exactly when it matters. The
    // running one has to be findable at a glance in a list of ten rows all
    // saying "Upgrade complete".
    expect(page().querySelector('[data-mark="current"]')?.textContent).toContain('41');
    expect(page().querySelector('[data-mark="deployed"]')?.textContent).toContain('running');
  });

  it('keeps a revision that would not decode, and says why its versions are blank', async () => {
    stubFetch(
      shell({
        '/api/c/prod/helm': releases,
        '/api/c/prod/helm/default/ingress': releaseDetail,
      }),
    );
    mountApp('/c/prod/helm/default/ingress');

    await waitFor(() => {
      expect(page().textContent).toContain('would not decode');
    });
    // A row renumbered around the gap would be a history nobody could check
    // against `helm history`.
    expect(page().querySelector('[data-revision="41"]')).not.toBeNull();
  });

  it('puts the running-revision line under the status it contradicts', async () => {
    stubFetch(
      shell({
        '/api/c/prod/helm': releases,
        '/api/c/prod/helm/default/ingress': releaseDetail,
      }),
    );
    mountApp('/c/prod/helm/default/ingress');

    await waitFor(() => {
      expect(page().querySelector('[data-fact="status"]')).not.toBeNull();
    });
    // A status of `failed` with the correction three rows away is a status of
    // `failed`.
    const status = page().querySelector('[data-fact="status"]');
    expect(status?.textContent).toContain('Running: revision 40');
  });

  it('keeps the namespace selection on the way back', async () => {
    stubFetch(
      shell({
        '/api/c/prod/helm': releases,
        '/api/c/prod/helm/default/ingress': releaseDetail,
      }),
    );
    mountApp('/c/prod/helm/default/ingress?namespace=argocd,ingress');

    // Returning a user to forty rows after they narrowed to one namespace
    // is the same defect as forgetting a filter.
    const back = await waitFor(() => pageBack());
    // In the list's own spelling, the repeated parameter.
    expect(back?.getAttribute('href')).toBe('/c/prod/helm?namespace=argocd&namespace=ingress');
  });
});

describe('the notes and the values panels', () => {
  // These pin the client half: three answers, and the fold says which one it is
  // showing.

  it('draws what the server sent', async () => {
    stubFetch(
      shell({
        '/api/c/prod/helm': releases,
        '/api/c/prod/helm/default/ingress': releaseDetail,
      }),
    );
    mountApp('/c/prod/helm/default/ingress');

    await waitFor(() => {
      expect(page().textContent).toContain('Notes');
    });
    const text = page().textContent ?? '';
    // Rendered, and not here: the notes arrive only through their reveal
    // and the values through theirs.
    expect(text).not.toContain('The ingress controller is now running.');
    expect(text).not.toContain('replicaCount');
    // Present and not shown: the reveal is offered, against the revision.
    expect(page().querySelector('[data-reveal-notes]')).not.toBeNull();
    expect(page().querySelector('[data-reveal-values]')).not.toBeNull();
    expect(text).toContain('revision 41');
  });

  it('says a chart ships no notes rather than drawing nothing', async () => {
    const detail = structuredClone(releaseDetail) as { body: { data: Record<string, unknown> } };
    delete detail.body.data.notesRendered;
    delete detail.body.data.valuesSupplied;
    stubFetch(
      shell({ '/api/c/prod/helm': releases, '/api/c/prod/helm/default/ingress': detail }),
    );
    mountApp('/c/prod/helm/default/ingress');

    await waitFor(() => {
      expect(page().textContent).toContain('Revisions');
    });
    const text = page().textContent ?? '';

    // The fold is **there**, and it says which answer it is. Hiding it would make
    // a chart with notes and one without look the same.
    expect(text).toContain('Notes');
    expect(text).toContain('Values');
    expect(text).toContain('No notes rendered');
    expect(text).toContain('No values supplied');
  });

  it('does not say "none" over a revision nothing could read', async () => {
    const detail = structuredClone(releaseDetail) as { body: { data: Record<string, unknown> } };
    delete detail.body.data.notesRendered;
    delete detail.body.data.valuesSupplied;
    detail.body.data.payloadNote =
      'Revision 41 is the current one and its stored document would not decode, so its ' +
      'notes and its values could not be read.';
    stubFetch(
      shell({ '/api/c/prod/helm': releases, '/api/c/prod/helm/default/ingress': detail }),
    );
    mountApp('/c/prod/helm/default/ingress');

    await waitFor(() => {
      expect(page().textContent).toContain('would not decode');
    });
    const text = page().textContent ?? '';

    // "There are none" is a claim about the release, and nothing here
    // established it: a failed answer is not an empty one. Saying it over an
    // unread revision would tell a user their release has no overrides.
    expect(text).not.toContain('No values supplied');
    expect(text).not.toContain('No notes rendered');
    // And no reveal is offered over it: there is nothing it could answer.
    expect(page().querySelector('[data-reveal-notes]')).toBeNull();
    expect(page().querySelector('[data-reveal-values]')).toBeNull();
  });
});

/**
 * The notes behind the same reveal. Charts print a generated admin password or
 * a token in their `NOTES.txt`, so the page does not receive them on every
 * view. The server half is
 * `internal/web`'s helmreveal_test.go; this is the page's half.
 */
describe('the notes, which arrive only when asked for', () => {
  const notes: StubReply = {
    body: { state: 'ok', data: { revision: 41, notes: 'The admin password is s3cr3t-admin-pw' } },
  };

  it('asks for them on a press, with the revision the page was drawn from, and hides them again', async () => {
    const asked: string[] = [];
    stubFetch(
      shell({
        '/api/c/prod/helm': releases,
        '/api/c/prod/helm/default/ingress': releaseDetail,
        '/api/c/prod/helm/default/ingress/notes': notes,
      }),
    );
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      asked.push(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url);
      return stubbed(input, init);
    });
    mountApp('/c/prod/helm/default/ingress');

    const button = await waitFor(() => {
      const found = page().querySelector('[data-reveal-notes]');
      if (found === null) throw new Error('no reveal control');
      return found;
    });
    // Nothing asked before the press: a reveal is a record on the server.
    expect(asked.filter((each) => each.includes('/notes'))).toHaveLength(0);
    expect(page().textContent).not.toContain('s3cr3t-admin-pw');

    fireEvent.click(button);
    await waitFor(() => {
      expect(page().textContent).toContain('The admin password is s3cr3t-admin-pw');
    });
    expect(asked.filter((each) => each.includes('/notes'))).toEqual([
      '/api/c/prod/helm/default/ingress/notes?revision=41',
    ]);
    // One reveal is one part: the values were not asked for alongside.
    expect(asked.filter((each) => each.includes('/values'))).toHaveLength(0);

    fireEvent.click(page().querySelector('[data-hide-notes]') as Element);
    expect(page().textContent).not.toContain('s3cr3t-admin-pw');
    expect(page().querySelector('[data-reveal-notes]')).not.toBeNull();
  });

  it("says a refusal in the server's words rather than drawing an empty box", async () => {
    stubFetch(
      shell({
        '/api/c/prod/helm': releases,
        '/api/c/prod/helm/default/ingress': releaseDetail,
        '/api/c/prod/helm/default/ingress/notes': {
          body: {
            state: 'forbidden',
            reason: { code: 'verb_denied', message: 'secrets "sh.helm.release.v1.ingress.v41" is forbidden for alice.' },
          },
        },
      }),
    );
    mountApp('/c/prod/helm/default/ingress');

    const button = await waitFor(() => {
      const found = page().querySelector('[data-reveal-notes]');
      if (found === null) throw new Error('no reveal control');
      return found;
    });
    fireEvent.click(button);
    await waitFor(() => {
      expect(page().textContent).toContain('is forbidden for alice');
    });
    expect(page().textContent).not.toContain('No notes rendered');
  });

  it('says a chart that turned out to ship none apart from a refusal', async () => {
    stubFetch(
      shell({
        '/api/c/prod/helm': releases,
        '/api/c/prod/helm/default/ingress': releaseDetail,
        '/api/c/prod/helm/default/ingress/notes': { body: { state: 'ok', data: { revision: 41, notes: '' } } },
      }),
    );
    mountApp('/c/prod/helm/default/ingress');

    const button = await waitFor(() => {
      const found = page().querySelector('[data-reveal-notes]');
      if (found === null) throw new Error('no reveal control');
      return found;
    });
    fireEvent.click(button);
    await waitFor(() => {
      expect(page().textContent).toContain('Revision 41 carries no notes');
    });
    expect(page().querySelector('[role="alert"]')).toBeNull();
  });

  it('offers no reveal where the chart ships none', async () => {
    const detail = structuredClone(releaseDetail) as { body: { data: Record<string, unknown> } };
    delete detail.body.data.notesRendered;
    stubFetch(shell({ '/api/c/prod/helm': releases, '/api/c/prod/helm/default/ingress': detail }));
    mountApp('/c/prod/helm/default/ingress');

    await waitFor(() => {
      expect(page().textContent).toContain('No notes rendered');
    });
    expect(page().querySelector('[data-reveal-notes]')).toBeNull();
    // The values' reveal is still offered: the two are answered apart.
    expect(page().querySelector('[data-reveal-values]')).not.toBeNull();
  });
});

/**
 * The values behind the reveal. They routinely carry passwords, so the page
 * does not receive them on every view. The server half -
 * the default answer carries none, the reveal is reviewed and recorded - is
 * `internal/web`'s helmreveal_test.go; this is the page's half.
 */
describe('the values, which arrive only when asked for', () => {
  const values: StubReply = {
    body: { state: 'ok', data: { revision: 41, values: 'controller:\n  replicaCount: 2\n' } },
  };

  it('asks for them on a press, with the revision the page was drawn from, and hides them again', async () => {
    const asked: string[] = [];
    stubFetch(
      shell({
        '/api/c/prod/helm': releases,
        '/api/c/prod/helm/default/ingress': releaseDetail,
        '/api/c/prod/helm/default/ingress/values': values,
      }),
    );
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      asked.push(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url);
      return stubbed(input, init);
    });
    mountApp('/c/prod/helm/default/ingress');

    const button = await waitFor(() => {
      const found = page().querySelector('[data-reveal-values]');
      if (found === null) throw new Error('no reveal control');
      return found;
    });
    // Nothing asked before the press: a reveal is a record on the server.
    expect(asked.filter((each) => each.includes('/values'))).toHaveLength(0);
    expect(page().textContent).not.toContain('replicaCount');

    fireEvent.click(button);
    await waitFor(() => {
      expect(page().textContent).toContain('replicaCount: 2');
    });
    expect(asked.filter((each) => each.includes('/values'))).toEqual([
      '/api/c/prod/helm/default/ingress/values?revision=41',
    ]);

    fireEvent.click(page().querySelector('[data-hide-values]') as Element);
    expect(page().textContent).not.toContain('replicaCount');
    expect(page().querySelector('[data-reveal-values]')).not.toBeNull();
  });

  it('says a refusal in the server\'s words rather than drawing an empty box', async () => {
    stubFetch(
      shell({
        '/api/c/prod/helm': releases,
        '/api/c/prod/helm/default/ingress': releaseDetail,
        '/api/c/prod/helm/default/ingress/values': {
          body: {
            state: 'forbidden',
            reason: { code: 'verb_denied', message: 'secrets "sh.helm.release.v1.ingress.v41" is forbidden for alice.' },
          },
        },
      }),
    );
    mountApp('/c/prod/helm/default/ingress');

    const button = await waitFor(() => {
      const found = page().querySelector('[data-reveal-values]');
      if (found === null) throw new Error('no reveal control');
      return found;
    });
    fireEvent.click(button);
    await waitFor(() => {
      expect(page().textContent).toContain('is forbidden for alice');
    });
    expect(page().textContent).not.toContain('No values supplied');
  });

  it('drops what was revealed on a move to another release at the same revision', async () => {
    const other = structuredClone(releaseDetail) as { body: { data: { release: Record<string, unknown> } } };
    other.body.data.release.name = 'other';
    stubFetch(
      shell({
        '/api/c/prod/helm': releases,
        '/api/c/prod/helm/default/ingress': releaseDetail,
        '/api/c/prod/helm/default/other': other as StubReply,
        '/api/c/prod/helm/default/ingress/values': values,
      }),
    );
    // `other` first, so its answer is cached and the move back draws it at once
    // with no loading state in between - the case that kept the component.
    const { router } = mountApp('/c/prod/helm/default/other');
    await waitFor(() => {
      expect(page().querySelector('h1')?.textContent).toBe('other');
    });
    await act(async () => {
      await router.navigate('/c/prod/helm/default/ingress');
    });
    const button = await waitFor(() => {
      if (page().querySelector('h1')?.textContent !== 'ingress') throw new Error('not on ingress yet');
      const found = page().querySelector('[data-reveal-values]');
      if (found === null) throw new Error('no reveal control');
      return found;
    });
    fireEvent.click(button);
    await waitFor(() => {
      expect(page().textContent).toContain('replicaCount: 2');
    });

    await act(async () => {
      await router.navigate('/c/prod/helm/default/other');
    });
    await waitFor(() => {
      expect(page().querySelector('h1')?.textContent).toBe('other');
    });
    // Revision 41 on both: were it keyed by the revision alone, ingress's
    // values would stay on screen under the other release's facts.
    expect(page().textContent).not.toContain('replicaCount');
    expect(page().querySelector('[data-reveal-values]')).not.toBeNull();
  });

  it('offers no reveal where none were supplied', async () => {
    const detail = structuredClone(releaseDetail) as { body: { data: Record<string, unknown> } };
    delete detail.body.data.valuesSupplied;
    stubFetch(shell({ '/api/c/prod/helm': releases, '/api/c/prod/helm/default/ingress': detail }));
    mountApp('/c/prod/helm/default/ingress');

    await waitFor(() => {
      expect(page().textContent).toContain('No values supplied');
    });
    expect(page().querySelector('[data-reveal-values]')).toBeNull();
  });
});

/**
 * Descriptive sentences that are not drawn: the read-only paragraph ending
 * "helm already pointed at it" first among them. Data, titles, short empty states and real
 * error messages stay; these may not come back on the list, the drawer or the
 * page.
 */
const REMOVED = [
  'This section is read-only',
  'already pointed at it',
  'helm rollback',
  'does not roll back, upgrade or uninstall',
  'Every namespace this console may read',
  'Narrowed to',
  'an answer about its',
  'shown only when asked for',
  'about the chart and not about this release',
  'own defaults apply in full',
  'turned out to carry',
];

function expectNoneOf(text: string): void {
  for (const sentence of REMOVED) expect(text).not.toContain(sentence);
}

describe('no descriptive sentence, and nothing writes', () => {
  it('draws none on the list', async () => {
    stubFetch(shell({ '/api/c/prod/helm': releases }));
    mountApp('/c/prod/helm?namespace=default');

    await waitFor(() => {
      expect(page().textContent).toContain('ingress-nginx');
    });
    expectNoneOf(page().textContent ?? '');
  });

  it('draws none on a release page', async () => {
    const detail = structuredClone(releaseDetail) as { body: { data: Record<string, unknown> } };
    detail.body.data.revisions = [];
    stubFetch(
      shell({
        '/api/c/prod/helm': releases,
        '/api/c/prod/helm/default/ingress': detail,
      }),
    );
    mountApp('/c/prod/helm/default/ingress');

    await waitFor(() => {
      expect(page().textContent).toContain('No revision kept');
    });
    expectNoneOf(page().textContent ?? '');
  });

  it('draws none in the drawer, with none of the notes or values supplied', async () => {
    const detail = structuredClone(releaseDetail) as { body: { data: Record<string, unknown> } };
    delete detail.body.data.notesRendered;
    delete detail.body.data.valuesSupplied;
    stubFetch(shell({ '/api/c/prod/helm': releases, '/api/c/prod/helm/default/ingress': detail }));
    mountApp('/c/prod/helm');

    const box = await openInDrawer('default/ingress');
    await waitFor(() => {
      expect(box.textContent).toContain('No values supplied');
    });
    expect(box.textContent).toContain('No notes rendered');
    expectNoneOf(box.textContent ?? '');
  });

  it('offers no control that changes anything on a release page', async () => {
    stubFetch(
      shell({
        '/api/c/prod/helm': releases,
        '/api/c/prod/helm/default/ingress': releaseDetail,
      }),
    );
    mountApp('/c/prod/helm/default/ingress');

    await waitFor(() => {
      expect(page().textContent).toContain('Revisions');
    });

    // This page has no form, and its two buttons are the reveals of the notes
    // and the values - GETs, recorded, that change nothing on the cluster. The list page has one form, and it
    // narrows a read.
    expect(page().querySelectorAll('form')).toHaveLength(0);
    const buttons = [...page().querySelectorAll('button')];
    expect(
      buttons.map((each) =>
        each.hasAttribute('data-reveal-notes') ? 'notes' : each.hasAttribute('data-reveal-values') ? 'values' : 'other',
      ),
    ).toEqual(['notes', 'values']);
  });
});
