import { queryOptions, useQuery, type UseQueryResult } from '@tanstack/react-query';
import { useSearchParams } from 'react-router';

import { request, useCapabilities, useClusters, useExtensions, type Cluster, type ExtensionInfo } from '../../api';
import type { components, paths } from '../../api/schema';
import { arrange, onlyCluster } from '../../shell/clusterState';
import { useProfileRead } from '../../shell/sharedCluster';

/**
 * The console's own settings.
 *
 * **Not enveloped**, and that is the contract's own rule with its own reason:
 * settings are this process's file rather than a cluster read, so they cannot be
 * `unreachable` - and a file the console cannot read at all is a broken console,
 * which is a 500 with a problem object and not a state.
 *
 * The one thing inside them that *is* enveloped is the archive, because an
 * archive with nowhere to write is a state and not a zero: a page reporting 0
 * bytes over a refused write is the most reassuring possible way to be wrong.
 */

type Schemas = components['schemas'];

export type Settings = Schemas['Settings'];
export type SettingsWrite = Schemas['SettingsWrite'];
export type NodeShellSettings = Schemas['NodeShellSettings'];
export type SettingsUpdate = Schemas['SettingsUpdate'];

function path<T extends keyof paths & string>(template: T): string {
  return template;
}

export function getSettings(signal?: AbortSignal): Promise<Settings> {
  return request<Settings>(path('/api/settings'), signal ? { signal } : {});
}

/**
 * The General tab's write.
 *
 * **Only what changed travels.** Every member of `SettingsWrite` is optional and
 * a write that says nothing about a field must not re-assert its zero: `false`
 * and "not said" are different instructions about keeping events, and a page
 * that posted its whole form on every save would turn one into the other the
 * first time a field failed to render.
 */
export function saveSettings(write: SettingsWrite): Promise<Settings> {
  return request<Settings>(path('/api/settings'), { method: 'PUT', body: write });
}

/**
 * The node shell's own write.
 *
 * Its own endpoint rather than a field of the settings object, because it
 * carries an image reference that is validated differently from everything else
 * on that page - and because a build with no archive must not have its node
 * shell save refused along with the archive's.
 */
export function saveNodeShell(write: NodeShellSettings): Promise<NodeShellSettings> {
  return request<NodeShellSettings>(path('/api/settings/nodeshell'), {
    method: 'PUT',
    body: write,
  });
}

/**
 * Settings › Infos' Check now: the update check past its
 * cache. It answers once the check has settled, with the object
 * `info.update` carries.
 */
export function checkUpdates(): Promise<SettingsUpdate> {
  return request<SettingsUpdate>(path('/api/settings/update-check'), { method: 'POST' });
}

export const settingsKey = ['settings'] as const;

/** How often the page reads again while a check started elsewhere is running. */
export const CHECKING_POLL_MS = 2000;

/**
 * No poll, but one. Settings are this process's own file and nothing else
 * writes it, so an interval here is a read of a value that changed when this
 * page changed it - **except the update check**, which the server runs on its
 * own at startup and on a schedule. While it says one is
 * in flight the page reads again, so the running check is drawn as running
 * and its answer lands without a reload; once it has settled the poll stops.
 */
export function settingsQuery() {
  return queryOptions({
    queryKey: settingsKey,
    queryFn: ({ signal }) => getSettings(signal),
    refetchInterval: (query) => (query.state.data?.info.update?.checking === true ? CHECKING_POLL_MS : false),
  });
}

export function useSettings(): UseQueryResult<Settings> {
  return useQuery(settingsQuery());
}

/** How often an open Infos page reads the update state again while nothing runs. */
export const UPDATE_POLL_MS = 60_000;

/**
 * The states the server's scheduled check can still change. `off` and
 * `notApplicable` are settled for the life of the process: no check ever runs
 * there, so there is nothing to read again.
 */
const CHANGEABLE: ReadonlySet<string> = new Set(['current', 'newer', 'failed']);

/**
 * Keeps the update state current **while the Infos page is open**.
 *
 * The settings read above polls only while a check is in flight, so a page
 * that read `checking: false` never read again: the server's scheduled check
 * could find a newer release, the Dock could show "1", and the page still said
 * "Up to date" - and the notification's click routes to this same page, so
 * nothing remounted it. This second observer of the same query reads again
 * every `UPDATE_POLL_MS` (every `CHECKING_POLL_MS` while a check runs), and
 * only from Infos: the read is the server's answer from memory, never a request
 * to GitHub, and General has no reason to poll for it.
 */
export function useUpdateWatch(): void {
  useQuery({
    ...settingsQuery(),
    refetchInterval: (query) => {
      const update = query.state.data?.info.update;
      if (update === undefined || !CHANGEABLE.has(update.state)) return false;
      return update.checking ? CHECKING_POLL_MS : UPDATE_POLL_MS;
    },
  });
}


/**
 * Whether the Settings page draws its Extra tab, and for which cluster
 *
 * **On every profile.** The tab draws a tile per extension the build carries,
 * every one off until it is turned on, with a
 * button where the listing says the person may press it (`writable`) and the
 * server's reason where not - the chart's values do not name it, the person
 * does not hold the `use` permission on it, KT_EXTRAS pins it, or several replicas have
 * no switch. Whether anything can be changed is the server's answer, per row,
 * and not the page's guess from the profile.
 *
 * **Which cluster** is the address's `?cluster=` when it names one the list
 * holds, else the first in the rail's order - and under the server profile
 * the one cluster there is. A real address, so a reload or a link lands on the
 * same cluster.
 *
 * **No tab unless the listing names at least one extra**: a build that
 * carries none draws nothing rather than an empty block, and so does a listing
 * that failed - a failure notice about a feature the build may not have would
 * be wrong.
 *
 * `deciding` while the profile, the cluster list or the listing is still being
 * read: the tab is not drawn yet, and an address that names it waits rather
 * than being sent to General before the answer is in.
 */
export type ExtraTab =
  | { readonly state: 'deciding' }
  | { readonly state: 'none' }
  | {
      readonly state: 'shown';
      readonly cluster: string;
      /** The clusters the tab can be pointed at, in the rail's order. */
      readonly clusters: readonly Cluster[];
      readonly items: readonly ExtensionInfo[];
    };

const DECIDING: ExtraTab = { state: 'deciding' };
const NONE: ExtraTab = { state: 'none' };

/** The `?cluster=` the Extra tab reads and writes. */
export const CLUSTER_PARAM = 'cluster';

export function useExtraTab(): ExtraTab {
  const profileRead = useProfileRead();
  const capabilities = useCapabilities().data;
  const shared = capabilities?.mode === 'shared';
  const clusters = useClusters();
  const [params] = useSearchParams();

  const listed = clusters.data?.state === 'ok' ? arrange(clusters.data.items) : [];
  const asked = params.get(CLUSTER_PARAM);
  const cluster = shared
    ? onlyCluster(clusters.data)?.name
    : (listed.find((one) => one.name === asked) ?? listed[0])?.name;
  const extensions = useExtensions(cluster, profileRead);

  if (!profileRead) return DECIDING;
  if (cluster === undefined) return clusters.isPending ? DECIDING : NONE;
  if (extensions.isPending) return DECIDING;
  if (extensions.isError || extensions.data.state !== 'ok' || extensions.data.items.length === 0) return NONE;
  return {
    state: 'shown',
    cluster,
    clusters: shared ? listed.filter((one) => one.name === cluster) : listed,
    items: extensions.data.items,
  };
}
