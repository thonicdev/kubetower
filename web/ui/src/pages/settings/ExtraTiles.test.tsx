import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { ExtensionInfo } from '../../api';
import type { Extension } from '../../extension';
import { KeyboardStack } from '../../keyboard/KeyboardStack';
import { resetKeyboardStack } from '../../keyboard/stack';
import { ShellProvider } from '../../shell/ShellContext';
import { extraColumnShown } from '../resources/extraColumnMemory';
import { ExtraTab } from './ExtraTab';

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
});

const row = (key: string, label: string, over: Partial<ExtensionInfo> = {}): ExtensionInfo => ({
  key,
  label,
  enabled: false,
  pinned: false,
  writable: true,
  settings: [],
  ...over,
});

const items: ExtensionInfo[] = [
  // Keys sort one way, labels another; one label starts in lower case.
  row('a', 'Zeta'),
  row('b', 'beta'),
  row('kubecron', 'KubeCron', {
    enabled: true,
    settings: [{ key: 'checks', label: 'Run the checks', value: true, default: true, kind: 'bool' }],
  }),
  row('c', 'Alpha'),
];

function draw(list: readonly ExtensionInfo[] = items) {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>
        <KeyboardStack>
          <ExtraTab cluster="prod" clusters={[]} items={list} />
        </KeyboardStack>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}
const tile = (key: string): HTMLElement => document.querySelector<HTMLElement>(`[data-extra="${key}"]`) as HTMLElement;

const edit = (key: string): HTMLElement => tile(key).querySelector('[data-extra-edit]') as HTMLElement;
const about = (key: string): HTMLElement => tile(key).querySelector('[data-extra-about]') as HTMLElement;
const modal = (): HTMLElement => document.querySelector('[role="dialog"]') as HTMLElement;

describe('the cards never grow', () => {
  it('draws the glyph, the name, the state badge, the switch, Edit and About, and nothing else', () => {
    draw();
    const cron = tile('kubecron');
    expect(cron.textContent).toContain('KubeCron');
    expect(cron.querySelector('[data-extra-icon]')?.getAttribute('data-extra-icon')).toBe('extra');
    expect(cron.querySelector('[data-extra-state]')?.textContent).toBe('On');
    expect(tile('a').querySelector('[data-extra-state]')?.textContent).toBe('Off');
    expect(cron.querySelector('[role="switch"]')?.getAttribute('aria-checked')).toBe('true');
    expect(edit('kubecron').textContent).toBe('Edit');
    expect(edit('kubecron').querySelector('svg')).not.toBeNull();
    expect(about('kubecron').textContent).toBe('About');
    expect(about('kubecron').parentElement).toBe(edit('kubecron').parentElement);
    expect(cron.querySelector('[data-extra-setting]')).toBeNull();
    expect(cron.querySelector('[aria-expanded]')).toBeNull();
  });

  it('opens the window of the extra whose Edit was pressed, and leaves the grid as it was', () => {
    draw();
    const before = [...document.querySelectorAll('[data-extra]')].map((one) => one.outerHTML);
    fireEvent.click(edit('kubecron'));
    expect(modal().querySelector('h2')?.textContent).toBe('KubeCron');
    expect(modal().querySelector('[data-extra-setting="checks"]')).not.toBeNull();
    expect([...document.querySelectorAll('[data-extra]')].map((one) => one.outerHTML)).toEqual(before);
    expect(document.querySelectorAll('[data-extra-modal]')).toHaveLength(1);
    // Edit holds no About: About is on the card.
    expect(modal().querySelector('[data-extra-about]')).toBeNull();
  });

  it('opens About from the card, with Close as its only button', () => {
    draw();
    fireEvent.click(about('c'));
    expect(modal().querySelector('h2')?.textContent).toBe('Alpha');
    expect(modal().querySelector('[data-extra-preview="c"]')).not.toBeNull();
    expect([...modal().querySelectorAll('button')].map((one) => one.textContent)).toEqual(['Close']);
  });

  it("draws the extra's own glyph where it names one", () => {
    render(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>
          <ShellProvider extensions={[{ key: 'kubecron', icon: 'clock', nav: { label: 'KubeCron', path: 'x/kubecron' }, routes: [] }]}>
            <ExtraTab cluster="prod" clusters={[]} items={items} />
          </ShellProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(tile('kubecron').querySelector('[data-extra-icon] use')?.getAttribute('href')).toBe('#i-clock');
    expect(tile('a').querySelector('[data-extra-icon] use')?.getAttribute('href')).toBe('#i-extra');
  });

  it('turns an extra on from the tile, without opening anything', async () => {
    const sent: string[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'PUT') sent.push(String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url));
      return Promise.resolve(new Response(null, { status: 204 }));
    });
    draw();
    fireEvent.click(tile('c').querySelector('[role="switch"]') as Element);
    await waitFor(() => {
      expect(sent).toEqual(['/api/c/prod/extensions/c']);
    });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });
});

describe("an extra's settings window", () => {
  type Sent = { path: string; body: unknown };
  const stubWrites = (status = 204, detail = ''): Sent[] => {
    const sent: Sent[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if ((init?.method ?? 'GET') === 'PUT') {
        sent.push({ path, body: JSON.parse(String(init?.body ?? 'null')) });
        if (status !== 204) {
          const problem = { type: 'about:blank', title: 'Refused', status, detail, code: 'invalid' };
          return Promise.resolve(new Response(JSON.stringify(problem), { status, headers: { 'Content-Type': 'application/problem+json' } }));
        }
        return Promise.resolve(new Response(null, { status: 204 }));
      }
      return Promise.resolve(new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }));
    });
    return sent;
  };
  const text = { key: 'argocd-url', label: 'Argo CD URL', kind: 'text', value: false, default: false, text: 'https://a.example', placeholder: 'https://argocd' } satisfies ExtensionInfo['settings'][number];
  const choice = {
    key: 'tool', label: 'Tool', kind: 'choice', value: false, default: false, text: 'argocd', placeholder: 'Auto-detect',
    options: [{ value: 'auto', label: 'Auto-detect' }, { value: 'argocd', label: 'Argo CD' }],
  } satisfies ExtensionInfo['settings'][number];
  const delivery = row('argocd', 'Delivery Center', { enabled: true, settings: [choice, text, { key: 'checks', label: 'Run the checks', value: true, default: true, kind: 'bool' }] });
  const save = (): HTMLButtonElement => modal().querySelector('button[type="submit"]') as HTMLButtonElement;
  const url = (): HTMLInputElement => modal().querySelector('[data-extra-setting="argocd-url"] input') as HTMLInputElement;

  it('draws the switch, the tool choice, the URL and the checkbox, and writes nothing until Save', async () => {
    const sent = stubWrites();
    draw([delivery]);
    fireEvent.click(edit('argocd'));
    expect(modal().querySelector<HTMLSelectElement>('[data-extra-setting="tool"] select')?.value).toBe('argocd');
    expect(url().value).toBe('https://a.example');
    expect(modal().querySelector('[role="switch"]')?.getAttribute('aria-checked')).toBe('true');
    fireEvent.change(url(), { target: { value: ' https://b.example ' } });
    fireEvent.click(modal().querySelector('[data-extra-setting="checks"] input') as Element);
    fireEvent.click(modal().querySelector('[role="switch"]') as Element);
    expect(sent).toEqual([]);

    fireEvent.click(save());
    await waitFor(() => {
      expect(document.querySelector('[role="dialog"]')).toBeNull();
    });
    expect(sent).toEqual([
      { path: '/api/c/prod/extensions/argocd/settings/checks', body: { value: false } },
      { path: '/api/c/prod/extensions/argocd/settings/argocd-url', body: { text: 'https://b.example' } },
      { path: '/api/c/prod/extensions/argocd', body: { enabled: false } },
    ]);
  });

  it('turns an extra on from the window', async () => {
    const sent = stubWrites();
    draw([row('c', 'Alpha')]);
    fireEvent.click(edit('c'));
    fireEvent.click(modal().querySelector('[role="switch"]') as Element);
    fireEvent.click(save());
    await waitFor(() => {
      expect(sent).toEqual([{ path: '/api/c/prod/extensions/c', body: { enabled: true } }]);
    });
  });

  it("keeps the window open with the console's refusal on a 400", async () => {
    stubWrites(400, '"x" is not an http(s) URL');
    draw([delivery]);
    fireEvent.click(edit('argocd'));
    fireEvent.change(url(), { target: { value: 'x' } });
    fireEvent.click(save());
    await waitFor(() => {
      expect(modal().querySelector('[role="alert"]')?.textContent).toContain('is not an http(s) URL');
    });
    expect(url().value).toBe('x');
    expect(save().disabled).toBe(false);
  });

  it('discards on Cancel, on Escape and on a click on the backdrop', async () => {
    const sent = stubWrites();
    draw([delivery]);
    const closers: [string, () => void][] = [
      ['Cancel', () => fireEvent.click([...modal().querySelectorAll('button')].find((b) => b.textContent === 'Cancel') as Element)],
      ['Escape', () => fireEvent.keyDown(window, { key: 'Escape' })],
      ['backdrop', () => {
        const veil = modal().parentElement as HTMLElement;
        fireEvent.mouseDown(veil);
        fireEvent.click(veil);
      }],
    ];
    for (const [, close] of closers) {
      fireEvent.click(edit('argocd'));
      fireEvent.change(url(), { target: { value: 'https://changed.example' } });
      close();
      await waitFor(() => {
        expect(document.querySelector('[role="dialog"]')).toBeNull();
      });
      fireEvent.click(edit('argocd'));
      expect(url().value).toBe('https://a.example');
      close();
      await waitFor(() => {
        expect(document.querySelector('[role="dialog"]')).toBeNull();
      });
    }
    expect(sent).toEqual([]);
  });

  it('keeps Tab inside, and gives the focus back to the Edit button on close', async () => {
    draw([delivery]);
    edit('argocd').focus();
    fireEvent.click(edit('argocd'));
    const stops = [...modal().querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled]), a[href]')];
    const last = stops[stops.length - 1] as HTMLElement;
    const first = stops[0] as HTMLElement;
    last.focus();
    fireEvent.keyDown(last, { key: 'Tab' });
    expect(document.activeElement).toBe(first);
    first.focus();
    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true });
    expect(document.activeElement).toBe(last);

    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => {
      expect(document.querySelector('[role="dialog"]')).toBeNull();
    });
    expect(document.activeElement).toBe(edit('argocd'));
  });

  it('is the whole screen below 640 px, a box above', () => {
    draw([delivery]);
    fireEvent.click(edit('argocd'));
    expect(modal().className).toContain('max-sm:h-full');
    expect(modal().parentElement?.className).toContain('max-sm:p-0');
  });

  it('draws a pinned line at its variable, with nothing to type in', () => {
    draw([row('argocd', 'Delivery Center', { enabled: true, settings: [{ ...text, pinned: true, env: 'KT_ARGOCD_URL' }] })]);
    fireEvent.click(edit('argocd'));
    expect(modal().querySelector('[data-extra-setting="argocd-url"]')?.textContent).toContain('KT_ARGOCD_URL');
    expect(modal().querySelector('[data-extra-setting="argocd-url"] input')).toBeNull();
  });
});

describe("an extra's tool badges", () => {
  const withBadges = (badges?: string[]): Extension[] => [
    {
      key: 'kubecron',
      nav: { label: 'KubeCron', path: 'x/kubecron' },
      routes: [],
      about: { description: 'd', reads: [], permissions: [], requires: [], ...(badges === undefined ? {} : { badges }) },
    } as Extension,
  ];
  const mount = (shipped: Extension[]) =>
    render(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>
          <ShellProvider extensions={shipped}>
            <ExtraTab cluster="prod" clusters={[]} items={[row('kubecron', 'KubeCron', { enabled: true })]} />
          </ShellProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    );

  it('draws them on the card and in its window', () => {
    mount(withBadges(['Cilium', 'Calico']));
    expect([...tile('kubecron').querySelectorAll('[data-extra-tool]')].map((one) => one.textContent)).toEqual(['Cilium', 'Calico']);
    fireEvent.click(edit('kubecron'));
    expect(modal().querySelector('[data-extra-about-badges]')?.textContent).toBe('CiliumCalico');
  });

  it('draws nothing without them', () => {
    mount(withBadges());
    expect(document.querySelector('[data-extra-about-badges], [data-extra-tool]')).toBeNull();
    fireEvent.click(edit('kubecron'));
    expect(document.querySelector('[data-extra-about-badges]')).toBeNull();
  });
});

describe('the columns an extra adds to the lists', () => {
  const cells = (): Promise<Record<string, never>> => Promise.resolve({});
  const shipped: Extension[] = [
    {
      key: 'kubecron',
      nav: { label: 'KubeCron', path: 'x/kubecron' },
      routes: [],
      columns: [
        { key: 'next', label: 'Next run', kinds: ['cronjobs'], cells },
        // Gated by a setting of its own: that setting decides it, not this box.
        { key: 'own', label: 'Own', kinds: ['*'], cells, setting: 'list-column' },
      ],
    },
  ];

  it('offers one Show the column box per column in the window, kept in the browser once saved', async () => {
    render(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>
          <ShellProvider extensions={shipped}>
            <ExtraTab cluster="prod" clusters={[]} items={[row('kubecron', 'KubeCron', { enabled: true })]} />
          </ShellProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    fireEvent.click(edit('kubecron'));
    const boxes = modal().querySelectorAll<HTMLInputElement>('[data-extra-column] input');
    expect(boxes).toHaveLength(1);
    expect(modal().querySelector('[data-extra-column]')?.textContent).toBe('Show the columnNext run');
    expect(boxes[0]?.checked).toBe(false);

    fireEvent.click(boxes[0] as Element);
    expect(extraColumnShown('kubecron', 'next')).toBe(false);
    fireEvent.click(modal().querySelector('button[type="submit"]') as Element);
    await waitFor(() => {
      expect(document.querySelector('[role="dialog"]')).toBeNull();
    });
    expect(extraColumnShown('kubecron', 'next')).toBe(true);
    expect(localStorage.getItem('kubetower.extracolumns')).toBe('{"kubecron/next":true}');

    // A page link, since the extra is on and has a page.
    fireEvent.click(edit('kubecron'));
    expect(modal().querySelector<HTMLInputElement>('[data-extra-column] input')?.checked).toBe(true);
    expect(modal().querySelector('[data-extra-page]')?.getAttribute('href')).toBe('/c/prod/x/kubecron');
    fireEvent.click(modal().querySelector('[data-extra-column] input') as Element);
    fireEvent.click(modal().querySelector('button[type="submit"]') as Element);
    await waitFor(() => {
      expect(localStorage.getItem('kubetower.extracolumns')).toBeNull();
    });
  });
});

describe('an extra whose component the cluster does not run', () => {
  const suggestions = (list: unknown[]) =>
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
      const body = path.endsWith('/suggestions') ? { state: 'ok', items: list } : {};
      return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } }));
    });
  const order = (): string[] =>
    [...document.querySelectorAll<HTMLElement>('[data-extra]')].map((one) => one.dataset['extra'] ?? '');

  it('stays listed grayed with a short state, switchable, after the others', async () => {
    suggestions([{ key: 'b', label: 'beta', evidence: 'cert-manager absent', absent: true }]);
    draw();
    await waitFor(() => {
      expect(tile('b').getAttribute('data-extra-absent')).toBe('cert-manager absent');
    });
    expect(tile('b').textContent).toContain('cert-manager absent');
    expect(tile('b').className).toContain('opacity-70');
    const toggle = tile('b').querySelector<HTMLButtonElement>('[role="switch"]');
    expect(toggle?.disabled).toBe(false);
    // Alphabetical, the absent one last: beta would have been second.
    expect(order()).toEqual(['c', 'kubecron', 'a', 'b']);
    expect(tile('c').getAttribute('data-extra-absent')).toBeNull();
  });

  it('is not claimed when the detection found something, or could not answer', async () => {
    suggestions([{ key: 'b', label: 'beta', evidence: 'cert-manager is installed' }]);
    draw();
    await waitFor(() => {
      expect(order()).toEqual(['c', 'b', 'kubecron', 'a']);
    });
    expect(document.querySelector('[data-extra-absent]')).toBeNull();
  });
});

describe('the sentence of a suggested extra', () => {
  const answer = (status: number, body: unknown) =>
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
      const ok = path.endsWith('/suggestions');
      return Promise.resolve(
        new Response(JSON.stringify(ok ? body : {}), {
          status: ok ? status : 200,
          headers: { 'content-type': ok && status !== 200 ? 'application/problem+json' : 'application/json' },
        }),
      );
    });

  it('is a badge on the card of an off extra only, the banner sentence in its tooltip', async () => {
    answer(200, {
      state: 'ok',
      items: [
        { key: 'b', label: 'beta', evidence: '2 Redis Services in 1 namespace' },
        { key: 'kubecron', label: 'KubeCron', evidence: '3 CronJobs' },
      ],
    });
    draw();
    await waitFor(() => {
      expect(tile('b').querySelector('[data-extra-suggested]')?.getAttribute('data-extra-suggested')).toBe(
        '2 Redis Services in 1 namespace on this cluster.',
      );
    });
    expect(tile('b').querySelector('[data-extra-suggested] [title]')?.getAttribute('title')).toBe(
      '2 Redis Services in 1 namespace on this cluster.',
    );
    expect(tile('b').querySelector('[data-extra-suggested]')?.textContent).toBe('detected');
    expect(tile('kubecron').querySelector('[data-extra-suggested]')).toBeNull();
    expect(tile('c').querySelector('[data-extra-suggested]')).toBeNull();
  });

  it('is not drawn when the suggestions could not be read', async () => {
    answer(500, { status: 500, title: 'Internal Server Error', code: 'internal', detail: 'boom' });
    draw();
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(document.querySelector('[data-extra-suggested]')).toBeNull();
    expect(document.body.textContent).not.toContain('boom');
  });
});
