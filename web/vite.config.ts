import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

import { changelog } from './scripts/changelog.ts';
import { thirdPartyNotices } from './scripts/third-party-notices.ts';

/**
 * The core SPA's build.
 *
 * Root is `web/`, so Vite's entry is `web/index.html` - one of exactly two
 * files the styling rules allow to be hand-written (the theme is the other), and it holds a
 * `<div id="root">` and the module script Vite requires.
 */
export default defineConfig(({ mode }) => ({
  plugins: [
    react(),
    tailwindcss(),
    // CHANGELOG.md, for What's new (#80).
    changelog(fileURLToPath(new URL('../CHANGELOG.md', import.meta.url))),
    // The npm half of THIRD_PARTY_NOTICES.txt: what this build bundled, with
    // each package's license text. Written to build/, outside dist/, so it is
    // neither embedded nor served; cmd/ktnotices merges it with the Go modules.
    thirdPartyNotices({
      root: fileURLToPath(new URL('.', import.meta.url)),
      output: 'build/third-party-npm.json',
    }),
  ],

  resolve: {
    alias: {
      '@kubetower/ui': fileURLToPath(new URL('./ui/src/index.ts', import.meta.url)),
    },

    // The first front-end pitfall: two copies of React or of
    // the router break hooks and router context *silently* - no error, a
    // component simply never sees the provider above it. The app and
    // web/ui are separate packages with separate node_modules, so this is not
    // hypothetical here; it is the ordinary case.
    //
    // The extras in app/extras/ are in this build and need nothing more.
    dedupe: ['react', 'react-dom', 'react-router', '@tanstack/react-query'],
  },

  build: {
    // The Go side embeds `web/dist` with `//go:embed all:dist` and looks for
    // `ui/index.html` to decide whether a bundle is present.
    outDir: 'dist/ui',
    emptyOutDir: true,
    // **No sourcemap in a build that can ship.** The bundle is embedded whole
    // (`//go:embed all:dist`) and served, and a map carries `sourcesContent`:
    // every comment of the TypeScript source, which is written for this
    // repository's maintainers, not for whoever runs the binary. `pnpm dev`
    // serves its own maps from memory whatever this says, so development keeps
    // them. KT_SOURCEMAP=1 puts them back for debugging a production build
    // locally; a release refuses a bundle holding any `.map` (release.yml's
    // archives job, the Dockerfile's console stage).
    sourcemap: process.env.KT_SOURCEMAP === '1',
  },

  server: {
    // One process serves /api/* and the SPA in production, so
    // there is no CORS and no second origin. In dev Vite stands in front, and
    // the proxy is what keeps the session cookie same-origin here too.
    proxy: {
      '/api': {
        target: process.env.KT_DEV_API ?? 'http://127.0.0.1:5823',
        ws: true,
        changeOrigin: false,
      },

      // The sign-in page, which the Go server renders: the console leaves
      // for it when a session ends, and through Vite it has to arrive there.
      '/auth': {
        target: process.env.KT_DEV_API ?? 'http://127.0.0.1:5823',
        changeOrigin: false,
      },

      // The tab icon, the manifest and the service worker. `index.html` links
      // them at `/kt-pwa/`, which the Go server serves from its embedded tree
      // (internal/web/server.go, `pwa()`), and which nothing under `web/` holds.
      // Without this entry Vite answers each of them with its SPA fallback:
      // `index.html`, 200, `text/html` - not a 404 - so the browser is handed a
      // document where it asked for an image, draws its default icon and logs
      // nothing: a "different icon in the tab" whose drawing is not wrong,
      // `pnpm dev` simply never served it.
      '/kt-pwa': {
        target: process.env.KT_DEV_API ?? 'http://127.0.0.1:5823',
        changeOrigin: false,
      },
    },
  },

  test: {
    environment: 'jsdom',
    include: ['ui/src/**/*.test.{ts,tsx}', 'app/**/*.test.{ts,tsx}', 'scripts/**/*.test.ts'],
    // One setup file: the Testing Library wait ceiling. Its comment says why.
    setupFiles: ['ui/src/testing/setup.ts'],
    // Thirty seconds a test, not vitest's five (#109). The full suite runs on a
    // machine other work shares, and at a load around 20 a page that mounts in
    // half a second alone takes several: five seconds measured the machine,
    // not the console. A hung test still fails the run.
    testTimeout: 30_000,
    // **Time ceilings are the perf check's, not the suite's** (#109). A test
    // states one with `withinCeiling` (ui/src/testing/ceiling.ts), which
    // asserts only under `--mode perf`: `pnpm run test:perf` runs those tests,
    // one file at a time, so a ceiling measures the console and not whatever
    // else the machine was doing.
    provide: { perf: mode === 'perf' },
    // Only the files that state a ceiling, found by reading them: loading all
    // 250 one at a time to skip their tests took eight minutes.
    ...(mode === 'perf' ? { fileParallelism: false, include: ceilingFiles() } : {}),
  },
}));

/** The test files that call `withinCeiling`, for `--mode perf` (#109). */
function ceilingFiles(): string[] {
  const root = fileURLToPath(new URL('.', import.meta.url));
  const found: string[] = [];
  const walk = (dir: string): void => {
    for (const entry of readdirSync(join(root, dir), { withFileTypes: true })) {
      const path = `${dir}/${entry.name}`;
      if (entry.isDirectory()) {
        if (entry.name !== 'node_modules') walk(path);
      } else if (/\.test\.tsx?$/.test(entry.name) && readFileSync(join(root, path), 'utf8').includes('withinCeiling(')) {
        found.push(path);
      }
    }
  };
  walk('ui/src');
  walk('app');
  return found;
}
