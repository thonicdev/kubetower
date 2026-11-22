import { describe, expect, it } from 'vitest';

import { arrange, arrangeAll, isLastVisible } from './columns';

/**
 * Replaces the part of `columns_test.go` and `columnsempty_test.go` that is
 * about the arrangement rather than about the drag: *a stored layout that no
 * longer matches the table draws neither a ghost nor a gap*, and *a secondary
 * column is not drawn until it is asked for*.
 */

const column = (key: string, order: number, visible = true) => ({ key, label: key, order, visible });

describe('the column arrangement', () => {
  it('draws the columns in the stored order', () => {
    const arranged = arrange([column('age', 2), column('name', 0), column('status', 1)]);
    expect(arranged.map((c) => c.key)).toEqual(['name', 'status', 'age']);
  });

  it('draws a column the stored layout does not know about, rather than a gap', () => {
    // The server resolved an order for a column added since the layout was
    // stored. It is in the payload, so it is drawn - the failure this asserts
    // against is a client that renders a remembered list of keys and silently
    // drops anything not in it.
    const arranged = arrange([column('name', 0), column('restarts', 7), column('age', 1)]);
    expect(arranged.map((c) => c.key)).toEqual(['name', 'age', 'restarts']);
  });

  it('draws no ghost for a column the payload no longer carries', () => {
    // The other direction, and it is the server's half: the payload lists what
    // exists. Nothing here can resurrect a removed column, because nothing here
    // holds a list of its own.
    const arranged = arrange([column('name', 0)]);
    expect(arranged.map((c) => c.key)).toEqual(['name']);
  });

  it('keeps the payload order when two columns share one order', () => {
    // Stable sort, required since ES2019. Without it a duplicated order is two
    // columns swapping places on a re-render, which is a column that moves when
    // nothing moved it - and the list re-renders every two seconds.
    const first = arrange([column('a', 3), column('b', 3), column('c', 3)]);
    const again = arrange([column('a', 3), column('b', 3), column('c', 3)]);
    expect(first.map((c) => c.key)).toEqual(['a', 'b', 'c']);
    expect(again.map((c) => c.key)).toEqual(first.map((c) => c.key));
  });

  it('leaves a hidden column out of the grid and in the menu', () => {
    const columns = [column('name', 0), column('node', 1, false)];
    expect(arrange(columns).map((c) => c.key)).toEqual(['name']);
    expect(arrangeAll(columns).map((c) => c.key)).toEqual(['name', 'node']);
  });

  it('refuses to hide the last visible column', () => {
    // A table with no columns is not a narrower table. It is a page that has
    // lost its contents to a checkbox, and the way back is another checkbox in
    // a menu now attached to nothing.
    const columns = [column('name', 0), column('node', 1, false)];
    expect(isLastVisible(columns, 'name')).toBe(true);
    expect(isLastVisible([column('name', 0), column('node', 1)], 'name')).toBe(false);
  });
});
