import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { KeyboardStack } from '../keyboard/KeyboardStack';
import { ColumnMenu } from './ColumnMenu';
import { FilterChips } from './FilterMenu';
import { NamespaceMenu } from './NamespaceMenu';
import { toolbarBase } from './toolbar';

/**
 * A list's toolbar holds the namespace menu, the filter chips, the search box
 * and its three controls, on one line. They measured 28, 24 and 32 px, so the
 * line was ragged; each now takes the toolbar's own height. jsdom lays nothing
 * out, so what is pinned is the height class, read from `toolbar.ts` rather
 * than written here a second time.
 */
const HEIGHT = /(?:^|\s)(h-\d+)(?:\s|$)/.exec(toolbarBase)?.[1];

function heightsOf(element: HTMLElement): string[] {
  return [...element.classList].filter((one) => /^(?:min-)?h-\d+$/.test(one));
}

describe("a list toolbar's controls", () => {
  it('take the height of the search box and its controls', () => {
    expect(HEIGHT).toBe('h-8');
    render(
      <KeyboardStack>
        <NamespaceMenu available={['default', 'shop']} selected={[]} onChange={() => undefined} />
        <FilterChips
          chips={[{ parameter: 'Node:a', column: 'Node', value: 'a' }]}
          onRemove={() => undefined}
        />
      </KeyboardStack>,
    );

    const menu = screen.getByRole('button', { name: /namespace/i });
    const chip = screen.getByRole('button', { name: 'Remove the filter Node: a' });
    expect(heightsOf(menu)).toEqual([HEIGHT]);
    expect(heightsOf(chip)).toEqual([HEIGHT]);
  });

  it('joins Columns to the funnel: a shared border and a squared left edge', () => {
    render(
      <KeyboardStack>
        <ColumnMenu
          columns={[{ key: 'a', visible: true, order: 0 }]}
          labelOf={(column) => column.key}
          onShow={() => undefined}
        />
      </KeyboardStack>,
    );
    const columns = screen.getByRole('button', { name: 'Columns' });
    expect(columns.classList.contains('rounded-l-none')).toBe(true);
    expect(columns.parentElement?.classList.contains('-ml-px')).toBe(true);
    expect(heightsOf(columns)).toEqual([HEIGHT]);
  });
});
