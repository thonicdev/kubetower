/**
 * One cluster, answered from here, for the dashboard mock.
 *
 * **Why a fixture and not a cluster.** A design change is judged on a
 * before/after pair at 1440px: the same page, the same numbers, twice. A
 * live cluster cannot give that — its counts move between the two screenshots,
 * and a Deployment that recovered between them reads as a design change. So the
 * numbers are literals, chosen to make every panel draw something: a healthy
 * cluster hides four of the ten (`Trouble`, `Restarting`, the alerts, the
 * spoilt mark), and a mock of the panel hierarchy that shows six panels is a
 * mock of a different page.
 *
 * **It is a copy of the wire shape and copies drift** — `measure/main.tsx`'s
 * limit 1 applies here as it does to the list scenes. `api/openapi.yaml` is the
 * contract; this file follows it by hand. `schema.ts` is not imported because
 * the scene's `fetch` shim hands these to the page as JSON exactly as the wire
 * would, and a fixture typed against the client's own narrowing would pass a
 * shape the server could never send.
 *
 * The shell's own answers — session, capabilities, clusters, preferences, nav,
 * extensions — are here too, so the scene can mount the **real** `Shell` around
 * the real `Overview`: the rail, the menu and the topbar are part of what a
 * 1440px screenshot of this page is, and a page drawn without them is drawn in
 * a slot of the wrong width.
 */

const CLUSTER = 'prod-eu-west';

/** `2026-09-18T09:14:20Z`, and every `at` below is a fixed offset from it. */
const NOW = '2026-09-18T09:14:20Z';

function ref(text: string, href: string, hint?: string) {
  return hint === undefined ? { text, href } : { text, href, hint };
}

function reading(value: number | null, text: string, trustworthy = true) {
  return { value, trustworthy, text };
}

/** A sparkline's samples: `n` points, `stepSeconds` apart, ending now. */
function series(values: (number | null)[], stepSeconds: number) {
  const started = new Date(Date.parse(NOW) - stepSeconds * 1000 * (values.length - 1)).toISOString();
  return { startedAt: started, stepSeconds, values, trustworthy: true };
}

const OVERVIEW = {
  state: 'ok',
  data: {
    cluster: CLUSTER,
    version: 'v1.31.4',
    collectedAt: NOW,
    allWell: false,
    nodesSummary: ref('3 of 3 nodes ready', `/c/${CLUSTER}/nodes`),
    nodesTone: 'ok',
    workloadsSummary: ref('187 pods', `/c/${CLUSTER}/pods`),

    counts: {
      state: 'ok',
      items: [
        { kind: 'deployments', label: 'Deployments', count: 42, healthy: 40, note: '2 not ready', tone: 'warn', ref: ref('Deployments', `/c/${CLUSTER}/deployments`) },
        { kind: 'statefulsets', label: 'StatefulSets', count: 7, healthy: 7, tone: 'ok', ref: ref('StatefulSets', `/c/${CLUSTER}/statefulsets`) },
        { kind: 'daemonsets', label: 'DaemonSets', count: 9, healthy: 9, tone: 'ok', ref: ref('DaemonSets', `/c/${CLUSTER}/daemonsets`) },
        { kind: 'jobs', label: 'Jobs', count: 23, healthy: 22, note: '1 failed', tone: 'bad', ref: ref('Jobs', `/c/${CLUSTER}/jobs`) },
        { kind: 'cronjobs', label: 'CronJobs', count: 11, ref: ref('CronJobs', `/c/${CLUSTER}/cronjobs`) },
        { kind: 'services', label: 'Services', count: 58, ref: ref('Services', `/c/${CLUSTER}/services`) },
      ],
    },

    phases: {
      state: 'ok',
      items: [
        { phase: 'Running', count: 178, tone: 'ok', ref: ref('Running', `/c/${CLUSTER}/pods?phase=Running`) },
        { phase: 'Pending', count: 4, tone: 'warn', ref: ref('Pending', `/c/${CLUSTER}/pods?phase=Pending`) },
        { phase: 'Succeeded', count: 3, tone: 'muted', ref: ref('Succeeded', `/c/${CLUSTER}/pods?phase=Succeeded`) },
        { phase: 'Failed', count: 2, tone: 'bad', ref: ref('Failed', `/c/${CLUSTER}/pods?phase=Failed`) },
      ],
    },

    capacity: {
      state: 'ok',
      items: [
        {
          label: 'CPU',
          requested: reading(27.4, '27.4 cores'),
          capacity: reading(48, '48 cores'),
          used: reading(11.2, '11.2 cores'),
          ref: ref('CPU requested', `/c/${CLUSTER}/nodes`),
          tone: 'ok',
        },
        {
          label: 'Memory',
          requested: reading(146.3, '146.3 GiB'),
          capacity: reading(188.1, '188.1 GiB'),
          used: reading(97.8, '97.8 GiB'),
          ref: ref('Memory requested', `/c/${CLUSTER}/nodes`),
          tone: 'warn',
        },
        {
          label: 'Pods',
          requested: reading(187, '187 pods'),
          capacity: reading(330, '330 pods'),
          ref: ref('Pods scheduled', `/c/${CLUSTER}/pods`),
          tone: 'ok',
        },
      ],
    },

    history: {
      state: 'ok',
      data: {
        source: 'prometheus',
        caption: 'Cluster totals over the last 6 hours, from Prometheus, one sample a minute.',
        zoomable: false,
        series: [
          {
            key: 'cpu',
            label: 'CPU used',
            unit: 'cores',
            data: series(
              [9.1, 9.4, 9.2, 9.8, 10.3, 10.1, 10.6, 11.4, 12.0, 11.7, 11.2, 10.9, 10.4, 10.8, 11.1, 11.5, 12.2, 12.6, 12.1, 11.6, 11.3, 11.0, 11.2, 11.2],
              900,
            ),
          },
          {
            key: 'memory',
            label: 'Memory used',
            unit: 'GiB',
            data: series(
              [91.2, 91.6, 92.0, 92.4, 93.1, 93.8, 94.2, 94.0, 94.6, 95.3, 95.9, 96.1, 96.4, 96.2, 96.8, 97.1, 97.4, 97.0, 97.3, 97.6, 97.9, 97.7, 97.8, 97.8],
              900,
            ),
          },
        ],
      },
    },

    alerts: {
      state: 'ok',
      items: [
        {
          subject: ref('worker-03', `/c/${CLUSTER}/nodes?search=worker-03&open=worker-03`),
          summary: 'under memory pressure',
          detail: 'kubelet has observed memory pressure for 12 minutes; the scheduler is steering new pods away from it',
          tone: 'warn',
        },
      ],
    },

    trouble: {
      state: 'ok',
      items: [
        {
          kind: ref('Deployment', `/c/${CLUSTER}/deployments`),
          scope: ref('payments', `/c/${CLUSTER}/deployments?namespace=payments`),
          object: ref('ledger-api', `/c/${CLUSTER}/deployments/payments/ledger-api`),
          detail: '2 of 3 replicas available — 1 pod ImagePullBackOff',
          since: '2026-09-18T08:41:00Z',
          age: '33m',
          tone: 'bad',
        },
        {
          kind: ref('Deployment', `/c/${CLUSTER}/deployments`),
          // Long enough to be cut at 1024px, so the measure check exercises the
          // name cap rather than a fit nothing was cut for.
          scope: ref('search-platform-observability-eu-west-3', `/c/${CLUSTER}/deployments?namespace=search-platform-observability-eu-west-3`),
          object: ref(
            'search-indexer-shard-rebalancer-eu-west-3',
            `/c/${CLUSTER}/deployments/search-platform-observability-eu-west-3/search-indexer-shard-rebalancer-eu-west-3`,
          ),
          detail: 'rollout in progress — 4 of 6 updated, 1 pending on a node with memory pressure',
          since: '2026-09-18T09:02:00Z',
          age: '12m',
          tone: 'warn',
        },
        {
          kind: ref('Job', `/c/${CLUSTER}/jobs`),
          scope: ref('reporting', `/c/${CLUSTER}/jobs?namespace=reporting`),
          object: ref('nightly-export-29281', `/c/${CLUSTER}/jobs/reporting/nightly-export-29281`),
          detail: 'failed — BackoffLimitExceeded after 4 attempts',
          since: '2026-09-18T03:17:00Z',
          age: '5h57m',
          tone: 'bad',
        },
      ],
    },

    restarting: {
      state: 'ok',
      items: [
        {
          scope: ref('payments', `/c/${CLUSTER}/pods?namespace=payments`),
          object: ref('ledger-api-7d9f8b6c4-x2k9p', `/c/${CLUSTER}/pods/payments/ledger-api-7d9f8b6c4-x2k9p`),
          container: 'ledger',
          init: false,
          restarts: 14,
          reason: 'CrashLoopBackOff',
          lastExitAt: '2026-09-18T09:11:40Z',
          age: '2m',
          tone: 'bad',
        },
        {
          scope: ref('observability', `/c/${CLUSTER}/pods?namespace=observability`),
          // A realistic DaemonSet pod name, 52 characters: long enough to make
          // this table scroll sideways at 1024px if its columns do not give way.
          object: ref(
            'kube-prometheus-stack-prometheus-node-exporter-x2k9p',
            `/c/${CLUSTER}/pods/observability/kube-prometheus-stack-prometheus-node-exporter-x2k9p`,
          ),
          container: 'collector',
          init: false,
          restarts: 3,
          reason: 'OOMKilled',
          lastExitAt: '2026-09-18T07:58:03Z',
          age: '1h16m',
          tone: 'warn',
        },
        {
          scope: ref('search', `/c/${CLUSTER}/pods?namespace=search`),
          object: ref('indexer-5b7c9d-qw4tz', `/c/${CLUSTER}/pods/search/indexer-5b7c9d-qw4tz`),
          container: 'wait-for-opensearch',
          init: true,
          restarts: 6,
          reason: 'Error',
          lastExitAt: '2026-09-18T09:09:12Z',
          age: '5m',
          tone: 'warn',
        },
      ],
    },

    top: {
      state: 'ok',
      items: [
        {
          label: 'CPU, by pod',
          entries: [
            { ref: ref('search/indexer-5b7c9d-8fh2m', `/c/${CLUSTER}/pods/search/indexer-5b7c9d-8fh2m`), reading: reading(2.84, '2.84 cores') },
            { ref: ref('search/opensearch-0', `/c/${CLUSTER}/pods/search/opensearch-0`), reading: reading(1.92, '1.92 cores') },
            { ref: ref('payments/ledger-api-7d9f8b6c4-jj7lp', `/c/${CLUSTER}/pods/payments/ledger-api-7d9f8b6c4-jj7lp`), reading: reading(1.41, '1.41 cores') },
            { ref: ref('observability/prometheus-0', `/c/${CLUSTER}/pods/observability/prometheus-0`), reading: reading(1.07, '1.07 cores') },
            { ref: ref('ingress/nginx-controller-6c8d7-zx9q2', `/c/${CLUSTER}/pods/ingress/nginx-controller-6c8d7-zx9q2`), reading: reading(0.63, '0.63 cores') },
          ],
        },
        {
          label: 'Memory, by pod',
          entries: [
            { ref: ref('search/opensearch-0', `/c/${CLUSTER}/pods/search/opensearch-0`), reading: reading(12.6, '12.6 GiB') },
            { ref: ref('search/opensearch-1', `/c/${CLUSTER}/pods/search/opensearch-1`), reading: reading(12.4, '12.4 GiB') },
            { ref: ref('observability/prometheus-0', `/c/${CLUSTER}/pods/observability/prometheus-0`), reading: reading(8.9, '8.9 GiB') },
            { ref: ref('data/postgres-primary-0', `/c/${CLUSTER}/pods/data/postgres-primary-0`), reading: reading(7.2, '7.2 GiB') },
            { ref: ref('data/redis-0', `/c/${CLUSTER}/pods/data/redis-0`), reading: reading(3.1, '3.1 GiB') },
          ],
        },
      ],
    },

    events: {
      state: 'ok',
      items: [
        {
          text: 'Back-off pulling image "registry.internal/payments/ledger:2.14.1"',
          at: '2026-09-18T09:13:50Z',
          age: '30s',
          scope: ref('payments', `/c/${CLUSTER}/events?namespace=payments`),
          object: ref('Pod/ledger-api-7d9f8b6c4-x2k9p', `/c/${CLUSTER}/pods/payments/ledger-api-7d9f8b6c4-x2k9p`),
          reason: 'BackOff',
          message: 'Back-off pulling image "registry.internal/payments/ledger:2.14.1"',
          count: 41,
          tone: 'bad',
        },
        {
          text: '0/3 nodes are available: 1 node(s) had untolerated taint {node.kubernetes.io/memory-pressure}',
          at: '2026-09-18T09:12:05Z',
          age: '2m',
          scope: ref('search', `/c/${CLUSTER}/events?namespace=search`),
          object: ref('Pod/indexer-5b7c9d-qw4tz', `/c/${CLUSTER}/pods/search/indexer-5b7c9d-qw4tz`),
          reason: 'FailedScheduling',
          message: '0/3 nodes are available: 1 node(s) had untolerated taint {node.kubernetes.io/memory-pressure}, 2 Insufficient memory.',
          count: 7,
          tone: 'warn',
        },
        {
          text: 'Readiness probe failed: HTTP probe failed with statuscode: 503',
          at: '2026-09-18T09:10:31Z',
          age: '4m',
          scope: ref('payments', `/c/${CLUSTER}/events?namespace=payments`),
          object: ref('Pod/ledger-api-7d9f8b6c4-jj7lp', `/c/${CLUSTER}/pods/payments/ledger-api-7d9f8b6c4-jj7lp`),
          reason: 'Unhealthy',
          message: 'Readiness probe failed: HTTP probe failed with statuscode: 503',
          count: 12,
          tone: 'warn',
        },
        {
          text: 'Node worker-03 status is now: NodeHasInsufficientMemory',
          at: '2026-09-18T09:02:14Z',
          age: '12m',
          object: ref('Node/worker-03', `/c/${CLUSTER}/nodes/worker-03`),
          reason: 'NodeHasInsufficientMemory',
          message: 'Node worker-03 status is now: NodeHasInsufficientMemory',
          count: 1,
          tone: 'warn',
        },
        {
          text: 'Job has reached the specified backoff limit',
          at: '2026-09-18T03:17:44Z',
          age: '5h57m',
          scope: ref('reporting', `/c/${CLUSTER}/events?namespace=reporting`),
          object: ref('Job/nightly-export-29281', `/c/${CLUSTER}/jobs/reporting/nightly-export-29281`),
          reason: 'BackoffLimitExceeded',
          message: 'Job has reached the specified backoff limit',
          count: 1,
          tone: 'bad',
        },
      ],
    },

    notShown: [],
  },
};

/* -------------------------------------------------------------------------- */
/* The shell's own answers                                                     */
/* -------------------------------------------------------------------------- */

const SESSION = {
  identity: 'alice',
  idleExpiresAt: '2999-01-01T00:00:00Z',
  absoluteExpiresAt: '2999-01-01T00:00:00Z',
};

const CAPABILITIES = { podExec: true, nodeShell: true, assistant: true };

const CLUSTERS = {
  state: 'ok',
  items: [
    { name: CLUSTER, label: 'Prod EU West', initials: 'PE', colour: 'violet', state: 'ok', order: 0 },
    { name: 'staging', label: 'Staging', initials: 'ST', colour: 'green-pale', state: 'ok', order: 1 },
    { name: 'docker-desktop', label: 'Docker Desktop', initials: 'DD', colour: 'teal', state: 'unreachable', order: 2 },
  ],
};

const NAV = {
  state: 'ok',
  data: {
    groups: [
      {
        key: 'workloads',
        label: 'Workloads',
        deferred: false,
        items: [
          ref('Pods', `/c/${CLUSTER}/pods`),
          ref('Deployments', `/c/${CLUSTER}/deployments`),
          ref('StatefulSets', `/c/${CLUSTER}/statefulsets`),
          ref('DaemonSets', `/c/${CLUSTER}/daemonsets`),
          ref('Jobs', `/c/${CLUSTER}/jobs`),
          ref('CronJobs', `/c/${CLUSTER}/cronjobs`),
        ],
      },
      {
        key: 'network',
        label: 'Network',
        deferred: false,
        items: [ref('Services', `/c/${CLUSTER}/services`), ref('Ingresses', `/c/${CLUSTER}/ingresses`)],
      },
      {
        key: 'config',
        label: 'Configuration',
        deferred: false,
        items: [ref('ConfigMaps', `/c/${CLUSTER}/configmaps`), ref('Secrets', `/c/${CLUSTER}/secrets`)],
      },
      {
        key: 'cluster',
        label: 'Cluster',
        deferred: false,
        items: [
          ref('Nodes', `/c/${CLUSTER}/nodes`),
          ref('Namespaces', `/c/${CLUSTER}/namespaces`),
          ref('Events', `/c/${CLUSTER}/events`),
        ],
      },
    ],
  },
};

/**
 * Path → body. Anything not here is refused with a 404 problem, never answered
 * with something plausible — `layoutkit.ts`'s `answerTheCluster` gives the
 * reason and it holds here: a catch-all `{state:'ok'}` is how a mock ends up
 * drawing an error notice and nobody noticing.
 */
export const OVERVIEW_ANSWERS: Readonly<Record<string, unknown>> = {
  '/api/session': SESSION,
  // A password is offered, so the session is drawn at the rail's foot; on the
  // loopback desktop (neither method) it is not (`SessionTile.tsx`).
  '/api/auth/methods': { password: true, sso: { state: 'off' } },
  '/api/capabilities': CAPABILITIES,
  '/api/clusters': CLUSTERS,
  '/api/preferences': { state: 'ok', data: {} },
  '/api/forwards': { state: 'empty', items: [] },
  [`/api/c/${CLUSTER}/nav`]: NAV,
  [`/api/c/${CLUSTER}/extensions`]: { state: 'empty', items: [] },
  [`/api/c/${CLUSTER}/overview`]: OVERVIEW,
};

export const OVERVIEW_CLUSTER = CLUSTER;

/**
 * The same cluster, served in a cluster: `mode: shared`,
 * no preferences kept, and the one cluster in the list - so no rail, no Kinds
 * toggle, and the session drawn in the top bar: the signed-in name, whose menu
 * holds Settings and Log out, which the `measure/*.spec.ts` specs measure
 * on it.
 */
export const IN_CLUSTER_ANSWERS: Readonly<Record<string, unknown>> = {
  ...OVERVIEW_ANSWERS,
  '/api/capabilities': { ...CAPABILITIES, mode: 'shared', preferences: false },
  '/api/clusters': { ...CLUSTERS, items: CLUSTERS.items.slice(0, 1) },
};

/**
 * The in-cluster console in its session's last minutes: the warning
 * stays drawn in the top bar, left of the name whose menu holds Log out. The
 * end is read when the harness loads, so it says seven minutes on arrival.
 */
export const IN_CLUSTER_ENDING_ANSWERS: Readonly<Record<string, unknown>> = {
  ...IN_CLUSTER_ANSWERS,
  '/api/session': { ...SESSION, absoluteExpiresAt: new Date(Date.now() + 7 * 60_000 - 1_000).toISOString() },
};

/**
 * The same cluster on the loopback desktop with one kubeconfig context:
 * neither a password nor an identity provider, so
 * no identity is drawn and Settings stays a plain link; preferences are kept,
 * so Arrange is drawn beside it, since there is no rail to carry it.
 */
export const DESKTOP_ONE_ANSWERS: Readonly<Record<string, unknown>> = {
  ...OVERVIEW_ANSWERS,
  '/api/auth/methods': { password: false, sso: { state: 'off' } },
  '/api/capabilities': { ...CAPABILITIES, mode: 'desktop' },
  '/api/clusters': { ...CLUSTERS, items: CLUSTERS.items.slice(0, 1) },
};

/**
 * The same cluster with **no metrics backend at all** — neither Prometheus nor
 * metrics-server. Every `used` under a ring is a
 * reading the server could not take, carrying its own sentence, and the two
 * panels that only a backend fills are `absent`. The sentences are the ones
 * `internal/web/apireads.go` and `internal/metrics` actually send, the longest
 * first, because the defect being measured is a long one running past its tile.
 */
const NO_BACKEND = 'the cluster answered: no Prometheus service matched, and metrics.k8s.io is not registered';

const UNMEASURED = {
  ...OVERVIEW,
  data: {
    ...OVERVIEW.data,
    capacity: {
      ...OVERVIEW.data.capacity,
      items: OVERVIEW.data.capacity.items.map((cell) => ({
        ...cell,
        used: reading(
          null,
          cell.label === 'Pods' ? 'a pod slot is claimed rather than consumed, so there is no separate usage figure' : NO_BACKEND,
          false,
        ),
      })),
    },
    history: { state: 'absent', reason: { code: 'not_installed', message: 'this cluster keeps no series', detail: NO_BACKEND } },
    top: { state: 'absent', reason: { code: 'not_installed', message: 'no metrics backend was found on this cluster', detail: NO_BACKEND } },
  },
};

export const OVERVIEW_UNMEASURED_ANSWERS: Readonly<Record<string, unknown>> = {
  ...OVERVIEW_ANSWERS,
  [`/api/c/${CLUSTER}/overview`]: UNMEASURED,
};

/**
 * The same cluster as an in-cluster console on a local cluster reads it: a
 * Prometheus that answers and holds neither per-container nor pod cgroup
 * cAdvisor series, and no metrics-server. `history`, `top` and `allWell` are
 * the shape of that payload, sentences included - `GET
 * /api/c/in-cluster/overview` - so the scene draws what such a cluster shows;
 * the rest is the dashboard fixture above, with no node alert.
 */
function noShape(metric: string): string {
  return (
    `this cluster's Prometheus has no per-container ${metric} series and no pod cgroup's either — ` +
    'cAdvisor here sees the machine, not its pods, so there is nothing to chart — ' +
    'this is not a claim that the cluster is idle'
  );
}

function unchartable(key: string, label: string, metric: string) {
  return {
    key,
    label,
    data: { startedAt: '0001-01-01T00:00:00Z', stepSeconds: 60, values: [], trustworthy: false },
    reason: { code: 'not_installed', message: noShape(metric) },
  };
}

const QUIET = {
  ...OVERVIEW,
  data: {
    ...OVERVIEW.data,
    allWell: true,
    alerts: { state: 'empty', items: [] },
    history: {
      state: 'ok',
      data: {
        series: [
          unchartable('cpu-used', 'CPU used', 'container_cpu_usage_seconds_total'),
          unchartable('memory-used', 'Memory used', 'container_memory_working_set_bytes'),
        ],
        caption:
          `CPU used: ${noShape('container_cpu_usage_seconds_total')}; ` +
          `Memory used: ${noShape('container_memory_working_set_bytes')}`,
        zoomable: false,
      },
    },
    top: {
      state: 'empty',
      reason: {
        code: 'empty_scope',
        message: 'the metrics backend answered and no pod on this cluster is using anything measurable',
        detail: 'Prometheus answered',
        scope: { cluster: CLUSTER, resource: 'pods', verb: 'list' },
      },
      items: [],
    },
  },
};

export const OVERVIEW_QUIET_ANSWERS: Readonly<Record<string, unknown>> = {
  ...OVERVIEW_ANSWERS,
  [`/api/c/${CLUSTER}/overview`]: QUIET,
};
