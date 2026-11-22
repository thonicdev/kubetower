import { describe, expect, it } from 'vitest';

import type { ExtensionInfo } from '../../api';
import { lockState } from './extraParts';

function row(over: Partial<ExtensionInfo>): ExtensionInfo {
  return { key: 'demo', label: 'Demo', enabled: false, pinned: false, writable: false, settings: [], ...over };
}

describe('lockState', () => {
  it('a writable row is not drawn disabled and carries no reason', () => {
    expect(lockState(row({ writable: true }))).toEqual({ shownDisabled: false, reason: undefined });
  });

  it('a row locked for want of `use` offers no button, so it is not drawn disabled', () => {
    const state = lockState(row({ locked: 'use', reason: 'You do not hold use on this extension.' }));
    expect(state.shownDisabled).toBe(false);
    expect(state.reason).toBe('You do not hold use on this extension.');
  });

  it('a row pinned by the deployment is drawn disabled, with its reason', () => {
    expect(lockState(row({ pinned: true, locked: 'pinned', reason: 'Pinned by KT_EXTRAS.' }))).toEqual({
      shownDisabled: true,
      reason: 'Pinned by KT_EXTRAS.',
    });
  });

  it('an empty reason is no reason', () => {
    expect(lockState(row({ locked: 'values', reason: '' }))).toEqual({ shownDisabled: true, reason: undefined });
  });
});
