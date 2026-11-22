import type { ExtensionAbout } from '@kubetower/ui';

import screenshot from './assets/preview.png';

/**
 * What Settings → Extra says about Pod Debugging before it is turned on. The
 * permissions follow the Go half's reads (`extras/pending`): keep the two in
 * step when a read is added.
 */
export const about: ExtensionAbout = {
  description:
    'Shows why each Pending pod waits - not enough CPU or memory, a taint, a node selector, pod affinity, a volume, a quota - ' +
    'in tiles that filter the list. Each pod opens on a node-fit matrix, the room on the closest nodes with its request laid ' +
    'over them, what it would take to start it, and its events on a timeline. A pending pod gets a Pod Debugging tab and tile in its panel, ' +
    'and the Pods list a Pending column. It reads only: nothing is changed in the cluster.',
  reads: [
    "Pending pods, and the scheduler's message each one carries",
    'Nodes, and the pods placed on them, to count what is left',
    'Events: FailedCreate for the workloads a quota refused, and one pod’s own events',
    'ResourceQuotas, and the claims, volumes and storage classes of pending pods',
  ],
  permissions: [
    'list pods',
    'list nodes (without it, only the scheduler’s reasons are shown)',
    'list events',
    'list resourcequotas',
    'list persistentvolumeclaims, persistentvolumes, storageclasses (storage.k8s.io)',
  ],
  requires: [],
  // Drawn by `pnpm shots:extras` from the measure harness's fake data.
  screenshot,
};
