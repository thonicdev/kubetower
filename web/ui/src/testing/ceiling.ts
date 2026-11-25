import { expect, inject } from 'vitest';

declare module 'vitest' {
  export interface ProvidedContext {
    /** Run under `--mode perf`: `pnpm run test:perf` (#109). */
    perf: boolean;
  }
}

/** What `pnpm run test:perf` selects a test by (`-t`), so what its name must hold. */
export const CEILING_NAME = 'time ceiling';

/**
 * A time ceiling, asserted only by the perf check (#109).
 *
 * `pnpm run test:perf` runs the tests that state one - their names say "time
 * ceiling" - one file at a time, where a duration measures the console. In the
 * ordinary suite, which shares the machine with everything else running, the
 * same test still checks what it draws and leaves the clock alone.
 *
 * **The name is checked in every mode.** The perf check finds these tests by
 * name, so a test renamed without "time ceiling" would be skipped there and its
 * ceiling measured nowhere; it fails here instead, in the ordinary suite too.
 */
export function withinCeiling(ms: number, ceiling: number): void {
  const name = expect.getState().currentTestName ?? '';
  if (!name.includes(CEILING_NAME)) {
    throw new Error(`withinCeiling in "${name}": the test's name must say "${CEILING_NAME}", or pnpm run test:perf never runs it`);
  }
  if (inject('perf')) expect(ms).toBeLessThan(ceiling);
}
