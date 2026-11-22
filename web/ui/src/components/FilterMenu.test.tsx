import { act, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { KeyboardStack } from '../keyboard/KeyboardStack';
import { resetKeyboardStack } from '../keyboard/stack';
import { FilterChips, FilterMenu } from './FilterMenu';

/**
 * The filter control on its own, apart from the page.
 */

afterEach(() => {
  resetKeyboardStack();
});

describe('the filter control', () => {
  it('draws the empty cell’s dash as “(none)”, and still sends the dash', () => {
    const onToggle = vi.fn();
    render(
      <KeyboardStack>
        <FilterMenu
          columns={[{ label: 'Node', values: ['—', 'kubeworker1'], chosen: [] }]}
          onToggle={onToggle}
          onClear={() => undefined}
        />
      </KeyboardStack>,
    );
    act(() => {
      fireEvent.click(screen.getByRole('button', { name: /^Filter by column/ }));
    });
    const none = screen.getByRole('button', { name: '(none)' });
    act(() => {
      fireEvent.click(none);
    });
    expect(onToggle).toHaveBeenCalledWith('Node', '—');

    render(
      <FilterChips chips={[{ parameter: 'Node:—', column: 'Node', value: '—' }]} onRemove={() => undefined} />,
    );
    expect(screen.getByRole('button', { name: 'Remove the filter Node: (none)' })).not.toBeNull();
  });

  it('gives the focus back to the funnel on Escape, rather than leaving it nowhere', () => {
    render(
      <KeyboardStack>
        <FilterMenu
          columns={[{ label: 'Status', values: ['Running'], chosen: [] }]}
          onToggle={() => undefined}
          onClear={() => undefined}
        />
      </KeyboardStack>,
    );
    const funnel = screen.getByRole('button', { name: /^Filter by column/ });
    act(() => {
      fireEvent.click(funnel);
    });
    const value = screen.getByRole('button', { name: 'Running' });
    value.focus();
    act(() => {
      fireEvent.keyDown(value, { key: 'Escape' });
    });
    expect(screen.queryByRole('group', { name: 'Filter by column' })).toBeNull();
    expect(document.activeElement).toBe(funnel);
  });

  // The button carries an icon and no words, and Filter > Status drops
  // down the values the column holds.
  it('opens on the columns, and a column drops down its values', () => {
    render(
      <KeyboardStack>
        <FilterMenu
          columns={[
            { label: 'Status', values: ['Running', 'Pending'], chosen: [] },
            { label: 'Node', values: ['kubeworker1'], chosen: [] },
          ]}
          onToggle={() => undefined}
          onClear={() => undefined}
        />
      </KeyboardStack>,
    );
    const funnel = screen.getByRole('button', { name: /^Filter by column/ });
    expect(funnel.textContent).toBe('');
    act(() => {
      fireEvent.click(funnel);
    });
    expect(screen.queryByText(/Show only the rows/)).toBeNull();
    expect(screen.queryByRole('button', { name: 'Running' })).toBeNull();
    const status = screen.getByRole('button', { name: 'Status' });
    expect(status.getAttribute('aria-expanded')).toBe('false');
    expect(screen.getByRole('button', { name: 'Node' }).getAttribute('aria-expanded')).toBe('false');
    act(() => {
      fireEvent.click(status);
    });
    expect(status.getAttribute('aria-expanded')).toBe('true');
    expect(screen.getByRole('button', { name: 'Running' })).not.toBeNull();
    expect(screen.getByRole('button', { name: 'Pending' })).not.toBeNull();
    expect(screen.queryByRole('button', { name: 'kubeworker1' })).toBeNull();
  });

  it('keeps the accent border while a filter is active, and the neutral one otherwise', () => {
    const { rerender } = render(
      <KeyboardStack>
        <FilterMenu
          columns={[{ label: 'Status', values: ['Running'], chosen: [] }]}
          onToggle={() => undefined}
          onClear={() => undefined}
        />
      </KeyboardStack>,
    );
    let funnel = screen.getByRole('button', { name: /^Filter by column/ });
    expect(funnel.className).toContain('border-line');
    expect(funnel.className).not.toContain('border-accent');
    rerender(
      <KeyboardStack>
        <FilterMenu
          columns={[{ label: 'Status', values: ['Running'], chosen: ['Running'] }]}
          onToggle={() => undefined}
          onClear={() => undefined}
        />
      </KeyboardStack>,
    );
    funnel = screen.getByRole('button', { name: /^Filter by column/ });
    expect(funnel.className).toContain('border-accent');
    expect(funnel.className).not.toContain('border-line');
  });

  it('draws the button joined to the field: its left corners squared', () => {
    render(
      <KeyboardStack>
        <FilterMenu
          columns={[{ label: 'Status', values: ['Running'], chosen: [] }]}
          onToggle={() => undefined}
          onClear={() => undefined}
        />
      </KeyboardStack>,
    );
    expect(screen.getByRole('button', { name: /^Filter by column/ }).className).toContain('rounded-l-none');
  });

  it('shows the values at once when there is a single filterable column', () => {
    render(
      <KeyboardStack>
        <FilterMenu
          columns={[{ label: 'Status', values: ['Running', 'Pending'], chosen: [] }]}
          onToggle={() => undefined}
          onClear={() => undefined}
        />
      </KeyboardStack>,
    );
    act(() => {
      fireEvent.click(screen.getByRole('button', { name: /^Filter by column/ }));
    });
    expect(screen.getByRole('button', { name: 'Running' })).not.toBeNull();
    expect(screen.getByRole('button', { name: 'Pending' })).not.toBeNull();
  });

  // With nothing to choose between, the column's name is a title and not a
  // disclosure - a chevron and `aria-expanded` on a button that does nothing
  // when clicked would lie.
  it('draws a single filterable column’s name as a plain title, not a disclosure', () => {
    render(
      <KeyboardStack>
        <FilterMenu
          columns={[{ label: 'Status', values: ['Running', 'Pending'], chosen: [] }]}
          onToggle={() => undefined}
          onClear={() => undefined}
        />
      </KeyboardStack>,
    );
    act(() => {
      fireEvent.click(screen.getByRole('button', { name: /^Filter by column/ }));
    });
    expect(screen.getByRole('button', { name: 'Running' })).not.toBeNull();
    expect(screen.getByRole('button', { name: 'Pending' })).not.toBeNull();
    const title = screen.getByRole('heading', { name: 'Status' });
    expect(screen.queryByRole('button', { name: 'Status' })).toBeNull();
    expect(title.closest('button')).toBeNull();
    expect(title.querySelector('button')).toBeNull();
    const panel = screen.getByRole('group', { name: 'Filter by column' });
    expect(panel.querySelector('[aria-expanded]')).toBeNull();
    expect(title.querySelector('svg')).toBeNull();
  });

  // The count beside a column's name is drawn as a bare figure; read aloud
  // against the name it ran into "Status1", so a reader is given the words.
  it('counts the chosen values in a single column’s title, in words a reader can say', () => {
    render(
      <KeyboardStack>
        <FilterMenu
          columns={[{ label: 'Status', values: ['Running', 'Pending'], chosen: ['Running'] }]}
          onToggle={() => undefined}
          onClear={() => undefined}
        />
      </KeyboardStack>,
    );
    act(() => {
      fireEvent.click(screen.getByRole('button', { name: /^Filter by column/ }));
    });
    const title = screen.getByRole('heading', { name: 'Status, 1 chosen' });
    expect(title.querySelector('[aria-hidden="true"]')?.textContent).toBe('1');
  });

  it('counts the chosen values on a column’s disclosure, in words a reader can say', () => {
    render(
      <KeyboardStack>
        <FilterMenu
          columns={[
            { label: 'Status', values: ['Running', 'Pending'], chosen: ['Running', 'Pending'] },
            { label: 'Node', values: ['kubeworker1'], chosen: [] },
          ]}
          onToggle={() => undefined}
          onClear={() => undefined}
        />
      </KeyboardStack>,
    );
    act(() => {
      fireEvent.click(screen.getByRole('button', { name: /^Filter by column/ }));
    });
    const status = screen.getByRole('button', { name: 'Status, 2 chosen' });
    expect(status.querySelector('[aria-hidden="true"]')?.textContent).toBe('2');
    expect(screen.getByRole('button', { name: 'Node' })).not.toBeNull();
  });
});

describe('the funnel in the segmented control', () => {
  it('shares its borders with both neighbors and rounds neither side', () => {
    render(
      <KeyboardStack>
        <FilterMenu
          columns={[{ label: 'Node', values: ['a'], chosen: [] }]}
          onToggle={() => undefined}
          onClear={() => undefined}
        />
      </KeyboardStack>,
    );
    const funnel = screen.getByRole('button', { name: /^Filter by column/ });
    expect(funnel.classList.contains('rounded-l-none')).toBe(true);
    expect(funnel.classList.contains('rounded-r-none')).toBe(true);
    expect(funnel.parentElement?.classList.contains('-ml-px')).toBe(true);
  });
});
