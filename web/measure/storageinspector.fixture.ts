import type { Claim, ClaimAnswer, Overview, PodAnswer, Volume } from '../app/extras/storage/data';

/**
 * Fake answers of `extras/storage`, in the shape its Go routes write: the
 * `extra-storage` scenes of `main.tsx`, which `shot-extras.mjs` photographs,
 * and the page's tests. Every name and figure is invented.
 *
 * They hold the cases the issue names: a Released volume kept by Retain, a
 * claim nothing mounts, a claim at 92 %, the snapshot kinds missing, and fill
 * figures that could not be read.
 */

const NOW = '2026-11-20T12:00:00Z';
const GI = 1024 ** 3;

function days(n: number): string {
  return new Date(Date.parse(NOW) - n * 86_400_000).toISOString().replace('.000Z', 'Z');
}

function claim(over: Partial<Claim> & Pick<Claim, 'namespace' | 'name'>): Claim {
  return {
    phase: 'Bound',
    class: 'gp3',
    requestedBytes: 20 * GI,
    capacityBytes: 20 * GI,
    accessModes: ['RWO'],
    reclaim: 'Delete',
    created: days(40),
    mount: 'mounted',
    pods: [],
    fill: { state: 'unmounted' },
    snapshots: 0,
    ...over,
  };
}

function measured(percent: number, capacity: number): Claim['fill'] {
  const level = percent >= 90 ? 'bad' : percent >= 80 ? 'warn' : 'ok';
  return { state: 'measured', usedBytes: Math.round((capacity * percent) / 100), capacityBytes: capacity, percent, level };
}

const CLAIMS: Claim[] = [
  claim({
    namespace: 'shop',
    name: 'data-orders-db-0',
    class: 'gp3-retain',
    reclaim: 'Retain',
    pods: ['orders-db-0'],
    workload: { kind: 'StatefulSet', name: 'orders-db' },
    fill: measured(92.4, 50 * GI),
    capacityBytes: 50 * GI,
    requestedBytes: 50 * GI,
    snapshot: { name: 'orders-db-0-1120', at: days(0.4), ready: true, class: 'csi-snap', sizeBytes: 50 * GI },
    snapshots: 7,
  }),
  claim({
    namespace: 'search',
    name: 'data-index-1',
    pods: ['index-1'],
    workload: { kind: 'StatefulSet', name: 'index' },
    fill: measured(84.1, 100 * GI),
    capacityBytes: 100 * GI,
    requestedBytes: 100 * GI,
    snapshot: { name: 'index-1-1119', at: days(1.2), ready: true },
    snapshots: 3,
  }),
  claim({
    namespace: 'shop',
    name: 'cart-cache',
    pods: ['cart-6f9d8-2xk4p', 'cart-6f9d8-9rt2m'],
    workload: { kind: 'Deployment', name: 'cart' },
    accessModes: ['RWX'],
    class: 'efs',
    fill: { state: 'shared', usedBytes: 3.2 * GI },
    capacityBytes: 5 * GI,
    requestedBytes: 5 * GI,
  }),
  claim({
    namespace: 'billing',
    name: 'ledger',
    pods: ['ledger-7c5b-qq8x1'],
    workload: { kind: 'Deployment', name: 'ledger' },
    fill: measured(41.7, 20 * GI),
    snapshot: { name: 'ledger-1118', at: days(2), ready: false },
    snapshots: 2,
  }),
  claim({
    namespace: 'billing',
    name: 'reports',
    pods: ['reports-5d7f-l2m3n'],
    workload: { kind: 'Deployment', name: 'reports' },
    fill: { state: 'unread' },
    capacityBytes: 10 * GI,
    requestedBytes: 10 * GI,
  }),
  claim({ namespace: 'shop', name: 'legacy-uploads', mount: 'unmounted', orphan: 'unmounted', created: days(210), capacityBytes: 30 * GI, requestedBytes: 30 * GI }),
  claim({
    namespace: 'analytics',
    name: 'data-warehouse-0',
    mount: 'unmounted',
    orphan: 'workloadGone',
    created: days(95),
    class: 'gp3-retain',
    reclaim: 'Retain',
    capacityBytes: 200 * GI,
    requestedBytes: 200 * GI,
  }),
  claim({ namespace: 'search', name: 'scratch', phase: 'Pending', mount: 'unmounted', orphan: 'unmounted', capacityBytes: 0, requestedBytes: 8 * GI, created: days(3) }),
];

const VOLUMES: Volume[] = [
  {
    name: 'pvc-5b1e0c2a',
    phase: 'Released',
    capacityBytes: 80 * GI,
    accessModes: ['RWO'],
    reclaim: 'Retain',
    class: 'gp3-retain',
    driver: 'ebs.csi.aws.com',
    claim: 'shop/old-orders',
    created: days(320),
    orphan: 'released',
  },
  { name: 'pv-spare-01', phase: 'Available', capacityBytes: 10 * GI, accessModes: ['RWO'], reclaim: 'Delete', class: 'manual', created: days(12), orphan: 'available' },
  {
    name: 'pvc-0c41f7d9',
    phase: 'Bound',
    capacityBytes: 50 * GI,
    accessModes: ['RWO'],
    reclaim: 'Retain',
    class: 'gp3-retain',
    driver: 'ebs.csi.aws.com',
    claim: 'shop/data-orders-db-0',
    created: days(40),
  },
  { name: 'pvc-8a77b210', phase: 'Bound', capacityBytes: 100 * GI, accessModes: ['RWO'], reclaim: 'Delete', class: 'gp3', driver: 'ebs.csi.aws.com', claim: 'search/data-index-1', created: days(40) },
  { name: 'pvc-1f2e3d4c', phase: 'Bound', capacityBytes: 20 * GI, accessModes: ['RWO'], reclaim: 'Delete', class: 'gp3', driver: 'ebs.csi.aws.com', claim: 'billing/ledger', created: days(40) },
];

export const STORAGE_OVERVIEW: Overview = {
  namespace: '',
  now: NOW,
  complete: true,
  notes: [],
  counts: {
    claims: CLAIMS.length,
    bound: 7,
    pending: 1,
    lost: 0,
    volumes: VOLUMES.length,
    classes: 4,
    orphans: 6,
    orphanClaims: 3,
    orphanVolumes: 2,
    orphanClasses: 1,
    retainedOrphans: 1,
    measured: 3,
    shared: 1,
    unread: 1,
    warn: 1,
    bad: 1,
    snapshotted: 3,
    requestedBytes: 443 * GI,
    provisionedBytes: 435 * GI,
  },
  claims: CLAIMS,
  shown: CLAIMS.length,
  volumes: VOLUMES,
  volumesRead: { state: 'read' },
  classes: [
    { name: 'gp3', provisioner: 'ebs.csi.aws.com', reclaim: 'Delete', binding: 'WaitForFirstConsumer', expansion: true, default: true, created: days(400), claims: 4, volumes: 2 },
    { name: 'gp3-retain', provisioner: 'ebs.csi.aws.com', reclaim: 'Retain', binding: 'WaitForFirstConsumer', expansion: true, default: false, created: days(400), claims: 2, volumes: 2 },
    { name: 'efs', provisioner: 'efs.csi.aws.com', reclaim: 'Delete', binding: 'Immediate', expansion: false, default: false, created: days(300), claims: 1, volumes: 0 },
    {
      name: 'st1-cold',
      provisioner: 'ebs.csi.aws.com',
      reclaim: 'Delete',
      binding: 'WaitForFirstConsumer',
      expansion: false,
      default: false,
      created: days(500),
      claims: 0,
      volumes: 0,
      orphan: 'unusedClass',
    },
  ],
  classesRead: { state: 'read' },
  mountsRead: { state: 'read' },
  fill: {
    state: 'partial', asked: 4, answered: 3, message: 'Fill partial · some kubelets did not answer',
    detail: 'node-c: dial tcp 10.0.3.14:10250: i/o timeout', via: ['victoriametrics', 'kubelet'],
  },
  snapshots: {
    state: 'read',
    total: 12,
    ready: 11,
    contents: 12,
    classes: [{ name: 'csi-snap', driver: 'ebs.csi.aws.com', deletionPolicy: 'Delete', default: true }],
  },
};

/** No snapshot kinds installed, and no kubelet readable. */
export const STORAGE_BARE: Overview = {
  ...STORAGE_OVERVIEW,
  claims: STORAGE_OVERVIEW.claims.map((c): Claim => {
    const out: Claim = { ...c, fill: c.mount === 'mounted' ? { state: 'unread' } : c.fill, snapshots: 0 };
    delete out.snapshot;
    return out;
  }),
  counts: { ...STORAGE_OVERVIEW.counts, measured: 0, shared: 0, unread: 5, warn: 0, bad: 0, snapshotted: 0 },
  fill: {
    state: 'unreadable',
    asked: 4,
    answered: 0,
    message: 'Fill unreadable · needs nodes/proxy (chart: rbac.nodeProxy)',
    detail: 'nodes "node-a" is forbidden: cannot get resource "nodes/proxy"',
  },
  snapshots: { state: 'absent', message: 'Not installed', total: 0, ready: 0, contents: -1, classes: [] },
};

/** The snapshot kinds installed and refused. */
export const STORAGE_SNAPSHOTS_REFUSED: Overview = {
  ...STORAGE_BARE,
  snapshots: { state: 'refused', message: 'Refused', detail: 'list volumesnapshots: forbidden', total: 0, ready: 0, contents: -1, classes: [] },
};

export const STORAGE_CLAIM: ClaimAnswer = {
  now: NOW,
  claim: CLAIMS[0] as Claim,
  ...(VOLUMES[2] === undefined ? {} : { volume: VOLUMES[2] }),
  ...(STORAGE_OVERVIEW.classes[1] === undefined ? {} : { class: STORAGE_OVERVIEW.classes[1] }),
  fill: { state: 'read', asked: 1, answered: 1 },
  mountsRead: { state: 'read' },
  snapshots: STORAGE_OVERVIEW.snapshots,
  history: [
    { name: 'orders-db-0-1120', at: days(0.4), ready: true, sizeBytes: 50 * GI },
    { name: 'orders-db-0-1119', at: days(1.4), ready: true, sizeBytes: 50 * GI },
    { name: 'orders-db-0-1118', at: days(2.4), ready: true, sizeBytes: 50 * GI },
  ],
};

export const STORAGE_POD: PodAnswer = {
  now: NOW,
  claims: [CLAIMS[0] as Claim],
  fill: { state: 'read', asked: 1, answered: 1 },
  snapshots: STORAGE_OVERVIEW.snapshots,
};
