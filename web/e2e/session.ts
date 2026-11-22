import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';

/**
 * What the browser path shares between its global setup, its tests and its
 * global teardown.
 *
 * **The session file.** `global-setup.ts` signs in once through the API and
 * keeps the session in a directory of its own, made with `mkdtemp` (0700 where
 * the platform has modes). It hands the path to the tests through
 * `KT_E2E_SESSION_FILE`: the workers start after global setup, with its
 * environment. A fixed name in the shared temporary directory would let a file
 * planted there be followed, and two runs overwrite each other's session.
 *
 * **What it keeps out of an artifact, and what it does not.** The file is
 * outside `test-results/`; its *content* is not. A test's context is created
 * from it, and a Playwright trace records the context's options - the
 * `kubetower_session` cookie in clear - and sends that cookie on every request
 * in its network log. So every trace of a signed-in test holds a live session
 * for the console under test. The CI job therefore records no trace
 * (`KT_E2E_TRACE=off`) and searches what it uploads for the cookie as well as
 * the password. On a developer's machine a trace stays in `test-results/`,
 * beside a console that is theirs.
 */
export const SESSION_ENV = 'KT_E2E_SESSION_FILE';

/** The session global setup kept, or undefined when it kept none. */
export function sessionFile(): string | undefined {
  const file = process.env[SESSION_ENV] ?? '';
  return file === '' ? undefined : file;
}

export const PASSWORD = process.env.KT_E2E_PASSWORD ?? '';
export const USER = process.env.KT_E2E_USER ?? 'admin';

/** The header every mutation carries (`internal/api/router.go`, `requestMarker`). */
export const MUTATION_HEADER = 'X-KubeTower-Request';

/**
 * Every file under `root` that holds `needle`, compared raw.
 *
 * For what is not a trace: `error-context.md` above all, which Playwright
 * writes for every failed test whatever `trace` says, and which holds an
 * accessibility snapshot of the page - a filled password field included, in
 * clear. A trace is a zip with compressed members, which this does not open;
 * the CI job's own search does.
 */
export function filesHolding(root: string, needle: string): string[] {
  if (needle === '') return [];
  const found: string[] = [];
  const walk = (dir: string): void => {
    let names: string[];
    try {
      names = readdirSync(dir);
    } catch {
      return;
    }
    for (const name of names) {
      const path = join(dir, name);
      if (statSync(path).isDirectory()) {
        walk(path);
      } else if (readFileSync(path).includes(needle)) {
        found.push(path);
      }
    }
  };
  walk(root);
  return found;
}
