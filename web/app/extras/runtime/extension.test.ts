import { describe, expect, it } from 'vitest';

import { currentTabAddress } from '../../../ui/src/pages/object/extensionTabs';
import { runtimeInspector } from './extension';

// The Runtime Inspector was published as the JVM Inspector under the key
// `jvm`: its bookmarks and its tab links must keep opening it.
describe('the Runtime Inspector under its name before 0.2.0', () => {
  it('declares jvm as a key it was published under', () => {
    expect(runtimeInspector.key).toBe('runtime');
    expect(runtimeInspector.legacyKeys).toEqual(['jvm']);
  });

  it('opens an old tab link on its own tab', () => {
    expect(currentTabAddress([runtimeInspector], 'x.jvm.jvm')).toBe('x.runtime.jvm');
  });
});
