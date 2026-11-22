import type { ReactNode } from 'react';

import { focusRingInside } from '../focus';
import { SettlingMark } from './SettlingMark';
import { toolbarControl } from './toolbar';

/**
 * The filter control.
 *
 * Presentational on purpose: it holds no state and does not touch the address
 * bar. `useUrlParameter` owns that, and owning it in one place is what keeps the
 * three decisions it carries from being re-derived per control.
 *
 * `name` is passed down to the element as well as being the parameter key, so
 * that the rule *the key is the control's own name* is visible in the DOM rather
 * than only in a closure, where a box filtering under another name would go
 * unnoticed.
 *
 * **One named exception to that rule, and it is named here so the DOM is not
 * read as lying.** The list's box is `filter` and writes two keys, `selector`
 * and `search`, from one string: two boxes would make the operator know which
 * layer they were typing at. The address keeps the two keys because every link made before carries
 * them apart; the box is their composition, and `pages/resources/filterText.ts`
 * is the grammar that decomposes it. So for that box `name` is the control's
 * name and the keys are the module's — one control, two parameters, and the
 * place the second is decided is one file rather than a closure.
 *
 * **What this control does next needs feedback of its own.** A React
 * re-render of the same component paints nothing the eye catches. That is why
 * the page draws a *pending* marker while a typed filter is settling rather than
 * leaving the previous answer on screen looking authoritative — and why the
 * check for it is by looking, not by reading a status code.
 *
 * **The previous answer does stay on screen while the next one is read**,
 * and the marker is what makes that honest rather than authoritative: a table
 * that dropped to its waiting state on every settled word would look like a
 * page reloading under the operator's typing. `useResourceList` says which
 * changes keep the rows and which do not.
 */
export interface FilterBoxProps {
  /** The query-string key, and the element's own name. Not defaulted to `q`.
   *  The one box that writes two keys is named `filter` — see above. */
  name: string;
  value: string;
  onChange: (value: string) => void;
  label: string;
  placeholder?: string;
  /** The field's tooltip: the syntaxes it reads, where there are several. */
  title?: string | undefined;
  /** True while the rows on screen are not yet the answer to what is typed:
   *  the value has not reached a request, or the request has not answered and
   *  the previous answer is standing in for it. */
  settling?: boolean;
  /** The control joined to the field's right end, with no gap:
   *  the list's Filter button. The field squares its right corners to meet it. */
  children?: ReactNode;
}

export function FilterBox({
  name,
  value,
  onChange,
  label,
  placeholder,
  title,
  settling = false,
  children,
}: FilterBoxProps): ReactNode {
  return (
    // `min-w-0` here and a floor on the field: in a toolbar narrower than its
    // controls the field is what gives way, down to 96px, rather than the
    // toolbar running on under whatever is beside it (at 1024px with a drawer
    // open, Columns and the `⋯` would be drawn under it).
    <div className="flex min-w-0 items-center gap-2">
      {/* Before the field and not after it: the marker keeps its room (see
          `SettlingMark`), and placed beyond the funnel that room would sit
          between the box and the control joined to it. */}
      <SettlingMark settling={settling} />
      <label htmlFor={`filter-${name}`} className="sr-only">
        {label}
      </label>
      <div className="flex min-w-0 items-center">
        <input
          id={`filter-${name}`}
          name={name}
          type="search"
          autoComplete="off"
          spellCheck={false}
          value={value}
          placeholder={placeholder ?? label}
          title={title}
          onChange={(event) => {
            onChange(event.target.value);
          }}
          // The ring is drawn one pixel INSIDE and the border goes transparent
          // under it, so the field gains a ring rather than a second outline
          // around the one it already has. On a text field `:focus` and
          // `:focus-visible` match together, including on a click.
          className={`${toolbarControl} w-56 min-w-24 justify-start bg-panel ${
            children === undefined ? '' : 'rounded-r-none'
          } ${focusRingInside} focus-visible:border-transparent`}
        />
        {children}
      </div>
    </div>
  );
}
