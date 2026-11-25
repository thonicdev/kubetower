import type { ReactNode } from 'react';

/**
 * KubeTower's mark, the three plates of `internal/web/static/pwa/icon.svg`,
 * drawn inline so it needs no request (#107). `Mark.test.tsx` holds the plates
 * to that file's, so the two cannot drift.
 */
export const PLATES: readonly { d: string; fill: string }[] = [
  { d: 'M256 249 L406 324 L256 399 L106 324 Z', fill: '#1d47a6' }, // style-allow: the published mark
  { d: 'M256 181 L406 256 L256 331 L106 256 Z', fill: '#3b7ae8' }, // style-allow: the published mark
  { d: 'M256 113 L406 188 L256 263 L106 188 Z', fill: '#6ea0ff' }, // style-allow: the published mark
];

export function Mark({ className = '' }: { className?: string }): ReactNode {
  return (
    <svg viewBox="96 100 320 312" className={className} aria-hidden="true" focusable="false" data-kubetower-mark="">
      {PLATES.map((plate) => (
        <path key={plate.d} d={plate.d} fill={plate.fill} />
      ))}
    </svg>
  );
}
