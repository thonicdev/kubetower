import { useMemo, useState } from 'react';

/**
 * Which rows are ticked, and what happens to them when the list is read again.
 *
 * The ticks are state and the rows are a function of it, so what is here is
 * two decisions, both of which a rewrite drops without noticing because
 * nothing about them is visible.
 *
 * ## 1. The count is written from the object and never counted off the DOM
 *
 * *"The DOM is two seconds stale by construction."* The panel is replaced every
 * two seconds, so a count taken by counting ticked checkboxes is a count of the
 * previous answer. In React it would be worse and quieter: counting rendered
 * rows would give a number that changes when a filter is typed, and a selection
 * bar saying "3 selected" over a filtered view where 11 are actually selected is
 * a destructive action confirmed against the wrong set. `count` below is
 * `picked.size` and nothing else has an opinion.
 *
 * ## 2. Pruning is skipped when the list failed
 *
 * The heavier one: empty and failed are different answers, inside a selection
 * helper too.
 * When the list is read again, rows that are no longer in it leave the
 * selection — **unless the read failed**, in which case "no rows" is not an
 * answer about what exists and pruning against it would silently empty the
 * operator's selection because a cluster hiccuped. Here it is a type: `listed` is
 * `undefined` on every arm that is not `ok`, so there is no list to prune
 * against and the code that would do it cannot be written.
 *
 * ## Scope
 *
 * Cluster plus kind. A selection is about a set of objects, and the same row id
 * on another kind is another object — carrying ticks across would offer a bulk
 * verb over things the operator never looked at. Changing the namespace or the
 * filter is **not** a scope change: those narrow the view of one set, and the
 * rows they hide are pruned only by rule 2 above, which is why a filter that
 * hides a ticked row does not untick it.
 */

export interface Selection {
  readonly picked: ReadonlySet<string>;
  /** From the object. Never from the table. */
  readonly count: number;
  /**
   * How many ticked rows left the list at the most recent successful read.
   *
   * Shown rather than swallowed: a selection that shrank between the preview and
   * the click is exactly the moment a bulk action stops being about the set the
   * operator chose.
   */
  readonly dropped: number;
  readonly allPicked: boolean;
  readonly somePicked: boolean;
  readonly toggle: (id: string) => void;
  /** Over the rows currently listed, which is all a tick-everything box can
   *  honestly mean when a filter is in force. */
  readonly setAll: (picked: boolean) => void;
  readonly clear: () => void;
}

interface State {
  scope: string;
  picked: ReadonlySet<string>;
  dropped: number;
}

export function useSelection(scope: string, listed: readonly string[] | undefined): Selection {
  const [state, setState] = useState<State>(() => ({ scope, picked: new Set<string>(), dropped: 0 }));

  const listedSet = useMemo(() => (listed === undefined ? undefined : new Set(listed)), [listed]);

  // Adjusted during render rather than in an effect: an effect would let one
  // paint happen with the previous kind's ticks on the new kind's rows, and that
  // paint is exactly long enough to click a bulk verb in.
  let current = state;

  if (state.scope !== scope) {
    current = { scope, picked: new Set<string>(), dropped: 0 };
    setState(current);
  } else if (listedSet !== undefined && state.picked.size > 0) {
    const kept = new Set<string>();
    for (const id of state.picked) if (listedSet.has(id)) kept.add(id);
    if (kept.size !== state.picked.size) {
      current = { scope, picked: kept, dropped: state.picked.size - kept.size };
      setState(current);
    }
  }

  const picked = current.picked;

  const allPicked =
    listed !== undefined && listed.length > 0 && listed.every((id) => picked.has(id));
  const somePicked = picked.size > 0;

  return {
    picked,
    count: picked.size,
    dropped: current.dropped,
    allPicked,
    somePicked,
    toggle: (id: string) => {
      setState((previous) => {
        const next = new Set(previous.picked);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        return { ...previous, picked: next, dropped: 0 };
      });
    },
    setAll: (want: boolean) => {
      setState((previous) => {
        if (listed === undefined) return previous;
        const next = new Set(previous.picked);
        for (const id of listed) {
          if (want) next.add(id);
          else next.delete(id);
        }
        return { ...previous, picked: next, dropped: 0 };
      });
    },
    clear: () => {
      setState((previous) => ({ ...previous, picked: new Set<string>(), dropped: 0 }));
    },
  };
}
