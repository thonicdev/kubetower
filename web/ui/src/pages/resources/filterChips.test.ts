import { describe, expect, it } from 'vitest';

import type { ListPage } from '../../api';
import type { ContributedColumn } from './extensionColumns';
import { filterColumnsOf, narrowByExtensionColumns } from './filterChips';

const column = (key: string, label: string, over: Record<string, unknown> = {}) => ({
  key,
  label,
  visible: true,
  order: 0,
  sortable: false,
  secondary: false,
  gauged: false,
  copyable: false,
  prose: false,
  filterable: false,
  role: 'other',
  ...over,
});
const page = {
  kind: 'pods',
  label: 'Pods',
  namespaced: true,
  columns: [
    column('node', 'Node', { filterable: true, filterValues: ['n1'] }),
    column('qos', 'QoS', { filterable: true, filterValues: ['Burstable'], visible: false }),
  ],
  rows: [
    { id: 'a/one', name: 'one', namespace: 'a', cells: {} },
    { id: 'a/two', name: 'two', namespace: 'a', cells: {} },
    { id: 'a/three', name: 'three', namespace: 'a', cells: {} },
  ],
} as unknown as ListPage;
const sync: ContributedColumn = {
  extension: 'argocd',
  id: 'x.argocd.sync',
  label: 'Argo CD',
  cells: { 'a/one': { text: 'Synced' }, 'a/two': { text: 'OutOfSync' }, 'a/three': { text: 'Synced' } },
};

describe('the filter control offers every shown column and no hidden one', () => {
  it('takes the shown server columns, then the extension columns with the values of their cells', () => {
    const offered = filterColumnsOf(page, ['Argo CD:Synced'], undefined, [sync]);
    expect(offered.map((one) => one.label)).toEqual(['Node', 'Argo CD']);
    expect(offered[1]).toEqual({ label: 'Argo CD', values: ['OutOfSync', 'Synced'], chosen: ['Synced'] });
  });

  it('follows the arrangement as drawn, where a column was hidden or shown by the person', () => {
    const shown = [
      { key: 'node', visible: false },
      { key: 'qos', visible: true },
    ];
    expect(filterColumnsOf(page, [], shown).map((one) => one.label)).toEqual(['QoS']);
  });

  it('leaves out an extension column no row has an answer for', () => {
    const none: ContributedColumn = { ...sync, cells: { 'elsewhere/x': { text: 'Synced' } } };
    expect(filterColumnsOf(page, [], undefined, [none]).map((one) => one.label)).toEqual(['Node']);
  });
});

describe('narrowByExtensionColumns', () => {
  it('ORs the values of a column and leaves the rows alone when no filter names one', () => {
    const names = (chosen: string[]) => narrowByExtensionColumns(page.rows, chosen, [sync]).map((row) => row.name);
    expect(names(['Argo CD:Synced'])).toEqual(['one', 'three']);
    expect(names(['argo cd:synced', 'Argo CD:OutOfSync'])).toEqual(['one', 'two', 'three']);
    expect(names(['Node:n1'])).toEqual(['one', 'two', 'three']);
  });
});
