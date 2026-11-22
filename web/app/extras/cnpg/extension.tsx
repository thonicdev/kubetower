import { lazy } from 'react';
import { Pending, type Extension, type ExtensionCell, type ExtensionShortcutTarget, type ExtensionTabProps } from '@kubetower/ui';

import { about } from './about';
import { CnpgProblem, KEY, PAGE_PATH, clusterHref, getPod, getPodRoles } from './data';
import { HEALTH_LABEL } from './parts';

/**
 * The TypeScript half of Postgres (CNPG), satisfying `@kubetower/ui`'s
 * `Extension`: two pages (every cluster, and one), a tab and a shortcut on an
 * instance pod and on a CloudNativePG Cluster, and a column on the Pods list.
 */

/** The kind of CloudNativePG's Cluster, as the console's address spells it. */
export const CLUSTER_KIND = 'clusters.postgresql.cnpg.io';

/** An instance pod carries this label; a Pooler's or a Job's does not. */
const isInstance = (object: { labels: Readonly<Record<string, string>> }) =>
  object.labels['cnpg.io/podRole'] === 'instance' || object.labels['cnpg.io/instanceName'] !== undefined;

/** A pod's tile: the cluster it is an instance of, its role and the cluster's state. */
export async function podShortcut({ cluster, namespace, name }: ExtensionTabProps, signal: AbortSignal): Promise<ExtensionShortcutTarget | null> {
  if (namespace === undefined || namespace === '') return null;
  try {
    const answer = await getPod(cluster, namespace, name, signal);
    const s = answer.of;
    return {
      title: s.name,
      badges: [answer.role, `${String(s.ready)}/${String(s.expected)} ready`, HEALTH_LABEL[s.health]],
      detail: s.phase,
      to: clusterHref(cluster, s.namespace, s.name),
    };
  } catch (error) {
    if (error instanceof CnpgProblem && error.kind === 'absent') return null;
    throw error;
  }
}

/** A Cluster's tile: where its page is, with nothing read to draw it. */
export function clusterShortcut({ cluster, namespace, name }: ExtensionTabProps): Promise<ExtensionShortcutTarget | null> {
  if (namespace === undefined || namespace === '') return Promise.resolve(null);
  return Promise.resolve({ title: name, badges: ['Postgres'], to: clusterHref(cluster, namespace, name) });
}

/** The Pods list's column: an instance's role, one read for the whole list. */
export async function roleCells(list: { cluster: string; kind: string }, signal: AbortSignal): Promise<Readonly<Record<string, ExtensionCell>>> {
  if (list.kind !== 'pods') return {};
  const answer = await getPodRoles(list.cluster, signal);
  const cells: Record<string, ExtensionCell> = {};
  for (const [key, { role, cluster }] of Object.entries(answer.pods)) {
    const namespace = key.split('/')[0] ?? '';
    cells[key] = {
      text: role,
      ...(role === 'primary' ? { tone: 'ok' } : {}),
      hint: `cluster ${cluster}`,
      href: clusterHref(list.cluster, namespace, cluster),
    };
  }
  return cells;
}

const PostgresTab = lazy(() => import('./PostgresTab'));

export const cnpg: Extension = {
  key: KEY,
  about,
  nav: { label: 'Postgres (CNPG)', path: PAGE_PATH },
  routes: [
    {
      path: PAGE_PATH,
      lazy: async () => ({ Component: (await import('./CnpgPage')).CnpgPage }),
      hydrateFallbackElement: <Pending what="this page" />,
    },
    {
      path: `${PAGE_PATH}/clusters/:namespace/:name`,
      lazy: async () => ({ Component: (await import('./ClusterPage')).ClusterPage }),
      hydrateFallbackElement: <Pending what="this Postgres cluster" />,
    },
  ],
  columns: [{ key: 'role', label: 'Postgres', kinds: ['pods'], cells: roleCells }],
  shortcuts: [
    { key: 'instance', kinds: ['pods'], when: isInstance, resolve: podShortcut },
    { key: 'cluster', kinds: [CLUSTER_KIND], resolve: clusterShortcut },
  ],
  tabs: [
    { key: 'instance', label: 'Postgres', kinds: ['pods'], when: isInstance, Component: PostgresTab },
    { key: 'cluster', label: 'Postgres', kinds: [CLUSTER_KIND], Component: PostgresTab },
  ],
};
