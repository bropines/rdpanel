import { detectPlatform, linuxCommand, qrValue, windowsCommand } from "./install";

const asset = (name: string, sha256?: string) => ({ id: "x", name, size: 1, url: `https://github.com/rustdesk/rustdesk/releases/download/1.5.0/${name}`, sha256 });

describe("install client helpers", () => {
  it("detects the platform", () => {
    expect(detectPlatform("Mozilla/5.0 (Windows NT 10.0; Win64; x64)")).toBe("windows");
    expect(detectPlatform("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)")).toBe("macos");
    expect(detectPlatform("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", 5)).toBe("ios"); // iPadOS
    expect(detectPlatform("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)")).toBe("ios");
    expect(detectPlatform("Mozilla/5.0 (Linux; Android 15; Pixel 9)")).toBe("android");
    expect(detectPlatform("Mozilla/5.0 (X11; Linux x86_64)")).toBe("linux");
  });

  it("builds verified install commands", () => {
    const sha = "ab".repeat(32);
    const ps = windowsCommand(asset("rustdesk-1.5.0-x86_64.exe", sha), "CFG");
    expect(ps).toContain(`-ne '${sha.toUpperCase()}'`);
    expect(ps).toContain("--silent-install");
    expect(ps).toContain("--config 'CFG'");
    expect(windowsCommand(asset("rustdesk-1.5.0-x86_64.exe"), "CFG")).not.toContain("Get-FileHash");

    const deb = linuxCommand(asset("rustdesk-1.5.0-x86_64.deb", sha), "CFG");
    expect(deb).toContain(`echo '${sha}  /tmp/rustdesk.deb' | sha256sum -c -`);
    expect(deb).toContain("apt-get install -y /tmp/rustdesk.deb");
    expect(linuxCommand(asset("rustdesk-1.5.0-0.x86_64.rpm"), "CFG")).toContain("dnf install -y /tmp/rustdesk.rpm");
    expect(qrValue("CFG")).toBe("config=CFG");
  });
});
