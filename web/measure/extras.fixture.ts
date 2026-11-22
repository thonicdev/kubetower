import type { Application } from '../app/extras/argocd/data';
import type { Detail as CnpgDetail, Overview as CnpgOverview, PodAnswer as CnpgPod, Summary as CnpgSummary } from '../app/extras/cnpg/data';
import type { View as ArgoReport } from '../app/extras/argocd/report';
import type { Board, CronJob, HeatmapAnswer } from '../app/extras/kubecron/data';
import type { Page as AdjustPage, Row as AdjustRow } from '../app/extras/kubeadjust/data';
import {
  CERTMANAGER_ISSUERS_AFTER,
  CERTMANAGER_ISSUERS_BEFORE,
  CERTMANAGER_NOBODY_BEFORE,
  CERTMANAGER_OVERVIEW,
  CERTMANAGER_TAB,
} from './certmanager.fixture';
import { DNS_ANSWERS, DNS_FAILED, DNS_FORWARD_ANSWERS, DNS_NO_METRICS_ANSWERS, DNS_REFUSED } from './dns.fixture';
import { EVENTS_ANSWERS } from './events.fixture';
import { jvmClassHistogram } from './jvm-histogram.fixture';
import { jvmMemoryHistory } from './jvm-history.fixture';
import { jvmThreadDump } from './jvm-threads.fixture';
import type { Container as JvmContainer, Inspection as JvmInspection, Inventory as JvmInventory, PodAnswer as JvmPod } from '../app/extras/jvm/data';
import type { Deployment as RedisDeployment, InstanceAnswer, Inventory } from '../app/extras/redis/data';
import { BADGE_ANSWERS, CASES as BADGE_CASES } from './netinspect-badges.fixture';
import { NETINSPECT_ANSWERS, NETINSPECT_AT, NETINSPECT_NEW_ANSWERS, NETINSPECT_NEW_AT } from './netinspect.fixture';
import { PENDING_ANSWERS, PENDING_CHECKOUT, PENDING_NOMINATED_ANSWERS, PENDING_NOMINATED_AT, PENDING_POD, PENDING_POD_AT, PENDING_REFUSED, PENDING_WORKLOAD_ANSWERS } from './pending.fixture';
import { SECURITY_POLICIES, SECURITY_POLICY } from './policies.fixture';
import {
  SECURITY_ABSENT,
  SECURITY_CHECKOUT,
  SECURITY_CHECKOUT_BRIEF,
  SECURITY_CLUSTER,
  SECURITY_COMPLIANCE,
  SECURITY_CONFIG,
  SECURITY_CONFIG_PATH,
  SECURITY_INSTALL_PLAN,
  SECURITY_OVERVIEW,
  SECURITY_PREREQ,
  SECURITY_RBAC,
} from './security.fixture';
import { SECURITY_CHECKOUT_SBOM, withBriefSignature, withSignature, withSupply } from './sbom.fixture';

/**
 * The shipped extras' main pages, each answered with a small, tidy,
 * invented cluster: the `extra-<key>` scenes of `main.tsx`, which
 * `shot-extras.mjs` photographs for Settings > Extra's preview.
 *
 * **A picture, not a measurement.** Every body is typed against the extra's own
 * `data.ts`, so a field renamed there fails `tsc` here; nothing checks that the
 * Go routes still write this shape (the harness's limit 1).
 *
 * Every name is invented and neutral (shop, payments, checkout-api, …). Times
 * are measured back from the moment the module loads, so the page reads as
 * fresh whenever it is opened; the screenshot script freezes the clock, which
 * makes two runs draw the same pixels.
 */

export const EXTRA_CLUSTER = 'production';

/** A refusal the route writes: its status and its problem body. */
export class ExtraProblem {
  readonly status: number;
  readonly body: unknown;
  constructor(status: number, body: unknown) {
    this.status = status;
    this.body = body;
  }
}

/** An answer: a JSON body sent with 200, or an `ExtraProblem`. */
export type ExtraAnswer = unknown;

const NOW = Date.now();
const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;
const at = (msAgo: number): string => new Date(NOW - msAgo).toISOString();
const ahead = (msAhead: number): string => new Date(NOW + msAhead).toISOString();
const base = (key: string): string => `/x/${EXTRA_CLUSTER}/${key}/`;
/** The three spans of the DNS page, all answered the same. */
const dnsStats = (answer: unknown): Record<string, unknown> =>
  Object.fromEntries(['15m', '1h', '6h'].map((span) => [`${base('dns')}stats?span=${span}`, answer]));

/* -------------------------------------------------------------------------- */
/* Argo CD - the Delivery Center                                               */
/* -------------------------------------------------------------------------- */

const GIT = 'https://git.example.com/platform/deploy.git';

function app(
  project: string,
  namespace: string,
  name: string,
  over: Partial<Application> = {},
): Application {
  return {
    namespace: 'argocd',
    name,
    sync: 'Synced',
    health: 'Healthy',
    deleting: false,
    project,
    destination: { server: 'https://kubernetes.default.svc', namespace },
    outOfSyncCount: 0,
    source: { repoURL: GIT, path: `apps/${name}`, targetRevision: 'main' },
    revision: '4f9c2e1a7b3d5c8e9f0a1b2c3d4e5f6a7b8c9d0e',
    automatedSync: true,
    lastSync: { phase: 'Succeeded', startedAt: at(2 * HOUR + 40_000), finishedAt: at(2 * HOUR) },
    ...over,
  };
}

const ARGO_APPS: Application[] = [
  app('shop', 'shop', 'storefront'),
  app('shop', 'shop', 'checkout-api', {
    sync: 'OutOfSync',
    outOfSyncCount: 2,
    lastSync: { phase: 'Succeeded', startedAt: at(26 * HOUR), finishedAt: at(26 * HOUR - 30_000) },
  }),
  app('shop', 'shop', 'catalog-service'),
  app('shop', 'shop', 'search-indexer', {
    health: 'Progressing',
    healthMessage: 'Waiting for rollout to finish: 1 of 3 updated replicas are available...',
  }),
  app('payments', 'payments', 'payments-gateway'),
  app('payments', 'payments', 'ledger-worker', {
    health: 'Degraded',
    healthMessage: 'Deployment "ledger-worker" exceeded its progress deadline',
    lastSync: {
      phase: 'Failed',
      startedAt: at(35 * MINUTE),
      finishedAt: at(34 * MINUTE),
      message: 'one or more objects failed to apply: admission webhook denied the request',
    },
  }),
  app('payments', 'payments', 'fraud-scoring'),
  app('platform', 'ingress-nginx', 'ingress-nginx', {
    source: { repoURL: 'https://kubernetes.github.io/ingress-nginx', chart: 'ingress-nginx', targetRevision: '4.11.3' },
  }),
  app('platform', 'cert-manager', 'cert-manager', {
    source: { repoURL: 'https://charts.jetstack.io', chart: 'cert-manager', targetRevision: 'v1.16.1' },
  }),
  app('platform', 'observability', 'prometheus-stack', {
    source: { repoURL: 'https://prometheus-community.github.io/helm-charts', chart: 'kube-prometheus-stack', targetRevision: '65.1.0' },
  }),
];

const ARGO_REPORT: ArgoReport = {
  collected: at(0),
  state: 'present',
  allWell: false,
  sync: [],
  health: [],
  areas: [],
  offenders: [],
  causes: [],
  sets: [],
  setTotal: 2,
  setFailing: 0,
  projects: [
    { namespace: 'argocd', name: 'shop', description: 'Customer-facing storefront and checkout', applications: 4, offending: 2 },
    { namespace: 'argocd', name: 'payments', description: 'Payment processing and ledger', applications: 3, offending: 1 },
    { namespace: 'argocd', name: 'platform', description: 'Ingress, certificates and monitoring', applications: 3, offending: 0 },
  ],
  projectTotal: 3,
  notes: [],
};

export /** What the resource route answers for an OutOfSync Deployment whose image
 *  git moved: the sync word and Argo CD's own diff. */
const ARGOCD_TAB_ANSWERS: Readonly<Record<string, unknown>> = {
  [`${base('argocd')}resource?kind=deployments&name=checkout-api&namespace=shop`]: {
    kind: 'Deployment',
    namespace: 'shop',
    name: 'checkout-api',
    managed: true,
    via: 'annotation',
    copied: false,
    application: { namespace: 'argocd', name: 'checkout', syncRevision: 'c0ffee1234', resources: [] },
    resource: { group: 'apps', kind: 'Deployment', namespace: 'shop', name: 'checkout-api', status: 'OutOfSync', requiresPruning: false },
    syncedRevision: 'beefcafe99',
    drift: { compared: true, masked: false, fields: [] },
    argoDiff: {
      compared: true,
      masked: false,
      setup: false,
      fields: [
        { path: 'spec.template.spec.containers[name=api].image', applied: 'shop/checkout-api:2.14.0', live: 'shop/checkout-api:2.13.2', change: 'changed' },
        { path: 'spec.replicas', applied: '4', live: '2', change: 'changed' },
      ],
    },
  },
};

const ARGOCD_ANSWERS: Readonly<Record<string, ExtraAnswer>> = {
  [base('argocd')]: { applications: ARGO_APPS, complete: true },
  [`${base('argocd')}report`]: ARGO_REPORT,
  // Argo CD's own UI is set, so each Application links to it.
  [`${base('argocd')}settings`]: { tool: 'auto', argoUrl: 'https://argocd.example.com' },
  // Flux CD is not installed: the page says nothing of it.
  [`${base('argocd')}flux`]: new ExtraProblem(404, {
    code: 'kind_unknown',
    message: 'This cluster serves no kustomizations.kustomize.toolkit.fluxcd.io, so Flux CD is not installed on it.',
  }),
};

/* -------------------------------------------------------------------------- */
/* KubeCron                                                                    */
/* -------------------------------------------------------------------------- */

/**
 * One CronJob, its last and next fire given as time ago and time ahead. They
 * agree with its schedule at the instant `shot-extras.mjs` freezes the clock,
 * a Wednesday at 09:40 UTC; opened at any other moment they are only plausible.
 */
function cron(
  namespace: string,
  name: string,
  schedule: string,
  fire: { ago: number; ahead: number },
  history: { runs: number; failed: number; avg: number; max: number },
  over: Partial<CronJob> = {},
): CronJob {
  const last = at(fire.ago);
  return {
    namespace,
    name,
    schedule,
    suspended: false,
    verdict: 'ok',
    nextAt: ahead(fire.ahead),
    lastScheduleTime: last,
    lastSuccessfulTime: last,
    activeCount: 0,
    runs: [],
    history: {
      runs: history.runs,
      failed: history.failed,
      avgDurationMs: history.avg,
      maxDurationMs: history.max,
      ...(history.failed > 0 ? { lastFailure: at(3 * DAY + 2 * HOUR + 40 * MINUTE) } : {}),
    },
    ...over,
  };
}

const CRONJOBS: CronJob[] = [
  cron('shop', 'sitemap-generator', '0 */6 * * *', { ago: 3 * HOUR + 40 * MINUTE, ahead: 2 * HOUR + 20 * MINUTE }, { runs: 28, failed: 0, avg: 42_000, max: 61_000 }),
  cron('shop', 'abandoned-cart-mailer', '*/30 * * * *', { ago: 10 * MINUTE, ahead: 20 * MINUTE }, { runs: 336, failed: 2, avg: 8_400, max: 19_000 }),
  cron('payments', 'ledger-reconcile', '15 2 * * *', { ago: 7 * HOUR + 25 * MINUTE, ahead: 16 * HOUR + 35 * MINUTE }, { runs: 7, failed: 0, avg: 312_000, max: 388_000 }),
  cron('payments', 'invoice-export', '0 5 * * 1-5', { ago: 4 * HOUR + 40 * MINUTE, ahead: 19 * HOUR + 20 * MINUTE }, { runs: 5, failed: 3, avg: 95_000, max: 140_000 }, {
    lastSuccessfulTime: at(4 * DAY + 4 * HOUR + 40 * MINUTE),
    history: { runs: 5, failed: 3, lastFailure: at(4 * HOUR + 38 * MINUTE), avgDurationMs: 95_000, maxDurationMs: 140_000, failing: true },
  }),
  cron('data', 'warehouse-sync', '0 * * * *', { ago: 40 * MINUTE, ahead: 20 * MINUTE }, { runs: 168, failed: 1, avg: 121_000, max: 245_000 }, {
    activeCount: 1,
  }),
  cron('data', 'postgres-backup', '30 1 * * *', { ago: 8 * HOUR + 10 * MINUTE, ahead: 15 * HOUR + 50 * MINUTE }, { runs: 7, failed: 0, avg: 540_000, max: 610_000 }),
  cron('platform', 'certificate-audit', '0 8 * * 1', { ago: 2 * DAY + HOUR + 40 * MINUTE, ahead: 4 * DAY + 22 * HOUR + 20 * MINUTE }, { runs: 1, failed: 0, avg: 15_000, max: 15_000 }, {
    suspended: true,
    verdict: 'suspended',
  }),
];

const COLLECTOR = {
  state: 'ok' as const,
  via: 'label' as const,
  source: 'found by its label in namespace kubecron (Service kubecron-collector), read without a token',
  observedSince: at(400 * DAY),
  metricsEnabled: true,
  runDays: 30,
  logDays: 7,
};

const CRON_BOARD: Board & { cronJobs: CronJob[] } = {
  collectedAt: at(0),
  complete: true,
  retained: { ok: true },
  history: { ok: true, detail: 'Run history from the collector, over the last 7 days.' },
  collector: COLLECTOR,
  counts: { total: 7, missed: 0, unknown: 0, suspended: 1, active: 1, failing: 1, namespaces: 4 },
  cronJobs: CRONJOBS,
};

/** A deterministic, uneven run count for day `d` of a CronJob: a busy job
 *  every day, a weekday job on weekdays, a rare failure, and the last
 *  `recentFailures` days failed. */
function days(seed: number, perDay: number, weekdaysOnly: boolean, failEvery: number, recentFailures = 0): HeatmapAnswer['cronJobs'][number]['days'] {
  const out: HeatmapAnswer['cronJobs'][number]['days'] = [];
  const today = new Date(NOW);
  for (let back = 371; back >= 0; back -= 1) {
    const day = new Date(Date.UTC(today.getUTCFullYear(), today.getUTCMonth(), today.getUTCDate() - back));
    const weekday = day.getUTCDay();
    if (weekdaysOnly && (weekday === 0 || weekday === 6)) continue;
    const failed = (back * 7 + seed) % failEvery === 0 || back < recentFailures ? 1 : 0;
    const total = perDay + ((back * 3 + seed) % 3);
    out.push({ day: day.toISOString().slice(0, 10), total, succeeded: total - failed, running: 0, failed });
  }
  return out;
}

const CRON_HEATMAP: HeatmapAnswer = {
  collector: COLLECTOR,
  complete: true,
  windowDays: 365,
  runDays: 365,
  observedSince: at(400 * DAY),
  cronJobs: [
    { namespace: 'shop', name: 'sitemap-generator', days: days(1, 4, false, 307) },
    { namespace: 'shop', name: 'abandoned-cart-mailer', days: days(2, 46, false, 151) },
    { namespace: 'payments', name: 'ledger-reconcile', days: days(3, 1, false, 263) },
    { namespace: 'payments', name: 'invoice-export', days: days(4, 1, true, 211, 6) },
    { namespace: 'data', name: 'warehouse-sync', days: days(5, 24, false, 181) },
    { namespace: 'data', name: 'postgres-backup', days: days(6, 1, false, 389) },
  ],
  unread: [],
};

export const KUBECRON_ANSWERS: Readonly<Record<string, ExtraAnswer>> = {
  [base('kubecron')]: CRON_BOARD,
  [`${base('kubecron')}daily`]: CRON_HEATMAP,
};

/* -------------------------------------------------------------------------- */
/* KubeAdjust - the overview                                                   */
/* -------------------------------------------------------------------------- */

function finding(over: Partial<AdjustRow>): AdjustRow {
  return {
    workload: 'checkout-api',
    workloadKind: 'Deployment',
    namespace: 'shop',
    pod: 'checkout-api-6d8f7c9b5-2xkqp',
    podCount: 3,
    container: 'app',
    resource: 'Memory',
    kind: 'overkill',
    action: 'Reduce request',
    message: 'Uses a fraction of what it requests',
    current: '2.00 GiB',
    suggested: '768 MiB',
    appliesToBoth: false,
    warnings: [],
    kubectl: '',
    scope: 'Deployment checkout-api · shop',
    confidence: 'high',
    insufficientData: false,
    ...over,
  };
}

const ADJUST_TOP: AdjustRow[] = [
  finding({
    workload: 'ledger-worker',
    namespace: 'payments',
    pod: 'ledger-worker-7b9d5f6c4-q8mzn',
    podCount: 2,
    kind: 'danger',
    action: 'Increase limit',
    message: 'Container was OOMKilled - memory limit is too low',
    current: '512 MiB',
    suggested: '896 MiB',
    warnings: ['3 restarts observed (OOMKilled)'],
    kubectl: 'kubectl set resources deployment/ledger-worker -c app --limits=memory=896Mi -n payments',
    scope: 'Deployment ledger-worker · payments',
  }),
  finding({
    workload: 'search-indexer',
    pod: 'search-indexer-0',
    workloadKind: 'StatefulSet',
    podCount: 1,
    resource: 'CPU',
    kind: 'warning',
    action: 'Increase request',
    message: 'Runs above its request most of the week',
    current: '250m',
    suggested: '600m',
    kubectl: 'kubectl set resources statefulset/search-indexer -c app --requests=cpu=600m -n shop',
    scope: 'StatefulSet search-indexer · shop',
    confidence: 'medium',
  }),
  finding({
    kubectl: 'kubectl set resources deployment/checkout-api -c app --requests=memory=768Mi -n shop',
  }),
  finding({
    workload: 'storefront',
    pod: 'storefront-5c7d8f9b6-h4tlw',
    podCount: 4,
    resource: 'CPU',
    current: '1000m',
    suggested: '300m',
    kubectl: 'kubectl set resources deployment/storefront -c web --requests=cpu=300m -n shop',
    container: 'web',
    scope: 'Deployment storefront · shop',
  }),
];

const ADJUST_PAGE: AdjustPage = {
  cluster: EXTRA_CLUSTER,
  namespace: '',
  view: 'overview',
  category: '',
  namespaces: ['data', 'observability', 'payments', 'shop'],
  notes: [
    { ok: true, label: 'Usage', detail: 'Prometheus, 7 days of series' },
    { ok: true, label: 'Node capacity', detail: '4 node(s)' },
    { ok: true, label: 'Disk', detail: 'read from every kubelet' },
  ],
  window: 'read',
  ready: true,
  workloads: 23,
  counts: [
    { kind: 'danger', label: 'At risk', count: 1 },
    { kind: 'warning', label: 'Worth looking at', count: 3 },
    { kind: 'overkill', label: 'Over-provisioned', count: 6 },
  ],
  categories: [
    { key: 'cpu', label: 'CPU', count: 5, selected: false },
    { key: 'memory', label: 'Memory', count: 4, selected: false },
    { key: 'storage', label: 'Storage', count: 1, selected: false },
  ],
  groups: [],
  commands: [],
  headline: {
    scope: 'this cluster',
    atRisk: 1,
    worth: 3,
    over: 6,
    reclaimCPU: '2.40 cores',
    reclaimMemory: '5.25 GiB',
    reclaimWorkloads: 6,
    usedOfRequestedCPU: 31,
    usedOfRequestedMemory: 54,
  },
  top: ADJUST_TOP,
  totals: {
    namespaces: 4,
    cpuTotal: '3.85 cores',
    cpuAvg: '960m',
    memTotal: '14.2 GiB',
    memAvg: '3.55 GiB',
    source: '7 days of usage',
    fromRequests: false,
  },
  namespaceCards: [
    { name: 'shop', pods: 11, workloads: 7, findings: 5, measured: true, band: 'overcommit', worst: 'warning', cpuRatio: '×3.8', memRatio: '×2.2', cpuRequest: '4.20 cores', cpuUsed: '1.10 cores', memRequest: '9.00 GiB', memUsed: '4.10 GiB', coverage: '', cpuIdle: '3.10 cores', memIdle: '4.90 GiB' },
    { name: 'payments', pods: 6, workloads: 4, findings: 3, measured: true, band: 'high', worst: 'danger', cpuRatio: '×1.9', memRatio: '×1.1', cpuRequest: '1.80 cores', cpuUsed: '950m', memRequest: '4.00 GiB', memUsed: '3.60 GiB', coverage: '', cpuIdle: '850m', memIdle: '400 MiB' },
    { name: 'data', pods: 4, workloads: 3, findings: 1, measured: true, band: 'ok', worst: 'overkill', cpuRatio: '×1.4', memRatio: '×1.3', cpuRequest: '1.50 cores', cpuUsed: '1.05 cores', memRequest: '6.00 GiB', memUsed: '4.60 GiB', coverage: '', cpuIdle: '450m', memIdle: '1.40 GiB' },
    { name: 'observability', pods: 9, workloads: 9, findings: 1, measured: true, band: 'ok', worst: 'overkill', cpuRatio: '×1.6', memRatio: '×1.2', cpuRequest: '1.20 cores', cpuUsed: '750m', memRequest: '2.40 GiB', memUsed: '1.90 GiB', coverage: '', cpuIdle: '450m', memIdle: '500 MiB' },
  ],
  nodeCards: null,
  nodeStates: null,
  cards: null,
  partial: '',
};

/** One namespace's answer: its suggestions, and the commands behind them. */
const ADJUST_SHOP_ROWS = ADJUST_TOP.filter((row) => row.namespace === 'shop');
const ADJUST_NAMESPACE: AdjustPage = {
  ...ADJUST_PAGE,
  namespace: 'shop',
  view: 'namespace',
  groups: [{ kind: 'warning', label: 'Worth a look', mark: '!', rows: ADJUST_SHOP_ROWS }],
  commands: ADJUST_SHOP_ROWS.map((row) => row.kubectl).filter((command) => command !== ''),
  namespaceCards: null,
};

export const KUBEADJUST_ANSWERS: Readonly<Record<string, ExtraAnswer>> = {
  [base('kubeadjust')]: ADJUST_PAGE,
  [`${base('kubeadjust')}?ns=shop`]: ADJUST_NAMESPACE,
};

/* -------------------------------------------------------------------------- */
/* Redis viewer - the inventory                                                */
/* -------------------------------------------------------------------------- */

interface Probe {
  engine: string;
  version: string;
  mode: string;
  keys: number;
  memory: string;
  role?: 'primary' | 'replica';
}

/** One Service, with its probe's answer beside it. */
const REDIS_DOORS: { namespace: string; service: string; port: number; probe: Probe }[] = [
  { namespace: 'shop', service: 'cart-redis-master', port: 6379, probe: { engine: 'Redis', version: '7.4.1', mode: 'standalone', keys: 18_342, memory: '42.7 MiB', role: 'primary' } },
  { namespace: 'shop', service: 'cart-redis-replicas', port: 6379, probe: { engine: 'Redis', version: '7.4.1', mode: 'standalone', keys: 18_342, memory: '41.9 MiB', role: 'replica' } },
  { namespace: 'shop', service: 'sessions', port: 6379, probe: { engine: 'Valkey', version: '8.0.1', mode: 'standalone', keys: 5_120, memory: '12.3 MiB' } },
  { namespace: 'payments', service: 'rate-limiter', port: 6379, probe: { engine: 'Redis', version: '7.2.6', mode: 'standalone', keys: 911, memory: '3.1 MiB' } },
  { namespace: 'data', service: 'queue-redis', port: 6379, probe: { engine: 'Redis', version: '7.4.1', mode: 'standalone', keys: 264, memory: '8.6 MiB' } },
  { namespace: 'data', service: 'feature-flags', port: 6379, probe: { engine: 'Valkey', version: '8.0.1', mode: 'standalone', keys: 87, memory: '1.2 MiB' } },
  { namespace: 'shop', service: 'product-cache', port: 6379, probe: { engine: 'Redis', version: '7.4.1', mode: 'standalone', keys: 41_906, memory: '118.4 MiB' } },
  { namespace: 'payments', service: 'idempotency-keys', port: 6379, probe: { engine: 'Redis', version: '7.2.6', mode: 'standalone', keys: 2_731, memory: '6.8 MiB' } },
  { namespace: 'search', service: 'query-cache', port: 6379, probe: { engine: 'Valkey', version: '8.0.1', mode: 'standalone', keys: 12_480, memory: '64.0 MiB' } },
  { namespace: 'notifications', service: 'pubsub', port: 6379, probe: { engine: 'Redis', version: '7.4.1', mode: 'standalone', keys: 12, memory: '1.0 MiB' } },
];

function door(namespace: string, service: string, role?: 'primary' | 'replica' | 'headless') {
  return {
    namespace,
    service,
    port: 6379,
    portName: 'tcp-redis',
    why: 'the port is named tcp-redis',
    ...(role === undefined ? {} : { role, roleFrom: 'labels' as const }),
  };
}

function member(key: string, pods: string[], doors: ReturnType<typeof door>[], workloads: string[] = []) {
  return {
    key,
    pods,
    behind: pods.length === 1 ? (pods[0] ?? '') : `${String(pods.length)} pods: ${pods.join(', ')}`,
    ready: pods.length,
    reachable: true,
    endpointless: false,
    doors,
    workloads,
  };
}

function single(namespace: string, name: string, workload: string): RedisDeployment {
  return {
    key: `d:${namespace}/${name}`,
    name,
    evidence: ['workload', 'pods'],
    workloads: [workload],
    members: [member(`${namespace}/${name}`, [`${name}-0`], [door(namespace, name)], [workload])],
  };
}

const CART: RedisDeployment = {
  key: 'd:shop/cart-redis',
  name: 'cart-redis',
  evidence: ['workload', 'pods', 'release'],
  release: 'cart',
  chart: 'redis-20.2.1',
  workloads: ['StatefulSet/cart-redis-master', 'StatefulSet/cart-redis-replicas'],
  members: [
    { ...member('shop/cart-redis-master', ['cart-redis-master-0'], [door('shop', 'cart-redis-master', 'primary')], ['StatefulSet/cart-redis-master']), role: 'primary', roleFrom: 'labels' },
    {
      ...member('shop/cart-redis-replicas', ['cart-redis-replicas-0', 'cart-redis-replicas-1'], [door('shop', 'cart-redis-replicas', 'replica')], ['StatefulSet/cart-redis-replicas']),
      role: 'replica',
      roleFrom: 'labels',
    },
  ],
};

const REDIS_INVENTORY: Inventory = {
  scope: '',
  complete: true,
  services: 10,
  members: 10,
  redises: 9,
  namespaces: [
    { name: 'data', deployments: [single('data', 'feature-flags', 'Deployment/feature-flags'), single('data', 'queue-redis', 'StatefulSet/queue-redis')] },
    { name: 'notifications', deployments: [single('notifications', 'pubsub', 'Deployment/pubsub')] },
    {
      name: 'payments',
      deployments: [single('payments', 'idempotency-keys', 'StatefulSet/idempotency-keys'), single('payments', 'rate-limiter', 'StatefulSet/rate-limiter')],
    },
    { name: 'search', deployments: [single('search', 'query-cache', 'StatefulSet/query-cache')] },
    {
      name: 'shop',
      deployments: [CART, single('shop', 'product-cache', 'StatefulSet/product-cache'), single('shop', 'sessions', 'StatefulSet/sessions')],
    },
  ],
};

function probeAnswer(d: (typeof REDIS_DOORS)[number]): InstanceAnswer {
  return {
    instance: door(d.namespace, d.service),
    pod: `${d.service}-0`,
    db: 0,
    server: {
      engine: d.probe.engine,
      version: d.probe.version,
      mode: d.probe.mode,
      databases: 16,
      ...(d.probe.role === undefined ? {} : { role: d.probe.role, ...(d.probe.role === 'primary' ? { replicas: 2 } : {}) }),
    },
    overview: { keys: d.probe.keys, usedMemory: d.probe.memory, keyspace: [{ index: 0, keys: d.probe.keys, expires: Math.round(d.probe.keys / 3) }] },
  };
}

export const REDIS_ANSWERS: Readonly<Record<string, ExtraAnswer>> = {
  [base('redis')]: REDIS_INVENTORY,
  ...Object.fromEntries(
    REDIS_DOORS.map((d) => [`${base('redis')}instances/${d.namespace}/${d.service}/${String(d.port)}/probe`, probeAnswer(d)]),
  ),
};

/* -------------------------------------------------------------------------- */
/* Postgres (CNPG)                                                             */
/* -------------------------------------------------------------------------- */

type PgState = 'healthy' | 'replicating' | 'failed' | 'unknown';

interface PgShape {
  states?: PgState[];
  backupAgo?: number | null;
  certDays?: number;
}

function pgSummary(namespace: string, name: string, over: Partial<CnpgSummary> = {}, shape: PgShape = {}): CnpgSummary {
  const states = shape.states ?? ['healthy', 'healthy', 'healthy'];
  const instances = states.map((state, i) => ({
    name: `${name}-${String(i + 1)}`,
    role: i === 0 ? ('primary' as const) : ('replica' as const),
    state,
    timeline: over.timeline ?? 2,
  }));
  const backupAgo = shape.backupAgo === undefined ? 3 * HOUR : shape.backupAgo;
  const lastSuccess = backupAgo === null ? undefined : at(backupAgo);
  const late = backupAgo === null || backupAgo > DAY;
  const certDays = shape.certDays ?? 71;
  const cert = ahead(certDays * DAY);
  return {
    namespace,
    name,
    phase: 'Cluster in healthy state',
    health: 'healthy',
    expected: states.length,
    ready: states.filter((s) => s === 'healthy').length,
    primary: `${name}-1`,
    target: `${name}-1`,
    timeline: 2,
    promotedAt: at(9 * DAY),
    instances,
    archive: { state: 'ok' },
    backup: {
      state: backupAgo === null ? 'never' : late ? 'late' : 'ok',
      expected: true,
      late,
      ...(lastSuccess === undefined ? {} : { lastSuccess }),
      scheduled: 1,
      recent: [],
      total: 30,
    },
    certs: { state: certDays < 7 ? 'warn' : 'ok', earliest: cert, list: [{ secret: `${name}-server`, expiresAt: cert }] },
    image: 'ghcr.io/cloudnative-pg/postgresql:18.6-system-trixie',
    nodesUsed: 3,
    poolers: 1,
    storageBytes: 20 * 1024 ** 3,
    ...over,
  };
}

const PG_CLUSTERS: CnpgSummary[] = [
  pgSummary(
    'payments',
    'ledger',
    {
      health: 'switchover',
      phase: 'Switchover in progress',
      phaseReason: 'Switching over to ledger-2',
      target: 'ledger-2',
      instances: [
        { name: 'ledger-1', role: 'primary', state: 'healthy', timeline: 4 },
        { name: 'ledger-2', role: 'replica', state: 'healthy', timeline: 4, target: true },
        { name: 'ledger-3', role: 'replica', state: 'healthy', timeline: 4 },
      ],
      timeline: 4,
    },
  ),
  pgSummary(
    'shop',
    'carts',
    { health: 'degraded', archive: { state: 'failing', message: 'unexpected failure invoking barman-cloud-wal-archive: exit status 2' } },
    { states: ['healthy', 'healthy', 'failed'], backupAgo: 31 * HOUR },
  ),
  pgSummary('analytics', 'events', { health: 'progressing', phase: 'Creating a new replica' }, { states: ['healthy', 'healthy', 'replicating'], certDays: 5 }),
  pgSummary('shop', 'orders'),
  pgSummary('shop', 'catalog', {}, { states: ['healthy', 'healthy'] }),
  pgSummary('payments', 'invoices'),
  pgSummary('data', 'warehouse', {}, { states: ['healthy', 'healthy'], backupAgo: 9 * HOUR }),
  pgSummary('identity', 'users'),
  pgSummary(
    'data',
    'reports',
    { archive: { state: 'none' }, backup: { state: 'none', expected: false, late: false, scheduled: 0, recent: [], total: 0 } },
    { states: ['healthy'] },
  ),
];

const CNPG_OVERVIEW: CnpgOverview = {
  now: new Date(NOW).toISOString(),
  complete: true,
  counts: { clusters: 9, healthy: 5, degraded: 1, switchover: 1, archiveFailing: 1, backupLate: 1, notBackedUp: 1, certsSoon: 1 },
  clusters: PG_CLUSTERS,
  sources: [
    { kind: 'backups.postgresql.cnpg.io', state: 'read', complete: true },
    { kind: 'scheduledbackups.postgresql.cnpg.io', state: 'read', complete: true },
    { kind: 'poolers.postgresql.cnpg.io', state: 'read', complete: true },
  ],
  staleAfterHours: 24,
  certWarnDays: 7,
};

const ORDERS = PG_CLUSTERS.find((c) => c.name === 'orders') ?? pgSummary('shop', 'orders');

const ORDERS_BACKUPS = Array.from({ length: 30 }, (_, i) => {
  const stopped = NOW - i * DAY - 3 * HOUR;
  const failed = i === 6 || i === 13;
  return {
    name: `orders-${String(20_260_000 + 30 - i)}`,
    phase: failed ? 'failed' : 'completed',
    method: 'plugin',
    createdAt: new Date(stopped - 20 * MINUTE).toISOString(),
    startedAt: new Date(stopped - 18 * MINUTE).toISOString(),
    stoppedAt: new Date(stopped).toISOString(),
    ...(failed ? { error: 'upload of the base backup failed: connection reset by peer' } : {}),
  };
});

const CNPG_ORDERS: CnpgDetail = {
  now: new Date(NOW).toISOString(),
  summary: {
    ...ORDERS,
    certs: {
      state: 'ok',
      earliest: ahead(71 * DAY),
      list: [
        { secret: 'orders-server', expiresAt: ahead(71 * DAY) },
        { secret: 'orders-replication', expiresAt: ahead(71 * DAY) },
        { secret: 'orders-ca', expiresAt: ahead(84 * DAY) },
      ],
    },
    backup: { ...ORDERS.backup, recent: ORDERS_BACKUPS.slice(0, 10) },
  },
  members: ORDERS.instances.map((i, n) => ({
    ...i,
    pod: { phase: 'Running', ready: true, restarts: n === 2 ? 1 : 0, node: `worker-${String(n + 1)}`, startedAt: at(9 * DAY), roleLabel: i.role },
    data: { claim: i.name, usedBytes: Math.round(([11.2, 10.9, 11.0][n] ?? 11) * 1024 ** 3), capacityBytes: 20 * 1024 ** 3, declaredBytes: 20 * 1024 ** 3, trustworthy: true },
  })),
  conditions: [
    { type: 'Ready', status: 'True', reason: 'ClusterIsReady', message: 'Cluster is Ready' },
    { type: 'ContinuousArchiving', status: 'True', reason: 'ContinuousArchivingSuccess', message: 'Continuous archiving is working' },
    { type: 'LastBackupSucceeded', status: 'True', reason: 'LastBackupSucceeded', message: 'Backup was successful' },
  ],
  events: [
    { type: 'Normal', reason: 'FailoverTarget', message: 'Failing over from orders-2 to orders-1', count: 1, at: at(40 * MINUTE) },
    { type: 'Normal', reason: 'FailingOver', message: "Current primary isn't healthy, initiating a failover from orders-2", count: 1, at: at(41 * MINUTE) },
  ],
  backups: ORDERS_BACKUPS,
  scheduled: [{ name: 'orders-nightly', schedule: '0 0 2 * * *', suspended: false, method: 'plugin', last: at(8 * HOUR) }],
  poolers: [{ name: 'orders-rw', type: 'rw', instances: 2, phase: 'active' }],
  declared: [
    { kind: 'Database', name: 'orders-app', pgName: 'app', applied: true },
    { kind: 'Publication', name: 'orders-pub', pgName: 'orders_pub', applied: true },
  ],
  writeService: 'orders-rw',
  readService: 'orders-r',
  sources: [],
  commands: [
    { act: 'backup', line: 'kubectl cnpg backup orders -n shop' },
    { act: 'restart', line: 'kubectl cnpg restart orders -n shop' },
    { act: 'reload', line: 'kubectl cnpg reload orders -n shop' },
    ...ORDERS.instances.filter((i) => i.role !== 'primary').map((i) => ({ act: 'promote' as const, instance: i.name, line: `kubectl cnpg promote orders ${i.name} -n shop` })),
  ],
};

const CNPG_POD: CnpgPod = { pod: 'orders-2', role: 'replica', of: ORDERS };

export const CNPG_ANSWERS: Readonly<Record<string, ExtraAnswer>> = {
  [base('cnpg')]: CNPG_OVERVIEW,
  [`${base('cnpg')}clusters/shop/orders`]: CNPG_ORDERS,
  [`${base('cnpg')}pods/shop/orders-2`]: CNPG_POD,
  // The lag button's one query, once pressed.
  [`${base('cnpg')}clusters/shop/orders/lag`]: {
    primary: 'orders-1',
    replicas: [
      { name: 'orders-2', state: 'streaming', sync: 'async', write: 0.0012, flush: 0.0067, replay: 0.0124 },
      { name: 'orders-3', state: 'streaming', sync: 'async', write: 0.4, flush: 1.9, replay: 12.6 },
    ],
  },
  // The SQL door, once Connect is pressed.
  [`${base('cnpg')}clusters/shop/orders/stats`]: {
    database: 'app', verified: true, version: '18.1', startedAt: '2026-11-02 06:12:40+00', connections: 37, maxConnections: 100, cacheHit: 0.9874,
    commits: 18_204_551, rollbacks: 1_203, deadlocks: 0,
    databases: [{ name: 'app', bytes: 12_884_901_888 }, { name: 'postgres', bytes: 7_861_740 }],
    replicas: [{ name: 'orders-2', state: 'streaming' }, { name: 'orders-3', state: 'streaming' }],
  },
  [`${base('cnpg')}clusters/shop/orders/tables`]: {
    database: 'app', verified: true, schemas: ['billing', 'public'], complete: true,
    tables: [
      { schema: 'billing', name: 'invoices', kind: 'table', rows: 1_840_220, bytes: 1_073_741_824 },
      { schema: 'billing', name: 'monthly_totals', kind: 'materialized', rows: 36, bytes: 16_384 },
      { schema: 'public', name: 'customers', kind: 'table', rows: 48_310, bytes: 22_020_096 },
      { schema: 'public', name: 'order_lines', kind: 'partitioned', rows: 9_120_400, bytes: 4_294_967_296 },
      { schema: 'public', name: 'orders', kind: 'table', rows: 2_402_118, bytes: 3_221_225_472 },
      { schema: 'public', name: 'v_open_orders', kind: 'view', rows: -1, bytes: 0 },
    ],
  },
  [`${base('cnpg')}clusters/shop/orders/tables/public/orders`]: {
    schema: 'public', table: 'orders', verified: true, columnsTotal: 6, more: true,
    columns: [
      { name: 'id', type: 'bigint' }, { name: 'customer_id', type: 'bigint' }, { name: 'status', type: 'text' },
      { name: 'total', type: 'numeric(12,2)' }, { name: 'placed_at', type: 'timestamp with time zone' }, { name: 'notes', type: 'jsonb' },
    ],
    rows: Array.from({ length: 12 }, (_, i) => [
      { text: String(2_402_118 - i) }, { text: String(48_000 + ((i * 37) % 310)) }, { text: ['paid', 'shipped', 'pending', 'paid'][i % 4] },
      { text: (19.9 + i * 13.37).toFixed(2) }, { text: `2026-11-11 ${String(9 + (i % 10)).padStart(2, '0')}:${String((i * 7) % 60).padStart(2, '0')}:00+00` },
      i % 3 === 0 ? { null: true } : { text: `{"gift": ${String(i % 2 === 0)}, "channel": "web"}` },
    ]),
  },
};

/* -------------------------------------------------------------------------- */
/* The JVM Inspector                                                           */
/* -------------------------------------------------------------------------- */

const MIB = 1024 * 1024;

function jvmContainer(
  name: string,
  image: string,
  reasons: string[],
  limitMiB: number | null,
  heapMiB: number | null,
  from: NonNullable<JvmContainer['heap']>['from'],
  source: string,
  usedMiB: number | null = null,
): JvmContainer {
  const limit = limitMiB === null ? null : { bytes: limitMiB * MIB, text: limitMiB % 1024 === 0 ? `${String(limitMiB / 1024)}Gi` : `${String(limitMiB)}Mi` };
  const percent = limitMiB === null || heapMiB === null ? null : Math.round((heapMiB / limitMiB) * 100);
  const level = limitMiB === null ? 'nolimit' : from === 'explicit' && heapMiB !== null && heapMiB >= limitMiB ? 'error' : (percent ?? 0) > 75 ? 'warning' : 'ok';
  const mib = (n: number) => (n >= 1024 ? `${(n / 1024).toFixed(1)} GiB` : `${n.toFixed(1)} MiB`);
  return {
    name,
    image,
    java: true,
    hints: reasons.map((reason) => ({ source: reason.startsWith('the image') ? 'image' : reason.includes('command') ? 'command' : 'env', reason })),
    request: limit === null ? null : { bytes: (limit.bytes / 2), text: `${String(limitMiB === null ? 0 : limitMiB / 2)}Mi` },
    limit,
    heap: { bytes: heapMiB === null ? null : heapMiB * MIB, text: heapMiB === null ? '' : mib(heapMiB), from, source },
    risk: {
      level,
      percent,
      sentence:
        level === 'nolimit'
          ? 'No memory limit: the heap grows with what the node has.'
          : `The heap may grow to ${String(percent)} % of the memory limit. The 75 % line is a heuristic of this page, not a JDK rule.`,
    },
    notes: [],
    workingSet: usedMiB === null ? null : { bytes: usedMiB * MIB, text: mib(usedMiB) },
  };
}

const TEMURIN = 'the image “eclipse-temurin:21-jre” is a Java runtime image (eclipse-temurin)';
const JAR = 'the command line runs a jar (-jar)';

const JVM_INVENTORY: JvmInventory = {
  cluster: EXTRA_CLUSTER,
  namespace: '',
  complete: true,
  read: 42,
  usage: { state: 'read', complete: true },
  pods: [
    {
      namespace: 'data',
      name: 'kafka-0',
      phase: 'Running',
      java: true,
      owner: { kind: 'StatefulSet', name: 'kafka' },
      containers: [
        jvmContainer('kafka', 'bitnami/kafka:3.7', ['the image “bitnami/kafka:3.7” is kafka, which runs on a JVM'], 4096, 1024, 'default',
          'The JVM’s default, 25 % of the memory limit', 1890),
      ],
    },
    {
      namespace: 'data',
      name: 'elasticsearch-0',
      phase: 'Running',
      java: true,
      owner: { kind: 'StatefulSet', name: 'elasticsearch' },
      containers: [
        jvmContainer('elasticsearch', 'elasticsearch:8.14.1', ['the image “elasticsearch:8.14.1” is elasticsearch, which runs on a JVM',
          'ES_JAVA_OPTS is set: the elasticsearch launcher passes it to the JVM'], 4096, 4096, 'explicit', '“-Xmx4g” in ES_JAVA_OPTS', 3950),
      ],
    },
    {
      namespace: 'identity',
      name: 'keycloak-0',
      phase: 'Running',
      java: true,
      owner: { kind: 'StatefulSet', name: 'keycloak' },
      containers: [
        jvmContainer('keycloak', 'quay.io/keycloak/keycloak:24.0', ['the image “quay.io/keycloak/keycloak:24.0” is keycloak, which runs on a JVM',
          'JAVA_TOOL_OPTIONS is set: every JVM reads it at start-up'], 1536, 1229, 'percentage', '“-XX:MaxRAMPercentage=80” in JAVA_TOOL_OPTIONS', 1180),
      ],
    },
    {
      namespace: 'shop',
      name: 'billing-7c9f4d-x2kq',
      phase: 'Running',
      java: true,
      owner: { kind: 'Deployment', name: 'billing', replicaSet: 'billing-7c9f4d' },
      containers: [jvmContainer('app', 'eclipse-temurin:21-jre', [TEMURIN, JAR], 2048, 2048, 'explicit', '“-Xmx2g” in the command line', 1720)],
    },
    {
      namespace: 'shop',
      name: 'catalogue-5b8d6-p4ml',
      phase: 'Running',
      java: true,
      owner: { kind: 'Deployment', name: 'catalogue', replicaSet: 'catalogue-5b8d6' },
      containers: [
        jvmContainer('api', 'registry.example.com/shop/catalogue:2.3-jdk17', ['the image “registry.example.com/shop/catalogue:2.3-jdk17” names a JDK',
          'JDK_JAVA_OPTIONS is set: the java launcher reads it at start-up'], 1024, 512, 'explicit', '“-Xmx512m” in JDK_JAVA_OPTIONS', 610),
      ],
    },
    {
      namespace: 'shop',
      name: 'catalogue-5b8d6-r7tz',
      phase: 'Running',
      java: true,
      owner: { kind: 'Deployment', name: 'catalogue', replicaSet: 'catalogue-5b8d6' },
      containers: [
        jvmContainer('api', 'registry.example.com/shop/catalogue:2.3-jdk17', ['the image “registry.example.com/shop/catalogue:2.3-jdk17” names a JDK',
          'JDK_JAVA_OPTIONS is set: the java launcher reads it at start-up'], 1024, 512, 'explicit', '“-Xmx512m” in JDK_JAVA_OPTIONS'),
      ],
    },
    {
      namespace: 'tools',
      name: 'jenkins-0',
      phase: 'Running',
      java: true,
      owner: { kind: 'StatefulSet', name: 'jenkins' },
      containers: [
        jvmContainer('jenkins', 'jenkins/jenkins:lts-jdk17', ['the image “jenkins/jenkins:lts-jdk17” names a JDK'], null, null, 'nolimit',
          'No memory limit: the JVM sizes its heap on the node’s memory.', 2310),
      ],
    },
  ],
};

export const JVM_ANSWERS: Readonly<Record<string, ExtraAnswer>> = {
  [base('jvm')]: JVM_INVENTORY,
};

/**
 * The JVM tab of one pod, after Inspect: what jcmd said, the memory bar against
 * the working set, request and limit. The figures are invented and add up: a
 * 1 GiB limit, -Xmx640m, and a working set 124 MiB above what the JVM says it
 * committed. The bar's geometry is written here as the Go route computes it,
 * in percentages of the 1 GiB track.
 */
const JVM_POD: JvmPod = {
  cluster: EXTRA_CLUSTER,
  namespace: 'shop',
  name: 'billing-7c9f4d-x2kq',
  phase: 'Running',
  java: true,
  containers: [
    jvmContainer('app', 'eclipse-temurin:21-jdk', ['the image “eclipse-temurin:21-jdk” is a Java runtime image (eclipse-temurin)'],
      1024, 640, 'explicit', '“-Xmx640m” in JAVA_TOOL_OPTIONS'),
  ],
};

const fig = (mib: number) => ({ bytes: Math.round(mib * MIB), text: `${mib.toFixed(1)} MiB` });

const JVM_INSPECTION: JvmInspection = {
  cluster: EXTRA_CLUSTER,
  namespace: 'shop',
  pod: 'billing-7c9f4d-x2kq',
  container: 'app',
  state: 'inspected',
  pid: 1,
  pidFrom: 'listed',
  sections: [
    { command: 'VM.version', state: 'ok' },
    { command: 'VM.uptime', state: 'ok' },
    { command: 'VM.flags', state: 'ok' },
    { command: 'VM.command_line', state: 'ok' },
    { command: 'VM.system_properties', state: 'ok' },
    { command: 'GC.heap_info', state: 'ok' },
    { command: 'VM.metaspace basic', state: 'ok' },
    { command: 'Compiler.codecache', state: 'ok' },
    {
      command: 'VM.native_memory summary',
      state: 'not-enabled',
      sentence:
        'Native memory tracking is not enabled in this JVM, so its memory outside the heap, metaspace and code cache is ' +
        'not broken down. It can only be turned on at start-up: -XX:NativeMemoryTracking=summary, for instance in ' +
        "JAVA_TOOL_OPTIONS, at a cost of 5 to 10 % of the JVM's performance according to Oracle.",
    },
  ],
  summary: {
    vm: 'OpenJDK 64-Bit Server VM version 21.0.13+9-LTS',
    version: 'JDK 21.0.13',
    major: 21,
    uptime: '3 d 4 h',
    gc: 'G1',
    flags: [
      { name: 'MaxHeapSize', value: String(640 * MIB), text: '640.0 MiB' },
      { name: 'InitialHeapSize', value: String(64 * MIB), text: '64.0 MiB' },
    ],
  },
  memory: {
    source: 'jvm',
    track: fig(1024),
    segments: [
      { key: 'heap', label: 'Java heap', ...fig(512), start: 0, width: 50, used: { ...fig(310), width: 30.3 } },
      { key: 'metaspace', label: 'Metaspace', ...fig(96), start: 50, width: 9.4 },
      { key: 'code', label: 'Code cache (used)', ...fig(48), start: 59.4, width: 4.7 },
      { key: 'outside', label: 'Outside the JVM or not tracked', ...fig(124), start: 64.1, width: 12.1, unattributed: true },
    ],
    markers: [
      { key: 'request', label: 'Request', ...fig(768), at: 75 },
      { key: 'limit', label: 'Limit', ...fig(1024), at: 100 },
      { key: 'workingSet', label: 'Working set', ...fig(780), at: 76.2 },
      { key: 'heapMax', label: 'Max heap', ...fig(640), at: 62.5 },
    ],
    committed: fig(656),
    workingSet: fig(780),
    heapUsed: fig(310),
    heapCommitted: fig(512),
    heapMax: fig(640),
    metaspaceUsed: fig(91),
    metaspaceCommitted: fig(96),
    codeCacheUsed: fig(48),
    codeCacheSize: fig(240),
    heapGauge: { usedPct: 48.4, committedPct: 80 },
    notes: [
      'Without native memory tracking, the JVM\'s threads, GC structures, compiler and symbols are not counted here: they are part of what is outside.',
    ],
  },
  allFlags: [
    { name: 'MaxHeapSize', value: String(640 * MIB) },
    { name: 'UseG1GC', value: 'true' },
  ],
  commandLine: {
    jvmArgs: [{ text: '-Xmx640m' }, { text: '-Dspring.datasource.password=', secret: 'spring.datasource.password' }],
    javaCommand: [{ text: 'org.example.billing.Application' }],
  },
  properties: [
    { key: 'java.version', value: '21.0.13', masked: false },
    { key: 'spring.datasource.password', value: '••••••', masked: true },
  ],
  // Three days and four hours of a busy G1 JVM: 0.6 % of it in pauses.
  gc: {
    collectors: [
      { name: 'G1 young collection pauses', young: true, invocations: 18402, seconds: 1520.4, share: 0.6 },
      { name: 'G1 full collection pauses', young: false, invocations: 2, seconds: 3.1, share: 0 },
      { name: 'G1 concurrent cycle pauses', young: false, invocations: 96, seconds: 9.8, share: 0 },
    ],
    policy: 'GarbageFirst',
    uptime: 273600,
    share: 0.6,
  },
  notes: [],
};

const JVM_POD_PATH = `/x/${EXTRA_CLUSTER}/jvm/pod/shop/billing-7c9f4d-x2kq`;

export const JVM_TAB_ANSWERS: Readonly<Record<string, ExtraAnswer>> = {
  [JVM_POD_PATH]: JVM_POD,
  [`${JVM_POD_PATH}/app/tools`]: { jcmd: true },
  [`${JVM_POD_PATH}/app/inspect`]: JVM_INSPECTION,
  [`${JVM_POD_PATH}/app/threads`]: jvmThreadDump(EXTRA_CLUSTER),
  [`${JVM_POD_PATH}/app/histogram`]: jvmClassHistogram(EXTRA_CLUSTER),
  [`${JVM_POD_PATH}/history`]: jvmMemoryHistory(EXTRA_CLUSTER),
};

/** The pod's tile on its Details: the spec route, and the console's menu
 *  saying the extra is on. */
export const JVM_TILE_ANSWERS: Readonly<Record<string, ExtraAnswer>> = {
  [JVM_POD_PATH]: JVM_POD,
  [`/api/c/${EXTRA_CLUSTER}/nav`]: {
    state: 'ok',
    data: { groups: [{ key: 'extra', label: 'Extra', items: [{ text: 'JVM Inspector', href: `/c/${EXTRA_CLUSTER}/x/jvm` }], deferred: false }] },
  },
};

export const SECURITY_ANSWERS: Readonly<Record<string, ExtraAnswer>> = {
  [base('security')]: withSupply(SECURITY_OVERVIEW),
  [`${base('security')}cluster`]: SECURITY_CLUSTER,
  [`${base('security')}rbac`]: SECURITY_RBAC,
  [`${base('security')}compliance`]: SECURITY_COMPLIANCE,
  [`${base('security')}policies`]: SECURITY_POLICIES,
  [`${base('security')}policies/kyverno/require-run-as-nonroot`]: SECURITY_POLICY,
  [SECURITY_CONFIG_PATH]: SECURITY_CONFIG,
  [`${base('security')}workload/shop/Deployment/checkout-api`]: withSignature(SECURITY_CHECKOUT),
  [`${base('security')}workload/shop/Deployment/checkout-api/sbom`]: SECURITY_CHECKOUT_SBOM,
};

/** The Deployment's tab and tile: its brief, its detail for the ten worst
 *  CVEs, and the console's menu saying the extra is on. */
export const SECURITY_PANEL_ANSWERS: Readonly<Record<string, ExtraAnswer>> = {
  ...SECURITY_ANSWERS,
  [`${base('security')}brief/shop/checkout-api?kind=deployments`]: withBriefSignature(SECURITY_CHECKOUT_BRIEF),
  [`/api/c/${EXTRA_CLUSTER}/nav`]: {
    state: 'ok',
    data: { groups: [{ key: 'extra', label: 'Extra', items: [{ text: 'Security Center', href: `/c/${EXTRA_CLUSTER}/x/security` }], deferred: false }] },
  },
};

/** A cluster without trivy-operator: the page's own route, and the console's
 *  install route the page links to. */
export const SECURITY_INSTALL_ANSWERS: Readonly<Record<string, ExtraAnswer>> = {
  [base('security')]: SECURITY_ABSENT,
  [`/api/c/${EXTRA_CLUSTER}/prereq/trivy-operator`]: SECURITY_PREREQ,
  [`/api/c/${EXTRA_CLUSTER}/prereq/trivy-operator/preview`]: SECURITY_INSTALL_PLAN,
};

/** Neither Argo CD nor Flux on the cluster: one short state. */
const ARGOCD_NEITHER_ANSWERS: Readonly<Record<string, ExtraAnswer>> = {
  [base('argocd')]: new ExtraProblem(404, { code: 'kind_unknown', message: 'This cluster serves no applications.argoproj.io, so Argo CD is not installed on it.' }),
  [`${base('argocd')}settings`]: { tool: 'auto' },
  [`${base('argocd')}flux`]: new ExtraProblem(404, { code: 'kind_unknown', message: 'This cluster serves neither Kustomizations nor HelmReleases, so Flux CD is not installed on it.' }),
};

/** Flux alone, with the Weave GitOps address set. */
const ARGOCD_FLUX_ANSWERS: Readonly<Record<string, ExtraAnswer>> = {
  [base('argocd')]: new ExtraProblem(404, { code: 'kind_unknown', message: 'This cluster serves no applications.argoproj.io, so Argo CD is not installed on it.' }),
  [`${base('argocd')}settings`]: { tool: 'auto', fluxUrl: 'https://gitops.example.com' },
  [`${base('argocd')}flux`]: {
    complete: true,
    objects: [
      { kind: 'Kustomization', namespace: 'flux-system', name: 'apps', ready: 'True', reason: 'ReconciliationSucceeded', reconciling: false, stalled: false, suspended: false, revision: 'main@sha1:4f9c2e1', deleting: false },
      { kind: 'Kustomization', namespace: 'flux-system', name: 'infrastructure', ready: 'False', reason: 'BuildFailed', reconciling: false, stalled: true, suspended: false, deleting: false },
      { kind: 'HelmRelease', namespace: 'podinfo', name: 'podinfo', ready: 'True', reason: 'InstallSucceeded', reconciling: false, stalled: false, suspended: false, revision: '6.7.1', deleting: false },
    ],
  },
};

/** The scenes, by name: which extra, and what its routes answer. */
export const EXTRA_SCENES = {
  'extra-argocd': { key: 'argocd', answers: ARGOCD_ANSWERS },
  'extra-argocd-neither': { key: 'argocd', answers: ARGOCD_NEITHER_ANSWERS },
  'extra-argocd-flux': { key: 'argocd', answers: ARGOCD_FLUX_ANSWERS },
  'extra-kubecron': { key: 'kubecron', answers: KUBECRON_ANSWERS },
  'extra-kubeadjust': { key: 'kubeadjust', answers: KUBEADJUST_ANSWERS },
  'extra-kubeadjust-namespace': { key: 'kubeadjust', answers: KUBEADJUST_ANSWERS, at: 'x/kubeadjust?ns=shop' },
  'extra-events': { key: 'events', answers: EVENTS_ANSWERS(EXTRA_CLUSTER) },
  'extra-events-namespace': { key: 'events', answers: EVENTS_ANSWERS(EXTRA_CLUSTER), at: 'x/events?namespace=shop' },
  // Not pages: the compact line on a Deployment's Details, and its Events tab, each alone.
  'extra-events-line': {
    key: 'events',
    answers: {
      ...EVENTS_ANSWERS(EXTRA_CLUSTER),
      [`/api/c/${EXTRA_CLUSTER}/nav`]: {
        state: 'ok',
        data: { groups: [{ key: 'extra', label: 'Extra', items: [{ text: 'Event Intelligence', href: `/c/${EXTRA_CLUSTER}/x/events` }], deferred: false }] },
      },
    },
    shortcut: { kind: 'deployments', namespace: 'shop', name: 'checkout-api' },
  },
  'extra-events-tab': {
    key: 'events',
    answers: EVENTS_ANSWERS(EXTRA_CLUSTER),
    tab: { key: 'events', kind: 'deployments', namespace: 'shop', name: 'checkout-api' },
  },
  'extra-dns': { key: 'dns', answers: DNS_ANSWERS(EXTRA_CLUSTER) },
  'extra-dns-no-metrics': { key: 'dns', answers: DNS_NO_METRICS_ANSWERS(EXTRA_CLUSTER) },
  'extra-dns-forward': { key: 'dns', answers: DNS_FORWARD_ANSWERS(EXTRA_CLUSTER) },
  'extra-dns-refused': { key: 'dns', answers: dnsStats(new ExtraProblem(DNS_REFUSED.status, DNS_REFUSED.body)) },
  'extra-dns-failed': { key: 'dns', answers: dnsStats(new ExtraProblem(DNS_FAILED.status, DNS_FAILED.body)) },
  'extra-redis': { key: 'redis', answers: REDIS_ANSWERS },
  'extra-jvm': { key: 'jvm', answers: JVM_ANSWERS },
  'extra-netinspect': { key: 'netinspect', answers: NETINSPECT_ANSWERS, at: NETINSPECT_AT.cluster },
  'extra-netinspect-namespace': { key: 'netinspect', answers: NETINSPECT_ANSWERS, at: NETINSPECT_AT.namespace },
  'extra-netinspect-service': { key: 'netinspect', answers: NETINSPECT_ANSWERS, at: NETINSPECT_AT.service },
  // Not a page: the Network tab of a pod, alone.
  'extra-netinspect-tab': { key: 'netinspect', answers: NETINSPECT_ANSWERS, tab: { key: 'pod', kind: 'pods', namespace: 'payments', name: 'ledger-db-0' } },
  'extra-netinspect-calico': { key: 'netinspect', answers: NETINSPECT_NEW_ANSWERS, at: NETINSPECT_NEW_AT.calico },
  'extra-netinspect-admin': { key: 'netinspect', answers: NETINSPECT_NEW_ANSWERS, at: NETINSPECT_NEW_AT.admin },
  // One pod per case of netinspect-badges.fixture.ts: its tile on the Details, and its Network tab.
  ...Object.fromEntries(
    Object.keys(BADGE_CASES).flatMap((name) => {
      const answers = {
        ...BADGE_ANSWERS,
        [`/api/c/${EXTRA_CLUSTER}/nav`]: {
          state: 'ok',
          data: { groups: [{ key: 'extra', label: 'Extra', items: [{ text: 'Network Inspector', href: `/c/${EXTRA_CLUSTER}/x/netinspect` }], deferred: false }] },
        },
      };
      return [
        [`extra-netinspect-tile-${name}`, { key: 'netinspect', answers, shortcut: { kind: 'pods', namespace: 'cases', name } }],
        [`extra-netinspect-badges-${name}`, { key: 'netinspect', answers, tab: { key: 'pod', kind: 'pods', namespace: 'cases', name } }],
      ];
    }),
  ),
  'extra-netinspect-pod': { key: 'netinspect', answers: NETINSPECT_ANSWERS, at: NETINSPECT_AT.pod },
  'extra-pending': { key: 'pending', answers: PENDING_ANSWERS(EXTRA_CLUSTER) },
  'extra-pending-pod': { key: 'pending', answers: PENDING_ANSWERS(EXTRA_CLUSTER), at: PENDING_POD_AT },
  // A pod the scheduler nominated a node for, after evicting lower-priority pods there.
  'extra-pending-nominated': { key: 'pending', answers: PENDING_NOMINATED_ANSWERS(EXTRA_CLUSTER), at: PENDING_NOMINATED_AT },
  // Not a page: the tile on a Pending pod's panel, which opens the debug window.
  'extra-pending-tile': {
    key: 'pending',
    answers: {
      ...PENDING_ANSWERS(EXTRA_CLUSTER),
      [`/api/c/${EXTRA_CLUSTER}/nav`]: {
        state: 'ok',
        data: { groups: [{ key: 'extra', label: 'Extra', items: [{ text: 'Pod Debugging', href: `/c/${EXTRA_CLUSTER}/x/pending` }], deferred: false }] },
      },
    },
    shortcut: PENDING_POD,
  },
  // Not a page: the Pending tab of a pending pod, drawn as the panel draws it.
  'extra-pending-pod-tab': { key: 'pending', answers: PENDING_ANSWERS(EXTRA_CLUSTER), tab: PENDING_POD },
  // Not a page: the Pending tab of a Deployment with pods waiting, and of one whose controller was refused.
  'extra-pending-workload': { key: 'pending', answers: PENDING_WORKLOAD_ANSWERS(EXTRA_CLUSTER), tab: PENDING_CHECKOUT },
  'extra-pending-refused': { key: 'pending', answers: PENDING_WORKLOAD_ANSWERS(EXTRA_CLUSTER), tab: PENDING_REFUSED },
  // The page with a pod's JVM tab open beside the list.
  'extra-jvm-panel': { key: 'jvm', answers: { ...JVM_ANSWERS, ...JVM_TAB_ANSWERS } },
  // Not a page: the JVM tab of one pod, drawn as the object panel draws it.
  'extra-jvm-tab': {
    key: 'jvm',
    answers: JVM_TAB_ANSWERS,
    tab: { key: 'jvm', kind: 'pods', namespace: 'shop', name: 'billing-7c9f4d-x2kq' },
  },
  // The same tab on a JVM started with -XX:+DisableAttachMechanism: Inspect is refused.
  'extra-jvm-attach-disabled': {
    key: 'jvm',
    answers: {
      ...JVM_TAB_ANSWERS,
      [`${JVM_POD_PATH}/app/inspect`]: new ExtraProblem(422, {
        code: 'attach_disabled',
        message: 'The JVM does not support the attach mechanism: it was started with -XX:+DisableAttachMechanism, so jcmd cannot ask it anything.',
        detail: 'com.sun.tools.attach.AttachNotSupportedException: The VM does not support the attach mechanism',
      }),
    },
    tab: { key: 'jvm', kind: 'pods', namespace: 'shop', name: 'billing-7c9f4d-x2kq' },
  },
  // The same tab, for its thread dump: the deadlock of the Go tests' JDK 21 dump.
  'extra-jvm-threads': {
    key: 'jvm',
    answers: JVM_TAB_ANSWERS,
    tab: { key: 'jvm', kind: 'pods', namespace: 'shop', name: 'billing-7c9f4d-x2kq' },
  },
  // The same tab, for its class histogram: the JDK 21 histogram of the Go tests.
  'extra-jvm-histogram': {
    key: 'jvm',
    answers: JVM_TAB_ANSWERS,
    tab: { key: 'jvm', kind: 'pods', namespace: 'shop', name: 'billing-7c9f4d-x2kq' },
  },
  // The same tab, for its memory history: a week of a container that restarted.
  'extra-jvm-history': {
    key: 'jvm',
    answers: JVM_TAB_ANSWERS,
    tab: { key: 'jvm', kind: 'pods', namespace: 'shop', name: 'billing-7c9f4d-x2kq' },
  },
  // Not a page either: the tile the extra draws at the top of the pod's Details.
  'extra-jvm-tile': {
    key: 'jvm',
    answers: JVM_TILE_ANSWERS,
    shortcut: { kind: 'pods', namespace: 'shop', name: 'billing-7c9f4d-x2kq' },
  },
  // Not a page: the Argo CD tab of an OutOfSync Deployment, alone.
  'extra-argocd-tab': {
    key: 'argocd',
    answers: ARGOCD_TAB_ANSWERS,
    tab: { key: 'sync', kind: 'deployments', namespace: 'shop', name: 'checkout-api' },
  },
  'extra-cnpg': { key: 'cnpg', answers: CNPG_ANSWERS },
  // An identity that may not read the custom resource definitions, and a catalog read that failed.
  'extra-cnpg-crd-refused': {
    key: 'cnpg',
    answers: {
      [base('cnpg')]: new ExtraProblem(403, {
        code: 'list_forbidden',
        message: 'You may not read the custom resource definitions, so whether CloudNativePG is installed is unknown.',
        detail: 'customresourcedefinitions.apiextensions.k8s.io is forbidden: User "dev" cannot list resource "customresourcedefinitions" at the cluster scope',
      }),
    },
  },
  'extra-cnpg-crd-failed': {
    key: 'cnpg',
    answers: {
      [base('cnpg')]: new ExtraProblem(502, { code: 'kind_unresolved', message: 'Resolving clusters.postgresql.cnpg.io failed.', detail: 'the server was unable to return a response in the time allotted' }),
    },
  },
  'extra-cnpg-cluster': { key: 'cnpg', answers: CNPG_ANSWERS, at: 'x/cnpg/clusters/shop/orders' },
  'extra-cnpg-tab': { key: 'cnpg', answers: CNPG_ANSWERS, tab: { kind: 'pods', namespace: 'shop', name: 'orders-2' } },
  'extra-certmanager': { key: 'certmanager', answers: { [base('certmanager')]: CERTMANAGER_OVERVIEW } },
  // A person holding `use` and nothing on cert-manager.io: before (a 200 with nothing in it) and after (a refusal).
  'extra-certmanager-nobody-before': { key: 'certmanager', answers: { [base('certmanager')]: CERTMANAGER_NOBODY_BEFORE } },
  'extra-certmanager-refused': {
    key: 'certmanager',
    answers: {
      [base('certmanager')]: new ExtraProblem(403, {
        code: 'list_forbidden',
        message: "The cluster says you may not list cert-manager's Certificates here, so what expires is not shown.",
      }),
    },
  },
  // A person with `view` in one namespace: the broken Issuers tile before and after.
  'extra-certmanager-issuers-before': { key: 'certmanager', at: 'x/certmanager?show=issuers', answers: { [base('certmanager')]: CERTMANAGER_ISSUERS_BEFORE } },
  'extra-certmanager-issuers-after': { key: 'certmanager', at: 'x/certmanager?show=issuers', answers: { [base('certmanager')]: CERTMANAGER_ISSUERS_AFTER } },
  // Not a page: the Certificate tab of an Ingress serving two certificates, with their issuance chains.
  'extra-certmanager-tab': {
    key: 'certmanager',
    answers: { [`${base('certmanager')}objects/ingresses/shop/front`]: CERTMANAGER_TAB },
    tab: { key: 'ingress', kind: 'ingresses', namespace: 'shop', name: 'front' },
  },
  'extra-security': { key: 'security', answers: SECURITY_ANSWERS },
  'extra-security-workload': { key: 'security', answers: SECURITY_ANSWERS, at: 'x/security/shop/Deployment/checkout-api' },
  'extra-security-sbom': { key: 'security', answers: SECURITY_ANSWERS, at: 'x/security/shop/Deployment/checkout-api?tab=sbom' },
  'extra-security-rbac': { key: 'security', answers: SECURITY_ANSWERS, at: 'x/security?view=rbac' },
  'extra-security-policies': { key: 'security', answers: SECURITY_ANSWERS, at: 'x/security?view=policies' },
  'extra-security-policy': { key: 'security', answers: SECURITY_ANSWERS, at: 'x/security?view=policies&policy=kyverno%2Frequire-run-as-nonroot' },
  'extra-security-compliance': { key: 'security', answers: SECURITY_ANSWERS, at: 'x/security?view=compliance' },
  // The page without trivy-operator, where Install opens the console's plan.
  'extra-security-install': { key: 'security', answers: SECURITY_INSTALL_ANSWERS },
  // Not pages: the Security tab of a Deployment, and the tile on its Details.
  'extra-security-tab': {
    key: 'security',
    answers: SECURITY_PANEL_ANSWERS,
    tab: { key: 'cves', kind: 'deployments', namespace: 'shop', name: 'checkout-api' },
  },
  'extra-security-tile': {
    key: 'security',
    answers: SECURITY_PANEL_ANSWERS,
    shortcut: { kind: 'deployments', namespace: 'shop', name: 'checkout-api' },
  },
} as const;
