import { describe, expect, it } from 'vitest';

import { isLicenceFile, viteOwnLicence } from './third-party-notices.ts';

describe('isLicenceFile', () => {
  it('takes every license or notice name, whatever its one extension', () => {
    for (const name of [
      'LICENSE',
      'LICENSE.md',
      'license.txt',
      'LICENSE',
      'COPYING',
      'NOTICE',
      'LICENSE-MIT',
      'LICENSE.libyaml',
      'UNLICENSE',
    ]) {
      expect(isLicenceFile(name), name).toBe(true);
    }
  });

  it('refuses a source or data file with a licence-shaped name', () => {
    for (const name of ['license.js', 'license.d.ts', 'LICENSE.json', 'licensed.md', 'LICENSES', 'README.md']) {
      expect(isLicenceFile(name), name).toBe(false);
    }
  });
});

describe('viteOwnLicence', () => {
  it("keeps Vite's own license and drops the build tool's bundled ones, saying so", () => {
    const cut = viteOwnLicence('# Vite core license\nMIT\n\n# Licenses of bundled dependencies\nlots');
    expect(cut).toContain('MIT');
    expect(cut).not.toContain('lots');
    expect(cut).toContain('not repeated here');
  });
});
