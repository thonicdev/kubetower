import type { ReactNode } from 'react';
import { CopyButton, focusRing } from '@kubetower/ui';

const DOCS = 'https://coredns.io/plugins/metrics/';

const MONITOR = `apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata: {name: coredns, namespace: kube-system}
spec:
  selector: {matchLabels: {k8s-app: kube-dns}}
  namespaceSelector: {matchNames: [kube-system]}
  endpoints: [{port: metrics}]`;

const SCRAPE = `- job_name: coredns
  kubernetes_sd_configs:
    - role: pod
  relabel_configs:
    - source_labels:
        - __meta_kubernetes_pod_label_k8s_app
        - __meta_kubernetes_pod_container_port_number
      regex: kube-dns;9153
      action: keep`;

function Snippet({ title, code }: { title: string; code: string }): ReactNode {
  return (
    <div className="flex flex-col gap-1">
      <p className="text-xs text-muted">{title}</p>
      <div className="flex items-start gap-2">
        <pre className="flex-1 overflow-x-auto rounded-md bg-panel p-2 font-mono text-xs">{code}</pre>
        <CopyButton value={code} label={`Copy the ${title}`} />
      </div>
    </div>
  );
}

/**
 * The one explanatory text on this page, closed by default: what turns the
 * metrics on. Shown only where CoreDNS was found and no series came back.
 */
export function EnableMetrics(): ReactNode {
  return (
    <details data-enable-metrics="" className="max-w-3xl text-sm">
      <summary className={`cursor-pointer list-none rounded-sm text-accent [&::-webkit-details-marker]:hidden ${focusRing}`}>Enable metrics</summary>
      <div className="mt-2 flex flex-col gap-3">
        <p>
          CoreDNS must load the <code className="font-mono">prometheus :9153</code> plugin in its Corefile, and Prometheus must scrape its pods on that
          port.
        </p>
        <Snippet title="ServiceMonitor" code={MONITOR} />
        <Snippet title="scrape_config" code={SCRAPE} />
        <a href={DOCS} target="_blank" rel="noreferrer" className="text-accent underline">
          CoreDNS metrics plugin
        </a>
      </div>
    </details>
  );
}
