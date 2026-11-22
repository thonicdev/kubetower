import { useMutation, useQueryClient, type QueryKey } from '@tanstack/react-query';
import { useSyncExternalStore } from 'react';

import {
  ApiError,
  queryKeys,
  resetColumns,
  setColumnBeside,
  setColumnDensity,
  setColumnShow,
  setColumnSize,
  useCapabilities,
  useKeepsPreferences,
  usePreferences,
  writeInOrder,
  type ListPage,
} from '../../api';
import { arrangeAll } from '../../components/columns';
import {
  columnMemory,
  forgetColumns,
  rememberDensity,
  rememberPlace,
  rememberShown,
  rememberWidths,
  subscribeToColumnMemory,
  withRemembered,
  type ArrangedColumns,
} from './columnMemory';

/**
 * The arrangement of a list's columns and its row height, and every gesture
 * that changes them - posted to the console where it keeps preferences, kept
 * in this browser where it does not.
 *
 * `page` is what carries the columns: a resource list's `ListPage`, or the
 * Helm list's answer, whose arrangement the console keeps under the kind
 * `helm`. `listKey` is the query that answer comes from, read again after a
 * write so the arrangement drawn is the one stored; a resource kind's list
 * where it is not given.
 */
export function useColumnControls<P extends ArrangedColumns = ListPage>(
  cluster: string | undefined,
  kind: string | undefined,
  page: P | undefined,
  listKey?: QueryKey,
) {
  const client = useQueryClient();
  const invalidate = () => {
    if (cluster === undefined || kind === undefined) return;
    void client.invalidateQueries({ queryKey: listKey ?? queryKeys.resourceKind(cluster, kind) });
  };

  // One column, one request. The schema refuses a second field and so does this.
  const show = useMutation({
    mutationFn: ({ column, visible }: { column: string; visible: boolean }) =>
      setColumnShow(kind ?? '', column, visible),
    onSuccess: invalidate,
  });

  const reset = useMutation({
    mutationFn: () => resetColumns(kind ?? ''),
    onSuccess: invalidate,
  });

  /*
    Row height, and it is the console's rather than this kind's.

    Read from `GET /api/preferences` and not from a store of this page's own,
    because `internal/columns/columns.go:758` keeps one value for the whole
    console: it is a statement about the person's screen, and somebody who
    wants more rows at once wants them everywhere. Absent is comfortable, which
    is the store's own default and therefore what a console nobody has touched
    draws.

    The write invalidates the preferences query and nothing else. It does not
    invalidate the list: density changes how the rows are drawn and not which
    rows there are, and re-asking the cluster for a padding is a request nobody
    asked for.
  */
  /*
    A resize, and a reorder.

    **The resize is the whole arrangement, on every gesture**, which is
    `DataGrid`'s call and not this one: the grid measures what is on screen and
    says what the gesture decided, and `DataGridProps.onResize` carries the
    reason. A stored width is a share of the table, so widening one
    column means the ones after it give up the same room between them and every
    number in the set has just changed. Here that is one POST per entry,
    sequentially, because `ColumnSizeWrite` is deliberately one column per
    request — the whole-object rule this endpoint set was written
    for — and `Store.Resize` clamps and saves each one. Sequential rather than
    concurrent so that a refusal partway leaves a prefix rather than an arbitrary
    subset; `Reset columns` is the way back from either. A width the server
    applied and could not save is not a refusal, so `writeInOrder` sends the rest
    of the set and the list is read again: stopping there would
    leave one share changed and its neighbors not.

    `setColumnBeside` sends a neighbor and a side rather than an index, because
    an index is a fact about the list the browser last saw and this panel is
    redrawn every two seconds.

    Both invalidate this kind's list **once, at the end**, which is how the
    answer comes back: the server clamps the width, resolves the order, and the
    arrangement that is drawn is the one on disk rather than the one the pointer
    left behind.
  */
  const resize = useMutation({
    mutationFn: (sizes: readonly { key: string; width: number | null }[]) =>
      writeInOrder(sizes.map((each) => () => setColumnSize(kind ?? '', each.key, each.width))),
    onSuccess: invalidate,
    onError: (error) => {
      if (error instanceof ApiError && error.isNotSaved) invalidate();
    },
  });

  const place = useMutation({
    mutationFn: ({
      column,
      target,
      edge,
    }: {
      column: string;
      target: string;
      edge: 'before' | 'after';
    }) => setColumnBeside(kind ?? '', column, target, edge),
    onSuccess: invalidate,
  });

  const preferences = usePreferences();
  /*
    **Who keeps the arrangement: the console, or this browser**.

    Where the server keeps preferences - the desktop - every control below
    posts to it, as above. Where it keeps none (`capabilities.preferences` is
    false: the server profile, or several replicas behind one address) the
    twelve routes do not exist, and on a shared console they would be the wrong
    thing anyway: one person's widths would become everybody's. So the same
    controls are drawn and each gesture lands in `columnMemory` - this browser's
    own storage - and sends no request at all: the arrangement is *per
    browser*.
  */
  const keeps = useKeepsPreferences();
  // **No gesture is kept anywhere until the capabilities have answered.**
  // `keeps` reads unknown as true, which is right for drawing - a desktop draws
  // its controls at once - and wrong for writing: under the server profile a
  // drag in that window would post to a route that does not exist and 404.
  // Settled means answered or failed; a failed read is an older server, which
  // keeps them.
  const decided = !useCapabilities().isPending;
  const memory = useSyncExternalStore(subscribeToColumnMemory, columnMemory, columnMemory);
  const arranged = (listPage: P): P => (keeps ? listPage : withRemembered(listPage, kind ?? '', memory));
  const shown = page === undefined ? undefined : arranged(page);


  const compact = keeps
    ? preferences.data?.columns.density === 'compact'
    : memory.density === 'compact';
  const density = useMutation({
    mutationFn: (want: boolean) => setColumnDensity(want ? 'compact' : 'comfortable'),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.preferences });
    },
  });

  const onShow = (column: string, visible: boolean): void => {
    if (!decided) return;
    if (keeps) {
      show.mutate({ column, visible });
      return;
    }
    if (kind === undefined) return;
    // Against what the server drew, not against `secondary`: see rememberShown.
    const sent = page?.columns.find((each) => each.key === column)?.visible === true;
    rememberShown(kind, column, visible, sent);
  };
  const onReset = (): void => {
    if (!decided) return;
    if (keeps) reset.mutate();
    else if (kind !== undefined) forgetColumns(kind);
  };
  const onDensity = (want: boolean): void => {
    if (!decided) return;
    if (keeps) density.mutate(want);
    else rememberDensity(want ? 'compact' : 'comfortable');
  };
  const onResize = (sizes: readonly { key: string; width: number | null }[]): void => {
    if (!decided) return;
    if (keeps) resize.mutate(sizes);
    else if (kind !== undefined) rememberWidths(kind, sizes);
  };
  const onPlace = (column: string, target: string, edge: 'before' | 'after'): void => {
    if (!decided) return;
    if (keeps) {
      place.mutate({ column, target, edge });
      return;
    }
    if (kind === undefined || shown === undefined) return;
    const onScreen = arrangeAll(shown.columns).map((each) => each.key);
    rememberPlace(kind, onScreen, column, target, edge);
  };

  return { arranged, shown, compact, onShow, onReset, onDensity, onResize, onPlace };
}
