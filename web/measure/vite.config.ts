import { fileURLToPath } from 'node:url';

import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig, type Plugin } from 'vite';

import { changelog } from '../scripts/changelog.ts';

/**
 * The layout harness's own dev server.
 *
 * It is a second Vite configuration rather than a flag on the app's, because
 * the app's `server.proxy` points `/api` and `/kt-pwa` at a console on 5823 and
 * this path must never assume one is running. Everything else — the React
 * plugin, Tailwind, the `@kubetower/ui` alias, the `dedupe` list — is the same
 * as `web/vite.config.ts` on purpose: a harness compiled differently from the
 * application measures a different application.
 *
 * ## There is no `measure.html`, and that is the styling rule, not a preference
 *
 * `scripts/check-styles.mjs` refuses any hand-written `.html` outside two
 * named files, so an entry document on disk would need a **third** exception —
 * which that file calls "a defect rather than a judgment call". So the
 * document is a string served by the middleware below and
 * handed to `transformIndexHtml`, which is the same pipeline Vite runs
 * `web/index.html` through: the module graph, the Tailwind stylesheet and HMR
 * all behave exactly as they do for the application.
 *
 * `configureServer` installs this before Vite's internal middlewares, so the
 * SPA fallback never sees `/`.
 */
function harnessDocument(): Plugin {
  const html = [
    '<!doctype html>',
    '<html lang="en">',
    '<head>',
    '<meta charset="utf-8" />',
    '<title>KubeTower layout harness</title>',
    '</head>',
    '<body>',
    '<div id="root"></div>',
    '<script type="module" src="/measure/main.tsx"></script>',
    '</body>',
    '</html>',
  ].join('\n');

  return {
    name: 'kubetower-measure-document',
    configureServer(server) {
      server.middlewares.use((request, response, next) => {
        const path = (request.url ?? '/').split('?')[0];
        if (path !== '/' && path !== '/index.html') {
          next();
          return;
        }
        server
          .transformIndexHtml(request.url ?? '/', html)
          .then((transformed) => {
            response.setHeader('Content-Type', 'text/html; charset=utf-8');
            response.end(transformed);
          })
          .catch(next);
      });
    },
  };
}

export default defineConfig({
  // `web/`, the same root the application builds from, so Tailwind's automatic
  // source detection sees exactly the files it sees for the real build. A root
  // of `web/measure` would scan a subtree and silently emit a smaller
  // stylesheet — which is a harness measuring CSS the console does not have.
  root: fileURLToPath(new URL('..', import.meta.url)),

  plugins: [harnessDocument(), react(), tailwindcss(), changelog(fileURLToPath(new URL('../../CHANGELOG.md', import.meta.url)))],

  resolve: {
    alias: {
      '@kubetower/ui': fileURLToPath(new URL('../ui/src/index.ts', import.meta.url)),
    },
    dedupe: ['react', 'react-dom', 'react-router', '@tanstack/react-query'],
  },

  server: {
    // 5847. 5841-5846 and 5191-5196 belong to other sessions and 5823 to the
    // installed console; `strictPort` so a clash fails loudly instead of
    // silently moving to 5848 and measuring nothing.
    host: '127.0.0.1',
    port: 5847,
    strictPort: true,
  },
});
