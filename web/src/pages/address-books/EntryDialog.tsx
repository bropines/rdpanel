import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown } from "lucide-react";
import { useEffect, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { z } from "zod";

import { addressBooksApi, bookKeys, type AddressBook, type Entry } from "@/api/addressBooks";
import { ApiError } from "@/api/client";
import { Field, TagChip, useToastError } from "@/components";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Checkbox, Switch, Textarea } from "@/components/ui/misc";
import { DEFAULT_TAG_COLOR } from "@/lib/argb";
import { cn } from "@/lib/utils";

export const RUSTDESK_ID_RE = /^[A-Za-z0-9_-]{1,100}$/;

export const entrySchema = z.object({
  rustdesk_id: z.string().trim().regex(RUSTDESK_ID_RE, "books.idInvalid"),
  alias: z.string().max(128, "validation.tooLong"),
  hostname: z.string().max(255, "validation.tooLong"),
  os_username: z.string().max(128, "validation.tooLong"),
  platform: z.string().max(64, "validation.tooLong"),
  note: z.string().max(4096, "validation.tooLong"),
  tags: z.array(z.string()),
  password: z.string().max(1024, "validation.tooLong"),
  clear_password: z.boolean(),
  login_name: z.string().max(128, "validation.tooLong"),
  rdp_port: z.union([z.literal(""), z.string().regex(/^\d{1,5}$/, "books.portInvalid")]),
  rdp_username: z.string().max(128, "validation.tooLong"),
  force_always_relay: z.boolean(),
  same_server: z.boolean(),
});
export type EntryValues = z.infer<typeof entrySchema>;

function toValues(e: Entry | null): EntryValues {
  return {
    rustdesk_id: e?.rustdesk_id ?? "",
    alias: e?.alias ?? "",
    hostname: e?.hostname ?? "",
    os_username: e?.os_username ?? "",
    platform: e?.platform ?? "",
    note: e?.note ?? "",
    tags: e?.tags ?? [],
    password: "",
    clear_password: false,
    login_name: e?.login_name ?? "",
    rdp_port: e?.rdp_port ?? "",
    rdp_username: e?.rdp_username ?? "",
    force_always_relay: e?.force_always_relay ?? false,
    same_server: e?.same_server ?? false,
  };
}

export function EntryDialog({
  book,
  entry,
  open,
  onOpenChange,
  readOnly,
}: {
  book: AddressBook;
  entry: Entry | null;
  open: boolean;
  onOpenChange: (o: boolean) => void;
  readOnly: boolean;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const [advanced, setAdvanced] = useState(false);
  const tags = useQuery({ queryKey: bookKeys.tags(book.id), queryFn: () => addressBooksApi.tags(book.id), enabled: open });
  const form = useForm<EntryValues>({ resolver: zodResolver(entrySchema), defaultValues: toValues(entry) });
  useEffect(() => {
    if (open) {
      form.reset(toValues(entry));
      setAdvanced(false);
    }
  }, [open, entry, form]);

  const save = useMutation({
    mutationFn: (v: EntryValues) => {
      const { clear_password, password, ...rest } = v;
      const body = { ...rest, note: v.note || null };
      if (entry) {
        return addressBooksApi.updateEntry(book.id, entry.id, {
          ...body,
          ...(clear_password ? { password: null } : password ? { password } : {}),
        });
      }
      return addressBooksApi.createEntry(book.id, { ...body, ...(password ? { password } : {}) });
    },
    onSuccess: () => {
      toast.success(t("common.saved"));
      void qc.invalidateQueries({ queryKey: ["address-books"] });
      onOpenChange(false);
    },
    onError: (e) => {
      if (e instanceof ApiError && e.code === "conflict") form.setError("rustdesk_id", { message: "books.idExists" });
      else toastError(e);
    },
  });

  const err = (k: keyof EntryValues) => {
    const m = form.formState.errors[k]?.message;
    return m ? t(m) : undefined;
  };
  const tagColor = (name: string) => tags.data?.items.find((x) => x.name === name)?.color_argb ?? DEFAULT_TAG_COLOR;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] max-w-2xl overflow-y-auto" closeLabel={t("common.close")}>
        <form onSubmit={form.handleSubmit((v) => save.mutate(v))} noValidate className="space-y-4">
          <DialogHeader>
            <DialogTitle>{entry ? t("books.editEntry") : t("books.newEntry")}</DialogTitle>
            <DialogDescription>{book.name}</DialogDescription>
          </DialogHeader>
          <fieldset disabled={readOnly} className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label={t("devices.rustdeskId")} htmlFor="e-id" error={err("rustdesk_id")}>
              <Input id="e-id" className="font-mono" autoFocus={!entry} {...form.register("rustdesk_id")} />
            </Field>
            <Field label={t("devices.alias")} htmlFor="e-alias" error={err("alias")}>
              <Input id="e-alias" {...form.register("alias")} />
            </Field>
            <Field label={t("devices.hostname")} htmlFor="e-host" error={err("hostname")}>
              <Input id="e-host" {...form.register("hostname")} />
            </Field>
            <Field label={t("devices.osUser")} htmlFor="e-user" error={err("os_username")}>
              <Input id="e-user" {...form.register("os_username")} />
            </Field>
            <Field label={t("books.platform")} htmlFor="e-platform" error={err("platform")}>
              <Input id="e-platform" placeholder="Windows / Linux / Mac OS / Android" {...form.register("platform")} />
            </Field>
            <Field
              label={t("books.password")}
              htmlFor="e-pass"
              hint={entry?.has_password ? t("books.passwordStored") : t("books.passwordHint")}
              error={err("password")}
            >
              <Input
                id="e-pass"
                type="password"
                autoComplete="new-password"
                placeholder={entry?.has_password ? "••••••••" : ""}
                disabled={form.watch("clear_password")}
                {...form.register("password")}
              />
              {entry?.has_password && (
                <label className="mt-1.5 flex items-center gap-2 text-xs text-muted-foreground">
                  <Checkbox checked={form.watch("clear_password")} onCheckedChange={(v) => form.setValue("clear_password", !!v)} />
                  {t("books.clearPassword")}
                </label>
              )}
            </Field>
            <Field label={t("books.tags")} htmlFor="e-tags" className="sm:col-span-2">
              <Controller
                control={form.control}
                name="tags"
                render={({ field }) => (
                  <div className="flex min-h-9 flex-wrap items-center gap-1.5 rounded-md border px-2 py-1.5">
                    {(tags.data?.items ?? []).map((tag) => {
                      const on = field.value.includes(tag.name);
                      return (
                        <button
                          key={tag.id}
                          type="button"
                          aria-pressed={on}
                          onClick={() => field.onChange(on ? field.value.filter((x) => x !== tag.name) : [...field.value, tag.name])}
                          className={cn("rounded-full", !on && "opacity-40 grayscale hover:opacity-80")}
                        >
                          <TagChip name={tag.name} color={tag.color_argb} />
                        </button>
                      );
                    })}
                    {field.value
                      .filter((n) => !tags.data?.items.some((x) => x.name === n))
                      .map((n) => (
                        <TagChip key={n} name={n} color={tagColor(n)} onRemove={() => field.onChange(field.value.filter((x) => x !== n))} />
                      ))}
                    <input
                      id="e-tags"
                      className="min-w-[6rem] flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
                      placeholder={t("books.addTag")}
                      onKeyDown={(e) => {
                        const v = e.currentTarget.value.trim();
                        if ((e.key === "Enter" || e.key === ",") && v) {
                          e.preventDefault();
                          if (!field.value.includes(v)) field.onChange([...field.value, v]);
                          e.currentTarget.value = "";
                        }
                      }}
                    />
                  </div>
                )}
              />
            </Field>
            <Field label={t("devices.note")} htmlFor="e-note" className="sm:col-span-2" error={err("note")}>
              <Textarea id="e-note" rows={2} {...form.register("note")} />
            </Field>
          </fieldset>
          <button
            type="button"
            className="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
            onClick={() => setAdvanced((a) => !a)}
            aria-expanded={advanced}
          >
            <ChevronDown className={cn("h-4 w-4 transition-transform", advanced && "rotate-180")} />
            {t("books.advanced")}
          </button>
          {advanced && (
            <fieldset disabled={readOnly} className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <Field label={t("books.loginName")} htmlFor="e-login" error={err("login_name")}>
                <Input id="e-login" {...form.register("login_name")} />
              </Field>
              <Field label={t("books.rdpPort")} htmlFor="e-rdpport" error={err("rdp_port")}>
                <Input id="e-rdpport" inputMode="numeric" {...form.register("rdp_port")} />
              </Field>
              <Field label={t("books.rdpUsername")} htmlFor="e-rdpuser" error={err("rdp_username")}>
                <Input id="e-rdpuser" {...form.register("rdp_username")} />
              </Field>
              <div className="space-y-2 sm:pt-6">
                <label className="flex items-center justify-between gap-2 text-sm">
                  {t("books.forceRelay")}
                  <Controller
                    control={form.control}
                    name="force_always_relay"
                    render={({ field }) => <Switch checked={field.value} onCheckedChange={field.onChange} />}
                  />
                </label>
                <label className="flex items-center justify-between gap-2 text-sm">
                  {t("books.sameServer")}
                  <Controller
                    control={form.control}
                    name="same_server"
                    render={({ field }) => <Switch checked={field.value} onCheckedChange={field.onChange} />}
                  />
                </label>
              </div>
            </fieldset>
          )}
          <DialogFooter className="gap-2">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {readOnly ? t("common.close") : t("common.cancel")}
            </Button>
            {!readOnly && (
              <Button type="submit" disabled={save.isPending}>
                {entry ? t("common.save") : t("common.create")}
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
