import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { CERTMANAGER_OVERVIEW, CERTMANAGER_TAB } from '../../../measure/certmanager.fixture';
import CertificateTab from './CertificateTab';
import type { Certificate } from './data';
import { certmanager } from './index';
import { certificateShortcut } from './shortcut';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type Reply = { status?: number; body: unknown };

function stub(replies: Record<string, Reply>): string[] {
  const asked: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      asked.push(url);
      const reply = replies[url];
      if (reply === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
      return Promise.resolve(
        new Response(JSON.stringify(reply.body), { status: reply.status ?? 200, headers: { 'Content-Type': 'application/json' } }),
      );
    }),
  );
  return asked;
}

function mount(kind: string, namespace: string, name: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <CertificateTab cluster="prod" kind={kind} namespace={namespace} name={name} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const cert = (i: number) => CERTMANAGER_OVERVIEW.certificates[i] as Certificate;
const SECRET = '/x/prod/certmanager/objects/secrets/shop/storefront-tls';

describe('the Certificate tab', () => {
  it('draws the days left as a bar, the dates and the issuer', async () => {
    stub({ [SECRET]: { body: { now: CERTMANAGER_OVERVIEW.now, certificates: [cert(1)] } } });
    mount('secrets', 'shop', 'storefront-tls');
    const meter = await screen.findByRole('meter', { name: 'Days left, over 90 days' });
    expect(meter.getAttribute('aria-valuenow')).toBe('3');
    expect(screen.getByText('3 days left')).toBeTruthy();
    expect(screen.getByText('letsencrypt')).toBeTruthy();
    expect(screen.getByText('renewal late')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'storefront' }).getAttribute('href')).toBe('/c/prod/x/certmanager?cert=shop%2Fstorefront');
  });

  it('says why a certificate is not ready, and draws no length for it', async () => {
    stub({ [SECRET]: { body: { now: CERTMANAGER_OVERVIEW.now, certificates: [cert(7)] } } });
    mount('secrets', 'shop', 'storefront-tls');
    await screen.findByText('not issued');
    expect(screen.getByRole('meter').getAttribute('aria-valuenow')).toBe('0');
    expect(screen.getByText(/has failed to complete/)).toBeTruthy();
  });

  it('says none, a refusal and a failure apart', async () => {
    stub({ [SECRET]: { status: 404, body: { code: 'no_certificate', message: 'none' } } });
    mount('secrets', 'shop', 'storefront-tls');
    await screen.findByText('No certificate');
    cleanup();

    stub({ [SECRET]: { status: 403, body: { code: 'list_forbidden', message: 'You may not list them.' } } });
    mount('secrets', 'shop', 'storefront-tls');
    await screen.findByText('Refused');
    cleanup();

    stub({ [SECRET]: { status: 502, body: { code: 'list_failed', message: 'Could not.' } } });
    mount('secrets', 'shop', 'storefront-tls');
    await screen.findByText('Could not read');
  });
});

describe('the issuance chain', () => {
  const INGRESS = '/x/prod/certmanager/objects/ingresses/shop/front';
  const chainOf = (name: string): HTMLElement => {
    const section = document.querySelector(`[data-cert="${name}"]`);
    if (section === null) throw new Error(`no card for ${name}`);
    return section.querySelector('[data-chain]') as HTMLElement;
  };

  it('draws a pending challenge with its reason, and the step the chain is stuck at', async () => {
    stub({ [INGRESS]: { body: CERTMANAGER_TAB } });
    mount('ingresses', 'shop', 'front');
    await screen.findAllByRole('group', { name: 'Issuance chain' });
    const chain = chainOf('shop/storefront');
    expect(chain.getAttribute('data-stuck')).toBe('Challenge');
    const challenge = chain.querySelector('[data-link="Challenge"][data-tone="bad"]') as HTMLElement;
    expect(challenge.textContent).toContain('wrong status code');
    expect(challenge.textContent).toContain('HTTP-01');
    expect(challenge.querySelector('[data-glyph="cross"]')).not.toBeNull();
    expect(chain.querySelector('[data-link="Challenge"][data-tone="ok"] [data-glyph="check"]')).not.toBeNull();
    expect(chain.querySelector('[data-link="Order"]')?.getAttribute('data-tone')).toBe('warn');
  });

  it('draws a denied request with its reason, an order that does not exist and an issuer it may not read, as three things', async () => {
    stub({ [INGRESS]: { body: CERTMANAGER_TAB } });
    mount('ingresses', 'shop', 'front');
    await screen.findAllByRole('group', { name: 'Issuance chain' });
    const chain = chainOf('shop/images');
    const request = chain.querySelector('[data-link="CertificateRequest"]') as HTMLElement;
    expect(request.getAttribute('data-tone')).toBe('bad');
    expect(request.textContent).toContain('denied');
    expect(request.textContent).toContain('Policy denied');
    expect(request.textContent).toContain('Denied ✓');
    const order = chain.querySelector('[data-link="Order"]') as HTMLElement;
    const issuer = chain.querySelector('[data-link="ClusterIssuer"]') as HTMLElement;
    expect(order.getAttribute('data-state')).toBe('none');
    expect(order.querySelector('[data-glyph="none"]')).not.toBeNull();
    expect(issuer.getAttribute('data-state')).toBe('refused');
    expect(issuer.querySelector('[data-glyph="lock"]')).not.toBeNull();
    expect(issuer.textContent).toContain('refused');
    expect(order.textContent).not.toContain('refused');
  });

  it('draws an errored order that made no challenge, and an answer with no chain', async () => {
    const errored = {
      ...CERTMANAGER_TAB.chains['images'],
      request: { ...CERTMANAGER_TAB.chains['storefront']?.request, tone: 'warn' },
      order: { kind: 'Order', state: 'found', name: 'order-1', tone: 'bad', phase: 'errored', reason: 'policy forbids the name' },
      issuer: { kind: 'ClusterIssuer', state: 'found', name: 'letsencrypt', tone: 'ok', phase: 'ready', type: 'acme' },
    };
    stub({ [SECRET]: { body: { now: CERTMANAGER_OVERVIEW.now, certificates: [cert(1)], chains: { storefront: errored } } } });
    mount('secrets', 'shop', 'storefront-tls');
    await screen.findByRole('group', { name: 'Issuance chain' });
    const order = document.querySelector('[data-link="Order"]') as HTMLElement;
    expect(order.textContent).toContain('errored');
    expect(order.textContent).toContain('policy forbids the name');
    cleanup();

    // An older answer with no chains still draws the certificate.
    stub({ [SECRET]: { body: { now: CERTMANAGER_OVERVIEW.now, certificates: [cert(1)] } } });
    mount('secrets', 'shop', 'storefront-tls');
    await screen.findByText('3 days left');
    expect(document.querySelector('[data-chain]')).toBeNull();
  });
});

describe('the wiring', () => {
  it('draws a Secret tab only on a Secret cert-manager marked', () => {
    const secret = certmanager.tabs?.find((t) => t.key === 'secret');
    expect(secret?.when?.({ labels: {}, annotations: { 'cert-manager.io/certificate-name': 'web' } })).toBe(true);
    expect(secret?.when?.({ labels: {}, annotations: {} })).toBe(false);
  });

  it('asks the route whether an Ingress has a certificate behind it', async () => {
    const path = '/x/prod/certmanager/objects/ingresses/shop/plain';
    stub({ [path]: { status: 404, body: { code: 'no_certificate', message: 'none' } } });
    const ingress = certmanager.tabs?.find((t) => t.key === 'ingress');
    const signal = new AbortController().signal;
    expect(await ingress?.probe?.({ cluster: 'prod', kind: 'ingresses', namespace: 'shop', name: 'plain' }, signal)).toBe(false);
    stub({ [path]: { body: { now: CERTMANAGER_OVERVIEW.now, certificates: [cert(2)] } } });
    expect(await ingress?.probe?.({ cluster: 'prod', kind: 'ingresses', namespace: 'shop', name: 'plain' }, signal)).toBe(true);
  });

  it('gives the shortcut the soonest certificate, or no tile', async () => {
    const path = '/x/prod/certmanager/objects/ingresses/shop/front';
    stub({ [path]: { body: { now: CERTMANAGER_OVERVIEW.now, certificates: [cert(2), cert(10)] } } });
    const signal = new AbortController().signal;
    const target = await certificateShortcut({ cluster: 'prod', kind: 'ingresses', namespace: 'shop', name: 'front' }, signal);
    expect(target?.title).toBe('images');
    expect(target?.badges).toEqual(['5 days left', 'Ready']);
    stub({ [path]: { status: 404, body: { code: 'no_certificate', message: 'none' } } });
    expect(await certificateShortcut({ cluster: 'prod', kind: 'ingresses', namespace: 'shop', name: 'front' }, signal)).toBeNull();
  });

  it('reads one column per list', async () => {
    const asked = stub({ '/x/prod/certmanager/cells?kind=secrets': { body: { 'shop/web-tls': { text: '3 d', tone: 'bad' } } } });
    const column = certmanager.columns?.[0];
    const cells = await column?.cells({ cluster: 'prod', kind: 'secrets' }, new AbortController().signal);
    expect(cells?.['shop/web-tls']?.text).toBe('3 d');
    expect(asked).toHaveLength(1);
    expect(column?.kinds).toEqual(['certificates.cert-manager.io', 'secrets', 'ingresses']);
  });
});
