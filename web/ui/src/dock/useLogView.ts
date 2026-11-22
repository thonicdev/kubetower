import { useState } from 'react';

/** The three view options of a log pane, all off to begin with. */
interface LogView {
  readonly timestamps: boolean;
  readonly wrap: boolean;
  readonly json: boolean;
}

/**
 * The view options of a log pane - timestamps, wrap, JSON - held as one value
 * with one toggle each, so they are one state and not three.
 */
export function useLogView(): LogView & {
  readonly toggleTimestamps: () => void;
  readonly toggleWrap: () => void;
  readonly toggleJSON: () => void;
} {
  const [view, setView] = useState<LogView>({ timestamps: false, wrap: false, json: false });
  return {
    ...view,
    toggleTimestamps: () => setView((was) => ({ ...was, timestamps: !was.timestamps })),
    toggleWrap: () => setView((was) => ({ ...was, wrap: !was.wrap })),
    toggleJSON: () => setView((was) => ({ ...was, json: !was.json })),
  };
}
