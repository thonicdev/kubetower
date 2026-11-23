import { describe, expect, it } from 'vitest';

import type { NavGroup } from '../../ui/src/api';
import { withoutContextual } from '../../ui/src/shell/contextualExtras';
import { extras } from './index';

// Which extras have a page in the menu and which are reached only from the
// panel of what they are about: the operator's decision.
describe('the extras the menu offers', () => {
  it('leaves out JVM Inspector and Pod Debugging, and only them', () => {
    const contextual = extras.filter((one) => one.nav.menu === false).map((one) => one.key).sort();
    expect(contextual).toEqual(['jvm', 'pending']);
  });

  it('draws the Redis viewer in the Extra group once the server lists it', () => {
    const groups: NavGroup[] = [
      {
        key: 'extra',
        label: 'Extra',
        items: [
          { text: 'Redis viewer', href: '/c/demo/x/redis' },
          { text: 'JVM Inspector', href: '/c/demo/x/jvm' },
        ],
      },
    ];
    const drawn = withoutContextual(groups, extras).flatMap((group) => group.items.map((item) => item.href));
    expect(drawn).toEqual(['/c/demo/x/redis']);
  });

  it('still gives each contextual extra a tab or a tile to be reached from', () => {
    for (const one of extras.filter((x) => x.nav.menu === false)) {
      expect((one.tabs?.length ?? 0) + (one.shortcuts?.length ?? 0)).toBeGreaterThan(0);
    }
  });
});
