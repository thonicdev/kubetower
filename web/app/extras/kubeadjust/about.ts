import type { ExtensionAbout } from '@kubetower/ui';

import screenshot from './assets/preview.png';

/**
 * What Settings → Extra says about KubeAdjust before it is turned on. The
 * permissions follow the Go half's reads (`extras/kubeadjust`) and the core's
 * metrics reads it asks for: keep the two in step when a read is added.
 */
export const about: ExtensionAbout = {
  description:
    "Says whether each workload's CPU and memory requests and limits are the right size, from what its containers " +
    'actually use, and grades how sure each recommendation is. It writes the kubectl commands that would fix them, ' +
    'for you to review and run by hand: nothing is changed in the cluster from here. Nodes get a KubeAdjust tab ' +
    'with their requests, limits, usage and pods, and Deployments, StatefulSets, DaemonSets and CronJobs one with their own ' +
    'recommendations.',
  reads: [
    'Pods, and the ReplicaSets and Jobs that own them',
    'The Deployment, StatefulSet, DaemonSet or CronJob whose tab is open',
    'Nodes, namespaces and PersistentVolumeClaims',
    'HorizontalPodAutoscalers and VerticalPodAutoscalers',
    "Each container's CPU and memory now, from metrics-server",
    'Seven days of CPU and memory, from Prometheus',
    "Each node's disk figures, from its kubelet",
  ],
  permissions: [
    'list pods, nodes, namespaces, persistentvolumeclaims',
    'list replicasets (apps), jobs (batch)',
    'list deployments, statefulsets, daemonsets (apps), cronjobs (batch), for the tab in their panel',
    'list horizontalpodautoscalers (autoscaling), verticalpodautoscalers (autoscaling.k8s.io)',
    'list pods (metrics.k8s.io)',
    'get nodes/proxy, for disk figures',
    "list services, and get services/proxy (or create pods/portforward) on Prometheus's Service",
  ],
  requires: [
    'metrics-server, for usage',
    'Prometheus, for a seven-day window (without it, one sample per pod)',
  ],
  // Drawn by `pnpm shots:extras` from the measure harness's fake data.
  screenshot,
};
