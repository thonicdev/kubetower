import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useSyncExternalStore, type KeyboardEvent, type ReactNode } from 'react';

import { setSeenVersion } from '../api/endpoints';
import { preferencesQuery, queryKeys, useCapabilities } from '../api/queries';
import type { Preferences } from '../api/types';
import { CloseButton } from '../components/CloseButton';
import { Icon, type IconName } from '../components/Icon';
import { Pill } from '../components/Pill';
import { externalHref } from '../externalHref';
import { focusRing } from '../focus';
import { useSettings } from '../pages/settings/data';
import { compareVersions, highlightsBetween, RELEASES, type Release } from './changelog';
import { rememberSeen, useSeenHere } from './seenMemory';

/**
 * Welcome and What's new (#80): a banner atop every page on the first visit of
 * a release this person has not seen.
 *
 *   never seen anything  Welcome to KubeTower, and the tour
 *   seen an older one    What's new, X.Y.Z: the highlights of every release
 *                        since, and the full changelog
 *   nothing notable      the tour
 *
 * × or Escape closes it, and it stays closed for this release: the desktop
 * keeps the release seen in its state directory (`preferences`), a server
 * console in each browser. Settings › Infos › What's new opens it again.
 * Nothing on a development build, nor under automation (`navigator.webdriver`)
 * or `?welcome=off`.
 */

type Shown = { kind: 'welcome' } | { kind: 'news'; releases: Release[] } | { kind: 'tour' };

/** The banner's own state, shared by every surface: opened from About, closed for a release. */
interface Banner {
  /** Opened from Settings › Infos, whatever was seen. */
  reopened: boolean;
  /** The release closed in this page, before the store has answered. */
  closed: string | undefined;
}

let banner: Banner = { reopened: false, closed: undefined };
const listeners = new Set<() => void>();
function setBanner(next: Banner): void {
  banner = next;
  for (const listener of listeners) listener();
}
function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
const current = (): Banner => banner;

/** Opens the banner again, from About. */
export function openWhatsNew(): void {
  setBanner({ ...banner, reopened: true });
}

/** Test seam. */
export function resetWhatsNew(): void {
  setBanner({ reopened: false, closed: undefined });
}

/** Automation and an explicit `?welcome=off` show no banner. */
function suppressed(): boolean {
  try {
    if (typeof navigator !== 'undefined' && navigator.webdriver) return true;
    return new URLSearchParams(window.location.search).get('welcome') === 'off';
  } catch {
    return false;
  }
}
const SUPPRESSED = suppressed();

/** What the banner shows now, and how it closes. */
export function useWhatsNew(): { release: string | undefined; shown: Shown | null; reopened: boolean; close: () => void } {
  const capabilities = useCapabilities().data;
  const release = capabilities?.release;
  // The desktop keeps it; a console that keeps no preferences, each browser.
  const keeps = capabilities?.preferences === true;
  const prefs = useQuery({ ...preferencesQuery(), enabled: keeps });
  const here = useSeenHere();
  const state = useSyncExternalStore(subscribe, current, current);
  const cache = useQueryClient();
  const write = useMutation({
    mutationFn: setSeenVersion,
    onSuccess: (_answer, version) => {
      cache.setQueryData<Preferences>(queryKeys.preferences, (was) =>
        was === undefined ? was : { ...was, seenVersion: version },
      );
    },
  });
  const close = (): void => {
    if (release === undefined || release === '') return;
    setBanner({ reopened: false, closed: release });
    if (keeps) write.mutate(release);
    else rememberSeen(release);
  };
  const off = { release, shown: null, reopened: state.reopened, close };
  if (release === undefined || release === '' || SUPPRESSED || capabilities === undefined) return off;
  if (state.reopened) {
    const news = highlightsBetween(RELEASES, undefined, release).filter((one) => one.version === release);
    return { ...off, shown: news.length > 0 ? { kind: 'news', releases: news } : { kind: 'tour' } };
  }
  if (state.closed === release) return off;
  if (keeps && prefs.data === undefined) return off;
  const seen = keeps ? prefs.data?.seenVersion : here;
  if (seen === undefined || seen === '') return { ...off, shown: { kind: 'welcome' } };
  const news = highlightsBetween(RELEASES, seen, release);
  if (news.length > 0) return { ...off, shown: { kind: 'news', releases: news } };
  // Seen this release or a newer one: nothing. An older one with nothing
  // notable since: the tour.
  return compareVersions(seen, release) < 0 ? { ...off, shown: { kind: 'tour' } } : off;
}

/** The main views, as tiles: what each is, in a few words. */
const TOUR: readonly { icon: IconName; label: string; caption: string }[] = [
  { icon: 'cluster', label: 'Clusters', caption: 'Every kubeconfig context' },
  { icon: 'workloads', label: 'Resources', caption: 'Every kind, live' },
  { icon: 'terminal', label: 'Dock', caption: 'Logs, shells, forwards' },
  { icon: 'extra', label: 'Extras', caption: 'Argo CD, Redis, Postgres…' },
  { icon: 'settings', label: 'Settings', caption: 'Extras, updates' },
];

export function WhatsNewBanner(): ReactNode {
  const { release, shown, reopened, close } = useWhatsNew();
  const box = useRef<HTMLElement>(null);
  // Opened from About: focus moves to it. Shown on its own, it takes nothing.
  useEffect(() => {
    if (reopened && shown !== null) box.current?.focus();
  }, [reopened, shown]);
  if (shown === null || release === undefined) return null;
  const onKeyDown = (event: KeyboardEvent): void => {
    if (event.key !== 'Escape') return;
    event.stopPropagation();
    close();
  };
  const title =
    shown.kind === 'welcome' ? 'Welcome to KubeTower' : shown.kind === 'news' ? "What's new" : 'KubeTower';
  return (
    <section
      ref={box}
      tabIndex={-1}
      aria-labelledby="whats-new-title"
      onKeyDown={onKeyDown}
      data-whats-new={shown.kind}
      className={`shrink-0 border-b border-l-2 border-line border-l-accent bg-panel px-3 py-2 text-xs ${focusRing}`}
    >
      <div className="flex items-center gap-2">
        <h2 id="whats-new-title" className="text-sm font-medium text-fg">
          {title}
        </h2>
        <Pill tone="info">{release}</Pill>
        {shown.kind === 'news' ? <ChangelogLink /> : null}
        <CloseButton onClick={close} size="inline" escape className="ml-auto" />
      </div>
      {shown.kind === 'news' ? <News releases={shown.releases} /> : <Tour />}
    </section>
  );
}

/**
 * The repository's CHANGELOG, from the settings read: asked only once news is
 * drawn, so a page with no banner reads nothing more.
 */
function ChangelogLink(): ReactNode {
  const repository = externalHref(useSettings().data?.info.repositoryUrl);
  if (repository === undefined) return null;
  return (
    <a
      href={`${repository.replace(/\/+$/, '')}/blob/main/CHANGELOG.md`}
      target="_blank"
      rel="noopener noreferrer"
      className={`text-accent underline underline-offset-2 ${focusRing}`}
      data-whats-new-changelog=""
    >
      Changelog
    </a>
  );
}

function News({ releases }: { releases: readonly Release[] }): ReactNode {
  return (
    <div className="mt-1.5 flex flex-wrap gap-x-6 gap-y-2" data-whats-new-releases="">
      {releases.map((one) => (
        <div key={one.version} className="min-w-0 basis-64" data-whats-new-release={one.version}>
          {releases.length > 1 ? <span className="font-medium text-muted">{one.version}</span> : null}
          <ul className="mt-0.5 list-disc space-y-0.5 pl-4 text-fg">
            {one.highlights.map((highlight) => (
              <li key={highlight}>{highlight}</li>
            ))}
          </ul>
        </div>
      ))}
    </div>
  );
}

function Tour(): ReactNode {
  return (
    <ul className="mt-1.5 grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-5" data-whats-new-tour="">
      {TOUR.map((tile) => (
        <li key={tile.label} className="flex min-w-0 items-center gap-2 rounded-md border border-line px-2 py-1.5">
          <Icon name={tile.icon} className="shrink-0 text-accent" />
          <span className="min-w-0">
            <span className="block font-medium text-fg">{tile.label}</span>
            <span className="block truncate text-muted" title={tile.caption}>
              {tile.caption}
            </span>
          </span>
        </li>
      ))}
    </ul>
  );
}
