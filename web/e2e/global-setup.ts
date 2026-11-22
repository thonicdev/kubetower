import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { request, type FullConfig } from '@playwright/test';
import { MUTATION_HEADER, PASSWORD, SESSION_ENV, USER } from './session';

/**
 * Signs in once, through the API, and keeps the session for every test, so
 * the password is in no trace.
 *
 * **Why not through the page.** `trace: 'retain-on-failure'` records every
 * action with its arguments, and the network with its request bodies, so a test
 * that typed the password into the form and then failed would leave a
 * `trace.zip` under `test-results/` holding it in clear, and in CI that is a
 * downloadable artifact. A request made here is in no trace: global setup runs
 * outside every test, and its context is not traced.
 *
 * One page still signs in through the form, because the form is part of what
 * the smoke path proves; that test turns tracing off and leaves no form behind
 * when it fails (see signin.spec.ts).
 *
 * **The session's own value is a secret too**, and every trace of a test that
 * uses it carries it (session.ts says how). So it is masked in the CI log the
 * moment it exists, and written where the CI job's search can read it
 * (`KT_E2E_SESSION_VALUES`), so that search refuses an artifact holding it.
 *
 * The failure says what the console answered and never what was sent.
 */
export default async function globalSetup(config: FullConfig): Promise<void> {
  delete process.env[SESSION_ENV];
  if (PASSWORD === '') return;

  const baseURL = config.projects[0]?.use.baseURL;
  const api = await request.newContext(baseURL === undefined ? {} : { baseURL });
  try {
    const answer = await api.post('/api/auth/login', {
      headers: { [MUTATION_HEADER]: '1' },
      data: { user: USER, password: PASSWORD },
    });
    if (answer.status() !== 204) {
      throw new Error(
        `sign-in through the API was refused by ${baseURL ?? 'the console'}: ` +
          `${answer.status()} ${(await answer.text()).slice(0, 300)}`,
      );
    }
    const state = await api.storageState();
    const values = state.cookies.map((cookie) => cookie.value).filter((value) => value !== '');
    if (values.length === 0) {
      throw new Error('the console accepted the sign-in and set no cookie');
    }
    // Before anything else can print them. A workflow command, so only where a
    // runner reads it; anywhere else this line would be the value on a terminal.
    if (process.env.GITHUB_ACTIONS === 'true') {
      for (const value of values) console.log(`::add-mask::${value}`);
    }
    const out = process.env.KT_E2E_SESSION_VALUES ?? '';
    if (out !== '') writeFileSync(out, `${values.join('\n')}\n`, { mode: 0o600 });

    // A directory of its own: `wx` cannot follow a link planted at a fixed name,
    // and nothing else is in it for the teardown to remove.
    const dir = mkdtempSync(join(tmpdir(), 'kubetower-e2e-'));
    const file = join(dir, 'session.json');
    writeFileSync(file, JSON.stringify(state), { mode: 0o600, flag: 'wx' });
    process.env[SESSION_ENV] = file;
  } finally {
    await api.dispose();
  }
}
