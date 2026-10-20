import { Plus, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/misc";
import { Select, SelectContent, SelectItem, SelectSeparator, SelectTrigger, SelectValue } from "@/components/ui/select";
import {
  decodeConfigString,
  hostFromInput,
  idUrlFromInput,
  isIpHost,
  isValidServer,
  loadUserServers,
  saveUserServers,
  serverFromConfig,
  type OtherServer,
} from "./servers";

const OWN = "own";
const ADD = "__add__";

/**
 * Server choice for the connect form: this panel's server, servers the admin
 * offers, and (when allowed) servers the user added in this browser.
 */
export function ServerPicker({
  adminServers,
  allowCustom,
  value,
  onChange,
}: {
  adminServers: OtherServer[];
  allowCustom: boolean;
  value: string;
  onChange: (id: string, server: OtherServer | null) => void;
}) {
  const { t } = useTranslation();
  const [userServers, setUserServers] = useState<OtherServer[]>(() => (allowCustom ? loadUserServers() : []));
  const [adding, setAdding] = useState(false);
  const all = useMemo(() => [...adminServers, ...userServers], [adminServers, userServers]);
  const current = all.find((s) => s.id === value) ?? null;
  const effective = current ? value : OWN;

  // Keep the parent in sync with the stored choice (or fall back to our server).
  useEffect(() => {
    onChange(effective, current);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [effective, current?.id]);

  return (
    <div className="space-y-1.5">
      <Label>{t("webclient.servers.label")}</Label>
      <div className="flex gap-1.5">
        <Select
          value={effective}
          onValueChange={(v) => {
            if (v === ADD) {
              setAdding(true);
              return;
            }
            onChange(v, all.find((s) => s.id === v) ?? null);
          }}
        >
          <SelectTrigger className="min-w-0 flex-1" aria-label={t("webclient.servers.label")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={OWN}>{t("webclient.servers.own", { host: window.location.hostname })}</SelectItem>
            {adminServers.map((s) => (
              <SelectItem key={s.id} value={s.id}>
                {s.name}
              </SelectItem>
            ))}
            {userServers.length > 0 && <SelectSeparator />}
            {userServers.map((s) => (
              <SelectItem key={s.id} value={s.id}>
                {s.name}
              </SelectItem>
            ))}
            {allowCustom && (
              <>
                <SelectSeparator />
                <SelectItem value={ADD}>
                  <span className="inline-flex items-center gap-1.5">
                    <Plus className="h-3.5 w-3.5" />
                    {t("webclient.servers.add")}
                  </span>
                </SelectItem>
              </>
            )}
          </SelectContent>
        </Select>
        {current?.source === "user" && (
          <Button
            type="button"
            variant="outline"
            size="icon"
            title={t("webclient.servers.remove")}
            aria-label={t("webclient.servers.remove")}
            onClick={() => {
              const next = userServers.filter((s) => s.id !== current.id);
              setUserServers(next);
              saveUserServers(next);
              onChange(OWN, null);
            }}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        )}
      </div>
      {current && <p className="text-xs text-muted-foreground">{t("webclient.servers.otherHint")}</p>}
      <AddServerDialog
        open={adding}
        onClose={() => setAdding(false)}
        onAdd={(s) => {
          const next = [...userServers, s];
          setUserServers(next);
          saveUserServers(next);
          setAdding(false);
          onChange(s.id, s);
        }}
      />
    </div>
  );
}

function AddServerDialog({ open, onClose, onAdd }: { open: boolean; onClose: () => void; onAdd: (s: OtherServer) => void }) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  const [host, setHost] = useState("");
  const [relay, setRelay] = useState("");
  const [key, setKey] = useState("");
  const [configString, setConfigString] = useState("");
  const [configError, setConfigError] = useState(false);
  useEffect(() => {
    if (!open) return;
    setName("");
    setHost("");
    setRelay("");
    setKey("");
    setConfigString("");
    setConfigError(false);
  }, [open]);

  const idUrl = idUrlFromInput(host);
  const relayUrl = relay.trim() ? idUrlFromInput(relay).replace(/\/ws\/id$/, "/ws/relay") : "";
  const candidate = { idUrl, relayUrl, key: key.trim() };
  const valid = !!name.trim() && isValidServer(candidate);

  const applyConfig = (v: string) => {
    setConfigString(v);
    if (!v.trim()) {
      setConfigError(false);
      return;
    }
    const c = decodeConfigString(v);
    if (!c) {
      setConfigError(true);
      return;
    }
    setConfigError(false);
    const sv = serverFromConfig(c);
    if (sv.host) setHost(sv.host);
    setRelay(sv.relay);
    if (c.key) setKey(c.key);
    if (!name && sv.name) setName(sv.name);
  };

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-[calc(100vw-2rem)] sm:max-w-md">
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!valid) return;
            onAdd({ id: `user:${crypto.randomUUID()}`, name: name.trim(), ...candidate, source: "user" });
          }}
        >
          <DialogHeader>
            <DialogTitle>{t("webclient.servers.addTitle")}</DialogTitle>
            <DialogDescription>{t("webclient.servers.addDesc")}</DialogDescription>
          </DialogHeader>
          <div className="space-y-1.5">
            <Label htmlFor="srv-config">{t("webclient.servers.configString")}</Label>
            <Textarea
              id="srv-config"
              rows={2}
              value={configString}
              onChange={(e) => applyConfig(e.target.value)}
              placeholder="=0nI9…"
              className="font-mono text-xs"
            />
            {configError && <p className="text-xs text-destructive">{t("webclient.servers.configInvalid")}</p>}
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="srv-name">{t("webclient.servers.name")}</Label>
            <Input id="srv-name" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="srv-host">{t("webclient.servers.host")}</Label>
            <Input id="srv-host" value={host} onChange={(e) => setHost(e.target.value)} placeholder="rd.example.com" autoComplete="off" />
            {idUrl && <p className="break-all text-xs text-muted-foreground">{idUrl}</p>}
            {isIpHost(hostFromInput(host)) && <p className="text-xs text-amber-600 dark:text-amber-400">{t("webclient.servers.ipWarning")}</p>}
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="srv-relay">{t("webclient.servers.relay")}</Label>
            <Input id="srv-relay" value={relay} onChange={(e) => setRelay(e.target.value)} placeholder={t("webclient.servers.relayPlaceholder")} autoComplete="off" />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="srv-key">{t("webclient.servers.key")}</Label>
            <Input id="srv-key" value={key} onChange={(e) => setKey(e.target.value)} placeholder="MEbIUN8…=" className="font-mono text-xs" autoComplete="off" />
          </div>
          <p className="text-xs text-muted-foreground">{t("webclient.servers.requirements")}</p>
          <DialogFooter className="gap-2">
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={!valid}>
              {t("common.add")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
