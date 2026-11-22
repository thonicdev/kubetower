import type { LogBuffer } from './logBuffer';

/**
 * Write what the container printed.
 *
 * **`lines` and never `shownText`.** A saved log is the file `kubectl logs`
 * would have written, and an indented copy is not that file - it cannot be
 * diffed against one, grepped with the patterns the operator's other tools use,
 * or handed to anything that parses a record per line. The JSON toggle is a way
 * of *reading* the pane, not a change to what the pane holds, and this is the
 * line where the difference becomes a file on somebody's disk.
 *
 * It writes the **filtered** set, because "the matches", "what is on screen" and
 * "what n/m counts" are one idea.
 *
 * Three mechanics with their own reasons, and all three are still true from a
 * React component: colons are stripped from the timestamp in the filename
 * because a name the browser refuses is a download that silently does not
 * happen; the anchor is put in the document because Firefox will not follow a
 * click on an anchor that is not in it; and the object URL is revoked after a
 * minute because revoking straight away can cancel the download it was created
 * for.
 */
export function downloadLog(buffer: LogBuffer, name: string, container: string): void {
  const text = buffer.shown.map((index) => buffer.lines[index]?.text ?? '').join('\n');
  const stamp = new Date().toISOString().replace(/:/g, '-');
  const file = `${name}${container === '' ? '' : `-${container}`}-${stamp}.log`;

  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }));
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = file;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
}
