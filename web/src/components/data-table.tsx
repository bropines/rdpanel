import {
  flexRender,
  getCoreRowModel,
  useReactTable,
  type ColumnDef,
  type RowSelectionState,
  type VisibilityState,
} from "@tanstack/react-table";
import { ArrowDown, ArrowUp, ArrowUpDown, ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight, Columns3, Search, X } from "lucide-react";
import { useMemo, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { EmptyState, ErrorState } from "@/components/common";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Checkbox, Skeleton } from "@/components/ui/misc";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatNumber } from "@/lib/format";
import { useLocalState, useSlashFocus } from "@/lib/hooks";
import { pageCount, PAGE_SIZES, sortParamToSorting } from "@/lib/table-state";
import { cn } from "@/lib/utils";

export interface ColumnMeta {
  /** i18n label used in the column chooser. */
  label?: string;
  className?: string;
  /** Hidden by default (user can show it). */
  defaultHidden?: boolean;
}

export interface DataTableProps<T> {
  /** Stable id: persists column visibility in localStorage. */
  id: string;
  columns: ColumnDef<T, unknown>[];
  data: T[] | undefined;
  total: number | undefined;
  isLoading: boolean;
  error: unknown;
  onRetry?: () => void;
  page: number;
  pageSize: number;
  onPageChange: (page: number) => void;
  onPageSizeChange?: (size: number) => void;
  sort: string | undefined;
  onSortChange?: (sort: string | undefined) => void;
  getRowId: (row: T) => string;
  selectable?: boolean;
  rowSelection?: RowSelectionState;
  onRowSelectionChange?: (s: RowSelectionState) => void;
  onRowClick?: (row: T) => void;
  /** Rendered above the table when rows are selected. */
  bulkActions?: (selectedIds: string[], clear: () => void) => ReactNode;
  empty?: ReactNode;
  /** Highlights a row (e.g. the one open in a drawer). */
  activeRowId?: string;
  dense?: boolean;
}

export function DataTable<T>(props: DataTableProps<T>) {
  const { t, i18n } = useTranslation();
  const {
    columns,
    data,
    total = 0,
    isLoading,
    error,
    page,
    pageSize,
    selectable,
    rowSelection = {},
    onRowSelectionChange,
  } = props;

  const defaultVisibility = useMemo(() => {
    const v: VisibilityState = {};
    for (const c of columns) {
      const meta = c.meta as ColumnMeta | undefined;
      const id = c.id ?? (c as { accessorKey?: string }).accessorKey;
      if (id && meta?.defaultHidden) v[id] = false;
    }
    return v;
  }, [columns]);
  const [visibility, setVisibility] = useLocalState<VisibilityState>(`rdpanel-cols-${props.id}`, defaultVisibility);

  const allColumns = useMemo<ColumnDef<T, unknown>[]>(() => {
    if (!selectable) return columns;
    const select: ColumnDef<T, unknown> = {
      id: "_select",
      enableSorting: false,
      enableHiding: false,
      header: ({ table }) => (
        <Checkbox
          aria-label={t("table.selectAll")}
          checked={table.getIsAllPageRowsSelected() ? true : table.getIsSomePageRowsSelected() ? "indeterminate" : false}
          onCheckedChange={(v) => table.toggleAllPageRowsSelected(!!v)}
        />
      ),
      cell: ({ row }) => (
        <Checkbox
          aria-label={t("table.selectRow")}
          checked={row.getIsSelected()}
          onClick={(e) => e.stopPropagation()}
          onCheckedChange={(v) => row.toggleSelected(!!v)}
        />
      ),
      meta: { className: "w-8" } satisfies ColumnMeta,
    };
    return [select, ...columns];
  }, [columns, selectable, t]);

  const table = useReactTable({
    data: data ?? [],
    columns: allColumns,
    getCoreRowModel: getCoreRowModel(),
    getRowId: props.getRowId,
    manualPagination: true,
    manualSorting: true,
    pageCount: pageCount(total, pageSize),
    // Sorting state is display-only (the server sorts); header clicks call onSortChange.
    state: {
      sorting: sortParamToSorting(props.sort),
      columnVisibility: visibility,
      rowSelection,
      pagination: { pageIndex: page - 1, pageSize },
    },
    onColumnVisibilityChange: (updater) => setVisibility(typeof updater === "function" ? updater(visibility) : updater),
    onRowSelectionChange: (updater) =>
      onRowSelectionChange?.(typeof updater === "function" ? updater(rowSelection) : updater),
    enableRowSelection: !!selectable,
  });

  const selectedIds = Object.keys(rowSelection).filter((k) => rowSelection[k]);
  const pages = pageCount(total, pageSize);
  const from = total === 0 ? 0 : (page - 1) * pageSize + 1;
  const to = Math.min(total, page * pageSize);
  const colCount = table.getVisibleLeafColumns().length;

  return (
    <div className="rounded-lg border bg-card">
      {selectable && selectedIds.length > 0 && props.bulkActions && (
        <div className="flex flex-wrap items-center gap-2 border-b bg-primary/5 px-3 py-2 text-sm">
          <span className="font-medium">{t("table.selected", { count: selectedIds.length })}</span>
          <Button variant="ghost" size="sm" onClick={() => onRowSelectionChange?.({})}>
            <X />
            {t("table.clearSelection")}
          </Button>
          <div className="ml-auto flex flex-wrap items-center gap-2">
            {props.bulkActions(selectedIds, () => onRowSelectionChange?.({}))}
          </div>
        </div>
      )}
      <Table className={cn(props.dense !== false && "text-[0.8125rem]")}>
        <TableHeader className="bg-muted/40">
          {table.getHeaderGroups().map((hg) => (
            <TableRow key={hg.id} className="hover:bg-transparent">
              {hg.headers.map((h) => {
                const meta = h.column.columnDef.meta as ColumnMeta | undefined;
                const sorted = h.column.getIsSorted();
                // Columns are display columns (cell renderers, no accessor), so sortability is
                // explicit: every column sorts server-side unless `enableSorting: false`.
                const canSort = h.column.columnDef.enableSorting !== false && h.column.id !== "_select" && !!props.onSortChange;
                const toggleSort = () => props.onSortChange?.(sorted === "asc" ? `-${h.column.id}` : h.column.id);
                return (
                  <TableHead
                    key={h.id}
                    className={cn("h-9 whitespace-nowrap px-3", meta?.className)}
                    aria-sort={sorted === "asc" ? "ascending" : sorted === "desc" ? "descending" : undefined}
                  >
                    {h.isPlaceholder ? null : canSort ? (
                      <button
                        type="button"
                        className="-ml-1 inline-flex items-center gap-1 rounded px-1 py-0.5 hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                        onClick={toggleSort}
                      >
                        {flexRender(h.column.columnDef.header, h.getContext())}
                        {sorted === "asc" ? (
                          <ArrowUp className="h-3.5 w-3.5" />
                        ) : sorted === "desc" ? (
                          <ArrowDown className="h-3.5 w-3.5" />
                        ) : (
                          <ArrowUpDown className="h-3.5 w-3.5 opacity-40" />
                        )}
                      </button>
                    ) : (
                      flexRender(h.column.columnDef.header, h.getContext())
                    )}
                  </TableHead>
                );
              })}
            </TableRow>
          ))}
        </TableHeader>
        <TableBody>
          {isLoading ? (
            Array.from({ length: Math.min(pageSize, 8) }).map((_, i) => (
              <TableRow key={`sk-${i}`} className="hover:bg-transparent">
                {Array.from({ length: colCount }).map((__, j) => (
                  <TableCell key={j} className="px-3 py-2.5">
                    <Skeleton className={cn("h-4", j === 0 ? "w-4" : "w-full max-w-[10rem]")} />
                  </TableCell>
                ))}
              </TableRow>
            ))
          ) : error ? (
            <TableRow className="hover:bg-transparent">
              <TableCell colSpan={colCount}>
                <ErrorState error={error} onRetry={props.onRetry} />
              </TableCell>
            </TableRow>
          ) : table.getRowModel().rows.length === 0 ? (
            <TableRow className="hover:bg-transparent">
              <TableCell colSpan={colCount}>{props.empty ?? <EmptyState title={t("table.empty")} />}</TableCell>
            </TableRow>
          ) : (
            table.getRowModel().rows.map((row) => (
              <TableRow
                key={row.id}
                data-state={row.getIsSelected() ? "selected" : undefined}
                className={cn(
                  props.onRowClick && "cursor-pointer focus-visible:bg-muted/60 focus-visible:outline-none",
                  props.activeRowId === row.id && "bg-muted/60",
                )}
                tabIndex={props.onRowClick ? 0 : undefined}
                onClick={props.onRowClick ? () => props.onRowClick?.(row.original) : undefined}
                onKeyDown={
                  props.onRowClick
                    ? (e) => {
                        if (e.key === "Enter" && e.target === e.currentTarget) props.onRowClick?.(row.original);
                        if (e.key === "ArrowDown") (e.currentTarget.nextElementSibling as HTMLElement | null)?.focus();
                        if (e.key === "ArrowUp") (e.currentTarget.previousElementSibling as HTMLElement | null)?.focus();
                        if (e.key === " " && selectable && e.target === e.currentTarget) {
                          e.preventDefault();
                          row.toggleSelected();
                        }
                      }
                    : undefined
                }
              >
                {row.getVisibleCells().map((cell) => {
                  const meta = cell.column.columnDef.meta as ColumnMeta | undefined;
                  return (
                    <TableCell key={cell.id} className={cn("px-3 py-2", meta?.className)}>
                      {flexRender(cell.column.columnDef.cell, cell.getContext())}
                    </TableCell>
                  );
                })}
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
      <div className="flex flex-wrap items-center justify-between gap-2 border-t px-3 py-2 text-sm text-muted-foreground">
        <div className="flex items-center gap-2">
          <span>
            {t("table.range", {
              from: formatNumber(from, i18n.language),
              to: formatNumber(to, i18n.language),
              total: formatNumber(total, i18n.language),
            })}
          </span>
          <ColumnChooser table={table} />
        </div>
        <div className="flex items-center gap-1">
          {props.onPageSizeChange && (
            <Select value={String(pageSize)} onValueChange={(v) => props.onPageSizeChange?.(Number(v))}>
              <SelectTrigger className="h-8 w-[5.5rem]" aria-label={t("table.pageSize")}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {PAGE_SIZES.map((s) => (
                  <SelectItem key={s} value={String(s)}>
                    {s}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          <Button variant="ghost" size="icon" className="h-8 w-8" disabled={page <= 1} onClick={() => props.onPageChange(1)} aria-label={t("table.first")}>
            <ChevronsLeft />
          </Button>
          <Button variant="ghost" size="icon" className="h-8 w-8" disabled={page <= 1} onClick={() => props.onPageChange(page - 1)} aria-label={t("table.prev")}>
            <ChevronLeft />
          </Button>
          <span className="px-1 tabular-nums">
            {page} / {pages}
          </span>
          <Button variant="ghost" size="icon" className="h-8 w-8" disabled={page >= pages} onClick={() => props.onPageChange(page + 1)} aria-label={t("table.next")}>
            <ChevronRight />
          </Button>
          <Button variant="ghost" size="icon" className="h-8 w-8" disabled={page >= pages} onClick={() => props.onPageChange(pages)} aria-label={t("table.last")}>
            <ChevronsRight />
          </Button>
        </div>
      </div>
    </div>
  );
}

function ColumnChooser<T>({ table }: { table: ReturnType<typeof useReactTable<T>> }) {
  const { t } = useTranslation();
  const hideable = table.getAllLeafColumns().filter((c) => c.getCanHide());
  if (hideable.length === 0) return null;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="sm" className="h-7 px-2">
          <Columns3 />
          <span className="hidden sm:inline">{t("table.columns")}</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        <DropdownMenuLabel>{t("table.columns")}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {hideable.map((c) => (
          <DropdownMenuCheckboxItem
            key={c.id}
            checked={c.getIsVisible()}
            onCheckedChange={(v) => c.toggleVisibility(!!v)}
            onSelect={(e) => e.preventDefault()}
          >
            {(c.columnDef.meta as ColumnMeta | undefined)?.label ?? c.id}
          </DropdownMenuCheckboxItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** Search box with "/" shortcut hint, used in table toolbars. */
export function SearchInput({
  value,
  onChange,
  placeholder,
  className,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  className?: string;
}) {
  const { t } = useTranslation();
  const ref = useSlashFocus<HTMLInputElement>();
  return (
    <div className={cn("relative w-full sm:w-64", className)}>
      <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
      <Input
        ref={ref}
        type="search"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder ?? t("common.search")}
        className="h-9 pl-8 pr-8"
        aria-label={placeholder ?? t("common.search")}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            onChange("");
            (e.target as HTMLInputElement).blur();
          }
        }}
      />
      <kbd className="pointer-events-none absolute right-2 top-1/2 hidden -translate-y-1/2 rounded border bg-muted px-1.5 text-[10px] text-muted-foreground sm:block">
        /
      </kbd>
    </div>
  );
}

/** Compact select used for filters; value "" = any. */
export function FilterSelect({
  value,
  onChange,
  options,
  placeholder,
  className,
}: {
  value: string;
  onChange: (v: string) => void;
  options: { value: string; label: ReactNode }[];
  placeholder: string;
  className?: string;
}) {
  const { t } = useTranslation();
  return (
    <Select value={value === "" ? "__any" : value} onValueChange={(v) => onChange(v === "__any" ? "" : v)}>
      <SelectTrigger className={cn("h-9 w-auto min-w-[7rem]", value && "border-primary/50 bg-primary/5", className)} aria-label={placeholder}>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="__any">
          {placeholder}: {t("common.any")}
        </SelectItem>
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value}>
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
