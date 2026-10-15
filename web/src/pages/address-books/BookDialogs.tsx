import { zodResolver } from "@hookform/resolvers/zod";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Plus, Trash2, Users, UserRound } from "lucide-react";
import { useEffect, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { z } from "zod";

import { addressBooksApi, bookKeys, type AddressBook, type Permission, type Tag } from "@/api/addressBooks";
import { ApiError } from "@/api/client";
import { devicesApi } from "@/api/devices";
import { EmptyState, Field, Mono, OnlineDot, TagChip, useToastError } from "@/components";
import { SearchInput } from "@/components/data-table";
import { UserGroupPicker, UserPicker } from "@/components/pickers";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Checkbox, Skeleton, Textarea } from "@/components/ui/misc";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { argbToHex, DEFAULT_TAG_COLOR, hexToArgb, TAG_PALETTE } from "@/lib/argb";
import { useDebounced } from "@/lib/hooks";
import { cn } from "@/lib/utils";
import { deviceName } from "@/pages/devices/DeviceDrawer";

/* ── Create / edit book ───────────────────────────────────────────────── */

const bookSchema = z.object({
  name: z.string().trim().min(1, "validation.required").max(128, "validation.tooLong"),
  note: z.string().max(4096, "validation.tooLong"),
  owner_user_id: z.number().nullable(),
  default_password: z.string().max(1024, "validation.tooLong"),
  clear_password: z.boolean(),
});
type BookValues = z.infer<typeof bookSchema>;

export function BookDialog({
  book,
  open,
  onOpenChange,
  isAdmin,
  onSaved,
}: {
  book: AddressBook | null;
  open: boolean;
  onOpenChange: (o: boolean) => void;
  isAdmin: boolean;
  onSaved?: (b: AddressBook) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const [ownerLabel, setOwnerLabel] = useState<string>();
  const form = useForm<BookValues>({
    resolver: zodResolver(bookSchema),
    defaultValues: { name: "", note: "", owner_user_id: null, default_password: "", clear_password: false },
  });
  useEffect(() => {
    if (open) {
      form.reset({ name: book?.name ?? "", note: book?.note ?? "", owner_user_id: null, default_password: "", clear_password: false });
      setOwnerLabel(undefined);
    }
  }, [open, book, form]);

  const save = useMutation({
    mutationFn: (v: BookValues) => {
      if (book) {
        return addressBooksApi.update(book.id, {
          name: v.name,
          note: v.note,
          ...(v.clear_password ? { default_password: null } : v.default_password ? { default_password: v.default_password } : {}),
        });
      }
      return addressBooksApi.create({
        name: v.name,
        note: v.note,
        ...(v.owner_user_id != null ? { owner_user_id: v.owner_user_id } : {}),
        ...(v.default_password ? { default_password: v.default_password } : {}),
      });
    },
    onSuccess: (b) => {
      toast.success(t("common.saved"));
      void qc.invalidateQueries({ queryKey: bookKeys.all });
      onOpenChange(false);
      onSaved?.(b);
    },
    onError: (e) => {
      if (e instanceof ApiError && e.code === "conflict") form.setError("name", { message: "books.nameTaken" });
      else toastError(e);
    },
  });
  const nameErr = form.formState.errors.name?.message;
  const personal = book?.is_personal;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")}>
        <form onSubmit={form.handleSubmit((v) => save.mutate(v))} noValidate className="space-y-4">
          <DialogHeader>
            <DialogTitle>{book ? t("books.editBook") : t("books.newBook")}</DialogTitle>
            <DialogDescription>{book ? null : t("books.newBookHint")}</DialogDescription>
          </DialogHeader>
          <Field label={t("books.name")} htmlFor="b-name" error={nameErr ? t(nameErr) : undefined}>
            <Input id="b-name" autoFocus {...form.register("name")} />
          </Field>
          {!book && isAdmin && (
            <Field label={t("books.owner")} htmlFor="b-owner" hint={t("books.ownerHint")}>
              <Controller
                control={form.control}
                name="owner_user_id"
                render={({ field }) => (
                  <UserPicker
                    id="b-owner"
                    value={field.value}
                    valueLabel={ownerLabel}
                    allowNone
                    onChange={(id, u) => {
                      field.onChange(id);
                      setOwnerLabel(u?.display_name);
                    }}
                  />
                )}
              />
            </Field>
          )}
          <Field label={t("books.note")} htmlFor="b-note">
            <Textarea id="b-note" rows={2} {...form.register("note")} />
          </Field>
          {!personal && (
            <Field
              label={t("books.defaultPassword")}
              htmlFor="b-pass"
              hint={book?.has_default_password ? t("books.defaultPasswordSet") : t("books.defaultPasswordHint")}
            >
              <Input
                id="b-pass"
                type="password"
                autoComplete="new-password"
                placeholder={book?.has_default_password ? "••••••••" : ""}
                disabled={form.watch("clear_password")}
                {...form.register("default_password")}
              />
              {book?.has_default_password && (
                <label className="mt-1.5 flex items-center gap-2 text-xs text-muted-foreground">
                  <Checkbox checked={form.watch("clear_password")} onCheckedChange={(v) => form.setValue("clear_password", !!v)} />
                  {t("books.clearPassword")}
                </label>
              )}
            </Field>
          )}
          <DialogFooter className="gap-2">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={save.isPending}>
              {book ? t("common.save") : t("common.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/* ── Share dialog ─────────────────────────────────────────────────────── */

const PERMISSIONS: Permission[] = ["read", "read_write", "full"];

function PermissionSelect({ value, onChange, id }: { value: Permission; onChange: (p: Permission) => void; id?: string }) {
  const { t } = useTranslation();
  return (
    <Select value={value} onValueChange={(v) => onChange(v as Permission)}>
      <SelectTrigger id={id} className="h-8 w-36">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {PERMISSIONS.map((p) => (
          <SelectItem key={p} value={p}>
            {t(`books.permission.${p}`)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

export function ShareDialog({ book, open, onOpenChange }: { book: AddressBook; open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const shares = useQuery({ queryKey: bookKeys.shares(book.id), queryFn: () => addressBooksApi.shares(book.id), enabled: open });
  const [type, setType] = useState<"user" | "group">("user");
  const [grantee, setGrantee] = useState<number | null>(null);
  const [granteeLabel, setGranteeLabel] = useState<string>();
  const [perm, setPerm] = useState<Permission>("read");
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: bookKeys.shares(book.id) });
    void qc.invalidateQueries({ queryKey: ["address-books", "list"] });
  };
  const add = useMutation({
    mutationFn: () => addressBooksApi.addShare(book.id, { grantee_type: type, grantee_id: grantee!, permission: perm }),
    onSuccess: () => {
      setGrantee(null);
      setGranteeLabel(undefined);
      invalidate();
    },
    onError: (e) => {
      if (e instanceof ApiError && e.code === "conflict") toast.error(t("books.alreadyShared"));
      else toastError(e);
    },
  });
  const change = useMutation({
    mutationFn: ({ id, p }: { id: number; p: Permission }) => addressBooksApi.updateShare(book.id, id, p),
    onSuccess: invalidate,
    onError: toastError,
  });
  const remove = useMutation({
    mutationFn: (id: number) => addressBooksApi.removeShare(book.id, id),
    onSuccess: invalidate,
    onError: toastError,
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl" closeLabel={t("common.close")}>
        <DialogHeader>
          <DialogTitle>{t("books.shareTitle", { name: book.name })}</DialogTitle>
          <DialogDescription>{t("books.shareHint")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-2 rounded-md border p-3 sm:flex-row sm:items-end">
          <div className="flex shrink-0 rounded-md border p-0.5">
            {(["user", "group"] as const).map((k) => (
              <button
                key={k}
                type="button"
                onClick={() => {
                  setType(k);
                  setGrantee(null);
                  setGranteeLabel(undefined);
                }}
                className={cn(
                  "inline-flex items-center gap-1 rounded px-2 py-1 text-xs",
                  type === k ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:bg-muted",
                )}
              >
                {k === "user" ? <UserRound className="h-3.5 w-3.5" /> : <Users className="h-3.5 w-3.5" />}
                {t(`books.grantee.${k}`)}
              </button>
            ))}
          </div>
          <div className="min-w-0 flex-1">
            {type === "user" ? (
              <UserPicker
                value={grantee}
                valueLabel={granteeLabel}
                onChange={(id, u) => {
                  setGrantee(id);
                  setGranteeLabel(u?.display_name);
                }}
              />
            ) : (
              <UserGroupPicker
                value={grantee}
                valueLabel={granteeLabel}
                onChange={(id, g) => {
                  setGrantee(id);
                  setGranteeLabel(g?.name);
                }}
              />
            )}
          </div>
          <PermissionSelect value={perm} onChange={setPerm} />
          <Button size="sm" className="h-8" disabled={grantee == null || add.isPending} onClick={() => add.mutate()}>
            <Plus />
            {t("books.addShare")}
          </Button>
        </div>
        <div className="max-h-72 overflow-y-auto">
          {shares.isLoading ? (
            <Skeleton className="h-20" />
          ) : !shares.data?.items.length ? (
            <EmptyState title={t("books.noShares")} className="py-6" />
          ) : (
            <ul className="divide-y rounded-md border">
              {shares.data.items.map((s) => (
                <li key={s.id} className="flex items-center gap-2 px-3 py-2 text-sm">
                  {s.grantee_type === "user" ? (
                    <UserRound className="h-4 w-4 text-muted-foreground" />
                  ) : (
                    <Users className="h-4 w-4 text-muted-foreground" />
                  )}
                  <span className="min-w-0 flex-1 truncate">{s.grantee.name}</span>
                  <PermissionSelect value={s.permission} onChange={(p) => change.mutate({ id: s.id, p })} />
                  <Button variant="ghost" size="icon" className="h-8 w-8" onClick={() => remove.mutate(s.id)} aria-label={t("books.removeShare")}>
                    <Trash2 />
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </div>
        <p className="text-xs text-muted-foreground">{t("books.permissionLegend")}</p>
      </DialogContent>
    </Dialog>
  );
}

/* ── Tag manager ──────────────────────────────────────────────────────── */

export function ColorPicker({ value, onChange }: { value: number; onChange: (argb: number) => void }) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {TAG_PALETTE.map((c) => (
        <button
          key={c}
          type="button"
          onClick={() => onChange(c)}
          className={cn("h-5 w-5 rounded-full ring-offset-2 ring-offset-background", value === c && "ring-2 ring-ring")}
          style={{ backgroundColor: argbToHex(c) }}
          aria-label={argbToHex(c)}
          aria-pressed={value === c}
        />
      ))}
      <label className="relative h-5 w-5 cursor-pointer overflow-hidden rounded-full border" title={t("books.customColor")}>
        <input
          type="color"
          value={argbToHex(value)}
          onChange={(e) => {
            const v = hexToArgb(e.target.value);
            if (v !== undefined) onChange(v);
          }}
          className="absolute -inset-2 h-9 w-9 cursor-pointer"
          aria-label={t("books.customColor")}
        />
      </label>
    </div>
  );
}

function TagRow({ book, tag, canEdit }: { book: AddressBook; tag: Tag; canEdit: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const [name, setName] = useState(tag.name);
  const [color, setColor] = useState(tag.color_argb);
  const [expanded, setExpanded] = useState(false);
  useEffect(() => {
    setName(tag.name);
    setColor(tag.color_argb);
  }, [tag]);
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: bookKeys.tags(book.id) });
    void qc.invalidateQueries({ queryKey: ["address-books", book.id, "entries"] });
  };
  const save = useMutation({
    mutationFn: () => addressBooksApi.updateTag(book.id, tag.id, { name: name.trim(), color_argb: color }),
    onSuccess: () => {
      setExpanded(false);
      invalidate();
    },
    onError: toastError,
  });
  const del = useMutation({ mutationFn: () => addressBooksApi.removeTag(book.id, tag.id), onSuccess: invalidate, onError: toastError });
  const dirty = name.trim() !== tag.name || color !== tag.color_argb;
  return (
    <li className="px-3 py-2">
      <div className="flex items-center gap-2">
        <button
          type="button"
          className="flex min-w-0 flex-1 items-center gap-2 text-left"
          onClick={() => canEdit && setExpanded((x) => !x)}
          disabled={!canEdit}
        >
          <TagChip name={tag.name} color={tag.color_argb} />
          <span className="text-xs text-muted-foreground">{t("books.entriesCount", { count: tag.entry_count })}</span>
        </button>
        {canEdit && (
          <Button variant="ghost" size="icon" className="h-7 w-7" onClick={() => del.mutate()} aria-label={t("common.delete")}>
            <Trash2 />
          </Button>
        )}
      </div>
      {expanded && (
        <div className="mt-2 space-y-2 rounded-md bg-muted/40 p-2">
          <Input value={name} onChange={(e) => setName(e.target.value)} className="h-8" aria-label={t("books.tagName")} />
          <ColorPicker value={color} onChange={setColor} />
          <div className="flex justify-end">
            <Button size="sm" disabled={!dirty || !name.trim() || save.isPending} onClick={() => save.mutate()}>
              <Check />
              {t("common.save")}
            </Button>
          </div>
        </div>
      )}
    </li>
  );
}

export function TagManagerDialog({
  book,
  open,
  onOpenChange,
  canEdit,
}: {
  book: AddressBook;
  open: boolean;
  onOpenChange: (o: boolean) => void;
  canEdit: boolean;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const tags = useQuery({ queryKey: bookKeys.tags(book.id), queryFn: () => addressBooksApi.tags(book.id), enabled: open });
  const [name, setName] = useState("");
  const [color, setColor] = useState(TAG_PALETTE[5] ?? DEFAULT_TAG_COLOR);
  const create = useMutation({
    mutationFn: () => addressBooksApi.createTag(book.id, { name: name.trim(), color_argb: color }),
    onSuccess: () => {
      setName("");
      void qc.invalidateQueries({ queryKey: bookKeys.tags(book.id) });
    },
    onError: (e) => {
      if (e instanceof ApiError && e.code === "conflict") toast.error(t("books.tagExists"));
      else toastError(e);
    },
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")}>
        <DialogHeader>
          <DialogTitle>{t("books.tagManager")}</DialogTitle>
          <DialogDescription>{t("books.tagManagerHint")}</DialogDescription>
        </DialogHeader>
        {canEdit && (
          <form
            className="space-y-2 rounded-md border p-3"
            onSubmit={(e) => {
              e.preventDefault();
              if (name.trim()) create.mutate();
            }}
          >
            <div className="flex gap-2">
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t("books.newTag")} className="h-8" maxLength={64} />
              <Button size="sm" type="submit" disabled={!name.trim() || create.isPending}>
                <Plus />
                {t("common.add")}
              </Button>
            </div>
            <ColorPicker value={color} onChange={setColor} />
          </form>
        )}
        <div className="max-h-80 overflow-y-auto">
          {tags.isLoading ? (
            <Skeleton className="h-20" />
          ) : !tags.data?.items.length ? (
            <EmptyState title={t("books.noTags")} className="py-6" />
          ) : (
            <ul className="divide-y rounded-md border">
              {tags.data.items.map((tag) => (
                <TagRow key={tag.id} book={book} tag={tag} canEdit={canEdit} />
              ))}
            </ul>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

/* ── Add entries from devices ─────────────────────────────────────────── */

export function AddFromDevicesDialog({
  book,
  open,
  onOpenChange,
  isAdmin,
}: {
  book: AddressBook;
  open: boolean;
  onOpenChange: (o: boolean) => void;
  isAdmin: boolean;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const [q, setQ] = useState("");
  const dq = useDebounced(q, 250);
  const [picked, setPicked] = useState<Set<number>>(new Set());
  const [tags, setTags] = useState("");
  useEffect(() => {
    if (!open) {
      setPicked(new Set());
      setQ("");
      setTags("");
    }
  }, [open]);
  const devices = useQuery({
    queryKey: ["devices", "list", { q: dq, page_size: 100, picker: true, mine: !isAdmin }],
    queryFn: () => devicesApi.list({ q: dq || undefined, page_size: 100, sort: "-online", ...(isAdmin ? {} : { owner: "me" }) }),
    enabled: open,
    placeholderData: keepPreviousData,
  });
  const add = useMutation({
    mutationFn: () =>
      addressBooksApi.addFromDevices(book.id, {
        device_ids: [...picked],
        tags: tags.split(",").map((s) => s.trim()).filter(Boolean),
      }),
    onSuccess: (r) => {
      toast.success(t("devices.addedToBook", { count: r.affected, skipped: r.skipped ?? 0 }));
      void qc.invalidateQueries({ queryKey: ["address-books"] });
      onOpenChange(false);
    },
    onError: toastError,
  });
  const items = devices.data?.items ?? [];
  const toggle = (id: number) =>
    setPicked((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl" closeLabel={t("common.close")}>
        <DialogHeader>
          <DialogTitle>{t("books.addFromDevices")}</DialogTitle>
          <DialogDescription>{t("books.addFromDevicesHint", { name: book.name })}</DialogDescription>
        </DialogHeader>
        <SearchInput value={q} onChange={setQ} className="sm:w-full" placeholder={t("devices.searchPlaceholder")} />
        <div className="max-h-80 overflow-y-auto rounded-md border">
          {devices.isLoading ? (
            <Skeleton className="m-3 h-20" />
          ) : items.length === 0 ? (
            <EmptyState title={t("common.noResults")} className="py-6" />
          ) : (
            <ul className="divide-y">
              {items.map((d) => (
                <li key={d.id}>
                  <label className="flex cursor-pointer items-center gap-3 px-3 py-2 text-sm hover:bg-muted/50">
                    <Checkbox checked={picked.has(d.id)} onCheckedChange={() => toggle(d.id)} />
                    <OnlineDot online={d.online} />
                    <span className="min-w-0 flex-1 truncate font-medium">{deviceName(d)}</span>
                    <Mono className="text-muted-foreground">{d.rustdesk_id}</Mono>
                    {d.owner && <Badge variant="outline" className="hidden sm:inline-flex">{d.owner.username}</Badge>}
                  </label>
                </li>
              ))}
            </ul>
          )}
        </div>
        <Field label={t("books.tags")} htmlFor="afd-tags" hint={t("books.tagsHint")}>
          <Input id="afd-tags" value={tags} onChange={(e) => setTags(e.target.value)} />
        </Field>
        <DialogFooter className="items-center gap-2">
          <span className="mr-auto text-sm text-muted-foreground">{t("table.selected", { count: picked.size })}</span>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button disabled={picked.size === 0 || add.isPending} onClick={() => add.mutate()}>
            <Plus />
            {t("books.addSelected", { count: picked.size })}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/* ── Bulk tag edit ────────────────────────────────────────────────────── */

export function BulkTagsDialog({
  book,
  entryIds,
  open,
  onOpenChange,
  onDone,
}: {
  book: AddressBook;
  entryIds: number[];
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const tags = useQuery({ queryKey: bookKeys.tags(book.id), queryFn: () => addressBooksApi.tags(book.id), enabled: open });
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [mode, setMode] = useState<"add_tags" | "remove_tags" | "set_tags">("add_tags");
  useEffect(() => {
    if (!open) setPicked(new Set());
  }, [open]);
  const run = useMutation({
    mutationFn: () => addressBooksApi.bulkEntries(book.id, { action: mode, entry_ids: entryIds, tags: [...picked] }),
    onSuccess: (r) => {
      toast.success(t("common.updatedCount", { count: r.affected }));
      void qc.invalidateQueries({ queryKey: ["address-books", book.id] });
      onOpenChange(false);
      onDone();
    },
    onError: toastError,
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")}>
        <DialogHeader>
          <DialogTitle>{t("books.bulkTags")}</DialogTitle>
          <DialogDescription>{t("books.bulkTagsHint", { count: entryIds.length })}</DialogDescription>
        </DialogHeader>
        <Select value={mode} onValueChange={(v) => setMode(v as typeof mode)}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="add_tags">{t("books.tagMode.add_tags")}</SelectItem>
            <SelectItem value="remove_tags">{t("books.tagMode.remove_tags")}</SelectItem>
            <SelectItem value="set_tags">{t("books.tagMode.set_tags")}</SelectItem>
          </SelectContent>
        </Select>
        <div className="flex flex-wrap gap-2">
          {tags.data?.items.map((tag) => {
            const on = picked.has(tag.name);
            return (
              <button
                key={tag.id}
                type="button"
                aria-pressed={on}
                onClick={() =>
                  setPicked((s) => {
                    const n = new Set(s);
                    if (on) n.delete(tag.name);
                    else n.add(tag.name);
                    return n;
                  })
                }
                className={cn("rounded-full ring-offset-2 ring-offset-background", on ? "ring-2 ring-ring" : "opacity-60 hover:opacity-100")}
              >
                <TagChip name={tag.name} color={tag.color_argb} />
              </button>
            );
          })}
          {tags.data && tags.data.items.length === 0 && <p className="text-sm text-muted-foreground">{t("books.noTags")}</p>}
        </div>
        <DialogFooter className="gap-2">
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button disabled={run.isPending || (picked.size === 0 && mode !== "set_tags")} onClick={() => run.mutate()}>
            {t("common.apply")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
