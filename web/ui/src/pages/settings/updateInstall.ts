import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { request } from '../../api';
import type { components, paths } from '../../api/schema';
import { settingsKey, useSettings, type Settings } from './data';

/**
 * The desktop's two clicks after "a newer release exists" (#48): Download,
 * then Restart to update. The state is the server's (`internal/updater`), read
 * in `info.update.install` and, while something runs, polled on its own route
 * and written back into the settings read so every surface draws one answer.
 */

export type UpdateInstall = components['schemas']['UpdateInstall'];

function path<T extends keyof paths & string>(template: T): string {
  return template;
}

export function downloadUpdate(): Promise<UpdateInstall> {
  return request<UpdateInstall>(path('/api/settings/update/download'), { method: 'POST' });
}

export function installUpdate(): Promise<UpdateInstall> {
  return request<UpdateInstall>(path('/api/settings/update/install'), { method: 'POST' });
}

export function setInstallUpdates(enabled: boolean): Promise<UpdateInstall> {
  return request<UpdateInstall>(path('/api/settings/update'), { method: 'PUT', body: { enabled } });
}

export function getUpdateInstall(signal?: AbortSignal): Promise<UpdateInstall> {
  return request<UpdateInstall>(path('/api/settings/update'), signal ? { signal } : {});
}

/** The phases that move on their own, read again until they settle. */
const MOVING: ReadonlySet<string> = new Set(['downloading', 'verifying', 'installing']);

/** How often a running download or install is read. */
export const INSTALL_POLL_MS = 500;

/** Write a fresh state into the settings read, where every surface reads it. */
function useKeep(): (next: UpdateInstall) => void {
  const cache = useQueryClient();
  return (next) =>
    cache.setQueryData<Settings>(settingsKey, (was) =>
      was?.info.update === undefined
        ? was
        : { ...was, info: { ...was.info, update: { ...was.info.update, install: next } } },
    );
}

/**
 * The update as the page draws it: the check's answer, the install state, and
 * the three writes. `offered` is the badge's and the banner's condition: a
 * newer release, installing allowed, and this installation supported.
 */
export function useUpdateInstall(): {
  latest: string | undefined;
  install: UpdateInstall | undefined;
  offered: boolean;
  download: ReturnType<typeof useMutation<UpdateInstall, Error, void>>;
  restart: ReturnType<typeof useMutation<UpdateInstall, Error, void>>;
  allow: ReturnType<typeof useMutation<UpdateInstall, Error, boolean>>;
} {
  const settings = useSettings();
  const update = settings.data?.info.update;
  const install = update?.install;
  const keep = useKeep();
  const cache = useQueryClient();
  const moving = install !== undefined && MOVING.has(install.phase);
  useQuery({
    queryKey: [...settingsKey, 'update'],
    queryFn: async ({ signal }) => {
      const next = await getUpdateInstall(signal);
      keep(next);
      // Settled: read the whole settings again, which after a restart is the
      // new build's version and its check.
      if (!MOVING.has(next.phase)) void cache.invalidateQueries({ queryKey: settingsKey, exact: true });
      return next;
    },
    enabled: moving,
    refetchInterval: moving ? INSTALL_POLL_MS : false,
    // The console is restarting under an install: the read fails until it is back.
    retry: false,
  });
  const download = useMutation<UpdateInstall, Error, void>({ mutationFn: downloadUpdate, onSuccess: keep });
  const restart = useMutation<UpdateInstall, Error, void>({ mutationFn: installUpdate, onSuccess: keep });
  const allow = useMutation<UpdateInstall, Error, boolean>({ mutationFn: setInstallUpdates, onSuccess: keep });
  const offered =
    update?.state === 'newer' && install !== undefined && install.enabled && install.supported;
  return { latest: update?.latest, install, offered, download, restart, allow };
}
