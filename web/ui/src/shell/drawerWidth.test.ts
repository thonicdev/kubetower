import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  chooseDrawerWidth,
  chosenDrawerWidth,
  clampDrawerWidth,
  defaultDrawerWidth,
  DRAWER_FLOOR,
  drawerBounds,
  isDefaultWidth,
  LIST_MIN,
  resetDrawerWidth,
  subscribeToDrawerWidth,
} from './drawerWidth';

const KEY = 'kubetower.drawer.width';

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
  resetDrawerWidth();
});

describe('the default is the width the drawer has always had', () => {
  it('is 560px on a window wide enough, and 46% of a narrower one', () => {
    // `--drawer-w: min(560px, 46vw)` in `theme.css`, the same two numbers.
    expect(defaultDrawerWidth(1440)).toBe(560);
    expect(defaultDrawerWidth(1920)).toBe(560);
    expect(defaultDrawerWidth(1024)).toBeCloseTo(471.04, 2);
  });
});

describe('the bounds', () => {
  it('go down to what the content needs, and leave the list its 1024px minimum', () => {
    // At 1440 with the rail and the kinds column, the drawer and the list share
    // 1192px: 1192 - 305 is as wide as the drawer may go. The narrowest is what
    // the drawer's content measured, rounded up so nothing is cut by a fraction.
    expect(drawerBounds(1440, 1192, 470.2)).toStrictEqual({ min: 471, max: 1192 - LIST_MIN });
  });

  it('never go under the measured floor, whatever this drawer needs', () => {
    expect(DRAWER_FLOOR).toBe(444);
    expect(drawerBounds(1440, 1192, 268).min).toBe(DRAWER_FLOOR);
  });

  it('keep the default as the floor where nothing was measured', () => {
    expect(drawerBounds(1440, 1192, null).min).toBe(560);
  });

  it('never put the floor above the default - a narrow window draws as today', () => {
    // At 900 the default is 414, under what a pod's controls need.
    expect(drawerBounds(900, 652, 444).min).toBeCloseTo(414, 0);
  });

  it('have no room to widen at 1024 with the kinds column shown - which is today', () => {
    // 1024 - 56 - 192 = 776 to share; the default drawer leaves the list 305,
    // which is where `LIST_MIN` comes from. Nothing is left to widen into.
    const bounds = drawerBounds(1024, 776, 444);
    expect(bounds.max).toBeCloseTo(defaultDrawerWidth(1024), 6);
    expect(bounds.min).toBe(444);
  });

  it('never put the maximum under the default on a window narrower than that', () => {
    const bounds = drawerBounds(900, 652, null);
    expect(bounds.min).toBeCloseTo(414, 0);
    expect(bounds.max).toBe(bounds.min);
  });

  it('clamp a width into them, and round it to a pixel', () => {
    const bounds = { min: 444, max: 887 };
    expect(clampDrawerWidth(400, bounds)).toBe(444);
    expect(clampDrawerWidth(700.4, bounds)).toBe(700);
    expect(clampDrawerWidth(5000, bounds)).toBe(887);
  });

  it('call a width the default within half a pixel of the window\'s share', () => {
    expect(isDefaultWidth(560, 1440)).toBe(true);
    expect(isDefaultWidth(471, 1024)).toBe(true);
    expect(isDefaultWidth(544, 1440)).toBe(false);
    expect(isDefaultWidth(576, 1440)).toBe(false);
  });
});

describe('the chosen width, remembered per browser', () => {
  it('is null - the default - when nothing was ever chosen', () => {
    expect(chosenDrawerWidth()).toBeNull();
  });

  it('is written to storage and read back by the next window', () => {
    chooseDrawerWidth(720);
    expect(localStorage.getItem(KEY)).toBe('720');
    expect(chosenDrawerWidth()).toBe(720);

    // A new window - a reload - reads it from storage.
    resetDrawerWidth();
    expect(chosenDrawerWidth()).toBe(720);
  });

  it('is rounded to a whole pixel before it is stored', () => {
    chooseDrawerWidth(701.6);
    expect(localStorage.getItem(KEY)).toBe('702');
  });

  it('is forgotten by a reset, which removes the key', () => {
    chooseDrawerWidth(720);
    chooseDrawerWidth(null);
    expect(localStorage.getItem(KEY)).toBeNull();
    expect(chosenDrawerWidth()).toBeNull();
  });

  it('answers null for a stored value that is not a sane width', () => {
    for (const raw of ['wide', '-40', '0', '"720"', 'null', '{}', '99999', 'NaN']) {
      localStorage.setItem(KEY, raw);
      resetDrawerWidth();
      expect(chosenDrawerWidth(), raw).toBeNull();
    }
  });

  it('tells this window of its own change', () => {
    const heard = vi.fn();
    const stop = subscribeToDrawerWidth(heard);
    chooseDrawerWidth(700);
    expect(heard).toHaveBeenCalledTimes(1);
    stop();
    chooseDrawerWidth(720);
    expect(heard).toHaveBeenCalledTimes(1);
  });

  it('does not follow another window, as the menu does not', () => {
    expect(chosenDrawerWidth()).toBeNull();
    const heard = vi.fn();
    subscribeToDrawerWidth(heard);

    // Another window chose a width: storage holds it, and this window is
    // neither told nor changed. Its own next start reads it.
    localStorage.setItem(KEY, '800');
    window.dispatchEvent(new StorageEvent('storage', { key: KEY, newValue: '800' }));
    expect(heard).not.toHaveBeenCalled();
    expect(chosenDrawerWidth()).toBeNull();
  });
});

describe('storage that is not there costs the memory, never the drawer', () => {
  it('reads as the default when storage throws', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('SecurityError');
    });
    expect(chosenDrawerWidth()).toBeNull();
  });

  it('keeps the width for this window when a write is refused', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('QuotaExceededError');
    });
    expect(() => {
      chooseDrawerWidth(700);
    }).not.toThrow();
    expect(chosenDrawerWidth()).toBe(700);
  });

  it('forgets for this window when a removal is refused', () => {
    chooseDrawerWidth(700);
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => {
      throw new Error('SecurityError');
    });
    expect(() => {
      chooseDrawerWidth(null);
    }).not.toThrow();
    expect(chosenDrawerWidth()).toBeNull();
  });
});
