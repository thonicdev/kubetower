import { useLayoutEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { btnSecondary } from '@kubetower/ui';

import { portsLabel, type Arrow, type Side } from './flows';
import { shortLabel } from './text';

/** What the graph draws for one side. */
export interface GraphSide {
  isolated: boolean;
  arrows: Arrow[];
  /** Isolated with no rule that allows anything. */
  nothing: boolean;
}

/** Which arrows of a side are lit, and which one a click has fixed. */
export interface Lit {
  keys: ReadonlySet<string>;
  /** A highlight comes from an arrow: the others are dimmed. */
  dim: boolean;
  sticky: string | null;
}

/** The small graph of the tab and the enlarged one of the dialog: its width,
 *  the room each side keeps for its labels, what a label holds at 10px before
 *  it is cut, and the least height. */
const SIZES = {
  small: { W: 640, EDGE: 192, CHARS: 30, H: 180, ROW: 0 },
  large: { W: 900, EDGE: 320, CHARS: 54, H: 220, ROW: 30 },
} as const;
const OPEN_KEY = 'open';
/** The narrowest the enlarged graph is drawn: below it the dialog scrolls the graph's frame, never the page. */
const MIN_W = 320;

/**
 * The pod in the middle, what may reach it on the left, what it may reach on
 * the right: dashed arrows, because they are rules, each saying its port and
 * protocol at mid-line. An open direction is one dotted "anyone" with no rule
 * behind it. An arrow is a button: hovering or focusing it lights its rows,
 * pressing it fixes that; a side with more than five peers draws one arrow
 * per namespace.
 */
export function PodGraph({
  ingress,
  egress,
  lit,
  onHover,
  onPick,
  large = false,
  onEnlarge,
}: {
  ingress: GraphSide;
  egress: GraphSide;
  lit: Record<Side, Lit>;
  onHover: (side: Side, key: string | null) => void;
  onPick: (side: Side, key: string) => void;
  /** The dialog's graph: wider, taller with more arrows, labels whole. */
  large?: boolean;
  /** Pressing the graph anywhere but on an arrow opens it enlarged. */
  onEnlarge?: () => void;
}): ReactNode {
  const size = large ? SIZES.large : SIZES.small;
  // The enlarged graph is drawn at the width it is given, one unit a pixel up
  // to its own width, so its text stays at its size in a narrow window
  // instead of shrinking with the drawing; the label room follows.
  const [room, setRoom] = useState<number | null>(null);
  const box = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = box.current;
    if (!large || el === null || typeof ResizeObserver === 'undefined') return;
    const watch = new ResizeObserver(([entry]) => {
      if (entry !== undefined) setRoom(entry.contentRect.width);
    });
    watch.observe(el);
    return () => watch.disconnect();
  }, [large]);
  const W = large && room !== null && room > 0 ? Math.round(Math.min(size.W, Math.max(MIN_W, room))) : size.W;
  const EDGE = Math.round((size.EDGE * W) / size.W);
  const most = Math.max(ingress.arrows.length, egress.arrows.length, 1);
  const H = Math.max(size.H, 44 + most * size.ROW);
  // About 6px a character at 10px, less the gap to the arrow.
  const GRAPH_CHARS = Math.max(10, Math.min(size.CHARS, Math.floor((EDGE - 14) / 6)));
  /** A narrow enlarged graph: arrows too short to carry their ports. */
  const tight = large && W < 600;
  const cx = W / 2;
  const cy = H / 2;
  const y = (i: number, n: number) => (n <= 1 ? cy : 22 + (i * (H - 44)) / (n - 1));
  /** The point on the pod's ring, `r` from its center, facing (x, y): each
   *  line meets the ring where it points, not all at one spot. */
  const rim = (x: number, py: number, r: number) => {
    const angle = Math.atan2(py - cy, x - cx);
    return { x: cx + r * Math.cos(angle), y: cy + r * Math.sin(angle) };
  };
  const one = (side: Side, s: GraphSide, flip: boolean): ReactNode => {
    const arrows: Arrow[] = s.isolated
      ? s.arrows
      : [{ key: OPEN_KEY, name: 'anyone (open)', text: { selector: 'anyone (open)', ns: '', sub: '' }, ports: [], flowIds: [], aggregate: false, more: false, members: [] }];
    const l = lit[side];
    return arrows.map((a, i) => {
      const ay = y(i, arrows.length);
      const edge = flip ? W - EDGE : EDGE;
      const end = rim(edge, ay, flip ? 31 : 36);
      const [x1, y1, x2, y2] = flip ? [end.x, end.y, edge, ay] : [edge, ay, end.x, end.y];
      // Nearer the peer than the pod, where five arrows are still apart.
      const t = flip ? 0.68 : 0.32;
      const mx = x1 + (x2 - x1) * t;
      const my = y1 + (y2 - y1) * t;
      const open = !s.isolated;
      const on = l.keys.has(a.key);
      const dimmed = l.dim && l.keys.size > 0 && !on;
      const ports = a.ports.length === 0 ? '' : portsLabel(a.ports);
      const tip = open
        ? `${a.name}: no policy isolates it`
        : flip
          ? `this pod → ${a.name} on ${a.ports.join(', ')}${a.members.length > 0 ? `\n${a.members.join('\n')}` : ''}`
          : `${a.name} → this pod on ${a.ports.join(', ')}${a.members.length > 0 ? `\n${a.members.join('\n')}` : ''}`;
      const name = a.aggregate ? a.text.selector : a.text.ns === '' ? a.text.selector : `${a.text.selector} · ${a.text.ns}`;
      const shown = shortLabel(name, GRAPH_CHARS);
      const lx = flip ? W - EDGE + 6 : EDGE - 6;
      const anchor = flip ? 'start' : 'end';
      const two = a.text.sub !== '' && !a.aggregate;
      const keys = (e: KeyboardEvent) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          onPick(side, a.key);
        }
      };
      const interactive = !open;
      return (
        <g
          key={`${side}-${a.key}`}
          data-arrow={a.key}
          data-side={side}
          data-active={on ? '' : undefined}
          data-dimmed={dimmed ? '' : undefined}
          opacity={dimmed ? 0.3 : 1}
          tabIndex={interactive ? 0 : undefined}
          role={interactive ? 'button' : undefined}
          aria-pressed={interactive ? l.sticky === a.key : undefined}
          aria-label={interactive ? `${name}, ${ports}` : undefined}
          className={interactive ? 'cursor-pointer outline-none' : undefined}
          onMouseEnter={interactive ? () => onHover(side, a.key) : undefined}
          onMouseLeave={interactive ? () => onHover(side, null) : undefined}
          onFocus={interactive ? () => onHover(side, a.key) : undefined}
          onBlur={interactive ? () => onHover(side, null) : undefined}
          onClick={
            interactive
              ? (e) => {
                  e.stopPropagation();
                  onPick(side, a.key);
                }
              : undefined
          }
          onKeyDown={interactive ? keys : undefined}
        >
          <title>{tip}</title>
          <path d={`M ${String(x1)} ${String(y1)} L ${String(x2)} ${String(y2)}`} stroke="transparent" strokeWidth={16} fill="none" />
          <line x1={x1} y1={y1} x2={x2} y2={y2} stroke="var(--color-accent)" strokeWidth={on ? 2.5 : 1.5} strokeDasharray={open ? '2 3' : '6 4'} markerEnd="url(#netinspect-pod-arrow)" />
          {/* An arrow too short to carry its ports beside the names keeps them in its tooltip and its rows. */}
          {ports === '' || tight ? null : (
            <text x={mx} y={my} textAnchor="middle" dominantBaseline="central" fontSize="9" fill="var(--color-fg)" stroke="var(--color-panel)" strokeWidth={3} paintOrder="stroke" data-arrow-ports="">
              {ports}
            </text>
          )}
          <text x={lx} y={two ? ay - 5 : ay} textAnchor={anchor} dominantBaseline="central" fontSize="10" fill="var(--color-fg)" fontWeight={on ? 600 : 400} data-arrow-name="">
            {shown}
          </text>
          {two ? (
            <text x={lx} y={ay + 7} textAnchor={anchor} dominantBaseline="central" fontSize="9" fill="var(--color-muted)">
              {a.text.sub}
            </text>
          ) : null}
        </g>
      );
    });
  };
  const svg = (
    <svg
      viewBox={`0 0 ${String(W)} ${String(H)}`}
      role="group"
      aria-label="Whom the rules let this pod talk to"
      className={`w-full rounded-lg border border-line bg-panel ${large ? '' : 'max-w-2xl'} ${onEnlarge === undefined ? '' : 'cursor-zoom-in'}`}
      data-mini-graph={large ? undefined : ''}
      data-large-graph={large ? '' : undefined}
      onClick={onEnlarge}
    >
      <defs>
        <marker id="netinspect-pod-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
          <path d="M 0 0 L 10 5 L 0 10 z" fill="var(--color-accent)" />
        </marker>
      </defs>
      {one('ingress', ingress, false)}
      {one('egress', egress, true)}
      {ingress.nothing ? (
        <text x={EDGE - 6} y={cy} textAnchor="end" dominantBaseline="central" fontSize="10" fill="var(--color-bad)">
          nothing allowed
        </text>
      ) : null}
      {egress.nothing ? (
        <text x={W - EDGE + 6} y={cy} dominantBaseline="central" fontSize="10" fill="var(--color-bad)">
          nothing allowed
        </text>
      ) : null}
      <circle cx={cx} cy={cy} r={26} fill="var(--color-panel-2)" stroke="var(--color-line)" strokeWidth={1.5} />
      <g transform={`translate(${String(cx)} ${String(cy)})`}>
        <circle r={29} fill="none" stroke={ingress.isolated ? 'var(--color-ok)' : 'transparent'} strokeWidth={3} strokeDasharray={`${(Math.PI * 29).toFixed(2)} ${(2 * Math.PI * 29).toFixed(2)}`} transform="rotate(180)" />
        <circle r={29} fill="none" stroke={egress.isolated ? 'var(--color-ok)' : 'transparent'} strokeWidth={3} strokeDasharray={`${(Math.PI * 29).toFixed(2)} ${(2 * Math.PI * 29).toFixed(2)}`} transform="scale(1 -1) rotate(180)" />
      </g>
      <text x={cx} y={cy} textAnchor="middle" dominantBaseline="central" fontSize="10" fill="var(--color-fg)">
        pod
      </text>
      <text x={8} y={12} fontSize="9" fill="var(--color-muted)">
        INGRESS
      </text>
      <text x={W - 8} y={12} textAnchor="end" fontSize="9" fill="var(--color-muted)">
        EGRESS
      </text>
    </svg>
  );
  if (large) {
    return (
      <div ref={box} className="w-full min-w-0" data-large-graph-box="">
        {svg}
      </div>
    );
  }
  if (onEnlarge === undefined) return svg;
  return (
    <div className="relative w-full max-w-2xl">
      {svg}
      <button
        type="button"
        data-enlarge=""
        aria-label="Enlarge"
        title="Enlarge"
        onClick={onEnlarge}
        className={`${btnSecondary} absolute right-1.5 bottom-1.5 bg-panel-2`}
      >
        <span aria-hidden="true">⤢</span>
      </button>
    </div>
  );
}
