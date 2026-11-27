import type { ReactNode } from 'react';

/**
 * The icon set, as one inline sprite plus a `<use>`.
 *
 * **Why a sprite and not a component per glyph**: an icon font would be a
 * third request that has to land before the toolbar stops being a row of
 * squares, and a font's glyphs are *text* to a screen reader - an icon font is
 * how a "Terminal" button comes to be announced as "private use area character".
 * One component per glyph would be dozens of components and imports for shapes
 * that never vary. These are shapes, marked `aria-hidden`, and **every control
 * that uses one carries its own label**.
 *
 * `currentColor` throughout, so an icon takes the color of the button it sits
 * in and hover and focus need no second rule. Drawn on a 24x24 grid with a 2px
 * stroke and round caps - the proportions that read cleanly when a browser
 * scales them to 15px.
 *
 * ## The one that is different, and it is deliberate
 *
 * `claude` is 248x248 rather than 24x24, and it is filled rather than stroked -
 * it is the mark as published, in its published color, not a redrawing of it
 * on our grid. Its own `fill` and `stroke` attributes sit on the path, so they
 * win over the sheet's `fill-none stroke-current` without a second class.
 * Redrawing a trademark to fit a grid is the one thing not to do here.
 * **And its fill is `CLAUDE_MARK`, not `currentColor`** - the one glyph in this
 * sheet that does not take the color of the control it sits in. A recolored
 * mark is a modified mark, and the color is also what makes a word beside it
 * unnecessary: `Terminals.tsx` draws the mark alone, with `aria-label`
 * carrying the name. Whether the mark may be used here at all is
 * `LICENSING.md`'s question, not this file's.
 *
 * ## Where the size and the stroke live
 *
 * In this file, as utilities, and **not** in a stylesheet: only three
 * hand-written CSS/HTML files are allowed and this is not one of them. `--icon` is a
 * plain `:root` declaration in `app/theme.css` for the same reason `--drawer-w`
 * is - it is a computed size, not a step on a scale, so it must not appear in
 * the spacing namespace where `p-icon` and `gap-icon` would become offers.
 */

/**
 * The Claude mark's own color, and the one color in this console that is not
 * ours to tokenize.
 *
 * The styles check refuses a literal color because the palette belongs in
 * `app/theme.css` — and that argument is exactly why this one must **not** go
 * there. `theme.css` is the console's palette, a set of values we choose and
 * may restyle; this is a third party's mark, published at this value, and a
 * recolored mark is a modified mark. Putting it on the scale would invite the
 * next person to harmonize it with the rest, which is the one thing not to do.
 *
 * So it is named here, once, and the two places that assert it in a test name
 * it too — a test that checks a brand color against a constant imported from
 * the file under test asserts nothing.
 */
export const CLAUDE_MARK = '#D97757'; // style-allow: a third party's published mark, not ours to tokenize

/** Every glyph the sheet defines. A name that is not here does not compile,
 *  which is the whole reason this is a union and not `string`: as data
 *  attributes, a typo would draw an empty box in silence. */
export type IconName =
    'terminal' | 'settings' | 'logs' | 'close' | 'forward' | 'stop' | 'copy' | 'external'
  | 'claude' | 'resource' | 'volume' | 'secret' | 'config' | 'node' | 'refresh' | 'edit'
  | 'trash' | 'evict' | 'cordon' | 'uncordon' | 'drain' | 'pause' | 'play' | 'trigger'
  | 'scale' | 'cpu' | 'memory' | 'network' | 'disk'
  // Appended for the kinds menu. A disclosure's own marker is
  // suppressed there and this is drawn in its place, rotated when the group is
  // open. One shape the sheet owes the menu, not a new family.
  | 'chevron'
  // A page header's way back: a plain left arrow, no frame.
  | 'back'
  // The glyph per kind and per group in that menu. One shape per *concept*,
  // not per kind - `shell/kindGlyph.ts` is what answers a kind, and it is where
  // the argument for sharing one lives. `resource`, `secret`, `config`, `node`,
  // `volume`, `disk`, `network`, `settings` and `scale` are shared with the
  // rest of the console.
  | 'pod' | 'deployment' | 'replicaset' | 'statefulset' | 'daemonset' | 'job'
  | 'cronjob' | 'service' | 'ingress' | 'endpoint' | 'class' | 'policy'
  | 'access' | 'quota' | 'lease' | 'webhook' | 'account' | 'role' | 'binding'
  | 'namespace' | 'event' | 'workloads' | 'cluster'
  // The dashboard's own row at the top of the cluster group. Its href
  // has no kind segment, so `kindGlyph` cannot answer it and it must not fall
  // through to `resource` - the first row of the menu drawn as an unknown kind.
  | 'overview'
  // A magnifier. Nothing draws it today; it stays in the sheet as a shape, not
  // as a control anybody can reach.
  | 'search'
  // The list's filter control, beside the search box. A funnel, which is what
  // every console draws for "narrow by a value".
  | 'filter'
  // A list row's menu. The `⋯` every other menu here draws
  // as text (U+22EF), drawn as a shape on a row because a row's text is what
  // the operator reads, selects and copies, and the glyph is not part of it.
  | 'more'
  // The menu's Extra heading, and an extra that names no icon of its own
  // (`Extension.icon`) or one the sheet does not define.
  | 'extra'
  // Log out, in the account menu and at the rail's foot.
  | 'logout'
  // A confirmation is pinned to what it was read at.
  | 'lock'
  // A kind marked as a favorite in the kinds column, which is drawn whatever
  // it holds. The outline offers the mark; the filled star is the mark. Its
  // own `fill` on the shape wins over the sheet's `fill-none`.
  | 'star' | 'starred'
  // A triangle with a mark: something could not be read in full.
  | 'warning'
  // One per shipped extra, drawn on its card and in its About window
  // (`Extension.icon`). An extra that names none draws `extra`.
  | 'certificate' | 'delivery' | 'database' | 'globe' | 'pulse' | 'coffee'
  | 'sliders' | 'clock' | 'topology' | 'hourglass' | 'layers' | 'shield'
  // About an extra: a circled i.
  | 'info'
  // What's new: a release's Added and Fixed entries (#107).
  | 'plus' | 'check'
  // A debug container (#147): the drawer's Debug control, its Start, a debug
  // tab's mark and an ephemeral container's badge. A bug, which is what a
  // debugger is drawn as everywhere else, so it is recognized, not learned.
  | 'debug';

/**
 * The sheet itself. Mounted **once**, by the Shell, above everything that draws
 * an icon.
 *
 * `<use href="#i-...">` resolves against the document, so the sheet has to be in
 * it - which is also the failure mode to know: an `<Icon>` rendered in a tree
 * that does not carry this component draws nothing at all, with no error. The
 * test pins that the Shell mounts it.
 *
 * `hidden` is the Tailwind utility, `display: none`: the sheet defines
 * shapes at the top of the console. Not React's `hidden` **attribute**, which
 * `SVGProps` does not carry and which does not compile here.
 */
export function IconSprite(): ReactNode {
  return (
    <svg className="hidden" aria-hidden="true" focusable="false" xmlns="http://www.w3.org/2000/svg">
      <defs>
      <symbol id="i-terminal" viewBox="0 0 24 24">
        <rect x="2.5" y="4" width="19" height="16" rx="2.5"/>
        <polyline points="7,9.5 10.5,12.5 7,15.5"/>
        <line x1="13" y1="16" x2="17.5" y2="16"/>
      </symbol>

      <symbol id="i-settings" viewBox="0 0 24 24">
        <circle cx="12" cy="12" r="3.2"/>
        <path d="M19.4 14.5a1.6 1.6 0 0 0 .32 1.77l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.6 1.6 0 0 0-1.77-.32 1.6 1.6 0 0 0-1 1.47V21a2 2 0 1 1-4 0v-.1A1.6 1.6 0 0 0 9 19.4a1.6 1.6 0 0 0-1.77.32l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.6 1.6 0 0 0 4.6 15a1.6 1.6 0 0 0-1.47-1H3a2 2 0 1 1 0-4h.1A1.6 1.6 0 0 0 4.6 9a1.6 1.6 0 0 0-.32-1.77l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.6 1.6 0 0 0 9 4.6 1.6 1.6 0 0 0 10 3.13V3a2 2 0 1 1 4 0v.1A1.6 1.6 0 0 0 15 4.6a1.6 1.6 0 0 0 1.77-.32l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.6 1.6 0 0 0 19.4 9v.09a1.6 1.6 0 0 0 1.47 1H21a2 2 0 1 1 0 4h-.1a1.6 1.6 0 0 0-1.5 1z"/>
      </symbol>

      <symbol id="i-logs" viewBox="0 0 24 24">
        <line x1="4" y1="7" x2="20" y2="7"/>
        <line x1="4" y1="12" x2="16" y2="12"/>
        <line x1="4" y1="17" x2="19" y2="17"/>
      </symbol>

      <symbol id="i-close" viewBox="0 0 24 24">
        <line x1="6" y1="6" x2="18" y2="18"/>
        <line x1="18" y1="6" x2="6" y2="18"/>
      </symbol>

      <symbol id="i-forward" viewBox="0 0 24 24">
        <line x1="9" y1="2.5" x2="9" y2="7"/>
        <line x1="15" y1="2.5" x2="15" y2="7"/>
        <path d="M6 7h12v4a6 6 0 0 1-6 6 6 6 0 0 1-6-6z"/>
        <line x1="12" y1="17" x2="12" y2="21.5"/>
      </symbol>

      <symbol id="i-stop" viewBox="0 0 24 24">
        <rect x="6.5" y="6.5" width="11" height="11" rx="1.5"/>
      </symbol>

      <symbol id="i-copy" viewBox="0 0 24 24">
        <rect x="9" y="9" width="12" height="12" rx="2"/>
        <path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/>
      </symbol>

      <symbol id="i-external" viewBox="0 0 24 24">
        <path d="M18 13.5V19a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h5.5"/>
        <polyline points="14.5,3 21,3 21,9.5"/>
        <line x1="10" y1="14" x2="21" y2="3"/>
      </symbol>

      <symbol id="i-claude" viewBox="0 0 248 248">
        <path fill={CLAUDE_MARK} stroke="none" d="M52.4285 162.873L98.7844 136.879L99.5485 134.602L98.7844 133.334H96.4921L88.7237 132.862L62.2346 132.153L39.3113 131.207L17.0249 130.026L11.4214 128.844L6.2 121.873L6.7094 118.447L11.4214 115.257L18.171 115.847L33.0711 116.911L55.485 118.447L71.6586 119.392L95.728 121.873H99.5485L100.058 120.337L98.7844 119.392L97.7656 118.447L74.5877 102.732L49.4995 86.1905L36.3823 76.62L29.3779 71.7757L25.8121 67.2858L24.2839 57.3608L30.6515 50.2716L39.3113 50.8623L41.4763 51.4531L50.2636 58.1879L68.9842 72.7209L93.4357 90.6804L97.0015 93.6343L98.4374 92.6652L98.6571 91.9801L97.0015 89.2625L83.757 65.2772L69.621 40.8192L63.2534 30.6579L61.5978 24.632C60.9565 22.1032 60.579 20.0111 60.579 17.4246L67.8381 7.49965L71.9133 6.19995L81.7193 7.49965L85.7946 11.0443L91.9074 24.9865L101.714 46.8451L116.996 76.62L121.453 85.4816L123.873 93.6343L124.764 96.1155H126.292V94.6976L127.566 77.9197L129.858 57.3608L132.15 30.8942L132.915 23.4505L136.608 14.4708L143.994 9.62643L149.725 12.344L154.437 19.0788L153.8 23.4505L150.998 41.6463L145.522 70.1215L141.957 89.2625H143.994L146.414 86.7813L156.093 74.0206L172.266 53.698L179.398 45.6635L187.803 36.802L193.152 32.5484H203.34L210.726 43.6549L207.415 55.1159L196.972 68.3492L188.312 79.5739L175.896 96.2095L168.191 109.585L168.882 110.689L170.738 110.53L198.755 104.504L213.91 101.787L231.994 98.7149L240.144 102.496L241.036 106.395L237.852 114.311L218.495 119.037L195.826 123.645L162.07 131.592L161.696 131.893L162.137 132.547L177.36 133.925L183.855 134.279H199.774L229.447 136.524L237.215 141.605L241.8 147.867L241.036 152.711L229.065 158.737L213.019 154.956L175.45 145.977L162.587 142.787H160.805V143.85L171.502 154.366L191.242 172.089L215.82 195.011L217.094 200.682L213.91 205.172L210.599 204.699L188.949 188.394L180.544 181.069L161.696 165.118H160.422V166.772L164.752 173.152L187.803 207.771L188.949 218.405L187.294 221.832L181.308 223.959L174.813 222.777L161.187 203.754L147.305 182.486L136.098 163.345L134.745 164.2L128.075 235.42L125.019 239.082L117.887 241.8L111.902 237.31L108.718 229.984L111.902 215.452L115.722 196.547L118.779 181.541L121.58 162.873L123.291 156.636L123.14 156.219L121.773 156.449L107.699 175.752L86.304 204.699L69.3663 222.777L65.291 224.431L58.2867 220.768L58.9235 214.27L62.8713 208.48L86.304 178.705L100.44 160.155L109.551 149.507L109.462 147.967L108.959 147.924L46.6977 188.512L35.6182 189.93L30.7788 185.44L31.4156 178.115L33.7079 175.752L52.4285 162.873Z"/>
      </symbol>

      <symbol id="i-resource" viewBox="0 0 24 24">
        <path d="M12 2.8l8 4.4v9.6l-8 4.4-8-4.4V7.2z"/>
        <polyline points="4,7.2 12,11.8 20,7.2"/>
        <line x1="12" y1="11.8" x2="12" y2="21.2"/>
      </symbol>

      <symbol id="i-volume" viewBox="0 0 24 24">
        <ellipse cx="12" cy="6" rx="7.5" ry="3"/>
        <path d="M4.5 6v12c0 1.66 3.36 3 7.5 3s7.5-1.34 7.5-3V6"/>
        <path d="M4.5 12c0 1.66 3.36 3 7.5 3s7.5-1.34 7.5-3"/>
      </symbol>

      <symbol id="i-secret" viewBox="0 0 24 24">
        <rect x="4" y="10.5" width="16" height="10" rx="2"/>
        <path d="M8 10.5V7a4 4 0 0 1 8 0v3.5"/>
      </symbol>

      <symbol id="i-config" viewBox="0 0 24 24">
        <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/>
        <polyline points="14,3 14,8 19,8"/>
        <line x1="8.5" y1="13" x2="15.5" y2="13"/>
        <line x1="8.5" y1="16.5" x2="13" y2="16.5"/>
      </symbol>

      <symbol id="i-node" viewBox="0 0 24 24">
        <rect x="2.5" y="4" width="19" height="7" rx="1.5"/>
        <rect x="2.5" y="13" width="19" height="7" rx="1.5"/>
        <line x1="6" y1="7.5" x2="6.01" y2="7.5"/>
        <line x1="6" y1="16.5" x2="6.01" y2="16.5"/>
      </symbol>

      <symbol id="i-refresh" viewBox="0 0 24 24">
        <polyline points="20.5,4 20.5,10 14.5,10"/>
        <path d="M19.4 15a8 8 0 1 1-1.6-8.4L20.5 9"/>
      </symbol>

      <symbol id="i-edit" viewBox="0 0 24 24">
        <path d="M4 20h4l10.5-10.5a2.83 2.83 0 0 0-4-4L4 16z"/>
        <line x1="13.5" y1="6.5" x2="17.5" y2="10.5"/>
      </symbol>

      <symbol id="i-trash" viewBox="0 0 24 24">
        <line x1="3.5" y1="6.5" x2="20.5" y2="6.5"/>
        <path d="M9 6.5V4.5a1.5 1.5 0 0 1 1.5-1.5h3A1.5 1.5 0 0 1 15 4.5v2"/>
        <path d="M6 6.5l1 13a1.5 1.5 0 0 0 1.5 1.4h7a1.5 1.5 0 0 0 1.5-1.4l1-13"/>
      </symbol>

      <symbol id="i-evict" viewBox="0 0 24 24">
        <path d="M13 3.5H6.5A2 2 0 0 0 4.5 5.5v13a2 2 0 0 0 2 2H13"/>
        <line x1="10" y1="12" x2="20.5" y2="12"/>
        <polyline points="17,8.5 20.5,12 17,15.5"/>
      </symbol>

      <symbol id="i-cordon" viewBox="0 0 24 24">
        <rect x="2.5" y="8.5" width="19" height="7" rx="1.5"/>
        <line x1="8" y1="8.5" x2="5" y2="15.5"/>
        <line x1="14" y1="8.5" x2="11" y2="15.5"/>
        <line x1="20" y1="8.5" x2="17" y2="15.5"/>
      </symbol>

      <symbol id="i-uncordon" viewBox="0 0 24 24">
        <rect x="2.5" y="4" width="19" height="5.5" rx="1.5"/>
        <line x1="12" y1="12.5" x2="12" y2="20.5"/>
        <polyline points="8.5,17 12,20.5 15.5,17"/>
      </symbol>

      <symbol id="i-drain" viewBox="0 0 24 24">
        <path d="M3.5 3.5h17l-6.5 8v6l-4 3v-9z"/>
        <line x1="12" y1="21" x2="12" y2="21.01"/>
      </symbol>

      <symbol id="i-pause" viewBox="0 0 24 24">
        <line x1="9" y1="5" x2="9" y2="19"/>
        <line x1="15" y1="5" x2="15" y2="19"/>
      </symbol>

      <symbol id="i-play" viewBox="0 0 24 24">
        <path d="M7.5 4.8l11 7.2-11 7.2z"/>
      </symbol>

      <symbol id="i-trigger" viewBox="0 0 24 24">
        <path d="M5.5 5.2l10 6.8-10 6.8z"/>
        <line x1="19" y1="4.5" x2="19" y2="19.5"/>
      </symbol>

      <symbol id="i-scale" viewBox="0 0 24 24">
        <line x1="12" y1="3.5" x2="12" y2="20.5"/>
        <polyline points="8,7 12,3.5 16,7"/>
        <polyline points="8,17 12,20.5 16,17"/>
      </symbol>

      <symbol id="i-cpu" viewBox="0 0 24 24">
        <rect x="5" y="5" width="14" height="14" rx="2"/>
        <rect x="9.5" y="9.5" width="5" height="5" rx="1"/>
        <line x1="9.5" y1="2.5" x2="9.5" y2="5"/>
        <line x1="14.5" y1="2.5" x2="14.5" y2="5"/>
        <line x1="9.5" y1="19" x2="9.5" y2="21.5"/>
        <line x1="14.5" y1="19" x2="14.5" y2="21.5"/>
        <line x1="2.5" y1="9.5" x2="5" y2="9.5"/>
        <line x1="2.5" y1="14.5" x2="5" y2="14.5"/>
        <line x1="19" y1="9.5" x2="21.5" y2="9.5"/>
        <line x1="19" y1="14.5" x2="21.5" y2="14.5"/>
      </symbol>

      <symbol id="i-memory" viewBox="0 0 24 24">
        <rect x="2.5" y="6" width="19" height="9" rx="1.5"/>
        <line x1="7" y1="9" x2="7" y2="12"/>
        <line x1="12" y1="9" x2="12" y2="12"/>
        <line x1="17" y1="9" x2="17" y2="12"/>
        <line x1="6.5" y1="15" x2="6.5" y2="18"/>
        <line x1="17.5" y1="15" x2="17.5" y2="18"/>
      </symbol>

      <symbol id="i-network" viewBox="0 0 24 24">
        <line x1="8.5" y1="3.5" x2="8.5" y2="20.5"/>
        <polyline points="4.5,16.5 8.5,20.5 12.5,16.5"/>
        <line x1="15.5" y1="20.5" x2="15.5" y2="3.5"/>
        <polyline points="11.5,7.5 15.5,3.5 19.5,7.5"/>
      </symbol>

      <symbol id="i-disk" viewBox="0 0 24 24">
        <ellipse cx="12" cy="6" rx="7.5" ry="3"/>
        <path d="M4.5 6v6c0 1.66 3.36 3 7.5 3s7.5-1.34 7.5-3V6"/>
        <path d="M4.5 12v6c0 1.66 3.36 3 7.5 3s7.5-1.34 7.5-3v-6"/>
      </symbol>

      {/* Drawn pointing right, which is the shut state. The menu rotates it a
          quarter turn when the group is open rather than swapping the glyph, so
          there is one shape and the turn is what an eye follows. */}
      <symbol id="i-chevron" viewBox="0 0 24 24">
        <polyline points="9,5 16,12 9,19"/>
      </symbol>

      <symbol id="i-back" viewBox="0 0 24 24">
        <line x1="19" y1="12" x2="5" y2="12"/>
        <polyline points="11,6 5,12 11,18"/>
      </symbol>

      {/* --- the kinds menu -----------------------------------------------
          One shape per concept rather than one per kind: `replicasets` and
          `replicationcontrollers` are the same idea spelt two years apart, and
          two hand-drawn variants of "several copies of a thing" would differ by
          accident rather than by meaning. `shell/kindGlyph.ts` is the map, and
          it is where a kind is answered - this file only owes it shapes.
          Appended as a block so the sheet above is untouched. */}

      <symbol id="i-pod" viewBox="0 0 24 24">
        <rect x="3.5" y="3.5" width="17" height="17" rx="4"/>
        <circle cx="12" cy="12" r="3.5"/>
      </symbol>

      <symbol id="i-deployment" viewBox="0 0 24 24">
        <path d="M12 2.8l8 4.2-8 4.2-8-4.2z"/>
        <polyline points="4,12 12,16.2 20,12"/>
        <polyline points="4,16.5 12,20.7 20,16.5"/>
      </symbol>

      {/* Several copies, side by side, standing on nothing. */}
      <symbol id="i-replicaset" viewBox="0 0 24 24">
        <rect x="2.5" y="7" width="5.5" height="10" rx="1.5"/>
        <rect x="9.25" y="7" width="5.5" height="10" rx="1.5"/>
        <rect x="16" y="7" width="5.5" height="10" rx="1.5"/>
      </symbol>

      {/* The same copies on a foundation - the identity and the volume that
          survive a restart, which is the whole of what makes it stateful. */}
      <symbol id="i-statefulset" viewBox="0 0 24 24">
        <rect x="2.5" y="4.5" width="5.5" height="9" rx="1.5"/>
        <rect x="9.25" y="4.5" width="5.5" height="9" rx="1.5"/>
        <rect x="16" y="4.5" width="5.5" height="9" rx="1.5"/>
        <line x1="2.5" y1="18.5" x2="21.5" y2="18.5"/>
      </symbol>

      {/* One on every node, which is the rule rather than a count. */}
      <symbol id="i-daemonset" viewBox="0 0 24 24">
        <rect x="3.5" y="3.5" width="5" height="5" rx="1"/>
        <rect x="9.5" y="3.5" width="5" height="5" rx="1"/>
        <rect x="15.5" y="3.5" width="5" height="5" rx="1"/>
        <rect x="2.5" y="13" width="19" height="7" rx="1.5"/>
      </symbol>

      <symbol id="i-job" viewBox="0 0 24 24">
        <rect x="3.5" y="3.5" width="17" height="17" rx="3"/>
        <polyline points="8,12.2 11,15.2 16.5,9.5"/>
      </symbol>

      <symbol id="i-cronjob" viewBox="0 0 24 24">
        <circle cx="12" cy="12" r="8.5"/>
        <polyline points="12,7 12,12 15.5,14"/>
      </symbol>

      {/* One address in front of several backends. */}
      <symbol id="i-service" viewBox="0 0 24 24">
        <circle cx="12" cy="5.5" r="2.5"/>
        <circle cx="5.5" cy="18.5" r="2.5"/>
        <circle cx="18.5" cy="18.5" r="2.5"/>
        <line x1="12" y1="8" x2="12" y2="11"/>
        <path d="M5.5 16V13h13v3"/>
      </symbol>

      {/* An arrow coming in from outside the cluster, which is the mirror of
          `evict` and reads as one beside it. */}
      <symbol id="i-ingress" viewBox="0 0 24 24">
        <path d="M13.5 3.5H19a1.5 1.5 0 0 1 1.5 1.5v14a1.5 1.5 0 0 1-1.5 1.5h-5.5"/>
        <line x1="3.5" y1="12" x2="14" y2="12"/>
        <polyline points="10.5,8.5 14,12 10.5,15.5"/>
      </symbol>

      <symbol id="i-endpoint" viewBox="0 0 24 24">
        <circle cx="5.5" cy="12" r="2.5"/>
        <circle cx="18.5" cy="12" r="2.5"/>
        <line x1="8" y1="12" x2="16" y2="12"/>
      </symbol>

      {/* A label put on a thing by somebody else - an ingress class, a storage
          class, a priority class. */}
      <symbol id="i-class" viewBox="0 0 24 24">
        <path d="M3.5 10.5V4.5a1 1 0 0 1 1-1h6l9.2 9.2a1.5 1.5 0 0 1 0 2.1l-5.4 5.4a1.5 1.5 0 0 1-2.1 0z"/>
        <line x1="7.5" y1="7.5" x2="7.51" y2="7.5"/>
      </symbol>

      {/* A rule that refuses something: a network policy, a disruption budget. */}
      <symbol id="i-policy" viewBox="0 0 24 24">
        <path d="M12 2.8l7.5 3v6c0 4.4-3.1 8.4-7.5 9.4-4.4-1-7.5-5-7.5-9.4v-6z"/>
      </symbol>

      {/* The same shield with a keyhole - who may do it, rather than what may
          be done. It heads Access Control and nothing under it. */}
      <symbol id="i-access" viewBox="0 0 24 24">
        <path d="M12 2.8l7.5 3v6c0 4.4-3.1 8.4-7.5 9.4-4.4-1-7.5-5-7.5-9.4v-6z"/>
        <circle cx="12" cy="10.8" r="1.8"/>
        <line x1="12" y1="12.6" x2="12" y2="15.4"/>
      </symbol>

      {/* A ceiling on consumption: a quota, a limit range. */}
      <symbol id="i-quota" viewBox="0 0 24 24">
        <path d="M3.8 17.5a8.5 8.5 0 1 1 16.4 0"/>
        <line x1="12" y1="17.5" x2="16.2" y2="11.8"/>
      </symbol>

      {/* A claim planted for a while and then given up. */}
      <symbol id="i-lease" viewBox="0 0 24 24">
        <line x1="5.5" y1="3" x2="5.5" y2="21"/>
        <path d="M5.5 4.5h11l-2.5 4 2.5 4h-11z"/>
      </symbol>

      <symbol id="i-webhook" viewBox="0 0 24 24">
        <circle cx="8" cy="6.5" r="3"/>
        <path d="M8 9.5v5.5a3.5 3.5 0 0 0 3.5 3.5h4.5"/>
        <polyline points="13.5,15.5 16.5,18.5 13.5,21.5"/>
      </symbol>

      <symbol id="i-account" viewBox="0 0 24 24">
        <circle cx="12" cy="8" r="3.8"/>
        <path d="M4.5 20.5a7.5 7.5 0 0 1 15 0"/>
      </symbol>

      <symbol id="i-role" viewBox="0 0 24 24">
        <circle cx="7.5" cy="12" r="3.5"/>
        <line x1="11" y1="12" x2="20.5" y2="12"/>
        <line x1="17.5" y1="12" x2="17.5" y2="15.5"/>
        <line x1="20.5" y1="12" x2="20.5" y2="16"/>
      </symbol>

      <symbol id="i-binding" viewBox="0 0 24 24">
        <path d="M10.2 13.8a4 4 0 0 1 0-5.6l2.6-2.6a4 4 0 0 1 5.6 5.6l-1.3 1.3"/>
        <path d="M13.8 10.2a4 4 0 0 1 0 5.6l-2.6 2.6a4 4 0 0 1-5.6-5.6l1.3-1.3"/>
      </symbol>

      <symbol id="i-namespace" viewBox="0 0 24 24">
        <path d="M8 3.5H6a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2h2"/>
        <path d="M16 3.5h2a2 2 0 0 1 2 2v13a2 2 0 0 1-2 2h-2"/>
        <circle cx="12" cy="12" r="2.5"/>
      </symbol>

      <symbol id="i-event" viewBox="0 0 24 24">
        <path d="M18 9.5a6 6 0 1 0-12 0c0 5-2 6.3-2 6.3h16s-2-1.3-2-6.3"/>
        <path d="M13.7 19.5a2 2 0 0 1-3.4 0"/>
      </symbol>

      {/* The group heads. Deliberately not the glyph of any kind inside them:
          a heading that draws its first row's shape reads as that row. */}
      <symbol id="i-workloads" viewBox="0 0 24 24">
        <rect x="3.5" y="3.5" width="7" height="7" rx="1.5"/>
        <rect x="13.5" y="3.5" width="7" height="7" rx="1.5"/>
        <rect x="3.5" y="13.5" width="7" height="7" rx="1.5"/>
        <rect x="13.5" y="13.5" width="7" height="7" rx="1.5"/>
      </symbol>

      <symbol id="i-cluster" viewBox="0 0 24 24">
        <circle cx="12" cy="12" r="8.5"/>
        <line x1="3.5" y1="12" x2="20.5" y2="12"/>
        <path d="M12 3.5a13 13 0 0 1 0 17 13 13 0 0 1 0-17"/>
      </symbol>

      {/* Four panels: the dashboard, which is what the page is. */}
      <symbol id="i-overview" viewBox="0 0 24 24">
        <rect x="3.5" y="3.5" width="7" height="7" rx="1.5"/>
        <rect x="13.5" y="3.5" width="7" height="7" rx="1.5"/>
        <rect x="3.5" y="13.5" width="7" height="7" rx="1.5"/>
        <rect x="13.5" y="13.5" width="7" height="7" rx="1.5"/>
      </symbol>

      <symbol id="i-search" viewBox="0 0 24 24">
        <circle cx="10.5" cy="10.5" r="6.5"/>
        <line x1="15.2" y1="15.2" x2="20.5" y2="20.5"/>
      </symbol>

      <symbol id="i-filter" viewBox="0 0 24 24">
        <path d="M3.5 5h17l-6.5 7.5v6l-4 2v-8z"/>
      </symbol>

      <symbol id="i-more" viewBox="0 0 24 24">
        <circle cx="5" cy="12" r="1"/>
        <circle cx="12" cy="12" r="1"/>
        <circle cx="19" cy="12" r="1"/>
      </symbol>

      {/* A plug seen face on - `kt-extra` from the cluster-icon set
          (`iconStyles.ts`), redrawn on this grid as a stroke. What an extra is:
          something plugged into the console, and not one more kind of resource,
          whose cube this must not echo. It heads the Extra group and draws
          every extra's entry: one common shape, since the contract an extra
          compiles against carries no icon and gains none.

          Two departures from `kt-extra`, both for 15px: the body tapers to the
          lead and the lead curls away, because a square face with two slots
          and a straight stem reads as a speech bubble at that size. And it is
          seen face on, never prongs up: prongs up is this sheet's `forward`,
          the port-forward glyph, and two shapes that differ only by an angle
          read as one. */}
      <symbol id="i-extra" viewBox="0 0 24 24">
        <path d="M6 3h12a1.5 1.5 0 0 1 1.5 1.5v5.5a5 5 0 0 1-5 5h-5a5 5 0 0 1-5-5V4.5A1.5 1.5 0 0 1 6 3z"/>
        <line x1="9.5" y1="7" x2="9.5" y2="10"/>
        <line x1="14.5" y1="7" x2="14.5" y2="10"/>
        <path d="M12 15v2.5a3.5 3.5 0 0 0 3.5 3.5H20"/>
      </symbol>

      {/* Log out. A door standing open on its frame, its knob on the leaf, and
          the way out leaving past it. Not `evict`, which is the same arrow
          leaving a bracket: that is something done *to a pod*, in the same
          menus as this, and two acts drawn one shape apart read as one act.
          Not `external` either: an arrow out of a box says *opens somewhere
          else*, which logging out does not. */}
      <symbol id="i-logout" viewBox="0 0 24 24">
        <path d="M10 3.5H5.5a1 1 0 0 0-1 1v15a1 1 0 0 0 1 1H10"/>
        <path d="M10 3.5l5 1.6v13.8l-5 1.6z"/>
        <line x1="12.5" y1="12" x2="12.51" y2="12"/>
        <line x1="17" y1="12" x2="21.5" y2="12"/>
        <polyline points="19.5,9.5 21.5,12 19.5,14.5"/>
      </symbol>

      {/* The pin on a confirmation - the act is refused
          rather than applied to an object that moved since it was read. A
          padlock rather than words, which would be plumbing read on every
          delete. A padlock with its keyhole, so it is not
          `secret`'s bare padlock: that one is a *kind* in the menu, and two
          meanings drawn one shape apart read as one. */}
      <symbol id="i-lock" viewBox="0 0 24 24">
        <rect x="5" y="11" width="14" height="10" rx="2"/>
        <path d="M8 11V7.5a4 4 0 0 1 8 0V11"/>
        <line x1="12" y1="15" x2="12" y2="17"/>
      </symbol>

      {/* a favorite kind. One shape, outlined to offer
          the mark and filled once it is made. */}
      <symbol id="i-star" viewBox="0 0 24 24">
        <polygon points="12,3 14.8,8.8 21,9.6 16.5,14 17.6,20.2 12,17.3 6.4,20.2 7.5,14 3,9.6 9.2,8.8"/>
      </symbol>

      <symbol id="i-starred" viewBox="0 0 24 24">
        <polygon points="12,3 14.8,8.8 21,9.6 16.5,14 17.6,20.2 12,17.3 6.4,20.2 7.5,14 3,9.6 9.2,8.8" fill="currentColor"/>
      </symbol>

      <symbol id="i-warning" viewBox="0 0 24 24">
        <path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/>
        <line x1="12" y1="9" x2="12" y2="13"/>
        <line x1="12" y1="17" x2="12.01" y2="17"/>
      </symbol>
      <symbol id="i-certificate" viewBox="0 0 24 24">
        <path d="M13 17H5a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2v4"/>
        <line x1="7" y1="8.5" x2="15" y2="8.5"/>
        <line x1="7" y1="12" x2="11" y2="12"/>
        <circle cx="17.5" cy="14" r="2.8"/>
        <polyline points="16,16.5 15.5,21 17.5,19.8 19.5,21 19,16.5"/>
      </symbol>

      <symbol id="i-delivery" viewBox="0 0 24 24">
        <path d="M20 11a8 8 0 0 0-14.3-4.9"/>
        <polyline points="5,2.5 5.5,6.5 9.5,6"/>
        <path d="M4 13a8 8 0 0 0 14.3 4.9"/>
        <polyline points="19,21.5 18.5,17.5 14.5,18"/>
      </symbol>

      <symbol id="i-database" viewBox="0 0 24 24">
        <ellipse cx="12" cy="5.5" rx="7.5" ry="2.5"/>
        <path d="M4.5 5.5v13c0 1.38 3.36 2.5 7.5 2.5s7.5-1.12 7.5-2.5v-13"/>
        <path d="M4.5 9.8c0 1.38 3.36 2.5 7.5 2.5s7.5-1.12 7.5-2.5"/>
        <path d="M4.5 14.2c0 1.38 3.36 2.5 7.5 2.5s7.5-1.12 7.5-2.5"/>
      </symbol>

      <symbol id="i-globe" viewBox="0 0 24 24">
        <circle cx="12" cy="12" r="9"/>
        <line x1="3" y1="12" x2="21" y2="12"/>
        <path d="M12 3c2.5 2.5 3.8 5.5 3.8 9s-1.3 6.5-3.8 9c-2.5-2.5-3.8-5.5-3.8-9S9.5 5.5 12 3z"/>
      </symbol>

      <symbol id="i-pulse" viewBox="0 0 24 24">
        <polyline points="2,12 6.5,12 9.5,5 14,19 17,12 22,12"/>
      </symbol>

      <symbol id="i-coffee" viewBox="0 0 24 24">
        <path d="M4 9h12v4.5a5.5 5.5 0 0 1-5.5 5.5h-1A5.5 5.5 0 0 1 4 13.5z"/>
        <path d="M16 10.5h1.5a2.5 2.5 0 0 1 0 5H16"/>
        <path d="M8.5 3c-.9 1 .9 2.2 0 3.5"/>
        <path d="M12 3c-.9 1 .9 2.2 0 3.5"/>
        <line x1="3" y1="21.5" x2="18" y2="21.5"/>
      </symbol>

      <symbol id="i-sliders" viewBox="0 0 24 24">
        <line x1="6" y1="3" x2="6" y2="12.5"/>
        <line x1="6" y1="16.5" x2="6" y2="21"/>
        <circle cx="6" cy="14.5" r="2"/>
        <line x1="12" y1="3" x2="12" y2="6.5"/>
        <line x1="12" y1="10.5" x2="12" y2="21"/>
        <circle cx="12" cy="8.5" r="2"/>
        <line x1="18" y1="3" x2="18" y2="14.5"/>
        <line x1="18" y1="18.5" x2="18" y2="21"/>
        <circle cx="18" cy="16.5" r="2"/>
      </symbol>

      <symbol id="i-clock" viewBox="0 0 24 24">
        <circle cx="12" cy="12" r="9"/>
        <polyline points="12,7 12,12 15.5,14.2"/>
      </symbol>

      <symbol id="i-topology" viewBox="0 0 24 24">
        <circle cx="5.5" cy="6" r="2.3"/>
        <circle cx="18.5" cy="6" r="2.3"/>
        <circle cx="12" cy="18" r="2.3"/>
        <line x1="7.8" y1="6" x2="16.2" y2="6"/>
        <line x1="6.6" y1="8" x2="10.9" y2="16"/>
        <line x1="17.4" y1="8" x2="13.1" y2="16"/>
      </symbol>

      <symbol id="i-hourglass" viewBox="0 0 24 24">
        <line x1="5.5" y1="3" x2="18.5" y2="3"/>
        <line x1="5.5" y1="21" x2="18.5" y2="21"/>
        <path d="M7 3v3.5l5 5.5 5-5.5V3"/>
        <path d="M7 21v-3.5l5-5.5 5 5.5V21"/>
      </symbol>

      <symbol id="i-layers" viewBox="0 0 24 24">
        <path d="M12 3l9 4.5-9 4.5-9-4.5z"/>
        <polyline points="3,12 12,16.5 21,12"/>
        <polyline points="3,16.5 12,21 21,16.5"/>
      </symbol>

      <symbol id="i-shield" viewBox="0 0 24 24">
        <path d="M12 2.8l7.5 2.7v6c0 4.6-3.2 8.2-7.5 9.7-4.3-1.5-7.5-5.1-7.5-9.7v-6z"/>
        <polyline points="8.8,12 11.2,14.4 15.4,9.8"/>
      </symbol>

      <symbol id="i-info" viewBox="0 0 24 24">
        <circle cx="12" cy="12" r="9"/>
        <line x1="12" y1="11" x2="12" y2="16.5"/>
        <line x1="12" y1="7.5" x2="12.01" y2="7.5"/>
      </symbol>

      <symbol id="i-plus" viewBox="0 0 24 24">
        <line x1="12" y1="5" x2="12" y2="19"/>
        <line x1="5" y1="12" x2="19" y2="12"/>
      </symbol>

      <symbol id="i-check" viewBox="0 0 24 24">
        <polyline points="5,12.5 10,17.5 19,7"/>
      </symbol>

      <symbol id="i-debug" viewBox="0 0 24 24">
        <path d="M8 6.5a4 4 0 0 1 8 0"/>
        <rect x="7" y="7" width="10" height="12" rx="5"/>
        <line x1="12" y1="11" x2="12" y2="19"/>
        <line x1="3" y1="13" x2="7" y2="13"/>
        <line x1="17" y1="13" x2="21" y2="13"/>
        <line x1="4" y1="7" x2="7" y2="9"/>
        <line x1="20" y1="7" x2="17" y2="9"/>
        <line x1="4" y1="19" x2="7" y2="17"/>
        <line x1="20" y1="19" x2="17" y2="17"/>
      </symbol>
      </defs>
    </svg>
  );
}

/**
 * One glyph, by name.
 *
 * `aria-hidden` and `focusable="false"` on every one of them: the label belongs
 * to the control, and a focusable SVG is an extra tab stop in Internet
 * Explorer's descendants that leads nowhere.
 *
 * `className` is appended rather than replacing, so a caller can size one
 * differently without losing the stroke rules - and cannot accidentally drop
 * them by passing a class of their own.
 */
/** The names the sheet defines, as its `<symbol>`s do (`Icon.test.tsx` holds
 *  the two together). */
const DEFINED: ReadonlySet<string> = new Set([
  'terminal',
  'settings',
  'logs',
  'close',
  'forward',
  'stop',
  'copy',
  'external',
  'claude',
  'resource',
  'volume',
  'secret',
  'config',
  'node',
  'refresh',
  'edit',
  'trash',
  'evict',
  'cordon',
  'uncordon',
  'drain',
  'pause',
  'play',
  'trigger',
  'scale',
  'cpu',
  'memory',
  'network',
  'disk',
  'chevron',
  'back',
  'pod',
  'deployment',
  'replicaset',
  'statefulset',
  'daemonset',
  'job',
  'cronjob',
  'service',
  'ingress',
  'endpoint',
  'class',
  'policy',
  'access',
  'quota',
  'lease',
  'webhook',
  'account',
  'role',
  'binding',
  'namespace',
  'event',
  'workloads',
  'cluster',
  'overview',
  'search',
  'filter',
  'more',
  'extra',
  'logout',
  'lock',
  'star',
  'starred',
  'warning',
  'certificate',
  'delivery',
  'database',
  'globe',
  'pulse',
  'coffee',
  'sliders',
  'clock',
  'topology',
  'hourglass',
  'layers',
  'shield',
  'info',
  'plus',
  'check',
  'debug',
])

export function Icon({ name, className = '' }: { name: IconName; className?: string }): ReactNode {
  // A name the sheet lacks (a plain-JS extension, a cast past IconName) draws
  // the generic extra glyph rather than an empty box.
  const drawn = DEFINED.has(name) ? name : 'extra';
  return (
    <svg
      className={`size-[var(--icon)] shrink-0 fill-none stroke-current align-[-2px] [stroke-linecap:round] [stroke-linejoin:round] [stroke-width:2] ${className}`} // style-allow: a 2px baseline nudge, no step on the scale
      aria-hidden="true"
      focusable="false"
    >
      <use href={`#i-${drawn}`} />
    </svg>
  );
}
