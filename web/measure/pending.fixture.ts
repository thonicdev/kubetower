import type { Category, Failure, Fit, NodeBar, PageAnswer, PodView, Preemption, Row, WorkloadView } from '../app/extras/pending/data';

/**
 * Pending's fake cluster, for its preview pictures (`extras.fixture.ts` names
 * the scenes): a dozen nodes, three dozen pods waiting for every reason the
 * page files, two workloads a quota refused, and one pod's view.
 */

const NOW = Date.now();
const MINUTE = 60_000;
const at = (msAgo: number): string => new Date(NOW - msAgo).toISOString();
const GI = 1024 ** 3;
const MI = 1024 ** 2;
const base = (cluster: string) => `/x/${cluster}/pending/`;

const MESSAGES: Record<string, { message: string; reasons: Row['verdict']['reasons']; main: Category }> = {
  cpu: {
    message: '0/12 nodes are available: 2 node(s) had untolerated taint {dedicated: gpu}, 10 Insufficient cpu. preemption: 0/12 nodes are available: 12 Preemption is not helpful for scheduling.',
    reasons: [
      { text: 'Insufficient cpu', nodes: 10, category: 'resources' },
      { text: 'node(s) had untolerated taint {dedicated: gpu}', nodes: 2, category: 'taints' },
    ],
    main: 'resources',
  },
  memory: {
    message: '0/12 nodes are available: 1 node(s) had untolerated taint {node-role.kubernetes.io/control-plane: }, 11 Insufficient memory.',
    reasons: [
      { text: 'node(s) had untolerated taint {node-role.kubernetes.io/control-plane: }', nodes: 1, category: 'taints' },
      { text: 'Insufficient memory', nodes: 11, category: 'resources' },
    ],
    main: 'resources',
  },
  gpu: {
    message: '0/12 nodes are available: 10 node(s) didn\'t match Pod\'s node affinity/selector, 2 Insufficient nvidia.com/gpu.',
    reasons: [
      { text: "node(s) didn't match Pod's node affinity/selector", nodes: 10, category: 'nodeAffinity' },
      { text: 'Insufficient nvidia.com/gpu', nodes: 2, category: 'resources' },
    ],
    main: 'nodeAffinity',
  },
  taint: {
    message: '0/12 nodes are available: 12 node(s) had untolerated taint {workload: batch}.',
    reasons: [{ text: 'node(s) had untolerated taint {workload: batch}', nodes: 12, category: 'taints' }],
    main: 'taints',
  },
  selector: {
    message: "0/12 nodes are available: 12 node(s) didn't match Pod's node affinity/selector.",
    reasons: [{ text: "node(s) didn't match Pod's node affinity/selector", nodes: 12, category: 'nodeAffinity' }],
    main: 'nodeAffinity',
  },
  anti: {
    message: "0/12 nodes are available: 9 node(s) didn't match pod anti-affinity rules, 3 Insufficient memory.",
    reasons: [
      { text: "node(s) didn't match pod anti-affinity rules", nodes: 9, category: 'podAffinity' },
      { text: 'Insufficient memory', nodes: 3, category: 'resources' },
    ],
    main: 'podAffinity',
  },
  spread: {
    message: "0/12 nodes are available: 12 node(s) didn't match pod topology spread constraints.",
    reasons: [{ text: "node(s) didn't match pod topology spread constraints", nodes: 12, category: 'spread' }],
    main: 'spread',
  },
  volume: {
    message: '0/12 nodes are available: pod has unbound immediate PersistentVolumeClaims.',
    reasons: [{ text: 'pod has unbound immediate PersistentVolumeClaims', nodes: -1, category: 'volumes' }],
    main: 'volumes',
  },
  zone: {
    message: '0/12 nodes are available: 8 node(s) had volume node affinity conflict, 4 Insufficient cpu.',
    reasons: [
      { text: 'node(s) had volume node affinity conflict', nodes: 8, category: 'volumes' },
      { text: 'Insufficient cpu', nodes: 4, category: 'resources' },
    ],
    main: 'volumes',
  },
};

function row(namespace: string, name: string, kind: keyof typeof MESSAGES | 'pull' | 'gated', ageMin: number, closest: string, failing: number, cpu: number, mem: number): Row {
  const created = at(ageMin * MINUTE);
  if (kind === 'pull') {
    const message = 'app: ImagePullBackOff: Back-off pulling image "registry.example.com/shop/cart:2.4.1-rc"';
    return {
      namespace, name, created, stage: 'starting', main: 'starting', categories: ['starting'], message,
      verdict: { total: -1, reasons: [{ text: message, nodes: -1, category: 'starting' }] },
      request: { cpu, memory: mem }, node: closest, owner: { kind: 'ReplicaSet', name: name.replace(/-[a-z0-9]{5}$/, '') },
    };
  }
  if (kind === 'gated') {
    const message = 'Held back by its scheduling gates: example.com/quota-check. The controller that set them removes them when it is ready.';
    return {
      namespace, name, created, stage: 'gated', main: 'gated', categories: ['gated'], message,
      verdict: { total: -1, reasons: [{ text: message, nodes: -1, category: 'gated' }] },
      request: { cpu, memory: mem },
    };
  }
  const m = MESSAGES[kind]!;
  return {
    namespace, name, created, since: created, stage: 'unschedulable', main: m.main,
    categories: [...new Set((m.reasons ?? []).map((r) => r.category))],
    message: m.message, verdict: { total: 12, reasons: m.reasons },
    closest: { node: closest, failing }, request: { cpu, memory: mem },
    owner: { kind: 'ReplicaSet', name: name.replace(/-[a-z0-9]{5}$/, '') },
    preemption: preemptionOf(kind),
  };
}

/** What the scheduler's `preemption:` half says for each message above. */
function preemptionOf(kind: keyof typeof MESSAGES): Preemption {
  const priority = { priority: 1000, class: 'standard' };
  if (kind === 'cpu') return { verdict: 'mixed', total: 12, unhelpful: 2, noVictims: 10, ...priority };
  if (kind === 'anti') return { verdict: 'noVictims', total: 12, unhelpful: 0, noVictims: 12, ...priority };
  return { verdict: 'unhelpful', total: 12, unhelpful: 12, noVictims: 0, ...priority };
}

const ROWS: Row[] = [
  row('ml', 'trainer-gpu-0', 'gpu', 190, 'gpu-pool-a1', 1, 4000, 16 * GI),
  row('shop', 'checkout-api-7f9c6-x2k8p', 'cpu', 42, 'node-pool-b4', 1, 2000, 2 * GI),
  row('shop', 'checkout-api-7f9c6-m4q7d', 'cpu', 42, 'node-pool-b2', 1, 2000, 2 * GI),
  row('shop', 'checkout-api-7f9c6-z9w2r', 'cpu', 41, 'node-pool-b4', 1, 2000, 2 * GI),
  row('analytics', 'spark-exec-17', 'memory', 38, 'node-pool-c1', 1, 1000, 24 * GI),
  row('analytics', 'spark-exec-18', 'memory', 38, 'node-pool-c1', 1, 1000, 24 * GI),
  row('analytics', 'spark-exec-19', 'memory', 37, 'node-pool-c2', 1, 1000, 24 * GI),
  row('batch', 'nightly-report-28fz1', 'taint', 33, 'node-pool-a1', 1, 500, 512 * MI),
  row('batch', 'nightly-export-k3j2s', 'taint', 33, 'node-pool-a2', 1, 500, 512 * MI),
  row('search', 'opensearch-data-3', 'zone', 30, 'node-pool-c2', 1, 2000, 8 * GI),
  row('search', 'opensearch-data-4', 'volume', 29, 'node-pool-a1', 0, 2000, 8 * GI),
  row('payments', 'ledger-0', 'anti', 25, 'node-pool-b1', 0, 500, 1 * GI),
  row('payments', 'ledger-1', 'anti', 25, 'node-pool-b3', 0, 500, 1 * GI),
  row('web', 'frontend-5d8b9-p2l4x', 'spread', 18, 'node-pool-a3', 0, 250, 256 * MI),
  row('web', 'frontend-5d8b9-c8v1n', 'spread', 18, 'node-pool-a1', 0, 250, 256 * MI),
  row('edge', 'ingress-canary-6m2xk', 'selector', 14, 'node-pool-a2', 1, 100, 128 * MI),
  row('shop', 'cart-6c7d8-h4m2q', 'pull', 9, 'node-pool-b2', 0, 250, 512 * MI),
  row('shop', 'cart-6c7d8-r7t9w', 'pull', 9, 'node-pool-b3', 0, 250, 512 * MI),
  row('tenants', 'acme-worker-0', 'gated', 6, '', 0, 500, 1 * GI),
  row('shop', 'checkout-api-7f9c6-b1n5s', 'cpu', 4, 'node-pool-b1', 1, 2000, 2 * GI),
];

function bar(name: string, cpu: number, cpuReq: number, mem: number, memReq: number, extra: Partial<NodeBar> = {}): NodeBar {
  return { name, ready: true, cordoned: false, taints: 0, cpu: { allocatable: cpu, requested: cpuReq }, memory: { allocatable: mem * GI, requested: memReq * GI }, pods: { allocatable: 110, requested: 40 }, ...extra };
}

const BARS: NodeBar[] = [
  bar('gpu-pool-a1', 15_800, 6_200, 120, 48, { taints: 1 }),
  bar('gpu-pool-a2', 15_800, 14_900, 120, 102, { taints: 1 }),
  bar('node-pool-a1', 7_800, 7_100, 30, 24),
  bar('node-pool-a2', 7_800, 6_600, 30, 21),
  bar('node-pool-a3', 7_800, 5_900, 30, 26, { cordoned: true }),
  bar('node-pool-b1', 7_800, 7_300, 30, 18),
  bar('node-pool-b2', 7_800, 6_900, 30, 27),
  bar('node-pool-b3', 7_800, 7_450, 30, 22),
  bar('node-pool-b4', 7_800, 6_400, 30, 19),
  bar('node-pool-c1', 15_800, 14_600, 62, 47),
  bar('node-pool-c2', 15_800, 14_300, 62, 51),
  bar('node-pool-c3', 15_800, 15_100, 62, 40, { ready: false }),
];

const FAILURES: Failure[] = [
  {
    namespace: 'tenants', kind: 'ReplicaSet', name: 'globex-api-84c6f', category: 'quota', count: 37, last: at(2 * MINUTE),
    message: 'pods "globex-api-84c6f-9xk2p" is forbidden: exceeded quota: tenant-globex, requested: requests.cpu=2, used: requests.cpu=7500m, limited: requests.cpu=8',
    quota: { name: 'tenant-globex', resource: 'requests.cpu', requested: '2', used: 7500, hard: 8000, room: false },
  },
  {
    namespace: 'tenants', kind: 'StatefulSet', name: 'initech-db', category: 'policy', count: 12, last: at(5 * MINUTE),
    message: 'pods "initech-db-2" is forbidden: violates PodSecurity "restricted:latest": allowPrivilegeEscalation != false (container "postgres" must set securityContext.allowPrivilegeEscalation=false)',
  },
];

function counts(rows: readonly Row[]): Partial<Record<Category, number>> {
  const out: Partial<Record<Category, number>> = {};
  for (const r of rows) out[r.main] = (out[r.main] ?? 0) + 1;
  return out;
}

const READ = { state: 'read', complete: true } as const;

const PAGE: PageAnswer = {
  cluster: 'production', namespace: '', read: at(0), pods: READ, capped: false,
  total: ROWS.length, counts: counts(ROWS), rows: ROWS, nodes: READ, placed: READ, bars: BARS,
  failures: FAILURES, failuresRead: READ,
};

const COLUMNS = [
  { key: 'cordon', label: 'Cordon', computed: true, scheduler: 0 },
  { key: 'taints', label: 'Taints', computed: true, scheduler: 0 },
  { key: 'nodeAffinity', label: 'Selector', computed: true, scheduler: 0 },
  { key: 'resource:cpu', label: 'CPU', computed: true, scheduler: 0 },
  { key: 'resource:memory', label: 'Memory', computed: true, scheduler: 0 },
  { key: 'pods', label: 'Pod count', computed: true, scheduler: 0 },
  { key: 'podAffinity', label: 'Pod affinity', computed: true, scheduler: 3 },
  { key: 'spread', label: 'Spread', computed: false, scheduler: 0, why: 'the topology key rack is not computed here, only the node and the zone are' },
];

/** What refuses a node, where the check says it: a pod whose anti-affinity matches. */
const DETAILS: Readonly<Record<string, Readonly<Record<string, string>>>> = {
  'node-pool-b1': { podAffinity: 'pod shop/checkout-api-7f9c6-q8d2n matches its anti-affinity on kubernetes.io/hostname=node-pool-b1' },
  'node-pool-a2': { podAffinity: 'pod shop/checkout-api-7f9c6-t5w9k matches its anti-affinity on kubernetes.io/hostname=node-pool-a2' },
};

function fit(b: NodeBar, failing: string[]): Fit {
  const free = b.cpu.allocatable - b.cpu.requested;
  const cells = COLUMNS.map((c) => {
    if (!c.computed) return { key: c.key, ok: null, detail: 'per the scheduler' };
    const bad = failing.includes(c.key);
    const detail =
      c.key === 'resource:cpu'
        ? bad
          ? `needs 2, ${free >= 1000 ? (free / 1000).toFixed(1) : `${free}m`} free of ${Math.round(b.cpu.allocatable / 1000)}`
          : `${(free / 1000).toFixed(1)} free`
        : c.key === 'taints' && bad
          ? 'dedicated=gpu:NoSchedule'
          : c.key === 'cordon' && bad
            ? 'cordoned'
            : bad
              ? (DETAILS[b.name]?.[c.key] ?? 'refused')
              : undefined;
    return { key: c.key, ok: !bad, ...(detail === undefined ? {} : { detail }) };
  });
  return { node: b.name, cells, failing: failing.length, excluded: failing.filter((f) => !f.startsWith('resource:')).length, deficit: 0 };
}

const byName = new Map(BARS.map((b) => [b.name, b]));
const FITS: Fit[] = [
  fit(byName.get('node-pool-b4')!, ['resource:cpu']),
  fit(byName.get('node-pool-a2')!, ['resource:cpu', 'podAffinity']),
  fit(byName.get('node-pool-b2')!, ['resource:cpu']),
  fit(byName.get('node-pool-a1')!, ['resource:cpu']),
  fit(byName.get('node-pool-b1')!, ['resource:cpu', 'podAffinity']),
  fit(byName.get('node-pool-b3')!, ['resource:cpu']),
  fit(byName.get('node-pool-c2')!, ['resource:cpu']),
  fit(byName.get('node-pool-c1')!, ['resource:cpu']),
  fit(byName.get('node-pool-c3')!, ['resource:cpu']),
  fit(byName.get('node-pool-a3')!, ['cordon', 'resource:cpu']),
  fit(byName.get('gpu-pool-a1')!, ['taints']),
  fit(byName.get('gpu-pool-a2')!, ['taints', 'resource:cpu']),
];

const POD_ROW = ROWS[1]!;

const POD: PodView = {
  cluster: 'production', pending: true, phase: 'Pending', read: at(0), row: POD_ROW,
  columns: COLUMNS, fits: FITS, nodesTotal: FITS.length, nodes: READ, placed: READ,
  bars: FITS.slice(0, 8).map((f) => byName.get(f.node)!),
  wouldTake: ['On node-pool-b4, the closest node: CPU needs 2, 1.4 free of 7.8. Free some there, or ask for less.'],
  claims: [], claimsRead: READ,
  events: [
    { type: 'Normal', reason: 'SuccessfulCreate', message: 'Created pod: checkout-api-7f9c6-x2k8p', first: at(42 * MINUTE), last: at(42 * MINUTE), count: 1, source: 'replicaset-controller' },
    { type: 'Warning', reason: 'FailedScheduling', message: POD_ROW.message, first: at(42 * MINUTE), last: at(MINUTE), count: 31, source: 'default-scheduler' },
    { type: 'Normal', reason: 'NotTriggerScaleUp', message: "pod didn't trigger scale-up: 1 max node group size reached", first: at(40 * MINUTE), last: at(3 * MINUTE), count: 14, source: 'cluster-autoscaler' },
  ],
  eventsRead: READ, eventsCapped: false,
};

export const PENDING_ANSWERS = (cluster: string): Readonly<Record<string, unknown>> => ({
  [base(cluster)]: PAGE,
  [`${base(cluster)}pod/shop/${POD_ROW.name}`]: POD,
  [`${base(cluster)}brief/shop/${POD_ROW.name}`]: { pending: true, row: POD_ROW },
});

/** The pending pod the tile and its window are drawn for. */
export const PENDING_POD = { kind: 'pods', namespace: 'shop', name: POD_ROW.name };

/** Where the pod view's picture opens, under the scene's cluster. */
export const PENDING_POD_AT = `x/pending/shop/${POD_ROW.name}`;

/**
 * A pod the scheduler found victims for: it nominated a node, and the pod's own
 * view (`extra-pending-nominated`) draws it on the matrix and in the strip.
 */
const NOMINATED_ROW: Row = {
  ...ROWS[0]!,
  namespace: 'analytics', name: 'spark-driver-0', owner: { kind: 'StatefulSet', name: 'spark-driver' }, closest: { node: 'node-pool-c1', failing: 1 },
  preemption: { verdict: 'nominated', total: 12, unhelpful: 0, noVictims: 0, nominated: 'node-pool-c1', priority: 100_000, class: 'critical' },
};

const NOMINATED: PodView = { ...POD, row: NOMINATED_ROW, fits: [FITS[7]!, ...FITS.filter((f) => f.node !== 'node-pool-c1')] };

export const PENDING_NOMINATED_ANSWERS = (cluster: string): Readonly<Record<string, unknown>> => ({
  [`${base(cluster)}pod/analytics/spark-driver-0`]: NOMINATED,
});

export const PENDING_NOMINATED_AT = 'x/pending/analytics/spark-driver-0';

/**
 * A Deployment with three pods waiting on CPU (its tab), a StatefulSet whose
 * controller was refused by a quota (its tab), and the Deployments list's
 * column over both kinds of answer.
 */
const CHECKOUT_ROWS = ROWS.filter((r) => r.name.startsWith('checkout-api-'));

const CHECKOUT: WorkloadView = {
  cluster: 'production', kind: 'Deployment', namespace: 'shop', name: 'checkout-api', read: at(0),
  pods: READ, capped: false, pending: CHECKOUT_ROWS.length,
  rows: [...CHECKOUT_ROWS].sort((a, b) => a.created.localeCompare(b.created)), failuresRead: READ,
};

const REFUSED: WorkloadView = {
  cluster: 'production', kind: 'ReplicaSet', namespace: 'tenants', name: 'globex-api', read: at(0),
  pods: READ, capped: false, pending: 0, rows: [], failure: FAILURES[0]!, failuresRead: READ,
};

export const PENDING_WORKLOAD_ANSWERS = (cluster: string): Readonly<Record<string, unknown>> => ({
  [`${base(cluster)}workload/deployments/shop/checkout-api`]: CHECKOUT,
  [`${base(cluster)}workload/deployments/tenants/globex-api`]: { ...REFUSED, kind: 'Deployment' },
});

export const PENDING_CHECKOUT = { key: 'workload', kind: 'deployments', namespace: 'shop', name: 'checkout-api' };
export const PENDING_REFUSED = { key: 'workload', kind: 'deployments', namespace: 'tenants', name: 'globex-api' };

/** The Deployments list's Pending column (`harness: listsAnswers`' two Deployments). */
export const pendingDeploymentCells = (cluster: string): Readonly<Record<string, { text: string; tone: string; hint: string; href: string }>> => ({
  'payments/ledger-api': {
    text: '2 pending', tone: 'bad', hint: 'Pod affinity 2',
    href: `/c/${cluster}/deployments/payments/ledger-api?tab=x.pending.workload`,
  },
  'shop-prod/storefront-web': {
    text: 'Cannot create', tone: 'bad', hint: 'Quota tenant-globex: requests.cpu 2 asked',
    href: `/c/${cluster}/deployments/shop-prod/storefront-web?tab=x.pending.workload`,
  },
});
