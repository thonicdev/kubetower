// Settings › General's Monitoring tile, one scene per answer, on the desktop
// and under the server profile. Every name is made up.

const SETTINGS = {
  general: { archiveEnabled: true, captureLogs: false, retentionDays: 7 },
  retentionChoices: [
    { days: 1, label: '1 day' },
    { days: 7, label: '7 days' },
    { days: 30, label: '30 days' },
  ],
  archive: { state: 'ok', data: { path: '/state/archive', bytes: 4194304, days: 6, oldestAt: '2026-11-19T00:00:00Z', writesRefused: false, note: '' } },
  nodeShell: { enabled: true, available: true, image: 'alpine:3.24', defaultImage: 'alpine:3.24' },
  info: {
    statePath: '/state',
    statePersistent: true,
    kubeconfigFiles: ['/kube/config'],
    listenAddress: '127.0.0.1:5823',
    withoutPassword: true,
    version: 'kubetower 0.2.2 (2026-11-25, linux/amd64)',
    development: false,
    repositoryUrl: 'https://github.com/thonicdev/kubetower',
    documentationUrl: 'https://thonicdev.github.io/kubetower-site/docs/',
  },
};

const SESSION = { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' };

const CLUSTERS = {
  state: 'ok',
  items: [
    { name: 'prod-eu', label: 'prod-eu', state: 'ok' },
    { name: 'staging', label: 'staging', state: 'ok' },
  ],
};

/** What prod-eu's Monitoring tile reads, one per case. */
const CASES: Readonly<Record<string, Record<string, unknown>>> = {
  // Discovery found Prometheus: the badge alone.
  prometheus: {
    source: '',
    editable: true,
    kind: 'prometheus',
    engine: 'prometheus',
    address: 'monitoring/kps-prometheus:9090',
    transport: 'port-forward',
    detail: 'service monitoring/kps-prometheus:9090 found via app=kube-prometheus-stack-prometheus, reached by port-forward',
  },
  // Discovery found VictoriaMetrics: the badge alone.
  victoriametrics: {
    source: '',
    editable: true,
    kind: 'prometheus',
    engine: 'victoriametrics',
    address: 'vm/vmsingle-main:8429',
    transport: 'proxy',
    detail: 'service vm/vmsingle-main:8429 found via app.kubernetes.io/name=vmsingle, reached by proxy, answered by VictoriaMetrics',
  },
  'metrics-server': {
    source: '',
    editable: true,
    kind: 'metrics-server',
    detail: 'metrics-server answers; instant usage available, no history',
  },
  // Nothing found: the field.
  none: {
    source: '',
    editable: true,
    kind: 'none',
    detail: 'the cluster answered: no Prometheus service matched, and metrics.k8s.io is not registered',
  },
  // A source set by hand: the field, holding it, so it can be cleared.
  manual: {
    source: 'vm/vmselect-main:8481/select/0/prometheus',
    editable: true,
    kind: 'prometheus',
    engine: 'victoriametrics',
    address: 'vm/vmselect-main:8481/select/0/prometheus',
    transport: 'proxy',
    detail: 'service vm/vmselect-main:8481/select/0/prometheus found via the source set in Settings, reached by proxy, answered by VictoriaMetrics',
  },
};

export const MONITORING_CASES = Object.keys(CASES);

export function monitoringAnswers(which: string): Record<string, unknown> {
  if (which === 'server') {
    // The server profile: one cluster, the one the console runs in, whose
    // source is the deployment's KT_PROMETHEUS_SERVICE, read only.
    return {
      '/api/session': SESSION,
      '/api/capabilities': { mode: 'shared' },
      '/api/clusters': { state: 'ok', items: [{ name: 'in-cluster', label: 'in-cluster', state: 'ok' }] },
      '/api/settings': SETTINGS,
      '/api/c/in-cluster/metrics/source': {
        source: 'vm/vmsingle-main:8428',
        editable: false,
        kind: 'prometheus',
        engine: 'victoriametrics',
        address: 'vm/vmsingle-main:8428',
        transport: 'proxy',
        detail: 'service vm/vmsingle-main:8428 found via KT_PROMETHEUS_SERVICE, reached by proxy, answered by VictoriaMetrics',
      },
    };
  }
  return {
    '/api/session': SESSION,
    '/api/clusters': CLUSTERS,
    '/api/settings': SETTINGS,
    '/api/c/prod-eu/metrics/source': CASES[which],
  };
}
