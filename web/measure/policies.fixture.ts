import type { PolicyCounts, PolicyDetail, PolicyOverview, PolicyResource, PolicyRow } from '../app/extras/security/policyData';

/**
 * The Security Center's Policies view on an invented cluster running Kyverno
 * and Gatekeeper: the `extra-security-policies` and `extra-security-policy`
 * scenes, photographed by `shot-extras.mjs`. Typed against `policyData.ts`.
 */

const iso = (ms: number): string => new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z');
const NOW = Date.now();
const pc = (fail: number, error: number, warn: number, audit = 0): PolicyCounts => ({ fail, error, warn, audit });
const sum = (xs: readonly PolicyCounts[]): PolicyCounts =>
  xs.reduce((a, b) => pc(a.fail + b.fail, a.error + b.error, a.warn + b.warn, a.audit + b.audit), pc(0, 0, 0));

const policies: PolicyRow[] = [
  { engine: 'kyverno', name: 'require-run-as-nonroot', mode: 'Audit', category: 'Pod Security Standards (Restricted)', severity: 'medium', counts: pc(14, 0, 0, 14), resources: 14 },
  { engine: 'gatekeeper', name: 'k8srequiredlabels.must-have-owner', kind: 'K8sRequiredLabels', mode: 'deny', counts: pc(11, 0, 0), resources: 11, listed: 11, violations: 11 },
  { engine: 'kyverno', name: 'disallow-latest-tag', mode: 'Enforce', category: 'Best Practices', severity: 'medium', counts: pc(6, 0, 0), resources: 6 },
  { engine: 'kyverno', name: 'require-requests-limits', mode: 'Audit', category: 'Best Practices', severity: 'medium', counts: pc(5, 0, 3, 5), resources: 8 },
  { engine: 'gatekeeper', name: 'k8sallowedrepos.trusted-registries', kind: 'K8sAllowedRepos', mode: 'dryrun', counts: pc(4, 0, 0, 4), resources: 4, listed: 4, violations: 4 },
  { engine: 'kyverno', name: 'disallow-privileged-containers', mode: 'Enforce', category: 'Pod Security Standards (Baseline)', severity: 'high', counts: pc(2, 0, 0), resources: 2 },
  { engine: 'kyverno', name: 'restrict-image-registries', mode: 'Mixed', category: 'Best Practices', severity: 'high', counts: pc(0, 1, 2), resources: 3 },
  { engine: 'gatekeeper', name: 'k8spspallowprivilegeescalation.no-escalation', kind: 'K8sPSPAllowPrivilegeEscalation', mode: 'warn', counts: pc(0, 0, 3, 3), resources: 3, listed: 3, violations: 3 },
];

const res = (namespace: string, kind: string, name: string, counts: PolicyCounts, n: number, workload?: string): PolicyResource => ({
  namespace,
  kind,
  name,
  counts,
  policies: n,
  ...(workload === undefined ? {} : { workload: { namespace, kind: workload.split('/')[0]!, name: workload.split('/')[1]! } }),
});

const resources: PolicyResource[] = [
  res('shop', 'Deployment', 'checkout-api', pc(4, 0, 1, 2), 5, 'Deployment/checkout-api'),
  res('shop', 'Deployment', 'storefront', pc(3, 0, 1, 2), 4, 'Deployment/storefront'),
  res('payments', 'StatefulSet', 'ledger', pc(3, 1, 0, 1), 4, 'StatefulSet/ledger'),
  res('ingress-nginx', 'DaemonSet', 'ingress-nginx-controller', pc(3, 0, 1, 1), 4, 'DaemonSet/ingress-nginx-controller'),
  res('data', 'StatefulSet', 'orders-db', pc(2, 0, 1, 2), 3, 'StatefulSet/orders-db'),
  res('payments', 'Deployment', 'gateway', pc(2, 0, 1, 1), 3, 'Deployment/gateway'),
  res('monitoring', 'Deployment', 'grafana', pc(2, 0, 0, 2), 2, 'Deployment/grafana'),
  res('shop', 'Pod', 'catalogue-6d8f9-k2x4q', pc(2, 0, 0, 2), 2, 'Deployment/catalogue'),
  res('data', 'StatefulSet', 'sessions-redis', pc(1, 0, 1, 1), 2, 'StatefulSet/sessions-redis'),
  res('tools', 'Job', 'backup-28990320', pc(1, 0, 0), 1, 'CronJob/backup'),
  res('kube-system', 'DaemonSet', 'node-exporter', pc(0, 0, 2, 2), 2, 'DaemonSet/node-exporter'),
];

const namespaces = [...new Set(resources.map((r) => r.namespace ?? ''))].map((ns) => {
  const mine = resources.filter((r) => r.namespace === ns);
  return { namespace: ns, counts: sum(mine.map((r) => r.counts)), resources: mine.length };
});
namespaces.sort((a, b) => b.counts.fail + b.counts.error + b.counts.warn - (a.counts.fail + a.counts.error + a.counts.warn));

export const SECURITY_POLICIES: PolicyOverview = {
  cluster: 'production',
  read: iso(NOW),
  engines: true,
  sources: [
    { kind: 'policyreports.wgpolicyk8s.io', state: 'read', complete: true, reports: 64 },
    { kind: 'reports.openreports.io', state: 'absent', complete: false, reports: 0 },
    { kind: 'clusterpolicyreports.wgpolicyk8s.io', state: 'read', complete: true, reports: 3 },
    { kind: 'clusterreports.openreports.io', state: 'absent', complete: false, reports: 0 },
    { kind: 'constraints.gatekeeper.sh', state: 'read', complete: true, reports: 3 },
    { kind: 'clusterpolicies.kyverno.io', state: 'read', complete: true, reports: 6 },
    { kind: 'policies.kyverno.io', state: 'read', complete: true, reports: 0 },
    { kind: 'validatingpolicies.policies.kyverno.io', state: 'absent', complete: false, reports: 0 },
  ],
  totals: sum(policies.map((p) => p.counts)),
  failing: resources.filter((r) => r.counts.fail + r.counts.error > 0).length,
  namespaces,
  policies,
  policiesTotal: policies.length,
  resources,
  resourcesTotal: resources.length,
  notes: [],
};

const NONROOT = 'validation error: Running as root is not allowed. The fields spec.securityContext.runAsNonRoot and spec.containers[*].securityContext.runAsNonRoot must be set to true.';

export const SECURITY_POLICY: PolicyDetail = {
  cluster: 'production',
  read: iso(NOW),
  sources: SECURITY_POLICIES.sources,
  policy: policies[0]!,
  results: resources
    .filter((r) => r.kind !== 'Job')
    .slice(0, 9)
    .map((r) => ({
      engine: 'kyverno',
      policy: 'require-run-as-nonroot',
      rule: r.kind === 'Pod' ? 'run-as-non-root' : 'autogen-run-as-non-root',
      result: 'fail' as const,
      severity: 'medium',
      category: 'Pod Security Standards (Restricted)',
      message: NONROOT,
      namespace: r.namespace ?? '',
      kind: r.kind,
      name: r.name,
      audit: true,
    })),
  total: 14,
  notes: [],
};
