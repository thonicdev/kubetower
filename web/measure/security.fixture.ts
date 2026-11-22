import type { ExtensionCell } from '../ui/src/extension';
import { PAGE_ROWS, configKey, configPath, type Brief, type Compliance, type ConfigCounts, type Control, type Counts, type Cve, type Detail, type FindingRow, type Findings, type Overview, type Source, type Workload } from '../app/extras/security/data';

/**
 * Security Center's pages, answered with a small, invented cluster: the
 * `extra-security` and `extra-security-workload` scenes of `main.tsx`, which
 * `shot-extras.mjs` photographs for Settings > Extra. Typed against the
 * extra's own `data.ts`, so a field renamed there fails `tsc` here. Every date
 * is measured from the moment the module loads; the screenshot script freezes
 * the clock.
 */

const NOW = Date.now();
const HOUR = 3_600_000;
const iso = (ms: number): string => new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z');

const c = (critical: number, high: number, medium: number, low: number, unknown = 0): Counts => ({ critical, high, medium, low, unknown });
const add = (a: Counts, b: Counts): Counts => c(a.critical + b.critical, a.high + b.high, a.medium + b.medium, a.low + b.low, a.unknown + b.unknown);
const ZERO = c(0, 0, 0, 0);

/** The images, each with what Trivy found in it. */
const IMAGES: Readonly<Record<string, Counts>> = {
  'registry.example.com/shop/checkout-api:1.14.2': c(3, 11, 24, 31),
  'registry.example.com/shop/storefront:4.2.0': c(1, 6, 18, 22),
  'registry.example.com/shop/catalogue:2.3.1': c(0, 4, 12, 15),
  'registry.example.com/payments/ledger:0.9.7': c(2, 8, 9, 14),
  'registry.example.com/payments/gateway:3.1.0': c(0, 2, 7, 11),
  'nginx:1.21.6': c(4, 17, 33, 48),
  'postgres:13.4': c(2, 13, 21, 40),
  'redis:6.2.6': c(1, 5, 8, 19),
  'grafana/grafana:9.1.0': c(0, 7, 15, 9),
  'quay.io/prometheus/node-exporter:v1.3.1': c(0, 3, 6, 4),
  'quay.io/prometheus/prometheus:v2.37.0': c(0, 2, 5, 6),
  'registry.k8s.io/ingress-nginx/controller:v1.3.0': c(1, 9, 14, 12),
  'busybox:1.35': c(0, 0, 1, 2),
  'registry.example.com/tools/report-job:1.0.3': c(0, 1, 3, 7),
  'registry.k8s.io/coredns/coredns:v1.9.3': c(0, 1, 2, 3),
  'registry.example.com/shop/mailer:1.2.0': ZERO,
};

interface Spec {
  namespace: string;
  kind: string;
  name: string;
  images: string[];
  config?: Counts | null;
  secrets?: number | null;
  exposure?: string;
  hoursAgo?: number;
  unscanned?: boolean;
}

const SPECS: readonly Spec[] = [
  { namespace: 'shop', kind: 'Deployment', name: 'checkout-api', images: ['registry.example.com/shop/checkout-api:1.14.2', 'nginx:1.21.6'], config: c(0, 2, 3, 1), secrets: 1, exposure: 'Ingress shop/checkout', hoursAgo: 3 },
  { namespace: 'shop', kind: 'Deployment', name: 'storefront', images: ['registry.example.com/shop/storefront:4.2.0', 'nginx:1.21.6'], config: c(0, 1, 2, 2), secrets: 0, exposure: 'Ingress shop/www', hoursAgo: 5 },
  { namespace: 'ingress-nginx', kind: 'DaemonSet', name: 'ingress-nginx-controller', images: ['registry.k8s.io/ingress-nginx/controller:v1.3.0'], config: c(0, 1, 1, 0), secrets: 0, exposure: 'Service ingress-nginx/ingress-nginx-controller (LoadBalancer)', hoursAgo: 7 },
  { namespace: 'payments', kind: 'StatefulSet', name: 'ledger', images: ['registry.example.com/payments/ledger:0.9.7'], config: c(0, 0, 2, 1), secrets: 2, hoursAgo: 4 },
  { namespace: 'data', kind: 'StatefulSet', name: 'orders-db', images: ['postgres:13.4'], config: c(0, 1, 2, 0), secrets: 0, hoursAgo: 9 },
  { namespace: 'shop', kind: 'Deployment', name: 'catalogue', images: ['registry.example.com/shop/catalogue:2.3.1', 'redis:6.2.6'], config: c(0, 0, 1, 2), secrets: 0, hoursAgo: 6 },
  { namespace: 'payments', kind: 'Deployment', name: 'gateway', images: ['registry.example.com/payments/gateway:3.1.0', 'nginx:1.21.6'], config: c(0, 0, 2, 1), secrets: 0, exposure: 'Service payments/gateway (NodePort)', hoursAgo: 2 },
  { namespace: 'data', kind: 'StatefulSet', name: 'sessions-redis', images: ['redis:6.2.6'], config: c(0, 1, 0, 1), secrets: 0, hoursAgo: 11 },
  { namespace: 'monitoring', kind: 'Deployment', name: 'grafana', images: ['grafana/grafana:9.1.0'], config: c(0, 0, 1, 1), secrets: 0, exposure: 'Ingress monitoring/grafana', hoursAgo: 8 },
  { namespace: 'data', kind: 'StatefulSet', name: 'reports-db', images: ['postgres:13.4'], config: c(0, 1, 1, 0), secrets: 0, hoursAgo: 10 },
  { namespace: 'monitoring', kind: 'DaemonSet', name: 'node-exporter', images: ['quay.io/prometheus/node-exporter:v1.3.1'], config: c(0, 2, 1, 0), secrets: 0, hoursAgo: 12 },
  { namespace: 'monitoring', kind: 'StatefulSet', name: 'prometheus', images: ['quay.io/prometheus/prometheus:v2.37.0'], config: c(0, 0, 1, 1), secrets: 0, hoursAgo: 12 },
  { namespace: 'shop', kind: 'Deployment', name: 'storefront-preview', images: ['registry.example.com/shop/storefront:4.2.0'], config: c(0, 0, 1, 1), secrets: 0, hoursAgo: 14 },
  { namespace: 'tools', kind: 'CronJob', name: 'nightly-report', images: ['registry.example.com/tools/report-job:1.0.3'], config: c(0, 0, 2, 0), secrets: 1, hoursAgo: 20 },
  { namespace: 'kube-system', kind: 'Deployment', name: 'coredns', images: ['registry.k8s.io/coredns/coredns:v1.9.3'], config: null, secrets: null, hoursAgo: 22 },
  { namespace: 'tools', kind: 'Job', name: 'db-migrate-41', images: ['busybox:1.35'], config: c(0, 0, 1, 0), secrets: 0, hoursAgo: 30 },
  { namespace: 'shop', kind: 'Deployment', name: 'mailer', images: ['registry.example.com/shop/mailer:1.2.0'], config: c(0, 0, 0, 1), secrets: 0, hoursAgo: 15 },
  { namespace: 'tools', kind: 'Pod', name: 'debug-shell', images: ['busybox:1.35'], config: null, secrets: null, unscanned: true },
];

const workloads: Workload[] = SPECS.map((s) => {
  const counts = s.unscanned === true ? ZERO : s.images.reduce((sum, i) => add(sum, IMAGES[i] ?? ZERO), ZERO);
  return {
    namespace: s.namespace,
    kind: s.kind,
    name: s.name,
    images: s.images,
    counts,
    config: s.config ?? null,
    secrets: s.secrets ?? null,
    exposed: s.exposure !== undefined,
    ...(s.exposure === undefined ? {} : { exposure: s.exposure }),
    ...(s.unscanned === true ? {} : { scanned: iso(NOW - (s.hoursAgo ?? 1) * HOUR) }),
  };
});

const weight = (x: Counts) => x.critical * 1e9 + x.high * 1e6 + x.medium * 1e3 + x.low;
workloads.sort((a, b) => weight(b.counts) - weight(a.counts));

const images = Object.entries(IMAGES)
  .map(([image, counts]) => ({ image, counts, workloads: workloads.filter((w) => w.images.includes(image)).length }))
  .filter((x) => x.workloads > 0)
  .sort((a, b) => weight(b.counts) - weight(a.counts));

const namespaces = [...new Set(workloads.map((w) => w.namespace))]
  .map((namespace) => {
    const own = workloads.filter((w) => w.namespace === namespace);
    const distinct = [...new Set(own.flatMap((w) => (w.scanned === undefined ? [] : w.images)))];
    return { namespace, counts: distinct.reduce((sum, i) => add(sum, IMAGES[i] ?? ZERO), ZERO), workloads: own.length };
  })
  .sort((a, b) => weight(b.counts) - weight(a.counts));

const totals = images.reduce((sum, x) => add(sum, x.counts), ZERO);
const severe = (x: Counts) => x.critical + x.high > 0;

const SOURCES: Source[] = [
  { kind: 'vulnerabilityreports', state: 'read', complete: true, reports: 21 },
  { kind: 'configauditreports', state: 'read', complete: true, reports: 16 },
  { kind: 'exposedsecretreports', state: 'read', complete: true, reports: 16 },
];

export const SECURITY_OVERVIEW: Overview = {
  cluster: 'production',
  read: iso(NOW),
  operator: true,
  sources: SOURCES,
  totals,
  affected: {
    critical: workloads.filter((w) => w.counts.critical > 0).length,
    high: workloads.filter((w) => w.counts.high > 0).length,
    medium: workloads.filter((w) => w.counts.medium > 0).length,
  },
  imageCount: images.filter((x) => severe(x.counts)).length,
  exposedCount: workloads.filter((w) => w.exposed && severe(w.counts)).length,
  secretCount: workloads.filter((w) => (w.secrets ?? 0) > 0).length,
  namespaces,
  images,
  workloads,
  total: workloads.length,
  capped: false,
};

/** The first page's distinct failed checks, as `/config` answers them: the
 *  overview's figures, which here count no check twice. */
const audited = workloads.slice(0, PAGE_ROWS).filter((w) => w.config !== null);
export const SECURITY_CONFIG_PATH = configPath('production', audited);
export const SECURITY_CONFIG: ConfigCounts = Object.fromEntries(audited.map((w) => [configKey(w), w.config ?? null]));

const API = 'registry.example.com/shop/checkout-api:1.14.2';
const NGINX = 'nginx:1.21.6';

function cve(id: string, severity: Cve['severity'], score: number | undefined, pkg: string, installed: string, fixed: string, title: string, container: string): Cve {
  return { id, severity, ...(score === undefined ? {} : { score }), pkg, installed, fixed, title, link: `https://avd.aquasec.com/nvd/${id.toLowerCase()}`, container };
}

const KNOWN: Cve[] = [
  cve('CVE-2023-44487', 'CRITICAL', 7.5, 'golang.org/x/net', 'v0.7.0', '0.17.0', 'HTTP/2 rapid reset can cause excessive work', 'api'),
  cve('CVE-2022-37434', 'CRITICAL', 9.8, 'zlib1g', '1:1.2.11.dfsg-2', '1:1.2.11.dfsg-2+deb11u2', 'heap-based buffer over-read in inflate', 'proxy'),
  cve('CVE-2023-38545', 'CRITICAL', 9.8, 'libcurl4', '7.74.0-1.3+deb11u2', '7.74.0-1.3+deb11u10', 'SOCKS5 heap buffer overflow', 'api'),
  cve('CVE-2022-1292', 'CRITICAL', 9.8, 'openssl', '1.1.1n-0+deb11u1', '1.1.1n-0+deb11u2', 'c_rehash script allows command injection', 'proxy'),
  cve('CVE-2023-4911', 'HIGH', 7.8, 'libc6', '2.31-13+deb11u3', '2.31-13+deb11u7', 'buffer overflow in ld.so', 'proxy'),
  cve('CVE-2024-6387', 'HIGH', 8.1, 'openssh-client', '1:8.4p1-5', '1:8.4p1-5+deb11u3', 'regreSSHion: signal handler race condition', 'api'),
  cve('CVE-2022-41723', 'HIGH', 7.5, 'golang.org/x/net', 'v0.7.0', '0.7.0', 'avoid quadratic complexity in HPACK decoding', 'api'),
  cve('CVE-2023-39325', 'HIGH', 7.5, 'golang.org/x/net', 'v0.7.0', '0.17.0', 'rapid stream resets can cause excessive work', 'api'),
  cve('CVE-2022-2068', 'HIGH', 9.8, 'libssl1.1', '1.1.1n-0+deb11u1', '1.1.1n-0+deb11u3', 'the c_rehash script allows command injection', 'proxy'),
  cve('CVE-2023-0286', 'HIGH', 7.4, 'libssl1.1', '1.1.1n-0+deb11u1', '1.1.1n-0+deb11u4', 'X.400 address type confusion in X.509 GeneralName', 'proxy'),
  cve('CVE-2021-33560', 'HIGH', 7.5, 'libgcrypt20', '1.8.7-6', '', 'mishandles ElGamal encryption', 'proxy'),
  cve('CVE-2022-29458', 'HIGH', 7.1, 'ncurses-base', '6.2+20201114-2', '6.2+20201114-2+deb11u2', 'segmentation fault via out-of-bounds read', 'proxy'),
  cve('CVE-2023-29491', 'HIGH', 7.8, 'libtinfo6', '6.2+20201114-2', '6.2+20201114-2+deb11u2', 'local users can trigger memory corruption', 'proxy'),
  cve('CVE-2023-45853', 'HIGH', 9.8, 'zlib1g', '1:1.2.11.dfsg-2', '', 'integer overflow in zipOpenNewFileInZip4_6', 'proxy'),
  cve('CVE-2022-3715', 'HIGH', 7.8, 'bash', '5.1-2+b3', '5.1-2+deb11u1', 'heap buffer overflow in valid_parameter_transform', 'proxy'),
];

const SCORE: Readonly<Record<string, number>> = { CRITICAL: 9.1, HIGH: 7.2, MEDIUM: 5.3 };

function filler(count: number, severity: Cve['severity'], from: number): Cve[] {
  const pkgs = ['libxml2', 'libsqlite3-0', 'perl-base', 'libtasn1-6', 'libsystemd0', 'tar', 'libgnutls30', 'libexpat1', 'libpcre2-8-0', 'login'];
  return Array.from({ length: count }, (_, i) => {
    const pkg = pkgs[i % pkgs.length] ?? 'libc-bin';
    const fixed = i % 3 === 0 ? '' : `${String(2 + (i % 4))}.${String(i % 9)}.1-1+deb11u${String(1 + (i % 5))}`;
    return cve(`CVE-202${String(1 + (i % 4))}-${String(from + i * 37)}`, severity, SCORE[severity] === undefined ? undefined : (SCORE[severity] ?? 0) + (i % 5) / 10, pkg, `${String(2 + (i % 4))}.${String(i % 9)}.0-1`, fixed, `${pkg}: issue ${String(i + 1)}`, i % 2 === 0 ? 'proxy' : 'api');
  });
}

const checkout = workloads.find((w) => w.name === 'checkout-api');

/** As many as the two images count: 7 critical, 28 high, 57 medium, 79 low. */
const CVES: Cve[] = [
  ...KNOWN.filter((x) => x.severity === 'CRITICAL'),
  ...filler(3, 'CRITICAL', 31_000),
  ...KNOWN.filter((x) => x.severity === 'HIGH'),
  ...filler(17, 'HIGH', 32_200),
  ...filler(57, 'MEDIUM', 20_100),
  ...filler(79, 'LOW', 41_300),
];

export const SECURITY_CHECKOUT: Detail = {
  cluster: 'production',
  read: iso(NOW),
  workload: { namespace: 'shop', kind: 'Deployment', name: 'checkout-api' },
  sources: SOURCES,
  containers: [
    { container: 'api', image: API, os: 'debian 11.6', scanned: checkout?.scanned ?? iso(NOW), counts: IMAGES[API] ?? ZERO },
    { container: 'proxy', image: NGINX, os: 'debian 10.12', eosl: true, scanned: checkout?.scanned ?? iso(NOW), counts: IMAGES[NGINX] ?? ZERO },
  ],
  exposure: ['Ingress shop/checkout', 'Service shop/checkout-api (ClusterIP)'],
  cves: CVES,
  cvesTotal: CVES.length,
  checks: [
    { id: 'KSV001', title: 'Process can elevate its own privileges', severity: 'MEDIUM', category: 'Kubernetes Security Check' },
    { id: 'KSV012', title: 'Runs as root user', severity: 'MEDIUM', category: 'Kubernetes Security Check' },
    { id: 'KSV014', title: 'Root file system is not read-only', severity: 'HIGH', category: 'Kubernetes Security Check' },
    { id: 'KSV017', title: 'Privileged container', severity: 'HIGH', category: 'Kubernetes Security Check' },
    { id: 'KSV020', title: 'Runs with UID <= 10000', severity: 'LOW', category: 'Kubernetes Security Check' },
    { id: 'KSV106', title: 'Container capabilities must only include NET_BIND_SERVICE', severity: 'MEDIUM', category: 'Kubernetes Security Check' },
  ],
  secrets: [{ rule: 'aws-access-key-id', title: 'AWS Access Key ID', category: 'AWS', severity: 'CRITICAL', target: '/app/config/.env' }],
};

/* -------------------------------------------------------------------------- */
/* The panel and the lists                                                     */
/* -------------------------------------------------------------------------- */

/** `/brief` of the checkout-api Deployment: the tab's and the tile's answer.
 *  The tile names the first container's image. */
export const SECURITY_CHECKOUT_BRIEF: Brief = {
  scanned: true,
  workload: { namespace: 'shop', kind: 'Deployment', name: 'checkout-api' },
  counts: checkout?.counts ?? ZERO,
  image: API,
  href: '/c/production/x/security/shop/Deployment/checkout-api',
};

/** One cell as `/cells` draws it: the worst level, every count and the image in the hint. */
function cell(cluster: string, ref: { namespace: string; kind: string; name: string }, counts: Counts, image: string): ExtensionCell {
  const text = counts.critical > 0 ? `${String(counts.critical)} critical` : counts.high > 0 ? `${String(counts.high)} high` : 'clean';
  const tone = counts.critical > 0 ? 'bad' : counts.high > 0 ? 'warn' : 'ok';
  const hint = `${String(counts.critical)} critical · ${String(counts.high)} high · ${String(counts.medium)} medium · ${String(counts.low)} low · ${image}`;
  return { text, tone, hint, href: `/c/${cluster}/x/security/${ref.namespace}/${ref.kind}/${ref.name}` };
}

/** `/cells?kind=pods` for the Pods list of the console's own scene
 *  (`pods.fixture.ts`): each pod bound to the workload that runs it, one
 *  pod left unscanned so its cell stays empty. */
export function securityPodCells(cluster: string): Readonly<Record<string, ExtensionCell>> {
  return {
    'observability/prometheus-0': cell(cluster, { namespace: 'observability', kind: 'StatefulSet', name: 'prometheus' }, IMAGES['quay.io/prometheus/prometheus:v2.37.0'] ?? ZERO, 'quay.io/prometheus/prometheus:v2.37.0'),
    'payments/ledger-api-7d9f8b6c4-x2k9p': cell(cluster, { namespace: 'payments', kind: 'Deployment', name: 'ledger-api' }, IMAGES['registry.example.com/payments/ledger:0.9.7'] ?? ZERO, 'registry.example.com/payments/ledger:0.9.7'),
    'payments/ledger-api-7d9f8b6c4-qw4tz': cell(cluster, { namespace: 'payments', kind: 'Deployment', name: 'ledger-api' }, IMAGES['registry.example.com/payments/ledger:0.9.7'] ?? ZERO, 'registry.example.com/payments/ledger:0.9.7'),
    'shop-prod/storefront-web-6f8d7c9b5-lm2xr': cell(cluster, { namespace: 'shop-prod', kind: 'Deployment', name: 'storefront-web' }, ZERO, 'ghcr.io/example/storefront:4.2.0'),
  };
}

/** The cluster's own reports: its components, cluster-scoped objects, control
 *  plane and nodes, as `GET /cluster` answers them, the worst first. */
const clusterRows: FindingRow[] = [
  { source: 'clustervulnerabilityreports', kind: 'Node', name: 'kubelet', image: 'k8s.io/kubelet:1.29.4', counts: c(1, 6, 9, 3), scanned: iso(NOW - 2 * HOUR) },
  { source: 'clustervulnerabilityreports', kind: 'Pod', namespace: 'kube-system', name: 'kube-apiserver', image: 'k8s.io/kube-apiserver:1.29.4', counts: c(1, 4, 7, 2), scanned: iso(NOW - 2 * HOUR) },
  { source: 'clustervulnerabilityreports', kind: 'Pod', namespace: 'kube-system', name: 'etcd', image: 'k8s.io/etcd:3.5.12', counts: c(0, 3, 5, 1), scanned: iso(NOW - 2 * HOUR) },
  { source: 'infraassessmentreports', kind: 'Pod', namespace: 'kube-system', name: 'kube-apiserver-cp-1', counts: c(0, 2, 3, 1), scanned: iso(NOW - 3 * HOUR) },
  { source: 'clusterinfraassessmentreports', kind: 'Node', name: 'worker-1', counts: c(0, 2, 1, 4), scanned: iso(NOW - 4 * HOUR) },
  { source: 'clusterinfraassessmentreports', kind: 'Node', name: 'worker-2', counts: c(0, 2, 1, 4), scanned: iso(NOW - 4 * HOUR) },
  { source: 'clusterconfigauditreports', kind: 'ClusterRole', name: 'ingress-nginx', counts: c(0, 1, 2, 0), scanned: iso(NOW - 6 * HOUR) },
  { source: 'clustervulnerabilityreports', kind: 'Pod', namespace: 'kube-system', name: 'coredns', image: 'k8s.io/coredns:1.11.1', counts: c(0, 1, 2, 0), scanned: iso(NOW - 2 * HOUR) },
  { source: 'infraassessmentreports', kind: 'Pod', namespace: 'kube-system', name: 'kube-scheduler-cp-1', counts: c(0, 0, 2, 1), scanned: iso(NOW - 3 * HOUR) },
  { source: 'clusterconfigauditreports', kind: 'CustomResourceDefinition', name: 'certificates.cert-manager.io', counts: c(0, 0, 0, 1), scanned: iso(NOW - 6 * HOUR) },
  { source: 'clusterinfraassessmentreports', kind: 'Node', name: 'cp-1', counts: c(0, 0, 0, 0), scanned: iso(NOW - 4 * HOUR) },
];

export const SECURITY_CLUSTER: Findings = {
  cluster: 'production',
  read: iso(NOW),
  sources: [
    { kind: 'clustervulnerabilityreports', state: 'read', complete: true, reports: 4 },
    { kind: 'clusterconfigauditreports', state: 'read', complete: true, reports: 2 },
    { kind: 'infraassessmentreports', state: 'read', complete: true, reports: 2 },
    { kind: 'clusterinfraassessmentreports', state: 'read', complete: true, reports: 3 },
  ],
  rows: clusterRows,
  totals: clusterRows.reduce((a, r) => add(a, r.counts), ZERO),
  total: clusterRows.length,
  capped: false,
  notes: [],
  byKind: { clustervulnerabilityreports: 4, clusterconfigauditreports: 2, infraassessmentreports: 2, clusterinfraassessmentreports: 3 },
};

/** Roles and ClusterRoles as trivy-operator's RBAC assessment found them. */
const rbacRows: FindingRow[] = [
  { source: 'clusterrbacassessmentreports', kind: 'ClusterRole', name: 'cluster-admin', counts: c(3, 2, 0, 0), scanned: iso(NOW - 5 * HOUR) },
  { source: 'clusterrbacassessmentreports', kind: 'ClusterRole', name: 'argocd-application-controller', counts: c(2, 1, 1, 0), scanned: iso(NOW - 5 * HOUR) },
  { source: 'rbacassessmentreports', kind: 'Role', namespace: 'shop', name: 'deployer', counts: c(1, 2, 0, 0), scanned: iso(NOW - 5 * HOUR) },
  { source: 'clusterrbacassessmentreports', kind: 'ClusterRole', name: 'ingress-nginx', counts: c(0, 2, 1, 0), scanned: iso(NOW - 5 * HOUR) },
  { source: 'rbacassessmentreports', kind: 'Role', namespace: 'payments', name: 'secret-reader', counts: c(0, 1, 0, 0), scanned: iso(NOW - 5 * HOUR) },
  { source: 'clusterrbacassessmentreports', kind: 'ClusterRole', name: 'view', counts: c(0, 0, 1, 0), scanned: iso(NOW - 5 * HOUR) },
  { source: 'rbacassessmentreports', kind: 'Role', namespace: 'monitoring', name: 'grafana', counts: c(0, 0, 0, 0), scanned: iso(NOW - 5 * HOUR) },
];

export const SECURITY_RBAC: Findings = {
  cluster: 'production',
  read: iso(NOW),
  sources: [
    { kind: 'rbacassessmentreports', state: 'read', complete: true, reports: 3 },
    { kind: 'clusterrbacassessmentreports', state: 'read', complete: true, reports: 4 },
  ],
  rows: rbacRows,
  totals: rbacRows.reduce((a, r) => add(a, r.counts), ZERO),
  total: rbacRows.length,
  capped: false,
  notes: [],
  byKind: { rbacassessmentreports: 3, clusterrbacassessmentreports: 4 },
};

const ctl = (id: string, name: string, severity: string, fail: number | null): Control => ({ id, name, severity, fail });
const nsa: Control[] = [
  ctl('1.0', 'Non-root containers', 'MEDIUM', 7),
  ctl('1.1', 'Immutable container file systems', 'LOW', 12),
  ctl('1.2', 'Preventing privileged containers', 'HIGH', 2),
  ctl('1.3', 'Share containers process namespaces', 'HIGH', 0),
  ctl('1.4', 'Share host process namespaces', 'HIGH', 0),
  ctl('1.5', 'Use the host network', 'HIGH', 1),
  ctl('1.6', 'Run with root privileges or with root group membership', 'LOW', 5),
  ctl('1.7', 'Restricts escalation to root privileges', 'MEDIUM', 6),
  ctl('1.8', 'Sets the SELinux context of the container', 'MEDIUM', 0),
  ctl('2.0', 'Pod and/or namespace Selectors', 'MEDIUM', 0),
  ctl('3.0', 'Use CNI plugin that supports NetworkPolicy API (Manual)', 'CRITICAL', null),
  ctl('4.0', 'Use ResourceQuota policies to limit resources', 'MEDIUM', 4),
];
const failedFirst = (cs: Control[]) => [...cs].sort((a, b) => Number((b.fail ?? 0) > 0) - Number((a.fail ?? 0) > 0));

export const SECURITY_COMPLIANCE: Compliance = {
  cluster: 'production',
  read: iso(NOW),
  source: { kind: 'clustercompliancereports', state: 'read', complete: true, reports: 2 },
  reports: [
    { name: 'k8s-pss-baseline-0.1', title: 'Kubernetes Pod Security Standards - Baseline', version: '0.1', pass: 9, fail: 2, updated: iso(NOW - 6 * HOUR), controlsTotal: 3,
      controls: [ctl('1', 'HostProcess', 'HIGH', 1), ctl('2', 'Host Namespaces', 'HIGH', 1), ctl('3', 'Privileged Containers', 'HIGH', 0)] },
    { name: 'nsa-1.0', title: 'National Security Agency - Kubernetes Hardening Guidance', version: '1.0', pass: 4, fail: 7, updated: iso(NOW - 6 * HOUR), controlsTotal: nsa.length, controls: failedFirst(nsa) },
  ],
};

/**
 * The page on a cluster without trivy-operator, and the console's install
 * route answering for it: Argo CD found in `argocd`, the Application not there
 * yet, and the plan with the Application exactly as the console writes it
 * (`internal/prereq`'s own test pins the same document). The `extra-security-install`
 * scene presses Install and photographs the dialog.
 */
export const SECURITY_ABSENT: Overview = {
  ...SECURITY_OVERVIEW,
  operator: false,
  sources: SOURCES.map((s) => ({ ...s, state: 'absent' as const, complete: true, reports: 0 })),
  workloads: [],
  namespaces: [],
  images: [],
  total: 0,
};

const TRIVY_CHART = { repo: 'https://aquasecurity.github.io/helm-charts', name: 'trivy-operator', version: '0.38.1', appVersion: '0.36.1', namespace: 'trivy-system' };

export const SECURITY_PREREQ = {
  state: 'ok',
  data: {
    key: 'trivy-operator',
    label: 'Trivy Operator',
    chart: TRIVY_CHART,
    argo: { state: 'found', namespace: 'argocd' },
    application: { state: 'absent', name: 'trivy-operator', namespace: 'argocd' },
    installable: true,
  },
};

const TRIVY_APPLICATION = `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  annotations:
    kubetower.io/why: Created from KubeTower's Security Center to install Trivy Operator
      on this cluster through Argo CD. Delete this Application to stop Argo reconciling
      it; that alone does not uninstall the chart.
  name: trivy-operator
  namespace: argocd
spec:
  destination:
    namespace: trivy-system
    server: https://kubernetes.default.svc
  project: default
  source:
    chart: trivy-operator
    repoURL: https://aquasecurity.github.io/helm-charts
    targetRevision: 0.38.1
  syncPolicy:
    automated:
      prune: false
      selfHeal: false
    syncOptions:
    - CreateNamespace=true
    - ServerSideApply=true
`;

const HEADLINE = 'Creates the Argo CD Application trivy-operator in argocd, which installs trivy-operator 0.38.1 into trivy-system.';

export const SECURITY_INSTALL_PLAN = {
  token: 'install-plan',
  expiresAt: iso(NOW + 2 * 60_000),
  sentence: HEADLINE,
  facts: {
    verb: 'install',
    targets: [{ kind: 'applications.argoproj.io', namespace: 'argocd', name: 'trivy-operator' }],
    count: 1,
    destructive: false,
    irreversible: false,
    headline: HEADLINE,
    button: 'Install trivy-operator 0.38.1',
    request: TRIVY_APPLICATION,
    confirmation: { kind: 'name', expected: 'trivy-operator', label: 'Type trivy-operator to install it' },
  },
};
