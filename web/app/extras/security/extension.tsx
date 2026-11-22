import { lazy } from 'react';
import { Pending, type Extension } from '@kubetower/ui';

import { about } from './about';
import { KEY, PAGE_PATH, PANEL_KINDS } from './data';
import { securityCells, securityProbe, securityShortcut } from './panel';

/**
 * The TypeScript half of Security Center, satisfying `@kubetower/ui`'s
 * `Extension`: the page of every finding trivy-operator wrote, and one
 * workload's own page, each in its own lazily loaded chunk. The Go routes
 * answer JSON at `/x/{cluster}/security/…`, a different address.
 *
 * And, drawn by the core while this extension is on for the cluster, on pods
 * and on the workloads that run them: a tab and a tile in the panel of an
 * object a vulnerability report covers, and a column in their lists.
 */
export const security: Extension = {
  key: KEY,
  about,
  nav: { label: 'Security Center', path: PAGE_PATH },
  routes: [
    {
      path: PAGE_PATH,
      lazy: async () => ({ Component: (await import('./SecurityPage')).SecurityPage }),
      hydrateFallbackElement: <Pending what="this page" />,
    },
    {
      path: `${PAGE_PATH}/:namespace/:kind/:name`,
      lazy: async () => ({ Component: (await import('./WorkloadPage')).WorkloadPage }),
      hydrateFallbackElement: <Pending what="this workload" />,
    },
  ],
  tabs: [{ key: 'cves', label: 'Security', kinds: PANEL_KINDS, probe: securityProbe, Component: lazy(() => import('./PanelTab')) }],
  shortcuts: [{ key: 'cves', kinds: PANEL_KINDS, resolve: securityShortcut }],
  columns: [{ key: 'cves', label: 'Security', kinds: PANEL_KINDS, cells: securityCells }],
};
