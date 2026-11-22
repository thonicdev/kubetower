/**
 * One box, two parameters: the grammar that tells them apart.
 *
 * A list is narrowed at two layers and they are not the same layer. A **label
 * selector** (`app=api,tier!=cache`) asks the *cluster* for less; a **search**
 * (`nginx`) looks through what came back. The distinction is exact, the request
 * carries them as two parameters, but the user should not have to know which
 * layer they are typing at before they can type.
 *
 * So the box is one field and **this module decides what it was given**, token
 * by token: a token in the label-selector grammar - it contains `=` (which
 * covers `==` and `!=`), a set operator, `in (…)` / `notin (…)`, or starts
 * with `!` (`!key`, the key is absent) - is sent as `selector`; anything else
 * is `search`. Both can coexist in one string
 * (`app=api nginx`), in either order, and two requirements typed as two words
 * are one selector joined by the grammar's own comma.
 *
 * **The address still carries two parameters.** `?selector=…&search=…` is a
 * pasteable address, and every link made when the box was two fields keeps
 * landing on the same rows; the server is untouched. The box is the
 * *composition* of the two (`composeFilter`) and each edit *decomposes* back
 * (`splitFilter`). Pure on purpose: this is where the grammar lives and where
 * the next reader looks, and its test table is the specification.
 *
 * **Not a parser.** The selector is passed through as typed - `app=api,` with
 * its trailing comma included - because it is the API server's grammar, it is
 * what an operator already types into `kubectl -l`, and a client that validated
 * it would be a second implementation of a parser whose errors the server
 * states better. What is decided here is only *which parameter*, never
 * *whether it is well-formed*.
 *
 * Two shapes the tokenizer has to know about, both from the set operators:
 * `env in (prod, staging)` is one requirement with two spaces and a
 * comma-space inside it, so whitespace inside parentheses does not split and
 * the `key in (…)` triple is re-joined; and `app=api, tier!=cache` is one
 * selector with the grammar's comma followed by a space, so a comma at the
 * seam between two tokens joins them when either side is selector-shaped.
 */
export interface FilterParts {
  /** What the cluster is asked for. Empty means no selector. */
  selector: string;
  /** What is looked for in the answer. Empty means no search. */
  search: string;
}

/** The two query-string keys, in the order the box composes them. */
export const FILTER_PARAMETERS = ['selector', 'search'] as const;

const SELECTOR = /=|\s(?:not)?in\s*\(/;
/** `!key`: the key is absent. A `!` followed by a label key's first character,
 *  so a lone `!` stays a search. */
const ABSENT = /^![A-Za-z0-9]/;

/** The box's tooltip: what it reads as a search and what as a selector. */
export const FILTER_SYNTAX = [
  'text: name, columns, label keys and values',
  'key=value  key!=value',
  'key in (a,b)  key notin (a,b)',
  '!key',
].join('\n');

/**
 * Whether one token is in the label-selector grammar rather than a search word.
 *
 * Requirement by requirement: a token the grammar's comma joined is a selector
 * as soon as one of its requirements is, wherever it stands. `app,!canary` and
 * `!canary,redis` are both `kubectl -l` selectors (a bare `app` is "the key is
 * present"); read as a search, either would find nothing and say so silently.
 */
export function isSelectorToken(token: string): boolean {
  return SELECTOR.test(token) || token.split(',').some((part) => ABSENT.test(part.trim()));
}

/** The two parameters for what the user typed. */
export function splitFilter(text: string): FilterParts {
  const selector: string[] = [];
  const search: string[] = [];
  for (const token of joinGrammar(tokenise(text))) {
    (isSelectorToken(token) ? selector : search).push(token);
  }
  return { selector: selector.join(','), search: search.join(' ') };
}

/** The one string the box shows for what the address carries. Selector first,
 *  because it narrows first. */
export function composeFilter({ selector, search }: FilterParts): string {
  return [selector, search].filter((part) => part !== '').join(' ');
}

/** Whitespace-separated words, except that whitespace inside parentheses is
 *  part of the word: `(prod, staging)` is one token. */
function tokenise(text: string): string[] {
  const tokens: string[] = [];
  let current = '';
  let depth = 0;
  for (const character of text) {
    if (character === '(') depth += 1;
    else if (character === ')') depth = Math.max(0, depth - 1);
    if (depth === 0 && /\s/.test(character)) {
      if (current !== '') tokens.push(current);
      current = '';
    } else {
      current += character;
    }
  }
  if (current !== '') tokens.push(current);
  return tokens;
}

/** Re-joins the pieces of one requirement that whitespace split apart. */
function joinGrammar(tokens: readonly string[]): string[] {
  const joined: string[] = [];
  let index = 0;
  while (index < tokens.length) {
    let token = tokens[index] ?? '';
    index += 1;
    while (index < tokens.length) {
      const next = tokens[index] ?? '';
      const after = tokens[index + 1];
      // `env in (a, b)`: key, operator and set are three words and one
      // requirement. The operator is lower-case in the grammar and only
      // counts as one when a set follows it, so a search for the word "in"
      // stays a search.
      if ((next === 'in' || next === 'notin') && after !== undefined && after.startsWith('(')) {
        token = `${token} ${next} ${after}`;
        index += 2;
        continue;
      }
      // `env in(a,b)`: the same with the space before the set omitted.
      if (/^(?:not)?in\(/.test(next)) {
        token = `${token} ${next}`;
        index += 1;
        continue;
      }
      // `app=api, tier!=cache`: the grammar's comma with a space after it.
      // Joined only when one side is already selector-shaped, so that a
      // comma in prose stays where the user put it.
      if (
        (token.endsWith(',') || next.startsWith(',')) &&
        (isSelectorToken(token) || isSelectorToken(next))
      ) {
        token = `${token}${next}`;
        index += 1;
        continue;
      }
      break;
    }
    joined.push(token);
  }
  return joined;
}
