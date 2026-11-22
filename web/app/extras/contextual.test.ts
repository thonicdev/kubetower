import { describe, expect, it } from 'vitest';

import { extras } from './index';

// Which extras have a page in the menu and which are reached only from the
// panel of what they are about: the operator's decision.
describe('the extras the menu offers', () => {
  it('leaves out JVM Inspector, Pod Debugging and the Redis viewer, and only them', () => {
    const contextual = extras.filter((one) => one.nav.menu === false).map((one) => one.key).sort();
    expect(contextual).toEqual(['jvm', 'pending', 'redis']);
  });

  it('still gives each contextual extra a tab or a tile to be reached from', () => {
    for (const one of extras.filter((x) => x.nav.menu === false)) {
      expect((one.tabs?.length ?? 0) + (one.shortcuts?.length ?? 0)).toBeGreaterThan(0);
    }
  });
});
