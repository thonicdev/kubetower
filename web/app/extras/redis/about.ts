import type { ExtensionAbout } from '@kubetower/ui';

import screenshot from './assets/preview.png';

/**
 * What Settings → Extra says about the Redis viewer before it is turned on.
 * The permissions follow the Go half's reads (`extras/redis`): keep the two in
 * step when a read is added.
 */
export const about: ExtensionAbout = {
  description:
    'Lists the Redis and Valkey instances the cluster runs, from their Services, and opens each one on its keys, ' +
    'values and memory. It connects only when you open an instance, through a tunnel opened and closed with the ' +
    'request. Every command it sends is a read: nothing is written to Redis or to the cluster from here.',
  reads: [
    'Services, EndpointSlices and Pods, to find the instances',
    'The keys, values and memory of an instance you open, through a tunnel to it',
    "The password its pod is started with, from that pod's Secret, when the instance asks for one",
  ],
  permissions: [
    'list services, endpointslices (discovery.k8s.io), pods',
    "create pods/portforward on the instance's pods",
    'get secrets on the Secret holding the password, for an instance that has one',
  ],
  requires: ['A Redis or Valkey Service in the cluster'],
  // Drawn by `pnpm shots:extras` from the measure harness's fake data.
  screenshot,
};
