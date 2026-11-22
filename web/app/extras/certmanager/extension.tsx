import { lazy } from 'react';
import { Pending, type Extension } from '@kubetower/ui';

import { about } from './about';
import { KEY, PAGE_PATH, getObject } from './data';
import { certificateCells, certificateShortcut } from './shortcut';

/** The kinds the column, the tab and the shortcut are drawn on. */
const KINDS = ['certificates.cert-manager.io', 'secrets', 'ingresses'] as const;

/** cert-manager marks the Secret it writes; any other Secret is not asked about. */
const CERTIFICATE_NAME = 'cert-manager.io/certificate-name';

const Tab = lazy(() => import('./CertificateTab'));

/**
 * The TypeScript half of the Cert Manager extra, satisfying `@kubetower/ui`'s
 * `Extension`. One page under the `x` segment the console reserves for an
 * extra's page, in its own lazily loaded chunk; the Go route answers JSON at
 * `/x/{cluster}/certmanager/`, a different address.
 *
 * And, drawn by the core while the extra is on for the cluster: an `Expires`
 * column on the lists of Certificates, Secrets and Ingresses (one read per
 * list); a `Certificate` tab and a shortcut tile in their panels. A Secret is
 * concerned only when cert-manager marked it; an Ingress only when the route
 * finds a certificate behind one of its TLS secrets.
 */
export const certmanager: Extension = {
  key: KEY,
  about,
  nav: { label: 'Cert Manager', path: PAGE_PATH },
  routes: [
    {
      path: PAGE_PATH,
      lazy: async () => ({ Component: (await import('./CertManager')).CertManager }),
      hydrateFallbackElement: <Pending what="this page" />,
    },
  ],
  columns: [{ key: 'expires', label: 'Expires', kinds: KINDS, cells: certificateCells }],
  tabs: [
    { key: 'certificate', label: 'Certificate', kinds: ['certificates.cert-manager.io'], Component: Tab },
    {
      key: 'secret',
      label: 'Certificate',
      kinds: ['secrets'],
      when: (object) => object.annotations[CERTIFICATE_NAME] !== undefined,
      Component: Tab,
    },
    {
      // An Ingress carries no mark of its own: the route says whether a
      // certificate is behind one of its TLS secrets.
      key: 'ingress',
      label: 'Certificate',
      kinds: ['ingresses'],
      probe: async ({ cluster, kind, namespace, name }, signal) =>
        namespace !== undefined && (await getObject(cluster, kind, namespace, name, signal)) !== null,
      Component: Tab,
    },
  ],
  shortcuts: [{ key: 'certificate', kinds: KINDS, resolve: certificateShortcut }],
};
