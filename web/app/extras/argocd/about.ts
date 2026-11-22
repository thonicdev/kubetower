import type { ExtensionAbout } from '@kubetower/ui';

import screenshot from './assets/preview.png';

/**
 * What Settings → Extra says about the Delivery Center before it is turned on.
 * The permissions follow the Go half's reads (`extras/argocd`): keep the two in
 * step when a read is added.
 */
export const about: ExtensionAbout = {
  description:
    'Shows the Argo CD Applications and the Flux CD Kustomizations and HelmReleases of a cluster: whether each is ' +
    'synced and healthy, its history, and what it is deleting. One Application opens on its own page, with the ' +
    'objects it manages, how each one differs from Git, and its Events. Objects Argo CD manages get an Argo CD tab, ' +
    'and the lists can draw an Argo CD column. It reads only: nothing is changed in the cluster from here.',
  reads: [
    'Argo CD Applications, ApplicationSets and AppProjects',
    'Flux CD Kustomizations and HelmReleases',
    "The objects an Application manages, and their owners, to show each one's drift",
    "An Application's Events",
    "The Services that tell where Argo CD is installed",
    "For Argo CD's own diff: argocd-server's Service, and the Secret kubetower-argocd-token holding a read-only " +
      'Argo CD token',
  ],
  permissions: [
    'list applications, applicationsets, appprojects (argoproj.io)',
    'list kustomizations (kustomize.toolkit.fluxcd.io), helmreleases (helm.toolkit.fluxcd.io)',
    'list on the kinds the Applications manage',
    'list events, services',
    "get services and create pods/portforward on argocd-server, for Argo CD's own diff",
    "get secrets on kubetower-argocd-token, in the console's namespace, for Argo CD's own diff",
  ],
  requires: [
    'Argo CD, or Flux CD, installed in the cluster',
    "For Argo CD's own diff: a Secret kubetower-argocd-token holding a read-only Argo CD token",
  ],
  // Drawn by `pnpm shots:extras` from the measure harness's fake data.
  screenshot,
};
