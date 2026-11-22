import type { Brief, Detail, Overview, Workload } from '../app/extras/security/data';
import type { Component, Components, Sbom, Signature, Tally, Verdict } from '../app/extras/security/supply';

/**
 * Security Center's supply chain, over the invented cluster of
 * `security.fixture.ts`: what the SBOM reports count per image, the verdict
 * the admission wrote on the pods, and the checkout-api's SBOM as its tab
 * reads it. Typed against the extra's own `supply.ts` and `data.ts`.
 */

/** Components per image, as the SBOM reports' columns count them. */
const COMPONENTS: Readonly<Record<string, number>> = {
  'registry.example.com/shop/checkout-api:1.14.2': 238,
  'registry.example.com/shop/storefront:4.2.0': 412,
  'registry.example.com/shop/catalogue:2.3.1': 196,
  'registry.example.com/payments/ledger:0.9.7': 167,
  'registry.example.com/payments/gateway:3.1.0': 143,
  'nginx:1.21.6': 143,
  'postgres:13.4': 171,
  'redis:6.2.6': 98,
  'grafana/grafana:9.1.0': 524,
  'registry.k8s.io/ingress-nginx/controller:v1.3.0': 287,
  'registry.example.com/shop/mailer:1.2.0': 88,
};

/** The admission's verdict per image, where Kyverno verifies it. */
const VERDICTS: Readonly<Record<string, Verdict>> = {
  'registry.example.com/shop/checkout-api:1.14.2': 'pass',
  'registry.example.com/shop/storefront:4.2.0': 'pass',
  'registry.example.com/shop/catalogue:2.3.1': 'pass',
  'registry.example.com/payments/ledger:0.9.7': 'pass',
  'registry.example.com/payments/gateway:3.1.0': 'fail',
  'registry.example.com/shop/mailer:1.2.0': 'pass',
  'nginx:1.21.6': 'skip',
  'redis:6.2.6': 'fail',
};

const RANK: Readonly<Record<Verdict, number>> = { pass: 1, skip: 2, fail: 3, unreadable: 0 };

function components(w: Workload): Components | null {
  const images = w.images.filter((i) => COMPONENTS[i] !== undefined);
  if (images.length === 0) return null;
  const per = images.map((image, k) => ({ container: k === 0 ? 'app' : 'proxy', image, components: COMPONENTS[image] ?? 0, dependencies: Math.round((COMPONENTS[image] ?? 0) * 0.9) }));
  return { total: per.reduce((s, i) => s + i.components, 0), dependencies: per.reduce((s, i) => s + i.dependencies, 0), images: per };
}

function signature(w: Workload): Signature | null {
  // Only the shop's and payments' namespaces are under the verification policy.
  if (w.namespace !== 'shop' && w.namespace !== 'payments') return null;
  const images = w.images.map((image) => ({ image, verdict: VERDICTS[image] ?? 'skip' }));
  const verdict = images.reduce<Verdict>((worst, i) => (RANK[i.verdict] > RANK[worst] ? i.verdict : worst), 'pass');
  return { verdict, images };
}

/** The page's overview with the two sources, the two counts and each row's cells. */
export function withSupply(o: Overview): Overview {
  const workloads = o.workloads.map((w) => ({ ...w, components: components(w), signature: signature(w) }));
  return {
    ...o,
    workloads,
    sbom: { kind: 'sbomreports', state: 'read', complete: true, reports: 19 },
    signatures: { kind: 'signatures', state: 'read', complete: true, reports: 14 },
    signatureFailed: workloads.filter((w) => w.signature?.verdict === 'fail').length,
    verified: workloads.filter((w) => w.signature?.verdict === 'pass').length,
  };
}

const CHECKOUT = 'registry.example.com/shop/checkout-api:1.14.2';
const NGINX = 'nginx:1.21.6';
const CHECKOUT_SIGNATURE: Signature = {
  verdict: 'skip',
  images: [
    { image: NGINX, verdict: 'skip' },
    { image: CHECKOUT, verdict: 'pass' },
  ],
};

export const withSignature = (d: Detail): Detail => ({ ...d, signature: CHECKOUT_SIGNATURE });
export const withBriefSignature = (b: Brief): Brief => ({ ...b, signature: { verdict: 'pass', images: [{ image: CHECKOUT, verdict: 'pass' }] } });

/** The checkout-api's components: its Go modules and Debian packages, then nginx's. */
const GO = [
  ['github.com/aws/aws-sdk-go-v2', 'v1.30.3', 'Apache-2.0'],
  ['github.com/go-chi/chi/v5', 'v5.0.12', 'MIT'],
  ['github.com/golang-jwt/jwt/v5', 'v5.2.1', 'MIT'],
  ['github.com/google/uuid', 'v1.6.0', 'BSD-3-Clause'],
  ['github.com/jackc/pgx/v5', 'v5.6.0', 'MIT'],
  ['github.com/prometheus/client_golang', 'v1.19.1', 'Apache-2.0'],
  ['github.com/redis/go-redis/v9', 'v9.5.3', 'BSD-2-Clause'],
  ['github.com/stripe/stripe-go/v76', 'v76.25.0', 'MIT'],
  ['go.opentelemetry.io/otel', 'v1.28.0', 'Apache-2.0'],
  ['golang.org/x/crypto', 'v0.24.0', 'BSD-3-Clause'],
  ['golang.org/x/net', 'v0.7.0', 'BSD-3-Clause'],
  ['google.golang.org/grpc', 'v1.64.0', 'Apache-2.0'],
  ['google.golang.org/protobuf', 'v1.34.2', 'BSD-3-Clause'],
] as const;
const DEB = [
  ['base-files', '11.1+deb11u7', 'GPL-2.0-or-later'],
  ['bash', '5.1-2+b3', 'GPL-3.0-or-later'],
  ['ca-certificates', '20210119', 'MPL-2.0'],
  ['coreutils', '8.32-4+b1', 'GPL-3.0-or-later'],
  ['libc6', '2.31-13+deb11u3', 'LGPL-2.1-or-later'],
  ['libcurl4', '7.74.0-1.3+deb11u2', 'curl'],
  ['libgcrypt20', '1.8.7-6', 'LGPL-2.1-or-later'],
  ['libssl1.1', '1.1.1n-0+deb11u1', 'OpenSSL'],
  ['libsystemd0', '247.3-7+deb11u1', 'LGPL-2.1-or-later'],
  ['ncurses-base', '6.2+20201114-2', 'X11'],
  ['openssh-client', '1:8.4p1-5', 'BSD-2-Clause'],
  ['tzdata', '2024a-0+deb11u1', ''],
  ['zlib1g', '1:1.2.11.dfsg-2', 'Zlib'],
] as const;

function list(container: string, total: number, distro: string): Component[] {
  const out: Component[] = [];
  for (let i = 0; out.length < total; i += 1) {
    const go = container === 'api' && i % 3 !== 2;
    const pool = go ? GO : DEB;
    const [name, version, license] = pool[i % pool.length] ?? DEB[0];
    const round = Math.floor(i / pool.length);
    const n = round === 0 ? name : `${name}${go ? `/v${String(round + 1)}` : `-${String(round)}`}`;
    const purl = go ? `pkg:golang/${n}@${version}` : `pkg:deb/debian/${n}@${version}?arch=amd64&distro=${distro}`;
    out.push({ name: n, version, type: 'library', license, purl, container });
  }
  out[0] = { name: 'debian', version: distro.replace('debian-', ''), type: 'operating-system', license: '', container };
  if (container === 'api') out[1] = { name: 'checkout-api', version: '1.14.2', type: 'application', license: '', purl: 'pkg:golang/registry.example.com/shop/checkout-api@1.14.2', container };
  return out;
}

function tally(all: readonly Component[], key: (c: Component) => string[]): Tally[] {
  const m = new Map<string, number>();
  for (const c of all) for (const k of key(c)) m.set(k, (m.get(k) ?? 0) + 1);
  return [...m].map(([name, count]) => ({ name, count })).sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
}

const ALL = [...list('api', 238, 'debian-11.6'), ...list('proxy', 143, 'debian-10.12')].sort((a, b) => a.name.localeCompare(b.name) || a.container.localeCompare(b.container));

export const SECURITY_CHECKOUT_SBOM: Sbom = {
  cluster: 'production',
  read: new Date().toISOString().replace(/\.\d{3}Z$/, 'Z'),
  workload: { namespace: 'shop', kind: 'Deployment', name: 'checkout-api' },
  source: { kind: 'sbomreports', state: 'read', complete: true, reports: 2 },
  images: [
    { container: 'api', image: CHECKOUT, digest: 'sha256:9f2c4e8a1b7d3f60c5e2a9d48b1f7c3e6a0d5b2f8c4e1a7d9b3f6c0e2a5d8b1f4', components: 238, dependencies: 214 },
    { container: 'proxy', image: NGINX, digest: 'sha256:2bcabc23b45489fb0885d69a06ba1d648aeda973fae7bb981bafbb884165e514', components: 143, dependencies: 129 },
  ],
  components: ALL,
  total: ALL.length,
  types: tally(ALL, (c) => [c.type]),
  licenses: tally(ALL, (c) => (c.license === '' ? [''] : c.license.split(', '))),
  notes: [],
};
