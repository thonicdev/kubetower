import { describe, expect, it } from 'vitest';

import { writeInOrder } from './endpoints';
import { ApiError } from './errors';

/*
  A preference write the server applied and could not save is `not_saved`,
  and it is not a refusal. A gesture of several writes
  stops at a refusal - it changed nothing - and goes on past `not_saved`, which
  changed the arrangement, then says so.
*/
describe('writeInOrder', () => {
  const problem = (status: number, code: string): ApiError =>
    new ApiError(status, code, { status, title: code, code, detail: code });

  it('stops at a refusal, so nothing after it is sent', async () => {
    const sent: string[] = [];
    const refusal = problem(400, 'invalid_request');
    await expect(
      writeInOrder([
        () => {
          sent.push('first');
          return Promise.reject(refusal);
        },
        () => {
          sent.push('second');
          return Promise.resolve();
        },
      ]),
    ).rejects.toBe(refusal);
    expect(sent).toEqual(['first']);
  });

  it('sends the rest past a write that was applied and not saved, then throws that', async () => {
    const sent: string[] = [];
    const unsaved = problem(500, 'not_saved');
    await expect(
      writeInOrder([
        () => {
          sent.push('first');
          return Promise.reject(unsaved);
        },
        () => {
          sent.push('second');
          return Promise.resolve();
        },
      ]),
    ).rejects.toBe(unsaved);
    expect(sent).toEqual(['first', 'second']);
    expect(unsaved.isNotSaved).toBe(true);
  });

  it('treats any other 500 as the failure it is', async () => {
    const sent: string[] = [];
    const internal = problem(500, 'internal');
    await expect(
      writeInOrder([
        () => {
          sent.push('first');
          return Promise.reject(internal);
        },
        () => {
          sent.push('second');
          return Promise.resolve();
        },
      ]),
    ).rejects.toBe(internal);
    expect(sent).toEqual(['first']);
  });
});
