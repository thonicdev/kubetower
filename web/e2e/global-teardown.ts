import { rmSync } from 'node:fs';
import { dirname } from 'node:path';
import type { FullConfig } from '@playwright/test';
import { filesHolding, PASSWORD, sessionFile } from './session';

/**
 * Removes the session global-setup.ts kept, whatever the run did, and then
 * refuses a run that left the password in `test-results/`.
 *
 * The refusal is the check behind signin.spec.ts's promise, and it holds on a
 * developer's machine as well as in CI: Playwright writes `error-context.md` for
 * every failed test, with an accessibility snapshot of the page, and a password
 * field still filled when a test fails is in that snapshot in clear. The run
 * fails naming the file, so it is deleted rather than kept by accident.
 */
export default function globalTeardown(config: FullConfig): void {
  const file = sessionFile();
  if (file !== undefined) rmSync(dirname(file), { recursive: true, force: true });

  const leaks = config.projects.flatMap((project) => filesHolding(project.outputDir, PASSWORD));
  if (leaks.length > 0) {
    throw new Error(
      `the password is written in clear in ${leaks.join(', ')}. Delete ` +
        'those files; a test that fills the password must leave no form behind when it fails',
    );
  }
}
