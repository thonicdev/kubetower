import type { ComponentType } from 'react';

/**
 * The pages, as chunks of their own - one table, read by two callers.
 *
 * `routes.tsx` turns each entry into a `lazy` route, and `prefetch.ts` warms an
 * entry when a pointer rests on a link to it. They have to agree on which
 * chunk an address needs, and a table both import is how they agree; a second
 * list in either file would drift on the first page added.
 *
 * ## Why this exists at all
 *
 * As one chunk, the bundle is around 900 kB - xterm and every page in the
 * console - all downloaded and parsed before the sign-in form, the one page
 * reached with a cold cache.
 *
 * The second reason: `chunkReload.ts` - the recovery path for a tab left open
 * across an upgrade - only runs where a dynamic import can ask for a chunk that
 * no longer exists. What it needs is written on `routes.tsx`'s `lazy`.
 *
 * ## What stays out of this table
 *
 * The sign-in view, the Shell and the cluster list. They are the first paint:
 * the sign-in form is what a cold cache downloads, and the rail, the menu and
 * the dock are what an accepted session sees before it has chosen a cluster.
 * A lazy Shell would withhold all three while a page's chunk is on the wire.
 *
 * ## Warming is asking, not caching
 *
 * A dynamic `import()` of one specifier is idempotent by the language: the
 * module map holds one record per URL, so `route.lazy()` and a hover on the
 * same page issue one request between them. Nothing here memoizes the promise
 * - it would be a second cache in front of the browser's, and a rejected
 * promise held here would refuse a retry the engine might have allowed. The
 * set below records what was *asked for*, for the tests and for nothing else.
 */

/**
 * The first segments under `/c/:cluster` that are pages and not kinds, spelled
 * once. `routes.tsx` mounts Helm from `HELM` and exports both to the menu as
 * `NON_KIND_SEGMENTS` (its comment says why); `pageFor` below reads
 * them to keep an extra's `/x/…` page out of the object chunk.
 */
export const HELM = 'helm';
export const EXTENSION = 'x';
/** The open port-forwards. Process state and not a kind;
 *  the server's menu spells the same segment (`forwardsNavKey`,
 *  `internal/web/forwardsnav.go`). */
export const FORWARDS = 'port-forwards';

export type PageKey =
  | 'settings'
  | 'overview'
  | 'helmList'
  | 'helmRelease'
  | 'forwards'
  | 'resourceList'
  | 'object'
  | 'noExtension';

/**
 * One import per page module. Written out rather than derived from a path,
 * because Vite resolves `import()` statically: a template string here is a
 * glob of the whole `pages/` tree and one chunk again.
 */
const PAGES: Readonly<Record<PageKey, () => Promise<ComponentType>>> = {
  settings: async () => (await import('../pages/settings/Settings')).SettingsPage,
  overview: async () => (await import('../pages/overview/Overview')).Overview,
  helmList: async () => (await import('../pages/helm/HelmList')).HelmList,
  helmRelease: async () => (await import('../pages/helm/HelmRelease')).HelmReleasePage,
  forwards: async () => (await import('../pages/forwards/PortForwards')).PortForwards,
  resourceList: async () => (await import('../pages/resources/ResourceList')).ResourceList,
  object: async () => (await import('../pages/object/ObjectPage')).ObjectPage,
  noExtension: async () => (await import('../pages/extension/NoExtensionPage')).NoExtensionPage,
};

const asked = new Set<PageKey>();

/** Ask for a page's chunk. Idempotent - see the header. */
export function loadPage(key: PageKey): Promise<ComponentType> {
  asked.add(key);
  return PAGES[key]();
}

/**
 * Which page an internal address needs, or nothing.
 *
 * Keyed off the path rather than off the route table because the navigation
 * surfaces are served by the API: `Ref.href` is a string the server chose, and
 * the router's own table is not addressable by it (`prefetch.ts` says the same
 * of the queries). The shapes are the ones `routes.tsx` declares, and the two
 * have to be read together when one changes.
 *
 * `/c/:cluster/x/…` is an extra's page and never one of these: its chunk is the
 * extra's own, mounted through `extension.routes`, and warming the object page
 * for it would be a download nobody asked for. `menuMemory.ts` reserves the
 * same segment for the same reason.
 */
export function pageFor(pathname: string): PageKey | undefined {
  const segments = pathname.split('/').filter((segment) => segment !== '');
  const [head, , third] = segments;

  // `/settings` redirects to `/settings/general`; both want the one chunk.
  if (head === 'settings') return segments.length <= 2 ? 'settings' : undefined;
  if (head !== 'c' || segments.length < 2) return undefined;

  if (segments.length === 2) return 'overview';
  if (third === EXTENSION) return undefined;
  if (third === HELM) {
    if (segments.length === 3) return 'helmList';
    if (segments.length === 5) return 'helmRelease';
    return undefined;
  }
  if (third === FORWARDS) return segments.length === 3 ? 'forwards' : undefined;
  if (segments.length === 3) return 'resourceList';
  if (segments.length === 4 || segments.length === 5) return 'object';
  return undefined;
}

/**
 * Warm the chunk behind an internal path, where there is one. The query string
 * is cut off first, for the reason `prefetch.ts` gives: a menu href carries the
 * remembered sort as often as not.
 *
 * A failed warm is dropped on the floor, deliberately. It is an optimization;
 * the navigation that follows asks again, and *that* failure is the one
 * `RouteError` draws and `chunkReload.ts` acts on.
 */
export function warmPage(href: string): void {
  const key = pageFor(href.split(/[?#]/)[0] ?? '');
  if (key === undefined) return;
  loadPage(key).catch(() => {});
}

/** The pages asked for so far. For tests. */
export function askedPages(): PageKey[] {
  return [...asked];
}

/** Test seam. */
export function forgetAskedPages(): void {
  asked.clear();
}
