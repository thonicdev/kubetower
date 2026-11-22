import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { TableMenu } from './TableMenu';

/**
 * The table menu's Reset and its hint.
 *
 * A stored width is a share of the table, so a sized table always fills its
 * panel; what Reset is for is *forget what I arranged*, which is wanted by
 * somebody who knows he arranged something.
 *
 * So the assertions below are about two small things, and one of them is a
 * **negative**: a table with nothing stored offers nothing, so the hint cannot
 * become furniture.
 */

/** The `⋯`, which is found by the same name in both states — deliberately, so
 *  that a screen-reader user does not learn one control twice. */
const MORE = 'More table options';

function show(props: Partial<Parameters<typeof TableMenu>[0]> = {}): {
  onReset: ReturnType<typeof vi.fn>;
  onDensity: ReturnType<typeof vi.fn>;
} {
  const onReset = vi.fn();
  const onDensity = vi.fn();
  render(
    <TableMenu compact={false} onDensity={onDensity} onReset={onReset} arranged={false} {...props} />,
  );
  return { onReset, onDensity };
}

describe('the table menu', () => {
  it('says nothing about an arrangement on a table nobody has arranged', () => {
    // The negative first, because it is the one that keeps the change honest:
    // a marker drawn always is a permanent piece of furniture on every table in
    // the console, for a remedy most of them never need.
    show({ arranged: false });

    expect(screen.queryByTestId('table-arranged')).toBeNull();
    const more = screen.getByRole('button', { name: MORE });
    expect(more.getAttribute('aria-describedby')).toBeNull();
    expect(screen.queryByText('this table is arranged')).toBeNull();
  });

  it('marks the ⋯ on a table the server says is arranged, in a dot and in words', () => {
    show({ arranged: true });

    // The dot. It is the whole of the discoverability half: an operator cannot
    // otherwise tell, from a table, that this one is not the default one.
    expect(screen.getByTestId('table-arranged')).toBeTruthy();

    // And the same fact in words, because a colored circle is not a sentence.
    // It is a *description*: the name stays the same in both states, so the
    // control is one thing rather than two.
    const more = screen.getByRole('button', { name: MORE });
    const describedBy = more.getAttribute('aria-describedby');
    expect(describedBy).toBeTruthy();
    expect(document.getElementById(describedBy ?? '')?.textContent).toBe('this table is arranged');
  });

  it('offers the reset only where there is something to go back from', () => {
    show({ arranged: false });
    fireEvent.click(screen.getByRole('button', { name: MORE }));
    expect(screen.queryByRole('button', { name: 'Reset columns' })).toBeNull();
    // Compact rows is drawn either way, and that asymmetry is the component's
    // own argument: a preference that only appears once you have changed it is
    // one nobody finds, and an undo with nothing to undo is a dead control.
    expect(screen.getByRole('button', { name: /Compact rows/ })).toBeTruthy();

    // cleanup() and not a wiped body: a wiped body leaves the React tree alive
    // (and a lint rule holds it).
    cleanup();

    show({ arranged: true });
    fireEvent.click(screen.getByRole('button', { name: MORE }));
    expect(screen.getByRole('button', { name: 'Reset columns' })).toBeTruthy();
  });

  it('names all three arrangements it forgets, before the click rather than after it', () => {
    show({ arranged: true });
    fireEvent.click(screen.getByRole('button', { name: MORE }));

    // Found by the name it has always had. The price line below the label is
    // hidden from a reader, so it does not become part of the control's name -
    // the `title` carries it there, in full.
    const reset = screen.getByRole('button', { name: 'Reset columns' });

    // One word must not cover three acts: a column order lost by surprise is
    // the cost of a vague label.
    expect(reset.textContent).toContain('widths, order and hidden columns');
    expect(reset.getAttribute('title')).toBe(
      "Forget this table's column widths, its column order and which columns are hidden",
    );
  });

  it('closes itself on the reset, so the answer is read on the table', () => {
    const { onReset } = show({ arranged: true });

    fireEvent.click(screen.getByRole('button', { name: MORE }));
    fireEvent.click(screen.getByRole('button', { name: 'Reset columns' }));

    expect(onReset).toHaveBeenCalledTimes(1);
    // With no argument: the store and the endpoint take a scope, and
    // this menu does not carry one yet, because the click travels through
    // ResourceList's mutation to reach the server. Three items that all did the
    // same thing would be a worse lie than one honest item.
    expect(onReset).toHaveBeenCalledWith();
    expect(screen.queryByRole('button', { name: 'Reset columns' })).toBeNull();
  });
});
