/**
 * @kubetower/ui - the console shell and its extension contract.
 *
 * Everything exported here is API an extension compiles against - the extras in
 * `app/extras/`; the package is published nowhere - so every change to it is a
 * Conventional Commit with an accurate type and a breaking one carries `!` and
 * a `BREAKING CHANGE:` footer. Nothing not listed here is public, whatever its
 * file permissions.
 */

export { createApp } from './createApp';
export type {
  Extension,
  ExtensionAbout,
  ExtensionCell,
  ExtensionColumn,
  ExtensionSettingsStatusProps,
  ExtensionShortcut,
  ExtensionShortcutTarget,
  ExtensionStatus,
  ExtensionMeter,
  ExtensionMeterSegment,
  ExtensionActivity,
  ExtensionTab,
  ExtensionTabObject,
  ExtensionTabProps,
} from './extension';
// A pod's live log, opened in the console's dock and read for the signed-in
// person by its own stream - for an extension's page or tab that names a pod
// Draws nothing where there is no dock.
export { LogsButton } from './dock/LogsButton';
// What an extension's page calls itself, for the topbar's breadcrumb and the
// tab title - as the core's own pages declare it, so an extra's page does not
// read "KubeTower" alone.
// A page whose title is a path declares it as a `trail` of segments, so the
// breadcrumb links each one that names a place.
export { trail, useDeclarePageFacts, type Crumb, type PageFacts } from './shell/pageFacts';

// The keyboard priority stack. An extra's dialog claims a layer from this table
// instead of adding a listener of its own; `keyboard/layers.ts` says why.
export { KeyLayer } from './keyboard/layers';
export { useKeyLayer, useEscape } from './keyboard/useKeyLayer';
export type { KeyClaim } from './keyboard/stack';

// Live handles: anything expensive to open, keyed so StrictMode's second mount
// finds the first.
export { useLiveHandle } from './live/useLiveHandle';
export { acquireHandle, releaseHandle, closeHandle, type OpenedHandle } from './live/handles';

// The one network module. ESLint forbids `fetch` and `WebSocket` everywhere
// else; an extra calls this or it calls nothing.
//
// The whole of it, and the generated types with it: an extra that asks a cluster
// anything gets the same `request`, the same envelope narrowing and the same
// types derived from `api/openapi.yaml` as the core. A hand-written `Reason` in
// another repository is a second vocabulary for the five states, and the two
// diverge on the first edit.
export * from './api';

// What an extra needs to open a tunnel's address in a new tab as the Ports
// panel does: the address the browser reaches a forward at (the loopback
// spelling that keeps the console's cookie from reaching the tunnel), and the
// one guard every link that leaves the console goes through.
export { forwardLink, type Scheme } from './pages/forwards/address';
export { externalHref } from './externalHref';

// The five states, drawn. Exported for the same reason the envelope is: an
// extra's list has to be able to tell `unreachable` from `empty`, or the defect
// this product is organized around has been reintroduced in a page nobody here
// reviews.
export { ListView, ValueView, type ListViewProps, type ValueViewProps } from './state/Result';
export { StateNotice, Failure, Pending, GoneNotice, isGoneProblem, type StateNoticeProps } from './state/StateNotice';

// The Shell, for the extras that need to put something in the drawer.
export { useShell } from './shell/ShellContext';

// Saying out loud what an action did, into the console's one live region.
//
// Exported for the same reason the keyboard stack is: an extra that rearranges
// something, or runs a verb whose only visible result is a list elsewhere on the
// page, would otherwise either stay silent or mount a second live region of its
// own - and two polite regions on one page announce in an order neither of them
// chose. `shell/announce.ts` carries the rest.
export { announce } from './shell/announce';

// The list primitives.
//
// Exported for the same reason `StateNotice` is, and the argument is a little
// stronger here. An extra that adds a page adds a list, and the two rules a list
// has to obey are not discoverable from a screenshot: the class is `kt-grid` and
// not `grid`, or Tailwind takes the table out of table layout and both of
// `theme.css`'s rules stop acting on anything; and every column width is a
// function of the column definition, never of a cell, or a value that arrives
// two seconds late is drawn in a column sized for the em dash that stood in for
// it. A second grid written in another repository gets both wrong, silently, and
// nobody here reviews it.
//
// `Remedy` is exported on the same grounds as `StateNotice`: telling
// `unreachable` from `forbidden` is worth nothing if both send the operator to
// the same place. It reads nothing and links nowhere, so it needs no provider.
export {
  DataGrid,
  columnWidth,
  headingRoom,
  isSized,
  type DataGridProps,
  type GridColumn,
  type GridSelection,
  type GridSort,
} from './components/DataGrid';
export { Remedy, type RemedyProps } from './components/Remedy';
export { FilterBox, type FilterBoxProps } from './components/FilterBox';
export { ColumnMenu, type ColumnMenuProps } from './components/ColumnMenu';
export { TableMenu, type TableMenuProps } from './components/TableMenu';
export { SelectionBar } from './components/SelectionBar';
// The console's verbs on one object, for an extra's own page about it
// the object's `actions`, through the plan dialog.
export { ObjectVerbs } from './pages/object/ObjectVerbs';
export { arrange, arrangeAll, isLastVisible, type Arrangeable } from './components/columns';
export { useSelection, type Selection } from './components/useSelection';
export { useUrlParameter } from './components/useUrlParameter';
export { useDebounced } from './components/useDebounced';

// The graded-status chip, on the same argument as the list primitives: an extra
// that adds a page draws a status, and a chip invented in another repository is
// a second status vocabulary in one console. `Pill.tsx` carries the shape, the
// tints and why the neutral pill exists; `tone` is the server's grade as a
// string, so an extra passes it through rather than mapping it.
export { Pill, type PillProps } from './components/Pill';
export type { IconName } from './components/Icon';

// How the console writes an instant and a span: an extra draws no time of its own.
export { formatInstant, formatClock, shortDuration } from './components/instant';

// The header of a page or sub-view: the way back as a plain arrow, then the
// kind, the name and the namespace. One header, so an extra's sub-view and the
// console's object page line up the same.
export { PageHeader, BackArrow } from './components/PageHeader';

// The tile, on the same argument once more: Settings → Extra draws one, and the
// Argo CD and Redis pages want the same look - one tile per Application, one per
// deployment. A tile drawn privately in each
// is three looks in one console. `Tile.tsx` carries the anatomy (title, a few
// chips, one secondary line, an action slot, the tone as a bar) and why the
// action is never inside the open control.
export {
  Tile,
  FilterTile,
  TileGrid,
  type TileProps,
  type FilterTileProps,
  type TileGridProps,
  type TileTone,
} from './components/Tile';

// The two shell behaviors an extra would otherwise re-implement.
//
// `Dialog`: Escape at a layer from the keyboard stack, a click
// on the veil and not on the box, the keyboard handed in and given back, and
// deliberately no focus trap. An extra that writes its own modal gets the
// keyboard wrong in a way nothing fails on.
//
// `CopyButton`, and the three-step fallback is the whole point of
// it: a console reached over plain http on a LAN address is not a secure
// context, so the Clipboard API is simply absent there and a one-step button is
// one that does nothing on half the installations.
export { Dialog, type DialogProps } from './components/Dialog';
export { CopyButton, copyText, type CopyButtonProps } from './components/CopyButton';

// The plan dialog, for an extra page that links to one of the console's own
// confirm-then-act routes (the Security Center's install of its operator): the
// server's sentence, the gesture it asks for and the outcome, drawn by the one
// renderer every act uses. A page drawing its own confirmation would be a
// second author of what the person agrees to.
export { PlanDialog } from './components/PlanDialog';

// The `⋯` menu, for the same reason as the dialog: its open state, scrim,
// Escape layer and arrow keys are written once, and an extra that needs a
// menu (the Redis viewer's value actions) draws this one rather than a second
// copy whose keyboard nobody tests. `copyText` above is what a menu item that
// copies calls, so a copy from a menu falls back exactly as the button does.
export { OverflowMenu, OverflowMenuItem, type OverflowMenuProps } from './components/OverflowMenu';
export { LIST_PAGE, Windowed } from './components/Windowed';

// The focus ring, as five utility strings.
//
// Exported for the reason the list primitives are: an extra adds pages, and a
// second ring invented in another repository gives the operator two rings in one
// console. Which of the five a control takes is decided by whether it has room
// outside itself, not by taste - `focus.ts` carries the rule.
export {
  focusRing,
  focusRingTight,
  focusRingFlush,
  focusRingInside,
  focusRingClear,
} from './focus';

// The destructive style, as one utility string, on the same argument as the
// ring: an extra that adds a page adds a Delete, and a red invented in another
// repository is a second destructive style in one console. `destructive.ts`
// carries why each part is what it is.
export { destructive } from './destructive';

// One look per kind of button and one close ×, so an extra's page draws the
// console's buttons rather than a second set (`components/buttons.ts`).
export { btn, btnSecondary, btnPrimary, btnDanger, btnGhost, btnToggle } from './components/buttons';
export { CloseButton, type CloseButtonProps } from './components/CloseButton';

// The one look of a table, on the argument of the list primitives: an extra's
// page that draws a table of its own draws it at the lists' size, heading and
// row height rather than a second set (`components/tableLook.ts`).
export { TABLE, HEAD_ROW, ROW, headCell, bodyCell } from './components/tableLook';

// The watching arm of the hover rule, on the same argument once more: an
// extra that adds a page adds a control that opens a stream, and a hover
// invented in another repository is a second vocabulary for *this control
// watches something*. `hover.ts` carries the rule.
export { watching } from './hover';
// An object's own address, for a link that opens it (an extension's shortcut to its tab).
export { objectPath } from './pages/object/address';
