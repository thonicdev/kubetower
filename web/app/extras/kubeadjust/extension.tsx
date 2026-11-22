import { lazy } from 'react';
import { Pending, type Extension } from '@kubetower/ui';

import { about } from './about';
import { KEY } from './data';

/**
 * The TypeScript half of this extra, satisfying `@kubetower/ui`'s `Extension`.
 * `app/main.tsx` hands it to `createApp`, and the console mounts `routes` under
 * `/c/:cluster` only.
 *
 * One page, under the `x` segment the console reserves for an extra's page, in
 * its own lazily loaded chunk. Its three views are the page's own state, in the
 * address (`?view=nodes`, `?ns=…`). The Go route answers JSON at
 * `/x/{cluster}/kubeadjust/`, a different address (see the argocd extra's
 * extension.tsx for why the two must not coincide).
 *
 * And two tabs, drawn by the core while this extension is on for the cluster:
 * in a Node's panel, the node's requests, limits and usage, its pods and the
 * recommendations that concern it; in a Deployment's, StatefulSet's,
 * DaemonSet's or CronJob's, that workload's card from the page - its bars, its findings with
 * their confidence, the kills remembered - and its commands.
 */
export const PAGE_PATH = `x/${KEY}`;

export const kubeadjust: Extension = {
  key: KEY,
  about,
  nav: { label: 'KubeAdjust', path: PAGE_PATH },
  routes: [
    {
      path: PAGE_PATH,
      lazy: async () => ({ Component: (await import('./KubeAdjust')).KubeAdjust }),
      hydrateFallbackElement: <Pending what="this page" />,
    },
  ],
  tabs: [
    {
      key: 'node',
      label: 'KubeAdjust',
      kinds: ['nodes'],
      Component: lazy(() => import('./NodeTab')),
    },
    {
      key: 'workload',
      label: 'KubeAdjust',
      kinds: ['deployments', 'statefulsets', 'daemonsets', 'cronjobs'],
      Component: lazy(() => import('./WorkloadTab')),
    },
  ],
};
