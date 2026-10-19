import { useQuery } from "@tanstack/react-query";
import { Check, ChevronsUpDown, Loader2 } from "lucide-react";
import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { addressBooksApi } from "@/api/addressBooks";
import { devicesApi } from "@/api/devices";
import { strategiesApi } from "@/api/strategies";
import { groupsApi, lookupApi, type GroupRef, type UserRef } from "@/api/users";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/misc";
import { userLabel } from "@/lib/format";
import { useDebounced } from "@/lib/hooks";
import { cn } from "@/lib/utils";

interface Option {
  id: number;
  label: string;
  hint?: string;
}

/** Searchable single-select with async options and an optional "none" choice. */
export function AsyncCombobox({
  value,
  valueLabel,
  onChange,
  queryKey,
  fetchOptions,
  placeholder,
  noneLabel,
  disabled,
  id,
  className,
}: {
  value: number | null | undefined;
  valueLabel?: ReactNode;
  onChange: (id: number | null, option?: Option) => void;
  queryKey: string;
  fetchOptions: (q: string) => Promise<Option[]>;
  placeholder: string;
  /** When set, a "none" option clears the value. */
  noneLabel?: string;
  disabled?: boolean;
  id?: string;
  className?: string;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const dq = useDebounced(q, 200);
  const { data, isFetching } = useQuery({
    queryKey: ["lookup", queryKey, dq],
    queryFn: () => fetchOptions(dq),
    enabled: open,
    staleTime: 30_000,
  });
  const options = data ?? [];
  const pick = (o: Option | null) => {
    onChange(o ? o.id : null, o ?? undefined);
    setOpen(false);
    setQ("");
  };
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          id={id}
          type="button"
          variant="outline"
          role="combobox"
          aria-expanded={open}
          disabled={disabled}
          className={cn("w-full justify-between font-normal", className)}
        >
          <span className={cn("truncate", value == null && "text-muted-foreground")}>
            {value == null ? (noneLabel ?? placeholder) : (valueLabel ?? `#${value}`)}
          </span>
          <ChevronsUpDown className="opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-[var(--radix-popover-trigger-width)] min-w-64 p-0">
        <div className="border-b p-2">
          <Input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder={t("common.search")}
            className="h-8"
            onKeyDown={(e) => {
              if (e.key === "Enter" && options[0]) {
                e.preventDefault();
                pick(options[0]);
              }
            }}
          />
        </div>
        <ul className="max-h-64 overflow-y-auto p-1" role="listbox">
          {noneLabel && (
            <li>
              <button
                type="button"
                className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm text-muted-foreground hover:bg-accent"
                onClick={() => pick(null)}
              >
                <Check className={cn("h-4 w-4", value == null ? "opacity-100" : "opacity-0")} />
                {noneLabel}
              </button>
            </li>
          )}
          {isFetching && options.length === 0 && (
            <li className="flex items-center gap-2 px-2 py-3 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" /> {t("common.loading")}
            </li>
          )}
          {!isFetching && options.length === 0 && (
            <li className="px-2 py-3 text-sm text-muted-foreground">{t("common.noResults")}</li>
          )}
          {options.map((o) => (
            <li key={o.id}>
              <button
                type="button"
                role="option"
                aria-selected={o.id === value}
                className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-accent focus:bg-accent focus:outline-none"
                onClick={() => pick(o)}
              >
                <Check className={cn("h-4 w-4 shrink-0", o.id === value ? "opacity-100" : "opacity-0")} />
                <span className="truncate">{o.label}</span>
                {o.hint && <span className="ml-auto truncate text-xs text-muted-foreground">{o.hint}</span>}
              </button>
            </li>
          ))}
        </ul>
      </PopoverContent>
    </Popover>
  );
}

export function UserPicker(props: {
  value: number | null | undefined;
  valueLabel?: ReactNode;
  onChange: (id: number | null, user?: UserRef) => void;
  allowNone?: boolean;
  disabled?: boolean;
  id?: string;
}) {
  const { t } = useTranslation();
  return (
    <AsyncCombobox
      id={props.id}
      value={props.value}
      valueLabel={props.valueLabel}
      disabled={props.disabled}
      onChange={(id, o) =>
        props.onChange(id, o ? { id: o.id, username: o.hint ?? o.label, display_name: o.label } : undefined)
      }
      queryKey="users"
      fetchOptions={async (q) =>
        (await lookupApi.users(q)).items.map((u) => ({ id: u.id, label: userLabel(u), hint: u.username }))
      }
      placeholder={t("pickers.selectUser")}
      noneLabel={props.allowNone ? t("pickers.noOwner") : undefined}
    />
  );
}

export function UserGroupPicker(props: {
  value: number | null | undefined;
  valueLabel?: ReactNode;
  onChange: (id: number | null, group?: GroupRef) => void;
  allowNone?: boolean;
  id?: string;
}) {
  const { t } = useTranslation();
  return (
    <AsyncCombobox
      id={props.id}
      value={props.value}
      valueLabel={props.valueLabel}
      onChange={(id, o) => props.onChange(id, o ? { id: o.id, name: o.label } : undefined)}
      queryKey="user-groups"
      fetchOptions={async (q) => (await lookupApi.userGroups(q)).items.map((g) => ({ id: g.id, label: g.name }))}
      placeholder={t("pickers.selectGroup")}
      noneLabel={props.allowNone ? t("pickers.noGroup") : undefined}
    />
  );
}

export function DeviceGroupPicker(props: {
  value: number | null | undefined;
  valueLabel?: ReactNode;
  onChange: (id: number | null, group?: GroupRef) => void;
  id?: string;
}) {
  const { t } = useTranslation();
  return (
    <AsyncCombobox
      id={props.id}
      value={props.value}
      valueLabel={props.valueLabel}
      onChange={(id, o) => props.onChange(id, o ? { id: o.id, name: o.label } : undefined)}
      queryKey="device-groups"
      fetchOptions={async (q) =>
        (await groupsApi.listDeviceGroups({ q, page_size: 50 })).items.map((g) => ({
          id: g.id,
          label: g.name,
          hint: String(g.device_count),
        }))
      }
      placeholder={t("pickers.selectDeviceGroup")}
      noneLabel={t("pickers.noGroup")}
    />
  );
}

/** Books the caller can write to (for "add to address book"). */
export function BookPicker(props: {
  value: number | null | undefined;
  valueLabel?: ReactNode;
  onChange: (id: number | null, label?: string) => void;
  scope?: "accessible" | "all";
  id?: string;
}) {
  const { t } = useTranslation();
  return (
    <AsyncCombobox
      id={props.id}
      value={props.value}
      valueLabel={props.valueLabel}
      onChange={(id, o) => props.onChange(id, o?.label)}
      queryKey={`books-${props.scope ?? "accessible"}`}
      fetchOptions={async (q) =>
        (await addressBooksApi.list({ q, page_size: 100, scope: props.scope ?? "accessible" })).items
          .filter((b) => b.permission !== "read")
          .map((b) => ({
            id: b.id,
            label: b.is_personal ? `${b.name} · ${b.owner.username}` : b.name,
            hint: b.is_personal ? t("books.personal") : t("books.shared"),
          }))
      }
      placeholder={t("pickers.selectBook")}
    />
  );
}

export function StrategyPicker(props: {
  value: number | null | undefined;
  valueLabel?: ReactNode;
  onChange: (id: number | null, strategy?: { id: number; name: string }) => void;
  noneLabel?: string;
  disabled?: boolean;
  id?: string;
}) {
  const { t } = useTranslation();
  return (
    <AsyncCombobox
      id={props.id}
      value={props.value}
      valueLabel={props.valueLabel}
      disabled={props.disabled}
      onChange={(id, o) => props.onChange(id, o ? { id: o.id, name: o.label } : undefined)}
      queryKey="strategies"
      fetchOptions={async () =>
        (await strategiesApi.list()).items.map((s) => ({
          id: s.id,
          label: s.name,
          hint: s.is_default ? t("strategies.default") : undefined,
        }))
      }
      placeholder={t("pickers.selectStrategy")}
      noneLabel={props.noneLabel ?? t("strategies.inheritedOrDefault")}
    />
  );
}

export function DevicePicker(props: {
  value: number | null | undefined;
  valueLabel?: ReactNode;
  onChange: (id: number | null, device?: { id: number; name: string }) => void;
  allowNone?: boolean;
  noneLabel?: string;
  disabled?: boolean;
  id?: string;
}) {
  const { t } = useTranslation();
  return (
    <AsyncCombobox
      id={props.id}
      value={props.value}
      valueLabel={props.valueLabel}
      disabled={props.disabled}
      onChange={(id, o) => props.onChange(id, o ? { id: o.id, name: o.label } : undefined)}
      queryKey="devices"
      fetchOptions={async (q) =>
        (await devicesApi.list({ q, page_size: 50 })).items.map((d) => ({
          id: d.id,
          label: d.alias || d.hostname || d.rustdesk_id,
          hint: d.rustdesk_id,
        }))
      }
      placeholder={t("pickers.selectDevice")}
      noneLabel={props.allowNone ? (props.noneLabel ?? t("pickers.noDevice")) : undefined}
    />
  );
}

