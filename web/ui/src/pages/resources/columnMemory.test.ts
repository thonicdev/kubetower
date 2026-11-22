import { afterEach, describe, expect, it, vi } from 'vitest';

import type { ListPage } from '../../api';
import {
  columnMemory,
  forgetColumns,
  rememberDensity,
  rememberPlace,
  rememberShown,
  rememberWidths,
  resetColumnMemory,
  subscribeToColumnMemory,
  withRemembered,
} from './columnMemory';

/**
 * A person's column layout in their own browser, where the console keeps none.
 * The same gestures as `internal/columns`, and the same answers.
 */

function column(key: string, order: number, extra: Record<string, unknown> = {}) {
  return {
    key,
    label: key,
    visible: true,
    order,
    width: null,
    sortable: true,
    secondary: false,
    gauged: false,
    copyable: false,
    prose: false,
    filterable: false,
    role: 'other' as const,
    ...extra,
  };
}

const page = {
  kind: 'pods',
  label: 'Pods',
  namespaced: true,
  columns: [
    column('name', 0, { role: 'name', lockedNote: 'The name cannot be hidden.' }),
    column('namespace', 1),
    column('cpu', 2),
    column('node', 3, { secondary: true, visible: false }),
  ],
  rows: [],
  columnsCustomised: true,
} as unknown as ListPage;

function drawn(of: ListPage): string[] {
  return [...of.columns]
    .sort((a, b) => a.order - b.order)
    .filter((each) => each.visible)
    .map((each) => each.key);
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('a browser that has arranged nothing', () => {
  it('draws what the server sent, and offers no reset', () => {
    const shown = withRemembered(page, 'pods', columnMemory());
    expect(drawn(shown)).toEqual(['name', 'namespace', 'cpu']);
    // The server's word is not taken: here it keeps nothing a person can undo.
    expect(shown.columnsCustomised).toBe(false);
  });
});

describe('the gestures', () => {
  it('clamps a width to the server bounds, and null takes it away', () => {
    rememberWidths('pods', [
      { key: 'cpu', width: 4000 },
      { key: 'namespace', width: 3 },
    ]);
    let shown = withRemembered(page, 'pods', columnMemory());
    expect(shown.columns.find((each) => each.key === 'cpu')?.width).toBe(720);
    expect(shown.columns.find((each) => each.key === 'namespace')?.width).toBe(56);
    expect(shown.columnsCustomised).toBe(true);

    rememberWidths('pods', [
      { key: 'cpu', width: null },
      { key: 'namespace', width: null },
    ]);
    shown = withRemembered(page, 'pods', columnMemory());
    expect(shown.columns.find((each) => each.key === 'cpu')?.width).toBeNull();
    // Back to nothing remembered, so no reset either.
    expect(shown.columnsCustomised).toBe(false);
  });

  it('places a column beside another, and keeps a column it has never seen after the rest', () => {
    rememberPlace('pods', ['name', 'namespace', 'cpu', 'node'], 'cpu', 'name', 'before');
    expect(drawn(withRemembered(page, 'pods', columnMemory()))).toEqual(['cpu', 'name', 'namespace']);

    // A release adds a column: it is drawn, after the remembered ones.
    const grown = {
      ...page,
      columns: [...page.columns, column('ip', 4)],
    } as ListPage;
    expect(drawn(withRemembered(grown, 'pods', columnMemory()))).toEqual([
      'cpu',
      'name',
      'namespace',
      'ip',
    ]);
  });

  it('remembers a visibility only where it differs from what the server drew', () => {
    rememberShown('pods', 'node', true, false);
    expect(drawn(withRemembered(page, 'pods', columnMemory()))).toContain('node');
    rememberShown('pods', 'node', false, false);
    // Ticked back: the memory is as it was found.
    expect(columnMemory().kinds.pods).toBeUndefined();
  });

  /*
    A secondary column the server sent **visible**. Compared against
    `secondary`, hiding it would compare equal to "shown" and write nothing, so
    the box would do nothing. Against what was sent, it hides.
  */
  it('hides a secondary column the server sent visible', () => {
    const stale = {
      ...page,
      columns: page.columns.map((each) => (each.key === 'node' ? { ...each, visible: true } : each)),
    } as ListPage;
    rememberShown('pods', 'node', false, true);
    expect(drawn(withRemembered(stale, 'pods', columnMemory()))).not.toContain('node');
    rememberShown('pods', 'node', true, true);
    expect(drawn(withRemembered(stale, 'pods', columnMemory()))).toContain('node');
    expect(columnMemory().kinds.pods).toBeUndefined();
  });

  it('never hides a column the server says cannot be', () => {
    rememberShown('pods', 'name', false, true);
    expect(drawn(withRemembered(page, 'pods', columnMemory()))).toContain('name');
  });

  it('resets one kind and keeps the row height, which is every table', () => {
    rememberDensity('compact');
    rememberWidths('pods', [{ key: 'cpu', width: 200 }]);
    rememberWidths('deployments', [{ key: 'cpu', width: 200 }]);
    forgetColumns('pods');
    expect(columnMemory().kinds.pods).toBeUndefined();
    expect(columnMemory().kinds.deployments).toBeDefined();
    expect(columnMemory().density).toBe('compact');
  });
});

describe('storage', () => {
  it('does not wipe what another tab wrote since this one read', () => {
    rememberWidths('pods', [{ key: 'cpu', width: 200 }]);
    // Another tab, which read nothing of this one, arranges another table.
    localStorage.setItem(
      'kubetower.columns',
      JSON.stringify({
        kinds: { pods: { widths: { cpu: 200 } }, deployments: { widths: { cpu: 300 } } },
      }),
    );
    // This tab's snapshot still predates it; its next gesture must not.
    rememberWidths('pods', [{ key: 'namespace', width: 120 }]);
    resetColumnMemory();
    expect(columnMemory().kinds.deployments?.widths).toEqual({ cpu: 300 });
    expect(columnMemory().kinds.pods?.widths).toEqual({ cpu: 200, namespace: 120 });
  });

  it('survives a reload', () => {
    rememberWidths('pods', [{ key: 'cpu', width: 200 }]);
    resetColumnMemory();
    expect(columnMemory().kinds.pods?.widths).toEqual({ cpu: 200 });
  });

  it('reads nonsense as nothing arranged, never as an error', () => {
    localStorage.setItem(
      'kubetower.columns',
      JSON.stringify({ density: 'huge', kinds: { pods: { widths: { cpu: 'wide' }, order: 3 } } }),
    );
    resetColumnMemory();
    expect(columnMemory()).toEqual({ kinds: {} });

    localStorage.setItem('kubetower.columns', '{not json');
    resetColumnMemory();
    expect(columnMemory()).toEqual({ kinds: {} });
  });

  it('keeps every gesture of this tab when storage reads but refuses writes', () => {
    // A full quota: `getItem` answers, `setItem` throws. Each gesture
    // re-reads storage before writing, and storage does not hold the previous
    // gesture - so without a latch the second resize snaps the first back.
    const unsubscribe = subscribeToColumnMemory(() => {});
    const refuse = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('full', 'QuotaExceededError');
    });
    rememberWidths('pods', [{ key: 'cpu', width: 200 }]);
    rememberWidths('pods', [{ key: 'memory', width: 300 }]);
    expect(columnMemory().kinds.pods?.widths).toEqual({ cpu: 200, memory: 300 });

    // Another tab's write does not take them away either: storage is not the
    // record of what this tab arranged while it refuses this tab's writes.
    window.dispatchEvent(new StorageEvent('storage', { key: 'kubetower.columns' }));
    expect(columnMemory().kinds.pods?.widths).toEqual({ cpu: 200, memory: 300 });

    // Room again: the next accepted write makes storage the record once more,
    // and another tab's write is followed.
    refuse.mockRestore();
    rememberWidths('pods', [{ key: 'age', width: 100 }]);
    expect(JSON.parse(localStorage.getItem('kubetower.columns') ?? '{}')).toMatchObject({
      kinds: { pods: { widths: { cpu: 200, memory: 300, age: 100 } } },
    });
    localStorage.setItem('kubetower.columns', JSON.stringify({ kinds: { pods: { widths: { cpu: 80 } } } }));
    window.dispatchEvent(new StorageEvent('storage', { key: 'kubetower.columns' }));
    expect(columnMemory().kinds.pods?.widths).toEqual({ cpu: 80 });
    unsubscribe();
  });

  it('costs the memory and never the table when storage refuses', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    resetColumnMemory();
    expect(columnMemory()).toEqual({ kinds: {} });
    // The gesture still holds for this tab.
    rememberWidths('pods', [{ key: 'cpu', width: 200 }]);
    expect(
      withRemembered(page, 'pods', columnMemory()).columns.find((each) => each.key === 'cpu')?.width,
    ).toBe(200);
  });
});
