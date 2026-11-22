import { useCallback, useMemo } from 'react';
import { useLocation, useSearchParams } from 'react-router';

/**
 * One query-string parameter, read and written as the address bar holds it.
 *
 * Three decisions, each a bug when it is missing.
 *
 * **1. `replace`, never `push`.** Writing a history entry per keystroke turns
 * Back into a backspace key: an operator who typed eight characters and wants
 * the previous page presses Back eight times and watches their own typing run
 * backwards. `{ replace: true }` is the fix in a router.
 *
 * **2. The key is the control's own name, not `q`.** At least one box in the
 * console filters server-side under a different name, and reflecting it as `q`
 * writes a parameter its own page does not read — a filter that appears to work
 * and is discarded on reload. So the name is a parameter of this hook and there
 * is no default.
 *
 * **3. The value is verbatim.** No trimming, no lower-casing, no dropping of a
 * trailing comma. The control is the parameter and the parameter is the
 * control; anything in between is a place for the two to disagree, and the
 * operator's typing is the authority on what they meant. An **empty** value is
 * removed from the address rather than written as `name=`, which is the one
 * exception and is not a transformation of a value — it is the difference
 * between asking for nothing and not asking.
 *
 * There is no per-kind filter memory in storage: the address is the only
 * place a filter lives.
 */
export function useUrlParameter(name: string): [string, (value: string) => void] {
  const [params, setParams] = useSearchParams();
  // Kept through the write: it is the same history entry, and its state is
  // what a page's Back reads (`back.ts`).
  const { state } = useLocation();
  const value = params.get(name) ?? '';

  const set = useCallback(
    (next: string) => {
      setParams(
        (previous) => {
          const updated = new URLSearchParams(previous);
          if (next === '') updated.delete(name);
          else updated.set(name, next);
          return updated;
        },
        { replace: true, state },
      );
    },
    [name, setParams, state],
  );

  return [value, set];
}

/**
 * Several parameters, written **in one navigation**.
 *
 * The same three decisions as `useUrlParameter` - `replace`, the control's own
 * names, verbatim values with an empty one removed - for a control that owns
 * more than one key. There is one such control: the list's filter box, which
 * writes `selector` and `search` from one string (`filterText.ts`).
 *
 * Why it cannot be two `useUrlParameter` calls: `useSearchParams`' functional
 * form is **not** a functional state update. React Router hands the updater a
 * copy of the *current render's* params, so two setters called in one handler
 * each start from the same address and the second navigation overwrites the
 * first - `app=api nginx` typed into one box would leave `?search=nginx` and
 * drop the selector, with nothing failing. One write, one `URLSearchParams`,
 * one `navigate`.
 *
 * The values object is memoized on its joined content, not on the
 * `URLSearchParams` object, for the reason `useUrlList` gives: the router hands
 * back a new object on every render and a new object in a dependency list is a
 * query key that changes every render. `names` is in the same list, so it has
 * to be a constant of the call site (`FILTER_PARAMETERS`) and not an array
 * literal in the render, or the memo is defeated the same way.
 */
export function useUrlParameters<Name extends string>(
  names: readonly Name[],
): [Readonly<Record<Name, string>>, (next: Readonly<Record<Name, string>>) => void] {
  const [params, setParams] = useSearchParams();
  // Kept through the write: it is the same history entry, and its state is
  // what a page's Back reads (`back.ts`).
  const { state } = useLocation();

  // JSON rather than a joiner, so that no character an operator could type is
  // a separator.
  const joined = JSON.stringify(names.map((name) => params.get(name) ?? ''));
  const values = useMemo(() => {
    const parts = JSON.parse(joined) as string[];
    const record = {} as Record<Name, string>;
    names.forEach((name, index) => {
      record[name] = parts[index] ?? '';
    });
    return record;
  }, [joined, names]);

  const set = useCallback(
    (next: Readonly<Record<Name, string>>) => {
      setParams(
        (previous) => {
          const updated = new URLSearchParams(previous);
          for (const name of names) {
            const value = next[name];
            if (value === '') updated.delete(name);
            else updated.set(name, value);
          }
          return updated;
        },
        { replace: true, state },
      );
    },
    [names, setParams, state],
  );

  return [values, set];
}
