import { afterEach, describe, expect, it } from 'vitest';

import {
  kindsMemory,
  readChoice,
  resetKindsMemory,
  setKindsMemory,
  subscribeToKindsMemory,
  withFavourite,
  withShowUnused,
} from './kindsMemory';

afterEach(() => {
  resetKindsMemory();
});

describe('what this browser keeps about the kinds column', () => {
  it('answers unused kinds hidden and no favorite when nothing is kept', () => {
    expect(kindsMemory()).toEqual({ showUnused: false, favourites: {} });
  });

  it('reads back what it wrote, across a fresh import', () => {
    setKindsMemory({ showUnused: true, favourites: { prod: ['pods', 'jobs'] } });
    expect(localStorage.getItem('kubetower.kinds')).toBe(
      JSON.stringify({ showUnused: true, favourites: { prod: ['pods', 'jobs'] } }),
    );

    const kept = localStorage.getItem('kubetower.kinds');
    resetKindsMemory();
    localStorage.setItem('kubetower.kinds', kept ?? '');
    expect(kindsMemory()).toEqual({ showUnused: true, favourites: { prod: ['pods', 'jobs'] } });
  });

  it('tells its listeners of a write', () => {
    let heard = 0;
    const stop = subscribeToKindsMemory(() => {
      heard += 1;
    });
    setKindsMemory({ showUnused: true, favourites: {} });
    stop();
    setKindsMemory({ showUnused: false, favourites: {} });
    expect(heard).toBe(1);
  });

  it('answers the default for what is not JSON', () => {
    localStorage.setItem('kubetower.kinds', '{not json');
    expect(kindsMemory()).toEqual({ showUnused: false, favourites: {} });
  });

  it('drops every member of the wrong type, and keeps the rest', () => {
    expect(readChoice(null)).toEqual({ showUnused: false, favourites: {} });
    expect(readChoice([true])).toEqual({ showUnused: false, favourites: {} });
    expect(readChoice({ showUnused: 'yes', favourites: [] })).toEqual({ showUnused: false, favourites: {} });
    expect(
      readChoice({
        showUnused: true,
        favourites: { prod: ['pods', 7, '', 'pods', 'jobs'], staging: 'pods', dev: [] },
      }),
    ).toEqual({ showUnused: true, favourites: { prod: ['pods', 'jobs'] } });
  });
});

describe('changing the choice', () => {
  it('sets showUnused and keeps the favorites', () => {
    const before = { showUnused: false, favourites: { prod: ['pods'] } };
    expect(withShowUnused(before, true)).toEqual({ showUnused: true, favourites: { prod: ['pods'] } });
  });

  it('marks a favorite per cluster, in the order marked', () => {
    let choice = withFavourite({ showUnused: false, favourites: {} }, 'prod', 'pods', true);
    choice = withFavourite(choice, 'prod', 'jobs', true);
    choice = withFavourite(choice, 'staging', 'pods', true);
    expect(choice.favourites).toEqual({ prod: ['pods', 'jobs'], staging: ['pods'] });
    // Marking again changes nothing.
    expect(withFavourite(choice, 'prod', 'pods', true)).toBe(choice);
  });

  it('unmarks, and drops a cluster left with none', () => {
    const choice = { showUnused: true, favourites: { prod: ['pods', 'jobs'], staging: ['pods'] } };
    expect(withFavourite(choice, 'prod', 'pods', false).favourites).toEqual({ prod: ['jobs'], staging: ['pods'] });
    expect(withFavourite(choice, 'staging', 'pods', false).favourites).toEqual({ prod: ['pods', 'jobs'] });
  });
});
