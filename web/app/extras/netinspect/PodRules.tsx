import { useState, type ReactNode } from 'react';
import { focusRing } from '@kubetower/ui';

import type { Direction, RuleKind } from './data';
import { MAX_ROWS, SCROLL_ROWS, filterFlows, peerName, portsLabel, type Flow, type Side } from './flows';
import { firstN, restTitle, shortLabel } from './text';

/** A rule's box: an allow dashed in the accent, a deny in the refusal
 *  color, one not evaluated dotted and faint. */
const RULE_LOOK: Record<RuleKind, string> = {
  allow: 'border-dashed border-accent/50',
  deny: 'border-dashed border-bad/60',
  pass: 'border-dashed border-line text-muted',
  unevaluated: 'border-dotted border-line text-muted',
};

/** Chips shown before a `+N`. */
const MAX_CHIPS = 3;
/** The longest a chip's text runs before its middle is cut. */
const CHIP_CHARS = 44;
/** The longest a row's peer runs before its middle is cut. */
const PEER_CHARS = 38;
const CHIP = 'max-w-full rounded-full border px-1.5 py-px text-2xs whitespace-nowrap';

/** What the arrows say about this direction's rows. */
export interface RowsLit {
  /** Rows an arrow or a row is lighting. */
  ids: ReadonlySet<string>;
  /** The others are dimmed. */
  dim: boolean;
  /** A pressed arrow: only its rows are listed, and its name. */
  filter: { ids: ReadonlySet<string>; name: string } | null;
  /** An arrow is pressed: its rows are all listed. */
  pinned: boolean;
}

/**
 * One direction as aligned rows, one per rule and peer: who, on which ports,
 * and under which policy and rule. Long lists show eight rows and a
 * `Show all`; past eight there is a filter; a very long one scrolls inside a
 * bounded height.
 */
export function DirectionBox({
  title,
  side,
  direction,
  hostNetwork,
  flows,
  lit,
  onRow,
  onClearFilter,
}: {
  title: string;
  side: Side;
  direction: Direction;
  hostNetwork: boolean;
  flows: readonly Flow[];
  lit: RowsLit;
  onRow: (id: string | null) => void;
  onClearFilter: () => void;
}): ReactNode {
  const [query, setQuery] = useState('');
  const [all, setAll] = useState(false);
  const base = lit.filter === null ? flows : flows.filter((f) => lit.filter?.ids.has(f.id) === true);
  const matching = filterFlows(base, query);
  const expanded = all || lit.pinned;
  const shown = expanded ? matching : matching.slice(0, MAX_ROWS);
  const scrolls = expanded && matching.length > SCROLL_ROWS;
  return (
    <section data-direction={side} className="flex min-w-0 flex-col gap-1.5 rounded-lg border border-line bg-panel p-3 text-xs">
      <div className="flex items-center gap-2">
        <h3 className="text-sm font-medium">{title}</h3>
        {lit.filter === null ? null : (
          <button type="button" data-clear-filter="" onClick={onClearFilter} title={`${lit.filter.name}\nShow every rule`} className={`${CHIP} flex min-w-0 items-center gap-1 border-accent/50 text-fg ${focusRing}`}>
            <span className="truncate">{shortLabel(lit.filter.name, 28)}</span>
            <span aria-hidden="true">×</span>
          </button>
        )}
        {lit.dim && lit.ids.size > 0 && lit.filter === null ? (
          <span data-match-count="" className="text-2xs text-muted">
            {lit.ids.size} of {flows.length}
          </span>
        ) : null}
        {flows.length > MAX_ROWS ? (
          <input
            type="search"
            value={query}
            placeholder="Filter"
            aria-label={`Filter ${title.toLowerCase()} rules`}
            data-filter=""
            onChange={(e) => setQuery(e.target.value)}
            className={`ml-auto w-32 rounded-md border border-line bg-panel-2 px-2 py-0.5 text-xs text-fg placeholder:text-muted ${focusRing}`}
          />
        ) : null}
      </div>
      {hostNetwork ? (
        <p className="text-muted">host network</p>
      ) : !direction.isolated ? (
        <p className="text-muted">open</p>
      ) : (
        <>
          <p className="flex flex-wrap items-center gap-1 text-muted">
            <span>isolated</span>
            <span aria-hidden="true">·</span>
            <Chips items={direction.policies.map((p) => p.name)} n={MAX_CHIPS} />
            {direction.rules.length === 0 ? <span className="text-bad"> · nothing allowed</span> : null}
          </p>
          <div data-rows="" data-scroll={scrolls ? '' : undefined} className={scrolls ? 'max-h-96 overflow-y-auto [scrollbar-gutter:stable]' : undefined}>
            <ul className="flex flex-col gap-1">
              {shown.map((f) => (
                <Row key={f.id} flow={f} state={lit.ids.has(f.id) ? 'on' : lit.dim && lit.ids.size > 0 ? 'dim' : undefined} onRow={onRow} />
              ))}
              {matching.length === 0 && flows.length > 0 ? (
                <li data-no-match="" className="px-2 py-1 text-muted">
                  no match
                </li>
              ) : null}
            </ul>
          </div>
          {matching.length > MAX_ROWS && !lit.pinned ? (
            <button type="button" data-show-all="" aria-expanded={all} onClick={() => setAll(!all)} className={`self-start rounded-md px-1 text-accent underline ${focusRing}`}>
              {all ? 'Show fewer' : `Show all ${String(matching.length)}`}
            </button>
          ) : null}
        </>
      )}
    </section>
  );
}

function Row({ flow: f, state, onRow }: { flow: Flow; state: 'on' | 'dim' | undefined; onRow: (id: string | null) => void }): ReactNode {
  const name = peerName(f.text, PEER_CHARS);
  const whole = f.text.ns === '' ? f.text.selector : `${f.text.selector} · ${f.text.ns}`;
  const kind = f.kind === 'allow' ? '' : `${f.kind === 'unevaluated' ? 'not evaluated' : f.kind} · `;
  const policy = `${kind}${f.policy} · rule ${String(f.ruleNo)}`;
  return (
    <li
      data-rule={`${f.policy}/${String(f.ruleNo)}`}
      data-flow={f.id}
      data-kind={f.kind}
      data-highlight={state}
      onMouseEnter={() => onRow(f.id)}
      onMouseLeave={() => onRow(null)}
      className={`grid grid-cols-[minmax(0,5fr)_minmax(0,3fr)_minmax(0,4fr)] items-baseline gap-x-2 rounded-md border px-2 py-1 ${RULE_LOOK[f.kind]} ${state === 'on' ? 'bg-accent/15' : ''} ${state === 'dim' ? 'opacity-35' : ''}`}
    >
      <span className="flex min-w-0 flex-col">
        <span className={`truncate ${f.kind === 'allow' || f.kind === 'deny' ? 'text-fg' : ''}`} title={whole === f.peer.label ? whole : `${whole}\n${f.peer.label}`} data-peer="">
          {name}
        </span>
        {f.text.sub === '' ? null : <span className="truncate text-2xs text-muted">{f.text.sub}</span>}
      </span>
      <span className="min-w-0 truncate" title={f.ports.join(', ')} data-ports="">
        {portsLabel(f.ports, 3)}
      </span>
      <span className="min-w-0 truncate text-right text-2xs text-muted" title={policy} data-policy="">
        {policy}
      </span>
    </li>
  );
}

/**
 * Names as chips that wrap: the first `n`, each cut in the middle when long
 * (its whole text in the tooltip), then a `+N` whose tooltip lists the rest.
 */
function Chips({ items, n }: { items: readonly string[]; n: number }): ReactNode {
  const { shown, rest } = firstN(items, n);
  return (
    <>
      {shown.map((text, i) => (
        <span key={`${String(i)}-${text}`} data-chip-text="" className={`${CHIP} border-line text-fg`} title={text}>
          {shortLabel(text, CHIP_CHARS)}
        </span>
      ))}
      {rest.length === 0 ? null : (
        <span data-more="" className={`${CHIP} border-line text-muted`} title={restTitle(rest)}>
          +{rest.length}
        </span>
      )}
    </>
  );
}
