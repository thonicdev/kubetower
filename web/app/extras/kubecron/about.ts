import type { ExtensionAbout } from '@kubetower/ui';

import screenshot from './assets/preview.png';

/**
 * What Settings → Extra says about KubeCron before it is turned on. The
 * permissions follow the Go half's reads (`extras/kubecron`): keep the two in
 * step when a read is added.
 */
export const about: ExtensionAbout = {
  description:
    'Shows whether each CronJob runs when it should, on a board and a heatmap of the days. Each CronJob opens on ' +
    'its history: the outcome, duration, log and resource use of its past runs. CronJobs, Jobs and their Pods get a ' +
    'KubeCron tab. It reads only: nothing is changed in the cluster from here.',
  reads: [
    'CronJobs and the Jobs they started',
    'The Pods of those Jobs, and their logs where you may read them',
    "A KubeCron collector's run history, through a tunnel to its Service",
  ],
  permissions: [
    'list cronjobs, jobs (batch)',
    'list pods',
    "get pods/log, to show a run's log",
    'list services (to find the collector), get services and create pods/portforward on it',
    "get secrets on the collector's token Secret, where one is configured",
  ],
  requires: [
    'CronJobs in the cluster',
    'For history past what the cluster keeps: a KubeCron collector (github.com/schmitech-fr/kubecron, mode=server)',
  ],
  // Drawn by `pnpm shots:extras` from the measure harness's fake data.
  screenshot,
};
