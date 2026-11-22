import type { ReactNode } from 'react';

import type { Container } from '../api';
import { btnToggle } from '../components/buttons';
import { OverflowMenu, OverflowMenuCheck, OverflowMenuItem } from '../components/OverflowMenu';
import { SearchField, Toggle } from '../components/SearchField';

/**
 * The bar's shape is Lens's, because Lens is what this was measured against -
 * less the right-hand group, which crowded the filter.
 *
 * **Time, JSON, Wrap and Download are behind the `⋯`.** Each is touched once
 * when a pane is opened, if at all, and four always-drawn buttons pushed the
 * bar onto a second row at 1024 px. **The width they gave back goes to the
 * filter**: it grows from its minimum up to `max-w-md`, and only what is left
 * after that goes to the gap before the `⋯` - an auto margin on the `⋯`'s own
 * wrapper rather than a growing spacer, because a spacer that grows splits the
 * room with the filter instead of taking what the filter does not want, and a
 * spacer of its own stays behind when the bar wraps. The `⋯` carries a dot while any of the three
 * toggles is on, because each changes what the pane draws and the reason must
 * not be out of sight.
 *
 * **Prev run stays in the bar, beside the container switcher and its restart
 * badge**, because it is not a way of reading the lines: it changes which run
 * they come from, and the restart count beside it is what says there is one.
 */
export function LogToolbar(props: {
  containers: readonly Container[];
  active: string;
  previous: boolean;
  onContainer: (name: string) => void;
  onPrevious: (want: boolean) => void;
  query: string;
  onQuery: (text: string) => void;
  onForce: () => void;
  regex: boolean;
  onRegex: (on: boolean) => void;
  matchCase: boolean;
  onMatchCase: (on: boolean) => void;
  error: string | null;
  matches: number;
  at: number;
  onStep: (step: number) => void;
  timestamps: boolean;
  onTimestamps: () => void;
  wrap: boolean;
  onWrap: () => void;
  json: boolean;
  onJSON: () => void;
  onDownload: () => void;
}): ReactNode {
  const chosen = props.containers.find((one) => one.name === props.active);
  // **Only the restart count can refuse Prev run**, and it is a client-side
  // check for a reason worth keeping: an EventSource never sees the status or
  // the body of a failed handshake, so the server's 404 for "previous
  // terminated container not found" arrives as nothing at all and the pane sits
  // empty looking like a stream that has not started.
  const restarts = chosen?.restarts;
  const canPrevious = restarts !== undefined && restarts > 0;
  // The toggles behind the `⋯` that are on, named for the sentence the dot
  // stands for.
  const hiddenOn = [
    ...(props.timestamps ? ['Time'] : []),
    ...(props.json ? ['JSON'] : []),
    ...(props.wrap ? ['Wrap'] : []),
  ];

  return (
    <div className="flex flex-wrap items-center gap-1 border-b border-line px-2 py-1 text-xs">
      {/* No switcher at all on a single-container pod. */}
      {props.containers.length > 1 && props.containers.map((one) => (
        <button
          key={one.name}
          type="button"
          aria-pressed={one.name === props.active}
          title={
            one.restarts === undefined
              ? one.name
              : one.restarts > 0
                ? `has restarted ${one.restarts} times — its previous run can be read`
                : 'has never restarted'
          }
          className={btnToggle(one.name === props.active)}
          onClick={() => props.onContainer(one.name)}
        >
          {one.init === true && <span className="text-muted">init:</span>}
          {one.name}
          {one.restarts !== undefined && one.restarts > 0 && (
            <span className="ml-1 text-warn">↺{one.restarts}</span>
          )}
        </button>
      ))}
      <Toggle
        on={props.previous}
        label="Prev run"
        disabled={!canPrevious}
        title={
          canPrevious
            ? 'Read the previous run of this container'
            : 'no previous run — this container has not restarted'
        }
        onClick={() => props.onPrevious(!props.previous)}
      />

      <SearchField
        label="Filter the log"
        placeholder="Filter"
        query={props.query}
        onQuery={props.onQuery}
        // Enter settles the filter now, with or without Shift: this box filters
        // the pane rather than stepping through it, and the stepper beside the
        // count is what moves between the matches.
        onEnter={() => props.onForce()}
        matchCase={props.matchCase}
        onMatchCase={props.onMatchCase}
        regex={{ on: props.regex, onChange: props.onRegex }}
        error={props.error}
        matches={props.matches}
        at={props.at}
        onStep={props.onStep}
      />

      {/* The auto margin is on the ⋯'s own box, so it travels with it: a bar
          that wraps keeps the ⋯ at the right of the row it lands on. A
          zero-width spacer would stay on the first row and leave the ⋯ alone
          at the left of the second. */}
      <div className="ml-auto">
        <OverflowMenu
          label="More log options"
          title="More: timestamps, JSON, wrapping and download"
          flagged={hiddenOn.length > 0}
          flaggedText={`${hiddenOn.join(', ')} on`}
          // Upward: the dock's bottom edge is the window's, and at its 120 px
          // minimum the pane below this bar is shorter than the four items.
          placement="above"
        >
          <OverflowMenuCheck
            checked={props.timestamps}
            label="Time"
            // The item says what it costs, because it is a request option and not
            // a rendering one: the apiserver either prefixes the instant or it
            // does not, and there is no way to add one afterwards to lines that
            // arrived without.
            title="Ask the cluster to stamp each line. This re-opens the stream, so the buffer starts again from the tail."
            onChange={props.onTimestamps}
          />
          <OverflowMenuCheck checked={props.json} label="JSON" title="Indent the first JSON value in each line" onChange={props.onJSON} />
          <OverflowMenuCheck checked={props.wrap} label="Wrap" title="Wrap long lines" onChange={props.onWrap} />
          <OverflowMenuItem label="Download" title="Save the lines this filter shows, as the container printed them" onSelect={props.onDownload} />
        </OverflowMenu>
      </div>
    </div>
  );
}
