import { describe, expect, it } from "vitest";

import { buildQuery } from "@/api/client";
import { parseTableSearch, searchPatch, sortingToSortParam, sortParamToSorting, tableStateToQuery, pageCount } from "@/lib/table-state";
import { buildDeviceQuery, seenAfter } from "@/pages/devices/DevicesPage";
import { auditQuery } from "@/pages/AuditPage";
import { parseTagFilter } from "@/pages/address-books/AddressBooksPage";

const defaults = { sort: "-last_seen_at", filterKeys: ["online", "owner", "os"] as const };

describe("table URL state", () => {
  it("parses search params with defaults and clamping", () => {
    expect(parseTableSearch({}, defaults)).toEqual({ page: 1, pageSize: 50, sort: "-last_seen_at", q: "", filters: {} });
    expect(
      parseTableSearch({ page: "3", page_size: 9999, sort: "hostname", q: "srv", online: true, owner: "me", junk: "x" }, defaults),
    ).toEqual({ page: 3, pageSize: 500, sort: "hostname", q: "srv", filters: { online: true, owner: "me" } });
  });

  it("rejects malformed sort and page values", () => {
    const s = parseTableSearch({ page: "-4", sort: "drop table;" }, defaults);
    expect(s.page).toBe(1);
    expect(s.sort).toBe("-last_seen_at");
  });

  it("builds the API query (trimmed search, empty values dropped)", () => {
    const q = tableStateToQuery({ page: 2, pageSize: 25, sort: "-hostname", q: "  pc ", filters: { online: false, os: "" } });
    expect(q).toEqual({ page: 2, page_size: 25, sort: "-hostname", q: "pc", online: false });
  });

  it("resets the page when filters, search, sort or size change", () => {
    expect(searchPatch({ filters: { online: true } }, defaults)).toEqual({ online: true, page: undefined });
    expect(searchPatch({ filters: { owner: "2", os: "linux", online: "false", version: "1.4.2", x: "" } }, defaults)).toEqual({
      owner: 2,
      os: "linux",
      online: false,
      version: "1.4.2",
      x: undefined,
      page: undefined,
    });
    expect(searchPatch({ q: "" }, defaults)).toEqual({ q: undefined, page: undefined });
    expect(searchPatch({ sort: "-last_seen_at" }, defaults)).toEqual({ sort: undefined, page: undefined });
    expect(searchPatch({ pageSize: 100 }, defaults)).toEqual({ page_size: 100, page: undefined });
    expect(searchPatch({ page: 4 }, defaults)).toEqual({ page: 4 });
    expect(searchPatch({ page: 1 }, defaults)).toEqual({ page: undefined });
  });

  it("maps sort params to TanStack sorting and back", () => {
    expect(sortParamToSorting("-last_seen_at")).toEqual([{ id: "last_seen_at", desc: true }]);
    expect(sortParamToSorting("alias")).toEqual([{ id: "alias", desc: false }]);
    expect(sortingToSortParam([{ id: "alias", desc: true }])).toBe("-alias");
    expect(sortingToSortParam([])).toBeUndefined();
    expect(pageCount(0, 50)).toBe(1);
    expect(pageCount(101, 50)).toBe(3);
  });

  it("serialises queries (arrays repeat, empties dropped)", () => {
    expect(buildQuery({ tag: ["a", "b"], q: "", page: 1, x: undefined, y: null, on: false })).toBe("tag=a&tag=b&page=1&on=false");
  });
});

describe("page query builders", () => {
  const now = Date.parse("2026-10-15T12:00:00Z");

  it("devices: boolean online, seen window, owner=me for My devices", () => {
    expect(buildDeviceQuery({ page: 1, page_size: 50, online: "true", seen: "24h", q: "pc" }, true, now)).toEqual({
      page: 1,
      page_size: 50,
      q: "pc",
      online: true,
      seen_after: "2026-10-14T12:00:00.000Z",
      owner: "me",
    });
    expect(buildDeviceQuery({ page: 1, page_size: 50, online: false, owner: "none" }, false, now)).toEqual({
      page: 1,
      page_size: 50,
      online: false,
      owner: "none",
    });
    expect(seenAfter("bogus", now)).toBeUndefined();
  });

  it("audit: range presets become from/to", () => {
    expect(auditQuery({ page: 1, page_size: 50, range: "1h", active: "true" }, now)).toEqual({
      page: 1,
      page_size: 50,
      from: "2026-10-15T11:00:00.000Z",
      active: true,
    });
    expect(auditQuery({ page: 1, page_size: 50, range: "all" }, now)).toEqual({ page: 1, page_size: 50 });
  });

  it("address books: tag filter", () => {
    expect(parseTagFilter("prod, db,,")).toEqual(["prod", "db"]);
    expect(parseTagFilter(undefined)).toEqual([]);
  });
});
