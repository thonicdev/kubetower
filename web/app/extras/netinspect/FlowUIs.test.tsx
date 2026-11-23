import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { configuredHref, tunnelSuffix, type FlowUIs } from './data';
import { FlowUIButtons } from './FlowUIs';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const TUNNEL_HUBBLE = { namespace: 'kube-system', service: 'hubble-ui', port: 80, portName: 'http' };
const TUNNEL_WHISKER = { namespace: 'calico-system', service: 'whisker', port: 8081 };

function stub(uis: FlowUIs | number, portForward = true): ReturnType<typeof vi.fn> {
  const fetch = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
    const json = (status: number, body: unknown) =>
      Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }));
    if (url === '/x/kind/netinspect/uis') return typeof uis === 'number' ? json(uis, { code: 'x', message: 'no' }) : json(200, uis);
    if (url === '/api/capabilities') return json(200, { portForward });
    if (url === '/api/forwards' && init?.method === 'POST') {
      return json(200, { id: 'f1', address: '127.0.0.1:41000', scheme: 'http' });
    }
    return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

function mount(namespace = ''): void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <FlowUIButtons cluster="kind" namespace={namespace} />
    </QueryClientProvider>,
  );
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
    const post = fetch.mock.calls.find((c) => c[0] === '/api/forwards');
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
