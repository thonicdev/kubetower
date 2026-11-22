import { configure } from '@testing-library/react';
import { vi } from 'vitest';

/**
 * **For a suite whose ceilings were measuring the runner, not the console.**
 *
 * Some suites (`Topbar`, `routes`, `LogPaging`, `TerminalPane`) assert nothing
 * about time, yet on a loaded runner they can outrun `setup.ts`'s 3 s wait
 * ceiling or vitest's 5 s test timeout. Each one draws something and then
 * reads it; on an idle machine each of these files takes half a second to
 * three, but on a runner sharing its VM with a docker build and a
 * `go test -race` one test can take tens of seconds. A ceiling sized for the
 * idle machine is then a measurement of the neighbors.
 *
 * So these suites keep the wait and lose the ceiling's guess: a `findBy*` or a
 * `waitFor` still returns the moment its element is there, which is all it
 * ever waited for, and fails only when the element does not come at all. What
 * that costs is time on a real failure - fifteen seconds to say so instead of
 * three - and it is paid by four files, not by every suite.
 *
 * **This buys time; it does not make anything deterministic.** The work these
 * tests do still scales with the machine's load, and a load heavy enough will
 * outrun thirty seconds too: the ceilings sit at about three times the slowest
 * test measured under heavy load. What does remove a dependence on scheduling
 * - fake timers, an awaited `findByRole` - is in the suites, not here.
 *
 * Other files could fail the same way under the same load; they opt in only
 * once they need to.
 *
 * Call it once, at the top of the file. vitest isolates each file in its own
 * module graph, so neither setting reaches another suite.
 */
export const LOADED_WAIT_MS = 15_000;
export const LOADED_TEST_MS = 30_000;

export function patientWithALoadedRunner(): void {
  configure({ asyncUtilTimeout: LOADED_WAIT_MS });
  vi.setConfig({ testTimeout: LOADED_TEST_MS, hookTimeout: LOADED_TEST_MS });
}
