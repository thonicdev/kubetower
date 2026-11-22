import type { Cluster, ClusterColour, ListResult, State } from '../api';

/**
 * A cluster's own state, drawn as one mark.
 *
 * `Cluster.state` exists because the list's envelope state answers a different
 * question: a `KUBECONFIG` naming five files one of which cannot be parsed is
 * `unreadable` for the *list*, and that "is a different fact from five clusters
 * that are all down". So one unreachable cluster leaves the other nine `ok`, and
 * the rail has to be able to say so per tile.
 *
 * The mark is never absence. A cluster that could not be asked keeps its tile -
 * dropping it would be the console's founding defect at the smallest possible
 * scale, since a rail short by one tile looks exactly like a rail that was
 * always that long.
 */

/** Full class strings, looked up. Tailwind scans source text, so an interpolated
 *  class name is one that exists in the DOM and in no stylesheet.
 *
 *  `pending` is the accent, and it is the only dot here that is neither gray nor
 *  on the warn/bad axis. `api/openapi.yaml`: *"`pending` is the ordinary state of
 *  every cluster at start-up: the probe runs on its own loop and this list answers
 *  out of what is already known"* - so at start-up this is most of the rail, and
 *  a rail that opens gray reads as a machine with nothing configured while a rail
 *  that opens amber reads as fourteen broken clusters. Neither is true and the
 *  second is the one that generates a support conversation. */
const DOT: Readonly<Record<State, string>> = {
  ok: 'bg-ok',
  empty: 'bg-muted',
  pending: 'bg-accent',
  absent: 'bg-muted',
  unreadable: 'bg-warn',
  forbidden: 'bg-warn',
  unreachable: 'bg-bad',
};

/**
 * The word for the dot, and the one place in this console where the six are
 * side by side and can be read against each other.
 *
 * **Where it is read.** In the tile's accessible description
 * (`stateSentence()`; its `title` is the name alone), in the header's dot and
 * on the icon's tooltip in Arrange - not printed beside the dot on the tile: at
 * rail width the word would be truncated, it would repeat what the color
 * already says, and it would cost every tile a line. A screen reader still gets
 * the sentence.
 *
 * **`not asked yet` against `could not be read`** is the distinction the sixth
 * state was added for, and the wording carries the whole of it: the four
 * failures are all in the past tense about an attempt that happened, and
 * `pending` is the only one that is not, because no attempt has happened. A
 * person who sees it knows the tile will change on its own; one who sees any
 * of the other four knows it will not without them.
 */
const WORDS: Readonly<Record<State, string>> = {
  ok: 'reachable',
  empty: 'nothing to show',
  pending: 'not asked yet',
  absent: 'not installed',
  unreadable: 'could not be read',
  forbidden: 'refused',
  unreachable: 'could not be reached',
};

/**
 * A tile's color, from the server's palette key.
 *
 * `Cluster.colour` is a key of the contract's `ClusterColour` enum and never a
 * CSS value, so the step from key to paint is this table and nothing else - the
 * same shape as `DOT` above, and for the same reason: full class strings,
 * because Tailwind scans source text. Each value pairs the swatch with its ink,
 * white on the deep half and dark on the pale half.
 *
 * `Record<ClusterColour, …>` makes the compiler hold the table to the enum: a
 * key added to the contract and not here is a type error, not a gray tile.
 */
const PLATE: Readonly<Record<ClusterColour, string>> = {
  red: 'bg-plate-red text-plate-ink-light',
  amber: 'bg-plate-amber text-plate-ink-light',
  lime: 'bg-plate-lime text-plate-ink-light',
  green: 'bg-plate-green text-plate-ink-light',
  teal: 'bg-plate-teal text-plate-ink-light',
  blue: 'bg-plate-blue text-plate-ink-light',
  violet: 'bg-plate-violet text-plate-ink-light',
  pink: 'bg-plate-pink text-plate-ink-light',
  'red-pale': 'bg-plate-red-pale text-plate-ink-dark',
  'amber-pale': 'bg-plate-amber-pale text-plate-ink-dark',
  'lime-pale': 'bg-plate-lime-pale text-plate-ink-dark',
  'green-pale': 'bg-plate-green-pale text-plate-ink-dark',
  'teal-pale': 'bg-plate-teal-pale text-plate-ink-dark',
  'blue-pale': 'bg-plate-blue-pale text-plate-ink-dark',
  'violet-pale': 'bg-plate-violet-pale text-plate-ink-dark',
  'pink-pale': 'bg-plate-pink-pale text-plate-ink-dark',
};

/** The server's limits on a nameplate (`nameplates.MaxName`, `MaxInitials`):
 *  every field that edits one stops where the server would cut. */
export const MAX_NAME = 32;
export const MAX_INITIALS = 3;

/** The sixteen keys in the order the swatch grid draws them: the eight deep
 *  hues, then the eight pale ones - `internal/nameplates`' own order. Read off
 *  `PLATE`, so a key cannot be in one list and not the other. */
export const PALETTE = Object.keys(PLATE) as readonly ClusterColour[];

/** What a swatch is called to a screen reader and on its tooltip: `teal-pale`
 *  is "Pale teal", the server's own label for it (`nameplates.Palette`). */
export function colourLabel(key: ClusterColour | undefined): string {
  // Only a server older than the required `colour` sends none.
  if (key === undefined) return 'Automatic';
  const pale = key.endsWith('-pale');
  const hue = pale ? key.slice(0, -'-pale'.length) : key;
  const word = `${hue.charAt(0).toUpperCase()}${hue.slice(1)}`;
  return pale ? `Pale ${hue}` : word;
}

/** The swatch and ink classes for a palette key. A key outside the enum - only
 *  a server older than the contract could send one - draws the neutral raised
 *  surface rather than nothing, so the tile is still a visible square. */
export function plateClass(colour: ClusterColour | undefined): string {
  return (colour === undefined ? undefined : PLATE[colour]) ?? 'bg-panel-2 text-fg';
}

export function stateDotClass(state: State): string {
  return DOT[state];
}

/**
 * The rail tile's corner badge: the dot's color, and a shape per kind of
 * state, so the badge says more than its color (WCAG 1.4.1).
 * A circle is reachable; a hollow ring has not been asked yet; a bar has
 * nothing to show or nothing installed; a square could not be read or was
 * refused; a diamond could not be reached. Two states share a shape where they
 * share a color and a meaning to act on, and the words that tell them apart
 * are the tile's description and its tooltip.
 */
const BADGE: Readonly<Record<State, string>> = {
  ok: 'size-2.5 rounded-full bg-ok',
  pending: 'size-2.5 rounded-full border-2 border-accent bg-panel',
  empty: 'h-1 w-2.5 rounded-full bg-muted',
  absent: 'h-1 w-2.5 rounded-full bg-muted',
  unreadable: 'size-2.5 rounded-xs bg-warn',
  forbidden: 'size-2.5 rounded-xs bg-warn',
  unreachable: 'size-2 rotate-45 rounded-xs bg-bad',
};

export function stateBadgeClass(state: State): string {
  return BADGE[state];
}

export function stateWord(state: State): string {
  return WORDS[state];
}

/** What is known about a cluster, as one sentence: the state word, and the
 *  server's sentence from `reason.message` where there is one, because "could
 *  not be reached" without a why is not usable in a support conversation.
 *
 *  It is the rail tile's accessible *description* (its name is the cluster's
 *  name alone, and so is its tooltip). */
export function stateSentence(cluster: Cluster): string {
  const word = stateWord(cluster.state);
  return cluster.reason === undefined ? word : `${word}: ${cluster.reason.message}`;
}

/**
 * The order the rail draws them in.
 *
 * Three keys.
 *
 * ## `bar` first
 *
 * A **hotbar**, `Cluster.bar`, 1-based, one ordered list of tiles each. That
 * matters more than it looks
 * - without it as the first key the rail interleaves two hotbars into one flat list, and
 * a tile's neighbors stop being the tiles it is grouped with, which is a
 * silently wrong rail rather than an unsorted one.
 *
 * A tile with **no** bar is on no hotbar - `api/openapi.yaml` says absence "is
 * what `hidden` also says and is the state a hidden tile is in" - so it sorts
 * after every bar rather than before bar 1.
 *
 * `order` is the stored arrangement within a bar and wins next. Where two
 * tiles carry the same order - or none, which is every tile on a fresh install -
 * the label decides, so the rail is stable across reloads rather than following
 * whatever order the `KUBECONFIG` was read in.
 */
export function arrange(clusters: readonly Cluster[]): Cluster[] {
  return [...clusters].sort((a, b) => {
    const ba = a.bar ?? Number.MAX_SAFE_INTEGER;
    const bb = b.bar ?? Number.MAX_SAFE_INTEGER;
    if (ba !== bb) return ba - bb;
    const oa = a.order ?? Number.MAX_SAFE_INTEGER;
    const ob = b.order ?? Number.MAX_SAFE_INTEGER;
    if (oa !== ob) return oa - ob;
    return a.label.localeCompare(b.label);
  });
}

/**
 * The one cluster, when there is exactly one - and `undefined` otherwise,
 * including while the list is in flight and when it failed.
 *
 * **The rail's condition, and it is the count rather than the profile.** Keying
 * the rail off `capabilities.mode` would fix a pod and leave a desktop with one
 * kubeconfig context looking at a column that chooses between one thing; the
 * count fixes both, and it comes back on its own the day there are two. A list
 * that has not answered, or answered with a failure, is not "one cluster": the
 * rail is what draws that failure, so it stays.
 *
 * **It stays the count for the rail.** The in-cluster console adds a condition
 * on the profile to this one for opening on its cluster - and that exception
 * is `sharedCluster.ts`, never this function.
 */
export function onlyCluster(result: ListResult<Cluster> | undefined): Cluster | undefined {
  if (result?.state !== 'ok' || result.items.length !== 1) return undefined;
  return result.items[0];
}
