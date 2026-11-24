import { lazy } from 'react';
import { Pending as Waiting, type Extension } from '@kubetower/ui';

import { about } from './about';
import { KEY, OBJECT_KINDS } from './data';

/**
 * The TypeScript half of this extra, satisfying `@kubetower/ui`'s `Extension`.
 * `app/main.tsx` hands it to `createApp`, and the console mounts `routes`
 * under `/c/:cluster` only.
 *
 * One page, under the `x` segment the console reserves for an extra's page,
 * with an entry in the menu, and, drawn by the core while this extension is on
 * for the cluster, its view in the Activity tab of a Pod, Deployment,
 * StatefulSet, DaemonSet, ReplicaSet and Job, in place of the core's event
 * lines. The Go routes answer JSON at
 * `/x/{cluster}/events/…`, a different address (see the argocd extra's
 * extension.tsx for why the two must not coincide).
 */
export const PAGE_PATH = `x/${KEY}`;

export const events: Extension = {
  key: KEY,
  icon: 'pulse',
  about,
  nav: { label: 'Event Intelligence', path: PAGE_PATH },
  routes: [
    {
      path: PAGE_PATH,
      lazy: async () => ({ Component: (await import('./EventsPage')).EventsPage }),
      hydrateFallbackElement: <Waiting what="this page" />,
    },
  ],
  // The timeline and the causes narrowed to the object, in the Activity tab.
  activity: { kinds: OBJECT_KINDS, Component: lazy(() => import('./ActivityView')) },
};
