import { describe, expect, it } from "vitest";

import { fromOverrideDraft, mbps, toOverrideDraft } from "@/api/relays";

describe("relay override draft", () => {
  it("round-trips overrides; empty fields inherit", () => {
    const d = toOverrideDraft({ total_bandwidth_mbps: 50, blocklist: ["1.2.3.4", "5.6.7.8"], blacklist: null });
    expect(d.nums.total_bandwidth_mbps).toBe("50");
    expect(d.nums.limit_speed_mbps).toBe("");
    expect(d.blocklist).toBe("1.2.3.4\n5.6.7.8");
    expect(d.blacklist).toBeNull();
    const back = fromOverrideDraft({ ...d, blocklist: "1.2.3.4, 9.9.9.9\n" });
    expect(back).toMatchObject({ total_bandwidth_mbps: 50, limit_speed_mbps: null, blocklist: ["1.2.3.4", "9.9.9.9"], blacklist: null });
  });

  it("rejects non-positive numbers and fractional seconds", () => {
    const d = toOverrideDraft({});
    expect(fromOverrideDraft({ ...d, nums: { ...d.nums, total_bandwidth_mbps: "-1" } })).toBeNull();
    expect(fromOverrideDraft({ ...d, nums: { ...d.nums, downgrade_start_check_s: "1.5" } })).toBeNull();
    expect(fromOverrideDraft({ ...d, nums: { ...d.nums, downgrade_threshold: "0.66" } })?.downgrade_threshold).toBe(0.66);
  });

  it("formats bit rates as Mb/s", () => {
    expect(mbps(121_000_000)).toBe("121");
    expect(mbps(1_500_000)).toBe("1.5");
  });
});
