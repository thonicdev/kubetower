import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

import { PLATES } from './Mark';

describe("KubeTower's mark", () => {
  it('draws the plates of the application icon, path for path and colour for colour', () => {
    // The walk to go.mod, as changelog.test.ts reads a file outside web/.
    let root = resolve(process.cwd());
    while (!existsSync(join(root, 'go.mod')) && dirname(root) !== root) root = dirname(root);
    const icon = readFileSync(join(root, 'internal', 'web', 'static', 'pwa', 'icon.svg'), 'utf8');
    const paths = [...icon.matchAll(/<path d="([^"]+)" fill="([^"]+)"\/>/g)].map((m) => ({ d: m[1], fill: m[2] }));
    expect(paths).toEqual(PLATES);
  });
});
