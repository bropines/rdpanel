import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ColumnDef, RowSelectionState } from "@tanstack/react-table";
import {
  BookOpen,
  BookUser,
  ChevronLeft,
  MonitorDown,
  MoreHorizontal,
  Pencil,
  Plus,
  Share2,
  Tags,
  Trash2,
  X,
} from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { addressBooksApi, bookKeys, type AddressBook, type Entry, type EntryListQuery } from "@/api/addressBooks";
import { ConfirmDialog, CopyButton, EmptyState, ErrorState, Mono, OnlineDot, PageHeader, RelativeTime, TagChip, useToastError } from "@/components";
import { DataTable, SearchInput, type ColumnMeta } from "@/components/data-table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/misc";
import { DEFAULT_TAG_COLOR } from "@/lib/argb";
import { formatRustdeskId, userLabel } from "@/lib/format";
import { useDebounced } from "@/lib/hooks";
import { useIsAdmin } from "@/lib/session";
import { useTableSearch } from "@/lib/use-table";
import { cn } from "@/lib/utils";
import { AddFromDevicesDialog, BookDialog, BulkTagsDialog, ShareDialog, TagManagerDialog } from "@/pages/address-books/BookDialogs";
import { EntryDialog } from "@/pages/address-books/EntryDialog";

/** `tags=a,b` (URL) ↔ `tag=a&tag=b` (API). Exported for tests. */
export function parseTagFilter(v: unknown): string[] {
  if (typeof v !== "string" || !v) return [];
  return v.split(",").map((s) => s.trim()).filter(Boolean);
}

const canWrite = (b: AddressBook) => b.permission !== "read";
const canManage = (b: AddressBook) => b.permission === "full";

function BookList({
  scope,
  selectedId,
  onSelect,
  onCreate,
}: {
  scope: "accessible" | "all";
  selectedId: number | undefined;
  onSelect: (b: AddressBook) => void;
  onCreate: () => void;
}) {
  const { t } = useTranslation();
  const [q, setQ] = useState("");
  const dq = useDebounced(q, 250);
  const query = { q: dq || undefined, scope, page_size: 200, sort: "name" as const };
  const books = useQuery({ queryKey: bookKeys.list(query), queryFn: () => addressBooksApi.list(query), placeholderData: keepPreviousData });
  const items = books.data?.items ?? [];
  const personal = items.filter((b) => b.is_personal);
  const shared = items.filter((b) => !b.is_personal);

  const Group = ({ title, list }: { title: string; list: AddressBook[] }) =>
    list.length === 0 ? null : (
      <div>
        <div className="px-2 pb-1 pt-3 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">{title}</div>
        <ul className="space-y-0.5">
          {list.map((b) => (
            <li key={b.id}>
              <button
                type="button"
                onClick={() => onSelect(b)}
                aria-current={b.id === selectedId ? "true" : undefined}
                className={cn(
                  "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring",
                  b.id === selectedId && "bg-muted font-medium",
                )}
              >
                {b.is_personal ? (
                  <BookUser className="h-4 w-4 shrink-0 text-muted-foreground" />
                ) : (
                  <BookOpen className="h-4 w-4 shrink-0 text-muted-foreground" />
                )}
                <span className="min-w-0 flex-1">
                  <span className="block truncate">{b.name}</span>
                  <span className="block truncate text-xs font-normal text-muted-foreground">
                    {userLabel(b.owner)}
                    {b.share_count > 0 && ` · ${t("books.sharesCount", { count: b.share_count })}`}
                  </span>
                </span>
                <span className="text-xs tabular-nums text-muted-foreground">{b.entry_count}</span>
              </button>
            </li>
          ))}
        </ul>
      </div>
    );

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center gap-2 border-b p-2">
        <SearchInput value={q} onChange={setQ} className="sm:w-full" placeholder={t("books.searchBooks")} />
        <Button size="icon" variant="outline" className="h-9 w-9 shrink-0" onClick={onCreate} aria-label={t("books.newBook")} title={t("books.newBook")}>
          <Plus />
        </Button>
      </div>
      <div className="flex-1 overflow-y-auto p-2 pt-0">
        {books.isLoading ? (
          <div className="space-y-2 pt-3">
            {Array.from({ length: 6 }).map((_, i) => (
              <Skeleton key={i} className="h-9" />
            ))}
          </div>
        ) : books.isError ? (
          <ErrorState error={books.error} onRetry={() => void books.refetch()} />
        ) : items.length === 0 ? (
          <EmptyState title={t("books.noBooks")} className="py-8" />
        ) : (
          <>
            <Group title={t("books.personalBooks")} list={personal} />
            <Group title={t("books.sharedBooks")} list={shared} />
          </>
        )}
      </div>
    </div>
  );
}

function BookView({ book, onBack, onDeleted, isAdmin }: { book: AddressBook; onBack: () => void; onDeleted: () => void; isAdmin: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const table = useTableSearch({ sort: "alias", filterKeys: ["tags"], prefix: "e_" });
  const { state, update } = table;
  const tagFilter = parseTagFilter(state.filters.tags);
  const query = useMemo<EntryListQuery>(() => {
    const { tags: _t, ...rest } = table.query;
    return { ...(rest as EntryListQuery), tag: tagFilter.length ? tagFilter : undefined };
  }, [table.query, tagFilter]);
  const entries = useQuery({
    queryKey: bookKeys.entries(book.id, query),
    queryFn: () => addressBooksApi.entries(book.id, query),
    placeholderData: keepPreviousData,
  });
  const tags = useQuery({ queryKey: bookKeys.tags(book.id), queryFn: () => addressBooksApi.tags(book.id) });
  const colorOf = (name: string) => tags.data?.items.find((x) => x.name === name)?.color_argb ?? DEFAULT_TAG_COLOR;
  const [selection, setSelection] = useState<RowSelectionState>({});
  const [dialog, setDialog] = useState<"edit" | "share" | "tags" | "devices" | "bulkTags" | "delete" | "deleteEntries" | null>(null);
  const [entry, setEntry] = useState<Entry | null>(null);
  const [entryOpen, setEntryOpen] = useState(false);
  const selectedIds = Object.keys(selection).filter((k) => selection[k]).map(Number);
  const writable = canWrite(book);

  const delBook = useMutation({
    mutationFn: () => addressBooksApi.remove(book.id),
    onSuccess: () => {
      toast.success(t("books.deleted"));
      void qc.invalidateQueries({ queryKey: bookKeys.all });
      onDeleted();
    },
    onError: toastError,
  });
  const delEntries = useMutation({
    mutationFn: (ids: number[]) => addressBooksApi.bulkEntries(book.id, { action: "delete", entry_ids: ids }),
    onSuccess: (r) => {
      toast.success(t("books.entriesDeleted", { count: r.affected }));
      setSelection({});
      setDialog(null);
      void qc.invalidateQueries({ queryKey: ["address-books"] });
    },
    onError: toastError,
  });

  const columns = useMemo<ColumnDef<Entry, unknown>[]>(
    () => [
      {
        id: "alias",
        header: t("books.entry"),
        enableHiding: false,
        meta: { label: t("books.entry"), className: "min-w-[11rem]" } satisfies ColumnMeta,
        cell: ({ row: { original: e } }) => (
          <div className="flex items-center gap-2.5">
            <OnlineDot online={e.online ?? false} className={e.online === null || e.online === undefined ? "opacity-40" : undefined} />
            <div className="min-w-0">
              <div className="truncate font-medium">{e.alias || e.hostname || e.rustdesk_id}</div>
              {e.hostname && e.alias && <div className="truncate text-xs text-muted-foreground">{e.hostname}</div>}
            </div>
          </div>
        ),
      },
      {
        id: "rustdesk_id",
        header: t("devices.rustdeskId"),
        meta: { label: t("devices.rustdeskId") } satisfies ColumnMeta,
        cell: ({ row: { original: e } }) => (
          <span className="inline-flex items-center gap-0.5 whitespace-nowrap">
            <Mono>{formatRustdeskId(e.rustdesk_id)}</Mono>
            <CopyButton value={e.rustdesk_id} label={t("devices.copyId")} />
          </span>
        ),
      },
      {
        id: "tags",
        header: t("books.tags"),
        enableSorting: false,
        meta: { label: t("books.tags") } satisfies ColumnMeta,
        cell: ({ row: { original: e } }) => (
          <div className="flex max-w-[18rem] flex-wrap gap-1">
            {e.tags.length === 0 ? <span className="text-muted-foreground">—</span> : e.tags.map((n) => <TagChip key={n} name={n} color={colorOf(n)} />)}
          </div>
        ),
      },
      {
        id: "platform",
        header: t("books.platform"),
        enableSorting: false,
        meta: { label: t("books.platform") } satisfies ColumnMeta,
        cell: ({ row: { original: e } }) => e.platform || <span className="text-muted-foreground">—</span>,
      },
      {
        id: "os_username",
        header: t("devices.osUser"),
        enableSorting: false,
        meta: { label: t("devices.osUser"), defaultHidden: true } satisfies ColumnMeta,
        cell: ({ row: { original: e } }) => e.os_username || <span className="text-muted-foreground">—</span>,
      },
      {
        id: "password",
        header: t("books.password"),
        enableSorting: false,
        meta: { label: t("books.password") } satisfies ColumnMeta,
        cell: ({ row: { original: e } }) =>
          e.has_password ? <Badge variant="outline">{t("books.stored")}</Badge> : <span className="text-muted-foreground">—</span>,
      },
      {
        id: "note",
        header: t("devices.note"),
        enableSorting: false,
        meta: { label: t("devices.note"), defaultHidden: true } satisfies ColumnMeta,
        cell: ({ row: { original: e } }) => <span className="line-clamp-1 text-muted-foreground">{e.note || "—"}</span>,
      },
      {
        id: "updated_at",
        header: t("books.updated"),
        meta: { label: t("books.updated") } satisfies ColumnMeta,
        cell: ({ row: { original: e } }) => <RelativeTime iso={e.updated_at} className="text-muted-foreground" />,
      },
    ],
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [t, tags.data],
  );

  const toggleTag = (name: string) => {
    const next = tagFilter.includes(name) ? tagFilter.filter((x) => x !== name) : [...tagFilter, name];
    update({ filters: { tags: next.join(",") } });
  };

  return (
    <div className="flex h-full min-w-0 flex-col">
      <div className="flex flex-wrap items-start gap-3 border-b p-3">
        <Button variant="ghost" size="icon" className="h-8 w-8 lg:hidden" onClick={onBack} aria-label={t("common.back")}>
          <ChevronLeft />
        </Button>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="truncate text-lg font-semibold">{book.name}</h2>
            <Badge variant={book.is_personal ? "secondary" : "outline"}>{book.is_personal ? t("books.personal") : t("books.shared")}</Badge>
            <Badge variant="outline" className="font-normal">
              {t(`books.permission.${book.permission}`)}
            </Badge>
          </div>
          <div className="mt-0.5 text-xs text-muted-foreground">
            {t("books.ownedBy", { name: userLabel(book.owner) })} · {t("books.entriesCount", { count: book.entry_count })}
            {book.note && ` · ${book.note}`}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {!book.is_personal && canManage(book) && (
            <Button size="sm" variant="outline" onClick={() => setDialog("share")}>
              <Share2 />
              {t("books.share")}
              {book.share_count > 0 && <span className="text-xs text-muted-foreground">{book.share_count}</span>}
            </Button>
          )}
          <Button size="sm" variant="outline" onClick={() => setDialog("tags")}>
            <Tags />
            {t("books.tagsButton")}
          </Button>
          {canManage(book) && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button size="sm" variant="outline" aria-label={t("common.more")}>
                  <MoreHorizontal />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={() => setDialog("edit")}>
                  <Pencil />
                  {t("books.editBook")}
                </DropdownMenuItem>
                {!book.is_personal && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem className="text-destructive focus:text-destructive" onSelect={() => setDialog("delete")}>
                      <Trash2 />
                      {t("books.deleteBook")}
                    </DropdownMenuItem>
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2 p-3">
        <SearchInput value={table.text} onChange={table.setText} placeholder={t("books.searchEntries")} />
        {writable && (
          <>
            <Button
              size="sm"
              onClick={() => {
                setEntry(null);
                setEntryOpen(true);
              }}
            >
              <Plus />
              {t("books.newEntry")}
            </Button>
            <Button size="sm" variant="outline" onClick={() => setDialog("devices")}>
              <MonitorDown />
              {t("books.addFromDevices")}
            </Button>
          </>
        )}
      </div>
      {(tags.data?.items.length ?? 0) > 0 && (
        <div className="flex flex-wrap items-center gap-1.5 px-3 pb-3">
          {tags.data!.items.map((tag) => {
            const on = tagFilter.includes(tag.name);
            return (
              <button
                key={tag.id}
                type="button"
                aria-pressed={on}
                onClick={() => toggleTag(tag.name)}
                className={cn(
                  "rounded-full ring-offset-1 ring-offset-background transition-opacity",
                  on ? "ring-2 ring-ring" : tagFilter.length > 0 ? "opacity-50 hover:opacity-100" : "",
                )}
              >
                <TagChip name={`${tag.name} · ${tag.entry_count}`} color={tag.color_argb} />
              </button>
            );
          })}
          {tagFilter.length > 0 && (
            <Button size="sm" variant="ghost" className="h-6 px-2 text-xs" onClick={() => update({ filters: { tags: "" } })}>
              <X />
              {t("common.reset")}
            </Button>
          )}
        </div>
      )}

      <div className="px-3 pb-3">
        <DataTable
          id="ab-entries"
          columns={columns}
          data={entries.data?.items}
          total={entries.data?.total}
          isLoading={entries.isLoading}
          error={entries.error}
          onRetry={() => void entries.refetch()}
          page={state.page}
          pageSize={state.pageSize}
          onPageChange={(page) => update({ page })}
          onPageSizeChange={(pageSize) => update({ pageSize })}
          sort={state.sort}
          onSortChange={(sort) => update({ sort: sort ?? "alias" })}
          getRowId={(e) => String(e.id)}
          selectable={writable}
          rowSelection={selection}
          onRowSelectionChange={setSelection}
          onRowClick={(e) => {
            setEntry(e);
            setEntryOpen(true);
          }}
          empty={
            <EmptyState
              icon={BookOpen}
              title={state.q || tagFilter.length ? t("books.noEntriesFiltered") : t("books.noEntries")}
              description={writable && !state.q ? t("books.noEntriesHint") : undefined}
            />
          }
          bulkActions={() => (
            <>
              <Button size="sm" variant="outline" onClick={() => setDialog("bulkTags")}>
                <Tags />
                {t("books.bulkTags")}
              </Button>
              <Button size="sm" variant="outline" className="text-destructive" onClick={() => setDialog("deleteEntries")}>
                <Trash2 />
                {t("common.delete")}
              </Button>
            </>
          )}
        />
      </div>

      <EntryDialog book={book} entry={entry} open={entryOpen} onOpenChange={setEntryOpen} readOnly={!writable} />
      <BookDialog book={book} open={dialog === "edit"} onOpenChange={(o) => setDialog(o ? "edit" : null)} isAdmin={isAdmin} />
      <ShareDialog book={book} open={dialog === "share"} onOpenChange={(o) => setDialog(o ? "share" : null)} />
      <TagManagerDialog book={book} open={dialog === "tags"} onOpenChange={(o) => setDialog(o ? "tags" : null)} canEdit={writable} />
      <AddFromDevicesDialog book={book} open={dialog === "devices"} onOpenChange={(o) => setDialog(o ? "devices" : null)} isAdmin={isAdmin} />
      <BulkTagsDialog
        book={book}
        entryIds={selectedIds}
        open={dialog === "bulkTags"}
        onOpenChange={(o) => setDialog(o ? "bulkTags" : null)}
        onDone={() => setSelection({})}
      />
      <ConfirmDialog
        open={dialog === "delete"}
        onOpenChange={(o) => !o && setDialog(null)}
        title={t("books.deleteTitle", { name: book.name })}
        description={t("books.deleteDescription", { count: book.entry_count })}
        confirmLabel={t("books.deleteBook")}
        destructive
        confirmText={book.entry_count > 0 ? book.name : undefined}
        pending={delBook.isPending}
        onConfirm={() => delBook.mutate()}
      />
      <ConfirmDialog
        open={dialog === "deleteEntries"}
        onOpenChange={(o) => !o && setDialog(null)}
        title={t("books.deleteEntriesTitle", { count: selectedIds.length })}
        description={t("books.deleteEntriesDescription")}
        confirmLabel={t("common.delete")}
        destructive
        pending={delEntries.isPending}
        onConfirm={() => delEntries.mutate(selectedIds)}
      />
    </div>
  );
}

function AddressBooksView({ my }: { my: boolean }) {
  const { t } = useTranslation();
  const isAdmin = useIsAdmin();
  const scope = isAdmin && !my ? "all" : "accessible";
  const table = useTableSearch({});
  const selectedId = Number(table.raw.book) || undefined;
  const [creating, setCreating] = useState(false);
  const book = useQuery({
    queryKey: ["address-books", "detail", selectedId],
    queryFn: () => addressBooksApi.get(selectedId!),
    enabled: selectedId !== undefined,
  });
  // Keep the selected book fresh when lists change (entry counts, share counts).
  const select = (b: AddressBook | undefined) =>
    table.setSearch({ book: b?.id, e_q: undefined, e_tags: undefined, e_page: undefined, e_sort: undefined });

  return (
    <div>
      <PageHeader title={my ? t("nav.myAddressBooks") : t("nav.addressBooks")} description={my ? t("books.myDescription") : t("books.description")} />
      <div className="grid min-h-[70vh] overflow-hidden rounded-lg border bg-card lg:grid-cols-[18rem_1fr]">
        <aside className={cn("border-b lg:border-b-0 lg:border-r", selectedId !== undefined && "hidden lg:block")}>
          <BookList scope={scope} selectedId={selectedId} onSelect={select} onCreate={() => setCreating(true)} />
        </aside>
        <section className={cn("min-w-0", selectedId === undefined && "hidden lg:block")}>
          {selectedId === undefined ? (
            <EmptyState icon={BookOpen} title={t("books.selectBook")} description={t("books.selectBookHint")} className="h-full" />
          ) : book.isLoading ? (
            <div className="space-y-3 p-4">
              <Skeleton className="h-8 w-64" />
              <Skeleton className="h-64" />
            </div>
          ) : book.isError ? (
            <ErrorState error={book.error} onRetry={() => void book.refetch()} />
          ) : book.data ? (
            <BookView key={book.data.id} book={book.data} isAdmin={isAdmin} onBack={() => select(undefined)} onDeleted={() => select(undefined)} />
          ) : null}
        </section>
      </div>
      <BookDialog book={null} open={creating} onOpenChange={setCreating} isAdmin={isAdmin && !my} onSaved={(b) => select(b)} />
    </div>
  );
}

export function AddressBooksPage() {
  return <AddressBooksView my={false} />;
}

export function MyAddressBooksPage() {
  return <AddressBooksView my />;
}
