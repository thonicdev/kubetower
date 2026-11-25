import { lazy } from 'react';
import { Pending, type Extension } from '@kubetower/ui';

import { about } from './about';
import { KEY, PAGE_PATH, getPod } from './data';
import { storageTile } from './tile';

const ClaimTab = lazy(() => import('./ClaimTab'));
const PodTab = lazy(() => import('./PodTab'));

/**
 * The TypeScript half of the Storage Inspector extra, satisfying
 * `@kubetower/ui`'s `Extension`: its page, a Storage tab on claims and on the
 * pods that mount one, and a tile on the Overview.
 */
export const storage: Extension = {
  key: KEY,
  icon: 'volume',
  about,
  nav: { label: 'Storage Inspector', path: PAGE_PATH },
  routes: [
    {
      path: PAGE_PATH,
      lazy: async () => ({ Component: (await import('./Storage')).Storage }),
      hydrateFallbackElement: <Pending what="this page" />,
    },
  ],
  tabs: [
    { key: 'claim', label: 'Storage', kinds: ['persistentvolumeclaims'], Component: ClaimTab },
    {
      // A pod carries no mark of the claims it mounts: the route says.
      key: 'pod',
      label: 'Storage',
      kinds: ['pods'],
      probe: async ({ cluster, namespace, name }, signal) =>
        namespace !== undefined && (await getPod(cluster, namespace, name, signal)).claims.length > 0,
      Component: PodTab,
    },
  ],
  overview: { resolve: storageTile },
};
