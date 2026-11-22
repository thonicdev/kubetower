import type { ExtensionAbout } from '@kubetower/ui';

import screenshot from './assets/preview.png';

/**
 * What Settings → Extra says about Postgres (CNPG) before it is turned on.
 * The permissions follow the Go half's reads (`extras/cnpg`): keep the two in
 * step when a read is added.
 */
export const about: ExtensionAbout = {
  description:
    'Shows every CloudNativePG cluster at a glance: its primary and replicas, how many instances are ready, switchovers and failovers, ' +
    'whether WAL archiving works, how old the last successful backup is, and when its certificates expire. Tiles filter the clusters ' +
    'that need a look. It reads the operator\'s resources; on a cluster\'s page, Connect reads its statistics and tables, and Read lag runs ' +
    'one fixed, read-only query in the primary. It changes nothing.',
  reads: [
    "CloudNativePG's Clusters, Backups, ScheduledBackups and Poolers, and its Databases, Publications and Subscriptions where served",
    "The instance pods and each cluster's Events",
    "The disk of each instance, from the kubelets of the nodes it runs on, on a cluster's page",
    "Each standby's replication lag, when asked: one fixed psql query on pg_stat_replication, run in the primary's postgres container",
  ],
  permissions: [
    'list clusters, backups, scheduledbackups, poolers, databases, publications, subscriptions (postgresql.cnpg.io)',
    'list pods, events',
    'list customresourcedefinitions (apiextensions.k8s.io)',
    'list nodes and get nodes/proxy, for the disks',
    'create pods/exec, on the primary, for Read lag on a cluster\'s page',
  ],
  requires: ["The CloudNativePG operator. Kubernetes' view role does not grant its resources: a ClusterRole must."],
  // Drawn by `pnpm shots:extras` from the measure harness's fake data.
  screenshot,
};
