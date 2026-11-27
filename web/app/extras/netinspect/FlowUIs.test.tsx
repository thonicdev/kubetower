import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { queryKeys } from '@kubetower/ui';

import { configuredHref, tunnelSuffix, type FlowUIs } from './data';
import { FlowUIButtons } from './FlowUIs';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const TUNNEL_HUBBLE = { namespace: 'kube-system', service: 'hubble-ui', port: 80, portName: 'http' };
const TUNNEL_WHISKER = { namespace: 'calico-system', service: 'whisker', port: 8081 };

const FORWARD = { id: 'f1', cluster: 'kind', namespace: 'kube-system', pod: 'hubble-ui-0', service: 'hubble-ui', remotePort: 80, address: '127.0.0.1:41000', scheme: 'http' };

/** The forwards the console holds, as `GET /api/forwards` answers them; a POST adds one. */
let held: unknown[] = [];

/** What `POST /api/forwards/{id}/window` was sent, and what it answers. */
let windows: { id: string; body: unknown }[] = [];
let windowAnswer: { status: number; body?: unknown } = { status: 204 };

function stub(uis: FlowUIs | number, portForward = true, toolWindow = false): ReturnType<typeof vi.fn> {
  held = [];
  windows = [];
  windowAnswer = { status: 204 };
  const fetch = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
    const json = (status: number, body: unknown) =>
      Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }));
    if (/^\/x\/[a-z]+\/netinspect\/uis$/.test(url)) return typeof uis === 'number' ? json(uis, { code: 'x', message: 'no' }) : json(200, uis);
    if (url === '/api/capabilities') return json(200, { portForward, toolWindow });
    const window = /^\/api\/forwards\/([^/]+)\/window$/.exec(url);
    if (window !== null && init?.method === 'POST') {
      windows.push({ id: decodeURIComponent(window[1] ?? ''), body: JSON.parse(init.body as string) });
      if (windowAnswer.status === 204) return Promise.resolve(new Response(null, { status: 204 }));
      return Promise.resolve(new Response(JSON.stringify(windowAnswer.body), { status: windowAnswer.status, headers: { 'Content-Type': 'application/problem+json' } }));
    }
    if (url === '/api/forwards' && init?.method === 'POST') {
      const body = JSON.parse(init.body as string) as { namespace: string; service: string; remotePort: number };
      const made = { ...FORWARD, namespace: body.namespace, service: body.service, remotePort: body.remotePort };
      held = [made];
      return json(200, made);
    }
    if (url === '/api/forwards') return json(200, held);
    return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

/** The client of the last mount, to read the forwards again as the page would. */
let client = new QueryClient();

function mount(namespace = '', cluster = 'kind'): (cluster: string) => void {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = (c: string) => (
    <QueryClientProvider client={client}>
      <FlowUIButtons cluster={c} namespace={namespace} />
    </QueryClientProvider>
  );
  const { rerender } = render(view(cluster));
  return (c) => rerender(view(c));
}

describe('FlowUIButtons', () => {
  it('draws no button where neither UI is there', async () => {
    const fetch = stub({});
    mount();
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining('/uis'), expect.anything()));
    await new Promise((r) => setTimeout(r, 30));
    expect(screen.queryByRole('button')).toBeNull();
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('draws no button, and no error, where the Services could not be read', async () => {
    stub(403);
    mount();
    await new Promise((r) => setTimeout(r, 30));
    expect(screen.queryByRole('button')).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('draws a button for each UI the cluster has, named as the UI is', async () => {
    stub({ hubble: { tunnel: TUNNEL_HUBBLE }, whisker: { tunnel: TUNNEL_WHISKER } });
    mount();
    expect(await screen.findByRole('button', { name: 'Hubble UI' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Whisker' })).toBeTruthy();
  });

  it('draws only the one that is there', async () => {
    stub({ whisker: { tunnel: TUNNEL_WHISKER } });
    mount();
    expect(await screen.findByRole('button', { name: 'Whisker' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Hubble UI' })).toBeNull();
  });

  it('opens the tunnel, then links to it with the namespace for Hubble UI, asking for no window', async () => {
    const fetch = stub({ hubble: { tunnel: TUNNEL_HUBBLE } });
    // The desktop window refuses every window a page opens: window.open answers null.
    const open = vi.fn(() => null);
    vi.stubGlobal('open', open);
    mount('shop');
    fireEvent.click(await screen.findByRole('button', { name: 'Hubble UI' }));
    const link = await screen.findByRole('link', { name: /Hubble UI/ });
    expect(link.getAttribute('href')).toMatch(/^http:\/\/(localhost|127\.0\.0\.1):41000\/\?namespace=shop$/);
    expect(link.getAttribute('target')).toBe('_blank');
    expect(link.getAttribute('rel')).toContain('noopener');
    expect(open).not.toHaveBeenCalled();
    expect(screen.queryByRole('alert')).toBeNull();
    const post = fetch.mock.calls.find((c) => c[0] === '/api/forwards' && (c[1] as RequestInit | undefined)?.method === 'POST');
    expect(JSON.parse((post?.[1] as RequestInit).body as string)).toEqual({
      cluster: 'kind', namespace: 'kube-system', service: 'hubble-ui', remotePort: 80, portName: 'http',
    });
  });

  it('links Whisker to its general view, with no namespace', async () => {
    stub({ whisker: { tunnel: TUNNEL_WHISKER } });
    mount('shop');
    fireEvent.click(await screen.findByRole('button', { name: 'Whisker' }));
    const link = await screen.findByRole('link', { name: /Whisker/ });
    expect(link.getAttribute('href')).toMatch(/:41000\/$/);
  });

  it('says why, and draws no link, when the tunnel is refused', async () => {
    const fetch = stub({ hubble: { tunnel: TUNNEL_HUBBLE } });
    fetch.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
      const json = (status: number, body: unknown) =>
        Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/problem+json' } }));
      if (url.endsWith('/uis')) return json(200, { hubble: { tunnel: TUNNEL_HUBBLE } });
      if (url === '/api/capabilities') return json(200, { portForward: true });
      if (init?.method === 'POST') return json(403, { title: 'Forbidden', status: 403, detail: 'You may not forward to pods in kube-system.' });
      return json(599, {});
    });
    mount();
    fireEvent.click(await screen.findByRole('button', { name: 'Hubble UI' }));
    expect((await screen.findByRole('alert')).textContent).toContain('kube-system');
    expect(screen.queryByRole('link')).toBeNull();
    expect(screen.getByRole('button', { name: 'Hubble UI' })).toBeTruthy();
  });

  it('gives the focus to the link that replaces the button', async () => {
    stub({ hubble: { tunnel: TUNNEL_HUBBLE } });
    mount('shop');
    fireEvent.click(await screen.findByRole('button', { name: 'Hubble UI' }));
    const link = await screen.findByRole('link', { name: /Hubble UI/ });
    await waitFor(() => expect(document.activeElement).toBe(link));
  });

  it('drops the focus request when the list read again does not draw the forward it opened', async () => {
    stub({ hubble: { tunnel: TUNNEL_HUBBLE } });
    // The page names the cluster another way than the forward does: it never matches.
    mount('shop', 'other');
    fireEvent.click(await screen.findByRole('button', { name: 'Hubble UI' }));
    await waitFor(() => expect((screen.getByRole('button', { name: 'Hubble UI' }) as HTMLButtonElement).disabled).toBe(false));
    const elsewhere = document.createElement('input');
    document.body.append(elsewhere);
    elsewhere.focus();
    // A forward of this cluster appears later: its link does not take the focus.
    held = [{ ...FORWARD, id: 'f2', cluster: 'other' }];
    await client.invalidateQueries({ queryKey: queryKeys.forwards });
    expect(await screen.findByRole('link', { name: /Hubble UI/ })).toBeTruthy();
    expect(document.activeElement).toBe(elsewhere);
    elsewhere.remove();
  });

  it('links at once to a forward already open, and turns back into a button once the server closed it', async () => {
    const fetch = stub({ hubble: { tunnel: TUNNEL_HUBBLE } });
    held = [FORWARD];
    mount('shop');
    const link = await screen.findByRole('link', { name: /Hubble UI/ });
    expect(link.getAttribute('href')).toMatch(/:41000\/\?namespace=shop$/);
    expect(fetch.mock.calls.some((c) => c[0] === '/api/forwards' && (c[1] as RequestInit | undefined)?.method === 'POST')).toBe(false);
    // The pod behind the tunnel went away: the server no longer holds it.
    held = [];
    fireEvent.click(link);
    expect(await screen.findByRole('button', { name: 'Hubble UI' })).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Hubble UI' }));
    expect(await screen.findByRole('link', { name: /Hubble UI/ })).toBeTruthy();
    expect(fetch.mock.calls.filter((c) => c[0] === '/api/forwards' && (c[1] as RequestInit | undefined)?.method === 'POST')).toHaveLength(1);
  });

  it("never links one cluster's button to another cluster's forward", async () => {
    stub({ hubble: { tunnel: TUNNEL_HUBBLE } });
    held = [FORWARD];
    const show = mount('shop');
    expect(await screen.findByRole('link', { name: /Hubble UI/ })).toBeTruthy();
    show('other');
    expect(await screen.findByRole('button', { name: 'Hubble UI' })).toBeTruthy();
    expect(screen.queryByRole('link', { name: /Hubble UI/ })).toBeNull();
  });

  it('has no tunnel button where the console cannot forward, unless an address is configured', async () => {
    stub({ hubble: { tunnel: TUNNEL_HUBBLE }, whisker: { url: 'https://whisker.example.com' } }, false);
    mount();
    const link = await screen.findByRole('link', { name: 'Whisker' });
    expect(link.getAttribute('href')).toBe('https://whisker.example.com');
    expect(screen.queryByRole('button', { name: 'Hubble UI' })).toBeNull();
  });

  it('uses the configured address, with the namespace, in a new tab, even with a tunnel to offer', async () => {
    stub({ hubble: { url: 'https://hubble.example.com/ui' } });
    mount('shop');
    const link = await screen.findByRole('link', { name: 'Hubble UI' });
    expect(link.getAttribute('href')).toBe('https://hubble.example.com/ui?namespace=shop');
    expect(link.getAttribute('target')).toBe('_blank');
    expect(link.getAttribute('rel')).toContain('noopener');
  });

  it('refuses a configured address that is not http or https', async () => {
    stub({ hubble: { url: 'javascript:alert(1)' } });
    mount();
    await new Promise((r) => setTimeout(r, 30));
    expect(screen.queryByRole('link')).toBeNull();
  });
});

describe('FlowUIButtons in the desktop window', () => {
  const posts = (fetch: ReturnType<typeof vi.fn>) => fetch.mock.calls.filter((c) => c[0] === '/api/forwards' && (c[1] as RequestInit | undefined)?.method === 'POST');

  it('opens the tunnel, then the UI in a window of its own that closes the tunnel with it', async () => {
    const fetch = stub({ hubble: { tunnel: TUNNEL_HUBBLE } }, true, true);
    const open = vi.fn(() => null);
    vi.stubGlobal('open', open);
    mount('shop');
    fireEvent.click(await screen.findByRole('button', { name: 'Hubble UI' }));
    await waitFor(() => expect(windows).toHaveLength(1));
    expect(windows[0]).toEqual({ id: 'f1', body: { tool: 'hubble', path: '/?namespace=shop', closeWithWindow: true } });
    expect(posts(fetch)).toHaveLength(1);
    expect(open).not.toHaveBeenCalled();
    // The button stays a button; the browser is ↗, now a link to the tunnel.
    expect(screen.getByRole('button', { name: 'Hubble UI' })).toBeTruthy();
    const browser = await screen.findByRole('link', { name: 'Hubble UI in browser' });
    expect(browser.getAttribute('href')).toMatch(/:41000\/\?namespace=shop$/);
    expect(browser.getAttribute('target')).toBe('_blank');
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('opens a forward already open in a window without closing it with the window', async () => {
    const fetch = stub({ whisker: { tunnel: TUNNEL_WHISKER } }, true, true);
    held = [{ ...FORWARD, namespace: 'calico-system', service: 'whisker', remotePort: 8081 }];
    mount('shop');
    expect(await screen.findByRole('link', { name: 'Whisker in browser' })).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Whisker' }));
    await waitFor(() => expect(windows).toHaveLength(1));
    expect(windows[0]).toEqual({ id: 'f1', body: { tool: 'whisker', path: '/', closeWithWindow: false } });
    expect(posts(fetch)).toHaveLength(0);
  });

  it('opens the tunnel for the browser from ↗, and asks for no window', async () => {
    const fetch = stub({ hubble: { tunnel: TUNNEL_HUBBLE } }, true, true);
    mount('shop');
    fireEvent.click(await screen.findByRole('button', { name: 'Hubble UI in browser' }));
    const browser = await screen.findByRole('link', { name: 'Hubble UI in browser' });
    expect(browser.getAttribute('href')).toMatch(/:41000\/\?namespace=shop$/);
    await waitFor(() => expect(document.activeElement).toBe(browser));
    expect(posts(fetch)).toHaveLength(1);
    expect(windows).toHaveLength(0);
  });

  it('says why when the window is refused, and keeps the browser open to it', async () => {
    stub({ hubble: { tunnel: TUNNEL_HUBBLE } }, true, true);
    windowAnswer = { status: 409, body: { title: 'Conflict', status: 409, detail: 'Not in the desktop window.' } };
    mount();
    fireEvent.click(await screen.findByRole('button', { name: 'Hubble UI' }));
    expect((await screen.findByRole('alert')).textContent).toContain('desktop window');
    expect(await screen.findByRole('link', { name: 'Hubble UI in browser' })).toBeTruthy();
  });

  it('keeps the browser link where the console is not in the desktop window', async () => {
    stub({ hubble: { tunnel: TUNNEL_HUBBLE } }, true, false);
    mount();
    fireEvent.click(await screen.findByRole('button', { name: 'Hubble UI' }));
    expect(await screen.findByRole('link', { name: /Hubble UI/ })).toBeTruthy();
    expect(screen.queryByRole('link', { name: 'Hubble UI in browser' })).toBeNull();
    expect(windows).toHaveLength(0);
  });
});

describe('URLs', () => {
  it('puts the namespace on Hubble UI only', () => {
    expect(configuredHref('hubble', 'https://h.example.com/ui', 'a b')).toBe('https://h.example.com/ui?namespace=a+b');
    expect(configuredHref('hubble', 'https://h.example.com/ui', '')).toBe('https://h.example.com/ui');
    expect(configuredHref('whisker', 'https://w.example.com', 'shop')).toBe('https://w.example.com');
    expect(tunnelSuffix('hubble', 'shop')).toBe('/?namespace=shop');
    expect(tunnelSuffix('hubble', '')).toBe('/');
    expect(tunnelSuffix('whisker', 'shop')).toBe('/');
  });
});
