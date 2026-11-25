import type { ExtensionAbout } from '@kubetower/ui';

import claimTab from './assets/claim-tab.png';
import orphans from './assets/orphans.png';
import tile from './assets/overview-tile.png';
import screenshot from './assets/preview.png';

/**
 * What Settings → Extra says about Storage Inspector before it is turned on.
 * The permissions follow the Go half's reads (`extras/storage`): keep the two
 * in step when a read is added.
 */
export const about: ExtensionAbout = {
  description:
    'Inventories the PersistentVolumes, PersistentVolumeClaims and StorageClasses, and flags the orphans: ' +
    'released or never-bound volumes, unmounted claims, claims whose StatefulSet is gone, unused classes. ' +
    'Grades how full each mounted claim is at 80 and 90 %, and shows the last VolumeSnapshot of each. ' +
    'Read-only. Adds a Storage tab to Pods and claims, and a tile to the Overview.',
  reads: [
    'PersistentVolumes, PersistentVolumeClaims and StorageClasses',
    'Pods and StatefulSets, for what mounts each claim',
    "Each node's kubelet stats summary, for how full each claim is",
    'VolumeSnapshots, VolumeSnapshotContents and VolumeSnapshotClasses, when installed',
  ],
  permissions: [
    'list persistentvolumes, persistentvolumeclaims, pods',
    'list storageclasses (storage.k8s.io), statefulsets (apps)',
    'list nodes, get nodes/proxy, for the fill',
    'list volumesnapshots, volumesnapshotcontents, volumesnapshotclasses (snapshot.storage.k8s.io)',
  ],
  requires: [],
  badges: ['read-only'],
  // Drawn by `pnpm shots:extras` from the measure harness's fake data.
  screenshot,
  gallery: [
    { src: orphans, caption: 'Orphans, by kind' },
    { src: claimTab, caption: 'Storage tab of a claim' },
    { src: tile, caption: 'Tile on the Overview' },
  ],
};
