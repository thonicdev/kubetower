/**
 * The person's choices about the kinds column:
 * whether it shows the kinds holding nothing they may see, and which kinds are
 * favorites, per cluster.
 *
 * The request: kinds not in use are hidden by default, one button shows them,
 * and a favorite is kept and drawn whatever its occupancy.
 *
 * ## Kept by the server where it keeps them, by this browser otherwise
 *
 * `Capabilities.kindPreferences` says whether `GET`/`PUT /api/me/kinds` keep
 * them for the person, across browsers. Where it is false - a server older than
 * the member, or a profile with no per-person store - they are kept here, in
 * `localStorage` under `kubetower.kinds`, in **the same shape** the server
 * takes, so the two are one idea with two homes rather than two ideas.
 *
 * The store is `viewMemory.ts`'s shape: a module-level snapshot, a listener
 * list, every read and write wrapped, and what is read validated member by
 * member. A private window or blocked site data makes the choice last as long
 * as the tab - the memory is what is lost, never the column.
 *
 * ## Not known yet is not "hide"
 *
 * `useKindsChoice` answers `ready: false` while the capabilities, or the
 * server's copy, have not answered. The column hides nothing then: a choice
 * nobody has read is not a choice to hide anything by (R5 §5, an unknown is
 * never a zero).
 */
import { useSyncExternalStore } from 'react';

import { useCapabilities, useKindPreferences, useSaveKindPreferences, type KindPreferences } from '../api';

const KEY = 'kubetower.kinds';

/** The server's shape, `KindPreferences`, as this browser keeps it. */
export interface KindsChoice {
  readonly showUnused: boolean;
  /** Per cluster name, the favorite kinds' slugs, in the order they were
   *  marked. A cluster with none has no member. */
  readonly favourites: Readonly<Record<string, readonly string[]>>;
}

const NOTHING: KindsChoice = { showUnused: false, favourites: {} };

let snapshot: KindsChoice | undefined;
const listeners = new Set<() => void>();

export function subscribeToKindsMemory(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** What this browser keeps. Unreadable, absent and nonsense all answer the
 *  default: unused kinds hidden, no favorite. */
export function kindsMemory(): KindsChoice {
  snapshot ??= readStorage();
  return snapshot;
}

export function setKindsMemory(next: KindsChoice): void {
  snapshot = next;
  try {
    localStorage.setItem(KEY, JSON.stringify(next));
  } catch {
    // The choice then lasts as long as the tab; see the header.
  }
  for (const listener of [...listeners]) listener();
}

/** The choice with `showUnused` changed. */
export function withShowUnused(choice: KindsChoice, showUnused: boolean): KindsChoice {
  return { showUnused, favourites: choice.favourites };
}

/** The choice with one kind of one cluster marked, or unmarked. A cluster left
 *  with no favorite loses its member, so nothing grows by clusters visited. */
export function withFavourite(choice: KindsChoice, cluster: string, slug: string, on: boolean): KindsChoice {
  const held = choice.favourites[cluster] ?? [];
  if (held.includes(slug) === on) return choice;
  const next = on ? [...held, slug] : held.filter((one) => one !== slug);
  const favourites: Record<string, readonly string[]> = { ...choice.favourites };
  if (next.length === 0) delete favourites[cluster];
  else favourites[cluster] = next;
  return { showUnused: choice.showUnused, favourites };
}

/** Validates what any source handed over, member by member: a member of the
 *  wrong type is dropped, never trusted. Used on what `localStorage` held. */
export function readChoice(value: unknown): KindsChoice {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return NOTHING;
  const candidate = value as { showUnused?: unknown; favourites?: unknown };
  const favourites: Record<string, readonly string[]> = {};
  const held = candidate.favourites;
  if (held !== null && typeof held === 'object' && !Array.isArray(held)) {
    for (const [cluster, slugs] of Object.entries(held as Record<string, unknown>)) {
      if (!Array.isArray(slugs)) continue;
      const kept = [
        ...new Set(slugs.filter((slug): slug is string => typeof slug === 'string' && slug !== '')),
      ];
      if (kept.length > 0) favourites[cluster] = kept;
    }
  }
  return { showUnused: candidate.showUnused === true, favourites };
}

function readStorage(): KindsChoice {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw === null) return NOTHING;
    return readChoice(JSON.parse(raw));
  } catch {
    return NOTHING;
  }
}

/** Test seam. Leaves the module as a fresh import would, cached snapshot
 *  included. */
export function resetKindsMemory(): void {
  snapshot = undefined;
  listeners.clear();
  try {
    localStorage.removeItem(KEY);
  } catch {
    // Nothing to clear where nothing could be written.
  }
}

/** What the column asks of the choice, wherever it is kept. */
export interface KindsChoiceHandle {
  /** Whether the choice has been read. `false` while the capabilities or the
   *  server's copy have not answered: the column hides nothing then. */
  readonly ready: boolean;
  readonly showUnused: boolean;
  setShowUnused: (next: boolean) => void;
  /** This cluster's favorites, in the order they were marked. */
  readonly favourites: readonly string[];
  isFavourite: (slug: string) => boolean;
  toggleFavourite: (slug: string) => void;
}

/**
 * The choice for one cluster's column, from the server where it keeps them
 * (`Capabilities.kindPreferences`), from this browser otherwise.
 *
 * **A server copy that could not be read falls back to this browser**, for
 * reading and writing: the column keeps working, and the choice is kept where
 * it can be rather than lost on every click.
 */
export function useKindsChoice(cluster: string | undefined): KindsChoiceHandle {
  const capabilities = useCapabilities();
  const keeps = capabilities.data?.kindPreferences === true;
  const server = useKindPreferences(keeps);
  const save = useSaveKindPreferences();
  const local = useSyncExternalStore(subscribeToKindsMemory, kindsMemory, kindsMemory);

  const onServer = keeps && server.data !== undefined;
  const ready = keeps ? server.data !== undefined || server.isError : !capabilities.isPending;
  const choice: KindsChoice = onServer && server.data !== undefined ? readChoice(server.data) : local;

  const write = (next: KindsChoice): void => {
    if (onServer) {
      const body: KindPreferences = {
        showUnused: next.showUnused,
        favourites: Object.fromEntries(Object.entries(next.favourites).map(([name, slugs]) => [name, [...slugs]])),
      };
      save.mutate(body);
      return;
    }
    setKindsMemory(next);
  };

  const mine = cluster === undefined ? [] : (choice.favourites[cluster] ?? []);
  return {
    ready,
    showUnused: choice.showUnused,
    setShowUnused: (next) => {
      if (next !== choice.showUnused) write(withShowUnused(choice, next));
    },
    favourites: mine,
    isFavourite: (slug) => mine.includes(slug),
    toggleFavourite: (slug) => {
      if (cluster === undefined) return;
      write(withFavourite(choice, cluster, slug, !mine.includes(slug)));
    },
  };
}
