import { OVERVIEW_ANSWERS, OVERVIEW_CLUSTER } from './overview.fixture';

/**
 * A Pods list, answered from here, for the list page's own scene.
 *
 * The whole page is what is measured - the toolbar with the filter control and
 * two chips beside the search box, the table with every row's `⋯`, and the
 * menus they open - in the Shell, so the panel gets the width it gets in the
 * console at 1024 and 1280. `overview.fixture.ts` already answers the Shell's
 * own reads; this adds the list, its namespaces and one pod's detail.
 *
 * **A copy of the wire shape, and copies drift** (limit 1 in `main.tsx`). The
 * columns are `internal/resources/kinds.go`'s Pods descriptor: twelve,
 * `Controlled by` secondary and so not drawn, and Status, Node and QoS
 * filterable. Keys are the
 * label and the position, as `columnKey` makes them.
 */

const CLUSTER = OVERVIEW_CLUSTER;

function column(
  label: string,
  order: number,
  extra: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    key: `${label.toLowerCase().replace(/ /g, '-')}-${String(order)}`,
    label,
    visible: true,
    order,
    sortable: true,
    secondary: false,
    gauged: false,
    copyable: false,
    prose: false,
    filterable: false,
    role: order === 0 ? 'name' : order === 1 ? 'namespace' : 'other',
    ...extra,
  };
}

const END = { align: 'end' };

const COLUMNS = [
  column('Name', 0),
  column('Namespace', 1),
  column('Ready', 2, END),
  column('Status', 3, { filterable: true, filterValues: ['CrashLoopBackOff', 'Pending', 'Running'] }),
  column('Restarts', 4, END),
  column('CPU', 5, { ...END, gauged: true }),
  column('Memory', 6, { ...END, gauged: true }),
  column('Containers', 7, END),
  column('Controlled by', 8, { visible: false, secondary: true }),
  column('Node', 9, {
    filterable: true,
    filterValues: ['ip-10-0-142-37.eu-west-3.compute.internal', 'kubeworker1', 'kubeworker2'],
  }),
  column('QoS', 10, { filterable: true, filterValues: ['BestEffort', 'Burstable', 'Guaranteed'] }),
  column('Age', 11, END),
];

const KEYS = COLUMNS.map((one) => one.key as string);

/** One row, cells in the columns' order. */
function pod(name: string, namespace: string, texts: readonly string[], cpu: number, memory: number) {
  const cells: Record<string, unknown> = {};
  const all = [name, namespace, ...texts];
  all.forEach((text, at) => {
    const key = KEYS[at] ?? '';
    if (key.startsWith('cpu-')) cells[key] = { text, gauge: { percent: cpu, tone: 'ok' } };
    else if (key.startsWith('memory-')) cells[key] = { text, gauge: { percent: memory, tone: memory > 80 ? 'warn' : 'ok' } };
    else if (key.startsWith('status-')) cells[key] = { text, tone: text === 'Running' ? 'ok' : 'warn' };
    else cells[key] = { text };
  });
  return { id: `${namespace}/${name}`, name, namespace, cells };
}

// Ready, Status, Restarts, CPU, Memory, Containers, Controlled by, Node, QoS, Age.
const ROWS = [
  pod('prometheus-0', 'observability', ['2/2', 'Running', '4', '312m', '1.4 GiB', '2', 'StatefulSet/prometheus', 'kubeworker1', 'Burstable', '12d'], 31, 72),
  pod('ledger-api-7d9f8b6c4-x2k9p', 'payments', ['1/1', 'Running', '0', '120m', '512 MiB', '1', 'ReplicaSet/ledger-api-7d9f8b6c4', 'kubeworker1', 'Guaranteed', '3d'], 12, 40),
  pod('ledger-api-7d9f8b6c4-qw4tz', 'payments', ['1/1', 'Running', '0', '98m', '488 MiB', '1', 'ReplicaSet/ledger-api-7d9f8b6c4', 'kubeworker1', 'Guaranteed', '3d'], 10, 38),
  pod('argocd-application-controller-0', 'argocd', ['1/1', 'Running', '1', '640m', '2.1 GiB', '1', 'StatefulSet/argocd-application-controller', 'kubeworker1', 'Burstable', '41d'], 64, 86),
  pod('storefront-web-6f8d7c9b5-lm2xr', 'shop-prod', ['3/3', 'Running', '0', '205m', '730 MiB', '3', 'ReplicaSet/storefront-web-6f8d7c9b5', 'kubeworker1', 'Burstable', '6h'], 20, 55),
];

/**
 * A stored arrangement, where the scene is the arranged one: a stored width on
 * every column drawn, which puts the table on its fixed layout - the stored
 * numbers as shares of the panel. The numbers are the shape of one a person
 * would drag, not a copy of anybody's `columns.json`.
 */
const ARRANGED: Readonly<Record<string, number>> = {
  'name-0': 260,
  'namespace-1': 110,
  'ready-2': 64,
  'status-3': 120,
  'restarts-4': 72,
  'cpu-5': 110,
  'memory-6': 110,
  'containers-7': 64,
  'node-9': 150,
  'qos-10': 90,
  'age-11': 64,
};

function podsPage(arranged: boolean, create = false) {
  return {
    state: 'ok',
    data: {
      kind: 'pods',
      label: 'Pods',
      namespaced: true,
      // The list's "+", permitted in the namespace in view.
      ...(create ? { create: { permitted: true, namespace: 'observability' } } : {}),
      columns: arranged
        ? COLUMNS.map((one) => {
            const width = ARRANGED[one.key as string];
            return width === undefined ? one : { ...one, width };
          })
        : COLUMNS,
      rows: ROWS,
      // What the scene's address asks for, echoed as the server echoes it: by
      // column key. A chip is drawn only for a filter the server applied.
      filters: [
        { column: 'node-9', value: 'kubeworker1' },
        { column: 'status-3', value: 'Running' },
      ],
      bulk: {
        scope: `${CLUSTER}|pods`,
        verbs: [
          { verb: 'evict', label: 'Evict', permitted: true, destructive: true },
          { verb: 'delete', label: 'Delete', permitted: true, destructive: true },
        ],
      },
    },
  };
}

const NAMESPACES = {
  state: 'ok',
  items: ['argocd', 'observability', 'payments', 'shop-prod'].map((name) => ({ name })),
};

/** The containers of each pod, all running: what Shell offers, one entry each. */
const CONTAINERS: Readonly<Record<string, readonly string[]>> = {
  'prometheus-0': ['prometheus', 'config-reloader'],
  'storefront-web-6f8d7c9b5-lm2xr': ['web', 'envoy', 'log-shipper'],
};

function running(name: string) {
  return { name, image: 'x', init: false, probes: [], status: { state: 'running', ready: true, restarts: 0 } };
}

/** Every row's own read, with the drawer's verbs and the containers on it: the
 *  row menu reads it. All three verbs are destructive and Delete alone is
 *  irreversible, as `actions.go`'s specs say. */
const DETAILS = Object.fromEntries(
  ROWS.map((row) => [
    `/api/c/${CLUSTER}/r/pods/${row.namespace}/${row.name}`,
    {
      state: 'ok',
      data: {
        kind: 'pods',
        namespace: row.namespace,
        name: row.name,
        title: 'Pod',
        tabs: [],
        sections: [],
        containers: (CONTAINERS[row.name] ?? ['app']).map(running),
        actions: [
          { verb: 'evict', label: 'Evict', permitted: true, destructive: true },
          { verb: 'restart', label: 'Restart', permitted: true, destructive: true },
          { verb: 'delete', label: 'Delete', permitted: true, destructive: true, irreversible: true },
        ],
      },
    },
  ]),
);

/** The store's answer, unwrapped as the server sends it: one bar holding the
 *  cluster, and no column arranged, so the table is drawn at its own widths. */
const PREFERENCES = {
  rail: { barCount: 1, activeBar: 1, bars: [[CLUSTER]], hidden: [] },
  columns: { kinds: {} },
};

/** The answers, with the table as a console nobody has arranged draws it, or
 *  `arranged` as stored widths draw it. */
export function podsAnswers(arranged: boolean, create = false): Readonly<Record<string, unknown>> {
  return {
    ...OVERVIEW_ANSWERS,
    '/api/preferences': PREFERENCES,
    [`/api/c/${CLUSTER}/r/pods`]: podsPage(arranged, create),
    [`/api/c/${CLUSTER}/namespaces`]: NAMESPACES,
    ...DETAILS,
  };
}

/** Where the scene opens: the Pods list with a Node and a Status chosen. */
export const PODS_PATH = `/c/${CLUSTER}/pods?filter=${encodeURIComponent('Node:kubeworker1')}&filter=${encodeURIComponent('Status:Running')}`;

/**
 * An Ingresses list, for the one thing the Pods list cannot show: a **link
 * inside a row**, whose own browser menu a right-click and the menu key both
 * leave alone. Hosts are pieces with an href,
 * as `internal/web` sends them.
 */
const INGRESS_COLUMNS = [
  column('Name', 0),
  column('Namespace', 1),
  column('Class', 2, { filterable: true, filterValues: ['nginx'] }),
  column('Hosts', 3, { copyable: true }),
  column('Age', 4, END),
];

function ingress(name: string, namespace: string, host: string) {
  const [nameKey, namespaceKey, classKey, hostsKey, ageKey] = INGRESS_COLUMNS.map((one) => one.key as string);
  return {
    id: `${namespace}/${name}`,
    name,
    namespace,
    cells: {
      [nameKey ?? '']: { text: name },
      [namespaceKey ?? '']: { text: namespace },
      [classKey ?? '']: { text: 'nginx' },
      [hostsKey ?? '']: { text: host, copy: host, parts: [{ text: host, href: `https://${host}` }] },
      [ageKey ?? '']: { text: '9d' },
    },
  };
}

const INGRESS_ROWS = [
  ingress('storefront', 'shop-prod', 'shop.example.com'),
  ingress('ledger-api', 'payments', 'ledger.example.com'),
];

export function ingressAnswers(): Readonly<Record<string, unknown>> {
  return {
    ...OVERVIEW_ANSWERS,
    '/api/preferences': PREFERENCES,
    [`/api/c/${CLUSTER}/r/ingresses`]: {
      state: 'ok',
      data: { kind: 'ingresses', label: 'Ingresses', namespaced: true, columns: INGRESS_COLUMNS, rows: INGRESS_ROWS },
    },
    [`/api/c/${CLUSTER}/namespaces`]: NAMESPACES,
    ...Object.fromEntries(
      INGRESS_ROWS.map((row) => [
        `/api/c/${CLUSTER}/r/ingresses/${row.namespace}/${row.name}`,
        {
          state: 'ok',
          data: {
            kind: 'ingresses',
            namespace: row.namespace,
            name: row.name,
            title: 'Ingress',
            tabs: [],
            sections: [],
            actions: [{ verb: 'delete', label: 'Delete', permitted: true, destructive: true, irreversible: true }],
          },
        },
      ]),
    ),
  };
}

export const INGRESS_PATH = `/c/${CLUSTER}/ingresses`;

/**
 * The same list with the longest statuses a pod commonly reports, one a row,
 * for the pill's width: a pill's semibold text
 * runs wider than the grid's per-character figure, by more the longer it is, so
 * `Running` fitting says nothing about `CreateContainerConfigError`.
 */
export const LONG_STATUSES = [
  'CreateContainerConfigError',
  'CrashLoopBackOff',
  'ContainerCreating',
  'ImagePullBackOff',
  'Running',
] as const;

export function podsLongStatusAnswers(): Readonly<Record<string, unknown>> {
  const base = podsPage(false);
  const rows = ROWS.map((row, at) => {
    const status = LONG_STATUSES[at] ?? 'Running';
    return { ...row, cells: { ...row.cells, 'status-3': { text: status, tone: status === 'Running' ? 'ok' : 'bad' } } };
  });
  return {
    ...podsAnswers(false),
    [`/api/c/${CLUSTER}/r/pods`]: { ...base, data: { ...base.data, rows, filters: [] } },
  };
}

/**
 * A pod whose drawer is taller than any window, and a list longer than one:
 * one scroll bar, not two, when a pod is opened.
 *
 * What can make the page scroll is not the drawer's height but **what sits low
 * in it**: a copy button's `sr-only` live region is `position: absolute`, and with
 * no positioned ancestor nearer than `<body>` the drawer's scroller does not
 * clip it - the document grows to reach it. So this pod carries what puts copy
 * buttons far down: eight containers ahead of eight forwardable ports, each
 * drawn with the `kubectl port-forward` line and its copy button (this console
 * has no `portForward`, so every port gets the command). And the list carries
 * forty rows with a copyable Node, so a row's copy button sits below the fold
 * of the list's own panel as well.
 *
 * Fabricated; the names are only plausible.
 */
const TALL_POD = 'prometheus-0';
const TALL_CONTAINERS = [
  'prometheus',
  'config-reloader',
  'thanos-sidecar',
  'oauth-proxy',
  'kube-rbac-proxy',
  'node-exporter',
  'log-shipper',
  'envoy',
];

export function podsTallAnswers(): Readonly<Record<string, unknown>> {
  const base = podsPage(false);
  const columns = (base.data.columns as Record<string, unknown>[]).map((one) =>
    one.key === 'node-9' ? { ...one, copyable: true } : one,
  );
  const rows = Array.from({ length: 40 }, (_, at) => {
    const row = ROWS[at % ROWS.length] ?? ROWS[0];
    if (row === undefined) throw new Error('no rows to repeat');
    const name = at < ROWS.length ? row.name : `${row.name}-${String(at)}`;
    const node = (row.cells['node-9'] as { text: string }).text;
    return {
      ...row,
      id: `${row.namespace}/${name}`,
      name,
      cells: { ...row.cells, 'name-0': { text: name }, 'node-9': { text: node, copy: node } },
    };
  });
  return {
    ...podsAnswers(false),
    [`/api/c/${CLUSTER}/r/pods`]: { ...base, data: { ...base.data, columns, rows, filters: [] } },
    [`/api/c/${CLUSTER}/r/pods/observability/${TALL_POD}`]: {
      state: 'ok',
      data: {
        kind: 'pods',
        namespace: 'observability',
        name: TALL_POD,
        title: 'Pod',
        tabs: [],
        sections: [],
        containers: TALL_CONTAINERS.map(running),
        ports: TALL_CONTAINERS.map((container, at) => ({
          port: 9090 + at,
          name: `http-${String(at)}`,
          protocol: 'TCP',
          container,
          forwardable: true,
        })),
        actions: [{ verb: 'delete', label: 'Delete', permitted: true, destructive: true, irreversible: true }],
      },
    },
  };
}

/** The list with the tall pod open in its drawer, and the same pod as a page. */
export const PODS_TALL_DRAWER = `/c/${CLUSTER}/pods?open=${encodeURIComponent(`observability/${TALL_POD}`)}`;
export const PODS_TALL_PAGE = `/c/${CLUSTER}/pods/observability/${TALL_POD}`;

/**
 * A pod, a service and a secret, each with its whole tab strip and the verbs
 * the server offers that kind (`internal/web/actions.go`, `verbsFor`) - for the
 * drawer's narrowest width. The drawer's minimum is the narrowest
 * width at which its header, its controls row and its tab strip still fit, and
 * these three are what it was measured on. Names are as long as real ones;
 * the name truncates by design at every width.
 */
const FULL_TABS = [
  { key: 'events', label: 'Events' },
  { key: 'changes', label: 'Changes', lazy: true },
  { key: 'manifest', label: 'Manifest', lazy: true },
];

const DELETE = { verb: 'delete', label: 'Delete', permitted: true, destructive: true, irreversible: true };

const DRAWER_OBJECTS = {
  pods: {
    namespace: 'observability',
    name: 'kube-prometheus-stack-prometheus-node-exporter-x7k2p',
    title: 'Pod',
    containers: ['node-exporter'].map(running),
    // `verbsFor("pods")`, in its order.
    actions: [
      { verb: 'restart', label: 'Restart', permitted: true, destructive: true },
      { verb: 'evict', label: 'Evict', permitted: true, destructive: true },
      DELETE,
    ],
  },
  services: {
    namespace: 'kube-system',
    name: 'kube-prometheus-stack-kube-controller-manager',
    title: 'Service',
    actions: [DELETE],
  },
  secrets: {
    namespace: 'kube-system',
    name: 'sh.helm.release.v1.kube-prometheus-stack.v1',
    title: 'Secret',
    actions: [DELETE],
  },
} as const;

export type DrawerKind = keyof typeof DRAWER_OBJECTS;

export const DRAWER_KINDS = Object.keys(DRAWER_OBJECTS) as readonly DrawerKind[];

/** The list each opens from, with that object's drawer open on arrival. */
export function drawerPath(kind: DrawerKind): string {
  const one = DRAWER_OBJECTS[kind];
  return `/c/${CLUSTER}/${kind}?open=${encodeURIComponent(`${one.namespace}/${one.name}`)}`;
}

export function drawerAnswers(): Readonly<Record<string, unknown>> {
  const answers: Record<string, unknown> = {
    ...OVERVIEW_ANSWERS,
    '/api/preferences': PREFERENCES,
    [`/api/c/${CLUSTER}/namespaces`]: NAMESPACES,
  };
  const columns = [column('Name', 0), column('Namespace', 1), column('Age', 2, END)];
  for (const kind of DRAWER_KINDS) {
    const { namespace, name, title, ...rest } = DRAWER_OBJECTS[kind];
    const [nameKey, namespaceKey, ageKey] = columns.map((one) => one.key as string);
    answers[`/api/c/${CLUSTER}/r/${kind}`] = {
      state: 'ok',
      data: {
        kind,
        label: `${title}s`,
        namespaced: true,
        columns,
        rows: [
          {
            id: `${namespace}/${name}`,
            name,
            namespace,
            cells: {
              [nameKey ?? '']: { text: name },
              [namespaceKey ?? '']: { text: namespace },
              [ageKey ?? '']: { text: '130d' },
            },
          },
        ],
      },
    };
    answers[`/api/c/${CLUSTER}/r/${kind}/${namespace}/${name}`] = {
      state: 'ok',
      data: { kind, namespace, name, title, tabs: FULL_TABS, sections: [], ...rest },
    };
  }
  // One route for every kind, the object in its query: the secret's manifest,
  // read-only, as `internal/web` tokenizes it. What it holds is what wrapped
  // mid-word at 390px - a timestamp and a masked value.
  answers[`/api/c/${CLUSTER}/manifest`] = { state: 'ok', data: { lines: SECRET_MANIFEST, editable: false } };
  return answers;
}

/** The timestamp the narrow-width spec reads whole on one line. */
export const MANIFEST_TIMESTAMP = '"2026-11-05T23:23:47Z"';

type Token = { text: string; class: 'plain' | 'key' | 'string' | 'punctuation' };

const SECRET_MANIFEST = (
  [
    [{ text: 'apiVersion', class: 'key' }, { text: ': ', class: 'punctuation' }, { text: 'v1', class: 'plain' }],
    [{ text: 'kind', class: 'key' }, { text: ': ', class: 'punctuation' }, { text: 'Secret', class: 'plain' }],
    [{ text: 'metadata', class: 'key' }, { text: ':', class: 'punctuation' }],
    [
      { text: '  creationTimestamp', class: 'key' },
      { text: ': ', class: 'punctuation' },
      { text: MANIFEST_TIMESTAMP, class: 'string' },
    ],
    [
      { text: '  name', class: 'key' },
      { text: ': ', class: 'punctuation' },
      { text: DRAWER_OBJECTS.secrets.name, class: 'plain' },
    ],
    [{ text: 'data', class: 'key' }, { text: ':', class: 'punctuation' }],
    [
      { text: '  release', class: 'key' },
      { text: ': ', class: 'punctuation' },
      { text: '(hidden - a mask the console draws in place of the value)', class: 'plain' },
    ],
  ] satisfies Token[][]
).map((tokens, at) => ({ number: at + 1, tokens }));

/**
 * A Deployments list and a Cluster roles list, with `internal/resources/kinds.go`'s
 * columns for each: the two lists besides Pods whose short headings were cut at
 * 900px. Values are as short as real ones, which is what made the headings
 * give way.
 */
const DEPLOYMENT_COLUMNS = [
  column('Name', 0),
  column('Namespace', 1),
  column('Ready', 2, END),
  column('Up-to-date', 3, END),
  column('Available', 4, END),
  column('Image', 5),
  column('Age', 6, END),
];

const CLUSTER_ROLE_COLUMNS = [
  column('Name', 0),
  column('Rules', 1, END),
  column('Resources', 2),
  column('Wildcards', 3),
  column('Age', 4, END),
].map((one) => (one.key === 'name-0' ? one : { ...one, role: 'other' }));

function plainRow(name: string, namespace: string | undefined, columns: readonly Record<string, unknown>[], texts: readonly string[]) {
  const cells: Record<string, unknown> = {};
  [name, ...(namespace === undefined ? [] : [namespace]), ...texts].forEach((text, at) => {
    cells[columns[at]?.key as string] = { text };
  });
  return { id: namespace === undefined ? name : `${namespace}/${name}`, name, ...(namespace === undefined ? {} : { namespace }), cells };
}

export function listsAnswers(): Readonly<Record<string, unknown>> {
  return {
    ...OVERVIEW_ANSWERS,
    '/api/preferences': PREFERENCES,
    [`/api/c/${CLUSTER}/namespaces`]: NAMESPACES,
    [`/api/c/${CLUSTER}/r/deployments`]: {
      state: 'ok',
      data: {
        kind: 'deployments',
        label: 'Deployments',
        namespaced: true,
        columns: DEPLOYMENT_COLUMNS,
        rows: [
          plainRow('ledger-api', 'payments', DEPLOYMENT_COLUMNS, ['2/2', '2', '2', 'ghcr.io/example/ledger-api:1.14.2', '3d']),
          plainRow('storefront-web', 'shop-prod', DEPLOYMENT_COLUMNS, ['3/3', '3', '3', 'ghcr.io/example/storefront:4.2.0', '6h']),
        ],
      },
    },
    [`/api/c/${CLUSTER}/r/clusterroles`]: {
      state: 'ok',
      data: {
        kind: 'clusterroles',
        label: 'Cluster roles',
        namespaced: false,
        columns: CLUSTER_ROLE_COLUMNS,
        rows: [
          plainRow('admin', undefined, CLUSTER_ROLE_COLUMNS, ['14', 'pods, services, deployments', '—', '130d']),
          plainRow('view', undefined, CLUSTER_ROLE_COLUMNS, ['9', 'pods, services', '—', '130d']),
        ],
      },
    },
  };
}

/**
 * A very long Pods list, 5000 rows by default: the grid lists every one and
 * draws only the ones in view.
 */
export function podsManyAnswers(length = 5000): Readonly<Record<string, unknown>> {
  const base = podsPage(false);
  const rows = Array.from({ length }, (_, at) => {
    const source = ROWS[at % ROWS.length];
    if (source === undefined) throw new Error('no source row');
    const name = `${source.name}-${String(at).padStart(3, '0')}`;
    return { ...source, id: `${source.namespace}/${name}`, name, cells: { ...source.cells, 'name-0': { text: name } } };
  });
  return {
    ...podsAnswers(false),
    [`/api/c/${CLUSTER}/r/pods`]: { ...base, data: { ...base.data, rows, filters: [] } },
  };
}

export const DEPLOYMENTS_PATH = `/c/${CLUSTER}/deployments`;
export const CLUSTER_ROLES_PATH = `/c/${CLUSTER}/clusterroles`;

/**
 * A pod whose containers' images hold every shape #110 names: a long
 * repository with a tag, a digest alone, a tag and a digest, and no tag at all
 * (the runtime pulls latest). Fabricated; the names are only plausible.
 */
const IMAGE_POD = 'checkout-api-7d9f8c6b5-x2kq';
export const IMAGE_SHAPES: readonly { container: string; image: string }[] = [
  { container: 'api', image: 'registry.internal.example.com:5000/platform-team/payments/checkout-api-service:2.14.3-rc.2' },
  { container: 'envoy', image: 'docker.io/envoyproxy/envoy@sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08' },
  { container: 'log-shipper', image: 'ghcr.io/observability-collective/log-shipper:1.8.0@sha256:2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae' },
  { container: 'debug', image: 'busybox' },
  // A tag as long as a tag may be (128 characters): cut at its end, never past the tile.
  { container: 'nightly', image: `ghcr.io/team/app:${'build-2026-11-25-'.repeat(7)}x`.slice(0, 'ghcr.io/team/app:'.length + 128) },
];

export function podImageAnswers(): Readonly<Record<string, unknown>> {
  return {
    ...podsAnswers(false),
    [`/api/c/${CLUSTER}/r/pods/shop-prod/${IMAGE_POD}`]: {
      state: 'ok',
      data: {
        kind: 'pods',
        namespace: 'shop-prod',
        name: IMAGE_POD,
        title: 'Pod',
        tabs: [],
        sections: [],
        containers: IMAGE_SHAPES.map(({ container, image }) => ({ ...running(container), image })),
        actions: [],
      },
    },
  };
}

export const POD_IMAGE_PAGE = `/c/${CLUSTER}/pods/shop-prod/${IMAGE_POD}`;
