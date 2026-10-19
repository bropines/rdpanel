import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { addressBooksApi, bookKeys } from "@/api/addressBooks";
import { deviceKeys, devicesApi } from "@/api/devices";
import { Field, useToastError } from "@/components";
import { BookPicker, DeviceGroupPicker, StrategyPicker, UserPicker } from "@/components/pickers";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";

type Mode = "owner" | "group" | "strategy" | "book";

/** Bulk assign owner / group, or add devices to an address book. */
export function DeviceBulkDialog({
  mode,
  deviceIds,
  open,
  onOpenChange,
  onDone,
  bookScope = "all",
}: {
  mode: Mode;
  deviceIds: number[];
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onDone?: () => void;
  bookScope?: "accessible" | "all";
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const [target, setTarget] = useState<number | null>(null);
  const [targetLabel, setTargetLabel] = useState<string>();
  const [tags, setTags] = useState("");

  const run = useMutation({
    mutationFn: async () => {
      if (mode === "book") {
        if (target == null) throw new Error("no book");
        const tagList = tags.split(",").map((s) => s.trim()).filter(Boolean);
        return addressBooksApi.addFromDevices(target, { device_ids: deviceIds, tags: tagList });
      }
      if (mode === "owner") {
        return devicesApi.bulk({ action: "assign_owner", device_ids: deviceIds, owner_user_id: target });
      }
      if (mode === "group") {
        return devicesApi.bulk({ action: "assign_group", device_ids: deviceIds, device_group_id: target });
      }
      return devicesApi.bulk({ action: "assign_strategy", device_ids: deviceIds, strategy_id: target });
    },
    onSuccess: (res) => {
      if (mode === "book") {
        toast.success(t("devices.addedToBook", { count: res.affected, skipped: res.skipped ?? 0 }));
        void qc.invalidateQueries({ queryKey: bookKeys.all });
      } else {
        toast.success(t("common.updatedCount", { count: res.affected }));
      }
      void qc.invalidateQueries({ queryKey: deviceKeys.all });
      onOpenChange(false);
      setTarget(null);
      setTags("");
      onDone?.();
    },
    onError: toastError,
  });

  const title =
    mode === "owner"
      ? t("devices.assignOwner")
      : mode === "group"
      ? t("devices.assignGroup")
      : mode === "strategy"
      ? t("devices.assignStrategy")
      : t("devices.addToBook");

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{t("devices.bulkTarget", { count: deviceIds.length })}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          {mode === "owner" && (
            <Field label={t("devices.owner")} htmlFor="bulk-owner">
              <UserPicker
                id="bulk-owner"
                value={target}
                valueLabel={targetLabel}
                allowNone
                onChange={(id, u) => {
                  setTarget(id);
                  setTargetLabel(u?.display_name);
                }}
              />
            </Field>
          )}
          {mode === "group" && (
            <Field label={t("devices.group")} htmlFor="bulk-group">
              <DeviceGroupPicker
                id="bulk-group"
                value={target}
                valueLabel={targetLabel}
                onChange={(id, g) => {
                  setTarget(id);
                  setTargetLabel(g?.name);
                }}
              />
            </Field>
          )}
          {mode === "strategy" && (
            <Field label={t("devices.strategy")} htmlFor="bulk-strategy">
              <StrategyPicker
                id="bulk-strategy"
                value={target}
                valueLabel={targetLabel}
                noneLabel={t("strategies.inheritedOrDefault")}
                onChange={(id, s) => {
                  setTarget(id);
                  setTargetLabel(s?.name);
                }}
              />
            </Field>
          )}
          {mode === "book" && (
            <>
              <Field label={t("books.book")} htmlFor="bulk-book">
                <BookPicker id="bulk-book" scope={bookScope} value={target} valueLabel={targetLabel} onChange={(id, label) => {
                  setTarget(id);
                  setTargetLabel(label);
                }} />
              </Field>
              <Field label={t("books.tags")} htmlFor="bulk-tags" hint={t("books.tagsHint")}>
                <Input id="bulk-tags" value={tags} onChange={(e) => setTags(e.target.value)} placeholder="office, linux" />
              </Field>
            </>
          )}
        </div>
        <DialogFooter className="gap-2">
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button onClick={() => run.mutate()} disabled={run.isPending || (mode === "book" && target == null)}>
            {t("common.apply")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
