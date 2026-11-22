import { QueryClientProvider } from '@tanstack/react-query';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { useEffect, type ReactNode } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { CreateAffordance, ListPage } from '../../api';
import { Dock } from '../../dock/Dock';
import { DockProvider, useDock, type DockApi } from '../../dock/DockContext';
import { KeyboardStack } from '../../keyboard/KeyboardStack';
import { resetKeyboardStack } from '../../keyboard/stack';
import { resetLiveHandles } from '../../live/handles';
import { createQueryClient } from '../../query/client';
import { Drawer } from '../../shell/Drawer';
import { ShellProvider, useShell } from '../../shell/ShellContext';
import { resetViewMemory } from '../../shell/viewMemory';
import { findCode, getCode, textOf, typeInto } from '../../testing/codeEditor';
import { preselect, singularOf } from './CreateObject';
import { ResourceList } from './ResourceList';

/**
 * The list's "+", at page altitude: drawn from `ListPage.create`, the template
 * read on the press, and the create mode of the manifest editor behind it - the
 * two requests and the list re-read after.
 */

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  resetKeyboardStack();
  resetViewMemory();
  localStorage.clear();
});

const TEMPLATE = 'apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: example\n  namespace: shop\n';

function listPage(create: CreateAffordance | undefined, rows = 1): ListPage {
  return {
    kind: 'deployments',
    label: 'Deployments',
    namespaced: true,
    columns: [
      {
        key: 'name',
        label: 'Name',
        visible: true,
        order: 0,
        sortable: true,
        secondary: false,
        gauged: false,
        copyable: false,
        prose: false,
        filterable: false,
        role: 'name',
      },
    ],
    rows: Array.from({ length: rows }, (_, at) => ({
      id: `shop/web-${String(at)}`,
      name: `web-${String(at)}`,
      namespace: 'shop',
      cells: { name: { text: `web-${String(at)}` } },
    })),
    bulk: { scope: 'prod|deployments', verbs: [{ verb: 'delete', label: 'Delete', permitted: true }] },
    ...(create === undefined ? {} : { create }),
  };
}

const plan = {
  token: 'create-token-1',
  expiresAt: '2099-01-01T00:00:00Z',
  facts: {
    verb: 'create',
    targets: [{ kind: 'Deployment', namespace: 'shop', name: 'api' }],
    count: 1,
    destructive: false,
    diff: '+kind: Deployment\n+  name: api\n',
  },
};

interface Sent {
  path: string;
  url: string;
  method: string;
  body: unknown;
}

interface Reply {
  status?: number;
  body: unknown;
}

/** Records every request, method and body included: "Review sent the edited
 *  document to the create route" is a claim about what left the process. */
function recorder(routes: Readonly<Record<string, Reply | (() => Reply)>>): Sent[] {
  const sent: Sent[] = [];
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : String(input);
    const path = url.split('?')[0] ?? url;
    sent.push({
      path,
      url,
      method: init?.method ?? 'GET',
      body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined,
    });
    const route = routes[path];
    const reply: Reply =
      route === undefined
        ? { status: 404, body: { status: 404, title: 'Not Found', code: 'not_found' } }
        : typeof route === 'function'
          ? route()
          : route;
    const status = reply.status ?? 200;
    return Promise.resolve(
      new Response(JSON.stringify(reply.body), {
        status,
        headers: { 'Content-Type': status < 400 ? 'application/json' : 'application/problem+json' },
      }),
    );
  });
  return sent;
}

const PERMITTED: CreateAffordance = { permitted: true, namespace: 'shop' };

function common(page: () => ListPage): Record<string, Reply | (() => Reply)> {
  return {
    '/api/c/prod/r/deployments': () => ({ body: { state: 'ok', data: page() } }),
    '/api/preferences': {
      body: { rail: { barCount: 1, bars: [], hidden: [], tiles: {} }, columns: { kinds: {} } },
    },
    '/api/c/prod/namespaces': { body: { state: 'ok', items: [{ name: 'default' }, { name: 'shop' }] } },
    '/api/c/prod/create/template': {
      body: { state: 'ok', data: { document: TEMPLATE, kind: 'Deployment' } },
    },
  };
}

function mount(): void {
  const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ResourceList /> }], {
    initialEntries: ['/c/prod/deployments?namespace=shop'],
  });
  render(
    <QueryClientProvider client={createQueryClient()}>
      <KeyboardStack>
        <ShellProvider extensions={[]}>
          <RouterProvider router={router} />
        </ShellProvider>
      </KeyboardStack>
    </QueryClientProvider>,
  );
}

const PLUS = 'Create a Deployment';

/** What the editor holds, once its chunk has drawn it. */
async function editorText(): Promise<string> {
  return textOf(await findCode('New Deployment manifest'));
}

/** The press, the namespace step as it comes, and Continue. */
async function openEditor(): Promise<void> {
  fireEvent.click(await screen.findByRole('button', { name: PLUS }));
  const step = await screen.findByRole('dialog', { name: PLUS });
  fireEvent.click(within(step).getByRole('button', { name: 'Continue' }));
  await screen.findByRole('dialog', { name: 'New Deployment' });
}

describe('the button', () => {
  it('is absent where the list says nothing about creating', async () => {
    recorder(common(() => listPage(undefined)));
    mount();
    await screen.findByText('web-0');
    expect(screen.queryByRole('button', { name: PLUS })).toBeNull();
  });

  it('is drawn disabled carrying the reason where creating is refused', async () => {
    recorder(
      common(() =>
        listPage({
          permitted: false,
          namespace: 'shop',
          reason: { code: 'verb_denied', message: 'You may not create deployments in shop.' },
        }),
      ),
    );
    mount();
    const plus = await screen.findByRole('button', { name: PLUS });
    expect(plus.getAttribute('aria-disabled')).toBe('true');
    // The reason is the button's accessible description, not a `title` only:
    // a touch screen never shows a title and a screen reader may not read it.
    const described = document.getElementById(plus.getAttribute('aria-describedby') ?? '');
    expect(described?.textContent).toBe('You may not create deployments in shop.');
    expect(described?.className).toContain('sr-only');

    // Shown on the focus, and on a tap, which opens nothing.
    fireEvent.focus(plus);
    expect(described?.className).not.toContain('sr-only');
    fireEvent.blur(plus);
    expect(described?.className).toContain('sr-only');
    fireEvent.click(plus);
    expect(described?.className).not.toContain('sr-only');
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('is named by the kind as the cluster spells it', async () => {
    recorder(common(() => ({ ...listPage({ ...PERMITTED, kind: 'Ingress' }), kind: 'ingresses', label: 'Ingresses' })));
    mount();
    expect(await screen.findByRole('button', { name: 'Create an Ingress' })).toBeTruthy();
  });

  it('gives the corner to the selection bar while rows are ticked', async () => {
    recorder(common(() => listPage(PERMITTED)));
    mount();
    await screen.findByRole('button', { name: PLUS });
    fireEvent.click(screen.getByRole('checkbox', { name: 'Select shop/web-0' }));
    expect(screen.getByText('1 selected')).toBeTruthy();
    expect(screen.queryByRole('button', { name: PLUS })).toBeNull();
  });
});

describe('creating', () => {
  it('reads the template for the list namespace on the press and shows it', async () => {
    const sent = recorder(common(() => listPage(PERMITTED)));
    mount();
    expect(sent.some((one) => one.path === '/api/c/prod/create/template')).toBe(false);
    await openEditor();

    const read = sent.find((one) => one.path === '/api/c/prod/create/template');
    const query = new URLSearchParams(read?.url.split('?')[1] ?? '');
    expect(query.get('kind')).toBe('deployments');
    expect(query.get('namespace')).toBe('shop');
    await waitFor(async () => expect(await editorText()).toBe(TEMPLATE));
  });

  it('says the body is the person to write on a generic template', async () => {
    recorder({
      ...common(() => listPage(PERMITTED)),
      '/api/c/prod/create/template': {
        body: { state: 'ok', data: { document: 'kind: Deployment\n', kind: 'Deployment', generic: true } },
      },
    });
    mount();
    await openEditor();
    await waitFor(() => expect(screen.getByText(/the body is yours to write/)).toBeTruthy());
  });

  it('reviews the edited document through previewCreate and shows the plan', async () => {
    const sent = recorder({ ...common(() => listPage(PERMITTED)), '/api/c/prod/create/preview': { body: plan } });
    mount();
    await openEditor();
    await waitFor(async () => expect(await editorText()).toBe(TEMPLATE));
    const edited = TEMPLATE.replace('name: example', 'name: api');
    typeInto(getCode('New Deployment manifest'), edited);
    fireEvent.click(screen.getByRole('button', { name: 'Review' }));

    const confirmation = await screen.findByRole('dialog', { name: 'Create this Deployment in shop?' });
    expect(confirmation.textContent).toContain('+  name: api');
    const preview = sent.find((one) => one.path === '/api/c/prod/create/preview');
    expect(preview?.method).toBe('POST');
    expect(preview?.body).toEqual({ document: edited });
    // Never the edit route: a create is not a patch.
    expect(sent.some((one) => one.path.startsWith('/api/c/prod/manifest'))).toBe(false);
  });

  it('applies with the token, closes, and re-reads the list', async () => {
    let created = false;
    const sent = recorder({
      ...common(() => listPage(PERMITTED, created ? 2 : 1)),
      '/api/c/prod/create/preview': { body: plan },
      '/api/c/prod/create/apply': () => {
        created = true;
        return { body: { succeeded: 1, failed: 0, results: [] } };
      },
    });
    mount();
    await openEditor();
    await waitFor(async () => expect(await editorText()).toBe(TEMPLATE));
    fireEvent.click(screen.getByRole('button', { name: 'Review' }));
    const confirmation = await screen.findByRole('dialog', { name: 'Create this Deployment in shop?' });
    const listReads = sent.filter((one) => one.path === '/api/c/prod/r/deployments').length;
    fireEvent.click(within(confirmation).getByRole('button', { name: 'Apply' }));

    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'New Deployment' })).toBeNull());
    const apply = sent.find((one) => one.path === '/api/c/prod/create/apply');
    expect(apply?.body).toEqual({ token: 'create-token-1' });
    expect(sent.filter((one) => one.path === '/api/c/prod/r/deployments').length).toBeGreaterThan(listReads);
    await screen.findByText('web-1');
  });

  it('shows a refusal at preview in the dialog', async () => {
    recorder({
      ...common(() => listPage(PERMITTED)),
      '/api/c/prod/create/preview': {
        status: 403,
        body: {
          status: 403,
          title: 'Forbidden',
          code: 'verb_denied',
          detail: 'You may not create deployments in shop.',
        },
      },
    });
    mount();
    await openEditor();
    await waitFor(async () => expect(await editorText()).toBe(TEMPLATE));
    fireEvent.click(screen.getByRole('button', { name: 'Review' }));

    const dialog = screen.getByRole('dialog', { name: 'New Deployment' });
    const alert = await within(dialog).findByRole('alert');
    expect(alert.textContent).toContain('You may not create deployments in shop.');
    expect(screen.queryByRole('dialog', { name: 'Create this Deployment in shop?' })).toBeNull();
  });

  it('shows a 409 at apply in the confirmation', async () => {
    recorder({
      ...common(() => listPage(PERMITTED)),
      '/api/c/prod/create/preview': { body: plan },
      '/api/c/prod/create/apply': {
        status: 409,
        body: { status: 409, title: 'Conflict', code: 'plan_stale', detail: 'deployments "api" already exists' },
      },
    });
    mount();
    await openEditor();
    await waitFor(async () => expect(await editorText()).toBe(TEMPLATE));
    fireEvent.click(screen.getByRole('button', { name: 'Review' }));
    const confirmation = await screen.findByRole('dialog', { name: 'Create this Deployment in shop?' });
    fireEvent.click(within(confirmation).getByRole('button', { name: 'Apply' }));
    const alert = await within(confirmation).findByRole('alert');
    expect(alert.textContent).toContain('already exists');
  });
});

/**
 * The list with what surrounds it in the console: the dock under it and the
 * drawer beside it, each opened through its own context as a page would.
 */
const around: { dock: DockApi | null; shell: ReturnType<typeof useShell> | null } = { dock: null, shell: null };

function Probes(): ReactNode {
  const dock = useDock();
  const shell = useShell();
  useEffect(() => {
    around.dock = dock;
    around.shell = shell;
  }, [dock, shell]);
  return null;
}

function mountWithDockAndDrawer(): void {
  const router = createMemoryRouter(
    [
      {
        path: '/c/:cluster/:kind',
        element: (
          <ShellProvider extensions={[]}>
            <DockProvider>
              <Probes />
              <ResourceList />
              <Dock />
              <Drawer />
            </DockProvider>
          </ShellProvider>
        ),
      },
    ],
    { initialEntries: ['/c/prod/deployments?namespace=shop'] },
  );
  render(
    <QueryClientProvider client={createQueryClient()}>
      <KeyboardStack>
        <RouterProvider router={router} />
      </KeyboardStack>
    </QueryClientProvider>,
  );
}

/** The "+" floats: outside the scrolling panel, so no scroll moves it, and
 *  outside the drawer and the dock, so neither covers it by holding it. */
function floats(plus: HTMLElement): void {
  const corner = plus.closest('[data-create-corner]');
  expect(corner?.className).toContain('absolute');
  expect(corner?.className).toContain('bottom-3');
  expect(corner?.className).toContain('right-4');
  expect(document.getElementById('panel')?.contains(plus)).toBe(false);
  expect(document.getElementById('drawer')?.contains(plus) ?? false).toBe(false);
  // Translucent, so a row passing under it still reads.
  expect(plus.className).toContain('bg-panel-2/70');
}

describe('the floating button', () => {
  afterEach(() => {
    around.dock = null;
    around.shell = null;
    resetLiveHandles();
  });

  it('is drawn over the list, and stays with a drawer open and with the dock open', async () => {
    // The log pane's stream is never reached: jsdom has no EventSource.
    vi.stubGlobal(
      'EventSource',
      class {
        close(): void {}
        onmessage: unknown = null;
        onerror: unknown = null;
      },
    );
    recorder(common(() => listPage(PERMITTED, 40)));
    mountWithDockAndDrawer();
    floats(await screen.findByRole('button', { name: PLUS }));
    // The list keeps room after its last row for it.
    expect(document.getElementById('panel')?.className).toContain('pb-16');

    act(() => {
      around.shell?.openDrawer(<p>web-0 in the drawer</p>);
    });
    expect(document.getElementById('drawer')?.hidden).toBe(false);
    floats(screen.getByRole('button', { name: PLUS }));

    act(() => {
      around.shell?.closeDrawer();
      around.dock?.openLogs({ cluster: 'prod', kind: 'pods', namespace: 'shop', name: 'web-0' });
    });
    expect(screen.getAllByRole('tab').length).toBeGreaterThan(0);
    floats(screen.getByRole('button', { name: PLUS }));

    act(() => {
      around.shell?.openDrawer(<p>web-1 in the drawer</p>);
    });
    floats(screen.getByRole('button', { name: PLUS }));
  });
});

describe('the namespace step', () => {
  function templateNamespace(sent: readonly Sent[]): string | null | undefined {
    const read = sent.find((one) => one.path === '/api/c/prod/create/template');
    if (read === undefined) return undefined;
    return new URLSearchParams(read.url.split('?')[1] ?? '').get('namespace');
  }

  it('asks first, with the list namespace chosen, and reads nothing until Continue', async () => {
    const sent = recorder(common(() => listPage(PERMITTED)));
    mount();
    fireEvent.click(await screen.findByRole('button', { name: PLUS }));
    const step = await screen.findByRole('dialog', { name: PLUS });
    const choice = await within(step).findByRole('combobox', { name: /Namespace/ });
    expect((choice as HTMLSelectElement).value).toBe('shop');
    expect(templateNamespace(sent)).toBeUndefined();

    fireEvent.click(within(step).getByRole('button', { name: 'Continue' }));
    await screen.findByRole('dialog', { name: 'New Deployment' });
    expect(screen.queryByRole('dialog', { name: PLUS })).toBeNull();
    expect(templateNamespace(sent)).toBe('shop');
  });

  it('carries the namespace chosen to the template and the editor', async () => {
    const other = TEMPLATE.replace('namespace: shop', 'namespace: default');
    const sent = recorder({
      ...common(() => listPage(PERMITTED)),
      '/api/c/prod/create/template': { body: { state: 'ok', data: { document: other, kind: 'Deployment' } } },
    });
    mount();
    fireEvent.click(await screen.findByRole('button', { name: PLUS }));
    const step = await screen.findByRole('dialog', { name: PLUS });
    const choice = await within(step).findByRole('combobox', { name: /Namespace/ });
    fireEvent.change(choice, { target: { value: 'default' } });
    fireEvent.click(within(step).getByRole('button', { name: 'Continue' }));

    await screen.findByRole('dialog', { name: 'New Deployment' });
    expect(templateNamespace(sent)).toBe('default');
    await waitFor(async () => expect(await editorText()).toBe(other));
  });

  it('closes on Cancel and opens nothing', async () => {
    const sent = recorder(common(() => listPage(PERMITTED)));
    mount();
    fireEvent.click(await screen.findByRole('button', { name: PLUS }));
    const step = await screen.findByRole('dialog', { name: PLUS });
    fireEvent.click(within(step).getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(templateNamespace(sent)).toBeUndefined();
  });

  it('is offered across every namespace, where the server asked for one to be chosen', async () => {
    recorder(
      common(() =>
        listPage({
          permitted: false,
          reason: {
            code: 'not_supported_here',
            message: 'choose one namespace to create in: a new object goes to exactly one, and this list shows every namespace',
          },
        }),
      ),
    );
    mount();
    const plus = await screen.findByRole('button', { name: PLUS });
    expect(plus.getAttribute('aria-disabled')).toBeNull();
    fireEvent.click(plus);
    const step = await screen.findByRole('dialog', { name: PLUS });
    // The list is narrowed to `shop` in the address, so that one comes first.
    const choice = await within(step).findByRole('combobox', { name: /Namespace/ });
    expect((choice as HTMLSelectElement).value).toBe('shop');
  });

  it('is skipped for a cluster-scoped kind, whose template is read with no namespace', async () => {
    const sent = recorder(
      common(() => ({
        ...listPage({ permitted: true, kind: 'Deployment' }),
        namespaced: false,
      })),
    );
    mount();
    fireEvent.click(await screen.findByRole('button', { name: PLUS }));
    await screen.findByRole('dialog', { name: 'New Deployment' });
    expect(screen.queryByRole('dialog', { name: PLUS })).toBeNull();
    expect(templateNamespace(sent)).toBeNull();
  });
});

describe('preselect', () => {
  it('takes the reviewed namespace, then the list, then default, then the first', () => {
    expect(preselect('shop', ['web'], ['default'])).toBe('shop');
    expect(preselect(undefined, ['web', 'shop'], ['default'])).toBe('web');
    expect(preselect(undefined, [], ['kube-system', 'default'])).toBe('default');
    expect(preselect(undefined, [], ['kube-system'])).toBe('kube-system');
    expect(preselect(undefined, [], [])).toBe('default');
  });
});

describe('singularOf', () => {
  it('names one of the kinds Kubernetes lists', () => {
    expect(singularOf('Deployments')).toBe('Deployment');
    expect(singularOf('Ingresses')).toBe('Ingress');
    expect(singularOf('NetworkPolicies')).toBe('NetworkPolicy');
    expect(singularOf('StorageClasses')).toBe('StorageClass');
    expect(singularOf('Leases')).toBe('Lease');
  });
});
