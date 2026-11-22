import { describe, expect, it } from 'vitest';

import { composeFilter, splitFilter } from './filterText';

/**
 * The grammar of the one box, as a table.
 *
 * Each row is a thing an operator types and the two parameters the address
 * must carry for it. The split is the whole decision - one field, and the
 * console decides which layer narrows - so the table is where the next
 * reader checks what a given string becomes before touching the page.
 */
describe('splitting what was typed', () => {
  it.each([
    // Plain words are a search, and several stay several.
    ['nginx', { selector: '', search: 'nginx' }],
    ['nginx web', { selector: '', search: 'nginx web' }],
    // One selector, sent as written: the API server's own grammar, unparsed.
    ['app=api', { selector: 'app=api', search: '' }],
    ['app=api,tier!=cache', { selector: 'app=api,tier!=cache', search: '' }],
    ['tier!=cache', { selector: 'tier!=cache', search: '' }],
    // Both, in either order.
    ['app=api nginx', { selector: 'app=api', search: 'nginx' }],
    ['nginx app=api', { selector: 'app=api', search: 'nginx' }],
    ['app=api,tier!=cache nginx', { selector: 'app=api,tier!=cache', search: 'nginx' }],
    // Two requirements typed as two words are one selector, joined by the
    // grammar's own comma.
    ['app=api nginx tier!=cache', { selector: 'app=api,tier!=cache', search: 'nginx' }],
    // The set operators carry spaces of their own and are one requirement.
    ['env in (prod, staging)', { selector: 'env in (prod, staging)', search: '' }],
    ['env in (prod, staging) nginx', { selector: 'env in (prod, staging)', search: 'nginx' }],
    ['env notin (dev) web', { selector: 'env notin (dev)', search: 'web' }],
    ['env in(prod,staging)', { selector: 'env in(prod,staging)', search: '' }],
    // A comma with a space after it is still the grammar's comma.
    ['app=api, tier!=cache nginx', { selector: 'app=api,tier!=cache', search: 'nginx' }],
    // Nothing, and nothing but space.
    ['', { selector: '', search: '' }],
    ['   ', { selector: '', search: '' }],
    // A word the user has not finished is still a word.
    ['app=api ', { selector: 'app=api', search: '' }],
    // `!key`, the key is absent, is the grammar too (#739); a lone `!` is not.
    ['!network', { selector: '!network', search: '' }],
    ['!network redis', { selector: '!network', search: 'redis' }],
    ['app=api,!canary', { selector: 'app=api,!canary', search: '' }],
    ['app=api !canary', { selector: 'app=api,!canary', search: '' }],
    ['!', { selector: '', search: '!' }],
    // `!key` in any position of a comma-joined selector (`kubectl -l` reads a
    // bare key as "present"), never a search word that finds nothing.
    ['app, !canary', { selector: 'app,!canary', search: '' }],
    ['redis,!canary', { selector: 'redis,!canary', search: '' }],
    ['!canary, redis', { selector: '!canary,redis', search: '' }],
    ['network=redis,!canary web', { selector: 'network=redis,!canary', search: 'web' }],
    // A comma in plain words stays a search.
    ['foo,bar', { selector: '', search: 'foo,bar' }],
    // A label's key or value alone is a search: the server matches both.
    ['network', { selector: '', search: 'network' }],
    ['redis', { selector: '', search: 'redis' }],
    // The other operators.
    ['network==redis', { selector: 'network==redis', search: '' }],
    ['network!=redis', { selector: 'network!=redis', search: '' }],
  ])('%j', (typed, expected) => {
    expect(splitFilter(typed)).toEqual(expected);
  });
});

describe('composing the address into one string', () => {
  it.each([
    [{ selector: '', search: '' }, ''],
    [{ selector: 'app=api', search: '' }, 'app=api'],
    [{ selector: '', search: 'nginx' }, 'nginx'],
    // The selector first, because it narrows first.
    [{ selector: 'app=api,tier!=cache', search: 'nginx' }, 'app=api,tier!=cache nginx'],
    [{ selector: 'env in (prod, staging)', search: 'nginx web' }, 'env in (prod, staging) nginx web'],
  ])('%j', (parts, expected) => {
    expect(composeFilter(parts)).toBe(expected);
  });

  it('round-trips what the address carries', () => {
    // A pasted link from before the change is composed into the box, and the
    // box's first edit decomposes it back to the same two parameters.
    for (const parts of [
      { selector: 'app=api,tier!=cache', search: 'nginx' },
      { selector: 'env in (prod, staging)', search: '' },
      { selector: '', search: 'storefront web' },
    ]) {
      expect(splitFilter(composeFilter(parts))).toEqual(parts);
    }
  });
});
