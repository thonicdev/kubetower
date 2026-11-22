import { useEffect, useRef } from 'react';

import { acquireHandle, releaseHandle, type OpenedHandle } from './handles';

/**
 * Hold a live handle for as long as this component is mounted, and do something
 * with it while it is held.
 *
 * `key` is the identity of the thing, not of the component: two dock tabs
 * showing the same pod's shell share one PTY because they pass the same key,
 * and StrictMode's second mount shares it for the same reason (`./handles.ts`).
 *
 * ## Why this returns nothing
 *
 * The obvious shape - acquire in an effect, put the handle in `useState`,
 * return it - is wrong twice over, and `react-hooks/set-state-in-effect`
 * rejects it outright: it renders once with `undefined` and then cascades a
 * second render for a value the component could not have used during the first
 * one anyway.
 *
 * It could not, because a live handle is only ever *attached to something*: a
 * terminal to a DOM node, a socket to a callback. All of that is an effect
 * already. So the handle is delivered where it is usable rather than where it
 * would have to be guarded:
 *
 * ```tsx
 * const host = useRef<HTMLDivElement>(null);
 * useLiveHandle(`pty:${cluster}/${pod}`, openPty, (pty) => {
 *   if (host.current === null) return;
 *   return pty.attachTo(host.current);   // detached when this component goes
 * });
 * ```
 *
 * `attach` may return its own teardown, which runs before the hold is released.
 * That is the per-component half - detaching a view - and it is separate from
 * the handle's own `dispose`, which only runs once nobody is holding it at all.
 *
 * Neither `open` nor `attach` is in the dependency list. Both are new functions
 * on every render, and depending on them would close and reopen the handle on
 * every render - which is the failure this module exists to prevent. The key is
 * the dependency, because the key is the identity.
 */
export function useLiveHandle<T>(
  key: string,
  open: () => OpenedHandle<T>,
  attach?: (value: T) => void | (() => void),
): void {
  const latest = useRef({ open, attach });
  useEffect(() => {
    latest.current = { open, attach };
  });

  useEffect(() => {
    const value = acquireHandle(key, () => latest.current.open());
    const detach = latest.current.attach?.(value);
    return () => {
      detach?.();
      releaseHandle(key);
    };
  }, [key]);
}
