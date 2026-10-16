import { describe, expect, it } from "vitest";

import { logStreamUrl } from "@/api/system";
import { formatLogTime, levelClass, mergeLogs, type LogEntry } from "@/pages/server/Logs";

const e = (seq: number, message = `m${seq}`): LogEntry => ({ seq, time: null, source: "hbbs", level: "info", message, raw: message });

describe("logs tab helpers", () => {
  it("appends newer entries and caps from the oldest side", () => {
    const out = mergeLogs([e(1), e(2)], [e(3), e(4)], 3);
    expect(out.map((x) => x.seq)).toEqual([2, 3, 4]);
  });

  it("dedupes and sorts overlapping or older batches", () => {
    const out = mergeLogs([e(3), e(5)], [e(1), e(3, "dup"), e(4)]);
    expect(out.map((x) => x.seq)).toEqual([1, 3, 4, 5]);
    expect(out[1]!.message).toBe("dup");
    expect(mergeLogs([e(1)], [])).toHaveLength(1);
  });

  it("colours levels and formats times", () => {
    expect(levelClass("error")).toContain("red");
    expect(levelClass("warn")).toContain("amber");
    expect(formatLogTime(null)).toBe("");
    expect(formatLogTime("garbage")).toBe("");
    expect(formatLogTime("2026-10-16T10:27:07.704Z")).toMatch(/^\d\d:27:07\.704$/);
  });

  it("builds the stream URL with filters", () => {
    expect(logStreamUrl({})).toBe("/api/v1/admin/server/logs/stream");
    expect(logStreamUrl({ source: "hbbs,hbbr", level: "warn", q: "a b", after: 7 })).toBe(
      "/api/v1/admin/server/logs/stream?source=hbbs%2Chbbr&level=warn&q=a+b&after=7",
    );
  });
});
