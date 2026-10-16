import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { z } from "zod";

import { ApiError } from "@/api/client";
import { userKeys, usersApi, type UserListItem } from "@/api/users";
import { Field, useToastError } from "@/components";
import { UserGroupPicker } from "@/components/pickers";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Switch, Textarea } from "@/components/ui/misc";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

export const USERNAME_RE = /^[A-Za-z0-9._@-]+$/;

/** Shared profile fields (create + edit). Messages are i18n keys. */
export const userProfileSchema = z.object({
  username: z.string().trim().min(1, "validation.required").max(128, "validation.tooLong").regex(USERNAME_RE, "users.usernameInvalid"),
  display_name: z.string().trim().max(128, "validation.tooLong"),
  email: z.union([z.literal(""), z.string().trim().email("validation.email")]),
  role: z.enum(["admin", "user"]),
  status: z.enum(["active", "disabled"]),
  user_group_id: z.number().nullable(),
  note: z.string().max(4096, "validation.tooLong"),
  totp_required: z.boolean(),
  email_verified: z.boolean(),
});

export const userCreateSchema = userProfileSchema.extend({
  password: z.union([z.literal(""), z.string().min(8, "validation.passwordMin")]),
});

export type UserProfileValues = z.infer<typeof userProfileSchema>;
export type UserCreateValues = z.infer<typeof userCreateSchema>;

export function UserFields({
  form,
  lastAdmin,
  groupLabel,
  create,
}: {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  form: ReturnType<typeof useForm<any>>;
  lastAdmin?: boolean;
  groupLabel?: string;
  create?: boolean;
}) {
  const { t } = useTranslation();
  const errors = form.formState.errors as Record<string, { message?: string } | undefined>;
  const err = (k: string) => (errors[k]?.message ? t(errors[k]!.message!) : undefined);
  const [gLabel, setGLabel] = useState(groupLabel);
  return (
    <div className="grid gap-3 sm:grid-cols-2">
      <Field label={t("users.username")} htmlFor="u-username" error={err("username")}>
        <Input id="u-username" autoComplete="off" {...form.register("username")} />
      </Field>
      <Field label={t("users.displayName")} htmlFor="u-display" error={err("display_name")}>
        <Input id="u-display" {...form.register("display_name")} />
      </Field>
      <Field label={t("users.email")} htmlFor="u-email" error={err("email")}>
        <Input id="u-email" type="email" {...form.register("email")} />
      </Field>
      {create && (
        <Field label={t("users.password")} htmlFor="u-password" error={err("password")} hint={t("users.passwordHint")}>
          <Input id="u-password" type="password" autoComplete="new-password" {...form.register("password")} />
        </Field>
      )}
      <Field
        label={t("users.role")}
        htmlFor="u-role"
        hint={lastAdmin ? <span className="text-amber-600 dark:text-amber-400">{t("users.lastAdminHint")}</span> : undefined}
      >
        <Controller
          control={form.control}
          name="role"
          render={({ field }) => (
            <Select value={field.value} onValueChange={field.onChange} disabled={lastAdmin}>
              <SelectTrigger id="u-role">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="user">{t("roles.user")}</SelectItem>
                <SelectItem value="admin">{t("roles.admin")}</SelectItem>
              </SelectContent>
            </Select>
          )}
        />
      </Field>
      <Field label={t("users.status")} htmlFor="u-status">
        <Controller
          control={form.control}
          name="status"
          render={({ field }) => (
            <Select value={field.value} onValueChange={field.onChange} disabled={lastAdmin}>
              <SelectTrigger id="u-status">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="active">{t("users.active")}</SelectItem>
                <SelectItem value="disabled">{t("users.disabled")}</SelectItem>
              </SelectContent>
            </Select>
          )}
        />
      </Field>
      <Field label={t("users.group")} htmlFor="u-group">
        <Controller
          control={form.control}
          name="user_group_id"
          render={({ field }) => (
            <UserGroupPicker
              id="u-group"
              value={field.value}
              valueLabel={gLabel}
              allowNone
              onChange={(id, g) => {
                field.onChange(id);
                setGLabel(g?.name);
              }}
            />
          )}
        />
      </Field>
      <div className="flex items-center justify-between gap-3 rounded-md border px-3 py-2 sm:mt-6">
        <label htmlFor="u-totp" className="text-sm">
          {t("users.require2fa")}
        </label>
        <Controller
          control={form.control}
          name="totp_required"
          render={({ field }) => <Switch id="u-totp" checked={field.value} onCheckedChange={field.onChange} />}
        />
      </div>
      <div className="flex items-center justify-between gap-3 rounded-md border px-3 py-2">
        <label htmlFor="u-email-verified" className="text-sm">
          {t("users.emailVerified")}
        </label>
        <Controller
          control={form.control}
          name="email_verified"
          render={({ field }) => <Switch id="u-email-verified" checked={field.value} onCheckedChange={field.onChange} />}
        />
      </div>
      <Field label={t("users.note")} htmlFor="u-note" className="sm:col-span-2" error={err("note")}>
        <Textarea id="u-note" rows={2} {...form.register("note")} />
      </Field>
    </div>
  );
}

/** Maps server field errors (`validation_failed`, `conflict`) back onto the form. */
export function applyServerErrors(form: { setError: (name: never, e: { message: string }) => void }, err: unknown): boolean {
  if (!(err instanceof ApiError)) return false;
  if (err.code === "conflict") {
    form.setError("username" as never, { message: "users.usernameTaken" });
    return true;
  }
  if (err.fields) {
    for (const [k, v] of Object.entries(err.fields)) form.setError(k as never, { message: v });
    return true;
  }
  return false;
}

export function CreateUserDialog({ open, onOpenChange, onCreated }: { open: boolean; onOpenChange: (o: boolean) => void; onCreated: (u: UserListItem) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const form = useForm<UserCreateValues>({
    resolver: zodResolver(userCreateSchema),
    defaultValues: {
      username: "",
      display_name: "",
      email: "",
      password: "",
      role: "user",
      status: "active",
      user_group_id: null,
      note: "",
      totp_required: false,
      email_verified: false,
    },
  });
  const create = useMutation({
    mutationFn: (v: UserCreateValues) =>
      usersApi.create({
        username: v.username,
        display_name: v.display_name,
        email: v.email || null,
        role: v.role,
        status: v.status,
        user_group_id: v.user_group_id,
        note: v.note,
        totp_required: v.totp_required,
        email_verified: v.email_verified && !!v.email,
        ...(v.password ? { password: v.password } : {}),
      }),
    onSuccess: (u) => {
      toast.success(t("users.createdToast", { name: u.username }));
      void qc.invalidateQueries({ queryKey: userKeys.all });
      form.reset();
      onOpenChange(false);
      onCreated(u);
    },
    onError: (e) => {
      if (!applyServerErrors(form, e)) toastError(e);
    },
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl" closeLabel={t("common.close")}>
        <form onSubmit={form.handleSubmit((v) => create.mutate(v))} noValidate className="space-y-4">
          <DialogHeader>
            <DialogTitle>{t("users.create")}</DialogTitle>
            <DialogDescription>{t("users.createDescription")}</DialogDescription>
          </DialogHeader>
          <UserFields form={form} create />
          <DialogFooter className="gap-2">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {t("common.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
