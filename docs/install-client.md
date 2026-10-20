# Install client

**My → Install client** (`/my/install`) gives every signed-in user the official
RustDesk client already set up for this server. The page picks the visitor's
platform and offers:

| platform | what users get |
|---|---|
| Windows | **Download for Windows**: the official installer, served by the panel as `rustdesk-licensed-<config string>.exe`. RustDesk reads its server settings from that file name, applies them on start and keeps them when it installs, so users just run it. For administrators: a PowerShell silent install (GPO, Intune, RMM) that downloads from GitHub, checks the SHA-256, installs and applies `--config`. |
| macOS | DMG links (Apple silicon, Intel), import steps and the `--config` command. |
| Linux | one command per family (deb: apt, rpm: dnf) that downloads, verifies, installs and configures; AppImage link. |
| Android, iPhone/iPad | APK / App Store link and a QR code (`config=<config string>`) for the app's scanner (Settings → QR icon). |

A **Manual setup** card shows the config string and the ID, relay, API and key fields for
any other case ("Import server config" in the client's ID/Relay Server settings).

## How downloads work

- The panel looks up the latest RustDesk release (or `RDPANEL_CLIENT_DOWNLOADS_VERSION`)
  through the GitHub API, at most every 6 h; failures keep the last good answer.
- Only assets under `https://github.com/rustdesk/rustdesk/releases/download/` are listed.
- The Windows installer is downloaded once per version into `<data_dir>/clients`,
  checked against GitHub's size and SHA-256, and served with Range support; older
  versions are removed. Other platforms link to GitHub directly.
- The page shows each file's SHA-256 with a link to the GitHub release and the command to
  check a downloaded file. The Windows installer from the panel is byte for byte the
  official one (only its name differs), so its hash matches GitHub's; the PowerShell and
  Linux commands check it themselves before installing.
- The settings are the ones on **Server → Client configuration**: `RDPANEL_PUBLIC_URL`'s
  host as ID server, `RDPANEL_HBBS_RELAY_PUBLIC_HOST` (or the same host) as relay,
  the public URL as API server, and hbbs' public key.

Set `RDPANEL_CLIENT_DOWNLOADS_ENABLED=false` on hosts without internet access: the page
then offers only the settings and the QR code, and the panel makes no request to GitHub.

Custom-branded clients (own name, icon, locked settings) need a build of their own and
are not covered by this page.
