import { useIsMutating, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';

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

/** The phases a page knows how to draw; any other is drawn as nothing. */
export const PHASES: ReadonlySet<string> = new Set(['idle', 'downloading', 'verifying', 'ready', 'installing', 'failed']);

/** How often a running download or install is read. */
export const INSTALL_POLL_MS = 500;

/**
 * How long "Installing…" waits for the restarted console before it is drawn
 * as failed and the poll stops.
 */
export const INSTALL_TIMEOUT_MS = 3 * 60_000;

/**
 * The two writes that start work, keyed so every surface sees one running:
 * the banner and Infos' version line both offer Download, and one click must
 * disable both.
 */
const DOWNLOAD_KEY = ['update', 'download'] as const;
const RESTART_KEY = ['update', 'restart'] as const;

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
 * Whether "Installing…" has waited past INSTALL_TIMEOUT_MS, and a way to wait
 * again. The clock starts when the phase becomes `installing`.
 */
function useInstallTimeout(installing: boolean): { timedOut: boolean; again: () => void } {
  const [round, setRound] = useState(0);
  const [expired, setExpired] = useState(-1);
  useEffect(() => {
    if (!installing) return undefined;
    const timer = setTimeout(() => setExpired(round), INSTALL_TIMEOUT_MS);
    return () => clearTimeout(timer);
  }, [installing, round]);
  return { timedOut: installing && expired === round, again: () => setRound((n) => n + 1) };
}

/**
 * The update as the page draws it: the check's answer, the install state, and
 * the three writes. `offered` is the badge's and the banner's condition: a
 * newer release, installing allowed, and this installation supported.
 */
export function useUpdateInstall(): {
  running: string | undefined;
  latest: string | undefined;
  install: UpdateInstall | undefined;
  offered: boolean;
  /** A download or a restart is being asked, from this surface or another. */
  busy: boolean;
  /** "Installing…" has had no answer for INSTALL_TIMEOUT_MS. */
  timedOut: boolean;
  /** Wait for the restarted console again after a timeout. */
  waitAgain: () => void;
  download: ReturnType<typeof useMutation<UpdateInstall, Error, void>>;
  restart: ReturnType<typeof useMutation<UpdateInstall, Error, void>>;
  allow: ReturnType<typeof useMutation<UpdateInstall, Error, boolean>>;
} {
  const settings = useSettings();
  const update = settings.data?.info.update;
  const install = update?.install;
  const keep = useKeep();
  const cache = useQueryClient();
  const { timedOut, again } = useInstallTimeout(install?.phase === 'installing');
  const moving = install !== undefined && MOVING.has(install.phase) && !timedOut;
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
  const reread = (): void => void cache.invalidateQueries({ queryKey: settingsKey, exact: true });
  const download = useMutation<UpdateInstall, Error, void>({
    mutationKey: DOWNLOAD_KEY,
    mutationFn: downloadUpdate,
    onSuccess: keep,
  });
  const restart = useMutation<UpdateInstall, Error, void>({
    mutationKey: RESTART_KEY,
    mutationFn: installUpdate,
    onSuccess: keep,
  });
  // The server applies the setting even when it cannot save it (500
  // not_saved): read it back rather than keep the box where it was.
  const allow = useMutation<UpdateInstall, Error, boolean>({
    mutationFn: setInstallUpdates,
    onSuccess: keep,
    onError: reread,
  });
  const busy = useIsMutating({ mutationKey: ['update'] }) > 0;
  const offered =
    update?.state === 'newer' && install !== undefined && install.enabled && install.supported;
  return {
    running: settings.data?.info.version,
    latest: update?.latest,
    install,
    offered,
    busy,
    timedOut,
    waitAgain: () => {
      again();
      reread();
    },
    download,
    restart,
    allow,
  };
}
