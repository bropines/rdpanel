// Helpers of the "Install client" page: platform detection and the install
// commands (official RustDesk packages from GitHub, checked by SHA-256).

import type { ClientAsset } from "@/api/system";

export type { ClientAsset };
export type PlatformId = "windows" | "macos" | "linux" | "android" | "ios";

export const PLATFORMS: PlatformId[] = ["windows", "macos", "linux", "android", "ios"];

export const APP_STORE_URL = "https://apps.apple.com/app/rustdesk-remote-desktop/id1581225015";

/** Best guess of the visitor's platform from the user agent. */
export function detectPlatform(ua: string, maxTouchPoints = 0): PlatformId {
  if (/android/i.test(ua)) return "android";
  if (/iphone|ipad|ipod/i.test(ua) || (/macintosh/i.test(ua) && maxTouchPoints > 1)) return "ios";
  if (/windows/i.test(ua)) return "windows";
  if (/mac os x|macintosh/i.test(ua)) return "macos";
  if (/linux|x11|cros/i.test(ua)) return "linux";
  return "windows";
}

export function findAsset(assets: ClientAsset[], id: string): ClientAsset | undefined {
  return assets.find((a) => a.id === id);
}

/** Silent install for Windows (PowerShell as administrator), as RustDesk's own deployment script does. */
export function windowsCommand(asset: ClientAsset, configString: string): string {
  const lines = [
    "$ErrorActionPreference = 'Stop'",
    "[Net.ServicePointManager]::SecurityProtocol = 'Tls12'",
    '$f = "$env:TEMP\\rustdesk-setup.exe"',
    `Invoke-WebRequest -UseBasicParsing -Uri '${asset.url}' -OutFile $f`,
  ];
  if (asset.sha256) {
    lines.push(`if ((Get-FileHash $f -Algorithm SHA256).Hash -ne '${asset.sha256.toUpperCase()}') { throw 'Checksum mismatch' }`);
  }
  lines.push(
    "Start-Process $f -ArgumentList '--silent-install'",
    "Start-Sleep -Seconds 20",
    `& "$env:ProgramFiles\\RustDesk\\rustdesk.exe" --config '${configString}'`,
  );
  return lines.join("\n");
}

/** Applies the settings to an installed macOS client (Terminal). */
export function macosCommand(configString: string): string {
  return `sudo /Applications/RustDesk.app/Contents/MacOS/RustDesk --config '${configString}'`;
}

/** Download, verify, install and configure on Linux (deb: apt, rpm: dnf). */
export function linuxCommand(asset: ClientAsset, configString: string): string {
  const deb = asset.name.endsWith(".deb");
  const file = deb ? "/tmp/rustdesk.deb" : "/tmp/rustdesk.rpm";
  const steps = [`curl -fL -o ${file} '${asset.url}'`];
  if (asset.sha256) steps.push(`echo '${asset.sha256}  ${file}' | sha256sum -c -`);
  steps.push(
    deb ? `sudo apt-get install -y ${file}` : `sudo dnf install -y ${file}`,
    `sudo rustdesk --config '${configString}'`,
    "sudo systemctl restart rustdesk",
  );
  return steps.join(" && \\\n  ");
}

/** What the mobile app's QR scanner expects. */
export function qrValue(configString: string): string {
  return `config=${configString}`;
}

export function formatSize(bytes: number): string {
  return `${(bytes / (1024 * 1024)).toFixed(bytes < 10 * 1024 * 1024 ? 1 : 0)} MB`;
}
