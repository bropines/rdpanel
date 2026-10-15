import { useNavigate, useSearch } from "@tanstack/react-router";
import { useCallback, useEffect, useMemo, useState } from "react";

import { useDebounced } from "@/lib/hooks";
import { parseTableSearch, searchPatch, tableStateToQuery, type FilterValue, type TableDefaults, type TableState } from "@/lib/table-state";

type Change = Partial<Omit<TableState, "filters">> & { filters?: Record<string, FilterValue | undefined> };

function clean(o: Record<string, unknown>) {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(o)) if (v !== undefined && v !== "") out[k] = v;
  return out;
}

/**
 * Table state bound to the URL search string. `prefix` namespaces keys so several
 * tables (tabs) can share one page.
 */
export function useTableSearch(defaults: TableDefaults & { prefix?: string } = {}) {
  const raw = useSearch({ strict: false }) as Record<string, unknown>;
  const navigate = useNavigate();
  const prefix = defaults.prefix ?? "";

  const search = useMemo(() => {
    if (!prefix) return raw;
    const s: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(raw)) if (k.startsWith(prefix)) s[k.slice(prefix.length)] = v;
    return s;
  }, [raw, prefix]);

  const state = useMemo(
    () => parseTableSearch(search, defaults),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [search, defaults.sort, defaults.pageSize, (defaults.filterKeys ?? []).join(",")],
  );

  const setSearch = useCallback(
    (patch: Record<string, unknown>) => {
      const prefixed: Record<string, unknown> = {};
      for (const [k, v] of Object.entries(patch)) prefixed[prefix + k] = v;
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => clean({ ...prev, ...prefixed }),
        replace: true,
      } as never);
    },
    [navigate, prefix],
  );

  const update = useCallback((change: Change) => setSearch(searchPatch(change, defaults)), [setSearch, defaults]);

  // Search box: local text, debounced into the URL.
  const [text, setText] = useState(state.q);
  const debounced = useDebounced(text, 300);
  useEffect(() => {
    if (debounced !== state.q) update({ q: debounced });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debounced]);
  useEffect(() => {
    if (state.q !== debounced) setText(state.q);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state.q]);

  const query = useMemo(() => tableStateToQuery(state), [state]);

  return { state, query, update, setSearch, raw: search, text, setText };
}
