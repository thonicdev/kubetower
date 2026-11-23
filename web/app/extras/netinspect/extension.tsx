import { lazy } from 'react';
import { Pending, type Extension } from '@kubetower/ui';

import { about } from './about';
import { KEY, PAGE_PATH, isolationCells, podShortcut } from './data';

/**
 * The TypeScript half of the Network Inspector, satisfying `@kubetower/ui`'s
 * `Extension`. `app/main.tsx` hands it to `createApp`, and the console mounts
 * `routes` under `/c/:cluster` only.
 *
 * One page, under the `x` segment the console reserves for an extra's page, in
 * its own lazily loaded chunk. The Go routes answer JSON at
 * `/x/{cluster}/netinspect/…`, a different address.
 *
 * And, on a pod, drawn by the core while the extra is on for the cluster: a
 * Network tab in its panel (its isolation each way, the policies that select
 * it, a small graph of whom their rules allow), a tile on its Details, and an
 * Isolation column in the Pods list - all from the rules, never traffic.
 */
export const netinspect: Extension = {
  key: KEY,
  icon: 'topology',
  about,
  nav: { label: 'Network Inspector', path: PAGE_PATH },
  routes: [
    {
      path: PAGE_PATH,
      lazy: async () => ({ Component: (await import('./NetInspect')).NetInspect }),
      hydrateFallbackElement: <Pending what="this page" />,
    },
    {
      path: `${PAGE_PATH}/pods/:namespace/:name`,
      lazy: async () => ({ Component: (await import('./PodTab')).PodPage }),
      hydrateFallbackElement: <Pending what="this page" />,
    },
  ],
  tabs: [{ key: 'pod', label: 'Network', kinds: ['pods'], Component: lazy(() => import('./PodTab')) }],
  shortcuts: [{ key: 'pod', kinds: ['pods'], resolve: podShortcut }],
  columns: [{ key: 'isolation', label: 'Isolation', kinds: ['pods'], cells: isolationCells }],
};
