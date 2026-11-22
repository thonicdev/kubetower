import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { FilterBox } from './FilterBox';
import { SettlingMark } from './SettlingMark';

/**
 * The marker every settling search box shares.
 *
 * What is pinned is the one property that is not visible in a screenshot of
 * either state alone: the room the word takes is the same whether or not the
 * word is showing, so the box beside it does not move when it appears.
 */
describe('the settling marker', () => {
  function parts(container: HTMLElement): { room: string; said: string } {
    const room = container.querySelector('[aria-hidden="true"]');
    const said = container.querySelector('[aria-live="polite"]');
    if (room === null || said === null) throw new Error('no marker');
    return { room: room.textContent ?? '', said: said.textContent ?? '' };
  }

  it('holds its room while nothing is settling, and says nothing', () => {
    const { container } = render(<SettlingMark settling={false} />);
    expect(parts(container)).toEqual({ room: 'filtering…', said: '' });
  });

  it('says it is filtering in the same room', () => {
    const { container } = render(<SettlingMark settling />);
    expect(parts(container)).toEqual({ room: 'filtering…', said: 'filtering…' });
  });

  it('is the marker the list’s filter box draws', () => {
    const { container, rerender } = render(
      <FilterBox name="filter" label="Filter" value="" onChange={() => undefined} />,
    );
    expect(parts(container)).toEqual({ room: 'filtering…', said: '' });
    rerender(<FilterBox name="filter" label="Filter" value="ngin" onChange={() => undefined} settling />);
    expect(parts(container)).toEqual({ room: 'filtering…', said: 'filtering…' });
  });

  it('is drawn before the field, so no room sits between the field and the control joined to it', () => {
    const { container } = render(
      <FilterBox name="filter" label="Filter" value="" onChange={() => undefined}>
        <button type="button">funnel</button>
      </FilterBox>,
    );
    const mark = container.querySelector('[data-settling]');
    const input = container.querySelector('input');
    const funnel = container.querySelector('button');
    if (mark === null || input === null || funnel === null) throw new Error('no box');
    expect(mark.compareDocumentPosition(input) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    // The field and the funnel are siblings with nothing between them.
    expect(input.nextElementSibling).toBe(funnel);
    expect(container.querySelector('[data-settling]')?.nextElementSibling?.tagName).toBe('LABEL');
  });
});
