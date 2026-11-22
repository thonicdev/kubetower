/**
 * A Secret's values in the manifest editor: put in, and taken back out.
 *
 * ## The document arrives masked, and the diff is computed against the mask
 *
 * `detail.Trimmed` writes `<masked>` over every value of a Secret before the
 * document reaches this client, and `internal/edit` diffs the edited document
 * against **that same masked form**. That equality is the whole safety of the
 * editor: a value the operator never touched is `<masked>` on both sides, so it
 * is equal, so it cannot enter the patch. The server's header comment says as
 * much - "a masked value is compared against the same mask, so it cannot be
 * written back over the real one".
 *
 * ## So a reveal has to be undone before the document is sent
 *
 * An operator editing a Secret needs to see its values, and gets them through
 * the one door (`revealEnvValue`, one request per key - `ManifestEditor.tsx`
 * says why). But a revealed value left in the document would now differ from
 * the mask the server diffs against, and *every* key would enter the patch as
 * a change - the confirmation would list ten fields for an edit to one label,
 * and ten values would travel in a write that was about none of them.
 *
 * `remask` is the other half of the reveal: on the way out, every value that is
 * still exactly what the door returned is written back as `<masked>`. What the
 * operator changed stays changed; what they only looked at leaves as it came.
 *
 * ## Line-based, scoped to the `data:` block, and why not a YAML parser
 *
 * The document is rendered by `sigs.k8s.io/yaml` on the server: two-space
 * indentation, top-level blocks at column zero, one `key: value` per line under
 * `data:`. Base64 has no space in it, so the renderer never folds a value onto
 * a second line, and it has no quote or backslash in it either. That is a
 * narrow enough shape to address by line, and addressing by line is what keeps
 * the operator's own edits - their indentation, their ordering, a comment they
 * typed - untouched by a reveal. A parse-and-reprint would rewrite the whole
 * textarea under them.
 *
 * Scoped to the `data:` block, not the document: a Secret may carry a key
 * called `name` or `labels`, and `metadata:` has children at the same
 * indentation with those exact names. A reveal that matched `  name: ` anywhere
 * would write a credential over the object's name.
 *
 * ## The revealed form is quoted
 *
 * `"YWRtaW4="` and not `YWRtaW4=`. A base64 string can happen to be all digits,
 * and YAML would read that as a number - the patch would then carry an integer
 * where the API expects a string and the dry run would refuse an edit the
 * operator did not make. Quoting costs nothing (there is no character in base64
 * that needs escaping) and removes the case. An empty value is `""` for the
 * same reason in the other direction: a bare `key: ` is YAML null, and null in a
 * merge patch **deletes the key**.
 */

/** The mask `detail.Trimmed` writes. The same string as Go's
 *  `detail.SecretValueMask`, and deliberately not valid base64, so it can never
 *  be mistaken for a value a Secret really holds. */
export const MASK = '<masked>';

const INDENT = '  ';

/** The lines of the document that sit under a top-level `data:`, as a
 *  half-open range - or `undefined` when there is no such block. */
function dataBlock(lines: readonly string[]): { from: number; to: number } | undefined {
  const heading = lines.findIndex((line) => line === 'data:');
  if (heading === -1) return undefined;
  let to = heading + 1;
  while (to < lines.length && (lines[to]?.startsWith(INDENT) === true || lines[to] === '')) {
    to += 1;
  }
  return { from: heading + 1, to };
}

function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/** `  key: value`, with the key as the renderer may have quoted it. The value
 *  is everything after the separator; a base64 value has no space and needs
 *  no further parsing. */
function keyPattern(key: string): RegExp {
  const k = escapeRegExp(key);
  return new RegExp(`^${INDENT}(?:"${k}"|'${k}'|${k}): (.*)$`);
}

/** Where `key` sits in the data block, and what its value reads as there. */
function locate(
  lines: readonly string[],
  key: string,
): { at: number; value: string } | undefined {
  const block = dataBlock(lines);
  if (block === undefined) return undefined;
  const pattern = keyPattern(key);
  for (let at = block.from; at < block.to; at += 1) {
    const match = pattern.exec(lines[at] ?? '');
    if (match !== null) return { at, value: match[1] ?? '' };
  }
  return undefined;
}

/** How the note `detail.manifestOf` writes over a masked document begins. */
const MASK_NOTE = '# The values below are masked.';

/** Whether any value in the data block is still the mask. */
export function anyMasked(document: string): boolean {
  const lines = document.split('\n');
  const block = dataBlock(lines);
  if (block === undefined) return false;
  return lines.slice(block.from, block.to).some((line) => line.endsWith(`: ${MASK}`));
}

/**
 * The document without the server's note that its values are masked, or the
 * document as it is when it does not open with that note. The note is the
 * comment lines at the very top, the first of which begins with `MASK_NOTE`.
 *
 * Once every value is shown, the note is untrue, and a document that says its
 * values are masked over the values themselves is one nobody can trust about
 * which is which. A comment, so taking it out changes nothing the cluster
 * reads.
 */
export function withoutMaskNote(document: string): string {
  if (!document.startsWith(MASK_NOTE)) return document;
  const lines = document.split('\n');
  let first = 0;
  while (first < lines.length && lines[first]?.startsWith('#') === true) first += 1;
  return lines.slice(first).join('\n');
}

/** A value as it is written into the document: always quoted (see the header). */
export function written(base64: string): string {
  return `"${base64}"`;
}

/** One layer of matching quotes removed, so an operator who deleted the quotes
 *  around a value they did not otherwise change is not charged with a change. */
function unquoted(value: string): string {
  if (value.length >= 2) {
    const first = value[0];
    if ((first === '"' || first === "'") && value.endsWith(first)) {
      return value.slice(1, -1);
    }
  }
  return value;
}

/**
 * The document with `key`'s mask replaced by the revealed value, or `undefined`
 * when the key is not in the data block **or is no longer masked** - the
 * operator may have typed over the mask before revealing, and a reveal must not
 * write over what they typed.
 */
export function revealInto(document: string, key: string, base64: string): string | undefined {
  const lines = document.split('\n');
  const found = locate(lines, key);
  if (found === undefined || found.value !== MASK) return undefined;
  lines[found.at] = `${INDENT}${keyAsWritten(lines[found.at] ?? '')}: ${written(base64)}`;
  return lines.join('\n');
}

/** The key exactly as the line spells it, quotes included, so a reveal keeps
 *  the renderer's quoting rather than re-deciding it. */
function keyAsWritten(line: string): string {
  const separator = line.indexOf(': ');
  return line.slice(INDENT.length, separator);
}

/**
 * The document as it must be sent: every revealed value the operator left as
 * it came back is written as the mask again. `revealed` maps a key to the
 * base64 the door returned for it.
 */
export function remask(document: string, revealed: Readonly<Record<string, string>>): string {
  const lines = document.split('\n');
  for (const [key, base64] of Object.entries(revealed)) {
    const found = locate(lines, key);
    if (found === undefined) continue;
    if (unquoted(found.value) === base64) {
      lines[found.at] = `${INDENT}${keyAsWritten(lines[found.at] ?? '')}: ${MASK}`;
    }
  }
  return lines.join('\n');
}

/**
 * The base64 of a text value, byte for byte what the API server holds for a
 * text key. `TextEncoder` first: `btoa` alone throws on any character outside
 * Latin-1, and a Secret can hold a certificate subject with one in it.
 */
export function toBase64(text: string): string {
  const bytes = new TextEncoder().encode(text);
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}
