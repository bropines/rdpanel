// Helpers of the public share page (/s/<token>): token parsing, the download
// offered per platform and the page title. Platform detection and the QR
// payload come from the "Install client" page helpers.

import type { SharePublicInfo } from "@/api/share";
import { SHARE_BASE } from "@/api/share";
import { APP_STORE_URL, findAsset, type PlatformId } from "@/pages/my/install";

/** Share tokens: 128 random bits as unpadded base64url (22 characters). */
export const SHARE_TOKEN_RE = /^[A-Za-z0-9_-]{22}$/;

/** The token of a /s/<token> path; `?token=` works in development. */
export function tokenFromLocation(pathname: string, search = ""): string | null {
  const m = /^\/s\/([^/]+)\/?$/.exec(pathname);
  const tok = m?.[1] ?? new URLSearchParams(search).get("token") ?? "";
  return SHARE_TOKEN_RE.test(tok) ? tok : null;
}

export const GITHUB_RELEASES_URL = "https://github.com/rustdesk/rustdesk/releases/latest";

export interface ShareDownload {
  /** i18n key (or literal label when `literal`). */
  label: string;
  literal?: boolean;
  href: string;
  size?: number;
  /** The panel's own preconfigured installer (counted, same origin). */
  preconfigured?: boolean;
  external?: boolean;
}

/** Downloads offered for a platform, the main one first. */
export function platformDownloads(info: SharePublicInfo, platform: PlatformId, token: string): ShareDownload[] {
  const a = (id: string, label: string, literal = false): ShareDownload[] => {
    const asset = findAsset(info.assets, id);
    return asset ? [{ label, literal, href: asset.url, size: asset.size, external: true }] : [];
  };
  switch (platform) {
    case "windows":
      return info.windows_file_name
        ? info.windows.map((w) => ({
            label: w.arch === "x64" ? "share.public.windows.download" : "share.public.windows.arm",
            href: `${SHARE_BASE}/${token}/windows/${w.arch}`,
            size: w.size,
            preconfigured: true,
          }))
        : [];
    case "macos":
      return [...a("macos-arm64", "install.macos.arm"), ...a("macos-x64", "install.macos.intel")];
    case "linux":
      return [
        ...a("linux-deb-x64", ".deb", true),
        ...a("linux-rpm-x64", ".rpm", true),
        ...a("linux-appimage-x64", "AppImage", true),
        ...a("linux-deb-arm64", ".deb ARM64", true),
        ...a("linux-rpm-arm64", ".rpm ARM64", true),
        ...a("linux-appimage-arm64", "AppImage ARM64", true),
      ];
    case "android":
      return a("android-universal", "APK", true);
    case "ios":
      return [{ label: "App Store", literal: true, href: APP_STORE_URL, external: true }];
  }
}

/** The admin's title, or null for the default ("Connect to <host>"). */
export function customTitle(info: Pick<SharePublicInfo, "title">): string | null {
  const t = info.title.trim();
  return t ? t : null;
}
