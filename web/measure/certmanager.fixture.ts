import type { Acme, Certificate, Chain, Issuer, ObjectAnswer, Overview } from '../app/extras/certmanager/data';

/**
 * Cert Manager's page, answered with a small, invented cluster: the
 * `extra-certmanager` scene of `main.tsx`, which `shot-extras.mjs` photographs
 * for Settings > Extra's preview. Typed against the extra's own `data.ts`, so a
 * field renamed there fails `tsc` here. Every date is measured from the moment
 * the module loads; the screenshot script freezes the clock.
 */

const NOW = Date.now();
const DAY = 86_400_000;
const iso = (ms: number): string => new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z');

function band(days: number | null): Certificate['band'] {
  if (days === null) return 'none';
  if (days < 0) return 'expired';
  return days < 7 ? 'week' : days < 30 ? 'month' : 'later';
}

function cert(namespace: string, name: string, days: number | null, over: Partial<Certificate> = {}): Certificate {
  const notAfter = days === null ? '' : iso(NOW + days * DAY + 3 * 3_600_000);
  return {
    namespace,
    name,
    secretName: `${name}-tls`,
    dnsNames: [`${name.replace(/-tls$/, '')}.example.com`],
    moreNames: 0,
    issuerKind: 'ClusterIssuer',
    issuerName: 'letsencrypt',
    notAfter,
    renewalTime: days === null ? '' : iso(NOW + (days - 30) * DAY),
    daysLeft: days === null ? null : days < 0 ? days - 1 : days,
    band: band(days),
    ready: days === null || days < 0 ? 'False' : 'True',
    reason: days === null ? 'DoesNotExist' : days < 0 ? 'Expired' : 'Ready',
    message: days === null ? 'Issuing certificate as Secret does not exist' : 'Certificate is up to date and has not expired',
    renewalOverdue: days !== null && days >= 0 && days < 29,
    failedAttempts: 0,
    ...over,
  };
}

const CERTS: Certificate[] = [
  cert('payments', 'checkout-api', -2, { message: 'Certificate expired 2 days ago', failedAttempts: 6 }),
  cert('shop', 'storefront', 3, { failedAttempts: 2 }),
  cert('shop', 'images', 5),
  cert('payments', 'invoices', 12),
  cert('identity', 'login', 18, { issuerName: 'internal-ca', issuerKind: 'Issuer', renewalOverdue: false }),
  cert('shop', 'search', 26, { renewalOverdue: false }),
  cert('platform', 'grafana', null, { issuerName: 'internal-ca', issuerKind: 'Issuer' }),
  cert('platform', 'registry', null, { reason: 'Failed', message: 'The certificate request has failed to complete and will be retried' }),
  ...[34, 41, 41, 47, 52, 55, 58, 61, 61, 61, 66, 70, 73, 77, 79, 84, 86, 88, 120, 160, 210, 300].map((d, i) =>
    cert(['shop', 'payments', 'identity', 'platform', 'analytics'][i % 5] ?? 'shop', `service-${String(i + 1).padStart(2, '0')}`, d, {
      dnsNames: [`svc${String(i + 1)}.example.com`, `www.svc${String(i + 1)}.example.com`],
      renewalOverdue: false,
    }),
  ),
];

const strip = Array.from({ length: 90 }, (_, day) => CERTS.filter((c) => c.daysLeft === day && c.band !== 'expired').length);

const ISSUERS: Issuer[] = [
  {
    kind: 'Issuer',
    namespace: 'identity',
    name: 'internal-ca',
    type: 'ca',
    ready: 'False',
    reason: 'ErrGetKeyPair',
    message: 'Error getting keypair for CA issuer: secrets "internal-ca-root" not found',
    broken: true,
  },
  { kind: 'ClusterIssuer', name: 'letsencrypt', type: 'acme', ready: 'True', reason: 'ACMEAccountRegistered', message: '', broken: false },
  { kind: 'ClusterIssuer', name: 'selfsigned', type: 'selfSigned', ready: 'True', reason: 'IsReady', message: '', broken: false },
];

const ACME: Acme[] = [
  {
    kind: 'Challenge',
    namespace: 'shop',
    name: 'storefront-1-2154961-0',
    state: 'pending',
    reason: 'Waiting for HTTP-01 challenge propagation: failed to perform self check GET request: context deadline exceeded',
    type: 'HTTP-01',
    dnsName: 'storefront.example.com',
    certificate: 'storefront',
  },
  {
    kind: 'Order',
    namespace: 'platform',
    name: 'registry-1-3950941',
    state: 'errored',
    reason: 'Failed to create Order: 400 urn:ietf:params:acme:error:rejectedIdentifier',
    type: '',
    dnsName: 'registry.example.com',
    certificate: 'registry',
  },
];

const count = (f: (c: Certificate) => boolean): number => CERTS.filter(f).length;

export const CERTMANAGER_OVERVIEW: Overview = {
  installed: true,
  namespace: '',
  now: iso(NOW),
  complete: true,
  notes: [],
  counts: {
    total: CERTS.length,
    expired: count((c) => c.band === 'expired'),
    week: count((c) => c.band === 'week'),
    month: count((c) => c.band === 'week' || c.band === 'month'),
    notReady: count((c) => c.ready !== 'True'),
    renewalOverdue: count((c) => c.renewalOverdue),
    acmeFailing: ACME.length,
    issuersBroken: ISSUERS.filter((i) => i.broken).length,
  },
  ring: { ready: count((c) => c.ready === 'True'), notReady: count((c) => c.ready === 'False'), unknown: 0 },
  strip,
  certificates: CERTS,
  shown: CERTS.length,
  issuers: { state: 'read', items: ISSUERS, total: ISSUERS.length },
  acme: { state: 'read', items: ACME, inFlight: 5 },
};

/** The notes a person who may list in one namespace only is given, one per kind read. */
const NARROWED_NOTES = ['certificates', 'issuers', 'orders', 'challenges'].map(
  (kind) => `You may not list ${kind}.cert-manager.io in every namespace, so this holds only the namespaces where you may.`,
);

const SHOP_ISSUERS: Issuer[] = [
  {
    kind: 'Issuer',
    namespace: 'shop',
    name: 'ca-broken',
    type: 'ca',
    ready: 'False',
    reason: 'ErrGetKeyPair',
    message: 'Error getting keypair for CA issuer: secrets "missing-ca-secret" not found',
    broken: true,
  },
  {
    kind: 'Issuer',
    namespace: 'shop',
    name: 'acme-failing',
    type: 'acme',
    ready: 'False',
    reason: 'ErrRegisterACMEAccount',
    message: 'Failed to register ACME account: dial tcp 192.0.2.1:14000: i/o timeout',
    broken: true,
  },
];

const shopOnly = (issuers: Overview['issuers']): Overview => ({
  ...CERTMANAGER_OVERVIEW,
  namespace: '',
  complete: false,
  notes: NARROWED_NOTES,
  counts: { ...CERTMANAGER_OVERVIEW.counts, issuersBroken: issuers.state === 'read' ? SHOP_ISSUERS.length : 0 },
  issuers,
});

/** A person with `view` in one namespace, before: the ClusterIssuers refused turned the whole tile into a dash. */
export const CERTMANAGER_ISSUERS_BEFORE: Overview = shopOnly({
  state: 'refused',
  message: 'The cluster says you may not list the ClusterIssuers here, so they are not shown.',
  items: SHOP_ISSUERS,
  total: SHOP_ISSUERS.length,
});

/** The same person, after: the Issuers of their namespace are counted, the ClusterIssuers named as left out. */
export const CERTMANAGER_ISSUERS_AFTER: Overview = {
  ...shopOnly({ state: 'read', items: SHOP_ISSUERS, total: SHOP_ISSUERS.length }),
  notes: [...NARROWED_NOTES, 'The cluster says you may not list the ClusterIssuers here, so they are not shown.'],
};

/** A person holding `use` and nothing on cert-manager.io, as the route answered before: a 200 with nothing in it. */
export const CERTMANAGER_NOBODY_BEFORE: Overview = {
  ...CERTMANAGER_OVERVIEW,
  complete: false,
  notes: NARROWED_NOTES,
  counts: { total: 0, expired: 0, week: 0, month: 0, notReady: 0, renewalOverdue: 0, acmeFailing: 0, issuersBroken: 0 },
  ring: { ready: 0, notReady: 0, unknown: 0 },
  strip: Array<number>(90).fill(0),
  certificates: [],
  shown: 0,
  issuers: { state: 'refused', message: 'The cluster says you may not list the Issuers here, so they are not shown.', items: [], total: 0 },
  acme: { state: 'refused', message: 'The cluster says you may not list the Orders here, so they are not shown.', items: [], inFlight: 0 },
};

const AT = (minutes: number): string => iso(NOW - minutes * 60_000);

/**
 * The Certificate tab of the Ingress `shop/front`, which serves two
 * certificates: one whose challenge cannot be reached, and one whose request
 * was denied, its issuer unreadable for this person.
 */
const TAB_CHAINS: Record<string, Chain> = {
  storefront: {
    secret: 'storefront-tls',
    stuck: 'Challenge',
    request: {
      kind: 'CertificateRequest',
      state: 'found',
      namespace: 'shop',
      name: 'storefront-2',
      tone: 'warn',
      phase: 'approved',
      reason: 'Pending',
      created: AT(190),
      changed: AT(189),
      conditions: [
        { type: 'Approved', status: 'True', reason: 'cert-manager.io' },
        { type: 'Ready', status: 'False', reason: 'Pending', message: 'Waiting on certificate issuance from order shop/storefront-2-1: "pending"' },
      ],
    },
    order: { kind: 'Order', state: 'found', namespace: 'shop', name: 'storefront-2-1', tone: 'warn', phase: 'pending', dnsName: 'storefront.example.com', created: AT(189) },
    challenges: [
      {
        kind: 'Challenge',
        state: 'found',
        namespace: 'shop',
        name: 'storefront-2-1-4417',
        tone: 'bad',
        phase: 'pending',
        reason: "Waiting for HTTP-01 challenge propagation: wrong status code '404', expected '200'",
        type: 'HTTP-01',
        dnsName: 'storefront.example.com',
        created: AT(189),
      },
      {
        kind: 'Challenge',
        state: 'found',
        namespace: 'shop',
        name: 'storefront-2-1-9120',
        tone: 'ok',
        phase: 'valid',
        type: 'DNS-01',
        dnsName: 'www.storefront.example.com',
        created: AT(189),
      },
    ],
    moreChallenges: 0,
    issuer: { kind: 'ClusterIssuer', state: 'found', name: 'letsencrypt', tone: 'ok', phase: 'ready', type: 'acme' },
  },
  images: {
    secret: 'images-tls',
    stuck: 'CertificateRequest',
    request: {
      kind: 'CertificateRequest',
      state: 'found',
      namespace: 'shop',
      name: 'images-1',
      tone: 'bad',
      phase: 'denied',
      reason: 'PolicyDenied',
      message: 'the policy forbids a wildcard name',
      created: AT(75),
      changed: AT(75),
      conditions: [
        { type: 'Approved', status: 'False' },
        { type: 'Denied', status: 'True', reason: 'PolicyDenied', message: 'the policy forbids a wildcard name' },
      ],
    },
    order: { kind: 'Order', state: 'none', tone: 'neutral' },
    challenges: [],
    moreChallenges: 0,
    issuer: { kind: 'ClusterIssuer', state: 'refused', name: 'letsencrypt', tone: 'neutral' },
  },
};

export const CERTMANAGER_TAB: ObjectAnswer = {
  now: CERTMANAGER_OVERVIEW.now,
  certificates: [CERTS[1] as Certificate, CERTS[2] as Certificate].map((c, i) => ({ ...c, name: i === 0 ? 'storefront' : 'images' })),
  chains: TAB_CHAINS,
};
