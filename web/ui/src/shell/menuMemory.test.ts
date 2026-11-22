import { afterEach, describe, expect, it, vi } from 'vitest';

import type { NavGroup } from '../api';
import {
  deferredGroupHoldsPath,
  groupHoldsPath,
  isOverviewHref,
  groupIsOpen,
  menuIsHidden,
  openGroups,
  resetMenuMode,
  resetOpenGroups,
  setGroupOpen,
  setMenuHidden,
  subscribeToMenuMode,
  subscribeToOpenGroups,
  visitedGroup,
  visitGroup,
} from './menuMemory';
import type * as MenuMemory from './menuMemory';

const KEY = 'kubetower.menu.open';

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
  resetOpenGroups();
});

const everyKey = ['cluster', 'workloads', 'network'];

describe('nothing arranged and everything arranged are different answers', () => {
  it('answers null when this browser has never arranged the menu', () => {
    // An empty answer and a missing one, at the smallest possible scale.
    // `null` draws only the group holding the page open - and an empty
    // arrangement, below, is the person having shut that one too.
    expect(openGroups()).toBeNull();
  });

  it('answers an empty list when a person has closed every group', () => {
    localStorage.setItem(KEY, '[]');
    expect(openGroups()).toStrictEqual([]);
  });

  it('answers null for a value that is not a list of names', () => {
    localStorage.setItem(KEY, '{"workloads":true}');
    expect(openGroups()).toBeNull();
  });

  it('answers null for a value that is not JSON at all', () => {
    localStorage.setItem(KEY, 'workloads,network');
    expect(openGroups()).toBeNull();
  });
});

describe('the first gesture against an unarranged menu records what was on screen', () => {
  it('closing one group leaves the others open', () => {
    setGroupOpen('workloads', false, everyKey);

    // Not `["workloads"]` inverted into "only workloads was ever open": the
    // other two are on screen and have to stay written down, or the next reload
    // shuts them for a gesture that was about one group.
    expect(openGroups()).toStrictEqual(['cluster', 'network']);
    expect(JSON.parse(localStorage.getItem(KEY) ?? 'null')).toStrictEqual(['cluster', 'network']);
  });

  it('opening a group that is already open writes nothing', () => {
    setGroupOpen('workloads', true, everyKey);
    expect(localStorage.getItem(KEY)).toBeNull();
    expect(openGroups()).toBeNull();
  });
});

/**
 * A visit is not an arrangement.
 *
 * Committing the group an arrival opened would leave *Custom Resources* open -
 * and its CustomResourceDefinition listing paid - on every page after one
 * custom resource page, in every later session. The arrival holds the group
 * open for the page on screen and writes nothing; only the person's own
 * gesture writes.
 */
describe('the group holding the current page is open for the visit, and not written down', () => {
  const network: NavGroup = { key: 'network', label: 'Network', deferred: false, items: [] };

  it('draws it open against an arrangement that does not hold it, and leaves storage alone', () => {
    localStorage.setItem(KEY, '["cluster"]');
    const write = vi.spyOn(Storage.prototype, 'setItem');
    visitGroup('network');
    expect(groupIsOpen(network, openGroups(), visitedGroup())).toBe(true);
    expect(openGroups()).toStrictEqual(['cluster']);
    expect(write).not.toHaveBeenCalled();
  });

  it('shuts it again when the visit moves on', () => {
    localStorage.setItem(KEY, '["cluster"]');
    visitGroup('network');
    visitGroup(undefined);
    expect(groupIsOpen(network, openGroups(), visitedGroup())).toBe(false);
    expect(localStorage.getItem(KEY)).toBe('["cluster"]');
  });

  it('ends the visit when a person closes the group they are standing in', () => {
    // The promise the arrival rule makes: a group closed while
    // standing in it stays closed. The visit is what was holding it open, so the
    // gesture has to end it - and has nothing to write, since nothing was.
    localStorage.setItem(KEY, '["cluster"]');
    visitGroup('network');
    const write = vi.spyOn(Storage.prototype, 'setItem');
    setGroupOpen('network', false, ['cluster']);
    expect(visitedGroup()).toBeUndefined();
    expect(groupIsOpen(network, openGroups(), visitedGroup())).toBe(false);
    expect(write).not.toHaveBeenCalled();
  });

  it('notifies once per change of visit, and not for the same one again', () => {
    const heard = vi.fn();
    subscribeToOpenGroups(heard);
    visitGroup('network');
    visitGroup('network');
    expect(heard).toHaveBeenCalledTimes(1);
  });
});

describe('storage that throws costs the memory and never the menu', () => {
  it('reads as no memory when getItem throws', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      // What a browser throws when site data is blocked. The catch is untyped, so
      // a plain Error exercises the same branch.
      throw new Error('access to storage is denied by this profile');
    });

    // A private window, blocked site data, or a locked-down profile. `null` is
    // the safe direction: every group open.
    expect(openGroups()).toBeNull();
  });

  it('keeps the arrangement for the life of the tab when setItem throws', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('QuotaExceededError', 'QuotaExceededError');
    });

    expect(() => {
      setGroupOpen('workloads', false, everyKey);
    }).not.toThrow();

    // In memory it took effect, which is what keeps the menu behaving while the
    // tab is open. Only the persistence is lost.
    expect(openGroups()).toStrictEqual(['cluster', 'network']);

    // And the next gesture keeps it. Storage that refused the first write holds
    // nothing of it, so a gesture that took its base from storage would hand
    // back an unarranged menu and reopen
    // Workloads. The base is this window's own arrangement.
    setGroupOpen('network', false, everyKey);
    expect(openGroups()).toStrictEqual(['cluster']);
  });

  it('keeps what this window arranged when storage stops answering between gestures', () => {
    setGroupOpen('workloads', false, everyKey);
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('access to storage is denied by this profile');
    });
    setGroupOpen('network', false, everyKey);
    expect(openGroups()).toStrictEqual(['cluster']);
  });
});

/**
 * Each window keeps its own menu: opening Network in one tab does not open it
 * in another. And no window writes another window's arrangement over the
 * stored one.
 *
 * A window here is a **fresh import of this module** - its own snapshot, its
 * own listeners - over the one `localStorage` jsdom shares between them, which
 * is what two windows of one origin are. A browser sends `storage` to every
 * window but the one that wrote; `fromElsewhere` is that delivery, made by hand
 * because jsdom does not send it across module instances that share a window.
 */
describe('each window keeps its own menu', () => {
  type MenuWindow = typeof MenuMemory;
  const windows: MenuWindow[] = [];

  async function openWindow(): Promise<MenuWindow> {
    vi.resetModules();
    const opened = await import('./menuMemory');
    windows.push(opened);
    // A window that is open has drawn its menu, which is its one read.
    opened.openGroups();
    return opened;
  }

  function fromElsewhere(key: string | null = KEY): void {
    window.dispatchEvent(
      new StorageEvent('storage', {
        key,
        newValue: key === null ? null : localStorage.getItem(key),
        storageArea: localStorage,
      }),
    );
  }

  afterEach(() => {
    for (const opened of windows.splice(0)) opened.resetOpenGroups();
  });

  it('opening Network in one window leaves the other window’s menu as it was, with nothing listening', async () => {
    localStorage.setItem(KEY, '["cluster"]');
    const listen = vi.spyOn(window, 'addEventListener');
    const first = await openWindow();
    const second = await openWindow();
    const heard = vi.fn();
    second.subscribeToOpenGroups(heard);
    first.subscribeToOpenGroups(() => undefined);
    const before = second.openGroups();

    first.setGroupOpen('network', true, []);
    expect(first.openGroups()).toStrictEqual(['cluster', 'network']);
    // What the browser does next: tell every other window storage changed.
    fromElsewhere();

    // The same snapshot - not an equal one - so `useSyncExternalStore` has
    // nothing to redraw, and nobody asked it to.
    expect(second.openGroups()).toBe(before);
    expect(second.openGroups()).toStrictEqual(['cluster']);
    expect(heard).not.toHaveBeenCalled();
    // And not because the event was filtered: nothing subscribes to it at all.
    expect(listen.mock.calls.filter(([type]) => type === 'storage')).toStrictEqual([]);
  });

  it('starts a third window from the last arrangement any window saved', async () => {
    // If a new window should instead always start from the default, this is
    // the one test to change - `third.openGroups()` is then `null`.
    const first = await openWindow();
    const second = await openWindow();
    first.setGroupOpen('network', true, []);
    second.setGroupOpen('workloads', true, []);

    const third = await openWindow();
    expect(third.openGroups()).toStrictEqual(['workloads']);
  });

  it('writes the gesturing window’s own arrangement whole, never another window’s', async () => {
    // One window folds X, a second folds Y. The
    // stored arrangement is what the second has on screen - every key in it is
    // one that window draws open - and neither window's menu moves for the
    // other's gesture, before or after it writes.
    localStorage.setItem(KEY, '["cluster","workloads","network"]');
    const first = await openWindow();
    const second = await openWindow();

    first.setGroupOpen('workloads', false, []);
    second.setGroupOpen('network', false, []);

    expect(JSON.parse(localStorage.getItem(KEY) ?? 'null')).toStrictEqual([
      'cluster',
      'workloads',
    ]);
    expect(second.openGroups()).toStrictEqual(['cluster', 'workloads']);
    expect(first.openGroups()).toStrictEqual(['cluster', 'network']);
  });

  it('writes nothing from a window that only arrives, so the stored arrangement stays the last one saved', async () => {
    // A window that draws its menu and stands on a page has made no gesture, and the
    // arrangement another window saved is left exactly as it was.
    const first = await openWindow();
    first.setGroupOpen('network', true, []);
    const write = vi.spyOn(Storage.prototype, 'setItem');

    const second = await openWindow();
    second.subscribeToOpenGroups(() => undefined);
    second.visitGroup('workloads');
    second.visitGroup(undefined);

    expect(write).not.toHaveBeenCalled();
    expect(localStorage.getItem(KEY)).toBe('["network"]');
  });

  it('keeps this window’s menu when another clears the site data', async () => {
    // A menu must not redraw itself because of another window, whichever
    // gesture caused it.
    const first = await openWindow();
    first.subscribeToOpenGroups(() => undefined);
    first.setGroupOpen('workloads', true, []);
    localStorage.removeItem(KEY);
    fromElsewhere(null);
    expect(first.openGroups()).toStrictEqual(['workloads']);
  });
});

describe('a gesture that changes nothing keeps the snapshot it had', () => {
  it('hands `useSyncExternalStore` the same array, so the menu is not redrawn for nothing', () => {
    setGroupOpen('workloads', false, everyKey);
    const before = openGroups();
    const heard = vi.fn();
    subscribeToOpenGroups(heard);
    setGroupOpen('workloads', false, everyKey);
    expect(openGroups()).toBe(before);
    expect(heard).not.toHaveBeenCalled();
  });

  it('keeps it when storage was changed by another window in between', () => {
    // A gesture reads this window's own arrangement, not storage, so another
    // window's write does not reach the screen through a no-op gesture either.
    setGroupOpen('workloads', false, everyKey);
    const before = openGroups();
    localStorage.setItem(KEY, '["workloads"]');
    setGroupOpen('workloads', false, everyKey);
    expect(openGroups()).toBe(before);
  });
});

describe('which group holds the page', () => {
  const group = (items: string[]): NavGroup => ({
    key: 'workloads',
    label: 'Workloads',
    deferred: false,
    items: items.map((href) => ({ text: href, href })),
  });

  it('matches the kind exactly', () => {
    expect(groupHoldsPath(group(['/c/prod/pods']), '/c/prod/pods')).toBe(true);
  });

  it('matches a detail route under the kind', () => {
    expect(groupHoldsPath(group(['/c/prod/pods']), '/c/prod/pods/default/api-0')).toBe(true);
  });

  it('does not match a kind whose name merely starts the same', () => {
    // The `/` boundary, and it is the whole reason the check is not a bare
    // startsWith: `podsx` is a kind discovery could genuinely return.
    expect(groupHoldsPath(group(['/c/prod/pods']), '/c/prod/podsx')).toBe(false);
  });

  it('ignores an item with no usable href rather than matching everything', () => {
    expect(groupHoldsPath(group(['']), '/c/prod/pods')).toBe(false);
  });

  it('holds the dashboard through the Overview entry, and nothing under it', () => {
    // The `Cluster` group opens with Overview at `/c/prod`. Matched as a
    // prefix, that group would hold every page on the cluster - it is the first
    // group, so it would be the one opened on every arrival, and a custom
    // resource page would never be seen as inside the deferred heading.
    expect(groupHoldsPath(group(['/c/prod', '/c/prod/nodes']), '/c/prod')).toBe(true);
    expect(groupHoldsPath(group(['/c/prod']), '/c/prod/pods')).toBe(false);
    expect(groupHoldsPath(group(['/c/prod']), '/c/prod/widgets.example.com')).toBe(false);
    expect(isOverviewHref('/c/prod')).toBe(true);
    expect(isOverviewHref('/c/prod/')).toBe(true);
    expect(isOverviewHref('/c/prod/pods')).toBe(false);
    expect(isOverviewHref('')).toBe(false);
    expect(isOverviewHref(undefined)).toBe(false);
  });

  it('holds the page whichever way the cluster segment is spelled', () => {
    // An address may spell the cluster with `@` escaped or not. Compared
    // whole, the Pods list reached that way would be in no group, and so "a
    // custom resource page".
    const menuHref = '/c/kubernetes-admin@kubernetes/pods';
    expect(groupHoldsPath(group([menuHref]), '/c/kubernetes-admin%40kubernetes/pods')).toBe(true);
    expect(
      groupHoldsPath(group([menuHref]), '/c/kubernetes-admin%40kubernetes/pods/default/api-0'),
    ).toBe(true);
    // And `MakeDNSFriendly`'s `/` to `--`, which no decoding would undo.
    expect(
      groupHoldsPath(
        group(['/c/arn:aws:eks:eu-west-1:1:cluster--tools/pods']),
        '/c/arn%3Aaws%3Aeks%3Aeu-west-1%3A1%3Acluster%2Ftools/pods',
      ),
    ).toBe(true);
    // The Overview entry, the same way, and still that page alone.
    expect(
      groupHoldsPath(group(['/c/kubernetes-admin@kubernetes']), '/c/kubernetes-admin%40kubernetes'),
    ).toBe(true);
    expect(
      groupHoldsPath(group(['/c/kubernetes-admin@kubernetes']), '/c/kubernetes-admin%40kubernetes/pods'),
    ).toBe(false);
    // Off the cluster, nothing is held, however the segment would be spelled.
    expect(groupHoldsPath(group([menuHref]), '/health')).toBe(false);
  });
});

describe('the sort a menu link carries does not stop it matching', () => {
  const group = (items: string[]): NavGroup => ({
    key: 'workloads',
    label: 'Workloads',
    deferred: false,
    items: items.map((href) => ({ text: href, href })),
  });

  it('matches when the href carries the remembered sort', () => {
    // `viewMemory.remembered` appends what was last used to a kind's
    // bare path, so this is the ordinary shape of a menu href and not a corner.
    // Comparing whole strings would stop matching the moment a table was sorted.
    expect(groupHoldsPath(group(['/c/prod/pods?sort=age']), '/c/prod/pods')).toBe(true);
  });

  it('still matches a detail route under a kind whose href carries one', () => {
    expect(groupHoldsPath(group(['/c/prod/pods?sort=age']), '/c/prod/pods/default/api-0')).toBe(
      true,
    );
  });
});

/**
 * A deferred group is **shut** on a browser that has never arranged the menu,
 * which is the whole of the custom resource heading's cost model. Drawing it open would list the cluster's CustomResourceDefinitions on
 * arrival at every cluster, for a heading nobody clicked.
 *
 * A browser that has never arranged the menu arrives with every group shut
 * except the one holding the page, which the visit opens - not a column of open
 * headings with the page somewhere inside them.
 */
describe('an unarranged menu opens only the group holding the page', () => {
  const cluster: NavGroup = { key: 'cluster', label: 'Cluster', deferred: false, items: [] };
  const workloads: NavGroup = { key: 'workloads', label: 'Workloads', deferred: false, items: [] };
  const network: NavGroup = { key: 'network', label: 'Network', deferred: false, items: [] };
  const custom: NavGroup = { key: 'custom-resources', label: 'Custom Resources', deferred: true, items: [] };
  const all = [cluster, workloads, network, custom];
  /** What `Menu.tsx` hands `setGroupOpen`: the arrangement alone, no visit. */
  const arranged = (): string[] => all.filter((group) => groupIsOpen(group, openGroups())).map((group) => group.key);
  const shown = (): string[] =>
    all.filter((group) => groupIsOpen(group, openGroups(), visitedGroup())).map((group) => group.key);
  /** A reload: the module forgets what it cached, storage keeps what was written. */
  const reload = (): void => {
    resetOpenGroups();
  };

  it('arrives with only the group holding the page open, and writes nothing', () => {
    expect(localStorage.getItem(KEY)).toBeNull();
    visitGroup('workloads');
    expect(shown()).toStrictEqual(['workloads']);
    expect(localStorage.getItem(KEY)).toBeNull();
  });

  it('arrives with everything shut on a page that is in no group', () => {
    visitGroup(undefined);
    expect(shown()).toStrictEqual([]);
  });

  it('keeps a group opened by hand across a reload, and a group closed by hand', () => {
    visitGroup('workloads');
    setGroupOpen('network', true, arranged());
    expect(JSON.parse(localStorage.getItem(KEY) ?? 'null')).toStrictEqual(['network']);

    reload();
    visitGroup('workloads');
    expect(shown()).toStrictEqual(['workloads', 'network']);

    setGroupOpen('network', false, arranged());
    reload();
    visitGroup('workloads');
    expect(shown()).toStrictEqual(['workloads']);
    expect(localStorage.getItem(KEY)).toBe('[]');
  });

  it('opens the shut group a deep link lands in, so the heading of the page is on screen', () => {
    // Arranged with Network shut, and a link straight into a Network page.
    localStorage.setItem(KEY, '["cluster"]');
    visitGroup('network');
    expect(shown()).toStrictEqual(['cluster', 'network']);
    // And the custom resource heading, reached by a link to a custom kind.
    visitGroup('custom-resources');
    expect(shown()).toStrictEqual(['cluster', 'custom-resources']);
  });
});

describe('a deferred group is closed until it is opened', () => {
  const workloads: NavGroup = { key: 'workloads', label: 'Workloads', deferred: false, items: [] };
  const custom: NavGroup = {
    key: 'custom-resources',
    label: 'Custom Resources',
    deferred: true,
    items: [],
  };

  it('is shut on an unarranged menu, as every group is that the page is not in', () => {
    // An ordinary group is shut there too; the deferred one also for what its
    // opening costs.
    expect(groupIsOpen(workloads, null)).toBe(false);
    expect(groupIsOpen(custom, null)).toBe(false);
  });

  it('is open once the arrangement says so, like any other group', () => {
    expect(groupIsOpen(custom, ['custom-resources'])).toBe(true);
    expect(groupIsOpen(workloads, ['custom-resources'])).toBe(false);
  });

  it('records the open when a person opens it against no arrangement', () => {
    // The reason `setGroupOpen` takes what is *shown* open rather than every key.
    // Handed every key it would find `custom-resources` already in the base,
    // write nothing, and the disclosure would be shut again on the next reload.
    setGroupOpen('custom-resources', true, ['workloads']);
    expect(openGroups()).toStrictEqual(['workloads', 'custom-resources']);
  });

  it('opens on arrival at a page inside it, against no arrangement, and writes nothing', () => {
    // The correctness requirement, not an optimization: on a custom resource page
    // the heading must be present and open, or following a link lands the
    // person on a page whose place in the menu is folded away. And it
    // is open for the visit, so storage is exactly as it was.
    visitGroup('custom-resources');
    expect(groupIsOpen(custom, openGroups(), visitedGroup())).toBe(true);
    expect(openGroups()).toBeNull();
    expect(localStorage.getItem(KEY)).toBeNull();
  });

  it('is written by a click on the heading, and stays open on the next visit and after it', () => {
    // The three steps, in the module's own terms. Arriving opens it and
    // writes nothing; leaving shuts it.
    visitGroup('custom-resources');
    visitGroup(undefined);
    expect(groupIsOpen(custom, openGroups(), visitedGroup())).toBe(false);

    // A person opens it by hand: that is an arrangement, and it is written.
    // `arrangedOpen` leaves the visit out, so nothing else rides along with it.
    setGroupOpen('custom-resources', true, ['workloads']);
    expect(JSON.parse(localStorage.getItem(KEY) ?? 'null')).toStrictEqual([
      'workloads',
      'custom-resources',
    ]);

    // Arriving on a custom page after that click keeps it open, and so does
    // leaving it again: the arrangement holds it now, not the visit.
    visitGroup('custom-resources');
    expect(groupIsOpen(custom, openGroups(), visitedGroup())).toBe(true);
    visitGroup(undefined);
    expect(groupIsOpen(custom, openGroups(), visitedGroup())).toBe(true);
  });

  it('does not ride along with a first gesture on another group during the visit', () => {
    // The second door. On an unarranged menu the first gesture writes down what
    // the arrangement draws; handed what was on screen instead, closing
    // Workloads on a custom resource page would have committed the heading too.
    visitGroup('custom-resources');
    setGroupOpen('workloads', false, ['workloads']);
    expect(openGroups()).toStrictEqual([]);
    expect(groupIsOpen(custom, openGroups(), visitedGroup())).toBe(true);
  });
});

/**
 * Which group holds the page, when the page is a custom resource.
 *
 * Two different questions, because a deferred group's items do not exist yet.
 * Once they do, the kinds are one level down and a check that looked only at
 * `items` would answer "no" for every one of them.
 */
describe('a custom resource page is inside the heading that has not loaded', () => {
  const menu: readonly NavGroup[] = [
    {
      key: 'workloads',
      label: 'Workloads',
      deferred: false,
      items: [{ text: 'Pods', href: '/c/prod/pods' }],
    },
    { key: 'custom-resources', label: 'Custom Resources', deferred: true, items: [] },
  ];

  it('finds the page one level down, once the group is filled', () => {
    const filled: NavGroup = {
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
    expect(groupHoldsPath(filled, '/c/prod/certificates.cert-manager.io')).toBe(true);
    expect(groupHoldsPath(filled, '/c/prod/pods')).toBe(false);
  });

  // What the route table reserves under `/c/:cluster` for pages that are not
  // kinds. The real list is `NON_KIND_SEGMENTS` in `router/routes.tsx`;
  // `Menu.test.tsx` mounts the application and proves that one is the one
  // handed in. Spelled here so this file stays a unit test of the function.
  const reserved = ['helm', 'x'] as const;

  it('names the deferred group for a kind no loaded group claims', () => {
    // `onACustomKind` asked from the other side: the loaded groups are exactly
    // the built-in kinds, so a kind route none of them holds is a custom one.
    expect(deferredGroupHoldsPath(menu, '/c/prod/certificates.cert-manager.io', reserved)).toBe(
      'custom-resources',
    );
  });

  it('names nothing for a kind a loaded group already holds', () => {
    expect(deferredGroupHoldsPath(menu, '/c/prod/pods', reserved)).toBeUndefined();
    expect(deferredGroupHoldsPath(menu, '/c/prod/pods/default/api-0', reserved)).toBeUndefined();
  });

  it('names nothing for a built-in kind under a cluster spelled the other way', () => {
    // `/c/pr%40d/pods` against a menu that says `/c/pr@d/pods`. Answering the
    // deferred group here would open the heading and list the cluster's CRDs on
    // the Pods list.
    const at: readonly NavGroup[] = [
      {
        key: 'workloads',
        label: 'Workloads',
        deferred: false,
        items: [{ text: 'Pods', href: '/c/pr@d/pods' }],
      },
      { key: 'custom-resources', label: 'Custom Resources', deferred: true, items: [] },
    ];
    expect(deferredGroupHoldsPath(at, '/c/pr%40d/pods', reserved)).toBeUndefined();
    expect(deferredGroupHoldsPath(at, '/c/pr%40d/widgets.example.com', reserved)).toBe(
      'custom-resources',
    );
  });

  it('names nothing for a project kind at the address its menu row links', () => {
    // Network holds the Gateway at `plural.group`, which is where the Argo CD
    // extra's rows link it. The bare plural is held by no row, so it would open
    // the deferred heading and list the cluster's CRDs - which do not hold the kind.
    const at: readonly NavGroup[] = [
      {
        key: 'network',
        label: 'Network',
        deferred: false,
        items: [{ text: 'Gateways', href: '/c/prod/gateways.gateway.networking.k8s.io' }],
      },
      { key: 'custom-resources', label: 'Custom Resources', deferred: true, items: [] },
    ];
    expect(
      deferredGroupHoldsPath(at, '/c/prod/gateways.gateway.networking.k8s.io', reserved),
    ).toBeUndefined();
    expect(
      deferredGroupHoldsPath(at, '/c/prod/gateways.gateway.networking.k8s.io/infra/edge', reserved),
    ).toBeUndefined();
    expect(deferredGroupHoldsPath(at, '/c/prod/gateways', reserved)).toBe('custom-resources');
  });

  it('names nothing off a kind route', () => {
    // The cluster overview, and the pages that are about the console rather than
    // about a cluster. None of them is in any menu group, and opening the
    // heading for one would pay for a CRD list on the page a person lands on.
    expect(deferredGroupHoldsPath(menu, '/c/prod', reserved)).toBeUndefined();
    expect(deferredGroupHoldsPath(menu, '/health', reserved)).toBeUndefined();
    expect(deferredGroupHoldsPath(menu, '/settings/general', reserved)).toBeUndefined();
  });

  it('names nothing for a page the route table reserves under the cluster', () => {
    // `/c/{cluster}/helm` is a kind route by shape and no loaded group claims
    // it - Helm is not a kind - so without `reserved` the answer would be "then
    // it must be a custom resource page", opening the heading and paying the
    // CRD listing for arriving on Helm. The same for an extra's `/x/…` page.
    expect(deferredGroupHoldsPath(menu, '/c/c/helm', reserved)).toBeUndefined();
    expect(deferredGroupHoldsPath(menu, '/c/c/helm/ns/rel', reserved)).toBeUndefined();
    expect(deferredGroupHoldsPath(menu, '/c/c/x/security', reserved)).toBeUndefined();
  });

  it('does not reserve a kind whose name merely starts like a reserved page', () => {
    // `helmreleases.helm.toolkit.fluxcd.io` is a kind Flux installs. The
    // reservation is one whole segment, not a prefix.
    expect(
      deferredGroupHoldsPath(menu, '/c/c/helmreleases.helm.toolkit.fluxcd.io', reserved),
    ).toBe('custom-resources');
  });

  it('names nothing when there is no deferred group at all', () => {
    expect(
      deferredGroupHoldsPath([menu[0]!], '/c/prod/widgets.example.com', reserved),
    ).toBeUndefined();
  });
});

/*
  The kinds column is shown or hidden: `kubetower.menu.mode`, which replaces
  an earlier build's `kubetower.menu.folded`.
*/
describe('the column is shown or hidden, remembered beside the arrangement', () => {
  const MODE = 'kubetower.menu.mode';
  const FOLD = 'kubetower.menu.folded';

  afterEach(() => {
    localStorage.clear();
    resetMenuMode();
  });

  it('is shown until something says otherwise', () => {
    // `null`'s meaning here is the same as above: nothing a person can see
    // today disappears because a feature was added.
    expect(menuIsHidden()).toBe(false);
  });

  it('writes hidden and shown, and reads them back on a fresh import', () => {
    setMenuHidden(true);
    expect(localStorage.getItem(MODE)).toBe('hidden');
    resetMenuMode();
    expect(menuIsHidden()).toBe(true);

    setMenuHidden(false);
    expect(localStorage.getItem(MODE)).toBe('shown');
    resetMenuMode();
    expect(menuIsHidden()).toBe(false);
  });

  it('migrates an old folded value to shown, and removes the old key', () => {
    // A browser that had the icons wanted the column narrow, not gone.
    localStorage.setItem(FOLD, '1');
    expect(menuIsHidden()).toBe(false);
    expect(localStorage.getItem(FOLD)).toBeNull();

    resetMenuMode();
    localStorage.setItem(FOLD, '0');
    expect(menuIsHidden()).toBe(false);
    expect(localStorage.getItem(FOLD)).toBeNull();
  });

  it('reads nonsense as shown', () => {
    localStorage.setItem(MODE, 'icons');
    expect(menuIsHidden()).toBe(false);
  });

  it('leaves the group arrangement alone, which is the whole reason for a second key', () => {
    // An object carrying both would be a shape every existing browser's parser
    // rejects - `readStorage` would answer `null`, `null` means *never
    // arranged*, and an unrelated feature would discard every
    // arrangement on the first load.
    localStorage.setItem(KEY, '["workloads"]');
    setMenuHidden(true);
    expect(localStorage.getItem(KEY)).toBe('["workloads"]');
    expect(openGroups()).toEqual(['workloads']);
  });

  it('tells its subscribers, once, and not when nothing changed', () => {
    let told = 0;
    const release = subscribeToMenuMode(() => {
      told += 1;
    });

    setMenuHidden(true);
    setMenuHidden(true);
    expect(told).toBe(1);

    setMenuHidden(false);
    expect(told).toBe(2);

    release();
    setMenuHidden(true);
    expect(told).toBe(2);
  });

  it('draws the column correctly when storage is not there at all', () => {
    // Private windows, blocked site data, a locked-down profile. The choice is
    // lost; the menu is not.
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked');
    });

    expect(menuIsHidden()).toBe(false);
    expect(() => {
      setMenuHidden(true);
    }).not.toThrow();
    // It still holds for this tab, because the cache is in the module.
    expect(menuIsHidden()).toBe(true);
  });

  it('exports nothing that reads or writes a fold any more', async () => {
    const module: Record<string, unknown> = await import('./menuMemory');
    expect(Object.keys(module).filter((name) => /fold/i.test(name))).toEqual([]);
  });
});

describe('the rail keeps no fold, and a fold an older build stored moves nothing', () => {
  const OPEN = 'kubetower.menu.open';
  const RAIL_FOLD = 'kubetower.rail.folded';

  afterEach(() => {
    localStorage.clear();
    resetMenuMode();
  });

  it('exports nothing that reads or writes a rail fold', async () => {
    // The rail is always the compact strip, so a rail fold key has no reader.
    // A leftover export would be a setting that appears to work and changes
    // nothing.
    const module: Record<string, unknown> = await import('./menuMemory');
    expect(Object.keys(module).filter((name) => /rail/i.test(name))).toEqual([]);
  });

  it('reads the menu as before in a browser that still carries the old rail key', () => {
    localStorage.setItem(OPEN, '["workloads","network"]');
    localStorage.setItem(RAIL_FOLD, '1');

    expect(openGroups()).toEqual(['workloads', 'network']);
    expect(menuIsHidden()).toBe(false);
    // Nothing writes it either: hiding the menu leaves the stale value as it was.
    setMenuHidden(true);
    expect(localStorage.getItem(RAIL_FOLD)).toBe('1');
  });
});
