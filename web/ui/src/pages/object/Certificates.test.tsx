import { QueryClientProvider } from '@tanstack/react-query';
import { render, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { CertificateSet, DetailPage, ObjectURL } from '../../api';
import { createQueryClient } from '../../query/client';
import { stubFetch } from '../../testing/harness';
import { ObjectDetail } from './ObjectDetail';

/**
 * The certificate cards and the addresses.
 *
 * Two assertions carry the weight and both are about **not collapsing two things
 * into one**: a bundle holding a leaf and its intermediates is not one
 * certificate, and a bundle whose second block did not parse is not a bundle
 * that could not be read.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

const OBJECT = '/api/c/prod/r/secrets/shop-prod/shop-tls';

const certificates: CertificateSet = {
  bundles: [
    {
      key: 'tls.crt',
      certificates: [
        {
          position: 1,
          role: 'leaf',
          subject: 'CN=shop.example',
          issuer: 'CN=R3',
          selfSigned: false,
          isCA: false,
          sans: ['shop.example', 'www.shop.example'],
          sansNote: '3 more',
          notBefore: '2026-07-01T00:00:00Z',
          notAfter: '2026-11-12T00:00:00Z',
          expiry: 'expires in 61d4h',
          expiryTone: 'ok',
          serial: '03:9a:1f',
          signatureAlgorithm: 'SHA256-RSA',
          publicKey: 'RSA 2048',
        },
        {
          position: 2,
          role: 'intermediate',
          subject: 'CN=R3',
          issuer: 'CN=ISRG Root X1',
          selfSigned: false,
          isCA: true,
          notBefore: '2020-09-04T00:00:00Z',
          notAfter: '2027-09-15T00:00:00Z',
          expiry: 'expires in 368d',
          expiryTone: 'ok',
        },
      ],
    },
    {
      key: 'ca.crt',
      note: 'the second PEM block is a certificate request, not a certificate',
      certificates: [
        {
          position: 1,
          role: 'root',
          subject: 'CN=internal',
          issuer: 'CN=internal',
          selfSigned: true,
          isCA: true,
          notBefore: '2024-01-01T00:00:00Z',
          notAfter: '2026-09-20T00:00:00Z',
          expiry: 'expires in 8d',
          expiryTone: 'warn',
        },
      ],
    },
  ],
  privateKeys: ['tls.key'],
};

const urls: ObjectURL[] = [
  {
    role: 'Ingress',
    address: 'https://shop.example/api',
    host: 'shop.example',
    path: '/api(/|$)(.*)',
    tls: true,
    service: 'api',
    note: 'the path is a pattern; only its literal beginning is opened',
  },
  { role: 'Ingress', address: 'http://internal.shop', host: 'internal.shop', tls: false },
];

const page: DetailPage = {
  kind: 'secrets',
  namespace: 'shop-prod',
  name: 'shop-tls',
  title: 'Secret',
  tabs: [],
  sections: [],
  certificates,
  urls,
  urlsNote: '1 rule points at a resource backend and has no address.',
};

async function mount(over: Partial<DetailPage> = {}): Promise<HTMLElement> {
  stubFetch({ [OBJECT]: { body: { state: 'ok', data: { ...page, ...over } } } });
  const router = createMemoryRouter(
    [
      {
        path: '/c/:cluster/:kind',
        element: (
          <ObjectDetail
            identity={{ cluster: 'prod', kind: 'secrets', namespace: 'shop-prod', name: 'shop-tls' }}
            chrome="drawer"
          />
        ),
      },
      { path: '/health', element: <p>health</p> },
    ],
    { initialEntries: ['/c/prod/secrets'] },
  );
  const view = render(
    <QueryClientProvider client={createQueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  // Waiting for the **body** and not the header: the header is drawn from the
  // identity the caller passed, before the read comes back, so waiting for the
  // name would settle instantly and every assertion below would run against an
  // empty panel. Every fixture here carries a certificate set.
  await waitFor(() => {
    expect(view.container.querySelector('[data-section="certificates"]')).not.toBeNull();
  });
  return view.container;
}

describe('the certificate cards', () => {
  it('draws one card per certificate and not one per key', async () => {
    // `tls.crt` routinely holds a leaf plus its intermediates, and rendering two
    // certificates as one is a wrong answer that looks right.
    const container = await mount();
    const bundle = container.querySelector('[data-bundle="tls.crt"]');
    expect(bundle?.querySelectorAll('[data-certificate]')).toHaveLength(2);
    expect(bundle?.querySelector('[data-certificate="1"]')?.textContent).toContain('leaf');
    expect(bundle?.querySelector('[data-certificate="2"]')?.textContent).toContain('intermediate');
  });

  it('draws the grade the server computed, and computes none of its own', async () => {
    // Graded against the certificate's own lifetime: cert-manager renews at two
    // thirds elapsed, so 30 days left on a 90-day certificate is healthy and 30
    // on a one-year one is three months late. A client holding only `notAfter`
    // cannot tell those apart - which is exactly what these two are, since the
    // 8-day one is `warn` and the 368-day one is `ok`.
    const container = await mount();
    const tones = [...container.querySelectorAll('[data-expiry-tone]')].map((node) => ({
      tone: node.getAttribute('data-expiry-tone'),
      text: node.textContent,
    }));
    expect(tones).toEqual([
      { tone: 'ok', text: 'expires in 61d4h' },
      { tone: 'ok', text: 'expires in 368d' },
      { tone: 'warn', text: 'expires in 8d' },
    ]);
  });

  it('puts a bundle’s refusal beside what did parse, never instead of it', async () => {
    // A bundle whose second block did not parse says so
    // beside the first. Drawing the note alone says nothing was read.
    const container = await mount();
    const bundle = container.querySelector('[data-bundle="ca.crt"]');
    expect(bundle?.querySelector('[data-bundle-note]')?.textContent).toContain(
      'certificate request',
    );
    expect(bundle?.querySelectorAll('[data-certificate]')).toHaveLength(1);
  });

  it('says why there is no bundle at all, which is a different sentence', async () => {
    // An object that promised a certificate and has none is not an object with no
    // certificates.
    const container = await mount({
      certificates: { note: 'this Secret is typed kubernetes.io/tls and carries no tls.crt' },
    });
    expect(container.querySelector('[data-certificates-note]')?.textContent).toContain(
      'no tls.crt',
    );
    expect(container.querySelector('[data-bundle]')).toBeNull();
  });

  it('names the private key and offers nothing that would read it', async () => {
    // "tls.key is in this Secret and this console does not read it" - the difference
    // between a console that cannot and one that chose not to.
    const container = await mount();
    const said = container.querySelector('[data-private-keys]');
    expect(said?.textContent).toContain('tls.key');
    expect(said?.querySelector('button')).toBeNull();
    // And no value of it anywhere on the panel.
    expect(container.querySelector('[data-section="certificates"]')?.querySelectorAll('button')).toHaveLength(0);
  });

  it('draws the SANs as chips and says how many were left out', async () => {
    // The list a user matches against the Ingress host they are debugging.
    const container = await mount();
    const sans = container.querySelector('[data-certificate="1"] [data-sans]');
    expect(sans?.textContent).toContain('shop.example');
    expect(sans?.textContent).toContain('www.shop.example');
    expect(container.querySelector('[data-sans-note]')?.textContent).toBe('3 more');
  });
});

describe('the addresses', () => {
  it('opens each in a new tab, safely', async () => {
    const container = await mount();
    const links = [...container.querySelectorAll('[data-section="addresses"] a')];
    expect(links).toHaveLength(2);
    for (const link of links) {
      expect(link.getAttribute('target')).toBe('_blank');
      expect(link.getAttribute('rel')).toBe('noopener noreferrer');
    }
  });

  it('labels the scheme from the object’s own TLS block, not from the string', async () => {
    const container = await mount();
    const rows = [...container.querySelectorAll('[data-address]')].map((n) => n.textContent ?? '');
    expect(rows[0]).toContain('https');
    expect(rows[1]).toContain('http');
    expect(rows[1]).not.toContain('https');
  });

  it('says where the address is not literally what the object writes', async () => {
    // A path pattern whose literal beginning is all that can be opened.
    const container = await mount();
    expect(container.querySelector('[data-address-note]')?.textContent).toContain('pattern');
    expect(container.querySelector('[data-urls-note]')?.textContent).toContain('resource backend');
  });

  it('draws an address that is not http or https as text, never as a link', async () => {
    // The host half of an Ingress address is the tenant's to write, so
    // the renderer refuses a scheme it does not open rather than trusting the
    // producer to have prefixed one.
    const container = await mount({
      urls: [{ address: 'javascript:alert(1)', tls: false } as ObjectURL],
    });
    const row = container.querySelector('[data-address]');
    expect(row?.textContent).toContain('javascript:alert(1)');
    expect(container.querySelector('[data-section="addresses"] a')).toBeNull();
  });

  it("links a detail field's reference only to a path here or an http address", async () => {
    // `ObjectDetail`'s one raw `href`. A path on this origin is
    // a link in place, an http or https address a link out in a new tab, and
    // anything else the text it is.
    const container = await mount({
      sections: [
        {
          key: 'summary',
          label: 'Summary',
          fields: [
            { label: 'Node', value: 'node-3', prose: false, pill: false, ref: { text: 'node-3', href: '/c/prod/nodes/node-3' } },
            { label: 'Docs', value: 'docs', prose: false, pill: false, ref: { text: 'docs', href: 'https://docs.example.com' } },
            { label: 'Bad', value: 'evil', prose: false, pill: false, ref: { text: 'evil', href: 'javascript:alert(1)' } },
          ],
        },
      ],
    });
    await waitFor(() => {
      expect(container.querySelector('a[href="/c/prod/nodes/node-3"]')).not.toBeNull();
    });
    expect(container.querySelector('a[href="/c/prod/nodes/node-3"]')?.getAttribute('target')).toBeNull();
    expect(container.querySelector('a[href="https://docs.example.com"]')?.getAttribute('target')).toBe('_blank');
    expect(container.querySelector('a[href^="javascript:"]')).toBeNull();
    expect(container.textContent).toContain('evil');
  });

  it('draws nothing at all where the object has no address', async () => {
    // Ingresses have them and nothing else does; inventing a scheme for a
    // LoadBalancer Service is the guess this refuses to make.
    const container = await mount({ urls: [], urlsNote: '' });
    expect(container.querySelector('[data-section="addresses"]')).toBeNull();
  });
});
