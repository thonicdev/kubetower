import type { ExtensionAbout } from '@kubetower/ui';

import screenshot from './assets/preview.png';
import activity from './assets/activity.png';

/**
 * What Settings → Extra says about Event Intelligence before it is turned on.
 * The permissions follow the Go half's reads (`extras/events`: `read.go`,
 * `changes.go`): keep the two in step when a read is added.
 */
export const about: ExtensionAbout = {
  description:
    'Makes Kubernetes Events readable. Repeats are counted once per object ("BackOff × 214"), grouped by cause across objects, and set on a timeline ' +
    'under what changed just before: a rollout with its revision, a ConfigMap edit, scaling, a new or failing node. Tiles count warnings, ' +
    'crash loops, OOM kills and scheduling failures and filter the list; the window is 15 minutes, 1 hour or 3 hours. ' +
    'On the timeline each workload is a row, darker where its Events are dense, nodes that join together are one marker, ' +
    'the changes Warnings came after lead the page, and Normal Events wait behind a toggle. ' +
    'On a Pod or a workload, its Activity tab draws the same timeline and causes narrowed to it, in place of the plain list of events. ' +
    'It shows what came after a change, never what it caused. It reads only: nothing is changed in the cluster.',
  reads: [
    'Events, deduplicated per object and grouped per cause',
    'ReplicaSets and their revisions, for rollouts and rollbacks',
    'Pods, for crash loops, OOM kills and which workload and node an Event belongs to',
    'ConfigMaps: only when each was last edited and by whom, never its data',
    'Nodes: when each appeared and when one stopped being ready',
  ],
  permissions: [
    'list events',
    'list replicasets (apps)',
    'list pods',
    'list configmaps (without it, no ConfigMap edit on the timeline)',
    'list nodes (without it, no node on the timeline)',
  ],
  requires: [],
  // Drawn by `pnpm shots:extras` from the measure harness's fake data.
  screenshot,
  gallery: [
    { src: activity, caption: 'The Activity tab of a Pod, Deployment, StatefulSet, DaemonSet, ReplicaSet or Job: the timeline and the causes, narrowed to the object and its pods' },
  ],
};
