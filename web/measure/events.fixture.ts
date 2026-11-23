import type { Answer, Cause, Change, Line, ObjectGroup, Section } from '../app/extras/events/data';

/**
 * Event Intelligence's fake cluster, for its preview pictures
 * (`extras.fixture.ts` names the scenes): an hour of Events on a shop cluster
 * after a rollout, a ConfigMap edit, a scaling and a node that came and went.
 * Every name is invented.
 */

const NOW = Date.now();
const MIN = 60_000;
const at = (minutesAgo: number): string => new Date(NOW - minutesAgo * MIN).toISOString();
const base = (cluster: string) => `/x/${cluster}/events/`;
const READ: Section = { state: 'read', complete: true };

const CHANGES: Change[] = [
  { id: 'rollout/shop/checkout-api/7', kind: 'rollout', objectKind: 'Deployment', namespace: 'shop', name: 'checkout-api', time: at(14), revision: 7, imagesBefore: ['shop/checkout-api:2.13.2'], imagesAfter: ['shop/checkout-api:2.14.0'], referenced: true },
  { id: 'config/shop/checkout-config/0', kind: 'config', objectKind: 'ConfigMap', namespace: 'shop', name: 'checkout-config', time: at(22), manager: 'kubectl-edit', referenced: true },
  { id: 'scaling/payments/payments-worker/0', kind: 'scaling', objectKind: 'HorizontalPodAutoscaler', namespace: 'payments', name: 'payments-worker', time: at(31), detail: 'New size: 8; reason: cpu resource utilization above target', referenced: true },
  { id: 'node-added//node-4/0', kind: 'node-added', objectKind: 'Node', name: 'node-4', time: at(41), referenced: true },
  { id: 'rollback/shop/web/3', kind: 'rollback', objectKind: 'Deployment', namespace: 'shop', name: 'web', time: at(50), revision: 3, referenced: true },
  { id: 'node-notready//node-2/0', kind: 'node-notready', objectKind: 'Node', name: 'node-2', time: at(8), referenced: true },
];

function groups(kind: string, namespace: string, prefix: string, n: number, per: number, firstAgo: number, lastAgo: number, message: string, over: Partial<ObjectGroup> = {}): ObjectGroup[] {
  return Array.from({ length: n }, (_, i) => ({
    kind,
    namespace,
    name: `${prefix}${kind === 'Pod' ? `-${String(7000 + i * 13)}-${String.fromCharCode(97 + (i % 26))}x${String(i % 9)}k` : ''}`,
    count: per + ((i * 7) % Math.max(1, per)),
    firstSeen: at(firstAgo - (i % 5)),
    lastSeen: at(lastAgo + (i % 3)),
    message,
    ...over,
  }));
}

function cause(reason: string, type: string, message: string, gs: ObjectGroup[], extra: Partial<Cause> = {}): Cause {
  const count = gs.reduce((s, g) => s + g.count, 0);
  const objects = new Set(gs.map((g) => `${g.kind}/${g.namespace}/${g.name}`)).size;
  return {
    reason,
    message,
    type,
    count,
    objects,
    kinds: [...new Set(gs.map((g) => g.kind))].sort(),
    firstSeen: gs.map((g) => g.firstSeen).sort()[0] ?? at(60),
    lastSeen: gs.map((g) => g.lastSeen).sort().at(-1) ?? at(0),
    examples: [gs[0]?.message ?? ''],
    groups: gs,
    ...extra,
  };
}

const BACKOFF = 'Back-off restarting failed container app in pod checkout-api-*(*)';
const CAUSES: Cause[] = [
  cause('BackOff', 'Warning', BACKOFF, groups('Pod', 'shop', 'checkout-api', 8, 24, 13, 1, BACKOFF, { workloadKind: 'Deployment', workloadName: 'checkout-api', node: 'node-3', since: 'rollout/shop/checkout-api/7' }), { since: 'rollout/shop/checkout-api/7' }),
  cause('FailedScheduling', 'Warning', '0/4 nodes are available: * Insufficient cpu.', groups('Pod', 'payments', 'payments-worker', 5, 9, 30, 12, '0/3 nodes are available: 3 Insufficient cpu.', { workloadKind: 'Deployment', workloadName: 'payments-worker', since: 'scaling/payments/payments-worker/0' }), { since: 'scaling/payments/payments-worker/0' }),
  cause('OOMKilling', 'Warning', 'Memory cgroup out of memory: Killed process * (app)', groups('Pod', 'shop', 'checkout-api', 3, 3, 12, 4, 'Memory cgroup out of memory: Killed process 4121 (app)', { workloadKind: 'Deployment', workloadName: 'checkout-api', node: 'node-3', since: 'rollout/shop/checkout-api/7' }).map((g, i) => ({ ...g, name: `checkout-api-6f9c-q${String(i)}z` })), { since: 'rollout/shop/checkout-api/7' }),
  cause('Unhealthy', 'Warning', 'Readiness probe failed: HTTP probe failed with statuscode: *', groups('Pod', 'shop', 'checkout-api', 6, 12, 14, 2, 'Readiness probe failed: HTTP probe failed with statuscode: 503', { workloadKind: 'Deployment', workloadName: 'checkout-api', since: 'config/shop/checkout-config/0' }).map((g, i) => ({ ...g, name: `checkout-api-6f9c-r${String(i)}w` })), { since: 'config/shop/checkout-config/0' }),
  cause('NodeNotReady', 'Warning', 'Node node-2 status is now: NodeNotReady', [{ kind: 'Node', namespace: '', name: 'node-2', count: 1, firstSeen: at(8), lastSeen: at(8), message: 'Node node-2 status is now: NodeNotReady', since: 'node-notready//node-2/0' }], { since: 'node-notready//node-2/0' }),
  cause('FailedMount', 'Warning', 'MountVolume.SetUp failed for volume "config" : configmap "*" not found', groups('Pod', 'shop', 'web', 2, 4, 52, 49, 'MountVolume.SetUp failed for volume "config" : configmap "web-config" not found', { workloadKind: 'Deployment', workloadName: 'web', since: 'rollback/shop/web/3' }), { since: 'rollback/shop/web/3' }),
  cause('Pulled', 'Normal', 'Successfully pulled image "shop/checkout-api:*" in *', groups('Pod', 'shop', 'checkout-api', 8, 1, 14, 12, 'Successfully pulled image "shop/checkout-api:2.14.0" in 1.2s', { workloadKind: 'Deployment', workloadName: 'checkout-api' })),
  cause('Scheduled', 'Normal', 'Successfully assigned */* to *', groups('Pod', 'payments', 'payments-worker', 3, 1, 28, 27, 'Successfully assigned payments/payments-worker to node-4', { workloadKind: 'Deployment', workloadName: 'payments-worker' })),
];

const sum = (reason: string) => CAUSES.filter((c) => c.reason === reason).reduce((n, c) => n + c.count, 0);
const warnings = CAUSES.filter((c) => c.type === 'Warning').reduce((n, c) => n + c.count, 0);
const touched = new Set(CAUSES.filter((c) => c.type === 'Warning').flatMap((c) => c.groups.map((g) => `${g.kind}/${g.namespace}/${g.name}`))).size;

export const EVENTS_PAGE: Answer = {
  cluster: 'production',
  namespace: '',
  window: '1h',
  since: at(60),
  generatedAt: new Date(NOW).toISOString(),
  tiles: {
    warnings: { count: warnings, state: 'read' },
    crashLoop: { count: 8, state: 'read' },
    oomKilled: { count: 3, state: 'read' },
    failedScheduling: { count: sum('FailedScheduling'), state: 'read' },
    objectsTouched: { count: touched, state: 'read' },
  },
  causes: CAUSES,
  changes: CHANGES,
  pods: [
    ...CAUSES[0]!.groups.map((g) => ({ namespace: g.namespace, name: g.name, crashLoop: true, oomKilled: false, workloadKind: 'Deployment', workloadName: 'checkout-api' })),
    ...CAUSES[2]!.groups.map((g) => ({ namespace: g.namespace, name: g.name, crashLoop: false, oomKilled: true, workloadKind: 'Deployment', workloadName: 'checkout-api' })),
  ],
  sources: { events: READ, replicasets: READ, pods: READ, configmaps: READ, nodes: READ },
  truncated: false,
  complete: true,
};

/** The page read for the shop namespace alone: its causes, and its own figures. */
export const EVENTS_SHOP: Answer = (() => {
  const causes = CAUSES.filter((c) => c.groups.some((g) => g.namespace === 'shop')).map((c) =>
    cause(c.reason, c.type, c.message, c.groups.filter((g) => g.namespace === 'shop'), c.since === undefined ? {} : { since: c.since }),
  );
  const shopPods = EVENTS_PAGE.pods.filter((p) => p.namespace === 'shop');
  return {
    ...EVENTS_PAGE,
    namespace: 'shop',
    causes,
    pods: shopPods,
    tiles: {
      warnings: { count: causes.filter((c) => c.type === 'Warning').reduce((n, c) => n + c.count, 0), state: 'read' },
      crashLoop: { count: shopPods.filter((p) => p.crashLoop).length, state: 'read' },
      oomKilled: { count: shopPods.filter((p) => p.oomKilled).length, state: 'read' },
      failedScheduling: { count: 0, state: 'read' },
      objectsTouched: { count: new Set(causes.filter((c) => c.type === 'Warning').flatMap((c) => c.groups.map((g) => `${g.kind}/${g.namespace}/${g.name}`))).size, state: 'read' },
    },
  };
})();

/** The Deployment's own view: what its pods and its changes say, and no more. */
export const EVENTS_CHECKOUT: Answer = (() => {
  const keep = (c: Cause) => c.groups.some((g) => g.workloadName === 'checkout-api');
  const causes = CAUSES.filter(keep).map((c) => cause(c.reason, c.type, c.message, c.groups.filter((g) => g.workloadName === 'checkout-api'), c.since === undefined ? {} : { since: c.since }));
  const wanted = new Set(['rollout/shop/checkout-api/7', 'config/shop/checkout-config/0', 'node-notready//node-2/0']);
  return {
    ...EVENTS_PAGE,
    namespace: 'shop',
    object: { kind: 'Deployment', namespace: 'shop', name: 'checkout-api' },
    causes,
    changes: CHANGES.filter((c) => wanted.has(c.id)),
    tiles: { ...EVENTS_PAGE.tiles, warnings: { count: causes.filter((c) => c.type === 'Warning').reduce((n, c) => n + c.count, 0), state: 'read' } },
  };
})();

export const EVENTS_CHECKOUT_LINE: Line = {
  kind: 'Deployment',
  namespace: 'shop',
  name: 'checkout-api',
  warnings: EVENTS_CHECKOUT.tiles.warnings.count,
  lastChange: CHANGES[0]!,
  lastChangeAgeSeconds: 14 * 60,
  generatedAt: EVENTS_PAGE.generatedAt,
  sources: { events: READ, replicasets: READ, pods: READ, configmaps: READ, nodes: READ },
  complete: true,
};

/** The answers of the page, by path and query, for a cluster. */
export const EVENTS_ANSWERS = (cluster: string): Readonly<Record<string, unknown>> => ({
  [`${base(cluster)}?window=1h`]: EVENTS_PAGE,
  [`${base(cluster)}?window=15m`]: EVENTS_PAGE,
  [`${base(cluster)}?window=3h`]: EVENTS_PAGE,
  [`${base(cluster)}?window=1h&namespace=shop`]: EVENTS_SHOP,
  [`${base(cluster)}object/deployments/shop/checkout-api?window=1h`]: EVENTS_CHECKOUT,
  [`${base(cluster)}line/deployments/shop/checkout-api`]: EVENTS_CHECKOUT_LINE,
});

/** The crash-looping pod the Activity tab is drawn for. */
export const ACTIVITY_POD = { namespace: 'shop', name: 'checkout-api-6f9c-q0z' };

/** Its address: the Activity tab of the pod, in the panel's page. */
export const activityPath = (cluster: string): string => `/c/${cluster}/pods/${ACTIVITY_POD.namespace}/${ACTIVITY_POD.name}?tab=activity`;

/**
 * What the console reads for that pod beside the extra's own answers: its
 * payload with the events it carries and the Events and Changes tabs it
 * declares, and its changes. `base` is the console's session, clusters and menu.
 */
export function activityAnswers(cluster: string, base: Readonly<Record<string, unknown>>): Readonly<Record<string, unknown>> {
  const ev = (reason: string, message: string, type: string, count: number, last: number) => ({
    type,
    reason,
    message,
    count,
    first: at(last + 12),
    last: at(last),
  });
  return {
    ...base,
    [`/api/c/${cluster}/r/pods/${ACTIVITY_POD.namespace}/${ACTIVITY_POD.name}`]: {
      state: 'ok',
      data: {
        kind: 'pods',
        namespace: ACTIVITY_POD.namespace,
        name: ACTIVITY_POD.name,
        title: 'Pod',
        tabs: [
          { key: 'events', label: 'Events' },
          { key: 'changes', label: 'Changes', lazy: true },
          { key: 'manifest', label: 'Manifest', lazy: true },
        ],
        sections: [],
        events: [
          ev('BackOff', 'Back-off restarting failed container app in pod checkout-api-6f9c-q0z', 'Warning', 24, 1),
          ev('Unhealthy', 'Readiness probe failed: HTTP probe failed with statuscode: 503', 'Warning', 12, 3),
          ev('Pulled', 'Successfully pulled image "shop/checkout-api:2.14.0" in 1.2s', 'Normal', 1, 13),
          ev('Scheduled', 'Successfully assigned shop/checkout-api-6f9c-q0z to node-3', 'Normal', 1, 14),
        ],
        eventsNote: '',
      },
    },
    [`/api/c/${cluster}/changes`]: {
      state: 'ok',
      data: { entries: [{ at: at(22), summary: 'kubectl-edit Update' }] },
    },
  };
}

/** The extra's answer for that pod: its own events and the changes before them. */
export const ACTIVITY_EXTRA = (cluster: string): Readonly<Record<string, unknown>> => ({
  [`${base(cluster)}object/pods/${ACTIVITY_POD.namespace}/${ACTIVITY_POD.name}?window=1h`]: {
    ...EVENTS_CHECKOUT,
    object: { kind: 'Pod', namespace: ACTIVITY_POD.namespace, name: ACTIVITY_POD.name },
  },
});

/**
 * The same hour with names as long as Kubernetes lets them be: a 63-character
 * namespace and workloads whose pods' names run past a hundred characters. The
 * page must keep its columns and draw no sideways scroll; the whole name is
 * on hover.
 */
const LONG: readonly (readonly [string, string])[] = [
  ['checkout-api', 'checkout-api-settlement-reconciliation-orchestrator-eu-west-1-blue'],
  ['payments-worker', 'payments-worker-ledger-synchronisation-batch-processor-canary-v2'],
  ['"shop"', '"team-shop-checkout-production-eu-west-1-primary-cluster-tenant-a"'],
  ['/shop/', '/team-shop-checkout-production-eu-west-1-primary-cluster-tenant-a/'],
  ['"payments"', '"team-payments-settlement-production-eu-west-1-primary-tenant-b"'],
  ['/payments/', '/team-payments-settlement-production-eu-west-1-primary-tenant-b/'],
];
const lengthen = <T>(value: T): T => {
  let text = JSON.stringify(value);
  for (const [from, to] of LONG) text = text.split(from).join(to);
  return JSON.parse(text) as T;
};
export const EVENTS_LONG_PAGE: Answer = lengthen(EVENTS_PAGE);

/** The page with long names, by path and query, for a cluster. */
export const EVENTS_LONG_ANSWERS = (cluster: string): Readonly<Record<string, unknown>> => ({
  [`${base(cluster)}?window=1h`]: EVENTS_LONG_PAGE,
  [`${base(cluster)}?window=15m`]: EVENTS_LONG_PAGE,
  [`${base(cluster)}?window=3h`]: EVENTS_LONG_PAGE,
});
