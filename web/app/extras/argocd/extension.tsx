import { lazy } from 'react';
import { Pending, type Extension } from '@kubetower/ui';

import { about } from './about';
import { argoColumn, managedHere, managedThroughOwner, marked } from './tracked';

/**
 * The TypeScript half of this extra, satisfying `@kubetower/ui`'s `Extension`.
 *
 * `app/main.tsx` hands it to `createApp(root, [argocd, kubecron, redis])`; the console
 * mounts `routes` under `/c/:cluster` and nowhere else (`router/routes.tsx` in
 * the core), so an extension cannot place a page outside the cluster scope.
 */

/** The Go side's `Key()`, and not free to change: it is the name the
 *  operator's per-cluster switch is stored under. */
export const KEY = 'argocd';

/**
 * Where the page lives, relative to `/c/:cluster`.
 *
 * **Not `/x/{cluster}/argocd/`**, which is where the Go route answers JSON. The
 * page and its data are two addresses: the console reserves the `x` segment
 * under `/c/:cluster` for an extra's page (`EXTENSION` in the core's
 * `router/pages.ts`, which keeps it out of the object page's chunk), and the
 * Go mount keeps `/x/` at the top for the routes (`mountextensions.go`). A page
 * at the JSON address would be a reload that downloads JSON.
 */
export const PAGE_PATH = `x/${KEY}`;

/** The Go side's `SettingListColumn`: the setting, kept per cluster, that
 *  decides whether the console's lists draw the Argo CD column
 *  (Settings → Extra → Delivery Center). Not free to
 *  change, for the reason `KEY` is not. */
export const LIST_COLUMN = 'list-column';

export const argocd: Extension = {
  key: KEY,
  about,
  nav: { label: 'Delivery Center', path: PAGE_PATH },
  routes: [
    {
      path: PAGE_PATH,
      // The function form of `lazy`, and a fallback on the route itself - the
      // two properties the core's own `page()` gives every page, and for the
      // reasons its header records: the object form swallows a rejected chunk
      // into a blank page, and without a fallback a cold load withholds the
      // whole Shell until the chunk arrives.
      lazy: async () => ({ Component: (await import('./DeliveryCenter')).DeliveryCenter }),
      hydrateFallbackElement: <Pending what="this page" />,
    },
    {
      // One Application, read whole. Its own chunk, for the same reason.
      path: `${PAGE_PATH}/applications/:namespace/:name`,
      lazy: async () => ({ Component: (await import('./ApplicationPage')).ApplicationPage }),
      hydrateFallbackElement: <Pending what="this Application" />,
    },
  ],
  // Synced or OutOfSync on every row Argo CD manages, in every list.
  // Drawn only while its setting is on for the cluster.
  columns: [{ key: 'sync', label: 'Argo CD', kinds: ['*'], cells: argoColumn, setting: LIST_COLUMN }],
  tabs: [
    {
      // Argo CD's view of any object it marks as its own: every kind,
      // drawn only when the object carries the mark.
      key: 'sync',
      label: 'Argo CD',
      kinds: ['*'],
      when: marked,
      probe: managedHere,
      Component: lazy(() => import('./ArgoTab')),
    },
    {
      // A Pod or a ReplicaSet carries no mark: Argo CD manages it through its
      // owner, which only the route can walk to, so the tab asks it.
      key: 'owner',
      label: 'Argo CD',
      kinds: ['pods', 'replicasets'],
      when: (object) => !marked(object),
      probe: managedThroughOwner,
      Component: lazy(() => import('./ArgoTab')),
    },
  ],
};
