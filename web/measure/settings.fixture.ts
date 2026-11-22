import type { ExtensionInfo } from '../ui/src/api';

/** The Settings > Extra tab's listing, as the console answers it for one
 *  cluster: every extra the build carries, labels as the Go side spells them. */
const row = (key: string, label: string, summary: string, enabled: boolean, settings: ExtensionInfo['settings'] = []): ExtensionInfo => ({
  key,
  label,
  summary,
  enabled,
  pinned: false,
  writable: true,
  settings,
});

const text = (key: string, label: string, placeholder: string): ExtensionInfo['settings'][number] => ({
  key,
  label,
  value: false,
  default: false,
  kind: 'text',
  placeholder,
});

/** Deliberately out of order: the tab sorts by label itself. */
export const SETTINGS_EXTRA_ITEMS: readonly ExtensionInfo[] = [
  row('argocd', 'Delivery Center', 'Argo CD applications and their drift', true, [
    { key: 'list-column', label: 'Show the Argo CD column in the lists', value: false, default: false },
    {
      key: 'tool', label: 'Tool', kind: 'choice', value: false, default: false, placeholder: 'Auto-detect', text: 'auto',
      options: [
        { value: 'auto', label: 'Auto-detect' },
        { value: 'argocd', label: 'Argo CD' },
        { value: 'flux', label: 'Flux' },
        { value: 'both', label: 'Both' },
      ],
    },
    { ...text('argocd-url', 'Argo CD URL', 'https://argocd.example.com'), text: 'https://argocd.example.com' },
    text('flux-url', 'Flux (Weave GitOps) URL', 'https://gitops.example.com'),
  ]),
  row('kubecron', 'KubeCron', 'Whether each CronJob runs when it should', true, [
    text('collector', 'Collector', 'found by itself'),
    text('token-secret', 'Token Secret', 'kubecron-api'),
  ]),
  row('kubeadjust', 'KubeAdjust', 'Whether requests and limits fit what pods use', false),
  row('redis', 'Redis viewer', 'Redis and Valkey Services, read only', false),
  row('jvm', 'JVM Inspector', 'Heap, threads and GC of Java pods', false),
  row('netinspect', 'Network Inspector', 'What NetworkPolicies allow', false),
  row('pending', 'Pod Debugging', 'Why a pod waits', true),
  row('cnpg', 'Postgres (CNPG)', 'CloudNativePG clusters', false),
  row('certmanager', 'Cert Manager', 'Certificates and their expiry', true),
  row('security', 'Security Center', 'Vulnerabilities from trivy-operator', false),
];
