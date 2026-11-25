import type { Answer, Cause, Change, ObjectGroup, Section } from '../app/extras/events/data';

/**
 * Event Intelligence on a cluster whose autoscaler added 17 nodes in 20
 * minutes (#86): the network agent's DaemonSet crash-loops on every new node,
 * pods wait for room before the nodes arrive, and the nodes' own Normal
 * Events double the rows. About two thousand Events, more lanes than the
 * timeline draws, and a cut answer. Every name is invented.
 */

const NOW = Date.now();
const MIN = 60_000;
const at = (minutesAgo: number): string => new Date(NOW - minutesAgo * MIN).toISOString();
const base = (cluster: string) => `/x/${cluster}/events/`;
const READ: Section = { state: 'read', complete: true };

/** A fixed sequence in [0, 1), so the picture is the same on every run. */
function sequence(seed: number): () => number {
  let s = seed;
  return () => {
    s = (s * 1_103_515_245 + 12_345) % 2_147_483_648;
    return s / 2_147_483_648;
  };
}
const rnd = sequence(86);

const nodeName = (i: number): string => `ip-10-20-${String(30 + ((i * 3) % 200))}-${String(11 + ((i * 37) % 200))}.eu-central-1.compute.internal`;

/** The nodes the cluster had before, and the 17 added between 38 and 18 minutes ago. */
const OLD_NODES = [0, 1, 2, 3, 4].map((i) => nodeName(100 + i));
const NEW_NODES = Array.from({ length: 17 }, (_, i) => ({ name: nodeName(i), addedAgo: 38 - Math.round((i * 20) / 16) }));

const nodeChange = (n: { name: string; addedAgo: number }): Change => ({
  id: `node-added//${n.name}/0`,
  kind: 'node-added',
  objectKind: 'Node',
  name: n.name,
  time: at(n.addedAgo),
  detail: 'Ready',
  referenced: true,
});

const CHANGES: Change[] = [
  ...NEW_NODES.map(nodeChange),
  { id: `node-notready//${OLD_NODES[2]!}/0`, kind: 'node-notready', objectKind: 'Node', name: OLD_NODES[2]!, time: at(6), referenced: true },
  { id: 'rollout/shop/storefront/12', kind: 'rollout', objectKind: 'Deployment', namespace: 'shop', name: 'storefront', time: at(52), revision: 12, imagesBefore: ['shop/storefront:4.1.0'], imagesAfter: ['shop/storefront:4.2.0'], referenced: true },
  { id: 'scaling/shop/storefront/0', kind: 'scaling', objectKind: 'HorizontalPodAutoscaler', namespace: 'shop', name: 'storefront', time: at(44), detail: 'New size: 30; reason: cpu resource utilization above target', referenced: true },
];

function cause(reason: string, type: string, message: string, gs: ObjectGroup[]): Cause {
  const count = gs.reduce((s, g) => s + g.count, 0);
  const objects = new Set(gs.map((g) => `${g.kind}/${g.namespace}/${g.name}`)).size;
  // As the server picks it: the change most of the groups came after.
  const votes = new Map<string, number>();
  for (const g of gs) if (g.since !== undefined) votes.set(g.since, (votes.get(g.since) ?? 0) + 1);
  const since = [...votes].sort((a, b) => b[1] - a[1])[0]?.[0];
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
    groups: [...gs].sort((a, b) => b.count - a.count),
    ...(since === undefined ? {} : { since }),
  };
}

const podHash = (i: number): string => (i * 7919 + 4099).toString(36).slice(-5).padStart(5, 'q');

/** One pod of a DaemonSet on each node given, its Events from a little after the node came to now. */
function daemonPods(ds: string, nodes: { name: string; addedAgo: number; old?: boolean }[], total: number, message: string, lag: number): ObjectGroup[] {
  const weights = nodes.map(() => 0.5 + rnd());
  const sum = weights.reduce((a, b) => a + b, 0);
  let left = total;
  return nodes.map((n, i) => {
    const count = i === nodes.length - 1 ? left : Math.max(1, Math.round((weights[i]! / sum) * total));
    left -= count;
    return {
      kind: 'Pod',
      namespace: 'kube-system',
      name: `${ds}-${podHash(i + ds.length * 31)}`,
      count,
      firstSeen: at(n.addedAgo - lag),
      lastSeen: at(rnd() * 1.5),
      workloadKind: 'DaemonSet',
      workloadName: ds,
      node: n.name,
      message,
      ...(n.old === true ? {} : { since: `node-added//${n.name}/0` }),
    };
  });
}

function pods(namespace: string, workload: string, n: number, per: number, firstAgo: number, lastAgo: number, message: string, since?: string): ObjectGroup[] {
  return Array.from({ length: n }, (_, i) => ({
    kind: 'Pod',
    namespace,
    name: `${workload}-5d8c7b9f6-${podHash(i + workload.length * 17)}`,
    count: per,
    firstSeen: at(firstAgo - (i % 4)),
    lastSeen: at(lastAgo + (i % 3)),
    workloadKind: 'Deployment',
    workloadName: workload,
    message,
    ...(since === undefined ? {} : { since }),
  }));
}

const nodeGroups = (reason: string, message: (n: string) => string, delay: number): ObjectGroup[] =>
  NEW_NODES.map((n) => ({ kind: 'Node', namespace: '', name: n.name, count: 1, firstSeen: at(n.addedAgo - delay), lastSeen: at(n.addedAgo - delay), message: message(n.name), since: `node-added//${n.name}/0` }));

// The 22 cilium pods: one on each new node, and five on nodes it had before.
const CILIUM_NODES = [...NEW_NODES, ...OLD_NODES.map((name) => ({ name, addedAgo: 58, old: true }))];
const BACKOFF = 'Back-off restarting failed container cilium-agent in pod cilium-*_kube-system(*)';
const UNHEALTHY = 'Readiness probe failed: Get "http://127.0.0.1:9879/healthz": dial tcp 127.0.0.1:9879: connect: connection refused';

const CAUSES: Cause[] = [
  cause('BackOff', 'Warning', BACKOFF, daemonPods('cilium', CILIUM_NODES, 1650, BACKOFF.replace('*_', 'x2k9q_').replace('(*)', '(5c1e)'), 1)),
  cause('Unhealthy', 'Warning', UNHEALTHY, daemonPods('cilium', NEW_NODES, 152, UNHEALTHY, 1)),
  cause('FailedDaemonPod', 'Warning', 'Found failed daemon pod kube-system/* on node *, will try to kill it', [
    { kind: 'DaemonSet', namespace: 'kube-system', name: 'cilium', count: 41, firstSeen: at(37), lastSeen: at(2), workloadKind: 'DaemonSet', workloadName: 'cilium', message: `Found failed daemon pod kube-system/cilium-x2k9q on node ${nodeName(0)}, will try to kill it`, since: `node-added//${nodeName(0)}/0` },
  ]),
  cause('FailedScheduling', 'Warning', '0/5 nodes are available: * Insufficient cpu. preemption: * No preemption victims found.', [
    ...pods('shop', 'storefront', 14, 6, 44, 21, '0/5 nodes are available: 5 Insufficient cpu. preemption: 0/5 nodes are available: 5 No preemption victims found.', 'scaling/shop/storefront/0'),
    ...pods('shop', 'search-indexer', 6, 5, 41, 24, '0/5 nodes are available: 5 Insufficient cpu. preemption: 0/5 nodes are available: 5 No preemption victims found.'),
  ]),
  cause('BackOff', 'Warning', 'Back-off restarting failed container ebs-plugin in pod ebs-csi-node-*_kube-system(*)', daemonPods('ebs-csi-node', NEW_NODES.slice(0, 9), 27, 'Back-off restarting failed container ebs-plugin in pod ebs-csi-node-m4p2z_kube-system(91ab)', 2)),
  cause('NodeNotReady', 'Warning', 'Node * status is now: NodeNotReady', [
    { kind: 'Node', namespace: '', name: OLD_NODES[2]!, count: 1, firstSeen: at(6), lastSeen: at(6), message: `Node ${OLD_NODES[2]!} status is now: NodeNotReady`, since: `node-notready//${OLD_NODES[2]!}/0` },
  ]),
  cause('Unhealthy', 'Warning', 'Liveness probe failed: HTTP probe failed with statuscode: *', pods('shop', 'storefront', 9, 3, 50, 30, 'Liveness probe failed: HTTP probe failed with statuscode: 500', 'rollout/shop/storefront/12')),
  cause('SuccessfulCreate', 'Normal', 'Created pod: cilium-*', [
    { kind: 'DaemonSet', namespace: 'kube-system', name: 'cilium', count: 39, firstSeen: at(38), lastSeen: at(1), workloadKind: 'DaemonSet', workloadName: 'cilium', message: 'Created pod: cilium-x2k9q' },
  ]),
  cause('SuccessfulDelete', 'Normal', 'Deleted pod: cilium-*', [
    { kind: 'DaemonSet', namespace: 'kube-system', name: 'cilium', count: 22, firstSeen: at(36), lastSeen: at(2), workloadKind: 'DaemonSet', workloadName: 'cilium', message: 'Deleted pod: cilium-x2k9q' },
  ]),
  cause('RegisteredNode', 'Normal', 'Node * event: Registered Node * in Controller', nodeGroups('RegisteredNode', (n) => `Node ${n} event: Registered Node ${n} in Controller`, 0)),
  cause('NodeReady', 'Normal', 'Node * status is now: NodeReady', nodeGroups('NodeReady', (n) => `Node ${n} status is now: NodeReady`, 1)),
  cause('Starting', 'Normal', 'Starting kubelet.', nodeGroups('Starting', () => 'Starting kubelet.', 0)),
  cause('Scheduled', 'Normal', 'Successfully assigned */* to *', [
    ...pods('shop', 'storefront', 30, 1, 30, 30, 'Successfully assigned shop/storefront-5d8c7b9f6-q1x2z to ip-10-20-30-11.eu-central-1.compute.internal'),
    ...pods('shop', 'search-indexer', 6, 1, 26, 26, 'Successfully assigned shop/search-indexer-5d8c7b9f6-q3x4z to ip-10-20-33-48.eu-central-1.compute.internal'),
  ]),
  cause('Pulled', 'Normal', 'Container image "*" already present on machine', pods('shop', 'storefront', 30, 1, 29, 29, 'Container image "shop/storefront:4.2.0" already present on machine')),
  cause('Created', 'Normal', 'Created container: *', pods('shop', 'storefront', 30, 1, 29, 29, 'Created container: storefront')),
  cause('Started', 'Normal', 'Started container *', pods('shop', 'storefront', 30, 1, 29, 29, 'Started container storefront')),
];

const held = (type: string) => CAUSES.filter((c) => c.type === type).reduce((n, c) => n + c.count, 0);
/** The Events past the caps: counted in the tiles, not in the lists. */
const CUT_WARNINGS = 46;
const touched = new Set(CAUSES.filter((c) => c.type === 'Warning').flatMap((c) => c.groups.map((g) => `${g.kind}/${g.namespace}/${g.name}`))).size;

export const EVENTS_NODES_PAGE: Answer = {
  cluster: 'production',
  namespace: '',
  window: '1h',
  since: at(60),
  generatedAt: new Date(NOW).toISOString(),
  tiles: {
    warnings: { count: held('Warning') + CUT_WARNINGS, state: 'read' },
    crashLoop: { count: 22, state: 'read' },
    oomKilled: { count: 0, state: 'read' },
    failedScheduling: { count: CAUSES.filter((c) => c.reason === 'FailedScheduling').reduce((n, c) => n + c.count, 0), state: 'read' },
    objectsTouched: { count: touched, state: 'read' },
  },
  causes: CAUSES,
  changes: CHANGES,
  pods: CAUSES[0]!.groups.map((g) => ({ namespace: g.namespace, name: g.name, ...(g.node === undefined ? {} : { node: g.node }), crashLoop: true, oomKilled: false, workloadKind: 'DaemonSet', workloadName: 'cilium' })),
  sources: { events: READ, replicasets: READ, pods: READ, configmaps: READ, nodes: READ },
  truncated: true,
  complete: true,
};

/** The Events the fixture holds, Warning and Normal: about two thousand. */
export const EVENTS_NODES_TOTAL = held('Warning') + held('Normal');

/** The page of that cluster, by path and query. */
export const EVENTS_NODES_ANSWERS = (cluster: string): Readonly<Record<string, unknown>> => ({
  [`${base(cluster)}?window=1h`]: EVENTS_NODES_PAGE,
  [`${base(cluster)}?window=15m`]: EVENTS_NODES_PAGE,
  [`${base(cluster)}?window=3h`]: EVENTS_NODES_PAGE,
});
