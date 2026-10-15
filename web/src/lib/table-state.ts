/**
 * Server-side table state lives in the URL search string so views are linkable and
 * survive reloads. These pure helpers convert between router search params, the
 * TanStack Table state and the API query (`page`, `page_size`, `sort`, `q`, filters).
 */
import type { SortingState } from "@tanstack/react-table";

export const PAGE_SIZES = [25, 50, 100, 200] as const;
export const DEFAULT_PAGE_SIZE = 50;

export type FilterValue = string | number | boolean;

export interface TableState {
  page: number;
  pageSize: number;
  sort: string | undefined;
  q: string;
  filters: Record<string, FilterValue>;
}

export interface TableDefaults {
  sort?: string;
  pageSize?: number;
  /** Filter keys this table understands (anything else in the URL is ignored). */
  filterKeys?: readonly string[];
}

const RESERVED = new Set(["page", "page_size", "sort", "q"]);

function toInt(v: unknown, fallback: number, min: number, max: number): number {
  const n = typeof v === "number" ? v : typeof v === "string" ? Number.parseInt(v, 10) : Number.NaN;
  if (!Number.isFinite(n)) return fallback;
  return Math.min(max, Math.max(min, Math.trunc(n)));
}

/** Router search object → table state (invalid values fall back to defaults). */
export function parseTableSearch(search: Record<string, unknown>, defaults: TableDefaults = {}): TableState {
  const pageSize = toInt(search.page_size, defaults.pageSize ?? DEFAULT_PAGE_SIZE, 1, 500);
  const filters: Record<string, FilterValue> = {};
  for (const key of defaults.filterKeys ?? []) {
    if (RESERVED.has(key)) continue;
    const v = search[key];
    if (typeof v === "string" && v !== "") filters[key] = v;
    else if (typeof v === "number" || typeof v === "boolean") filters[key] = v;
  }
  const sort = typeof search.sort === "string" && /^-?[a-z_]+$/.test(search.sort) ? search.sort : defaults.sort;
  return {
    page: toInt(search.page, 1, 1, 1_000_000),
    pageSize,
    sort,
    q: typeof search.q === "string" ? search.q : typeof search.q === "number" ? String(search.q) : "",
    filters,
  };
}

/** Table state → API query parameters (empty values dropped). */
export function tableStateToQuery(state: TableState): Record<string, string | number | boolean> {
  const out: Record<string, string | number | boolean> = { page: state.page, page_size: state.pageSize };
  if (state.sort) out.sort = state.sort;
  if (state.q.trim()) out.q = state.q.trim();
  for (const [k, v] of Object.entries(state.filters)) {
    if (v !== "" && v !== undefined) out[k] = v;
  }
  return out;
}

/**
 * Select widgets hand back strings; store booleans and ids as real JSON values so the
 * router writes `?online=false&owner=2` instead of quoted strings (`online=%22false%22`).
 */
export function typedFilterValue(v: FilterValue | undefined): FilterValue | undefined {
  if (v === "" || v === undefined) return undefined;
  if (v === "true") return true;
  if (v === "false") return false;
  if (typeof v === "string" && /^(0|[1-9]\d{0,15})$/.test(v)) return Number(v);
  return v;
}

/** Search patch for a state change: filter/search/sort changes reset to page 1. */
export function searchPatch(
  change: Partial<Omit<TableState, "filters">> & { filters?: Record<string, FilterValue | undefined> },
  defaults: TableDefaults = {},
): Record<string, unknown> {
  const patch: Record<string, unknown> = {};
  let resetPage = false;
  if (change.q !== undefined) {
    patch.q = change.q || undefined;
    resetPage = true;
  }
  if (change.sort !== undefined) {
    patch.sort = change.sort === defaults.sort ? undefined : change.sort;
    resetPage = true;
  }
  if (change.filters) {
    for (const [k, v] of Object.entries(change.filters)) patch[k] = typedFilterValue(v);
    resetPage = true;
  }
  if (change.pageSize !== undefined) {
    patch.page_size = change.pageSize === (defaults.pageSize ?? DEFAULT_PAGE_SIZE) ? undefined : change.pageSize;
    resetPage = true;
  }
  if (change.page !== undefined) patch.page = change.page <= 1 ? undefined : change.page;
  else if (resetPage) patch.page = undefined;
  return patch;
}

/** `-last_seen_at` → `[{id: "last_seen_at", desc: true}]`. */
export function sortParamToSorting(sort: string | undefined): SortingState {
  if (!sort) return [];
  return sort.startsWith("-") ? [{ id: sort.slice(1), desc: true }] : [{ id: sort, desc: false }];
}

/** Inverse of sortParamToSorting (first column only; the API sorts by one key). */
export function sortingToSortParam(sorting: SortingState): string | undefined {
  const first = sorting[0];
  if (!first) return undefined;
  return first.desc ? `-${first.id}` : first.id;
}

/** Total number of pages (at least 1). */
export function pageCount(total: number, pageSize: number): number {
  return Math.max(1, Math.ceil(total / Math.max(1, pageSize)));
}
