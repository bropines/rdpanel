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

### When the Windows installer is not offered

The server settings travel in the file name, so the preconfigured installer is only
offered when they fit into one of at most 192 characters, `.exe` included. The reason is
Windows' path limit. Classic Windows allows 260 characters for the whole path, folder and
file name together; a download folder such as `C:\Users\<name>\Downloads\` takes 40 to 60
of them, a browser adds ` (1)` to a name that already exists, and Explorer, some browsers
and messengers still enforce the limit. When a name is too long the end of it is cut off —
which is where the settings begin — and RustDesk then starts with no server configured and
says nothing about it.

The name carries the ID server, the API server and the key, but **not the relay**: hbbs
tells every client which relay to use when it answers a connection attempt, so a client
without one of its own behaves the same, and leaving it out buys about 48 characters of a
budget that decides whether the installer can be offered at all. The relay is still shown
on this page and on a share page, and is part of the config string for a manual import.

The key alone takes about 60 characters of the name, so an ordinary deployment comes to
roughly 160 to 190 and fits. Long host names push it past the limit: the panel then does
not offer the installer, and the download answers 409, rather than handing out a name that
may be damaged on the way. The API server is never dropped to make it fit — without one
the client guesses `http://<host>:21114`, the wrong scheme and port for an HTTPS
deployment.

If your settings do not fit, use the **Manual setup** card: the user installs the
official client from rustdesk.com or GitHub and imports the config string, or fills in
the ID, relay, API and key fields. A [share link](share-links.md) works the same way: its
page shows the same settings and config string for the people you send it to, and it
offers the preconfigured installer under the same rule.

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

To give the same downloads and settings to people without a panel account, create a
**share link** on this page (when `RDPANEL_SHARE_LINKS_ENABLED` is on and you may share):
see [share-links.md](share-links.md).

Custom-branded clients (own name, icon, locked settings) need a build of their own and
are not covered by this page.
