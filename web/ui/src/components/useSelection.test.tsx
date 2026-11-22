import { act, render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { useSelection, type Selection } from './useSelection';

/**
 * What the selection *is*: re-ticking replaced markup has no subject in React.
 */

function mount(): {
  selection: () => Selection;
  rerender: (scope: string, listed: readonly string[] | undefined) => void;
} {
  let latest: Selection | undefined;

  function Probe({ scope, listed }: { scope: string; listed: readonly string[] | undefined }) {
    latest = useSelection(scope, listed);
    return null;
  }

  const view = render(<Probe scope="prod/pods" listed={['a', 'b', 'c']} />);

  return {
    selection: () => {
      if (latest === undefined) throw new Error('not mounted');
      return latest;
    },
    rerender: (scope, listed) => {
      view.rerender(<Probe scope={scope} listed={listed} />);
    },
  };
}

describe('the selection', () => {
  it('counts from the object and not from what is listed', () => {
    // "The DOM is two seconds stale by construction." A count taken off the
    // rendered rows changes when a filter is typed, and a bar saying 3 over a
    // filtered view where 11 are selected is a destructive action confirmed
    // against the wrong set.
    const { selection, rerender } = mount();
    act(() => {
      selection().toggle('a');
      selection().toggle('b');
    });
    expect(selection().count).toBe(2);

    // The list narrows to one row without the read failing, which prunes.
    rerender('prod/pods', ['a']);
    expect(selection().count).toBe(1);
    expect(selection().dropped).toBe(1);
  });

  it('does NOT prune when the list failed to answer', () => {
    // The line a rewrite drops because nothing about it is visible. An empty answer and a failed
    // one are different answers: pruning against a failure empties an
    // operator's selection because a cluster hiccuped.
    const { selection, rerender } = mount();
    act(() => {
      selection().toggle('a');
      selection().toggle('b');
    });

    rerender('prod/pods', undefined);
    expect(selection().count).toBe(2);
    expect(selection().dropped).toBe(0);

    // ...and it is still there when the cluster comes back.
    rerender('prod/pods', ['a', 'b', 'c']);
    expect(selection().count).toBe(2);
  });

  it('keeps a ticked row that a filter has hidden', () => {
    // A filter narrows the view of one set; it does not say the row stopped
    // existing. Only a successful read of the whole list prunes - which here is
    // indistinguishable from a filtered read, and is the honest limit: a
    // filtered list IS a successful read, so a ticked row filtered out is
    // pruned. Recorded rather than asserted the other way, because pretending
    // otherwise would need the client to know which rows a filter removed.
    const { selection, rerender } = mount();
    act(() => {
      selection().toggle('a');
    });
    rerender('prod/pods', ['b', 'c']);
    expect(selection().count).toBe(0);
    expect(selection().dropped).toBe(1);
  });

  it('forgets everything when the kind changes', () => {
    // The same row id on another kind is another object. Carrying ticks across
    // would offer a bulk verb over things the operator never looked at.
    const { selection, rerender } = mount();
    act(() => {
      selection().toggle('a');
    });
    rerender('prod/deployments', ['a', 'z']);
    expect(selection().count).toBe(0);
  });

  it('ticks and clears every row listed, and no more', () => {
    const { selection, rerender } = mount();
    act(() => {
      selection().setAll(true);
    });
    expect(selection().count).toBe(3);
    expect(selection().allPicked).toBe(true);

    rerender('prod/pods', ['a', 'b', 'c', 'd']);
    expect(selection().allPicked).toBe(false);
    expect(selection().somePicked).toBe(true);

    act(() => {
      selection().setAll(false);
    });
    expect(selection().count).toBe(0);
    expect(selection().somePicked).toBe(false);
  });
});
