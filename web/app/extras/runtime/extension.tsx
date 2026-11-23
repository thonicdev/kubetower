import { lazy } from 'react';
import { Pending, type Extension } from '@kubetower/ui';

import { about } from './about';
import { COLUMN_KINDS, KEY, getPod, javaColumn } from './data';
import { TAB, podTile } from './shortcut';

/**
 * The TypeScript half of the Runtime Inspector (the JVM Inspector until 0.2.0), satisfying `@kubetower/ui`'s
 * `Extension`. `app/main.tsx` hands it to `createApp`, and the console mounts
 * `routes` under `/c/:cluster` only.
 *
 * One page, under the `x` segment the console reserves for an extra's page:
 * the probable Java pods with their heap against their limit. The Go routes
 * answer JSON at `/x/{cluster}/jvm/…`, a different address.
 *
 * And one tab, on a pod, drawn where its spec says a container probably runs
 * Java: the `probe` reads the same spec route as the page and runs nothing in
 * a container. The tab runs jcmd only when Inspect is clicked; its address is
 * `x.runtime.jvm` (`x.jvm.jvm` before 0.2.0, still answered).
 */
export const PAGE_PATH = `x/${KEY}`;

export const runtimeInspector: Extension = {
  key: KEY,
  // Published as the JVM Inspector until 0.2.0: its bookmarks and tab links.
  legacyKeys: ['jvm'],
  about,
  // No menu entry: reached from a Java pod's tab and tile.
  nav: { label: 'Runtime Inspector', path: PAGE_PATH, menu: false },
  routes: [
    {
      path: PAGE_PATH,
      lazy: async () => ({ Component: (await import('./JVMInspector')).JVMInspector }),
      hydrateFallbackElement: <Pending what="this page" />,
    },
  ],
  // "Java" on every pod the spec shows as Java, and on the workloads that own
  // them, toned by the worst container's risk.
  columns: [{ key: 'heap', label: 'Java', kinds: COLUMN_KINDS, cells: javaColumn }],
  // A tile at the top of a Java pod's Details, from its spec alone: the
  // runtime, the risk, the heap against the limit; it opens the JVM tab.
  shortcuts: [{ key: 'pod', kinds: ['pods'], resolve: podTile }],
  tabs: [
    {
      key: TAB,
      label: 'JVM',
      kinds: ['pods'],
      probe: async ({ cluster, namespace, name }, signal) => {
        if (namespace === undefined || namespace === '') return false;
        // A rejection draws no tab: the tab is for a pod the spec says is
        // Java, and a pod the console could not read is not shown to be one.
        const pod = await getPod(cluster, namespace, name, signal);
        return pod.java;
      },
      Component: lazy(() => import('./JVMTab')),
    },
  ],
};
