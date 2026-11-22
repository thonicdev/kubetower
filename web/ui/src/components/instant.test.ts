import { afterEach, describe, expect, it } from 'vitest';

import { formatClock, formatDay, formatInstant, uiLocale } from './instant';

/**
 * One date format, following the UI's language. Each instant is
 * built from local components so that the reader's zone, which every caller
 * keeps, does not move the figures under the assertion.
 */
const AT = new Date(2026, 9, 10, 14, 3, 5);

afterEach(() => {
  document.documentElement.lang = '';
});

describe('formatInstant', () => {
  it('writes a month name and a 24-hour clock, whatever the machine is set to', () => {
    // `09/10/2026` was the 9th of October to one reader and the 10th of
    // September to the next; a 12-hour clock sorts `2:03 PM` before `9:00 AM`.
    expect(formatInstant(AT)).toBe('Oct 10, 14:03');
    expect(formatInstant(AT.toISOString(), { seconds: true })).toBe('Oct 10, 14:03:05');
    expect(formatInstant(AT, { year: true })).toBe('Oct 10, 2026, 14:03');
  });

  it('follows the document language, not the browser locale', () => {
    expect(uiLocale()).toBe('en');
    document.documentElement.lang = 'en-GB';
    expect(uiLocale()).toBe('en-GB');
    expect(formatInstant(AT)).toBe('10 Oct, 14:03');
  });

  it('hands back what does not parse, rather than "Invalid Date"', () => {
    expect(formatInstant('not a time')).toBe('not a time');
    expect(formatClock('')).toBe('');
  });
});

describe('formatClock and formatDay', () => {
  it('write the time of day and the day, in the same shape', () => {
    expect(formatClock(AT)).toBe('14:03:05');
    expect(formatDay(AT)).toBe('Oct 10, 2026');
  });
});
