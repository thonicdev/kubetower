import { act, render } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { announce, currentAnnouncement, resetAnnouncements } from './announce';
import { LiveRegion } from './LiveRegion';

afterEach(() => {
  resetAnnouncements();
});

function region(): HTMLElement {
  const element = document.querySelector('[role="status"]');
  if (!(element instanceof HTMLElement)) throw new Error('no live region');
  return element;
}

describe('the console says out loud what an action did', () => {
  it('starts silent, so nothing is announced on load', () => {
    render(<LiveRegion />);
    expect(region().textContent).toBe('');
  });

  it('renders what was announced', () => {
    render(<LiveRegion />);
    act(() => {
      announce('prod taken off the rail.');
    });
    expect(region().textContent).toBe('prod taken off the rail.');
  });

  it('replaces the text node when the same words are said twice', () => {
    render(<LiveRegion />);
    act(() => {
      announce('prod moved below staging.');
    });
    const first = region().firstElementChild;

    act(() => {
      announce('prod moved below staging.');
    });

    // Two identical messages in a row are otherwise one change and are
    // announced once, and 'moved down' twice is exactly what walking a tile
    // down a hotbar produces. In React an identical string is not even a DOM
    // write - so the node is
    // keyed on a sequence number and replaced instead.
    expect(region().firstElementChild).not.toBe(first);
    expect(region().textContent).toBe('prod moved below staging.');
  });

  it('ignores an empty announcement', () => {
    announce('');
    expect(currentAnnouncement().seq).toBe(0);
  });

  it('survives an announcement made while nothing is listening', () => {
    // The region is mounted in the Shell, and the sign-in route is outside it.
    announce('something happened');
    render(<LiveRegion />);
    expect(region().textContent).toBe('something happened');
  });

  it('stops updating once unmounted, and does not throw', () => {
    const { unmount } = render(<LiveRegion />);
    unmount();
    expect(() => {
      announce('after the fact');
    }).not.toThrow();
  });
});
