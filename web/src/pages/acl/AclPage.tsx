import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertCircle,
  Check,
  CheckCircle2,
  Clipboard,
  ExternalLink,
  FileCode,
  FlaskConical,
  FolderTree,
  HardDrive,
  Keyboard,
  Layers,
  Lock,
  Mic,
  Monitor,
  MoreHorizontal,
  Pencil,
  Plus,
  RefreshCw,
  RotateCcw,
  Shield,
  ShieldAlert,
  ShieldCheck,
  Star,
  Terminal,
  Trash2,
  Unlock,
  User,
  Users,
  X,
} from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import {
  aclApi,
  aclKeys,
  type ACLPolicyValidationResult,
  type ACLTestResult,
  type ConnectionACL,
  type ConnectionACLInput,
  type ControlRole,
  type ControlRoleInput,
} from "@/api/acl";
import { ConfirmDialog, EmptyState, ErrorState, PageHeader, useErrorMessage } from "@/components";
import { ControlRolePicker, DeviceGroupPicker, DevicePicker, UserGroupPicker, UserPicker } from "@/components/pickers";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton, Switch, Tabs, TabsContent, TabsList, TabsTrigger, Textarea } from "@/components/ui/misc";
import { cn } from "@/lib/utils";

export function AclPage() {
  const { t } = useTranslation();
  const [activeTab, setActiveTab] = useState<"rules" | "roles" | "policy">("rules");
  const [testOpen, setTestOpen] = useState(false);
  const [ruleModalOpen, setRuleModalOpen] = useState(false);
  const [editingRule, setEditingRule] = useState<ConnectionACL | null>(null);
  const [roleModalOpen, setRoleModalOpen] = useState(false);
  const [editingRole, setEditingRole] = useState<ControlRole | null>(null);

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <PageHeader
          title={t("acl.title")}
          description={t("acl.description")}
        />
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" onClick={() => setTestOpen(true)}>
            <FlaskConical className="mr-2 h-4 w-4 text-primary" />
            {t("acl.testAccess")}
          </Button>
          {activeTab === "rules" && (
            <Button
              onClick={() => {
                setEditingRule(null);
                setRuleModalOpen(true);
              }}
            >
              <Plus className="mr-2 h-4 w-4" />
              {t("acl.addRule")}
            </Button>
          )}
          {activeTab === "roles" && (
            <Button
              onClick={() => {
                setEditingRole(null);
                setRoleModalOpen(true);
              }}
            >
              <Plus className="mr-2 h-4 w-4" />
              {t("acl.addRole")}
            </Button>
          )}
        </div>
      </div>

      <Tabs value={activeTab} onValueChange={(v) => setActiveTab(v as "rules" | "roles" | "policy")}>
        <TabsList className="grid w-full max-w-lg grid-cols-3">
          <TabsTrigger value="rules" className="flex items-center gap-2">
            <Shield className="h-4 w-4" />
            {t("acl.rulesTab")}
          </TabsTrigger>
          <TabsTrigger value="roles" className="flex items-center gap-2">
            <Layers className="h-4 w-4" />
            {t("acl.rolesTab")}
          </TabsTrigger>
          <TabsTrigger value="policy" className="flex items-center gap-2">
            <FileCode className="h-4 w-4" />
            {t("acl.policyTab")}
          </TabsTrigger>
        </TabsList>

        <TabsContent value="rules" className="mt-6">
          <RulesTab
            onEdit={(rule) => {
              setEditingRule(rule);
              setRuleModalOpen(true);
            }}
            onAdd={() => {
              setEditingRule(null);
              setRuleModalOpen(true);
            }}
          />
        </TabsContent>

        <TabsContent value="roles" className="mt-6">
          <RolesTab
            onEdit={(role) => {
              setEditingRole(role);
              setRoleModalOpen(true);
            }}
          />
        </TabsContent>

        <TabsContent value="policy" className="mt-6">
          <PolicyTab />
        </TabsContent>
      </Tabs>

      {/* Modals */}
      <RuleDialog
        open={ruleModalOpen}
        onOpenChange={setRuleModalOpen}
        rule={editingRule}
      />
      <RoleDialog
        open={roleModalOpen}
        onOpenChange={setRoleModalOpen}
        role={editingRole}
      />
      <TestAccessDialog
        open={testOpen}
        onOpenChange={setTestOpen}
      />
    </div>
  );
}

// -----------------------------------------------------------------------------
// Rules Tab
// -----------------------------------------------------------------------------

function RulesTab({
  onEdit,
  onAdd,
}: {
  onEdit: (rule: ConnectionACL) => void;
  onAdd: () => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const errMessage = useErrorMessage();

  const { data, isLoading, error } = useQuery({
    queryKey: aclKeys.rules(),
    queryFn: aclApi.listRules,
  });

  const [deleteTarget, setDeleteTarget] = useState<ConnectionACL | null>(null);

  const toggleMutation = useMutation({
    mutationFn: ({ id, enabled }: { id: number; enabled: boolean }) =>
      aclApi.updateRule(id, { enabled }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: aclKeys.rules() });
    },
    onError: (err) => {
      toast.error(errMessage(err));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => aclApi.deleteRule(id),
    onSuccess: () => {
      toast.success(t("acl.ruleDeleted"));
      void qc.invalidateQueries({ queryKey: aclKeys.rules() });
      void qc.invalidateQueries({ queryKey: aclKeys.roles() });
      setDeleteTarget(null);
    },
    onError: (err) => {
      toast.error(errMessage(err));
    },
  });

  if (isLoading) {
    return (
      <div className="space-y-3">
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-12 w-full" />
      </div>
    );
  }

  if (error) {
    return <ErrorState error={error} />;
  }

  const rules = data?.items ?? [];

  if (rules.length === 0) {
    return (
      <EmptyState
        icon={Shield}
        title={t("acl.noRulesTitle")}
        description={t("acl.noRulesDescription")}
        action={
          <Button onClick={onAdd}>
            <Plus className="mr-2 h-4 w-4" />
            {t("acl.addRule")}
          </Button>
        }
      />
    );
  }

  return (
    <div className="space-y-4">
      <div className="rounded-md border bg-card">
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead className="border-b bg-muted/50 text-xs font-medium uppercase text-muted-foreground">
              <tr>
                <th className="px-4 py-3 text-center">{t("acl.priority")}</th>
                <th className="px-4 py-3">{t("acl.ruleName")}</th>
                <th className="px-4 py-3">{t("acl.subject")}</th>
                <th className="px-4 py-3">{t("acl.target")}</th>
                <th className="px-4 py-3">{t("acl.action")}</th>
                <th className="px-4 py-3">{t("acl.controlRole")}</th>
                <th className="px-4 py-3 text-center">{t("acl.status")}</th>
                <th className="px-4 py-3 text-right">{t("common.actions")}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {rules
                .slice()
                .sort((a, b) => a.priority - b.priority)
                .map((r) => (
                  <tr key={r.id} className={cn("hover:bg-muted/30 transition-colors", !r.enabled && "opacity-60")}>
                    <td className="px-4 py-3 text-center">
                      <Badge variant="outline" className="font-mono text-xs font-semibold">
                        {r.priority}
                      </Badge>
                    </td>
                    <td className="px-4 py-3">
                      <div className="font-medium text-foreground">{r.name}</div>
                      {r.note && <div className="text-xs text-muted-foreground line-clamp-1">{r.note}</div>}
                    </td>
                    <td className="px-4 py-3">
                      {r.user_id ? (
                        <div className="flex items-center gap-1.5 font-medium">
                          <User className="h-3.5 w-3.5 text-muted-foreground" />
                          <span>{r.user_name || `#${r.user_id}`}</span>
                        </div>
                      ) : r.user_group_id ? (
                        <div className="flex items-center gap-1.5 font-medium">
                          <Users className="h-3.5 w-3.5 text-muted-foreground" />
                          <span>{r.user_group_name || `Group #${r.user_group_id}`}</span>
                        </div>
                      ) : (
                        <Badge variant="secondary" className="text-xs">
                          {t("acl.allUsers")}
                        </Badge>
                      )}
                    </td>
                    <td className="px-4 py-3">
                      {r.device_id ? (
                        <div className="flex items-center gap-1.5 font-medium">
                          <Monitor className="h-3.5 w-3.5 text-muted-foreground" />
                          <span>{r.device_name || `#${r.device_id}`}</span>
                        </div>
                      ) : r.device_group_id ? (
                        <div className="flex items-center gap-1.5 font-medium">
                          <FolderTree className="h-3.5 w-3.5 text-muted-foreground" />
                          <span>{r.device_group_name || `Group #${r.device_group_id}`}</span>
                        </div>
                      ) : (
                        <Badge variant="secondary" className="text-xs">
                          {t("acl.allDevices")}
                        </Badge>
                      )}
                    </td>
                    <td className="px-4 py-3">
                      {r.action === "allow" ? (
                        <Badge className="bg-emerald-600 hover:bg-emerald-700 font-semibold tracking-wide uppercase text-[10px]">
                          <Unlock className="mr-1 h-3 w-3" />
                          {t("acl.actionAllow")}
                        </Badge>
                      ) : (
                        <Badge variant="destructive" className="font-semibold tracking-wide uppercase text-[10px]">
                          <Lock className="mr-1 h-3 w-3" />
                          {t("acl.actionDeny")}
                        </Badge>
                      )}
                    </td>
                    <td className="px-4 py-3">
                      {r.action === "allow" ? (
                        <Badge variant="outline" className="text-xs font-normal">
                          {r.control_role_name || t("acl.defaultRole")}
                        </Badge>
                      ) : (
                        <span className="text-xs text-muted-foreground">—</span>
                      )}
                    </td>
                    <td className="px-4 py-3 text-center">
                      <Switch
                        checked={r.enabled}
                        onCheckedChange={(checked) => toggleMutation.mutate({ id: r.id, enabled: checked })}
                      />
                    </td>
                    <td className="px-4 py-3 text-right">
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <Button variant="ghost" size="icon" className="h-8 w-8">
                            <MoreHorizontal className="h-4 w-4" />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem onClick={() => onEdit(r)}>
                            <Pencil className="mr-2 h-4 w-4" />
                            {t("common.edit")}
                          </DropdownMenuItem>
                          <DropdownMenuSeparator />
                          <DropdownMenuItem
                            className="text-destructive focus:text-destructive"
                            onClick={() => setDeleteTarget(r)}
                          >
                            <Trash2 className="mr-2 h-4 w-4" />
                            {t("common.delete")}
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </td>
                  </tr>
                ))}
            </tbody>
          </table>
        </div>
      </div>

      <ConfirmDialog
        open={deleteTarget != null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title={t("acl.deleteRuleTitle")}
        description={t("acl.deleteRuleDesc", { name: deleteTarget?.name })}
        confirmLabel={t("common.delete")}
        destructive
        onConfirm={() => deleteTarget && deleteMutation.mutate(deleteTarget.id)}
      />
    </div>
  );
}

// -----------------------------------------------------------------------------
// Roles Tab
// -----------------------------------------------------------------------------

function RolesTab({
  onEdit,
}: {
  onEdit: (role: ControlRole) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const errMessage = useErrorMessage();

  const { data, isLoading, error } = useQuery({
    queryKey: aclKeys.roles(),
    queryFn: aclApi.listRoles,
  });

  const [deleteTarget, setDeleteTarget] = useState<ControlRole | null>(null);

  const setDefaultMutation = useMutation({
    mutationFn: (id: number) => aclApi.setDefaultRole(id),
    onSuccess: () => {
      toast.success(t("acl.roleSetDefault"));
      void qc.invalidateQueries({ queryKey: aclKeys.roles() });
    },
    onError: (err) => {
      toast.error(errMessage(err));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => aclApi.deleteRole(id),
    onSuccess: () => {
      toast.success(t("acl.roleDeleted"));
      void qc.invalidateQueries({ queryKey: aclKeys.roles() });
      setDeleteTarget(null);
    },
    onError: (err) => {
      toast.error(errMessage(err));
    },
  });

  if (isLoading) {
    return (
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
        <Skeleton className="h-52 w-full" />
        <Skeleton className="h-52 w-full" />
        <Skeleton className="h-52 w-full" />
      </div>
    );
  }

  if (error) {
    return <ErrorState error={error} />;
  }

  const roles = data?.items ?? [];

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
        {roles.map((role) => (
          <Card key={role.id} className={cn("flex flex-col relative", role.is_default && "border-primary/50 shadow-sm")}>
            <CardHeader className="pb-3">
              <div className="flex items-start justify-between gap-2">
                <div>
                  <CardTitle className="text-base flex items-center gap-2">
                    {role.name}
                    {role.is_default && (
                      <Badge variant="default" className="text-[10px] uppercase font-semibold tracking-wider">
                        <Star className="mr-1 h-3 w-3 fill-current" />
                        {t("acl.default")}
                      </Badge>
                    )}
                  </CardTitle>
                  <CardDescription className="text-xs mt-1 line-clamp-2">
                    {role.note || t("acl.noNote")}
                  </CardDescription>
                </div>

                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button variant="ghost" size="icon" className="h-8 w-8 -mr-2 -mt-2">
                      <MoreHorizontal className="h-4 w-4" />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem onClick={() => onEdit(role)}>
                      <Pencil className="mr-2 h-4 w-4" />
                      {t("common.edit")}
                    </DropdownMenuItem>
                    {!role.is_default && (
                      <DropdownMenuItem onClick={() => setDefaultMutation.mutate(role.id)}>
                        <Star className="mr-2 h-4 w-4" />
                        {t("acl.makeDefault")}
                      </DropdownMenuItem>
                    )}
                    <DropdownMenuSeparator />
                    <DropdownMenuItem
                      disabled={role.is_default}
                      className="text-destructive focus:text-destructive"
                      onClick={() => setDeleteTarget(role)}
                    >
                      <Trash2 className="mr-2 h-4 w-4" />
                      {t("common.delete")}
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
            </CardHeader>

            <CardContent className="flex-1 space-y-3 pb-4">
              <div className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
                {t("acl.permissions")}
              </div>
              <div className="grid grid-cols-2 gap-2 text-xs">
                <CapabilityBadge label={t("acl.capKeyboardMouse")} allowed={role.allow_keyboard_mouse} icon={Keyboard} />
                <CapabilityBadge label={t("acl.capClipboard")} allowed={role.allow_clipboard} icon={Clipboard} />
                <CapabilityBadge label={t("acl.capFileTransfer")} allowed={role.allow_file_transfer} icon={HardDrive} />
                <CapabilityBadge label={t("acl.capAudio")} allowed={role.allow_audio} icon={Mic} />
                <CapabilityBadge label={t("acl.capTCPTunneling")} allowed={role.allow_tcp_tunneling} icon={ExternalLink} />
                <CapabilityBadge label={t("acl.capTerminal")} allowed={role.allow_terminal} icon={Terminal} />
                <CapabilityBadge label={t("acl.capRestart")} allowed={role.allow_restart} icon={RefreshCw} />
              </div>
            </CardContent>

            <div className="border-t bg-muted/20 px-6 py-2.5 text-xs text-muted-foreground flex justify-between items-center rounded-b-lg">
              <span>{t("acl.rulesReferencing", { count: role.rule_count })}</span>
              {!role.is_default && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 text-xs px-2"
                  onClick={() => setDefaultMutation.mutate(role.id)}
                >
                  {t("acl.setAsDefault")}
                </Button>
              )}
            </div>
          </Card>
        ))}
      </div>

      <ConfirmDialog
        open={deleteTarget != null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title={t("acl.deleteRoleTitle")}
        description={t("acl.deleteRoleDesc", { name: deleteTarget?.name })}
        confirmLabel={t("common.delete")}
        destructive
        onConfirm={() => deleteTarget && deleteMutation.mutate(deleteTarget.id)}
      />
    </div>
  );
}

function PolicyTab() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const errMessage = useErrorMessage();

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: aclKeys.policy(),
    queryFn: () => aclApi.getPolicy(),
  });

  const [policyText, setPolicyText] = useState<string>("");
  const [initialized, setInitialized] = useState(false);
  const [validationResult, setValidationResult] = useState<ACLPolicyValidationResult | null>(null);

  // Sync state when data is loaded
  if (data && !initialized) {
    setPolicyText(data.policy);
    setInitialized(true);
  }

  const isDirty = initialized && policyText !== (data?.policy ?? "");

  const testMutation = useMutation({
    mutationFn: () => aclApi.testPolicy({ policy: policyText }),
    onSuccess: (res) => {
      setValidationResult(res);
      if (res.valid) {
        toast.success(t("acl.policy.testsPassedToast", { count: res.tests_count }));
      } else {
        toast.error(t("acl.policy.testsFailedToast", { count: res.errors?.length ?? 1 }));
      }
    },
    onError: (err) => {
      toast.error(errMessage(err));
    },
  });

  const saveMutation = useMutation({
    mutationFn: () => aclApi.updatePolicy({ policy: policyText }),
    onSuccess: (res) => {
      setValidationResult(res);
      queryClient.invalidateQueries({ queryKey: aclKeys.all });
      toast.success(t("acl.policy.saveSuccess"));
    },
    onError: (err) => {
      toast.error(errMessage(err));
    },
  });

  const handleReset = () => {
    if (data?.policy) {
      setPolicyText(data.policy);
      setValidationResult(null);
      toast.info(t("acl.policy.resetDone"));
    }
  };

  // Keyboard shortcuts: Tab key support in textarea, Ctrl+Enter / Cmd+Enter to run tests, Ctrl+S / Cmd+S to save
  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
      e.preventDefault();
      testMutation.mutate();
    } else if ((e.ctrlKey || e.metaKey) && e.key === "s") {
      e.preventDefault();
      saveMutation.mutate();
    } else if (e.key === "Tab") {
      e.preventDefault();
      const target = e.currentTarget;
      const start = target.selectionStart;
      const end = target.selectionEnd;
      const value = target.value;
      target.value = value.substring(0, start) + "  " + value.substring(end);
      target.selectionStart = target.selectionEnd = start + 2;
      setPolicyText(target.value);
    }
  };

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-96 w-full" />
      </div>
    );
  }

  if (error) {
    return <ErrorState error={error} onRetry={() => refetch()} />;
  }

  const lineCount = policyText.split("\n").length;

  return (
    <div className="space-y-6">
      {/* Editor & Actions Card */}
      <Card>
        <CardHeader className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between pb-3">
          <div>
            <CardTitle className="text-base font-medium flex items-center gap-2">
              <FileCode className="h-5 w-5 text-primary" />
              {t("acl.policy.editorTitle")}
              {isDirty && (
                <Badge variant="secondary" className="text-xs bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/20">
                  {t("acl.policy.unsavedChanges")}
                </Badge>
              )}
            </CardTitle>
            <CardDescription>{t("acl.policy.editorDescription")}</CardDescription>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={handleReset}
              disabled={!isDirty || saveMutation.isPending}
            >
              <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
              {t("acl.policy.resetBtn")}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => testMutation.mutate()}
              disabled={testMutation.isPending || saveMutation.isPending}
            >
              <FlaskConical className={cn("mr-1.5 h-3.5 w-3.5 text-primary", testMutation.isPending && "animate-spin")} />
              {t("acl.policy.runTestsBtn")}
            </Button>
            <Button
              size="sm"
              onClick={() => saveMutation.mutate()}
              disabled={saveMutation.isPending || testMutation.isPending}
            >
              <ShieldCheck className={cn("mr-1.5 h-3.5 w-3.5", saveMutation.isPending && "animate-spin")} />
              {t("acl.policy.saveBtn")}
            </Button>
          </div>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="relative rounded-md border border-input bg-muted/20 font-mono focus-within:ring-1 focus-within:ring-ring">
            <Textarea
              value={policyText}
              onChange={(e) => setPolicyText(e.target.value)}
              onKeyDown={handleKeyDown}
              spellCheck={false}
              className="min-h-[480px] w-full resize-y border-0 bg-transparent p-4 font-mono text-xs sm:text-sm leading-relaxed focus-visible:ring-0"
              placeholder="// { ... policy ... }"
            />
          </div>
          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>{t("acl.policy.linesCount", { count: lineCount })}</span>
            <span className="hidden sm:inline">{t("acl.policy.shortcutsHint")}</span>
          </div>
        </CardContent>
      </Card>

      {/* Test & Validation Results */}
      {validationResult && (
        <Card className={cn("border-l-4", validationResult.valid ? "border-l-emerald-500" : "border-l-destructive")}>
          <CardHeader className="pb-3">
            <div className="flex items-center justify-between">
              <CardTitle className="text-base font-medium flex items-center gap-2">
                {validationResult.valid ? (
                  <>
                    <CheckCircle2 className="h-5 w-5 text-emerald-500" />
                    <span>{t("acl.policy.validationPassedTitle")}</span>
                  </>
                ) : (
                  <>
                    <AlertCircle className="h-5 w-5 text-destructive" />
                    <span>{t("acl.policy.validationFailedTitle")}</span>
                  </>
                )}
              </CardTitle>
              <div className="flex items-center gap-2">
                <Badge variant={validationResult.valid ? "default" : "destructive"}>
                  {validationResult.valid
                    ? t("acl.policy.testsPassedBadge", { count: validationResult.tests_count })
                    : t("acl.policy.validationFailedBadge")}
                </Badge>
              </div>
            </div>
            <CardDescription>
              {t("acl.policy.summaryDetails", {
                roles: validationResult.roles_count,
                rules: validationResult.rules_count,
                tests: validationResult.tests_count,
              })}
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            {/* Errors List if any */}
            {validationResult.errors && validationResult.errors.length > 0 && (
              <div className="rounded-md bg-destructive/10 p-3 text-destructive text-sm space-y-1 border border-destructive/20">
                <div className="font-semibold">{t("acl.policy.errorsHeader")}:</div>
                <ul className="list-disc list-inside space-y-0.5 text-xs sm:text-sm">
                  {validationResult.errors.map((err, idx) => (
                    <li key={idx}>{err}</li>
                  ))}
                </ul>
              </div>
            )}

            {/* Test Assertions Breakdown */}
            {validationResult.test_results && validationResult.test_results.length > 0 && (
              <div className="space-y-2">
                <div className="text-sm font-semibold text-foreground">{t("acl.policy.assertionsBreakdown")}:</div>
                <div className="grid gap-2 sm:grid-cols-2">
                  {validationResult.test_results.map((tr, idx) => (
                    <div
                      key={idx}
                      className={cn(
                        "flex flex-col justify-between gap-2 rounded-lg border p-3 text-xs sm:text-sm",
                        tr.passed ? "bg-emerald-500/5 border-emerald-500/20" : "bg-destructive/5 border-destructive/20",
                      )}
                    >
                      <div className="space-y-1">
                        <div className="flex items-center justify-between">
                          <span className="font-mono text-xs font-semibold">{tr.test.src} → {tr.test.dst}</span>
                          <Badge
                            variant={tr.passed ? "outline" : "destructive"}
                            className={cn(
                              "text-xs",
                              tr.passed && "border-emerald-500/30 text-emerald-600 dark:text-emerald-400 bg-emerald-500/10",
                            )}
                          >
                            {tr.passed ? (
                              <Check className="mr-1 h-3 w-3" />
                            ) : (
                              <X className="mr-1 h-3 w-3" />
                            )}
                            {tr.passed ? t("acl.policy.assertionPassed") : t("acl.policy.assertionFailed")}
                          </Badge>
                        </div>
                        <div className="text-xs text-muted-foreground">
                          {t("acl.policy.expected")}: <span className="font-medium text-foreground">{tr.test.expect}{tr.test.role ? ` (${tr.test.role})` : ""}</span> |{" "}
                          {t("acl.policy.actual")}: <span className="font-medium text-foreground">{tr.actual_decision}{tr.actual_role ? ` (${tr.actual_role})` : ""}</span>
                        </div>
                      </div>
                      {tr.error && (
                        <div className="rounded bg-destructive/10 px-2 py-1 text-xs text-destructive">
                          {tr.error}
                        </div>
                      )}
                    </div>
                  ))}
                </div>
              </div>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  );
}

function CapabilityBadge({
  label,
  allowed,
  icon: Icon,
}: {
  label: string;
  allowed: boolean;
  icon: React.ElementType;
}) {
  return (
    <div
      className={cn(
        "flex items-center gap-1.5 rounded px-2 py-1 border transition-colors",
        allowed
          ? "border-emerald-500/20 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400 font-medium"
          : "border-border bg-muted/40 text-muted-foreground line-through opacity-60",
      )}
    >
      <Icon className="h-3 w-3 shrink-0" />
      <span className="truncate">{label}</span>
      {allowed ? (
        <Check className="ml-auto h-3 w-3 shrink-0 text-emerald-600 dark:text-emerald-400" />
      ) : (
        <X className="ml-auto h-3 w-3 shrink-0" />
      )}
    </div>
  );
}

// -----------------------------------------------------------------------------
// Add / Edit Rule Dialog
// -----------------------------------------------------------------------------

function RuleDialog({
  open,
  onOpenChange,
  rule,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  rule: ConnectionACL | null;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const errMessage = useErrorMessage();

  const [name, setName] = useState(rule?.name ?? "");
  const [note, setNote] = useState(rule?.note ?? "");
  const [priority, setPriority] = useState<number>(rule?.priority ?? 100);
  const [action, setAction] = useState<"allow" | "deny">(rule?.action ?? "allow");
  const [enabled, setEnabled] = useState(rule?.enabled ?? true);

  // Subject: "all" | "user" | "group"
  const [subjectType, setSubjectType] = useState<"all" | "user" | "group">(
    rule?.user_id ? "user" : rule?.user_group_id ? "group" : "all",
  );
  const [userId, setUserId] = useState<number | null>(rule?.user_id ?? null);
  const [userName, setUserName] = useState<string>(rule?.user_name ?? "");
  const [userGroupId, setUserGroupId] = useState<number | null>(rule?.user_group_id ?? null);
  const [userGroupName, setUserGroupName] = useState<string>(rule?.user_group_name ?? "");

  // Target: "all" | "device" | "group"
  const [targetType, setTargetType] = useState<"all" | "device" | "group">(
    rule?.device_id ? "device" : rule?.device_group_id ? "group" : "all",
  );
  const [deviceId, setDeviceId] = useState<number | null>(rule?.device_id ?? null);
  const [deviceName, setDeviceName] = useState<string>(rule?.device_name ?? "");
  const [deviceGroupId, setDeviceGroupId] = useState<number | null>(rule?.device_group_id ?? null);
  const [deviceGroupName, setDeviceGroupName] = useState<string>(rule?.device_group_name ?? "");

  // Control Role
  const [controlRoleId, setControlRoleId] = useState<number | null>(rule?.control_role_id ?? null);
  const [controlRoleName, setControlRoleName] = useState<string>(rule?.control_role_name ?? "");

  // Reset when dialog opens/changes
  const handleOpenChange = (nextOpen: boolean) => {
    if (nextOpen && rule) {
      setName(rule.name);
      setNote(rule.note ?? "");
      setPriority(rule.priority);
      setAction(rule.action as "allow" | "deny");
      setEnabled(rule.enabled);
      setSubjectType(rule.user_id ? "user" : rule.user_group_id ? "group" : "all");
      setUserId(rule.user_id ?? null);
      setUserName(rule.user_name || "");
      setUserGroupId(rule.user_group_id ?? null);
      setUserGroupName(rule.user_group_name || "");
      setTargetType(rule.device_id ? "device" : rule.device_group_id ? "group" : "all");
      setDeviceId(rule.device_id ?? null);
      setDeviceName(rule.device_name || "");
      setDeviceGroupId(rule.device_group_id ?? null);
      setDeviceGroupName(rule.device_group_name || "");
      setControlRoleId(rule.control_role_id ?? null);
      setControlRoleName(rule.control_role_name || "");
    } else if (nextOpen && !rule) {
      setName("");
      setNote("");
      setPriority(100);
      setAction("allow");
      setEnabled(true);
      setSubjectType("all");
      setUserId(null);
      setUserName("");
      setUserGroupId(null);
      setUserGroupName("");
      setTargetType("all");
      setDeviceId(null);
      setDeviceName("");
      setDeviceGroupId(null);
      setDeviceGroupName("");
      setControlRoleId(null);
      setControlRoleName("");
    }
    onOpenChange(nextOpen);
  };

  const saveMutation = useMutation({
    mutationFn: async () => {
      const payload: ConnectionACLInput = {
        name,
        note,
        priority,
        action,
        enabled,
        user_id: subjectType === "user" ? userId : null,
        user_group_id: subjectType === "group" ? userGroupId : null,
        device_id: targetType === "device" ? deviceId : null,
        device_group_id: targetType === "group" ? deviceGroupId : null,
        control_role_id: action === "allow" ? controlRoleId : null,
      };

      if (rule) {
        return aclApi.updateRule(rule.id, payload);
      }
      return aclApi.createRule(payload);
    },
    onSuccess: () => {
      toast.success(rule ? t("acl.ruleUpdated") : t("acl.ruleCreated"));
      void qc.invalidateQueries({ queryKey: aclKeys.rules() });
      void qc.invalidateQueries({ queryKey: aclKeys.roles() });
      onOpenChange(false);
    },
    onError: (err) => {
      toast.error(errMessage(err));
    },
  });

  const canSave =
    name.trim().length > 0 &&
    priority >= 1 &&
    priority <= 1000000 &&
    (subjectType !== "user" || userId != null) &&
    (subjectType !== "group" || userGroupId != null) &&
    (targetType !== "device" || deviceId != null) &&
    (targetType !== "group" || deviceGroupId != null);

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-w-xl max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{rule ? t("acl.editRule") : t("acl.newRule")}</DialogTitle>
          <DialogDescription>{t("acl.ruleDialogDesc")}</DialogDescription>
        </DialogHeader>

        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (canSave) saveMutation.mutate();
          }}
          className="space-y-4 py-2"
        >
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
            <div className="sm:col-span-2 space-y-1">
              <Label htmlFor="rule-name">{t("acl.ruleName")} *</Label>
              <Input
                id="rule-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t("acl.ruleNamePlaceholder")}
                required
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="rule-priority">{t("acl.priority")} *</Label>
              <Input
                id="rule-priority"
                type="number"
                min={1}
                max={1000000}
                value={priority}
                onChange={(e) => setPriority(Number(e.target.value))}
                required
              />
            </div>
          </div>

          <div className="space-y-1">
            <Label htmlFor="rule-note">{t("common.note")}</Label>
            <Input
              id="rule-note"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder={t("acl.ruleNotePlaceholder")}
            />
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 pt-1">
            <div className="space-y-1">
              <Label>{t("acl.action")}</Label>
              <Select value={action} onValueChange={(v) => setAction(v as "allow" | "deny")}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="allow">{t("acl.actionAllow")}</SelectItem>
                  <SelectItem value="deny">{t("acl.actionDeny")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1">
              <Label>{t("acl.status")}</Label>
              <div className="flex items-center gap-2 pt-2">
                <Switch checked={enabled} onCheckedChange={setEnabled} id="rule-enabled" />
                <Label htmlFor="rule-enabled" className="font-normal cursor-pointer text-sm">
                  {enabled ? t("common.enabled") : t("common.disabled")}
                </Label>
              </div>
            </div>
          </div>

          {/* Subject matching */}
          <div className="rounded-lg border p-3.5 space-y-3 bg-muted/20">
            <Label className="text-xs uppercase font-bold tracking-wider text-muted-foreground flex items-center gap-1.5">
              <User className="h-3.5 w-3.5" />
              {t("acl.matchingSubject")}
            </Label>
            <div className="flex gap-4">
              <label className="flex items-center space-x-2 cursor-pointer text-sm">
                <input
                  type="radio"
                  name="subject-type"
                  value="all"
                  checked={subjectType === "all"}
                  onChange={() => setSubjectType("all")}
                  className="accent-primary"
                />
                <span>{t("acl.allUsers")}</span>
              </label>
              <label className="flex items-center space-x-2 cursor-pointer text-sm">
                <input
                  type="radio"
                  name="subject-type"
                  value="user"
                  checked={subjectType === "user"}
                  onChange={() => setSubjectType("user")}
                  className="accent-primary"
                />
                <span>{t("acl.specificUser")}</span>
              </label>
              <label className="flex items-center space-x-2 cursor-pointer text-sm">
                <input
                  type="radio"
                  name="subject-type"
                  value="group"
                  checked={subjectType === "group"}
                  onChange={() => setSubjectType("group")}
                  className="accent-primary"
                />
                <span>{t("acl.userGroup")}</span>
              </label>
            </div>

            {subjectType === "user" && (
              <div className="pt-1">
                <UserPicker
                  value={userId}
                  valueLabel={userName}
                  onChange={(id, u) => {
                    setUserId(id);
                    setUserName(u ? u.display_name || u.username : "");
                  }}
                />
              </div>
            )}

            {subjectType === "group" && (
              <div className="pt-1">
                <UserGroupPicker
                  value={userGroupId}
                  valueLabel={userGroupName}
                  onChange={(id, g) => {
                    setUserGroupId(id);
                    setUserGroupName(g ? g.name : "");
                  }}
                />
              </div>
            )}
          </div>

          {/* Target matching */}
          <div className="rounded-lg border p-3.5 space-y-3 bg-muted/20">
            <Label className="text-xs uppercase font-bold tracking-wider text-muted-foreground flex items-center gap-1.5">
              <Monitor className="h-3.5 w-3.5" />
              {t("acl.matchingTarget")}
            </Label>
            <div className="flex gap-4">
              <label className="flex items-center space-x-2 cursor-pointer text-sm">
                <input
                  type="radio"
                  name="target-type"
                  value="all"
                  checked={targetType === "all"}
                  onChange={() => setTargetType("all")}
                  className="accent-primary"
                />
                <span>{t("acl.allDevices")}</span>
              </label>
              <label className="flex items-center space-x-2 cursor-pointer text-sm">
                <input
                  type="radio"
                  name="target-type"
                  value="device"
                  checked={targetType === "device"}
                  onChange={() => setTargetType("device")}
                  className="accent-primary"
                />
                <span>{t("acl.specificDevice")}</span>
              </label>
              <label className="flex items-center space-x-2 cursor-pointer text-sm">
                <input
                  type="radio"
                  name="target-type"
                  value="group"
                  checked={targetType === "group"}
                  onChange={() => setTargetType("group")}
                  className="accent-primary"
                />
                <span>{t("acl.deviceGroup")}</span>
              </label>
            </div>

            {targetType === "device" && (
              <div className="pt-1">
                <DevicePicker
                  value={deviceId}
                  valueLabel={deviceName}
                  onChange={(id, d) => {
                    setDeviceId(id);
                    setDeviceName(d ? d.name : "");
                  }}
                />
              </div>
            )}

            {targetType === "group" && (
              <div className="pt-1">
                <DeviceGroupPicker
                  value={deviceGroupId}
                  valueLabel={deviceGroupName}
                  onChange={(id, g) => {
                    setDeviceGroupId(id);
                    setDeviceGroupName(g ? g.name : "");
                  }}
                />
              </div>
            )}
          </div>

          {/* Role granted (only if action == allow) */}
          {action === "allow" && (
            <div className="space-y-1.5">
              <Label>{t("acl.controlRole")}</Label>
              <ControlRolePicker
                value={controlRoleId}
                valueLabel={controlRoleName}
                allowNone
                noneLabel={t("acl.useDefaultRole")}
                onChange={(id, r) => {
                  setControlRoleId(id);
                  setControlRoleName(r ? r.name : "");
                }}
              />
              <p className="text-xs text-muted-foreground">{t("acl.controlRoleHelp")}</p>
            </div>
          )}

          <DialogFooter className="pt-3">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={!canSave || saveMutation.isPending}>
              {saveMutation.isPending ? t("common.saving") : t("common.save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// -----------------------------------------------------------------------------
// Add / Edit Role Dialog
// -----------------------------------------------------------------------------

function RoleDialog({
  open,
  onOpenChange,
  role,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  role: ControlRole | null;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const errMessage = useErrorMessage();

  const [name, setName] = useState(role?.name ?? "");
  const [note, setNote] = useState(role?.note ?? "");
  const [allowKeyboardMouse, setAllowKeyboardMouse] = useState(role?.allow_keyboard_mouse ?? true);
  const [allowClipboard, setAllowClipboard] = useState(role?.allow_clipboard ?? true);
  const [allowFileTransfer, setAllowFileTransfer] = useState(role?.allow_file_transfer ?? true);
  const [allowAudio, setAllowAudio] = useState(role?.allow_audio ?? true);
  const [allowTCPTunneling, setAllowTCPTunneling] = useState(role?.allow_tcp_tunneling ?? true);
  const [allowTerminal, setAllowTerminal] = useState(role?.allow_terminal ?? true);
  const [allowRestart, setAllowRestart] = useState(role?.allow_restart ?? true);
  const [isDefault, setIsDefault] = useState(role?.is_default ?? false);

  const handleOpenChange = (nextOpen: boolean) => {
    if (nextOpen && role) {
      setName(role.name);
      setNote(role.note ?? "");
      setAllowKeyboardMouse(role.allow_keyboard_mouse);
      setAllowClipboard(role.allow_clipboard);
      setAllowFileTransfer(role.allow_file_transfer);
      setAllowAudio(role.allow_audio);
      setAllowTCPTunneling(role.allow_tcp_tunneling);
      setAllowTerminal(role.allow_terminal);
      setAllowRestart(role.allow_restart);
      setIsDefault(role.is_default);
    } else if (nextOpen && !role) {
      setName("");
      setNote("");
      setAllowKeyboardMouse(true);
      setAllowClipboard(true);
      setAllowFileTransfer(true);
      setAllowAudio(true);
      setAllowTCPTunneling(true);
      setAllowTerminal(true);
      setAllowRestart(true);
      setIsDefault(false);
    }
    onOpenChange(nextOpen);
  };

  const saveMutation = useMutation({
    mutationFn: async () => {
      const payload: ControlRoleInput = {
        name,
        note,
        allow_keyboard_mouse: allowKeyboardMouse,
        allow_clipboard: allowClipboard,
        allow_file_transfer: allowFileTransfer,
        allow_audio: allowAudio,
        allow_tcp_tunneling: allowTCPTunneling,
        allow_terminal: allowTerminal,
        allow_restart: allowRestart,
        is_default: isDefault,
      };

      if (role) {
        return aclApi.updateRole(role.id, payload);
      }
      return aclApi.createRole(payload);
    },
    onSuccess: () => {
      toast.success(role ? t("acl.roleUpdated") : t("acl.roleCreated"));
      void qc.invalidateQueries({ queryKey: aclKeys.roles() });
      onOpenChange(false);
    },
    onError: (err) => {
      toast.error(errMessage(err));
    },
  });

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{role ? t("acl.editRole") : t("acl.newRole")}</DialogTitle>
          <DialogDescription>{t("acl.roleDialogDesc")}</DialogDescription>
        </DialogHeader>

        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (name.trim()) saveMutation.mutate();
          }}
          className="space-y-4 py-2"
        >
          <div className="space-y-1">
            <Label htmlFor="role-name">{t("acl.roleName")} *</Label>
            <Input
              id="role-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("acl.roleNamePlaceholder")}
              required
            />
          </div>

          <div className="space-y-1">
            <Label htmlFor="role-note">{t("common.note")}</Label>
            <Input
              id="role-note"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder={t("acl.roleNotePlaceholder")}
            />
          </div>

          <div className="rounded-lg border p-3 space-y-3 bg-muted/20">
            <Label className="text-xs uppercase font-bold tracking-wider text-muted-foreground">
              {t("acl.capabilitiesTitle")}
            </Label>
            <div className="space-y-2.5">
              <PermissionSwitch
                id="cap-kbm"
                label={t("acl.capKeyboardMouse")}
                checked={allowKeyboardMouse}
                onChange={setAllowKeyboardMouse}
              />
              <PermissionSwitch
                id="cap-clip"
                label={t("acl.capClipboard")}
                checked={allowClipboard}
                onChange={setAllowClipboard}
              />
              <PermissionSwitch
                id="cap-files"
                label={t("acl.capFileTransfer")}
                checked={allowFileTransfer}
                onChange={setAllowFileTransfer}
              />
              <PermissionSwitch
                id="cap-audio"
                label={t("acl.capAudio")}
                checked={allowAudio}
                onChange={setAllowAudio}
              />
              <PermissionSwitch
                id="cap-tunnel"
                label={t("acl.capTCPTunneling")}
                checked={allowTCPTunneling}
                onChange={setAllowTCPTunneling}
              />
              <PermissionSwitch
                id="cap-term"
                label={t("acl.capTerminal")}
                checked={allowTerminal}
                onChange={setAllowTerminal}
              />
              <PermissionSwitch
                id="cap-restart"
                label={t("acl.capRestart")}
                checked={allowRestart}
                onChange={setAllowRestart}
              />
            </div>
          </div>

          <div className="flex items-center gap-2 pt-1">
            <Switch checked={isDefault} onCheckedChange={setIsDefault} id="role-default" />
            <Label htmlFor="role-default" className="cursor-pointer text-sm font-normal">
              {t("acl.setAsDefault")}
            </Label>
          </div>

          <DialogFooter className="pt-2">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={!name.trim() || saveMutation.isPending}>
              {saveMutation.isPending ? t("common.saving") : t("common.save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function PermissionSwitch({
  id,
  label,
  checked,
  onChange,
}: {
  id: string;
  label: string;
  checked: boolean;
  onChange: (c: boolean) => void;
}) {
  return (
    <div className="flex items-center justify-between text-sm">
      <Label htmlFor={id} className="font-normal cursor-pointer">
        {label}
      </Label>
      <Switch id={id} checked={checked} onCheckedChange={onChange} />
    </div>
  );
}

// -----------------------------------------------------------------------------
// Test Access Simulator Dialog
// -----------------------------------------------------------------------------

function TestAccessDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useTranslation();
  const errMessage = useErrorMessage();

  const [userId, setUserId] = useState<number | null>(null);
  const [userName, setUserName] = useState<string>("");
  const [deviceId, setDeviceId] = useState<number | null>(null);
  const [deviceName, setDeviceName] = useState<string>("");
  const [result, setResult] = useState<ACLTestResult | null>(null);

  const testMutation = useMutation({
    mutationFn: () => aclApi.testAccess({ user_id: userId!, device_id: deviceId! }),
    onSuccess: (data) => {
      setResult(data);
    },
    onError: (err) => {
      toast.error(errMessage(err));
    },
  });

  const canEvaluate = userId != null && deviceId != null;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <FlaskConical className="h-5 w-5 text-primary" />
            {t("acl.testAccessTitle")}
          </DialogTitle>
          <DialogDescription>{t("acl.testAccessDesc")}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div className="space-y-1.5">
            <Label>{t("acl.testUser")}</Label>
            <UserPicker
              value={userId}
              valueLabel={userName}
              onChange={(id, u) => {
                setUserId(id);
                setUserName(u ? u.display_name || u.username : "");
                setResult(null);
              }}
            />
          </div>

          <div className="space-y-1.5">
            <Label>{t("acl.testDevice")}</Label>
            <DevicePicker
              value={deviceId}
              valueLabel={deviceName}
              onChange={(id, d) => {
                setDeviceId(id);
                setDeviceName(d ? d.name : "");
                setResult(null);
              }}
            />
          </div>

          <Button
            className="w-full"
            disabled={!canEvaluate || testMutation.isPending}
            onClick={() => testMutation.mutate()}
          >
            {testMutation.isPending ? t("common.evaluating") : t("acl.runEvaluation")}
          </Button>

          {/* Result view */}
          {result && (
            <div className="space-y-3 pt-2">
              <div
                className={cn(
                  "p-4 rounded-lg border flex items-start gap-3",
                  result.decision === "allow" && "border-emerald-500/30 bg-emerald-500/10 text-emerald-950 dark:text-emerald-100",
                  result.decision === "deny" && "border-destructive/30 bg-destructive/10 text-destructive dark:text-red-200",
                  result.decision === "owner" && "border-blue-500/30 bg-blue-500/10 text-blue-950 dark:text-blue-100",
                  result.decision === "no_rules" && "border-muted bg-muted/30 text-foreground",
                )}
              >
                {result.decision === "allow" ? (
                  <ShieldCheck className="h-6 w-6 text-emerald-600 dark:text-emerald-400 shrink-0 mt-0.5" />
                ) : result.decision === "deny" ? (
                  <ShieldAlert className="h-6 w-6 text-destructive shrink-0 mt-0.5" />
                ) : (
                  <Shield className="h-6 w-6 text-blue-600 dark:text-blue-400 shrink-0 mt-0.5" />
                )}
                <div>
                  <div className="font-semibold text-base">
                    {result.decision === "allow" && t("acl.decisionAllow")}
                    {result.decision === "deny" && t("acl.decisionDeny")}
                    {result.decision === "owner" && t("acl.decisionOwner")}
                    {result.decision === "no_rules" && t("acl.decisionNoRules")}
                  </div>
                  <div className="text-xs opacity-90 mt-0.5">
                    {result.decision === "allow" && t("acl.decisionAllowDesc")}
                    {result.decision === "deny" && t("acl.decisionDenyDesc")}
                    {result.decision === "owner" && t("acl.decisionOwnerDesc")}
                    {result.decision === "no_rules" && t("acl.decisionNoRulesDesc")}
                  </div>
                </div>
              </div>

              {result.matched_rule && (
                <div className="rounded-md border p-3 text-xs space-y-1 bg-muted/20">
                  <span className="font-semibold text-muted-foreground uppercase tracking-wider block">
                    {t("acl.matchedRule")}
                  </span>
                  <div className="flex items-center justify-between font-medium">
                    <span>{result.matched_rule.name}</span>
                    <Badge variant="outline" className="font-mono text-[10px]">
                      {t("acl.priority")}: {result.matched_rule.priority}
                    </Badge>
                  </div>
                  {result.matched_rule.note && (
                    <div className="text-muted-foreground">{result.matched_rule.note}</div>
                  )}
                </div>
              )}

              {result.effective_role && (
                <div className="rounded-md border p-3 text-xs space-y-2 bg-muted/20">
                  <div className="flex items-center justify-between">
                    <span className="font-semibold text-muted-foreground uppercase tracking-wider">
                      {t("acl.effectiveRole")}
                    </span>
                    <Badge variant="secondary" className="font-medium">
                      {result.effective_role.name}
                    </Badge>
                  </div>
                  <div className="grid grid-cols-2 gap-1.5 pt-1">
                    <CapabilityBadge label={t("acl.capKeyboardMouse")} allowed={result.effective_role.allow_keyboard_mouse} icon={Keyboard} />
                    <CapabilityBadge label={t("acl.capClipboard")} allowed={result.effective_role.allow_clipboard} icon={Clipboard} />
                    <CapabilityBadge label={t("acl.capFileTransfer")} allowed={result.effective_role.allow_file_transfer} icon={HardDrive} />
                    <CapabilityBadge label={t("acl.capAudio")} allowed={result.effective_role.allow_audio} icon={Mic} />
                    <CapabilityBadge label={t("acl.capTCPTunneling")} allowed={result.effective_role.allow_tcp_tunneling} icon={ExternalLink} />
                    <CapabilityBadge label={t("acl.capTerminal")} allowed={result.effective_role.allow_terminal} icon={Terminal} />
                    <CapabilityBadge label={t("acl.capRestart")} allowed={result.effective_role.allow_restart} icon={RefreshCw} />
                  </div>
                </div>
              )}
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.close")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
