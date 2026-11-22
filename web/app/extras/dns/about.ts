import type { ExtensionAbout } from '@kubetower/ui';

import line from './assets/line.png';
import screenshot from './assets/preview.png';

/**
 * What Settings → Extra says about DNS before it is turned on. The permissions
 * follow the Go half's read (`extras/dns`, through `Host.DNSWindow`): keep the
 * two in step.
 */
export const about: ExtensionAbout = {
  description:
    'CoreDNS health from its Prometheus metrics: queries per second, NXDOMAIN and SERVFAIL share, p50 and p99 latency and cache hit rate, ' +
    'per pod and per upstream, over 15 minutes, 1 hour or 6 hours. Tiles filter the bars under them; an upstream that stopped answering reads "No answers". ' +
    'On CoreDNS’s Service and pods, a line gives the current figures and leads to the page. ' +
    'It reads only: nothing is changed in the cluster, and Prometheus is asked fixed queries only.',
  reads: [
    'CoreDNS pods, to tell its series from any other and to name each pod',
    'CoreDNS metrics (queries, answer codes, latency, cache, upstreams), through the cluster’s Prometheus',
  ],
  permissions: ['list pods in the namespace CoreDNS runs in'],
  requires: ['CoreDNS', 'Prometheus scraping CoreDNS (metrics port 9153)'],
  // Drawn by `pnpm shots:extras` from the measure harness's fake data.
  screenshot,
  gallery: [{ src: line, caption: 'The line on CoreDNS’s Service and pods: the current figures, and the worst one marked' }],
};
