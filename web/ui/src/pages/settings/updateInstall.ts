import { useIsMutating, useMutation, useMutationState, useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect } from 'react';

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

export function openUpdateFolder(): Promise<UpdateInstall> {
  return request<UpdateInstall>(path('/api/settings/update/open-folder'), { method: 'POST' });
}

/** "Install updates automatically", and when it installs (#122). */
export type InstallUpdatesWrite = components['schemas']['UpdateInstallWrite'];

export function setInstallUpdates(write: InstallUpdatesWrite): Promise<UpdateInstall> {
  return request<UpdateInstall>(path('/api/settings/update'), { method: 'PUT', body: write });
}

/**
 * What a verified automatic update waits on a person for (#122): a password
 * (a .deb or .rpm, never asked unprompted), or, set to install now, what a
 * restart would close. Nothing when it installs on its own.
 */
export function waitsOn(install: UpdateInstall): 'password' | 'open' | undefined {
  if (!install.enabled || install.phase !== 'ready') return undefined;
  if (install.password) return 'password';
  if (install.when === 'now' && (install.ends.terminals > 0 || install.ends.forwards > 0)) return 'open';
  return undefined;
}

/**
 * Whether the update needs a person: by hand, a newer release to download or
 * a step to take; automatic, only a wait on them, a failure or the manual
 * phase. The badge on Settings and its banner follow it.
 */
export function needsPerson(install: UpdateInstall, newer: boolean): boolean {
  if (!install.supported) return false;
  if (!install.enabled) return newer || install.phase !== 'idle';
  // Ready again with a reason (an install that did not finish) is said too.
  const unfinished = install.phase === 'ready' && (install.reason ?? '') !== '';
  return waitsOn(install) !== undefined || unfinished || install.phase === 'failed' || install.phase === 'manual';
}

export function getUpdateInstall(signal?: AbortSignal): Promise<UpdateInstall> {
  return request<UpdateInstall>(path('/api/settings/update'), signal ? { signal } : {});
}

/** The phases that move on their own, read again until they settle. */
const MOVING: ReadonlySet<string> = new Set(['downloading', 'verifying', 'installing']);

/** The phases a page knows how to draw; any other is drawn as nothing. */
export const PHASES: ReadonlySet<string> = new Set([
  'idle',
  'downloading',
  'verifying',
  'ready',
  'installing',
  'failed',
  'manual',
]);

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
const OPEN_FOLDER_KEY = ['update', 'open-folder'] as const;

/** How long a refused download or restart stays drawn. */
export const REFUSED_MS = 10_000;

/**
 * Forget a refused download or restart once it has been read: after
 * REFUSED_MS, and when `place` changes (another Settings tab) or the caller
 * goes. The refusal is shared by every surface, so it is forgotten in one place.
 */
export function useForgetRefusals(place: string): void {
  const cache = useQueryClient();
  const refused = useMutationState({ filters: { mutationKey: ['update'], status: 'error' } }).length;
  useEffect(() => {
    if (refused === 0) return undefined;
    const timer = setTimeout(() => forgetRefusals(cache), REFUSED_MS);
    return () => clearTimeout(timer);
  }, [cache, refused]);
  // Leaving the tab, or Settings, forgets it at once.
  useEffect(() => () => forgetRefusals(cache), [cache, place]);
}

function forgetRefusals(cache: ReturnType<typeof useQueryClient>): void {
  const mutations = cache.getMutationCache();
  for (const one of mutations.findAll({ mutationKey: ['update'], status: 'error' })) mutations.remove(one);
}

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
 * The current `installing`'s clock - when it began and whether its time ran
 * out - in the query cache rather than in a component, so the banner and
 * Infos' line, and a tab mounted later, read one clock and say the same thing.
 */
const CLOCK_KEY = ['update', 'installing-clock'] as const;

interface InstallClock {
  since: number;
  expired: boolean;
}

/** Mark the clock expired once INSTALL_TIMEOUT_MS has passed by the wall clock. */
function expireIfLate(cache: ReturnType<typeof useQueryClient>): void {
  cache.setQueryData<InstallClock | null>(CLOCK_KEY, (was) =>
    was !== null && was !== undefined && !was.expired && Date.now() - was.since >= INSTALL_TIMEOUT_MS ? { ...was, expired: true } : was,
  );
}

/**
 * Whether "Installing…" has waited past INSTALL_TIMEOUT_MS, and a way to start
 * the clock again. The clock starts when the phase becomes `installing` and is
 * cleared when it leaves it, so a later install never inherits an old timeout.
 */
function useInstallClock(installing: boolean): { timedOut: boolean; reset: () => void } {
  const cache = useQueryClient();
  const clock =
    useQuery<InstallClock | null>({
      queryKey: CLOCK_KEY,
      queryFn: () => null,
      enabled: false,
      initialData: null,
      staleTime: Infinity,
      gcTime: Infinity,
    }).data ?? null;
  const since = clock?.since;
  useEffect(() => {
    if (installing && since === undefined) {
      const fresh: InstallClock = { since: Date.now(), expired: false };
      cache.setQueryData<InstallClock | null>(CLOCK_KEY, fresh);
    }
    if (!installing && since !== undefined) cache.setQueryData<InstallClock | null>(CLOCK_KEY, null);
    if (!installing || since === undefined) return undefined;
    // Every surface arms the same timer; the first to fire marks that clock.
    const timer = setTimeout(
      () =>
        cache.setQueryData<InstallClock | null>(CLOCK_KEY, (was) => (was?.since === since ? { since, expired: true } : was)),
      Math.max(0, since + INSTALL_TIMEOUT_MS - Date.now()),
    );
    return () => clearTimeout(timer);
  }, [cache, installing, since]);
  return {
    timedOut: installing && clock?.expired === true,
    reset: () => cache.setQueryData<InstallClock | null>(CLOCK_KEY, null),
  };
}

/**
 * The update as the page draws it: the check's answer, the install state, and
 * the writes. `offered` is the badge's condition: a newer release this
 * installation can take that needs a person (needsPerson).
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
  /**
   * Wait for the restarted console again after a timeout, on a fresh clock.
   * Never a second install: the server answers 409 to one outside `ready`.
   */
  waitAgain: () => void;
  /** The last download or restart refused, whichever surface asked it. */
  refused: Error | null;
  download: ReturnType<typeof useMutation<UpdateInstall, Error, void>>;
  restart: ReturnType<typeof useMutation<UpdateInstall, Error, void>>;
  /** Show the verified file, where the installation's folder is not writable. */
  openFolder: ReturnType<typeof useMutation<UpdateInstall, Error, void>>;
  allow: ReturnType<typeof useMutation<UpdateInstall, Error, InstallUpdatesWrite>>;
} {
  const settings = useSettings();
  const update = settings.data?.info.update;
  const install = update?.install;
  const keep = useKeep();
  const cache = useQueryClient();
  const clock = useInstallClock(install?.phase === 'installing');
  const timedOut = clock.timedOut;
  const moving = install !== undefined && MOVING.has(install.phase) && !timedOut;
  useQuery({
    queryKey: [...settingsKey, 'update'],
    queryFn: async ({ signal }) => {
      // Each read also checks the clock against the wall: a timer is late by
      // as long as the machine slept, and the poll still runs (#99).
      expireIfLate(cache);
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
    // Refused, the console may have moved on: read what it says.
    onError: reread,
  });
  const openFolder = useMutation<UpdateInstall, Error, void>({
    mutationKey: OPEN_FOLDER_KEY,
    mutationFn: openUpdateFolder,
    onSuccess: keep,
    onError: reread,
  });
  // The server applies the setting even when it cannot save it (500
  // not_saved): read it back rather than keep the box where it was.
  const allow = useMutation<UpdateInstall, Error, InstallUpdatesWrite>({
    mutationFn: setInstallUpdates,
    onSuccess: keep,
    onError: reread,
  });
  const busy = useIsMutating({ mutationKey: ['update'] }) > 0;
  // The newest download or restart, from any surface: its refusal is drawn
  // wherever the control is, and a newer request clears it.
  const asked = useMutationState({ filters: { mutationKey: ['update'] }, select: (one) => one.state });
  const last = asked[asked.length - 1];
  const refused = last?.status === 'error' && last.error instanceof Error ? last.error : null;
  const offered = update?.state === 'newer' && install !== undefined && needsPerson(install, true);
  return {
    running: settings.data?.info.version,
    latest: update?.latest,
    install,
    offered,
    busy,
    timedOut,
    waitAgain: () => {
      clock.reset();
      reread();
    },
    refused,
    download,
    restart,
    openFolder,
    allow,
  };
}
