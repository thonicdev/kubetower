import { createContext, useContext, useMemo, useRef, useState, type ReactNode } from 'react';

import type { Extension } from '../extension';
import { forgetOpen, rememberOpen, type OpenObject } from './viewMemory';

/**
 * What a drawer is about, when it is about one object on one cluster. The
 * object page's identity has this exact shape and is passed as it is.
 */
export interface DrawerSubject extends OpenObject {
  readonly cluster: string;
}

interface ShellState {
  /** The extensions this build was handed. What the Shell *offers* is filtered
   *  against `GET /api/c/{cluster}/extensions` at render time; this is only the
   *  set that exists in the bundle. */
  readonly extensions: readonly Extension[];

  /** The detail drawer's contents, or `null` when it is empty. */
  readonly drawer: ReactNode | null;
  /**
   * Open it. `about` says which object, on which cluster, and is what
   * `viewMemory` remembers so the rail tile can return to it. A
   * drawer opened without it is remembered by nobody, which is the right
   * default for a drawer that is not one list's row.
   */
  readonly openDrawer: (content: ReactNode, about?: DrawerSubject) => void;
  /**
   * **The operator shut it** - the × button, Escape, or a control that takes
   * its place (a log or a shell in the dock). The memory of it is forgotten:
   * a tile that brought back a drawer somebody had deliberately closed would
   * be worse than one that forgot.
   */
  readonly closeDrawer: () => void;
  /**
   * **A navigation emptied it** - `Drawer.tsx`'s rule: nothing
   * follows a person to another page, and a cluster switch is the loudest
   * case. The memory is kept: the drawer as the page was left is what the
   * tile returns to.
   *
   * Two functions and not one flag, because the two callers are in different
   * files and a boolean defaulting to either value would make one of them
   * right by accident. The name at the call site is the whole distinction,
   * and it is the one place this goes wrong with every other test green.
   */
  readonly emptyDrawer: () => void;
}

interface DrawerState {
  readonly content: ReactNode;
  readonly about: DrawerSubject | undefined;
}

const ShellContext = createContext<ShellState | null>(null);

export function ShellProvider({
  extensions,
  children,
}: {
  extensions: readonly Extension[];
  children: ReactNode;
}): ReactNode {
  const [drawer, setDrawer] = useState<DrawerState | null>(null);
  // What the drawer holds *now*, for `closeDrawer` to read. The × button is
  // handed `closeDrawer` by the page that builds the drawer's content, in the
  // render before the drawer holds anything - so a closure over `drawer` there
  // would forget nothing, and the first shape of this did exactly that with
  // Escape (which reads the live context) passing. A ref is written in the
  // same handlers that write the state and is never read in render.
  const held = useRef<DrawerState | null>(null);

  const value = useMemo<ShellState>(
    () => ({
      extensions,
      drawer: drawer === null ? null : drawer.content,
      openDrawer: (content: ReactNode, about?: DrawerSubject) => {
        // Remembered as it opens, and not in an effect of the page that opened
        // it: the page is what `Drawer.tsx` unmounts on the way out, and the
        // memory is the thing meant to outlive it.
        if (about !== undefined) rememberOpen(about.cluster, about);
        held.current = { content, about };
        setDrawer(held.current);
      },
      closeDrawer: () => {
        const about = held.current?.about;
        if (about !== undefined) forgetOpen(about.cluster);
        held.current = null;
        setDrawer(null);
      },
      emptyDrawer: () => {
        held.current = null;
        setDrawer(null);
      },
    }),
    [extensions, drawer],
  );

  return <ShellContext value={value}>{children}</ShellContext>;
}

export function useShell(): ShellState {
  const value = useContext(ShellContext);
  if (value === null) throw new Error('useShell outside <ShellProvider>');
  return value;
}

/**
 * The shell's API where there may not be one, and `useDockIfPresent`'s twin.
 *
 * The object panel is rendered in three places and only two of them have a
 * Shell above them: the drawer and the object's own route do, and a test that
 * renders the panel alone does not. The panel's dock controls ask for this to
 * empty the drawer once a log or a shell is open (`LogsButton`, `Terminals`) -
 * a question with a real answer outside the shell, which is *there is no
 * drawer*, and not an error. A context a component reaches for has that cost:
 * either every caller is inside it or the accessor says what to do when it is
 * not.
 */
export function useShellIfPresent(): ShellState | null {
  return useContext(ShellContext);
}
