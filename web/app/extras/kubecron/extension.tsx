import { lazy } from 'react';
import { Pending, type Extension } from '@kubetower/ui';

import { about } from './about';
import { KEY } from './data';

/**
 * The TypeScript half of this extra, satisfying `@kubetower/ui`'s `Extension`.
 * `app/main.tsx` hands it to `createApp`, and the console mounts
 * `routes` under `/c/:cluster` only.
 *
 * Three pages, all under the `x` segment the console reserves for an extra's
 * page: the board with its heatmap, one CronJob's history, and one run's log
 * full-page. The Go routes answer JSON at `/x/{cluster}/kubecron/…`, a different
 * address (see the argocd extra's extension.tsx for why the two must not
 * coincide).
 *
 * And one tab, in the panel of a CronJob, a Job or a Pod, drawn by the
 * core while this extension is on for the cluster.
 */
export const PAGE_PATH = `x/${KEY}`;

export const kubecron: Extension = {
  key: KEY,
  about,
  nav: { label: 'KubeCron', path: PAGE_PATH },
  routes: [
    {
      path: PAGE_PATH,
      lazy: async () => ({ Component: (await import('./KubeCron')).KubeCron }),
      hydrateFallbackElement: <Pending what="this page" />,
    },
    {
      path: `${PAGE_PATH}/:namespace/:name`,
      lazy: async () => ({ Component: (await import('./CronJobPage')).CronJobPage }),
      hydrateFallbackElement: <Pending what="this page" />,
    },
    {
      // One run's log, full-page. Three segments, so no
      // namespace and name can be taken for it.
      path: `${PAGE_PATH}/runs/:id/log`,
      lazy: async () => ({ Component: (await import('./LogPage')).LogPage }),
      hydrateFallbackElement: <Pending what="this log" />,
    },
  ],
  // The diagnosis in this extra's tile on the Settings page.
  SettingsStatus: lazy(() => import('./CollectorStatus')),
  tabs: [
    {
      key: 'runs',
      label: 'KubeCron',
      kinds: ['cronjobs', 'jobs'],
      Component: lazy(() => import('./KubeCronTab')),
    },
    {
      // A Pod only when a Job made it: the tab on every Pod said "not started
      // by a Job" on all the others. Same key, so the
      // address stays x.kubecron.runs.
      key: 'runs',
      label: 'KubeCron',
      kinds: ['pods'],
      when: ({ labels }) => 'job-name' in labels || 'batch.kubernetes.io/job-name' in labels,
      Component: lazy(() => import('./KubeCronTab')),
    },
  ],
};
