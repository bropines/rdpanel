import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Loader2, Plus, RefreshCw, RotateCcw, Save, Server, Trash2, XCircle } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import { ldapApi, ldapKeys, type LDAPSettings, type LDAPSettingsInput, type LDAPTestRequest, type LDAPTestResult } from "@/api/ldap";
import { groupsApi } from "@/api/users";
import { ErrorState, Field, useToastError } from "@/components";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton, Switch } from "@/components/ui/misc";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

export interface LDAPDraft {
  enabled: boolean;
  server_url: string;
  start_tls: boolean;
  insecure_skip_verify: boolean;
  bind_dn: string;
  bind_password: string;
  bind_password_touched: boolean;
  base_dn: string;
  user_search_base: string;
  user_filter: string;
  username_attribute: string;
  display_name_attribute: string;
  email_attribute: string;
  group_attribute: string;
  group_mappings: Record<string, number>;
  default_user_group_id: number | null;
  auto_register: boolean;
  sync_interval_mins: number;
}

export function ldapDraft(s: LDAPSettings): LDAPDraft {
  return {
    enabled: s.enabled,
    server_url: s.server_url,
    start_tls: s.start_tls,
    insecure_skip_verify: s.insecure_skip_verify,
    bind_dn: s.bind_dn,
    bind_password: "",
    bind_password_touched: false,
    base_dn: s.base_dn,
    user_search_base: s.user_search_base,
    user_filter: s.user_filter,
    username_attribute: s.username_attribute,
    display_name_attribute: s.display_name_attribute,
    email_attribute: s.email_attribute,
    group_attribute: s.group_attribute,
    group_mappings: { ...s.group_mappings },
    default_user_group_id: s.default_user_group_id ?? null,
    auto_register: s.auto_register,
    sync_interval_mins: s.sync_interval_mins,
  };
}

export function ldapInput(d: LDAPDraft): LDAPSettingsInput {
  const body: LDAPSettingsInput = {
    enabled: d.enabled,
    server_url: d.server_url.trim(),
    start_tls: d.start_tls,
    insecure_skip_verify: d.insecure_skip_verify,
    bind_dn: d.bind_dn.trim(),
    base_dn: d.base_dn.trim(),
    user_search_base: d.user_search_base.trim(),
    user_filter: d.user_filter.trim(),
    username_attribute: d.username_attribute.trim(),
    display_name_attribute: d.display_name_attribute.trim(),
    email_attribute: d.email_attribute.trim(),
    group_attribute: d.group_attribute.trim(),
    group_mappings: d.group_mappings,
    default_user_group_id: d.default_user_group_id,
    auto_register: d.auto_register,
    sync_interval_mins: Number(d.sync_interval_mins) || 0,
  };
  if (d.bind_password_touched) {
    body.bind_password = d.bind_password;
  }
  return body;
}

export function LDAPSettingsCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();

  const q = useQuery({ queryKey: ldapKeys.settings(), queryFn: ldapApi.get });
  const groupsQ = useQuery({
    queryKey: ["user-groups", "select-list"],
    queryFn: () => groupsApi.listUserGroups({ page_size: 500 }),
  });

  const [d, setD] = useState<LDAPDraft | null>(null);
  const [isDirty, setIsDirty] = useState(false);
  const [testOpen, setTestOpen] = useState(false);
  const [newLdapGroup, setNewLdapGroup] = useState("");
  const [newLocalGroupId, setNewLocalGroupId] = useState<string>("");

  useEffect(() => {
    if (q.data) {
      setD(ldapDraft(q.data));
      setIsDirty(false);
    }
  }, [q.data]);

  const save = useMutation({
    mutationFn: (body: LDAPSettingsInput) => ldapApi.update(body),
    onSuccess: (s) => {
      qc.setQueryData(ldapKeys.settings(), s);
      setD(ldapDraft(s));
      setIsDirty(false);
      toast.success(t("settings.ldapSaved"));
    },
    onError: toastError,
  });

  const sync = useMutation({
    mutationFn: () => ldapApi.sync(),
    onSuccess: (res) => {
      void qc.invalidateQueries({ queryKey: ldapKeys.settings() });
      void qc.invalidateQueries({ queryKey: ["users"] });
      toast.success(t("settings.ldapSyncSuccess", { count: res.synced_count }));
    },
    onError: toastError,
  });

  if (q.isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-48 w-full" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (q.isError || !q.data || !d) {
    return <ErrorState error={q.error} onRetry={() => q.refetch()} />;
  }

  const updateDraft = (patch: Partial<LDAPDraft>) => {
    setD((prev) => (prev ? { ...prev, ...patch } : null));
    setIsDirty(true);
  };

  const handleAddMapping = () => {
    const groupName = newLdapGroup.trim();
    const localId = Number(newLocalGroupId);
    if (!groupName || !localId) return;

    updateDraft({
      group_mappings: {
        ...d.group_mappings,
        [groupName]: localId,
      },
    });
    setNewLdapGroup("");
    setNewLocalGroupId("");
  };

  const handleRemoveMapping = (groupName: string) => {
    const next = { ...d.group_mappings };
    delete next[groupName];
    updateDraft({ group_mappings: next });
  };

  const userGroups = groupsQ.data?.items ?? [];

  return (
    <div className="space-y-6">
      {/* Top Banner / Sync Info */}
      <Card>
        <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-4">
          <div>
            <CardTitle className="flex items-center gap-2">
              <Server className="h-5 w-5" />
              {t("settings.ldapTitle")}
            </CardTitle>
            <CardDescription>{t("settings.ldapDesc")}</CardDescription>
          </div>
          <div className="flex items-center gap-3">
            <span className="text-sm font-medium">{d.enabled ? t("common.enabled") : t("common.disabled")}</span>
            <Switch
              checked={d.enabled}
              onCheckedChange={(enabled) => updateDraft({ enabled })}
            />
          </div>
        </CardHeader>
        <CardContent>
          <div className="flex flex-wrap items-center justify-between gap-4 rounded-lg border bg-muted/40 p-4">
            <div className="flex flex-wrap items-center gap-4 text-sm">
              <div>
                <span className="text-muted-foreground">{t("settings.ldapLastSync")}: </span>
                <span className="font-medium">
                  {q.data.last_sync_at
                    ? new Date(q.data.last_sync_at).toLocaleString()
                    : t("settings.ldapNeverSynced")}
                </span>
              </div>
              {q.data.last_sync_status && (
                <Badge
                  variant={q.data.last_sync_status.startsWith("error") ? "destructive" : "secondary"}
                >
                  {q.data.last_sync_status}
                </Badge>
              )}
              {q.data.last_sync_count > 0 && (
                <div className="text-muted-foreground">
                  {t("settings.ldapSyncedUsers", { count: q.data.last_sync_count })}
                </div>
              )}
            </div>
            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setTestOpen(true)}
              >
                {t("settings.ldapTestConnection")}
              </Button>
              <Button
                type="button"
                variant="secondary"
                size="sm"
                disabled={!d.enabled || sync.isPending}
                onClick={() => sync.mutate()}
              >
                {sync.isPending ? (
                  <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                ) : (
                  <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
                )}
                {t("settings.ldapSyncNow")}
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Connection & Bind */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("settings.ldapServerAndAuth")}</CardTitle>
          <CardDescription>{t("settings.ldapServerAndAuthDesc")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field label={t("settings.ldapServerUrl")} hint={t("settings.ldapServerUrlHint")}>
              <Input
                placeholder="ldaps://ldap.example.com:636"
                value={d.server_url}
                onChange={(e) => updateDraft({ server_url: e.target.value })}
              />
            </Field>

            <Field label={t("settings.ldapBindDn")} hint={t("settings.ldapBindDnHint")}>
              <Input
                placeholder="CN=svc-rustdesk,OU=ServiceAccounts,DC=example,DC=com"
                value={d.bind_dn}
                onChange={(e) => updateDraft({ bind_dn: e.target.value })}
              />
            </Field>
          </div>

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field
              label={t("settings.ldapBindPassword")}
              hint={
                q.data.has_bind_password && !d.bind_password_touched
                  ? t("settings.ldapBindPasswordSaved")
                  : t("settings.ldapBindPasswordHint")
              }
            >
              <Input
                type="password"
                placeholder={
                  q.data.has_bind_password && !d.bind_password_touched
                    ? "••••••••"
                    : t("settings.ldapEnterPassword")
                }
                value={d.bind_password}
                onChange={(e) =>
                  updateDraft({
                    bind_password: e.target.value,
                    bind_password_touched: true,
                  })
                }
              />
            </Field>

            <div className="flex flex-col justify-end space-y-3 pt-2">
              <div className="flex items-center justify-between">
                <div>
                  <div className="text-sm font-medium">{t("settings.ldapStartTls")}</div>
                  <div className="text-xs text-muted-foreground">{t("settings.ldapStartTlsHint")}</div>
                </div>
                <Switch
                  checked={d.start_tls}
                  disabled={d.server_url.startsWith("ldaps://")}
                  onCheckedChange={(start_tls) => updateDraft({ start_tls })}
                />
              </div>

              <div className="flex items-center justify-between">
                <div>
                  <div className="text-sm font-medium">{t("settings.ldapInsecureSkipVerify")}</div>
                  <div className="text-xs text-muted-foreground">{t("settings.ldapInsecureSkipVerifyHint")}</div>
                </div>
                <Switch
                  checked={d.insecure_skip_verify}
                  onCheckedChange={(insecure_skip_verify) => updateDraft({ insecure_skip_verify })}
                />
              </div>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Directory Search & Filters */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("settings.ldapSearchAndFilter")}</CardTitle>
          <CardDescription>{t("settings.ldapSearchAndFilterDesc")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field label={t("settings.ldapBaseDn")} hint={t("settings.ldapBaseDnHint")}>
              <Input
                placeholder="DC=example,DC=com"
                value={d.base_dn}
                onChange={(e) => updateDraft({ base_dn: e.target.value })}
              />
            </Field>

            <Field label={t("settings.ldapUserSearchBase")} hint={t("settings.ldapUserSearchBaseHint")}>
              <Input
                placeholder="OU=Users,DC=example,DC=com"
                value={d.user_search_base}
                onChange={(e) => updateDraft({ user_search_base: e.target.value })}
              />
            </Field>
          </div>

          <Field label={t("settings.ldapUserFilter")} hint={t("settings.ldapUserFilterHint")}>
            <Input
              placeholder="(&(objectClass=user)(sAMAccountName=%s))"
              value={d.user_filter}
              onChange={(e) => updateDraft({ user_filter: e.target.value })}
            />
          </Field>
        </CardContent>
      </Card>

      {/* Schema Attributes */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("settings.ldapAttributes")}</CardTitle>
          <CardDescription>{t("settings.ldapAttributesDesc")}</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <Field label={t("settings.ldapUsernameAttr")} hint="sAMAccountName / uid">
              <Input
                value={d.username_attribute}
                onChange={(e) => updateDraft({ username_attribute: e.target.value })}
              />
            </Field>

            <Field label={t("settings.ldapDisplayNameAttr")} hint="displayName / cn">
              <Input
                value={d.display_name_attribute}
                onChange={(e) => updateDraft({ display_name_attribute: e.target.value })}
              />
            </Field>

            <Field label={t("settings.ldapEmailAttr")} hint="mail / userPrincipalName">
              <Input
                value={d.email_attribute}
                onChange={(e) => updateDraft({ email_attribute: e.target.value })}
              />
            </Field>

            <Field label={t("settings.ldapGroupAttr")} hint="memberOf">
              <Input
                value={d.group_attribute}
                onChange={(e) => updateDraft({ group_attribute: e.target.value })}
              />
            </Field>
          </div>
        </CardContent>
      </Card>

      {/* Provisioning & Group Mappings */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("settings.ldapProvisioning")}</CardTitle>
          <CardDescription>{t("settings.ldapProvisioningDesc")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-6">
          <div className="grid grid-cols-1 gap-6 sm:grid-cols-3">
            <div className="flex flex-col justify-between rounded-lg border p-3">
              <div>
                <div className="text-sm font-medium">{t("settings.ldapAutoRegister")}</div>
                <div className="text-xs text-muted-foreground">{t("settings.ldapAutoRegisterHint")}</div>
              </div>
              <div className="pt-3">
                <Switch
                  checked={d.auto_register}
                  onCheckedChange={(auto_register) => updateDraft({ auto_register })}
                />
              </div>
            </div>

            <div className="rounded-lg border p-3">
              <Field label={t("settings.ldapDefaultGroup")} hint={t("settings.ldapDefaultGroupHint")}>
                <Select
                  value={d.default_user_group_id ? String(d.default_user_group_id) : "none"}
                  onValueChange={(val) =>
                    updateDraft({
                      default_user_group_id: val === "none" ? null : Number(val),
                    })
                  }
                >
                  <SelectTrigger>
                    <SelectValue placeholder={t("settings.ldapNoDefaultGroup")} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">{t("settings.ldapNoDefaultGroup")}</SelectItem>
                    {userGroups.map((g) => (
                      <SelectItem key={g.id} value={String(g.id)}>
                        {g.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </div>

            <div className="rounded-lg border p-3">
              <Field label={t("settings.ldapSyncInterval")} hint={t("settings.ldapSyncIntervalHint")}>
                <Input
                  type="number"
                  min="0"
                  step="5"
                  value={d.sync_interval_mins}
                  onChange={(e) => updateDraft({ sync_interval_mins: Number(e.target.value) || 0 })}
                />
              </Field>
            </div>
          </div>

          {/* Group Mappings Table */}
          <div className="space-y-3">
            <div className="text-sm font-medium">{t("settings.ldapGroupMappings")}</div>
            <div className="text-xs text-muted-foreground">{t("settings.ldapGroupMappingsHint")}</div>

            <div className="rounded-md border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("settings.ldapGroupDnOrCn")}</TableHead>
                    <TableHead>{t("settings.ldapMappedRdGroup")}</TableHead>
                    <TableHead className="w-16"></TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {Object.entries(d.group_mappings).length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={3} className="text-center text-sm text-muted-foreground">
                        {t("settings.ldapNoGroupMappings")}
                      </TableCell>
                    </TableRow>
                  ) : (
                    Object.entries(d.group_mappings).map(([groupName, localId]) => {
                      const localGrp = userGroups.find((g) => g.id === localId);
                      return (
                        <TableRow key={groupName}>
                          <TableCell className="font-mono text-xs">{groupName}</TableCell>
                          <TableCell>
                            <Badge variant="outline">{localGrp ? localGrp.name : `ID #${localId}`}</Badge>
                          </TableCell>
                          <TableCell>
                            <Button
                              type="button"
                              variant="ghost"
                              size="sm"
                              className="h-8 w-8 p-0 text-destructive hover:bg-destructive/10"
                              onClick={() => handleRemoveMapping(groupName)}
                            >
                              <Trash2 className="h-4 w-4" />
                            </Button>
                          </TableCell>
                        </TableRow>
                      );
                    })
                  )}
                </TableBody>
              </Table>
            </div>

            <div className="flex flex-wrap items-center gap-3 pt-2">
              <Input
                placeholder="CN=DevOps,OU=Groups,DC=example,DC=com"
                className="flex-1 min-w-[240px]"
                value={newLdapGroup}
                onChange={(e) => setNewLdapGroup(e.target.value)}
              />
              <Select
                value={newLocalGroupId}
                onValueChange={setNewLocalGroupId}
              >
                <SelectTrigger className="w-[200px]">
                  <SelectValue placeholder={t("settings.ldapSelectGroup")} />
                </SelectTrigger>
                <SelectContent>
                  {userGroups.map((g) => (
                    <SelectItem key={g.id} value={String(g.id)}>
                      {g.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!newLdapGroup.trim() || !newLocalGroupId}
                onClick={handleAddMapping}
              >
                <Plus className="mr-1.5 h-3.5 w-3.5" />
                {t("settings.ldapAddMapping")}
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Floating Save Toolbar */}
      {isDirty && (
        <div className="fixed inset-x-3 bottom-3 z-20 mx-auto flex max-w-3xl flex-wrap items-center gap-3 rounded-lg border bg-background/95 px-4 py-3 shadow-lg backdrop-blur md:left-64">
          <span className="text-sm font-medium">{t("settings.unsaved")}</span>
          <div className="ml-auto flex gap-2">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => {
                setD(ldapDraft(q.data));
                setIsDirty(false);
              }}
            >
              <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
              {t("common.discard")}
            </Button>
            <Button
              type="button"
              size="sm"
              disabled={save.isPending}
              onClick={() => save.mutate(ldapInput(d))}
            >
              {save.isPending ? (
                <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
              ) : (
                <Save className="mr-1.5 h-3.5 w-3.5" />
              )}
              {t("common.save")}
            </Button>
          </div>
        </div>
      )}

      {/* Test Connection Dialog */}
      <LDAPTestDialog
        open={testOpen}
        onOpenChange={setTestOpen}
        draft={d}
        userGroups={userGroups}
      />
    </div>
  );
}

function LDAPTestDialog({
  open,
  onOpenChange,
  draft,
  userGroups,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  draft: LDAPDraft;
  userGroups: { id: number; name: string }[];
}) {
  const { t } = useTranslation();
  const [testUser, setTestUser] = useState("");
  const [testPassword, setTestPassword] = useState("");
  const [result, setResult] = useState<LDAPTestResult | null>(null);

  const testMutation = useMutation({
    mutationFn: (body: LDAPTestRequest) => ldapApi.test(body),
    onSuccess: (res) => {
      setResult(res);
    },
    onError: (err) => {
      setResult({
        ok: false,
        message:
          err instanceof ApiError && err.fields?.bind_password
            ? t("settings.ldapTestNeedsBindPassword")
            : err instanceof Error
              ? err.message
              : String(err),
        connected: false,
        bound: false,
        user_found: false,
        user_bound: false,
      });
    },
  });

  const handleRunTest = () => {
    testMutation.mutate({
      settings: ldapInput(draft),
      bind_password: draft.bind_password_touched ? draft.bind_password : undefined,
      test_username: testUser.trim() || undefined,
      test_password: testPassword || undefined,
    });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>{t("settings.ldapTestTitle")}</DialogTitle>
          <DialogDescription>{t("settings.ldapTestDesc")}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label={t("settings.ldapTestUsername")} hint={t("settings.ldapTestUsernameHint")}>
              <Input
                placeholder="johndoe"
                value={testUser}
                onChange={(e) => setTestUser(e.target.value)}
              />
            </Field>

            <Field label={t("settings.ldapTestPassword")} hint={t("settings.ldapTestPasswordHint")}>
              <Input
                type="password"
                placeholder="••••••••"
                value={testPassword}
                onChange={(e) => setTestPassword(e.target.value)}
              />
            </Field>
          </div>

          {result && (
            <div className="rounded-lg border p-4 space-y-3 bg-muted/30">
              <div className="flex items-center gap-2">
                {result.ok ? (
                  <CheckCircle2 className="h-5 w-5 text-green-500 shrink-0" />
                ) : (
                  <XCircle className="h-5 w-5 text-destructive shrink-0" />
                )}
                <span className="font-semibold text-sm">{result.message}</span>
              </div>

              <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 text-xs">
                <div className="flex items-center justify-between border-b pb-1">
                  <span className="text-muted-foreground">{t("settings.ldapServerConnected")}:</span>
                  <span className="font-medium">{result.connected ? "✓" : "✗"}</span>
                </div>
                <div className="flex items-center justify-between border-b pb-1">
                  <span className="text-muted-foreground">{t("settings.ldapAdminBound")}:</span>
                  <span className="font-medium">{result.bound ? "✓" : "✗"}</span>
                </div>
                {testUser && (
                  <>
                    <div className="flex items-center justify-between border-b pb-1">
                      <span className="text-muted-foreground">{t("settings.ldapUserFound")}:</span>
                      <span className="font-medium">{result.user_found ? "✓" : "✗"}</span>
                    </div>
                    {testPassword && (
                      <div className="flex items-center justify-between border-b pb-1">
                        <span className="text-muted-foreground">{t("settings.ldapUserPasswordValid")}:</span>
                        <span className="font-medium">{result.user_bound ? "✓" : "✗"}</span>
                      </div>
                    )}
                  </>
                )}
              </div>

              {result.user_dn && (
                <div className="text-xs">
                  <span className="text-muted-foreground">{t("settings.ldapUserDn")}: </span>
                  <code className="rounded bg-muted px-1.5 py-0.5 font-mono">{result.user_dn}</code>
                </div>
              )}

              {result.mapped_group_id && (
                <div className="text-xs">
                  <span className="text-muted-foreground">{t("settings.ldapResolvedGroup")}: </span>
                  <Badge variant="outline">
                    {userGroups.find((g) => g.id === result.mapped_group_id)?.name ??
                      `ID #${result.mapped_group_id}`}
                  </Badge>
                </div>
              )}

              {result.groups && result.groups.length > 0 && (
                <div className="text-xs space-y-1">
                  <span className="text-muted-foreground">{t("settings.ldapDiscoveredGroups")}:</span>
                  <div className="flex flex-wrap gap-1">
                    {result.groups.map((g) => (
                      <Badge key={g} variant="secondary" className="font-mono text-[10px]">
                        {g}
                      </Badge>
                    ))}
                  </div>
                </div>
              )}
            </div>
          )}
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
          >
            {t("common.close")}
          </Button>
          <Button
            type="button"
            disabled={testMutation.isPending}
            onClick={handleRunTest}
          >
            {testMutation.isPending ? (
              <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
            ) : null}
            {t("settings.ldapRunTest")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
