import { useCallback, useEffect, type MouseEvent } from 'react';
import { useLocation, useNavigate, useNavigationType, type Location } from 'react-router';

/**
 * The way back from Settings to the page it was opened from.
 *
 * Settings sits outside the cluster scope (`/settings/:tab`), so nothing in its
 * address says where the person was. The page they left is carried in the
 * history entry's **state** instead: `{ from }`, written by every link that
 * opens Settings from a console page, forwarded by every move inside Settings
 * (its tabs, the rail's tiles there), which **replace** the entry rather than
 * add one. So:
 *
 * - one browser Back (or Alt+Left) from anywhere in Settings lands on the page
 *   it was opened from, because Settings is one entry above it;
 * - the Back control does the same, with `navigate(-1)` where the entry below
 *   is that page (`idx` says so) and a plain navigation to its address where it
 *   is not (a reload in another tab, a history that moved);
 * - a reload of Settings keeps the way back: the state is in `history.state`.
 *
 * Opened directly - a typed address, a bookmark, a new tab - there is no state,
 * and no Back control.
 *
 * **The scroll position** of `main#page` is noted when Settings is opened and
 * put back once the page it was opened from is drawn again, whichever way the
 * person went back (`useRestoreSettingsScroll`, mounted by the Shell).
 */
export interface SettingsOrigin {
  /** The whole address: path, query and hash. */
  path: string;
  /** The history entry's key, which a Back restores. */
  key: string;
  /** Its position in React Router's history (`history.state.idx`). */
  idx: number | undefined;
  /** `main#page`'s scrollTop when Settings was opened. */
  scroll: number;
}

export interface SettingsState {
  from: SettingsOrigin;
}

/** The region a navigation replaces, and the one that scrolls. */
const PAGE = 'page';

function historyIndex(): number | undefined {
  const state: unknown = window.history.state;
  if (typeof state !== 'object' || state === null) return undefined;
  const idx = (state as { idx?: unknown }).idx;
  return typeof idx === 'number' ? idx : undefined;
}

/** The origin a Settings location carries, or none. Read defensively: a
 *  history entry's state can be anything another version wrote. */
export function originOf(location: Pick<Location, 'state'>): SettingsOrigin | undefined {
  const state: unknown = location.state;
  if (typeof state !== 'object' || state === null) return undefined;
  const from = (state as { from?: unknown }).from;
  if (typeof from !== 'object' || from === null) return undefined;
  const { path, key, idx, scroll } = from as Partial<SettingsOrigin>;
  if (typeof path !== 'string' || !path.startsWith('/') || path.startsWith('//')) return undefined;
  return {
    path,
    key: typeof key === 'string' ? key : '',
    idx: typeof idx === 'number' ? idx : undefined,
    scroll: typeof scroll === 'number' && scroll > 0 ? scroll : 0,
  };
}

export function isSettingsPath(pathname: string): boolean {
  return pathname === '/settings' || pathname.startsWith('/settings/');
}

/** The state to carry into Settings from `location`: a new origin from a
 *  console page, the one already carried from inside Settings. */
export function settingsStateFrom(location: Location): SettingsState | undefined {
  if (isSettingsPath(location.pathname)) {
    const carried = originOf(location);
    return carried === undefined ? undefined : { from: carried };
  }
  return {
    from: {
      path: `${location.pathname}${location.search}${location.hash}`,
      key: location.key,
      idx: historyIndex(),
      scroll: document.getElementById(PAGE)?.scrollTop ?? 0,
    },
  };
}

/**
 * Open Settings at `to`, carrying where the person is. Returns a click handler
 * for a link (a plain left click only: a modified click keeps the browser's
 * own meaning, a new tab, which has nothing to go back to) and a function for
 * a menu item.
 */
export function useOpenSettings(): {
  open: (to: string) => void;
  onClick: (to: string) => (event: MouseEvent<HTMLAnchorElement>) => void;
} {
  const location = useLocation();
  const navigate = useNavigate();
  const open = useCallback(
    (to: string): void => {
      const state = settingsStateFrom(location);
      // From inside Settings, a move is not a new entry: one Back still leaves.
      const inside = isSettingsPath(location.pathname);
      void navigate(to, { ...(state === undefined ? {} : { state }), ...(inside ? { replace: true } : {}) });
    },
    [location, navigate],
  );
  const onClick = useCallback(
    (to: string) =>
      (event: MouseEvent<HTMLAnchorElement>): void => {
        if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
          return;
        }
        event.preventDefault();
        open(to);
      },
    [open],
  );
  return { open, onClick };
}

/** Back to where Settings was opened from, or `undefined` where it was not
 *  opened from a console page. */
export function useSettingsBack(): { origin: SettingsOrigin; back: () => void } | undefined {
  const location = useLocation();
  const navigate = useNavigate();
  const origin = originOf(location);
  const back = useCallback((): void => {
    if (origin === undefined) return;
    pendingScroll = { key: origin.key, path: origin.path, top: origin.scroll };
    const here = historyIndex();
    if (origin.idx !== undefined && here === origin.idx + 1) {
      void navigate(-1);
      return;
    }
    void navigate(origin.path, { replace: true });
  }, [navigate, origin]);
  return origin === undefined ? undefined : { origin, back };
}

/** The scroll position to put back, and on which entry. */
let pendingScroll: { key: string; path: string; top: number } | undefined;

/**
 * Note the origin's scroll for when the person leaves Settings by the
 * browser's Back rather than the control. Mounted by the Settings page.
 */
export function useNoteSettingsScroll(origin: SettingsOrigin | undefined): void {
  useEffect(() => {
    if (origin === undefined) return;
    return () => {
      pendingScroll ??= { key: origin.key, path: origin.path, top: origin.scroll };
    };
  }, [origin?.key, origin?.path, origin?.scroll]); // eslint-disable-line react-hooks/exhaustive-deps
}

/** How long a page may take to grow tall enough for its old position. */
const RESTORE_MS = 3000;

/**
 * Put the page's scroll back once Settings has been left for its origin.
 * Mounted once, by the Shell. The page may still be reading: the position is
 * tried on each frame until the page is tall enough, for at most three seconds,
 * and given up as soon as the person scrolls.
 */
export function useRestoreSettingsScroll(): void {
  const location = useLocation();
  const type = useNavigationType();
  useEffect(() => {
    const wanted = pendingScroll;
    if (wanted === undefined || isSettingsPath(location.pathname)) return;
    pendingScroll = undefined;
    const same =
      type === 'POP' ? location.key === wanted.key : `${location.pathname}${location.search}${location.hash}` === wanted.path;
    if (!same || wanted.top <= 0) return;
    const page = document.getElementById(PAGE);
    if (page === null) return;
    const started = performance.now();
    let frame = 0;
    let stopped = false;
    const stop = (): void => {
      stopped = true;
    };
    page.addEventListener('wheel', stop, { passive: true });
    page.addEventListener('touchstart', stop, { passive: true });
    page.addEventListener('keydown', stop);
    const step = (): void => {
      if (stopped) return;
      page.scrollTop = wanted.top;
      if (Math.abs(page.scrollTop - wanted.top) < 1 || performance.now() - started > RESTORE_MS) return;
      frame = requestAnimationFrame(step);
    };
    step();
    return () => {
      cancelAnimationFrame(frame);
      page.removeEventListener('wheel', stop);
      page.removeEventListener('touchstart', stop);
      page.removeEventListener('keydown', stop);
    };
  }, [location, type]);
}

/**
 * A short name for an origin's address, for the Back control: the cluster and
 * the page under it (`prod › pods`, the cluster by the label the rail shows),
 * the cluster alone for its overview,
 * `Clusters` for the console's home. The whole address goes in its tooltip.
 */
export function originLabel(path: string, clusterLabel: (name: string) => string = (name) => name): string {
  const bare = path.split(/[?#]/)[0] ?? '';
  const parts = bare.split('/').filter((one) => one !== '').map((one) => {
    try {
      return decodeURIComponent(one);
    } catch {
      return one;
    }
  });
  if (parts.length === 0) return 'Clusters';
  if (parts[0] === 'c' && parts[1] !== undefined) {
    const page = parts[2] === 'x' ? parts[3] : parts[2];
    const cluster = clusterLabel(parts[1]);
    return page === undefined ? cluster : `${cluster} › ${page}`;
  }
  return parts.join(' › ');
}
