import { lazy } from 'react';
import { Pending, type Extension } from '@kubetower/ui';

import { about } from './about';
import { KEY, RedisProblem, getService } from './data';
import { PAGE_PATH } from './parts';
import { labelledRedis, podNotChecked, podShortcut, serviceShortcut } from './shortcut';

/**
 * The TypeScript half of the Redis viewer, satisfying `@kubetower/ui`'s
 * `Extension`. `app/main.tsx` hands it to `createApp`, and the console mounts
 * `routes` under `/c/:cluster` only.
 *
 * Two pages under the `x` segment the console reserves for an extra's page:
 * the inventory, which the menu's Extra group opens, and one instance. The Go routes answer JSON at
 * `/x/{cluster}/redis/…`, a different address.
 *
 * And one tab, in a Service's panel, drawn by
 * the core while the extra is on for the cluster - and only on a Service the
 * inventory would offer, which the tab's `probe` asks the route about: the
 * panel's `when` sees labels and annotations, never ports. The probe dials
 * nothing; the tab's address is `x.redis.backend`. Where the route could not
 * answer, the tab is drawn and says it could not check.
 *
 * And a shortcut, a tile on the Details of a Service or a Pod of a Redis
 * deployment, that opens the deployment's page (`shortcut.ts`).
 */
export const redis: Extension = {
  key: KEY,
  icon: 'layers',
  about,
  // In the menu's Extra group: the inventory lists every Redis the cluster
  // runs. The tab below opens one from its Service, the tile from its Service
  // or its Pod.
  nav: { label: 'Redis viewer', path: PAGE_PATH },
  routes: [
    {
      path: PAGE_PATH,
      lazy: async () => ({ Component: (await import('./RedisViewer')).RedisViewer }),
      hydrateFallbackElement: <Pending what="this page" />,
    },
    {
      path: `${PAGE_PATH}/:namespace/:service/:port`,
      lazy: async () => ({ Component: (await import('./InstancePage')).InstancePage }),
      hydrateFallbackElement: <Pending what="this page" />,
    },
  ],
  // A tile at the top of a Service's or a Pod's Details that opens its
  // deployment's page, from discovery alone.
  shortcuts: [
    { key: 'deployment', kinds: ['services'], resolve: serviceShortcut },
    { key: 'deployment', kinds: ['pods'], resolve: podShortcut },
    // A Redis pod whose namespace could not be read: said, not left blank.
    { key: 'unchecked', kinds: ['pods'], when: labelledRedis, resolve: podNotChecked },
  ],
  tabs: [
    {
      key: 'backend',
      label: 'Redis',
      kinds: ['services'],
      probe: async ({ cluster, namespace, name }, signal) => {
        if (namespace === undefined || namespace === '') return false;
        try {
          await getService(cluster, namespace, name, signal);
          return true;
        } catch (error) {
          // A Service that is not a Redis answers `not_redis`, and gets no
          // tab. Any other answer - the Services or their slices could not be
          // read - draws the tab, which says it could not check: hiding it
          // would make "could not look" read as "not a Redis" (R5 §5). The
          // gate's refusals, an unknown cluster among them, draw none either.
          if (error instanceof RedisProblem && (error.code === 'not_redis' || error.code === 'gate' || error.code === 'unknown_cluster')) return false;
          if (signal.aborted) return false;
          return true;
        }
      },
      Component: lazy(() => import('./RedisTab')),
    },
  ],
};
