import { cleanup, fireEvent, type RenderResult, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DetailPage } from '../../api';
import { renderWithClient, stubFetch } from '../../testing/harness';
import type { ObjectIdentity } from './address';
import { ObjectDetail } from './ObjectDetail';

/**
 * A Role's rules on the page that opens by default.
 *
 * Asserted **on the rendered rows** and not on the payload: `internal/detail`
 * has the object in hand, so a test that stopped at the JSON would pass
 * against a panel nobody drew.
 */

// `cleanup()` and the storage teardown are the shared hook's
// (`ui/src/testing/setup.ts`); a wipe here would be a no-op beside it.
afterEach(() => {
  vi.unstubAllGlobals();
});

const identity: ObjectIdentity = {
  cluster: 'prod',
  kind: 'roles',
  namespace: 'shop',
  name: 'reader',
};

const PATH = '/api/c/prod/r/roles/shop/reader';

function renderPanel(ui: ReactNode): RenderResult {
  const router = createMemoryRouter(
    [
      { path: '/c/:cluster/:kind', element: ui },
      { path: '/health', element: <p>the health page</p> },
    ],
    { initialEntries: ['/c/prod/roles'] },
  );
  return renderWithClient(<RouterProvider router={router} />);
}

function pageWith(extra: Partial<DetailPage>): DetailPage {
  return {
    kind: 'roles',
    namespace: 'shop',
    name: 'reader',
    title: 'Role',
    tabs: [],
    sections: [
      {
        key: 'summary',
        label: 'Summary',
        state: 'ok',
        fields: [{ label: 'Kind', value: 'Role', prose: false, pill: false }],
      },
    ],
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

function panel(view: RenderResult): HTMLElement {
  const found = view.container.querySelector<HTMLElement>('[data-section="rules"]');
  if (found === null) throw new Error('there is no rules panel');
  return found;
}

function lines(view: RenderResult): HTMLElement[] {
  return [...panel(view).querySelectorAll<HTMLElement>('[data-rule]')];
}

function pills(line: HTMLElement | undefined, group: string): string[] {
  const holder = line?.querySelector(`[data-group="${group}"]`);
  return [...(holder?.querySelectorAll('[data-pill]') ?? [])].map((pill) => pill.textContent ?? '');
}

function tones(line: HTMLElement | undefined, group: string): string[] {
  const holder = line?.querySelector(`[data-group="${group}"]`);
  return [...(holder?.querySelectorAll('[data-pill]') ?? [])].map((pill) => pill.getAttribute('data-tone') ?? '');
}

describe('a Role’s rules, on the page that opens by default', () => {
  it('draws every rule without the Manifest tab, the core group as core', async () => {
    const view = await drawn(
      pageWith({
        rules: {
          state: 'ok',
          rules: [
            { apiGroups: [''], resources: ['pods', 'configmaps'], verbs: ['get', 'list', 'watch'] },
            { apiGroups: ['apps'], resources: ['deployments'], verbs: ['get'] },
          ],
        },
      }),
    );

    const found = lines(view);
    expect(found.length).toBe(2);
    expect(pills(found[0], 'groups')).toEqual(['core']);
    expect(pills(found[0], 'resources')).toEqual(['pods', 'configmaps']);
    expect(pills(found[0], 'verbs')).toEqual(['get', 'list', 'watch']);
    expect(pills(found[1], 'groups')).toEqual(['apps']);
    expect(panel(view).querySelector('table')).toBeNull();
  });

  it('colors verbs by risk', async () => {
    const view = await drawn(
      pageWith({
        rules: {
          state: 'ok',
          rules: [{ apiGroups: [''], resources: ['pods'], verbs: ['get', 'create', 'delete', 'bind', '*'] }],
        },
      }),
    );

    expect(tones(lines(view)[0], 'verbs')).toEqual(['normal', 'warn', 'warn', 'bad', 'bad']);
  });

  it('shows a rule’s resourceNames, which are the whole of what it grants', async () => {
    const view = await drawn(
      pageWith({
        rules: {
          state: 'ok',
          rules: [
            {
              apiGroups: [''],
              resources: ['secrets'],
              resourceNames: ['db-credentials'],
              verbs: ['get'],
            },
          ],
        },
      }),
    );

    expect(pills(lines(view)[0], 'names')).toEqual(['db-credentials']);
  });

  it('draws a non-resource rule by its URLs and never as an empty rule', async () => {
    const view = await drawn(
      pageWith({
        rules: {
          state: 'ok',
          rules: [{ nonResourceURLs: ['/healthz', '/metrics'], verbs: ['get'] }],
        },
      }),
    );

    expect(pills(lines(view)[0], 'urls')).toEqual(['/healthz', '/metrics']);
    expect(pills(lines(view)[0], 'verbs')).toEqual(['get']);
  });

  it('folds past eight pills behind a +N button that expands its own group', async () => {
    const many = Array.from({ length: 11 }, (_, at) => `res${at}`);
    const view = await drawn(
      pageWith({
        rules: { state: 'ok', rules: [{ apiGroups: [''], resources: many, verbs: ['get'] }] },
      }),
    );

    const line = lines(view)[0];
    expect(pills(line, 'resources').length).toBe(8);
    const more = line?.querySelector<HTMLButtonElement>('[data-group="resources"] button');
    expect(more?.textContent).toBe('+3');
    expect(more?.getAttribute('aria-expanded')).toBe('false');
    // The verbs group has one pill and no button.
    expect(line?.querySelector('[data-group="verbs"] button')).toBeNull();

    fireEvent.click(more as HTMLButtonElement);

    expect(pills(line, 'resources').length).toBe(11);
    expect(line?.querySelector('[data-group="resources"] button')?.getAttribute('aria-expanded')).toBe('true');
  });

  it('puts rules that differ only in their resources on one line', async () => {
    const view = await drawn(
      pageWith({
        rules: {
          state: 'ok',
          rules: [
            { apiGroups: [''], resources: ['pods'], verbs: ['get'] },
            { apiGroups: [''], resources: ['services'], verbs: ['get'] },
            { apiGroups: [''], resources: ['secrets'], verbs: ['get'], resourceNames: ['x'] },
          ],
        },
      }),
    );

    const found = lines(view);
    expect(found.length).toBe(2);
    expect(pills(found[0], 'resources')).toEqual(['pods', 'services']);
  });

  it('summarizes the rules: a count, a red * and a red escalation verb', async () => {
    const view = await drawn(
      pageWith({
        rules: {
          state: 'ok',
          rules: [
            { apiGroups: ['*'], resources: ['*'], verbs: ['*'], wildcards: 'verbs', wildcardTone: 'bad' },
            { apiGroups: ['rbac.authorization.k8s.io'], resources: ['roles'], verbs: ['escalate', 'bind'] },
          ],
        },
      }),
    );

    const strip = [...panel(view).querySelectorAll('[data-rules-summary] [data-pill]')];
    expect(strip.map((pill) => pill.textContent)).toEqual(['2', '*', 'escalate', 'bind']);
    expect(strip.map((pill) => pill.getAttribute('data-tone'))).toEqual(['normal', 'bad', 'bad', 'bad']);
  });

  it('shows no red pill in the summary where nothing is risky', async () => {
    const view = await drawn(
      pageWith({
        rules: { state: 'ok', rules: [{ apiGroups: [''], resources: ['pods'], verbs: ['get', 'create'] }] },
      }),
    );

    const strip = [...panel(view).querySelectorAll('[data-rules-summary] [data-pill]')];
    expect(strip.map((pill) => pill.textContent)).toEqual(['1']);
  });

  it('draws an aggregated ClusterRole’s selectors', async () => {
    const view = await drawn(
      pageWith({
        rules: {
          state: 'ok',
          rules: [{ apiGroups: [''], resources: ['pods'], verbs: ['get'] }],
          selectors: [['rbac.example.com/aggregate-to-view=true'], ['tier in (a,b)', '!hidden']],
          note: 'This ClusterRole is aggregated.',
        },
      }),
    );

    const selectors = [...panel(view).querySelectorAll('[data-selector]')];
    expect(selectors.length).toBe(2);
    expect([...selectors[1]!.querySelectorAll('[data-pill]')].map((pill) => pill.textContent)).toEqual([
      'tier in (a,b)',
      '!hidden',
    ]);
    expect(panel(view).querySelector('[data-rules-selectors]')?.getAttribute('title')).toContain('aggregated');
  });

  it('still draws the selectors of an aggregated role whose rules are empty', async () => {
    const view = await drawn(
      pageWith({ rules: { state: 'empty', selectors: [['a=b']] } }),
    );

    expect(panel(view).querySelectorAll('[data-selector]').length).toBe(1);
    expect(panel(view).textContent).toContain('grants nothing');
  });

  it('says a Role grants nothing rather than drawing no panel', async () => {
    const view = await drawn(pageWith({ rules: { state: 'empty' } }));

    expect(panel(view).textContent).toContain('grants nothing');
    expect(panel(view).querySelector('[data-rule]')).toBeNull();
  });

  it('draws a refusal, distinguishably, where the rules could not be read', async () => {
    // An empty answer and a failed answer, and on this surface it is the point rather than
    // a principle: somebody opens this panel to conclude *that is all this Role
    // can do*, so a fault drawn as "grants nothing" is the worst answer there is.
    const refused = await drawn(
      pageWith({
        rules: {
          state: 'unreadable',
          reason: { code: 'undecodable', message: 'This object’s rules could not be read.' },
        },
      }),
    );
    const refusedText = panel(refused).textContent ?? '';
    cleanup();
    vi.unstubAllGlobals();

    const empty = await drawn(pageWith({ rules: { state: 'empty' } }));
    const emptyText = panel(empty).textContent ?? '';

    // Asserted on the **word each state is drawn under**, not merely on the two
    // strings differing: comparing them would pass against a panel that drew
    // the refusal under the heading `Nothing here` with the reason below it —
    // which is the collapse itself, one line lower.
    expect(refusedText).toContain('Could not be read');
    expect(refusedText).not.toContain('Nothing here');
    expect(emptyText).toContain('Nothing here');
    expect(emptyText).not.toContain('Could not be read');
    expect(refusedText).not.toBe(emptyText);
    expect(refusedText).not.toContain('grants nothing');
  });

  it('draws no panel at all on a kind that has no rules', async () => {
    const view = await drawn(pageWith({}));
    expect(view.container.querySelector('[data-section="rules"]')).toBeNull();
  });

  it('is enough on its own to keep the panel from saying there is nothing here', async () => {
    // A Role has no containers, no ports, no addresses and usually no relations.
    // Without this member in `Body`'s emptiness check, the object whose whole
    // content is its rules would draw "Nothing here" over them.
    const view = await drawn({
      kind: 'roles',
      namespace: 'shop',
      name: 'reader',
      title: 'Role',
      tabs: [],
      sections: [],
      rules: { state: 'ok', rules: [{ apiGroups: [''], resources: ['pods'], verbs: ['get'] }] },
    });

    expect(view.container.querySelector('[data-section="rules"]')).not.toBeNull();
    expect(view.container.textContent ?? '').not.toContain('Nothing here');
  });
});
