import { Pending as Waiting, type Extension } from '@kubetower/ui';

import { about } from './about';
import { KEY } from './data';
import { dnsLine, isCoreDNS } from './panel';

/**
 * The TypeScript half of this extra, satisfying `@kubetower/ui`'s `Extension`:
 * one page under the `x` segment the console reserves for an extra's page, with
 * an entry in the menu. The Go route answers JSON at `/x/{cluster}/dns/stats`,
 * a different address (see the argocd extra's extension.tsx for why).
 */
export const PAGE_PATH = `x/${KEY}`;

export const dns: Extension = {
  key: KEY,
  icon: 'globe',
  about,
  nav: { label: 'DNS', path: PAGE_PATH },
  routes: [
    {
      path: PAGE_PATH,
      lazy: async () => ({ Component: (await import('./DNSPage')).DNSPage }),
      hydrateFallbackElement: <Waiting what="this page" />,
    },
  ],
  // One read when the panel opens, at 15 minutes; only on CoreDNS's Service and pods.
  shortcuts: [{ key: 'line', kinds: ['services', 'pods'], when: isCoreDNS, resolve: dnsLine }],
};
