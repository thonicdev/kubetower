/**
 * How wide the detail drawer is drawn, and the width a person chose for it.
 *
 * ## Why
 *
 * A drawer may be wanted a bit wider, or narrower. The drawer is
 * `--drawer-w`, `min(560px, 46vw)`, for every kind
 * - it is one drawer - so the handle on its inner edge (`Drawer.tsx`) widens
 * every kind at once, and one width is remembered rather than one per kind. A
 * width per kind would make the drawer jump as a person moves from a pod to
 * its service, which is the same reading at the same screen.
 *
 * ## The bounds, and where the numbers come from
 *
 * - **The default is `--drawer-w`**, and it stays the default; the drawer can
 *   be narrowed below it too. Home, a
 *   double click, and any gesture that lands exactly on it go back to it, and
 *   back at the default nothing is stored.
 * - **Never narrower than the drawer's content needs**: the narrowest width
 *   at which its header (kind, the page link, close), its controls row (Logs,
 *   Shell, Claude and the verbs) and its tab strip each still fit on
 *   one line. Measured in the layout harness (layout test 23, Chrome on
 *   macOS): a pod with Claude, Restart, Evict and Delete needs **444px** - 407
 *   without Claude, which the server profile does not draw - and a service or
 *   a secret 268px. `DRAWER_FLOOR` is the widest of the three, and the floor
 *   for every kind: at 268 a secret's own Details panel already scrolls
 *   sideways (55px on a k3s `k3s-serving` secret), and one floor
 *   keeps a narrowed drawer the same width from one kind to the next.
 *   **And the drawer on screen is measured too** (`Drawer.tsx`,
 *   `contentNeed`), because the figure is a property of the font as much as
 *   of the content - the CI runner draws the same labels about 5% wider - and
 *   of the object: a kind with more verbs needs more. `need` is that
 *   measurement; where it is wider than the floor it wins, and `null` -
 *   nothing measured - keeps the default as the floor, so the drawer is never
 *   narrowed blind. Where the default itself is narrower - a narrow window -
 *   the default wins.
 * - **Never so wide that the list loses its usable minimum**, `LIST_MIN`. It is
 *   not chosen: it is what the list already gets at 1024px - the narrowest
 *   window the toolbar is proven whole at with a drawer open (layout test
 *   19) - with the 56px rail, the
 *   192px kinds column and the default drawer of 471px. 1024 - 56 - 192 - 471
 *   is 305, measured in the harness at 304.97.
 * - **On a window too narrow to give both, the drawer is drawn at its default**: the
 *   maximum is never under the default, so a widened drawer has nowhere to go
 *   and the list keeps what it always had.
 *
 * The clamp on a window resize is the stylesheet's, not a listener's: a
 * widened drawer shrinks first and stops at the default, and the list column
 * starts from `LIST_MIN` (`Drawer.tsx` says how). `drawerBounds` is the same
 * rule in numbers, for the handle's keyboard and its `aria-value*`.
 *
 * ## Per browser, and per window
 *
 * The width lives in `localStorage` under `KEY`, on every profile: the console
 * keeps no shared preference in a cluster, and this is the same
 * bargain `columnMemory.ts` strikes there - each person's own, in their own
 * browser, and no request sent.
 *
 * It follows the menu's rule (`menuMemory.ts`): a window reads the width once, a
 * gesture writes this window's width whole, and **nothing listens to
 * `storage`** - a drag in one window never resizes the drawer in another. The
 * next window to open starts from the last width any window saved.
 *
 * ## Storage may not be there at all
 *
 * Private windows, blocked site data, a locked-down profile: every read and
 * write can throw. A read that throws is the default; a write that throws keeps
 * the width for this window. That costs the memory and never the drawer.
 */
const KEY = 'kubetower.drawer.width';

/** `min(560px, 46vw)`, the two numbers of `--drawer-w` in `theme.css`. */
export const DRAWER_CAP = 560;
export const DRAWER_SHARE = 0.46;

/** The narrowest a drawer is drawn on a pod, a service or a secret, measured.
 *  See the header. */
export const DRAWER_FLOOR = 444;

/** The list's usable minimum beside a drawer. See the header. */
export const LIST_MIN = 305;

/** The keyboard steps, the column grip's (`DataGrid.tsx`): 16px, Shift 4px. */
export const STEP = 16;
export const FINE = 4;

/** Anything wider than this in storage was not written here. */
const SANE = 8192;

export interface DrawerBounds {
  readonly min: number;
  readonly max: number;
}

/** The width the drawer is drawn at when nothing was chosen. */
export function defaultDrawerWidth(viewport: number): number {
  return Math.min(DRAWER_CAP, viewport * DRAWER_SHARE);
}

/**
 * How narrow and how wide the drawer may be drawn. `room` is what the list and
 * the drawer share - the window less the rail and the kinds column. `need` is
 * the narrowest width the drawer's content fits in, or `null` where nothing
 * was measured, which keeps the default as the floor. Never under
 * `DRAWER_FLOOR`.
 */
export function drawerBounds(viewport: number, room: number, need: number | null): DrawerBounds {
  const fallback = defaultDrawerWidth(viewport);
  const min = need === null ? fallback : Math.min(fallback, Math.max(DRAWER_FLOOR, Math.ceil(need)));
  return { min, max: Math.max(fallback, room - LIST_MIN) };
}

/** Whether a width is the default at this window - what is forgotten rather
 *  than stored. Within half a pixel, because the default is a share of the
 *  window and a stored width is a whole pixel. */
export function isDefaultWidth(width: number, viewport: number): boolean {
  return Math.abs(width - defaultDrawerWidth(viewport)) < 0.5;
}

/** A width inside the bounds, to the pixel - and never under the minimum, which
 *  is why the floor is applied after the rounding. */
export function clampDrawerWidth(width: number, bounds: DrawerBounds): number {
  return Math.max(bounds.min, Math.min(bounds.max, Math.round(width)));
}

/** The snapshot. `undefined` is "not read yet"; `null` is "nothing chosen". */
let snapshot: number | null | undefined;

const listeners = new Set<() => void>();

/** The width this window draws, or `null` for the default. */
export function chosenDrawerWidth(): number | null {
  if (snapshot === undefined) snapshot = readStorage();
  return snapshot;
}

/** Told of this window's own changes and nothing else. */
export function subscribeToDrawerWidth(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** A person's gesture: a width, or `null` to go back to the default. */
export function chooseDrawerWidth(width: number | null): void {
  const next = width === null ? null : Math.round(width);
  snapshot = next;
  try {
    if (next === null) localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, String(next));
  } catch {
    // Refused: the width lasts as long as this window. Not reported - nothing
    // on screen is wrong and nothing the person could do would change it.
  }
  for (const listener of [...listeners]) listener();
}

function readStorage(): number | null {
  let raw: string | null;
  try {
    raw = localStorage.getItem(KEY);
  } catch {
    return null;
  }
  if (raw === null || !/^\d+$/.test(raw)) return null;
  const width = Number(raw);
  return width > 0 && width <= SANE ? width : null;
}

/** Test seam: leaves the module as a fresh import would. */
export function resetDrawerWidth(): void {
  snapshot = undefined;
  listeners.clear();
}
