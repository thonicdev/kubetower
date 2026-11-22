import type { RuleSet } from '../ui/src/api';

/**
 * Invented RBAC rule sets for the `rbac` scene: a short ClusterRole,
 * cluster-admin, a long controller role, an aggregated ClusterRole and a
 * namespaced Role. Shaped on what such roles look like; no cluster's real role.
 */

const READ = ['get', 'list', 'watch'];
const WRITE = ['create', 'update', 'patch', 'delete'];

export const RBAC_CASES: Record<string, RuleSet> = {
  short: {
    state: 'ok',
    rules: [
      { apiGroups: [''], resources: ['pods', 'pods/log', 'services', 'endpoints'], verbs: READ },
      { apiGroups: ['apps'], resources: ['deployments', 'replicasets'], verbs: READ },
      { nonResourceURLs: ['/healthz', '/version'], verbs: ['get'] },
    ],
  },
  'cluster-admin': {
    state: 'ok',
    rules: [
      { apiGroups: ['*'], resources: ['*'], verbs: ['*'], wildcards: 'api groups, resources, verbs', wildcardTone: 'bad' },
      { nonResourceURLs: ['*'], verbs: ['*'], wildcards: 'verbs', wildcardTone: 'warn' },
    ],
  },
  long: {
    state: 'ok',
    rules: [
      { apiGroups: [''], resources: ['secrets'], verbs: ['create', 'delete', 'get', 'update'] },
      { apiGroups: [''], resources: ['serviceaccounts'], verbs: ['get', 'list', 'watch', 'create'] },
      { apiGroups: [''], resources: ['serviceaccounts/token'], verbs: ['create'] },
      { apiGroups: [''], resources: ['configmaps'], verbs: [...READ, ...WRITE] },
      { apiGroups: [''], resources: ['events'], verbs: ['create', 'patch', 'update'] },
      {
        apiGroups: [''],
        resources: ['pods', 'services', 'endpoints', 'namespaces', 'nodes', 'persistentvolumes', 'persistentvolumeclaims', 'replicationcontrollers', 'resourcequotas', 'limitranges'],
        verbs: READ,
      },
      { apiGroups: ['apps'], resources: ['deployments', 'statefulsets', 'daemonsets', 'replicasets'], verbs: [...READ, ...WRITE] },
      { apiGroups: ['batch'], resources: ['jobs', 'cronjobs'], verbs: [...READ, ...WRITE] },
      { apiGroups: ['argoproj.io'], resources: ['applications', 'appprojects', 'applicationsets'], verbs: ['*'], wildcards: 'verbs', wildcardTone: 'warn' },
      { apiGroups: ['networking.k8s.io'], resources: ['ingresses', 'networkpolicies'], verbs: READ },
      { apiGroups: ['rbac.authorization.k8s.io'], resources: ['roles', 'rolebindings'], verbs: ['bind', 'escalate', 'get', 'list'] },
      { apiGroups: ['rbac.authorization.k8s.io'], resources: ['clusterroles', 'clusterrolebindings'], verbs: ['get', 'list', 'watch'] },
      { apiGroups: ['authentication.k8s.io'], resources: ['users', 'groups'], verbs: ['impersonate'] },
      { apiGroups: ['coordination.k8s.io'], resources: ['leases'], verbs: [...READ, ...WRITE] },
      { apiGroups: ['storage.k8s.io'], resources: ['storageclasses', 'volumeattachments', 'csinodes', 'csidrivers'], verbs: READ },
      { apiGroups: ['policy'], resources: ['poddisruptionbudgets'], verbs: READ },
      { apiGroups: ['admissionregistration.k8s.io'], resources: ['validatingwebhookconfigurations', 'mutatingwebhookconfigurations'], verbs: READ },
      { apiGroups: ['apiextensions.k8s.io'], resources: ['customresourcedefinitions'], verbs: READ },
      { apiGroups: ['metrics.k8s.io'], resources: ['pods', 'nodes'], verbs: ['get', 'list'] },
      { apiGroups: ['events.k8s.io'], resources: ['events'], verbs: ['create', 'patch'] },
      { apiGroups: [''], resources: ['configmaps'], resourceNames: ['argocd-cm', 'argocd-rbac-cm', 'argocd-cmd-params-cm', 'argocd-gpg-keys-cm'], verbs: ['get', 'update'] },
      { nonResourceURLs: ['/healthz', '/healthz/*', '/metrics', '/version'], verbs: ['get'] },
    ],
  },
  aggregated: {
    state: 'ok',
    rules: [
      { apiGroups: [''], resources: ['pods', 'services', 'configmaps'], verbs: READ },
      { apiGroups: ['apps'], resources: ['deployments'], verbs: READ },
      { apiGroups: ['batch'], resources: ['jobs'], verbs: READ },
    ],
    selectors: [['rbac.example.com/aggregate-to-view=true'], ['rbac.example.com/tier in (platform,data)', '!rbac.example.com/hidden']],
    note: 'This ClusterRole is aggregated: these rules are written by the controller from the roles its label selector matches, and they change when those roles do.',
  },
  role: {
    state: 'ok',
    rules: [
      { apiGroups: [''], resources: ['configmaps'], verbs: READ },
      { apiGroups: [''], resources: ['secrets'], resourceNames: ['db-credentials', 'tls-cert'], verbs: ['get'] },
      { apiGroups: ['apps'], resources: ['deployments'], resourceNames: ['checkout-api'], verbs: ['get', 'patch'] },
    ],
  },
};
