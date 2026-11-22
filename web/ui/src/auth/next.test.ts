import { describe, expect, it } from 'vitest';

import { safeNext, signInPath } from './next';

describe('safeNext', () => {
  it('keeps a path on this origin', () => {
    expect(safeNext('/c/prod')).toBe('/c/prod');
    expect(safeNext('/c/prod?tab=events#top')).toBe('/c/prod?tab=events#top');
  });

  // An unchecked `next` turns this console's own sign-in page into a credible
  // hop to somewhere else, which is the one thing a sign-in page must not be.
  it.each([
    'https://evil.example/',
    '//evil.example/',
    'http://evil.example',
    'javascript:alert(1)',
    '/\\evil.example',
    'c/prod',
    '',
  ])('refuses %s', (raw) => {
    expect(safeNext(raw)).toBe('/');
  });

  it('refuses nothing at all', () => {
    expect(safeNext(null)).toBe('/');
    expect(safeNext(undefined)).toBe('/');
  });

  it('refuses the sign-in page itself, which would be a loop', () => {
    expect(safeNext('/login')).toBe('/');
    expect(safeNext('/login?next=%2F')).toBe('/');
    expect(safeNext('/auth/login')).toBe('/');
    expect(safeNext('/auth/login?next=%2F')).toBe('/');
  });
});

describe('signInPath', () => {
  it('carries where the operator was', () => {
    expect(signInPath({ pathname: '/c/prod', search: '?tab=events', hash: '' })).toBe(
      '/auth/login?next=%2Fc%2Fprod%3Ftab%3Devents',
    );
  });

  it('carries nothing when there is nothing worth carrying', () => {
    expect(signInPath({ pathname: '/', search: '', hash: '' })).toBe('/auth/login');
    expect(signInPath({ pathname: '/login', search: '', hash: '' })).toBe('/auth/login');
  });
});
