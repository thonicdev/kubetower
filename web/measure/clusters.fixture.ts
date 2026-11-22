import { OVERVIEW_ANSWERS, OVERVIEW_CLUSTER } from './overview.fixture';

/**
 * Eight clusters with the names that break a layout, answered from here, for
 * the whole console.
 *
 * **The real long cases**: an EKS context is its ARN,
 * 89 characters here, and kubeadm's default context is
 * `kubernetes-admin@kubernetes`. Each comes with a **twin that differs only at
 * the end** - an `_OWNER` suffix on the ARN, `-1` against `-1_OWNER` - because a
 * name cut at its tail turns a twin into a duplicate, which is the reason the
 * cut is in the middle.
 *
 * `initials` and `colour` are what the server would send for these names
 * (`internal/web/rail.go` `initials()`, `internal/nameplates` `AutoColour()`),
 * copied rather than computed: limit 1 of `measure/main.tsx` applies, and this
 * file is a copy of the wire shape, not a second derivation of it.
 *
 * One cluster is unreachable, so the rail's state badge and the picker's
 * failure card are drawn too.
 */

export const LONG_ARN =
  'arn:aws:eks:eu-west-3:123456789012:cluster/a-very-long-production-cluster-name-for-layout';

const CLUSTERS = [
  { name: LONG_ARN, label: LONG_ARN, initials: 'AV', colour: 'red-pale', state: 'ok' },
  { name: `${LONG_ARN}_OWNER`, label: `${LONG_ARN}_OWNER`, initials: 'AV', colour: 'teal', state: 'ok' },
  { name: 'kubernetes-admin@kubernetes', label: 'kubernetes-admin@kubernetes', initials: 'KA', colour: 'blue-pale', state: 'ok' },
  { name: 'STAGING-EKS-1', label: 'STAGING-EKS-1', initials: 'S1', colour: 'pink', state: 'ok' },
  { name: 'STAGING-EKS-1_OWNER', label: 'STAGING-EKS-1_OWNER', initials: 'S1', colour: 'pink-pale', state: 'ok' },
  { name: 'staging', label: 'Staging', initials: 'S', colour: 'violet', state: 'ok' },
  { name: 'docker-desktop', label: 'docker-desktop', initials: 'DD', colour: 'red-pale', state: 'ok' },
  {
    name: 'edge-lab',
    label: 'edge-lab',
    initials: 'EL',
    colour: 'lime',
    state: 'unreachable',
    reason: { code: 'cluster_unreachable', message: 'dial tcp 10.255.255.1:6443: i/o timeout' },
  },
].map((cluster, order) => ({ ...cluster, order, bar: 1 }));

const OVERVIEW = OVERVIEW_ANSWERS[`/api/c/${OVERVIEW_CLUSTER}/overview`] as {
  state: string;
  data: Record<string, unknown>;
};

const PODS = {
  state: 'ok',
  data: {
    kind: 'pods',
    label: 'Pods',
    refreshSeconds: 5,
    namespaced: true,
    columns: [{ key: 'name', label: 'Name', role: 'name' }],
    rows: [],
    bulk: { scope: 'kind', verbs: [] },
  },
};

/**
 * Path → body, keyed by the **decoded** path: the console encodes a context
 * name into its address (`encodeURIComponent`, so the ARN's `/` and `:` travel
 * escaped), and the shim decodes before it looks up.
 */
export const LONG_NAME_ANSWERS: Readonly<Record<string, unknown>> = {
  '/api/session': OVERVIEW_ANSWERS['/api/session'],
  '/api/auth/methods': OVERVIEW_ANSWERS['/api/auth/methods'],
  '/api/capabilities': { ...(OVERVIEW_ANSWERS['/api/capabilities'] as object), localShell: true },
  '/api/clusters': { state: 'ok', items: CLUSTERS },
  '/api/preferences': {
    columns: { density: 'comfortable', byKind: {} },
    rail: { barCount: 3, activeBar: 1, bars: [CLUSTERS.map((cluster) => cluster.name)], hidden: [] },
  },
  '/api/forwards': { state: 'empty', items: [] },
  // The home reads the machine's notices from here; none.
  '/api/health': { state: 'ok', data: { checks: [] } },
  [`/api/c/${LONG_ARN}/nav`]: OVERVIEW_ANSWERS[`/api/c/${OVERVIEW_CLUSTER}/nav`],
  [`/api/c/${LONG_ARN}/extensions`]: { state: 'empty', items: [] },
  [`/api/c/${LONG_ARN}/overview`]: { ...OVERVIEW, data: { ...OVERVIEW.data, cluster: LONG_ARN } },
  [`/api/c/${LONG_ARN}/r/pods`]: PODS,
};
