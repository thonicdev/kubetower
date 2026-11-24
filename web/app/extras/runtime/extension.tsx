import { lazy } from 'react';
import { Pending, type Extension, type ExtensionTabProps } from '@kubetower/ui';

import { about } from './about';
import { COLUMN_KINDS, KEY, RUNTIME_LABEL, javaColumn, sharedPod } from './data';
import { TAB, podTile } from './shortcut';

/**
 * The TypeScript half of the Runtime Inspector (the JVM Inspector until 0.2.0), satisfying `@kubetower/ui`'s
 * `Extension`. `app/main.tsx` hands it to `createApp`, and the console mounts
 * `routes` under `/c/:cluster` only.
 *
 * One page, under the `x` segment the console reserves for an extra's page:
 * the probable Java pods with their heap against their limit. The Go routes
 * answer JSON at `/x/{cluster}/runtime/…`, a different address (`/x/{cluster}/jvm/…`
 * before 0.2.0, redirected there by the server).
 *
 * And one tab, on a pod, drawn where its spec says a container probably runs
 * Java: the `probe` reads the same spec route as the page and runs nothing in
 * a container. The tab runs jcmd only when Inspect is clicked; its address is
 * `x.runtime.jvm` (`x.jvm.jvm` before 0.2.0, still answered).
 */
export const PAGE_PATH = `x/${KEY}`;

export const runtimeInspector: Extension = {
  key: KEY,
  icon: 'coffee',
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
  // The runtimes of every pod the spec shows one for, and of the workloads
  // that own them; toned by the worst Java container's risk where there is one.
  columns: [{ key: 'heap', label: 'Runtime', kinds: COLUMN_KINDS, cells: javaColumn }],
  // A tile at the top of a Java pod's Details, from its spec alone: the
  // runtime, the risk, the heap against the limit; it opens the JVM tab.
  shortcuts: [{ key: 'pod', kinds: ['pods'], resolve: podTile }],
  tabs: [
    {
      key: TAB,
      label: 'JVM',
      kinds: ['pods'],
      // A rejection draws no tab: the tab is for a pod the spec says is
      // Java, and a pod the console could not read is not shown to be one.
      probe: async ({ cluster, namespace, name }) =>
        namespace !== undefined && namespace !== '' && (await sharedPod(cluster, namespace, name)).java,
      Component: lazy(() => import('./JVMTab')),
    },
    ...(['go', 'node', 'python'] as const).map((runtime) => ({
      key: runtime,
      label: RUNTIME_LABEL[runtime],
      kinds: ['pods'],
      probe: async ({ cluster, namespace, name }: ExtensionTabProps) =>
        namespace !== undefined && namespace !== '' && ((await sharedPod(cluster, namespace, name)).runtimes ?? []).includes(runtime),
      Component: lazy(() => import('./RuntimeTab').then((m) => ({ default: m.tabFor(runtime) }))),
    })),
  ],
};
