import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Check,
  Copy,
  FolderTree,
  Lock,
  MoreHorizontal,
  Pencil,
  Plus,
  Shield,
  ShieldAlert,
  Star,
  Trash2,
} from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { strategiesApi, strategyKeys, type ClientStrategy, type ClientStrategyInput } from "@/api/strategies";
import { ConfirmDialog, EmptyState, ErrorState, Field, useErrorMessage } from "@/components";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton, Switch, Textarea } from "@/components/ui/misc";

const TEMPLATES: Record<string, { name: string; note: string; options: Record<string, string> }> = {
  standard: {
    name: "Standard Office",
    note: "Balanced policy for typical workstations: password or click confirmation, clipboard and files enabled, local config changes blocked.",
    options: {
      "approve-mode": "both",
      "verification-method": "both",
      "enable-keyboard": "Y",
      "enable-clipboard": "Y",
      "enable-file-transfer": "Y",
      "enable-audio": "Y",
      "enable-tunnel": "Y",
      "enable-remote-restart": "Y",
      "enable-record-session": "N",
      "allow-remote-config-modification": "N",
      "lock-after-session-end": "N",
      "direct-server": "N",
    },
  },
  strict: {
    name: "Strict Security",
    note: "Strict policy for sensitive hosts: temporary passwords only, clipboard and file transfers disabled, automatic screen lock on disconnect.",
    options: {
      "approve-mode": "password",
      "verification-method": "use-temporary-password",
      "enable-keyboard": "Y",
      "enable-clipboard": "N",
      "enable-file-transfer": "N",
      "enable-audio": "N",
      "enable-tunnel": "N",
      "enable-remote-restart": "N",
      "enable-record-session": "Y",
      "allow-remote-config-modification": "N",
      "lock-after-session-end": "Y",
      "direct-server": "N",
    },
  },
  server: {
    name: "Unattended Server",
    note: "Policy for unattended servers: permanent password only, audio disabled, screen locked on disconnect.",
    options: {
      "approve-mode": "password",
      "verification-method": "use-permanent-password",
      "enable-keyboard": "Y",
      "enable-clipboard": "Y",
      "enable-file-transfer": "Y",
      "enable-audio": "N",
      "enable-tunnel": "Y",
      "enable-remote-restart": "Y",
      "enable-record-session": "N",
      "allow-remote-config-modification": "N",
      "lock-after-session-end": "Y",
      "direct-server": "N",
    },
  },
};

function StrategyDialog({
  open,
  onOpenChange,
  strategy,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  strategy: ClientStrategy | null;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const errMessage = useErrorMessage();

  const [name, setName] = useState(strategy?.name ?? "");
  const [note, setNote] = useState(strategy?.note ?? "");
  const [isDefault, setIsDefault] = useState(strategy?.is_default ?? false);

  // Options state
  const initialOpts = strategy?.config_options ?? TEMPLATES["standard"]!.options;
  const [approveMode, setApproveMode] = useState(initialOpts["approve-mode"] ?? "both");
  const [verifyMethod, setVerifyMethod] = useState(initialOpts["verification-method"] ?? "both");
  const [whitelist, setWhitelist] = useState(initialOpts["whitelist"] ?? "");
  const [directServer, setDirectServer] = useState(initialOpts["direct-server"] === "Y");

  const [enableKeyboard, setEnableKeyboard] = useState(initialOpts["enable-keyboard"] !== "N");
  const [enableClipboard, setEnableClipboard] = useState(initialOpts["enable-clipboard"] !== "N");
  const [enableFileTransfer, setEnableFileTransfer] = useState(initialOpts["enable-file-transfer"] !== "N");
  const [enableAudio, setEnableAudio] = useState(initialOpts["enable-audio"] !== "N");
  const [enableTunnel, setEnableTunnel] = useState(initialOpts["enable-tunnel"] !== "N");
  const [enableRestart, setEnableRestart] = useState(initialOpts["enable-remote-restart"] !== "N");
  const [enableRecording, setEnableRecording] = useState(initialOpts["enable-record-session"] === "Y");

  const [allowConfigMod, setAllowConfigMod] = useState(initialOpts["allow-remote-config-modification"] === "Y");
  const [lockAfterSession, setLockAfterSession] = useState(initialOpts["lock-after-session-end"] === "Y");

  const applyTemplate = (key: string) => {
    const tmpl = TEMPLATES[key];
    if (!tmpl) return;
    if (!strategy) {
      setName(tmpl.name);
      setNote(tmpl.note);
    }
    setApproveMode(tmpl.options["approve-mode"] ?? "both");
    setVerifyMethod(tmpl.options["verification-method"] ?? "both");
    setEnableKeyboard(tmpl.options["enable-keyboard"] !== "N");
    setEnableClipboard(tmpl.options["enable-clipboard"] !== "N");
    setEnableFileTransfer(tmpl.options["enable-file-transfer"] !== "N");
    setEnableAudio(tmpl.options["enable-audio"] !== "N");
    setEnableTunnel(tmpl.options["enable-tunnel"] !== "N");
    setEnableRestart(tmpl.options["enable-remote-restart"] !== "N");
    setEnableRecording(tmpl.options["enable-record-session"] === "Y");
    setAllowConfigMod(tmpl.options["allow-remote-config-modification"] === "Y");
    setLockAfterSession(tmpl.options["lock-after-session-end"] === "Y");
    setDirectServer(tmpl.options["direct-server"] === "Y");
  };

  const save = useMutation({
    mutationFn: () => {
      const opts: Record<string, string> = {
        "approve-mode": approveMode,
        "verification-method": verifyMethod,
        "enable-keyboard": enableKeyboard ? "Y" : "N",
        "enable-clipboard": enableClipboard ? "Y" : "N",
        "enable-file-transfer": enableFileTransfer ? "Y" : "N",
        "enable-audio": enableAudio ? "Y" : "N",
        "enable-tunnel": enableTunnel ? "Y" : "N",
        "enable-remote-restart": enableRestart ? "Y" : "N",
        "enable-record-session": enableRecording ? "Y" : "N",
        "allow-remote-config-modification": allowConfigMod ? "Y" : "N",
        "lock-after-session-end": lockAfterSession ? "Y" : "N",
        "direct-server": directServer ? "Y" : "N",
      };
      if (whitelist.trim()) {
        opts["whitelist"] = whitelist.trim();
      } else {
        opts["whitelist"] = "";
      }

      const body: ClientStrategyInput = {
        name: name.trim(),
        note: note.trim(),
        config_options: opts,
        is_default: isDefault,
      };

      return strategy ? strategiesApi.update(strategy.id, body) : strategiesApi.create(body);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: strategyKeys.all });
      toast.success(t("common.saved"));
      onOpenChange(false);
    },
    onError: (err) => {
      toast.error(errMessage(err));
    },
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{strategy ? t("strategies.editStrategy") : t("strategies.createStrategy")}</DialogTitle>
          <DialogDescription>{t("strategies.strategyDialogDesc")}</DialogDescription>
        </DialogHeader>

        {!strategy && (
          <div className="flex items-center gap-2 p-2 rounded-md bg-muted/60 border text-xs">
            <span className="font-medium text-muted-foreground">{t("strategies.loadTemplate")}:</span>
            <Button type="button" variant="outline" size="sm" className="h-7 text-xs" onClick={() => applyTemplate("standard")}>
              {t("strategies.templates.standard")}
            </Button>
            <Button type="button" variant="outline" size="sm" className="h-7 text-xs" onClick={() => applyTemplate("strict")}>
              {t("strategies.templates.strict")}
            </Button>
            <Button type="button" variant="outline" size="sm" className="h-7 text-xs" onClick={() => applyTemplate("server")}>
              {t("strategies.templates.server")}
            </Button>
          </div>
        )}

        <div className="space-y-4 pt-2">
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <Field label={t("strategies.name")} htmlFor="strat-name">
              <Input
                id="strat-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g. Standard Office"
                maxLength={64}
                required
              />
            </Field>

            <div className="flex items-center space-x-2 pt-6">
              <Switch id="strat-default" checked={isDefault} onCheckedChange={setIsDefault} />
              <label htmlFor="strat-default" className="text-sm font-medium leading-none cursor-pointer">
                {t("strategies.makeDefault")}
              </label>
            </div>
          </div>

          <Field label={t("strategies.note")} htmlFor="strat-note">
            <Textarea
              id="strat-note"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder={t("strategies.notePlaceholder")}
              rows={2}
              maxLength={4096}
            />
          </Field>

          {/* Section: Access & Authentication */}
          <div className="border rounded-lg p-3 space-y-3 bg-card">
            <h4 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-1.5">
              <Lock className="h-3.5 w-3.5" />
              {t("strategies.sections.auth")}
            </h4>

            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <Field label={t("strategies.approveMode.label")}>
                <Select value={approveMode} onValueChange={setApproveMode}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="both">{t("strategies.approveMode.both")}</SelectItem>
                    <SelectItem value="password">{t("strategies.approveMode.password")}</SelectItem>
                    <SelectItem value="click">{t("strategies.approveMode.click")}</SelectItem>
                  </SelectContent>
                </Select>
              </Field>

              <Field label={t("strategies.verificationMethod.label")}>
                <Select value={verifyMethod} onValueChange={setVerifyMethod}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="both">{t("strategies.verificationMethod.both")}</SelectItem>
                    <SelectItem value="use-temporary-password">{t("strategies.verificationMethod.temporary")}</SelectItem>
                    <SelectItem value="use-permanent-password">{t("strategies.verificationMethod.permanent")}</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
            </div>

            <Field label={t("strategies.whitelist.label")} hint={t("strategies.whitelist.hint")}>
              <Input
                value={whitelist}
                onChange={(e) => setWhitelist(e.target.value)}
                placeholder="e.g. 123456789, 987654321"
              />
            </Field>

            <div className="flex items-center justify-between pt-1">
              <div>
                <p className="text-sm font-medium">{t("strategies.directServer.title")}</p>
                <p className="text-xs text-muted-foreground">{t("strategies.directServer.desc")}</p>
              </div>
              <Switch checked={directServer} onCheckedChange={setDirectServer} />
            </div>
          </div>

          {/* Section: Session Features & Permissions */}
          <div className="border rounded-lg p-3 space-y-3 bg-card">
            <h4 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-1.5">
              <Shield className="h-3.5 w-3.5" />
              {t("strategies.sections.permissions")}
            </h4>

            <div className="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-2">
              <div className="flex items-center justify-between py-1">
                <span className="text-sm">{t("strategies.features.keyboard")}</span>
                <Switch checked={enableKeyboard} onCheckedChange={setEnableKeyboard} />
              </div>
              <div className="flex items-center justify-between py-1">
                <span className="text-sm">{t("strategies.features.clipboard")}</span>
                <Switch checked={enableClipboard} onCheckedChange={setEnableClipboard} />
              </div>
              <div className="flex items-center justify-between py-1">
                <span className="text-sm">{t("strategies.features.fileTransfer")}</span>
                <Switch checked={enableFileTransfer} onCheckedChange={setEnableFileTransfer} />
              </div>
              <div className="flex items-center justify-between py-1">
                <span className="text-sm">{t("strategies.features.audio")}</span>
                <Switch checked={enableAudio} onCheckedChange={setEnableAudio} />
              </div>
              <div className="flex items-center justify-between py-1">
                <span className="text-sm">{t("strategies.features.tunnel")}</span>
                <Switch checked={enableTunnel} onCheckedChange={setEnableTunnel} />
              </div>
              <div className="flex items-center justify-between py-1">
                <span className="text-sm">{t("strategies.features.remoteRestart")}</span>
                <Switch checked={enableRestart} onCheckedChange={setEnableRestart} />
              </div>
              <div className="flex items-center justify-between py-1 sm:col-span-2 border-t pt-2 mt-1">
                <div>
                  <span className="text-sm font-medium">{t("strategies.features.recordSession")}</span>
                  <p className="text-xs text-muted-foreground">{t("strategies.features.recordSessionDesc")}</p>
                </div>
                <Switch checked={enableRecording} onCheckedChange={setEnableRecording} />
              </div>
            </div>
          </div>

          {/* Section: Security Enforcements */}
          <div className="border rounded-lg p-3 space-y-3 bg-card">
            <h4 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-1.5">
              <ShieldAlert className="h-3.5 w-3.5" />
              {t("strategies.sections.security")}
            </h4>

            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <div>
                  <p className="text-sm font-medium">{t("strategies.security.allowConfigMod.title")}</p>
                  <p className="text-xs text-muted-foreground">{t("strategies.security.allowConfigMod.desc")}</p>
                </div>
                <Switch checked={allowConfigMod} onCheckedChange={setAllowConfigMod} />
              </div>

              <div className="flex items-center justify-between">
                <div>
                  <p className="text-sm font-medium">{t("strategies.security.lockAfterSession.title")}</p>
                  <p className="text-xs text-muted-foreground">{t("strategies.security.lockAfterSession.desc")}</p>
                </div>
                <Switch checked={lockAfterSession} onCheckedChange={setLockAfterSession} />
              </div>
            </div>
          </div>
        </div>

        <DialogFooter className="pt-2">
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button type="button" onClick={() => save.mutate()} disabled={!name.trim() || save.isPending}>
            {save.isPending ? t("common.saving") : t("common.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function StrategiesTab() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const errMessage = useErrorMessage();

  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingStrat, setEditingStrat] = useState<ClientStrategy | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<ClientStrategy | null>(null);

  const q = useQuery({
    queryKey: strategyKeys.list(),
    queryFn: strategiesApi.list,
  });

  const setDefault = useMutation({
    mutationFn: (id: number) => strategiesApi.setDefault(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: strategyKeys.all });
      toast.success(t("strategies.toasts.defaultSet"));
    },
    onError: (err) => {
      toast.error(errMessage(err));
    },
  });

  const del = useMutation({
    mutationFn: (id: number) => strategiesApi.delete(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: strategyKeys.all });
      toast.success(t("strategies.toasts.deleted"));
      setDeleteTarget(null);
    },
    onError: (err) => {
      toast.error(errMessage(err));
    },
  });

  if (q.isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-32 w-full" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  if (q.isError) {
    return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  }

  const items = q.data?.items ?? [];

  return (
    <div className="space-y-6">
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h3 className="text-lg font-medium">{t("strategies.title")}</h3>
          <p className="text-sm text-muted-foreground">{t("strategies.subtitle")}</p>
        </div>
        <Button
          onClick={() => {
            setEditingStrat(null);
            setDialogOpen(true);
          }}
          className="gap-1.5"
        >
          <Plus className="h-4 w-4" />
          {t("strategies.newStrategy")}
        </Button>
      </div>

      {items.length === 0 ? (
        <EmptyState
          icon={FolderTree}
          title={t("strategies.emptyTitle")}
          description={t("strategies.emptyDesc")}
          action={
            <Button
              onClick={() => {
                setEditingStrat(null);
                setDialogOpen(true);
              }}
            >
              {t("strategies.newStrategy")}
            </Button>
          }
        />
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {items.map((strat) => {
            const opts = strat.config_options || {};
            return (
              <Card key={strat.id} className={strat.is_default ? "border-primary/50 shadow-sm" : undefined}>
                <CardHeader className="pb-3">
                  <div className="flex items-start justify-between gap-2">
                    <div className="space-y-1">
                      <div className="flex items-center gap-2 flex-wrap">
                        <CardTitle className="text-base font-semibold">{strat.name}</CardTitle>
                        {strat.is_default && (
                          <Badge variant="success" className="gap-1 text-xs">
                            <Star className="h-3 w-3 fill-current" />
                            {t("strategies.badges.default")}
                          </Badge>
                        )}
                        <Badge variant="secondary" className="text-xs">
                          {t("strategies.devicesCount", { count: strat.device_count })}
                        </Badge>
                      </div>
                      {strat.note && <CardDescription className="text-xs line-clamp-2">{strat.note}</CardDescription>}
                    </div>

                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button variant="ghost" size="icon" className="h-8 w-8 -mr-2">
                          <MoreHorizontal className="h-4 w-4" />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem
                          onClick={() => {
                            setEditingStrat(strat);
                            setDialogOpen(true);
                          }}
                        >
                          <Pencil className="h-4 w-4 mr-2" />
                          {t("common.edit")}
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          onClick={() => {
                            setEditingStrat({
                              ...strat,
                              id: 0,
                              name: `${strat.name} (Copy)`,
                              is_default: false,
                            });
                            setDialogOpen(true);
                          }}
                        >
                          <Copy className="h-4 w-4 mr-2" />
                          {t("strategies.actions.duplicate")}
                        </DropdownMenuItem>
                        {!strat.is_default && (
                          <DropdownMenuItem onClick={() => setDefault.mutate(strat.id)}>
                            <Check className="h-4 w-4 mr-2" />
                            {t("strategies.actions.makeDefault")}
                          </DropdownMenuItem>
                        )}
                        <DropdownMenuSeparator />
                        <DropdownMenuItem
                          disabled={strat.is_default}
                          className="text-destructive focus:text-destructive"
                          onClick={() => setDeleteTarget(strat)}
                        >
                          <Trash2 className="h-4 w-4 mr-2" />
                          {t("common.delete")}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                </CardHeader>

                <CardContent className="space-y-3 pt-0 text-xs">
                  <div className="space-y-1.5 p-2.5 rounded-md bg-muted/40 border">
                    <div className="flex justify-between py-0.5">
                      <span className="text-muted-foreground">{t("strategies.summary.mode")}:</span>
                      <span className="font-medium capitalize">{opts["approve-mode"] ?? "both"}</span>
                    </div>
                    <div className="flex justify-between py-0.5">
                      <span className="text-muted-foreground">{t("strategies.summary.auth")}:</span>
                      <span className="font-medium">
                        {opts["verification-method"] === "use-temporary-password"
                          ? t("strategies.verificationMethod.temporary")
                          : opts["verification-method"] === "use-permanent-password"
                          ? t("strategies.verificationMethod.permanent")
                          : t("strategies.verificationMethod.both")}
                      </span>
                    </div>
                  </div>

                  <div className="flex flex-wrap gap-1">
                    <Badge variant={opts["enable-clipboard"] === "N" ? "outline" : "secondary"} className="text-[10px]">
                      {opts["enable-clipboard"] === "N" ? "✕ Clipboard" : "✓ Clipboard"}
                    </Badge>
                    <Badge variant={opts["enable-file-transfer"] === "N" ? "outline" : "secondary"} className="text-[10px]">
                      {opts["enable-file-transfer"] === "N" ? "✕ Files" : "✓ Files"}
                    </Badge>
                    <Badge variant={opts["allow-remote-config-modification"] === "Y" ? "destructive" : "secondary"} className="text-[10px]">
                      {opts["allow-remote-config-modification"] === "Y" ? "⚠️ Local config editable" : "🔒 Config locked"}
                    </Badge>
                    {opts["lock-after-session-end"] === "Y" && (
                      <Badge variant="secondary" className="text-[10px]">
                        🔒 Lock on exit
                      </Badge>
                    )}
                    {opts["direct-server"] === "Y" && (
                      <Badge variant="secondary" className="text-[10px]">
                        ⚡ Direct LAN
                      </Badge>
                    )}
                  </div>
                </CardContent>
              </Card>
            );
          })}
        </div>
      )}

      {dialogOpen && (
        <StrategyDialog
          open={dialogOpen}
          onOpenChange={(open) => {
            setDialogOpen(open);
            if (!open) setEditingStrat(null);
          }}
          strategy={editingStrat}
        />
      )}

      {deleteTarget && (
        <ConfirmDialog
          open={Boolean(deleteTarget)}
          onOpenChange={(open) => !open && setDeleteTarget(null)}
          title={t("strategies.deleteTitle", { name: deleteTarget.name })}
          description={t("strategies.deleteDesc")}
          confirmLabel={t("common.delete")}
          destructive
          pending={del.isPending}
          onConfirm={() => del.mutate(deleteTarget.id)}
        />
      )}
    </div>
  );
}
