import { useQueryClient } from '@tanstack/react-query';
import { useCallback, useSyncExternalStore } from 'react';

import type { components } from '../api/schema';
import { kindOfHref } from './kindGlyph';

/**
 * The namespace chosen on one list, carried to the next, per cluster.
 *
 * A namespace filter is kept across kinds: Pods filtered to `default`, then
 * ConfigMaps from the menu, stays on `default`, though a list's namespace
 * lives only in its own address and the menu's links would otherwise carry none.
 *
 * ## The address stays the answer; this only writes links
 *
 * `viewMemory.ts` says why a namespace belongs in the address:
 * it is what somebody can paste to a colleague. So nothing here *applies* a
 * namespace to a page. The page shown is always the one its address names, and
 * what this store changes is the **href** of a link that opens another list -
 * the menu's rows, which the jump box follows, and the rail tile's way back to
 * a cluster. The selection is in the address before the page is drawn, so a
 * carried namespace and a pasted one are the same page, and the picker reads
 * it from where it reads every other.
 *
 * Doing it at arrival instead - a list with no `?namespace=` reaching for the
 * remembered one - was considered and refused: it cannot tell "opened from the
 * menu" from a link another page built with no namespace, such as the
 * Overview's, and that link is followed as written.
 *
 * ## What changes the choice, and what does not
 *
 * - **An address that names a namespace** - the picker's own write, a pasted
 *   link, a bookmark, a carried menu row - becomes the choice. `ResourceList`
 *   writes it once the kind is known to be namespaced, from its answer or from
 *   the menu's `Ref.namespaced` (`useMenuSaysNamespaced` below), so a list
 *   whose read was refused still remembers what its address asked for.
 * - **"All namespaces" chosen on the list itself** clears it. That is the
 *   picker's act and it is written by the picker's handler, at the moment it
 *   is chosen - never inferred from the address, because:
 * - **An address that names none changes nothing.** The node page's *Pods*
 *   link, an Overview card and a relation build a list link with no namespace
 *   on purpose; the page opens as the link says, on every namespace, and the
 *   menu goes on carrying the earlier choice. From the address alone, "the
 *   person cleared the picker" and "arrived on a bare link" are the same
 *   string, which is why the first is recorded where it happens.
 * - **A cluster-scoped kind** neither applies nor clears it, and the next
 *   namespaced list finds the choice where it was. A kind whose scope is not
 *   known yet writes nothing either: "not known to be namespaced" is neither
 *   answer.
 *
 * ## Which links carry it
 *
 * Only a link to a namespaced kind's list, and the scope is the server's
 * answer, never a guess from the name: the menu's `Ref.namespaced`, and for
 * the rail tile, the kinds whose lists answered `namespaced` in this session
 * (`listedAsNamespaced` below). A link that already names a namespace is left
 * as it is - it was written by something that meant that one.
 *
 * ## sessionStorage, per cluster, and it may not be there
 *
 * The issue's own scope: for the session, on every profile - the in-cluster
 * console keeps no server-side preferences, so the browser is the one place
 * that works on both. Not `localStorage`: a namespace filter that survives a
 * restart is a list silently showing 3 of 200 pods on a morning nobody
 * remembers choosing it, which is `internal/web/views.go`'s argument for
 * forgetting with the session. A store that cannot be written - a private
 * window, blocked site data - keeps the choice in this module for as long as
 * the page lives, and nothing is reported, as in `viewMemory.ts`.
 *
 * A namespace that has since been deleted is carried as it was chosen: the
 * list then says why it is empty, which is the answer a person can act on,
 * and a choice dropped behind their back is not.
 */

const KEY = 'kubetower.namespaces';

/** What is held for one cluster. */
interface ClusterChoice {
  /** The selection, canonical (sorted, unique, non-empty), and absent where
   *  "All namespaces" is the choice - which is the same as no choice. */
  readonly chosen?: readonly string[];
  /** The kinds whose list answered `namespaced: true` on this cluster, in this
   *  session: what the rail tile knows about a kind the menu is not drawing. */
  readonly namespaced?: readonly string[];
}

type Memory = Readonly<Record<string, ClusterChoice>>;

/** One empty array, so a subscriber reading "nothing chosen" twice reads the
 *  same value and `useSyncExternalStore` does not render again for it. */
const NONE: readonly string[] = Object.freeze([]);

let snapshot: Memory | undefined;
const listeners = new Set<() => void>();

/** Subscribe, for `useSyncExternalStore`. */
export function watchNamespaceMemory(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function memory(): Memory {
  snapshot ??= readStorage();
  return snapshot;
}

/** The selection carried on this cluster: empty for "All namespaces". */
export function chosenNamespaces(cluster: string): readonly string[] {
  return memory()[cluster]?.chosen ?? NONE;
}

/** Whether this kind's list answered `namespaced` on this cluster, this session. */
export function listedAsNamespaced(cluster: string, kind: string): boolean {
  return memory()[cluster]?.namespaced?.includes(kind) ?? false;
}

/** The selection carried on this cluster, kept current. */
export function useChosenNamespaces(cluster: string | undefined): readonly string[] {
  return useSyncExternalStore(
    watchNamespaceMemory,
    () => (cluster === undefined ? NONE : chosenNamespaces(cluster)),
    () => NONE,
  );
}

/**
 * What a namespaced list's address says, written down for its cluster.
 *
 * Called by `ResourceList` with the selection its address names, once the
 * kind is known to be namespaced, and by its picker with the selection chosen -
 * empty for "All namespaces", which clears the choice. Returns before
 * notifying anybody when nothing changed, so a list re-reading the same
 * namespace every two seconds writes nothing.
 */
export function rememberNamespaces(cluster: string, kind: string, selection: readonly string[]): void {
  const current = memory();
  const held = current[cluster];
  const chosen = canonical(selection);
  const knownKind = held?.namespaced?.includes(kind) ?? false;
  if (knownKind && same(held?.chosen ?? NONE, chosen)) return;

  const next: { chosen?: readonly string[]; namespaced?: readonly string[] } = {};
  if (chosen.length > 0) next.chosen = chosen;
  next.namespaced = knownKind ? (held?.namespaced ?? []) : [...(held?.namespaced ?? []), kind];
  commit({ ...current, [cluster]: next });
}

type NavMenu = components['schemas']['NavMenu'];
type NavGroup = components['schemas']['NavGroup'];

/**
 * Whether the menu already read for this cluster marks this kind's row
 * `namespaced` - the server's answer about its scope, known before (and
 * whatever) its list answers.
 *
 * `ResourceList` needs it for one case: an explicit `?namespace=` on a list
 * whose read was refused or failed. The list's own `namespaced` comes from its
 * answer, so without this the address would never be remembered there, though
 * it named a namespace on a kind that has them.
 *
 * **It reads the cache and never asks.** The menu is read by `Menu` and the
 * deferred custom resource heading by its disclosure; this walks whatever of
 * both is held under `['nav', cluster]` and answers false where nothing is. A
 * list mounted with no menu - its own tests, an extra's page - therefore asks
 * nothing new. Subscribed to the cache, so the answer moves when the menu
 * lands; a boolean, so the many other cache events re-render nothing.
 */
export function useMenuSaysNamespaced(cluster: string | undefined, kind: string | undefined): boolean {
  const client = useQueryClient();
  const subscribe = useCallback(
    (listener: () => void) => client.getQueryCache().subscribe(listener),
    [client],
  );
  const read = (): boolean => {
    if (cluster === undefined || kind === undefined) return false;
    for (const [, value] of client.getQueriesData<unknown>({ queryKey: ['nav', cluster] })) {
      if (refsOf(value).some((ref) => ref.namespaced === true && kindOfRef(ref.href) === kind)) {
        return true;
      }
    }
    return false;
  };
  return useSyncExternalStore(subscribe, read, () => false);
}

/** Every Ref in a cached menu or menu group answer, one level of nesting
 *  included; nothing for an answer that is not `ok`. */
/**
 * The menu's name for this kind - its row's text, "Ingress classes" for
 * `ingressclasses` - from the menu already read for this cluster.
 *
 * `ResourceList` titles its page and the breadcrumb from the list's answer,
 * and a refused or failed read carries no label: without this the page would
 * be titled with the address's segment while the kinds column, beside it,
 * says the kind's name. Reads the cache and never asks, exactly as
 * `useMenuSaysNamespaced` above, for the same reasons; `undefined` where the
 * menu holds no row for the kind.
 */
export function useMenuTitle(cluster: string | undefined, kind: string | undefined): string | undefined {
  const client = useQueryClient();
  const subscribe = useCallback(
    (listener: () => void) => client.getQueryCache().subscribe(listener),
    [client],
  );
  const read = (): string | undefined => {
    if (cluster === undefined || kind === undefined) return undefined;
    for (const [, value] of client.getQueriesData<unknown>({ queryKey: ['nav', cluster] })) {
      const ref = refsOf(value).find((one) => kindOfRef(one.href) === kind);
      if (ref !== undefined) return ref.text;
    }
    return undefined;
  };
  return useSyncExternalStore(subscribe, read, () => undefined);
}

function refsOf(value: unknown): readonly components['schemas']['Ref'][] {
  if (value === null || typeof value !== 'object') return [];
  const answer = value as { state?: unknown; data?: unknown };
  if (answer.state !== 'ok' || answer.data === null || typeof answer.data !== 'object') return [];
  const data = answer.data as Partial<NavMenu> & Partial<NavGroup>;
  const groups: NavGroup[] = Array.isArray(data.groups) ? [...data.groups] : [];
  if (Array.isArray(data.items)) groups.push(data as NavGroup);
  const out: components['schemas']['Ref'][] = [];
  for (const group of groups) {
    out.push(...(group.items ?? []));
    for (const sub of group.groups ?? []) out.push(...(sub.items ?? []));
  }
  return out;
}

/** The kind a menu href opens, decoded as the router decodes `:kind`. */
function kindOfRef(href: string | undefined): string | undefined {
  const segment = kindOfHref(href);
  if (segment === undefined) return undefined;
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment;
  }
}

/**
 * An href with the carried selection in its query, or the href as it was.
 *
 * As it was where there is nothing to carry, and where the href already names
 * a namespace: that link was written by something that meant that one - the
 * same rule `legacyParameters.ts` follows for a link carrying both spellings.
 * Everything else in the query (the sort `viewMemory` appends, an `?open=`) and
 * any fragment are kept. Repeated `namespace`, the spelling `useUrlList`
 * writes, so the address a carried link lands on is the one the picker would
 * have written.
 */
export function withNamespaces(href: string, selection: readonly string[]): string {
  if (selection.length === 0) return href;
  const hashAt = href.indexOf('#');
  const hash = hashAt === -1 ? '' : href.slice(hashAt);
  const beforeHash = hashAt === -1 ? href : href.slice(0, hashAt);
  const queryAt = beforeHash.indexOf('?');
  const path = queryAt === -1 ? beforeHash : beforeHash.slice(0, queryAt);
  const query = new URLSearchParams(queryAt === -1 ? '' : beforeHash.slice(queryAt + 1));
  if (query.has('namespace')) return href;
  for (const one of selection) query.append('namespace', one);
  return `${path}?${query.toString()}${hash}`;
}

function commit(next: Memory): void {
  snapshot = next;
  try {
    sessionStorage.setItem(KEY, JSON.stringify(next));
  } catch {
    // See the header: the choice lasts as long as the page, and nothing on
    // screen is wrong.
  }
  for (const listener of [...listeners]) listener();
}

/** Sorted, unique, non-empty: `useUrlList`'s canonical form, so the store and
 *  the address agree on when two selections are one. */
function canonical(values: readonly string[]): readonly string[] {
  return [...new Set(values.filter((one) => one !== ''))].sort();
}

function same(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((one, at) => one === b[at]);
}

/**
 * Read what is stored, defensively: written by an older build, or edited by
 * hand, and a shape that does not match is nothing rather than a crash on the
 * shell's first render. `viewMemory.readStorage` makes the same bargain.
 */
function readStorage(): Memory {
  try {
    const raw = sessionStorage.getItem(KEY);
    if (raw === null) return {};
    const parsed: unknown = JSON.parse(raw);
    if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
    const out: Record<string, ClusterChoice> = {};
    for (const [cluster, value] of Object.entries(parsed as Record<string, unknown>)) {
      if (value === null || typeof value !== 'object') continue;
      const entry = value as { chosen?: unknown; namespaced?: unknown };
      const choice: { chosen?: readonly string[]; namespaced?: readonly string[] } = {};
      const chosen = strings(entry.chosen);
      if (chosen !== undefined && chosen.length > 0) choice.chosen = canonical(chosen);
      const kinds = strings(entry.namespaced);
      if (kinds !== undefined) choice.namespaced = kinds;
      out[cluster] = choice;
    }
    return out;
  } catch {
    return {};
  }
}

function strings(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) return undefined;
  return value.filter((one): one is string => typeof one === 'string');
}

/** Test seam. Leaves the module as a fresh import would - the cached snapshot
 *  included, or one test's choice is still in memory for the next. */
export function resetNamespaceMemory(): void {
  snapshot = undefined;
  try {
    sessionStorage.removeItem(KEY);
  } catch {
    // Nothing to clear where nothing could be written.
  }
}
