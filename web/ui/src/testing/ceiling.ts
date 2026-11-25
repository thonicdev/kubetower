import { expect, inject } from 'vitest';

declare module 'vitest' {
  export interface ProvidedContext {
    /** Run under `--mode perf`: `pnpm run test:perf` (#109). */
    perf: boolean;
  }
}

/**
 * A time ceiling, asserted only by the perf check (#109).
 *
 * `pnpm run test:perf` runs the tests that state one - their names say "time
 * ceiling" - one file at a time, where a duration measures the console. In the
 * ordinary suite, which shares the machine with everything else running, the
 * same test still checks what it draws and leaves the clock alone.
 */
export function withinCeiling(ms: number, ceiling: number): void {
  if (inject('perf')) expect(ms).toBeLessThan(ceiling);
}
