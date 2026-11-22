import { afterEach, describe, expect, it, vi } from 'vitest';

import type { NavGroup } from '../api';
import { forgetOtherClusters, rememberGroup, rememberedGroup, resetNavMemory } from './navMemory';

const KEY = 'kubetower.nav.prod.custom-resources';

const group: NavGroup = {
  key: 'custom-resources',
  label: 'Custom Resources',
  deferred: false,
  items: [],
  groups: [
    {
      key: 'cert-manager.io',
      label: 'cert-manager.io',
      deferred: false,
      items: [{ text: 'Certificates', href: '/c/prod/certificates.cert-manager.io' }],
    },
  ],
};

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
  resetNavMemory();
});

describe('what was fetched for a deferred group is written down, per cluster and group', () => {
  it('answers nothing where nothing was ever fetched', () => {
    expect(rememberedGroup('prod', 'custom-resources')).toBeUndefined();
  });

  it('reads back what was written, with when', () => {
    rememberGroup('prod', 'custom-resources', group, 1_700_000_000_000);

    expect(rememberedGroup('prod', 'custom-resources')).toStrictEqual({
      at: 1_700_000_000_000,
      group,
    });
    expect(JSON.parse(localStorage.getItem(KEY) ?? 'null')).toStrictEqual({
      at: 1_700_000_000_000,
      group,
    });
  });

  it('keeps one cluster apart from another', () => {
    // The key carries the cluster: the custom kinds installed on prod are not
    // the ones installed on staging, and a snapshot that crossed over would draw
    // a menu entry for a kind the cluster does not serve.
    rememberGroup('prod', 'custom-resources', group, 1);
    expect(rememberedGroup('staging', 'custom-resources')).toBeUndefined();
  });

  it('keeps the note, which is the sentence that tells two empty answers apart', () => {
    const none: NavGroup = {
      key: 'custom-resources',
      label: 'Custom Resources',
      deferred: false,
      items: [],
      note: 'This cluster serves no custom resource kinds.',
    };
    rememberGroup('prod', 'custom-resources', none, 1);
    expect(rememberedGroup('prod', 'custom-resources')?.group.note).toBe(
      'This cluster serves no custom resource kinds.',
    );
  });

  it('keeps the access note across a reload, which is what says which kinds are left out', () => {
    // Read back from storage, not from the module's cache: after F5
    // the heading is drawn from the snapshot, and a reader that rebuilt the
    // group field by field without this one would draw it quietly short.
    const short: NavGroup = {
      ...group,
      accessNote:
        '3 kinds this console may not list are not shown: the identity it reads with has to be granted the read on them.',
    };
    rememberGroup('prod', 'custom-resources', short, 1);
    resetNavMemory();
    expect(rememberedGroup('prod', 'custom-resources')?.group).toStrictEqual(short);
  });

  it('does not write the same moment twice', () => {
    rememberGroup('prod', 'custom-resources', group, 1);
    const write = vi.spyOn(Storage.prototype, 'setItem');
    // What a query seeded from the snapshot hands back on its first render: the
    // snapshot itself, stamped with the snapshot's own time. Not a new fact.
    rememberGroup('prod', 'custom-resources', group, 1);
    expect(write).not.toHaveBeenCalled();
  });
});

describe('a snapshot that cannot be trusted is no snapshot', () => {
  it.each([
    ['not JSON', 'certificates'],
    ['a list', '[]'],
    ['no time', JSON.stringify({ group })],
    ['no group', JSON.stringify({ at: 1 })],
    ['a group with no key', JSON.stringify({ at: 1, group: { label: 'x', items: [] } })],
    ['items that are not a list', JSON.stringify({ at: 1, group: { key: 'k', label: 'x', items: 'no' } })],
  ])('ignores %s', (_what, raw) => {
    // Written by an older build, or edited by hand. Treated as nothing rather
    // than as a crash on the first render of the menu - the same bargain
    // `viewMemory.ts` strikes.
    localStorage.setItem(KEY, raw);
    expect(rememberedGroup('prod', 'custom-resources')).toBeUndefined();
  });

  it('drops an item with no text and keeps the rest', () => {
    localStorage.setItem(
      KEY,
      JSON.stringify({
        at: 1,
        group: { key: 'k', label: 'x', items: [{ href: '/c/prod/a' }, { text: 'B', href: '/c/prod/b' }] },
      }),
    );
    expect(rememberedGroup('prod', 'custom-resources')?.group.items).toStrictEqual([
      { text: 'B', href: '/c/prod/b' },
    ]);
  });
});

describe('storage that throws costs the memory and never the menu', () => {
  it('reads as nothing when getItem throws', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('access to storage is denied by this profile');
    });
    expect(rememberedGroup('prod', 'custom-resources')).toBeUndefined();
  });

  it('keeps the answer for the life of the tab when setItem throws', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('QuotaExceededError', 'QuotaExceededError');
    });
    expect(() => {
      rememberGroup('prod', 'custom-resources', group, 1);
    }).not.toThrow();
    expect(rememberedGroup('prod', 'custom-resources')?.at).toBe(1);
  });
});

describe('a cluster that has left the list', () => {
  it('is forgotten, in storage and in memory, and the others are kept', () => {
    rememberGroup('prod', 'custom-resources', group, 1);
    rememberGroup('gone', 'custom-resources', group, 2);
    // A dot in a context name is ordinary (`prod.eu-west-1`), and the key is
    // `<prefix>.<cluster>.<group>`, so a cluster is matched as a whole prefix.
    rememberGroup('prod.eu', 'custom-resources', group, 3);
    localStorage.setItem('kubetower.view.other', 'not ours');

    forgetOtherClusters(['prod', 'prod.eu']);

    expect(localStorage.getItem('kubetower.nav.gone.custom-resources')).toBeNull();
    expect(rememberedGroup('gone', 'custom-resources')).toBeUndefined();
    expect(rememberedGroup('prod', 'custom-resources')?.at).toBe(1);
    expect(rememberedGroup('prod.eu', 'custom-resources')?.at).toBe(3);
    expect(localStorage.getItem('kubetower.view.other')).toBe('not ours');
  });

  it('costs nothing when storage will not be read', () => {
    vi.spyOn(Storage.prototype, 'key').mockImplementation(() => {
      throw new Error('blocked');
    });
    expect(() => forgetOtherClusters(['prod'])).not.toThrow();
  });
});
