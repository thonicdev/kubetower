import { lazy } from 'react';
import { Pending as Waiting, type Extension } from '@kubetower/ui';

import { about } from './about';
import { KEY, WORKLOAD_KINDS } from './data';
import { pendingCells, pendingProbe, pendingShortcut, workloadProbe } from './panel';

/**
 * The TypeScript half of this extra, satisfying `@kubetower/ui`'s `Extension`.
 * `app/main.tsx` hands it to `createApp`, and the console mounts `routes`
 * under `/c/:cluster` only.
 *
 * Two pages, under the `x` segment the console reserves for an extra's page:
 * every pending pod, and one pod's view. The Go routes answer JSON at
 * `/x/{cluster}/pending/…`, a different address (see the argocd extra's
 * extension.tsx for why the two must not coincide).
 *
 * And, drawn by the core while this extension is on for the cluster: a tab
 * and a shortcut tile in a pending pod's panel, a tab in the panel of a
 * Deployment, StatefulSet, DaemonSet or Job with pending pods, and a column in
 * the lists of those kinds and of Pods.
 */
export const PAGE_PATH = `x/${KEY}`;

export const pending: Extension = {
  key: KEY,
  about,
  // No menu entry: a Pending pod's tile opens the window, which leads to the page.
  nav: { label: 'Pod Debugging', path: PAGE_PATH, menu: false },
  routes: [
    {
      path: PAGE_PATH,
      lazy: async () => ({ Component: (await import('./PendingPage')).PendingPage }),
      hydrateFallbackElement: <Waiting what="this page" />,
    },
    {
      path: `${PAGE_PATH}/:namespace/:name`,
      lazy: async () => ({ Component: (await import('./PendingPage')).PodPage }),
      hydrateFallbackElement: <Waiting what="this pod" />,
    },
  ],
  tabs: [
    {
      key: 'why',
      label: 'Pod Debugging',
      kinds: ['pods'],
      probe: pendingProbe,
      Component: lazy(() => import('./PodTab')),
    },
    {
      key: 'workload',
      label: 'Pending',
      kinds: WORKLOAD_KINDS,
      probe: workloadProbe,
      Component: lazy(() => import('./WorkloadTab')),
    },
  ],
  shortcuts: [{ key: 'why', kinds: ['pods'], resolve: pendingShortcut, dialog: lazy(() => import('./DebugWindow')) }],
  columns: [{ key: 'reason', label: 'Pending', kinds: ['pods', ...WORKLOAD_KINDS], cells: pendingCells }],
};
