// The API contract's front-end half, checked. Without this it is a habit.
//
//   `api/openapi.yaml` is hand-written and comes before the handlers.
//   `openapi-typescript` derives `web/ui/src/api/schema.ts` from it.
//
// The generated file is committed, because the app compiles against it and a
// build step between editing a component and seeing a type error is a build step
// somebody will skip. A committed generated file is a file that can be stale,
// and a stale one is the exact thing the generation was meant to prevent: a
// second copy of the contract that diverges on the first edit.
//
// So this regenerates into a temporary file and compares. It never writes to the
// tree - a check that fixes what it finds is a check whose failure nobody sees.

import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const webRoot = fileURLToPath(new URL('..', import.meta.url));
const spec = join(webRoot, '..', 'api', 'openapi.yaml');
const committed = join(webRoot, 'ui', 'src', 'api', 'schema.ts');

const scratch = mkdtempSync(join(tmpdir(), 'kubetower-api-'));
const fresh = join(scratch, 'schema.ts');

try {
  // The binary rather than the package manager: no script here shells out to
  // pnpm, because one that does only works where that manager is on PATH, which
  // corepack's shim is not.
  const bin = join(webRoot, 'node_modules', 'openapi-typescript', 'bin', 'cli.js');
  const run = spawnSync(process.execPath, [bin, spec, '-o', fresh], {
    cwd: webRoot,
    encoding: 'utf8',
  });

  if (run.status !== 0) {
    console.error('check-api-types: openapi-typescript failed.');
    if (run.stderr) console.error(run.stderr);
    process.exit(1);
  }

  const a = readFileSync(committed, 'utf8');
  const b = readFileSync(fresh, 'utf8');

  if (a !== b) {
    console.error('check-api-types: ui/src/api/schema.ts is stale against api/openapi.yaml.');
    console.error('                 Run `pnpm run gen:api` and commit the result.');
    process.exit(1);
  }

  console.log('check-api-types: ok. ui/src/api/schema.ts matches api/openapi.yaml.');
} finally {
  rmSync(scratch, { recursive: true, force: true });
}
