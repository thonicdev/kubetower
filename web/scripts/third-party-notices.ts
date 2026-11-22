import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join, resolve } from 'node:path';

import type { Plugin } from 'vite';

/**
 * The npm half of the release's third-party notices: every package whose code
 * ends up in the console's bundle, with its license text.
 *
 * `LICENSING.md` says third-party notices are preserved, and a minified bundle
 * preserves none: the minifier drops the license comments, and the bundle is
 * then embedded in a binary nobody unpacks. So the notices are a file of their
 * own, generated from what was actually bundled - not from `package.json`, which
 * lists the build tools beside the runtime, and not by hand, which drifts on
 * the next dependency update.
 *
 * What counts as bundled is read from the build itself: every module id of
 * every emitted chunk, mapped to the package directory it lives in. A package
 * that contributes CSS rather than modules (Tailwind's preflight, compiled into
 * the stylesheet by its own plugin) is found by the `/*! name vX | license` banner
 * it leaves in the emitted asset.
 *
 * The helpers the bundler itself writes into the output (Vite's module-preload
 * polyfill and preload helper, Rolldown's runtime) are virtual modules, never
 * under `node_modules`; they are credited to the package they come from.
 *
 * The plugin writes JSON to `web/build/`, outside `dist/`, so it is neither
 * embedded in the binary nor served. `cmd/ktnotices` merges it with the Go
 * modules linked into the binary and writes the one THIRD_PARTY_NOTICES.txt a
 * release ships.
 *
 * **A bundled package with no license file fails the build.** A notices file
 * that silently skips one is the defect this exists to prevent, and the remedy
 * is a decision about that package, not a gap in a generated file.
 */

/** One bundled package, as `cmd/ktnotices` reads it. */
export interface BundledPackage {
  name: string;
  version: string;
  /** The `license` field of its package.json, as published. */
  license: string;
  files: { name: string; text: string }[];
}

/**
 * The file names a license or a notice is published under: LICENSE, LICENSE.md,
 * LICENSE-MIT, NOTICE.txt, LICENSE.libyaml. Any one extension, except those of
 * a source or data file, so license.js is never read as a license. The same
 * rule as `isLicenceFile` in cmd/ktnotices.
 */
const LICENCE_NAME = /^(licen[cs]e|copying|notice|unlicen[cs]e)([-_][^.]*)?(\.([a-z0-9]+))?$/i;
const SOURCE_EXTENSIONS = new Set([
  'go', 's', 'c', 'h', 'cc', 'cpp', 'hpp',
  'js', 'mjs', 'cjs', 'ts', 'mts', 'cts', 'tsx', 'jsx',
  'py', 'rb', 'sh', 'rs', 'java', 'proto',
  'json', 'yaml', 'yml', 'toml', 'mod', 'sum',
]);
export function isLicenceFile(name: string): boolean {
  const match = LICENCE_NAME.exec(name);
  return match !== null && !SOURCE_EXTENSIONS.has((match[4] ?? '').toLowerCase());
}

/** A preserved banner in an emitted asset: `/*! tailwindcss v4.3.3 | MIT License | ...`. */
const BANNER = /\/\*!\s*(@?[a-z0-9][\w.-]*(?:\/[\w.-]+)?)\s+v\d[^\s|]*\s*\|/gi;

/** The package directory a module id lives in, or undefined for our own source. */
function packageDir(id: string): string | undefined {
  // Virtual modules start with NUL; a query or a hash is not part of the path.
  const clean = id.replace(/^\0/, '').replace(/[?#].*$/, '').replaceAll('\\', '/');
  const at = clean.lastIndexOf('/node_modules/');
  if (at < 0) return undefined;
  const rest = clean.slice(at + '/node_modules/'.length).split('/');
  const name = rest[0]?.startsWith('@') ? `${rest[0]}/${rest[1]}` : rest[0];
  if (!name || name.startsWith('.')) return undefined;
  return clean.slice(0, at + '/node_modules/'.length) + name;
}

function readPackage(dir: string): BundledPackage {
  const manifest = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8')) as {
    name: string;
    version: string;
    license?: string | { type?: string };
  };
  const license =
    typeof manifest.license === 'string' ? manifest.license : (manifest.license?.type ?? 'UNKNOWN');
  const files = readdirSync(dir, { withFileTypes: true })
    .filter((entry) => entry.isFile() && isLicenceFile(entry.name))
    .map((entry) => entry.name)
    .sort()
    .map((name) => {
      const text = readFileSync(join(dir, name), 'utf8');
      return { name, text: manifest.name === 'vite' && name === 'LICENSE.md' ? viteOwnLicence(text) : text };
    });
  return { name: manifest.name, version: manifest.version, license, files };
}

/**
 * Vite's LICENSE.md is its own MIT license followed by some hundred kilobytes
 * of licenses for the dependencies Vite bundles into its *build tool*, none of
 * which reaches a page. Only the helpers Vite injects into the bundle do, and
 * they are Vite's own code, so that one file is cut at that heading and says
 * so. Every other package's license is printed whole.
 */
const BUNDLED_SECTION = '\n# Licenses of bundled dependencies';
export function viteOwnLicence(text: string): string {
  const at = text.indexOf(BUNDLED_SECTION);
  if (at < 0) return text;
  return (
    `${text.slice(0, at).trimEnd()}\n\n` +
    '(The rest of this file lists the licenses of the dependencies this package bundles into its own ' +
    'build tool. None of them is part of the KubeTower bundle, so they are not repeated here.)\n'
  );
}

/**
 * The package a virtual module comes from: `\0vite/preload-helper.js` is Vite's,
 * `\0rolldown/runtime.js` is Rolldown's. Both are helpers the bundler writes into
 * the output itself, so they ship without ever being under `node_modules`.
 */
function virtualPackage(id: string): string | undefined {
  if (!id.startsWith('\0')) return undefined;
  const parts = id.slice(1).split('/');
  const name = parts[0]?.startsWith('@') ? `${parts[0]}/${parts[1]}` : parts[0];
  return name || undefined;
}

export function thirdPartyNotices(options: { root: string; output: string }): Plugin {
  const fromRoot = createRequire(join(options.root, 'package.json'));
  /** A package's directory, from the project or else from Vite's own (Rolldown is Vite's dependency). */
  const resolvePackage = (name: string): string | undefined => {
    const bases = [fromRoot];
    try {
      bases.push(createRequire(fromRoot.resolve('vite/package.json')));
    } catch {
      // No Vite to look beside: the project is the only place.
    }
    for (const base of bases) {
      try {
        return dirname(base.resolve(`${name}/package.json`));
      } catch {
        // Try the next place.
      }
    }
    return undefined;
  };
  return {
    name: 'kubetower:third-party-notices',
    apply: 'build',
    generateBundle(_output, bundle) {
      const dirs = new Set<string>();
      for (const item of Object.values(bundle)) {
        if (item.type === 'chunk') {
          for (const id of item.moduleIds) {
            const dir = packageDir(id);
            if (dir) {
              dirs.add(dir);
              continue;
            }
            const virtual = virtualPackage(id);
            if (!virtual) continue;
            const found = resolvePackage(virtual);
            if (!found) {
              this.error(`${item.fileName} contains the virtual module ${JSON.stringify(id)}, and no package ${virtual} resolves to name its license`);
            }
            dirs.add(found);
          }
          continue;
        }
        const source = typeof item.source === 'string' ? item.source : new TextDecoder().decode(item.source);
        for (const banner of source.matchAll(BANNER)) {
          const name = banner[1];
          if (!name) continue;
          let manifest: string;
          try {
            manifest = fromRoot.resolve(`${name}/package.json`);
          } catch {
            this.error(`${item.fileName} carries a license banner for ${name}, which does not resolve from ${options.root}`);
          }
          dirs.add(dirname(manifest));
        }
      }

      const byName = new Map<string, BundledPackage>();
      for (const dir of dirs) {
        if (!existsSync(join(dir, 'package.json'))) continue;
        const found = readPackage(dir);
        byName.set(`${found.name}@${found.version}`, found);
      }
      const packages = [...byName.values()].sort((a, b) =>
        a.name === b.name ? a.version.localeCompare(b.version) : a.name.localeCompare(b.name),
      );
      const bare = packages.filter((found) => found.files.length === 0).map((found) => `${found.name}@${found.version}`);
      if (bare.length > 0) {
        this.error(
          `bundled packages with no license file, so the third-party notices would omit them: ${bare.join(', ')}`,
        );
      }
      if (packages.length === 0) {
        this.error('no npm package was found in the bundle, which cannot be right for a React console');
      }

      const output = resolve(options.root, options.output);
      mkdirSync(dirname(output), { recursive: true });
      writeFileSync(output, `${JSON.stringify(packages, null, 2)}\n`);
      this.info(`third-party notices: ${packages.length} npm packages -> ${output}`);
    },
  };
}
