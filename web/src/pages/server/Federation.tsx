import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertCircle, CheckCircle2, Globe2, HelpCircle, Loader2, Network, Plus, RefreshCw, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import { federationApi, federationKeys, type CreateFederationPeerInput, type FederationPeer } from "@/api/federation";
import { ConfirmDialog, EmptyState, ErrorState, Field, Mono, RelativeTime, useErrorMessage } from "@/components";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton, Switch } from "@/components/ui/misc";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "@/lib/utils";

/** Minimum length of a peer's RDPANEL_FEDERATION_TOKEN (server-side check too). */
const MIN_TOKEN_LEN = 16;

const isFederationDisabled = (err: unknown) => err instanceof ApiError && err.code === "federation_disabled";

function PeerStatusBadge({ status, error }: { status: FederationPeer["status"]; error?: string | null }) {
  const { t } = useTranslation();
  if (status === "active") {
    return (
      <Badge variant="success" className="gap-1">
        <CheckCircle2 className="h-3 w-3" />
        {t("federation.status.active")}
      </Badge>
    );
  }
  if (status === "unreachable") {
    return (
      <Badge variant="destructive" className="gap-1" title={error ?? undefined}>
        <AlertCircle className="h-3 w-3" />
        {t("federation.status.unreachable")}
      </Badge>
    );
  }
  return (
    <Badge variant="outline" className="gap-1">
      <HelpCircle className="h-3 w-3" />
      {t("federation.status.unknown")}
    </Badge>
  );
}

function AddPeerDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const errMessage = useErrorMessage();

  const [name, setName] = useState("");
  const [apiUrl, setApiUrl] = useState("");
  const [authToken, setAuthToken] = useState("");
  const [syncRelays, setSyncRelays] = useState(true);

  const reset = () => {
    setName("");
    setApiUrl("");
    setAuthToken("");
    setSyncRelays(true);
  };

  const create = useMutation({
    mutationFn: (body: CreateFederationPeerInput) => federationApi.createPeer(body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: federationKeys.peers });
      void qc.invalidateQueries({ queryKey: federationKeys.relays });
      toast.success(t("federation.toasts.peerAdded"));
      reset();
      onOpenChange(false);
    },
    onError: (err) => {
      toast.error(errMessage(err));
    },
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim() || !apiUrl.trim() || authToken.trim().length < MIN_TOKEN_LEN) return;
    create.mutate({
      name: name.trim(),
      api_url: apiUrl.trim(),
      auth_token: authToken.trim(),
      sync_relays: syncRelays,
    });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[480px]">
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>{t("federation.addDialog.title")}</DialogTitle>
            <DialogDescription>{t("federation.addDialog.description")}</DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <Field label={t("federation.peers.name")}>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t("federation.addDialog.namePlaceholder")}
                disabled={create.isPending}
                required
              />
            </Field>

            <Field label={t("federation.peers.apiUrl")}>
              <Input
                value={apiUrl}
                onChange={(e) => setApiUrl(e.target.value)}
                placeholder={t("federation.addDialog.urlPlaceholder")}
                type="url"
                disabled={create.isPending}
                required
              />
            </Field>

            <Field label={t("federation.peers.authToken")} htmlFor="fed-token" hint={t("federation.addDialog.tokenHint")}>
              <Input
                id="fed-token"
                value={authToken}
                onChange={(e) => setAuthToken(e.target.value)}
                placeholder={t("federation.addDialog.tokenPlaceholder")}
                type="password"
                autoComplete="off"
                minLength={MIN_TOKEN_LEN}
                disabled={create.isPending}
                required
              />
            </Field>

            <div className="flex items-center justify-between rounded-lg border p-3">
              <div className="space-y-0.5">
                <span className="text-sm font-medium">{t("federation.peers.syncRelays")}</span>
                <p className="text-xs text-muted-foreground">{t("federation.peers.syncRelaysDesc")}</p>
              </div>
              <Switch checked={syncRelays} onCheckedChange={setSyncRelays} disabled={create.isPending} />
            </div>
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={create.isPending || !name || !apiUrl || authToken.trim().length < MIN_TOKEN_LEN}>
              {create.isPending && <Loader2 className="mr-1.5 h-4 w-4 animate-spin" />}
              {t("federation.addDialog.submit")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function FederationTab() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const errMessage = useErrorMessage();

  const [addingPeer, setAddingPeer] = useState(false);
  const [deletingPeer, setDeletingPeer] = useState<FederationPeer | null>(null);

  // "Federation is off" is a 503 that retrying cannot change.
  const retry = (count: number, err: unknown) =>
    !isFederationDisabled(err) && !(err instanceof ApiError && err.status < 500) && count < 2;

  const peersQuery = useQuery({
    queryKey: federationKeys.peers,
    queryFn: federationApi.listPeers,
    retry,
  });

  const relaysQuery = useQuery({
    queryKey: federationKeys.relays,
    queryFn: federationApi.listRelays,
    retry,
    enabled: !isFederationDisabled(peersQuery.error),
  });

  const syncPeer = useMutation({
    mutationFn: (id: number) => federationApi.syncPeer(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: federationKeys.peers });
      void qc.invalidateQueries({ queryKey: federationKeys.relays });
      toast.success(t("federation.toasts.syncSuccess"));
    },
    onError: (err) => {
      toast.error(errMessage(err));
    },
  });

  const deletePeer = useMutation({
    mutationFn: (id: number) => federationApi.deletePeer(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: federationKeys.peers });
      void qc.invalidateQueries({ queryKey: federationKeys.relays });
      toast.success(t("federation.toasts.peerDeleted"));
      setDeletingPeer(null);
    },
    onError: (err) => {
      toast.error(errMessage(err));
    },
  });

  const peers = peersQuery.data ?? [];
  const federatedRelays = relaysQuery.data ?? [];

  if (isFederationDisabled(peersQuery.error)) {
    return (
      <Card>
        <CardContent className="pt-6">
          <EmptyState icon={Globe2} title={t("federation.disabled.title")} description={t("federation.disabled.description")} />
        </CardContent>
      </Card>
    );
  }

  return (
    <div className="space-y-6">
      {/* ── Peers Card ──────────────────────────────────────────────────────── */}
      <Card>
        <CardHeader className="flex flex-row items-center justify-between gap-4 pb-3">
          <div>
            <CardTitle className="text-base">{t("federation.peers.title")}</CardTitle>
            <CardDescription>{t("federation.peers.description")}</CardDescription>
          </div>
          <Button onClick={() => setAddingPeer(true)} className="gap-1.5" size="sm">
            <Plus className="h-4 w-4" />
            {t("federation.peers.add")}
          </Button>
        </CardHeader>
        <CardContent>
          {peersQuery.isError ? (
            <ErrorState error={peersQuery.error} onRetry={() => void peersQuery.refetch()} />
          ) : peersQuery.isLoading ? (
            <div className="space-y-2">
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-12 w-full" />
            </div>
          ) : peers.length === 0 ? (
            <EmptyState
              icon={Globe2}
              title={t("federation.peers.title")}
              description={t("federation.peers.empty")}
              action={
                <Button onClick={() => setAddingPeer(true)} variant="outline" size="sm">
                  <Plus className="h-4 w-4" />
                  {t("federation.peers.add")}
                </Button>
              }
            />
          ) : (
            <div className="rounded-md border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("federation.peers.name")}</TableHead>
                    <TableHead>{t("federation.peers.apiUrl")}</TableHead>
                    <TableHead>{t("federation.peers.status")}</TableHead>
                    <TableHead>{t("federation.peers.syncRelays")}</TableHead>
                    <TableHead>{t("federation.peers.lastSync")}</TableHead>
                    <TableHead className="w-[120px] text-right">{t("common.actions")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {peers.map((p) => {
                    const isSyncing = syncPeer.isPending && syncPeer.variables === p.id;
                    return (
                      <TableRow key={p.id}>
                        <TableCell className="font-medium">{p.name}</TableCell>
                        <TableCell>
                          <Mono className="text-xs">{p.api_url}</Mono>
                        </TableCell>
                        <TableCell>
                          <PeerStatusBadge status={p.status} error={p.last_error} />
                          {p.status === "unreachable" && p.last_error && (
                            <div className="mt-0.5 max-w-[16rem] truncate text-xs text-muted-foreground" title={p.last_error}>
                              {p.last_error}
                            </div>
                          )}
                        </TableCell>
                        <TableCell>
                          <Badge variant={p.sync_relays ? "secondary" : "outline"}>
                            {p.sync_relays ? t("common.enabled") : t("common.disabled")}
                          </Badge>
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {p.last_sync_at ? <RelativeTime iso={p.last_sync_at} /> : "—"}
                        </TableCell>
                        <TableCell className="text-right">
                          <div className="flex items-center justify-end gap-1">
                            <Button
                              variant="ghost"
                              size="icon"
                              title={t("federation.peers.syncNow")}
                              disabled={isSyncing}
                              onClick={() => syncPeer.mutate(p.id)}
                            >
                              <RefreshCw className={cn("h-4 w-4", isSyncing && "animate-spin")} />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="text-destructive hover:text-destructive"
                              title={t("common.delete")}
                              onClick={() => setDeletingPeer(p)}
                            >
                              <Trash2 className="h-4 w-4" />
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>

      {/* ── Federated Relays Card ───────────────────────────────────────────── */}
      <Card>
        <CardHeader className="flex flex-row items-center justify-between gap-4 pb-3">
          <div>
            <CardTitle className="text-base">{t("federation.relays.title")}</CardTitle>
            <CardDescription>{t("federation.relays.description")}</CardDescription>
          </div>
          <Badge variant="outline" className="gap-1">
            <Network className="h-3 w-3" />
            {federatedRelays.length}
          </Badge>
        </CardHeader>
        <CardContent>
          {relaysQuery.isError ? (
            <ErrorState error={relaysQuery.error} onRetry={() => void relaysQuery.refetch()} />
          ) : relaysQuery.isLoading ? (
            <div className="space-y-2">
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-12 w-full" />
            </div>
          ) : federatedRelays.length === 0 ? (
            <EmptyState
              icon={Network}
              title={t("federation.relays.title")}
              description={t("federation.relays.empty")}
            />
          ) : (
            <div className="rounded-md border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("federation.relays.remoteName")}</TableHead>
                    <TableHead>{t("federation.relays.publicHost")}</TableHead>
                    <TableHead>{t("federation.relays.peer")}</TableHead>
                    <TableHead>{t("federation.relays.region")}</TableHead>
                    <TableHead>{t("federation.relays.health")}</TableHead>
                    <TableHead className="text-right">{t("federation.relays.lastSync")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {federatedRelays.map((r) => (
                    <TableRow key={r.id}>
                      <TableCell className="font-medium">{r.remote_name}</TableCell>
                      <TableCell>
                        <Mono className="text-xs">{r.public_host}</Mono>
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline">{r.peer_name || `#${r.peer_id}`}</Badge>
                      </TableCell>
                      <TableCell>
                        {r.region ? <Badge variant="secondary">{r.region.toUpperCase()}</Badge> : "—"}
                      </TableCell>
                      <TableCell>
                        <Badge variant="success" className="gap-1">
                          <CheckCircle2 className="h-3 w-3" />
                          {r.health}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-right text-xs text-muted-foreground">
                        <RelativeTime iso={r.last_sync_at} />
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>

      <AddPeerDialog open={addingPeer} onOpenChange={setAddingPeer} />

      <ConfirmDialog
        open={deletingPeer !== null}
        onOpenChange={(o) => !o && setDeletingPeer(null)}
        title={t("federation.peers.deleteTitle", { name: deletingPeer?.name ?? "" })}
        description={t("federation.peers.deleteHint")}
        confirmLabel={t("common.delete")}
        destructive
        pending={deletePeer.isPending}
        onConfirm={() => deletingPeer && deletePeer.mutate(deletingPeer.id)}
      />
    </div>
  );
}
