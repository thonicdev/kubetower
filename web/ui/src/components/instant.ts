/**
 * One way to write an instant, for every page.
 *
 * A page calling `toLocaleString` with the browser's locale and its own
 * options draws ISO on one page, a French-locale string on another, and an
 * ambiguous `09/10/2026` - the 9th of October or the 10th of September,
 * depending on who reads it - on a third.
 *
 * ## The UI's language, never the machine's
 *
 * The locale is the document's own `lang` (`web/index.html` says `en`), so the
 * dates follow the language the sentences around them are written in. A
 * console whose words are English and whose dates are French is the
 * mixed-language screen to avoid.
 *
 * ## A month name, never a number
 *
 * `10 Oct` cannot be read two ways; `10/10` and `09/10` can. And a 24-hour
 * clock, because `02:03` beside `14:03` is a sort order and `2:03 PM` is not.
 *
 * ## The reader's zone
 *
 * The instant is drawn in the browser's time zone, as every caller here already
 * did: the operator reading the page is at the browser, and it is the same
 * fact either way.
 *
 * Something that does not parse is handed back as it came, rather than as
 * "Invalid Date": a string the server sent is still more use than a word this
 * function made up.
 */

export interface InstantShape {
  /** Seconds as well, where two events a second apart are the ordinary case. */
  readonly seconds?: boolean;
  /** The year as well, where the instant may be older than this one. */
  readonly year?: boolean;
}

/** The UI's language, read from the document. `en` where nothing says. */
export function uiLocale(): string {
  const lang = typeof document === 'undefined' ? '' : document.documentElement.lang;
  return lang === '' ? 'en' : lang;
}

function parsed(at: string | Date): Date | undefined {
  const date = typeof at === 'string' ? new Date(at) : at;
  return Number.isNaN(date.getTime()) ? undefined : date;
}

/** A date and a time: `Oct 10, 14:03`, or with the year and the seconds. */
export function formatInstant(at: string | Date, shape: InstantShape = {}): string {
  const date = parsed(at);
  if (date === undefined) return typeof at === 'string' ? at : '';
  return new Intl.DateTimeFormat(uiLocale(), {
    month: 'short',
    day: 'numeric',
    ...(shape.year === true ? { year: 'numeric' } : {}),
    hour: '2-digit',
    minute: '2-digit',
    ...(shape.seconds === true ? { second: '2-digit' } : {}),
    hourCycle: 'h23',
  }).format(date);
}

/** A time of day alone, seconds included: `14:03:05`. For a stamp on a page
 *  that is about now - when this was read. */
export function formatClock(at: string | Date): string {
  const date = parsed(at);
  if (date === undefined) return typeof at === 'string' ? at : '';
  return new Intl.DateTimeFormat(uiLocale(), {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
  }).format(date);
}

/** A day alone, with its year: `Oct 10, 2026`. */
export function formatDay(at: string | Date): string {
  const date = parsed(at);
  if (date === undefined) return typeof at === 'string' ? at : '';
  return new Intl.DateTimeFormat(uiLocale(), { month: 'short', day: 'numeric', year: 'numeric' }).format(date);
}

/**
 * "4m", "3d" - a span on the **server's** scale, `resources.ShortDuration`
 * transcribed: truncated, seconds under a minute, minutes under an hour, hours
 * under a day, then days, so a run that ended in March is "231d", which is the
 * fact somebody is looking for. A negative span - two clocks that disagree - is
 * drawn as nothing elapsed rather than as a time in the future.
 *
 * Moved here from the Helm page's private `ago` when the pod panel's last
 * termination needed the same scale: two copies of one
 * scale are two answers the first time one of them is changed.
 */
export function shortDuration(milliseconds: number): string {
  const seconds = Math.max(0, Math.floor(milliseconds / 1000));
  if (seconds < 60) return `${String(seconds)}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${String(minutes)}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${String(hours)}h`;
  return `${String(Math.floor(hours / 24))}d`;
}
