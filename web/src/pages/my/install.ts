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

/**
 * Silent install for Windows, as RustDesk's own deployment script does, with
 * two differences that matter when a person pastes it into a console.
 *
 * It is one script block. Pasted line by line, each line runs on its own, so a
 * `throw` ends only the line it is on and everything after it still runs — a
 * refused rights check went on to install anyway and then quietly did nothing.
 * A block is a single statement and stops where it fails.
 *
 * And it asks for administrator rights instead of needing them. RustDesk
 * applies `--config` only when it is installed and the process is elevated
 * (core_main.rs); unelevated it prints one line and exits, which looks exactly
 * like success. The block writes itself to a file and runs that elevated, so
 * the settings are actually applied.
 */
export function windowsCommand(asset: ClientAsset, configString: string): string {
  const body = [
    "$ErrorActionPreference = 'Stop'",
    "[Net.ServicePointManager]::SecurityProtocol = 'Tls12'",
    `$url = '${asset.url}'`,
    ...(asset.sha256 ? [`$sha = '${asset.sha256.toUpperCase()}'`] : []),
    `$cfg = '${configString}'`,
    "# Any existing installation will do, whoever made it: the EXE installer, the",
    "# MSI (which is also what winget installs) or a per-user copy. They all land",
    "# in one of these folders or leave an uninstall entry behind, and --config",
    "# only needs the exe. Installing over an MSI is refused by RustDesk itself.",
    "function Find-RustDesk {",
    '  $try = @("$env:ProgramW6432\\RustDesk\\rustdesk.exe", "$env:ProgramFiles\\RustDesk\\rustdesk.exe", "${env:ProgramFiles(x86)}\\RustDesk\\rustdesk.exe", "$env:LOCALAPPDATA\\Programs\\RustDesk\\rustdesk.exe")',
    "  $roots = @('HKLM:\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\*','HKLM:\\SOFTWARE\\WOW6432Node\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\*','HKCU:\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\*')",
    "  foreach ($e in (Get-ItemProperty $roots -ErrorAction SilentlyContinue | Where-Object { $_.DisplayName -like 'RustDesk*' })) {",
    "    if ($e.InstallLocation) { $try += (Join-Path $e.InstallLocation 'rustdesk.exe') }",
    "    if ($e.DisplayIcon) { $try += ($e.DisplayIcon -replace '^\"|\"$|,\\d+$', '') }",
    "  }",
    "  $try | Where-Object { $_ -and (Test-Path -LiteralPath $_ -PathType Leaf) } | Select-Object -First 1",
    "}",
    "$exe = Find-RustDesk",
    "$install = -not $exe",
    "if ($install -and $args -notcontains 'install' -and [Environment]::UserInteractive) {",
    "  if ((Read-Host 'RustDesk is not installed. Download and install it now? [y/N]') -notmatch '^[yYдД]') { throw 'Cancelled' }",
    "}",
    "$me = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())",
    "if (-not $me.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {",
    "  $ps1 = Join-Path $env:TEMP 'rustdesk-install.ps1'",
    "  Set-Content -LiteralPath $ps1 -Encoding utf8 -Value $MyInvocation.MyCommand.ScriptBlock.ToString()",
    "  $a = @('-NoProfile','-ExecutionPolicy','Bypass','-File',$ps1)",
    "  if ($install) { $a += 'install' }",
    "  try {",
    "    $p = Start-Process -FilePath (Get-Process -Id $PID).Path -Verb RunAs -Wait -PassThru -ArgumentList $a",
    "  } catch {",
    "    Remove-Item -LiteralPath $ps1 -ErrorAction SilentlyContinue",
    "    throw 'Administrator rights are required and were not granted'",
    "  }",
    "  Remove-Item -LiteralPath $ps1 -ErrorAction SilentlyContinue",
    "  if ($p.ExitCode -ne 0) { throw \"The elevated run failed (exit code $($p.ExitCode)); run this from an elevated PowerShell to see why\" }",
    "  return",
    "}",
    "if ($install) {",
    "  $f = Join-Path $env:TEMP 'rustdesk-setup.exe'",
    "  Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $f",
    ...(asset.sha256 ? ["  if ((Get-FileHash $f -Algorithm SHA256).Hash -ne $sha) { throw 'Checksum mismatch' }"] : []),
    "  Start-Process $f -ArgumentList '--silent-install' -Wait",
    "  $exe = Find-RustDesk",
    "  if (-not $exe) { throw 'RustDesk did not install' }",
    "}",
    "& $exe --config $cfg",
  ];
  return ["& {", ...body.map((l) => `  ${l}`), "}"].join("\n");
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
