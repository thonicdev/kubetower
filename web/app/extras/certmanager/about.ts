import type { ExtensionAbout } from '@kubetower/ui';

import screenshot from './assets/preview.png';

/**
 * What Settings → Extra says about Cert Manager before it is turned on. The
 * permissions follow the Go half's reads (`extras/certmanager`): keep the two
 * in step when a read is added.
 */
export const about: ExtensionAbout = {
  description:
    'Answers what expires in the next 30 days: every certificate cert-manager manages, with the days it has left, ' +
    'on a 90-day strip and in tiles that filter. Then what is failing: ACME orders and challenges with their reason, ' +
    'and issuers that are not ready. It reads only what cert-manager writes in its resources, and never a Secret: a ' +
    "Secret's days left come from the Certificate that names it. It adds an Expires column and a Certificate tab to " +
    'Certificates, Secrets and Ingresses.',
  reads: [
    "cert-manager's Certificates: their dates, Ready condition and issuer",
    'Issuers and ClusterIssuers, and whether each is ready',
    'ACME Orders and Challenges, and why one is failing',
    "Ingresses, to show the days left of the certificate behind each one's TLS secrets",
  ],
  permissions: [
    'list certificates, issuers, clusterissuers (cert-manager.io)',
    'list orders, challenges (acme.cert-manager.io)',
    'list ingresses (networking.k8s.io), for their column and tab',
  ],
  requires: ['cert-manager'],
  // Drawn by `pnpm shots:extras` from the measure harness's fake data.
  screenshot,
};
