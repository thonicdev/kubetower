import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useMemo, useState, type ReactNode } from 'react';
import { CloseButton, HEAD_ROW, Pending, Pill, ROW, TABLE, bodyCell, btnSecondary, focusRing, headCell } from '@kubetower/ui';

import { rowsQuery, tablesQuery, type Cell, type PageKey, type Rows, type TableRow, type Tables } from './data';
import { bytes } from './parts';
import { Problem } from './states';

/**
 * A cluster's tables, the Redis Viewer's way: the tables of a database and
 * schema on the left, with their estimated rows and size; the picked table's
 * rows on the right, a page at a time, each value drawn for its type; and the
 * value a cell holds in full under them. Read through the cluster's own
 * read-only SQL door (`extras/cnpg/sqlconn.go`), only when asked.
 */
export function TableBrowser({ cluster, namespace, name, first }: { cluster: string; namespace: string; name: string; first: Tables }): ReactNode {
  const [database, setDatabase] = useState(first.database);
  const read = useQuery({ ...tablesQuery(cluster, namespace, name, database === first.database ? '' : database), enabled: database !== first.database });
  const tables = database === first.database ? first : read.data;
  const databases = first.databases ?? [first.database];
  const [schema, setSchema] = useState('');
  const [search, setSearch] = useState('');
  const [picked, setPicked] = useState<{ schema: string; table: string } | undefined>(undefined);

  const shown = useMemo(() => {
    if (tables === undefined) return [];
    const needle = search.trim().toLowerCase();
    return tables.tables.filter((t) => (schema === '' || t.schema === schema) && (needle === '' || `${t.schema}.${t.name}`.toLowerCase().includes(needle)));
  }, [tables, schema, search]);

  return (
    <div className="flex flex-col gap-2" data-table-browser="">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <Choice label="Database" value={database} options={databases} onChange={(d) => { setDatabase(d); setSchema(''); setPicked(undefined); }} />
        {tables !== undefined && tables.schemas.length > 1 ? (
          <Choice label="Schema" value={schema} options={['', ...tables.schemas]} onChange={setSchema} />
        ) : null}
        <input
          type="search"
          aria-label="Find a table"
          placeholder="table"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className={`min-w-0 flex-1 basis-32 rounded-md border border-line bg-panel-2 px-2 py-1 ${focusRing}`}
        />
        {tables === undefined ? null : (
          <Pill neutral="muted" title={tables.complete ? undefined : `The first ${String(tables.tables.length)} tables`}>
            {`${String(shown.length)}${tables.complete ? '' : '+'} tables`}
          </Pill>
        )}
      </div>
      {tables === undefined ? (
        read.isError ? <Problem error={read.error} onRetry={() => void read.refetch()} /> : <Pending what={`the tables of ${database}`} />
      ) : (
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
          <TableList tables={shown} schemas={tables.schemas} picked={picked} onPick={setPicked} />
          {picked === undefined ? null : (
            <TableView
              key={`${database}.${picked.schema}.${picked.table}`}
              cluster={cluster}
              namespace={namespace}
              name={name}
              database={database}
              table={tables.tables.find((t) => t.schema === picked.schema && t.name === picked.table)}
              schema={picked.schema}
              tableName={picked.table}
            />
          )}
        </div>
      )}
    </div>
  );
}

function Choice({ label, value, options, onChange }: { label: string; value: string; options: readonly string[]; onChange: (v: string) => void }): ReactNode {
  return (
    <select aria-label={label} className={`rounded-md border border-line bg-panel px-1 py-1 text-fg ${focusRing}`} value={value} onChange={(e) => onChange(e.target.value)}>
      {options.map((o) => (
        <option key={o} value={o}>
          {o === '' ? 'all schemas' : o}
        </option>
      ))}
    </select>
  );
}

const KIND_WORD: Readonly<Record<TableRow['kind'], string>> = {
  table: '',
  partitioned: 'partitioned',
  view: 'view',
  materialized: 'mat. view',
  foreign: 'foreign',
};

function TableList({
  tables,
  schemas,
  picked,
  onPick,
}: {
  tables: readonly TableRow[];
  schemas: readonly string[];
  picked: { schema: string; table: string } | undefined;
  onPick: (p: { schema: string; table: string }) => void;
}): ReactNode {
  if (tables.length === 0) return <p className="text-xs text-muted">No table</p>;
  return (
    <ul className="flex max-h-96 flex-col overflow-y-auto rounded-md border border-line text-xs lg:col-span-1" data-table-list="">
      {tables.map((row) => {
        const id = `${row.schema}.${row.name}`;
        const on = picked?.schema === row.schema && picked.table === row.name;
        return (
          <li key={id}>
            <button
              type="button"
              data-table={id}
              aria-pressed={on}
              onClick={() => onPick({ schema: row.schema, table: row.name })}
              className={`flex w-full items-center gap-2 px-2 py-1.5 text-left hover:bg-panel-2 ${on ? 'bg-accent/15' : ''} ${focusRing}`}
            >
              <span className="min-w-0 flex-1 truncate font-mono" title={id}>
                {schemas.length > 1 || row.schema !== 'public' ? <span className="text-muted">{row.schema}.</span> : null}
                {row.name}
              </span>
              {KIND_WORD[row.kind] === '' ? null : <Pill neutral="muted">{KIND_WORD[row.kind]}</Pill>}
              <span className="w-16 text-right text-muted tabular-nums" title="Postgres' estimate of its rows">
                {row.rows < 0 ? '?' : `~${compact(row.rows)}`}
              </span>
              <span className="w-14 text-right text-muted tabular-nums">{bytes(row.bytes)}</span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

const KEY_PILL: Readonly<Record<PageKey, { text: string; title: string }>> = {
  primary: { text: 'by key', title: 'Pages follow the primary key' },
  ctid: { text: 'by ctid', title: 'No primary key: pages follow the rows on disk' },
  offset: { text: 'by offset', title: 'A view: pages follow an offset, up to 10,000 rows' },
};

function TableView({
  cluster,
  namespace,
  name,
  database,
  schema,
  tableName,
  table,
}: {
  cluster: string;
  namespace: string;
  name: string;
  database: string;
  schema: string;
  tableName: string;
  table: TableRow | undefined;
}): ReactNode {
  // The cursors of the pages read so far: the first is '', each next one the
  // page before's `next`.
  const [cursors, setCursors] = useState<string[]>(['']);
  const cursor = cursors[cursors.length - 1] ?? '';
  const query = useQuery({ ...rowsQuery(cluster, namespace, name, { database, schema, table: tableName, cursor }), placeholderData: keepPreviousData });
  const [cell, setCell] = useState<{ row: number; column: number } | undefined>(undefined);

  const r = query.data;
  return (
    <section className="flex min-w-0 flex-col gap-2 lg:col-span-2" data-rows={`${schema}.${tableName}`} aria-label={`${schema}.${tableName}`}>
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <h3 className="font-mono text-sm font-medium">
          {schema === 'public' ? null : <span className="text-muted">{schema}.</span>}
          {tableName}
        </h3>
        {table !== undefined && KIND_WORD[table.kind] !== '' ? <Pill neutral="muted">{KIND_WORD[table.kind]}</Pill> : null}
        {table !== undefined && table.rows >= 0 ? (
          <Pill neutral="muted" title="Postgres' estimate">{`~${table.rows.toLocaleString('en-US')} rows`}</Pill>
        ) : null}
        {r?.key === undefined ? null : (
          <Pill neutral="muted" title={KEY_PILL[r.key].title}>
            {KEY_PILL[r.key].text}
          </Pill>
        )}
        {r !== undefined && r.columnsTotal > r.columns.length ? (
          <Pill tone="warn" title={`The first ${String(r.columns.length)} of ${String(r.columnsTotal)} columns`}>{`${String(r.columns.length)}/${String(r.columnsTotal)} columns`}</Pill>
        ) : null}
        <span className="ml-auto flex items-center gap-1">
          {r === undefined ? null : (
            <span className="text-muted tabular-nums">{range(r)}</span>
          )}
          <button type="button" className={btnSecondary} aria-label="Previous page" disabled={cursors.length < 2 || query.isFetching} onClick={() => { setCell(undefined); setCursors((c) => c.slice(0, -1)); }}>
            ‹
          </button>
          <button
            type="button"
            className={btnSecondary}
            aria-label="Next page"
            disabled={r?.next === undefined || !r.more || query.isFetching}
            onClick={() => {
              const next = r?.next;
              if (next === undefined) return;
              setCell(undefined);
              setCursors((c) => [...c, next]);
            }}
          >
            ›
          </button>
        </span>
      </div>
      {query.isError ? (
        <Problem error={query.error} onRetry={() => void query.refetch()} />
      ) : r === undefined ? (
        <Pending what={`${schema}.${tableName}`} />
      ) : (
        <>
          <div className="max-h-96 overflow-auto rounded-md border border-line">
            <table className={`${TABLE} w-max min-w-full`}>
              <thead className="sticky top-0 bg-panel-2">
                <tr className={HEAD_ROW}>
                  {r.columns.map((c) => (
                    <th key={c.name} className={headCell()} title={c.type}>
                      <span className="flex items-center gap-1 normal-case">
                        <span className="font-mono text-fg">{c.name}</span>
                        {c.primary === true ? <Pill tone="info">key</Pill> : null}
                      </span>
                      <span className="block font-mono text-2xs normal-case">{c.type}</span>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {r.rows.map((row, i) => (
                  <tr key={i} className={ROW}>
                    {row.map((value, j) => (
                      <td key={j} className={`${bodyCell(true)} ${cellLook(r.columns[j]?.type, cell?.row === i && cell.column === j)}`} onClick={() => setCell({ row: i, column: j })} data-cell={`${String(i)}:${String(j)}`}>
                        <CellValue value={value} type={r.columns[j]?.type ?? ''} />
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {cell === undefined ? null : (
            <CellDetail column={r.columns[cell.column]} value={r.rows[cell.row]?.[cell.column]} onClose={() => setCell(undefined)} />
          )}
        </>
      )}
    </section>
  );
}

function cellLook(type: string | undefined, on: boolean): string {
  return `max-w-72 cursor-pointer truncate ${numeric(type) ? 'text-right tabular-nums' : ''} ${on ? 'bg-accent/15' : ''}`;
}

function range(r: Rows): string {
  if (r.rows.length === 0) return '0 rows';
  const start = (r.start ?? 0) + 1;
  return `${start.toLocaleString('en-US')}–${(start + r.rows.length - 1).toLocaleString('en-US')}`;
}

const NUMERIC = /^(smallint|integer|bigint|numeric|real|double precision|decimal|money|int[248]?|float[48]?)(\(.*\))?$/;
function numeric(type: string | undefined): boolean {
  return type !== undefined && NUMERIC.test(type);
}

function isJson(type: string): boolean {
  return type === 'json' || type === 'jsonb';
}

/** One value in its cell, for its type. */
function CellValue({ value, type }: { value: Cell; type: string }): ReactNode {
  if (value.null === true) return <span className="text-muted italic">null</span>;
  if (value.bytes !== undefined) {
    return (
      <span className="font-mono">
        <span className="text-muted">{bytes(value.bytes)}</span> {value.hex ?? ''}
      </span>
    );
  }
  const text = value.text ?? '';
  if (type === 'boolean') return <span className="font-mono">{text === 't' || text === 'true' ? 'true' : 'false'}</span>;
  return (
    <span className="font-mono">
      {isJson(type) ? oneLine(text) : text}
      {value.cut === true ? '…' : ''}
    </span>
  );
}

function oneLine(text: string): string {
  return text.replace(/\s+/g, ' ');
}

/** The value a cell holds in full: JSON laid out, a bytea's first bytes, a
 *  long text whole as far as it was read. */
function CellDetail({ column, value, onClose }: { column: { name: string; type: string } | undefined; value: Cell | undefined; onClose: () => void }): ReactNode {
  if (column === undefined || value === undefined) return null;
  const text = value.text ?? '';
  let shown = text;
  if (isJson(column.type) && value.cut !== true) {
    try {
      shown = JSON.stringify(JSON.parse(text), null, 2);
    } catch {
      shown = text;
    }
  }
  return (
    <aside className="flex flex-col gap-2 rounded-md border border-line bg-panel-2 p-2 text-xs" data-cell-detail="" aria-label={column.name}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono font-medium">{column.name}</span>
        <Pill neutral="muted">{column.type}</Pill>
        {value.cut === true ? <Pill tone="warn" title="Read up to 512 characters">cut</Pill> : null}
        {value.bytes !== undefined ? <Pill neutral="muted">{bytes(value.bytes)}</Pill> : null}
        <span className="ml-auto flex gap-1">
          {value.null === true || value.bytes !== undefined ? null : (
            <button type="button" className={btnSecondary} onClick={() => void navigator.clipboard.writeText(text)}>
              Copy
            </button>
          )}
          <CloseButton onClick={onClose} label="Close the value" />
        </span>
      </div>
      {value.null === true ? (
        <span className="text-muted italic">null</span>
      ) : value.bytes !== undefined ? (
        <pre className="font-mono break-all whitespace-pre-wrap">{value.hex ?? ''}</pre>
      ) : (
        <pre className="max-h-64 overflow-auto font-mono break-words whitespace-pre-wrap">
          {shown}
          {value.cut === true ? '…' : ''}
        </pre>
      )}
    </aside>
  );
}

function compact(n: number): string {
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`;
  return `${(n / 1_000_000).toFixed(n < 10_000_000 ? 1 : 0)}M`;
}
