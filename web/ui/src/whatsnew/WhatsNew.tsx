import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useId, useSyncExternalStore, type ReactNode } from 'react';
import { Link, useMatch } from 'react-router';

import { setSeenVersion } from '../api/endpoints';
import { preferencesQuery, queryKeys, useCapabilities, useExtensions } from '../api/queries';
import type { Preferences } from '../api/types';
import { clusterPath } from '../clusters/clusterPath';
import { btnPrimary } from '../components/buttons';
import { Dialog } from '../components/Dialog';
import { Icon, type IconName } from '../components/Icon';
import { Pill } from '../components/Pill';
import { externalHref } from '../externalHref';
import { focusRing } from '../focus';
import { KeyLayer } from '../keyboard/layers';
import { useSettings } from '../pages/settings/data';
import { compareVersions, highlightsBetween, RELEASES, type Highlight, type Release } from './changelog';
import { Mark } from './Mark';
import { rememberSeen, useSeenHere } from './seenMemory';

/**
 * Welcome and What's new (#80): on the first visit of a release this person has
 * not seen, a centred dialog - the one an extra's About opens (#107).
 *
 *   never seen anything  Welcome to KubeTower, and the tour
 *   seen an older one    What's new, X.Y.Z: the highlights of every release
 *                        since, and the full changelog
 *   nothing notable      the tour
 *
 * Close, Get started, Escape, a click on the veil or a card closes it, and it
 * stays closed for this release: the desktop
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
  /** How many times it was opened from Infos: each opening, and only it, takes the focus. */
  opens: number;
  /** The release closed in this page, before the store has answered. */
  closed: string | undefined;
  /** Why the release closed could not be kept: the banner is back, saying so. */
  failed: string | undefined;
}

const CLOSED: Banner = { reopened: false, opens: 0, closed: undefined, failed: undefined };
let banner: Banner = CLOSED;
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
  setBanner({ ...banner, reopened: true, opens: banner.opens + 1, failed: undefined });
}

/** Test seam. */
export function resetWhatsNew(): void {
  setBanner(CLOSED);
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
export function useWhatsNew(): {
  release: string | undefined;
  shown: Shown | null;
  /** Counts the openings from Infos, so focus is taken once per opening. */
  opens: number;
  failed: string | undefined;
  close: () => void;
} {
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
    // Not kept: it would come back on the next start unannounced, so it comes back now.
    onError: (error) => setBanner({ ...banner, closed: undefined, failed: error.message || 'Not saved' }),
  });
  const seen = keeps ? prefs.data?.seenVersion : here;
  const close = (): void => {
    if (release === undefined || release === '') return;
    setBanner({ ...banner, reopened: false, closed: release, failed: undefined });
    // An older build, opened from Infos, records nothing: the newer release
    // seen stays the one What's new counts from.
    if (seen !== undefined && seen !== '' && compareVersions(release, seen) <= 0) return;
    if (keeps) write.mutate(release);
    else rememberSeen(release);
  };
  const off = { release, shown: null, opens: state.opens, failed: state.failed, close };
  if (release === undefined || release === '' || SUPPRESSED || capabilities === undefined) return off;
  if (state.reopened) {
    const news = highlightsBetween(RELEASES, undefined, release).filter((one) => one.version === release);
    return { ...off, shown: news.length > 0 ? { kind: 'news', releases: news } : { kind: 'tour' } };
  }
  if (state.closed === release) return off;
  if (keeps && prefs.data === undefined) return off;
  if (seen === undefined || seen === '') return { ...off, shown: { kind: 'welcome' } };
  const news = highlightsBetween(RELEASES, seen, release);
  if (news.length > 0) return { ...off, shown: { kind: 'news', releases: news } };
  // Seen this release or a newer one: nothing. An older one with nothing
  // notable since: the tour.
  return compareVersions(seen, release) < 0 ? { ...off, shown: { kind: 'tour' } } : off;
}

/** A card of the welcome: where it leads, by what it shows. */
interface Card {
  key: string;
  icon: IconName;
  label: string;
  tip: string;
  /** The extra it opens, when it is one: off on this cluster, it leads to Settings › Extra. */
  extra?: string;
  /** Its page under the cluster: '' is the Overview. */
  page?: string;
}

/**
 * The product, as cards: each opens its view (#107). Their lines are the
 * site's (kubetower-site, `src/config/landing.ts` and `extras.ts`) where it has
 * one; Overview, Runtime Inspector and Event Intelligence have their own.
 */
const CARDS: readonly Card[] = [
  { key: 'clusters', icon: 'cluster', label: 'Clusters', tip: 'Every cluster your kubeconfig names, in one window.' },
  { key: 'overview', icon: 'overview', label: 'Overview', tip: 'Nodes, workloads and capacity at a glance.', page: '' },
  { key: 'netinspect', icon: 'topology', label: 'Network Inspector', tip: 'Would one pod reach another? Read from the rules.', extra: 'netinspect', page: 'x/netinspect' },
  { key: 'runtime', icon: 'coffee', label: 'Runtime Inspector', tip: 'Java, Go and Node.js, inspected on a click.', extra: 'runtime', page: 'x/runtime' },
  { key: 'events', icon: 'pulse', label: 'Event Intelligence', tip: 'Events grouped by cause, under what changed.', extra: 'events', page: 'x/events' },
  { key: 'extras', icon: 'extra', label: 'Extras', tip: 'Pages for what only some clusters run, switched on per cluster.' },
];

/** The welcome's words, the site's hero (#107): the one screen with a sentence. */
const TAGLINE = 'The free Kubernetes console.';
const INTRO = 'Every cluster in your kubeconfig in one window, from a single binary on your machine or a Helm chart in your cluster.';

/** Where a card leads, on the cluster in the address when there is one. */
function cardHref(card: Card, cluster: string | undefined, on: ReadonlySet<string>): string {
  if (card.key === 'clusters') return '/';
  if (card.page === undefined) return '/settings/extra';
  if (cluster === undefined) return card.extra === undefined ? '/' : '/settings/extra';
  if (card.extra !== undefined && !on.has(card.extra)) return '/settings/extra';
  return card.page === '' ? clusterPath(cluster) : `${clusterPath(cluster)}/${card.page}`;
}

export function WhatsNewDialog(): ReactNode {
  const { release, shown, failed, close } = useWhatsNew();
  if (shown === null || release === undefined) return null;
  const news = shown.kind === 'news';
  const title = shown.kind === 'welcome' ? 'Welcome to KubeTower' : news ? `What's new ${release}` : `KubeTower ${release}`;
  return (
    <Dialog title={title} onClose={close} layer={KeyLayer.Dialog} size="wide" showClose={false}>
      <div data-whats-new={shown.kind} className="flex flex-col gap-4">
        {news ? <News releases={shown.releases} /> : <Welcome onOpen={close} />}
        <div className="flex flex-wrap items-center gap-3">
          {news ? <ChangelogLink /> : null}
          {/* Always in the dialog, so a screen reader announces "Not saved" as it arrives. */}
          <span role="status" data-whats-new-status="">
            {failed !== undefined ? (
              <span className="text-bad" title={failed} data-whats-new-failed="">
                Not saved
              </span>
            ) : null}
          </span>
          <button type="button" className={`ml-auto ${btnPrimary}`} onClick={close} data-whats-new-done="">
            {news ? 'Close' : 'Get started'}
          </button>
        </div>
      </div>
    </Dialog>
  );
}

/**
 * The repository's CHANGELOG, from the settings read: asked only once news is
 * drawn, so a dialog with no news reads nothing more.
 */
function ChangelogLink(): ReactNode {
  const repository = externalHref(useSettings().data?.info.repositoryUrl);
  if (repository === undefined) return null;
  return (
    <a
      href={`${repository.replace(/\/+$/, '')}/blob/main/CHANGELOG.md`}
      target="_blank"
      rel="noopener noreferrer"
      className={`inline-flex items-center gap-1 text-accent underline underline-offset-2 ${focusRing}`}
      data-whats-new-changelog=""
    >
      Changelog
      <Icon name="external" />
    </a>
  );
}

/** Added and Fixed, told apart by their glyph and their badge. */
const KIND: Readonly<Record<Highlight['kind'], { icon: IconName; label: string; tone: string }>> = {
  added: { icon: 'plus', label: 'Added', tone: 'info' },
  fixed: { icon: 'check', label: 'Fixed', tone: 'ok' },
};

function News({ releases }: { releases: readonly Release[] }): ReactNode {
  return (
    <div className="flex flex-col gap-3" data-whats-new-releases="">
      {releases.map((one) => (
        <section key={one.version} aria-label={one.version} className="flex min-w-0 flex-col gap-1.5" data-whats-new-release={one.version}>
          {releases.length > 1 ? (
            <span className="self-start">
              <Pill>{one.version}</Pill>
            </span>
          ) : null}
          <ul className="flex flex-col gap-1.5">
            {one.highlights.map((highlight) => {
              const kind = KIND[highlight.kind];
              return (
                <li key={highlight.title} data-highlight={highlight.kind} className="flex min-w-0 items-center gap-2 rounded-md border border-line px-2.5 py-2">
                  <span aria-hidden="true" className={`grid size-7 shrink-0 place-items-center rounded-md border ${highlight.kind === 'added' ? 'border-accent/40 bg-accent/10 text-accent' : 'border-ok/40 bg-ok/10 text-ok'}`}>
                    <Icon name={kind.icon} />
                  </span>
                  <span className="min-w-0 flex-1 text-fg">{highlight.title}</span>
                  <Pill tone={kind.tone}>{kind.label}</Pill>
                </li>
              );
            })}
          </ul>
        </section>
      ))}
    </div>
  );
}

/** The logo, and a card per view: a click opens it and closes the dialog. */
function Welcome({ onOpen }: { onOpen: () => void }): ReactNode {
  const match = useMatch('/c/:cluster/*');
  const cluster = match?.params.cluster;
  const extensions = useExtensions(cluster);
  const on = new Set(extensions.data?.state === 'ok' ? extensions.data.items.filter((one) => one.enabled).map((one) => one.key) : []);
  return (
    <div className="flex flex-col items-center gap-4" data-whats-new-welcome="">
      <Mark className="size-16" />
      <div className="flex flex-col items-center gap-1 text-center">
        <p className="text-base font-medium text-fg" data-whats-new-tagline="">
          {TAGLINE}
        </p>
        <p className="max-w-xl text-muted" data-whats-new-intro="">
          {INTRO}
        </p>
      </div>
      <ul className="grid w-full grid-cols-2 gap-2 sm:grid-cols-3" data-whats-new-cards="">
        {CARDS.map((card) => (
          <WelcomeCard key={card.key} card={card} href={cardHref(card, cluster, on)} onOpen={onOpen} />
        ))}
      </ul>
    </div>
  );
}

/**
 * One card: its icon, its label and its line, which is also the link's
 * description (`aria-describedby`), never a `title` that reaches neither a
 * keyboard nor a touch screen.
 */
function WelcomeCard({ card, href, onOpen }: { card: Card; href: string; onOpen: () => void }): ReactNode {
  const tip = useId();
  return (
    <li className="min-w-0">
      <Link
        to={href}
        onClick={onOpen}
        aria-describedby={tip}
        data-whats-new-card={card.key}
        className={`flex h-full min-w-0 flex-col items-center gap-2 rounded-lg border border-line px-2 py-3 text-center hover:border-accent/60 hover:bg-white/5 ${focusRing}`}
      >
        <span aria-hidden="true" className="grid size-10 place-items-center rounded-lg border border-accent/40 bg-accent/10 text-accent [--icon:calc(var(--spacing)*5)]">
          <Icon name={card.icon} />
        </span>
        <span className="min-w-0 truncate font-medium text-fg">{card.label}</span>
        <span id={tip} data-whats-new-tip="" className="text-2xs text-muted">
          {card.tip}
        </span>
      </Link>
    </li>
  );
}
