import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';

import type { ExtensionInfo } from '../../api';
import { byLabel, ExtraTab } from './ExtraTab';

const row = (key: string, label: string, over: Partial<ExtensionInfo> = {}): ExtensionInfo => ({
  key,
  label,
  enabled: false,
  pinned: false,
  writable: true,
  settings: [],
  ...over,
});

const items: ExtensionInfo[] = [
  // Keys sort one way, labels another; one label starts in lower case.
  row('a', 'Zeta'),
  row('b', 'beta'),
  row('kubecron', 'KubeCron', {
    enabled: true,
    settings: [{ key: 'checks', label: 'Run the checks', value: true, default: true, kind: 'bool' }],
  }),
  row('c', 'Alpha'),
];

function draw(list: readonly ExtensionInfo[] = items) {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>
        <ExtraTab cluster="prod" clusters={[]} items={list} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}
describe('Settings > Extra lists the extras alphabetically by label', () => {
  it('sorts case-insensitively, whatever the order of the listing', () => {
    expect(byLabel(items).map((one) => one.label)).toEqual(['Alpha', 'beta', 'KubeCron', 'Zeta']);
    draw();
    const drawn = [...document.querySelectorAll('[data-extra]')].map((one) => one.getAttribute('data-extra'));
    expect(drawn).toEqual(['c', 'b', 'kubecron', 'a']);
  });
});
