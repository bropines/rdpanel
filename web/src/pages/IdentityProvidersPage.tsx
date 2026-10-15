import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, FlaskConical, KeyRound, Pencil, Plus, Trash2, XCircle } from "lucide-react";
import { useEffect, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { z } from "zod";

import { ApiError } from "@/api/client";
import {
  identityProviders,
  type IdentityProvider,
  type IdentityProviderInput,
  type IdentityProviderPatch,
  type IdentityProviderTestResult,
  type IdpKind,
} from "@/api/identityProviders";
import { ConfirmDialog, CopyButton, EmptyState, ErrorState, Field, Mono, PageHeader, useToastError } from "@/components";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton, Switch } from "@/components/ui/misc";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

const DEFAULT_SCOPES: Record<IdpKind, string> = {
  github: "read:user, user:email",
  google: "openid, email, profile",
  oidc: "openid, email, profile",
};

export const idpSchema = z
  .object({
    kind: z.enum(["github", "google", "oidc"]),
    slug: z.string().trim().regex(/^[a-z0-9][a-z0-9_-]{0,63}$/, "idp.slugInvalid"),
    display_name: z.string().trim().min(1, "validation.required").max(64, "validation.tooLong"),
    client_id: z.string().trim().min(1, "validation.required"),
    client_secret: z.string(),
    issuer: z.string().trim(),
    scopes: z.string(),
    pkce_enabled: z.boolean(),
    pkce_method: z.enum(["S256", "plain"]),
    auto_register: z.boolean(),
    link_by_verified_email: z.boolean(),
    enabled: z.boolean(),
    sort_order: z.string().regex(/^-?\d+$/, "validation.integer"),
    has_secret: z.boolean(),
  })
  .superRefine((v, ctx) => {
    if (v.kind === "oidc") {
      let ok = false;
      try {
        ok = new URL(v.issuer).protocol === "https:";
      } catch {
        ok = false;
      }
      if (!ok) ctx.addIssue({ code: "custom", path: ["issuer"], message: "idp.issuerInvalid" });
    }
    if (!v.has_secret && !v.client_secret) ctx.addIssue({ code: "custom", path: ["client_secret"], message: "validation.required" });
  });
export type IdpValues = z.infer<typeof idpSchema>;

function toValues(p: IdentityProvider | null): IdpValues {
  const kind: IdpKind = p && p.kind !== "linuxdo" ? p.kind : "oidc";
  return {
    kind,
    slug: p?.slug ?? "",
    display_name: p?.display_name ?? "",
    client_id: p?.client_id ?? "",
    client_secret: "",
    issuer: p?.issuer ?? "",
    scopes: p ? p.scopes.join(", ") : DEFAULT_SCOPES[kind],
    pkce_enabled: p?.pkce_enabled ?? true,
    pkce_method: p?.pkce_method ?? "S256",
    auto_register: p?.auto_register ?? false,
    link_by_verified_email: p?.link_by_verified_email ?? false,
    enabled: p?.enabled ?? true,
    sort_order: String(p?.sort_order ?? 0),
    has_secret: p?.has_client_secret ?? false,
  };
}

/**
 * Form → API body; the secret is only sent when typed (write-only, omitted =
 * keep). Editing sends a PATCH body without slug/kind. Exported for tests.
 */
export function toInput(v: IdpValues, editing: true): IdentityProviderPatch;
export function toInput(v: IdpValues, editing: false): IdentityProviderInput;
export function toInput(v: IdpValues, editing: boolean): IdentityProviderInput | IdentityProviderPatch {
  const body: IdentityProviderPatch = {
    display_name: v.display_name.trim(),
    client_id: v.client_id.trim(),
    issuer: v.kind === "oidc" ? v.issuer.trim() : null,
    scopes: v.scopes.split(/[\s,]+/).filter(Boolean),
    pkce_enabled: v.pkce_enabled,
    pkce_method: v.pkce_method,
    auto_register: v.auto_register,
    link_by_verified_email: v.link_by_verified_email,
    enabled: v.enabled,
    sort_order: Number(v.sort_order),
  };
  if (v.client_secret) body.client_secret = v.client_secret;
  if (editing) return body;
  return { ...body, client_id: v.client_id.trim(), kind: v.kind, slug: v.slug.trim() };
}

function ProviderDialog({ provider, open, onOpenChange }: { provider: IdentityProvider | null; open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const form = useForm<IdpValues>({ resolver: zodResolver(idpSchema), defaultValues: toValues(provider) });
  useEffect(() => {
    if (open) form.reset(toValues(provider));
  }, [open, provider, form]);
  const kind = form.watch("kind");
  const editing = provider !== null;
  const save = useMutation({
    mutationFn: (v: IdpValues) => (editing ? identityProviders.update(provider.id, toInput(v, true)) : identityProviders.create(toInput(v, false))),
    onSuccess: () => {
      toast.success(t("common.saved"));
      void qc.invalidateQueries({ queryKey: ["identity-providers"] });
      void qc.invalidateQueries({ queryKey: ["auth", "providers"] });
      onOpenChange(false);
    },
    onError: (e) => {
      if (e instanceof ApiError && e.code === "conflict") form.setError("slug", { message: "idp.slugTaken" });
      else toastError(e);
    },
  });
  const errors = form.formState.errors;
  const err = (k: keyof IdpValues) => (errors[k]?.message ? t(errors[k]!.message!) : undefined);
  const sw = (name: "pkce_enabled" | "auto_register" | "link_by_verified_email" | "enabled", label: string, hint?: string) => (
    <div className="flex items-start justify-between gap-3 rounded-md border px-3 py-2">
      <div>
        <label htmlFor={`idp-${name}`} className="text-sm">
          {label}
        </label>
        {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      </div>
      <Controller control={form.control} name={name} render={({ field }) => <Switch id={`idp-${name}`} checked={field.value} onCheckedChange={field.onChange} />} />
    </div>
  );

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[92vh] max-w-2xl overflow-y-auto" closeLabel={t("common.close")}>
        <form onSubmit={form.handleSubmit((v) => save.mutate(v))} noValidate className="space-y-4">
          <DialogHeader>
            <DialogTitle>{editing ? t("idp.edit", { name: provider.display_name }) : t("idp.add")}</DialogTitle>
            <DialogDescription>{t("idp.dialogHint")}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={t("idp.kind")} htmlFor="idp-kind">
              <Controller
                control={form.control}
                name="kind"
                render={({ field }) => (
                  <Select
                    value={field.value}
                    disabled={editing}
                    onValueChange={(v) => {
                      field.onChange(v);
                      form.setValue("scopes", DEFAULT_SCOPES[v as IdpKind]);
                      if (!form.getValues("display_name")) form.setValue("display_name", t(`idp.kinds.${v}`));
                      if (!form.getValues("slug")) form.setValue("slug", v);
                    }}
                  >
                    <SelectTrigger id="idp-kind">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {(["github", "google", "oidc"] as const).map((k) => (
                        <SelectItem key={k} value={k}>
                          {t(`idp.kinds.${k}`)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              />
            </Field>
            <Field label={t("idp.slug")} htmlFor="idp-slug" error={err("slug")} hint={t("idp.slugHint")}>
              <Input id="idp-slug" className="font-mono" disabled={editing} {...form.register("slug")} />
            </Field>
            <Field label={t("idp.displayName")} htmlFor="idp-name" error={err("display_name")}>
              <Input id="idp-name" {...form.register("display_name")} />
            </Field>
            <Field label={t("idp.sortOrder")} htmlFor="idp-sort" error={err("sort_order")}>
              <Input id="idp-sort" inputMode="numeric" className="max-w-[8rem]" {...form.register("sort_order")} />
            </Field>
            {kind === "oidc" && (
              <Field label={t("idp.issuer")} htmlFor="idp-issuer" error={err("issuer")} hint={t("idp.issuerHint")} className="sm:col-span-2">
                <Input id="idp-issuer" placeholder="https://accounts.example.com" {...form.register("issuer")} />
              </Field>
            )}
            <Field label={t("idp.clientId")} htmlFor="idp-cid" error={err("client_id")}>
              <Input id="idp-cid" className="font-mono" autoComplete="off" {...form.register("client_id")} />
            </Field>
            <Field
              label={t("idp.clientSecret")}
              htmlFor="idp-secret"
              error={err("client_secret")}
              hint={provider?.has_client_secret ? t("idp.secretStored") : t("idp.secretHint")}
            >
              <Input
                id="idp-secret"
                type="password"
                autoComplete="new-password"
                placeholder={provider?.has_client_secret ? "••••••••••••" : ""}
                {...form.register("client_secret")}
              />
            </Field>
            <Field label={t("idp.scopes")} htmlFor="idp-scopes" className="sm:col-span-2">
              <Input id="idp-scopes" className="font-mono" {...form.register("scopes")} />
            </Field>
          </div>
          <div className="grid gap-2 sm:grid-cols-2">
            {sw("enabled", t("idp.enabled"))}
            <div className="flex items-center justify-between gap-3 rounded-md border px-3 py-2">
              <label htmlFor="idp-pkce_enabled" className="text-sm">
                {t("idp.pkce")}
              </label>
              <div className="flex items-center gap-2">
                <Controller
                  control={form.control}
                  name="pkce_method"
                  render={({ field }) => (
                    <Select value={field.value} onValueChange={field.onChange} disabled={!form.watch("pkce_enabled")}>
                      <SelectTrigger className="h-7 w-24" aria-label={t("idp.pkceMethod")}>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="S256">S256</SelectItem>
                        <SelectItem value="plain">plain</SelectItem>
                      </SelectContent>
                    </Select>
                  )}
                />
                <Controller control={form.control} name="pkce_enabled" render={({ field }) => <Switch id="idp-pkce_enabled" checked={field.value} onCheckedChange={field.onChange} />} />
              </div>
            </div>
            {sw("auto_register", t("idp.autoRegister"), t("idp.autoRegisterHint"))}
            {sw("link_by_verified_email", t("idp.linkByEmail"), t("idp.linkByEmailHint"))}
          </div>
          {provider && (
            <div className="rounded-md bg-muted/50 p-3 text-sm">
              <div className="mb-1 text-xs text-muted-foreground">{t("idp.callbackUrl")}</div>
              <div className="flex items-center gap-1">
                <Mono className="break-all text-xs">{provider.callback_url}</Mono>
                <CopyButton value={provider.callback_url} />
              </div>
            </div>
          )}
          <DialogFooter className="gap-2">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={save.isPending}>
              {editing ? t("common.save") : t("common.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ProviderCard({ p, onEdit, onDelete }: { p: IdentityProvider; onEdit: () => void; onDelete: () => void }) {
  const { t } = useTranslation();
  const toastError = useToastError();
  const [result, setResult] = useState<IdentityProviderTestResult | null>(null);
  const test = useMutation({ mutationFn: () => identityProviders.test(p.id), onSuccess: setResult, onError: toastError });
  return (
    <Card>
      <CardContent className="space-y-3 p-4">
        <div className="flex flex-wrap items-start gap-3">
          <div className="rounded-md bg-muted p-2">
            <KeyRound className="h-4 w-4" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-medium">{p.display_name}</span>
              <Badge variant="outline" className="font-normal">
                {t(`idp.kinds.${p.kind}`, { defaultValue: p.kind })}
              </Badge>
              {p.enabled ? <Badge variant="success">{t("idp.enabledBadge")}</Badge> : <Badge variant="secondary">{t("idp.disabledBadge")}</Badge>}
            </div>
            <div className="mt-0.5 text-xs text-muted-foreground">
              <Mono>{p.slug}</Mono>
              {p.issuer && ` · ${p.issuer}`}
            </div>
          </div>
          <div className="flex gap-1">
            <Button variant="outline" size="sm" onClick={() => test.mutate()} disabled={test.isPending}>
              <FlaskConical />
              {t("idp.test")}
            </Button>
            <Button variant="ghost" size="icon" className="h-8 w-8" onClick={onEdit} aria-label={t("common.edit")}>
              <Pencil />
            </Button>
            <Button variant="ghost" size="icon" className="h-8 w-8" onClick={onDelete} aria-label={t("common.delete")}>
              <Trash2 />
            </Button>
          </div>
        </div>
        <div className="flex flex-wrap gap-1.5 text-xs">
          <Badge variant="secondary" className="font-normal">{p.pkce_enabled ? `PKCE ${p.pkce_method}` : t("idp.noPkce")}</Badge>
          {p.auto_register && <Badge variant="secondary" className="font-normal">{t("idp.autoRegister")}</Badge>}
          {p.link_by_verified_email && <Badge variant="secondary" className="font-normal">{t("idp.linkByEmail")}</Badge>}
          <Badge variant="secondary" className="font-normal">{p.has_client_secret ? t("idp.secretSet") : t("idp.secretMissing")}</Badge>
          <span className="text-muted-foreground">{p.scopes.join(" ")}</span>
        </div>
        <div className="flex items-center gap-1 rounded-md bg-muted/50 px-2 py-1.5 text-xs">
          <span className="shrink-0 text-muted-foreground">{t("idp.callbackUrl")}:</span>
          {p.callback_url ? (
            <>
              <Mono className="truncate text-xs">{p.callback_url}</Mono>
              <CopyButton value={p.callback_url} className="ml-auto" />
            </>
          ) : (
            <span className="text-amber-700 dark:text-amber-300">{t("idp.noPublicUrl")}</span>
          )}
        </div>
        {result && (
          <div className={`flex items-start gap-2 rounded-md border px-3 py-2 text-sm ${result.ok ? "border-emerald-500/30 bg-emerald-500/10" : "border-destructive/30 bg-destructive/10"}`} role="status">
            {result.ok ? <CheckCircle2 className="mt-0.5 h-4 w-4 text-emerald-600" /> : <XCircle className="mt-0.5 h-4 w-4 text-destructive" />}
            <div className="min-w-0">
              <div>{result.ok ? t("idp.testOk") : result.error || t("idp.testFailed")}</div>
              {result.authorization_endpoint && <Mono className="block truncate text-xs text-muted-foreground">{result.authorization_endpoint}</Mono>}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export function IdentityProvidersPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const q = useQuery({ queryKey: ["identity-providers"], queryFn: identityProviders.list });
  const [editing, setEditing] = useState<IdentityProvider | null>(null);
  const [open, setOpen] = useState(false);
  const [deleting, setDeleting] = useState<IdentityProvider | null>(null);
  const del = useMutation({
    mutationFn: (p: IdentityProvider) => identityProviders.remove(p.id),
    onSuccess: () => {
      toast.success(t("idp.deleted"));
      setDeleting(null);
      void qc.invalidateQueries({ queryKey: ["identity-providers"] });
    },
    onError: toastError,
  });
  const items = [...(q.data?.items ?? [])].sort((a, b) => a.sort_order - b.sort_order || a.display_name.localeCompare(b.display_name));
  return (
    <div>
      <PageHeader
        title={t("nav.identityProviders")}
        description={t("idp.description")}
        actions={
          <Button
            size="sm"
            onClick={() => {
              setEditing(null);
              setOpen(true);
            }}
          >
            <Plus />
            {t("idp.add")}
          </Button>
        }
      />
      {q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : q.isLoading ? (
        <div className="grid gap-3 lg:grid-cols-2">
          <Skeleton className="h-40" />
          <Skeleton className="h-40" />
        </div>
      ) : items.length === 0 ? (
        <Card>
          <EmptyState icon={KeyRound} title={t("idp.empty")} description={t("idp.emptyHint")} />
        </Card>
      ) : (
        <div className="grid gap-3 lg:grid-cols-2">
          {items.map((p) => (
            <ProviderCard
              key={p.id}
              p={p}
              onEdit={() => {
                setEditing(p);
                setOpen(true);
              }}
              onDelete={() => setDeleting(p)}
            />
          ))}
        </div>
      )}
      <ProviderDialog provider={editing} open={open} onOpenChange={setOpen} />
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t("idp.deleteTitle", { name: deleting?.display_name ?? "" })}
        description={t("idp.deleteDescription")}
        confirmLabel={t("common.delete")}
        destructive
        confirmText={deleting?.slug}
        pending={del.isPending}
        onConfirm={() => deleting && del.mutate(deleting)}
      />
    </div>
  );
}
