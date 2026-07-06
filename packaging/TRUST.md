# Installing Blamely — trust, warnings, and verification

Blamely's installers are **code-signed with a self-signed certificate** (publisher
"Blamely") and its checksums are **GPG-signed**, but nothing chains to a public
Certificate Authority, Azure Trusted Signing, or an Apple Developer ID. That is a
deliberate, documented trade-off — it means:

- **Windows SmartScreen** and **macOS Gatekeeper** will show a warning on first
  run. This is *expected*, not a sign the download is broken.
- The warnings clear only where an admin/security team explicitly trusts Blamely
  (imports the cert, or whitelists the app). This page tells your users and your
  security team exactly how to do that, and how to verify every artifact.

Every release publishes, alongside the installers:

| Asset | Purpose |
|-------|---------|
| `SHA256SUMS` | SHA-256 of every artifact |
| `SHA256SUMS.asc` | GPG detached signature of `SHA256SUMS` |
| `blamely-gpg-public.asc` | public GPG key that made `SHA256SUMS.asc` |
| `blamely-codesign.crt` | public half of the code-signing certificate (no private key) |

---

## Windows — `blamely_<ver>_windows_<arch>_setup.exe`

The installer is **per-user** (no admin/UAC). It:

- installs `blamely.exe` to `%LOCALAPPDATA%\Programs\Blamely`
- drops `sqlite3.exe` into `%USERPROFILE%\.blamely\bin` (the IDE plugins' DB reader)
- adds the install dir to your **user** `PATH`
- runs the bundled, code-signed **`blamely.exe` directly** (no PowerShell, no cmd,
  no script — the EDR/SmartScreen-friendly way) to set up the per-user daemon, git
  `core.hooksPath`, and AI-tool hooks. Its step-by-step report of detected IDEs and
  installed plugins appears on the final **"Installation details"** page and is
  saved to `%USERPROFILE%\.blamely\last-install.log`
- runs `blamely uninstall` on removal

### Getting past SmartScreen
Because the signature isn't CA-trusted, you'll see **"Windows protected your PC"**:

1. Click **More info**.
2. Click **Run anyway**.

To remove the warning fleet-wide, an admin can import `blamely-codesign.crt` into
**Trusted Root Certification Authorities** *and* **Trusted Publishers** (e.g. via
Group Policy). Only do this if your security team has reviewed and approved Blamely.

---

## macOS — `blamely_<ver>_macos_<arch>.dmg` (contains a `.pkg`)

The `.pkg` installs `blamely` to `/usr/local/bin` and its postinstall runs
`blamely install` **as the logged-in user** (Blamely is per-user: a launchd
LaunchAgent + git/AI hooks under `~/.blamely`). The binary is **ad-hoc signed**;
the `.pkg`/`.dmg` are **not notarized**.

### Getting past Gatekeeper
1. Open the `.dmg`, then **right-click** the `.pkg` → **Open** → **Open**.
2. If macOS still blocks it: **System Settings → Privacy & Security**, scroll to
   the blocked-item notice, click **Open Anyway**.
3. Command-line alternative (removes the download quarantine flag):
   ```sh
   xattr -dr com.apple.quarantine ~/Downloads/blamely_*_macos_*.dmg
   ```

After it finishes, a **summary dialog** appears — click **Show Details** to open
the full step-by-step report (detected IDEs, installed plugins, hooks). That report
is also saved to `~/.blamely/last-install.log`, mirrored to `/var/log/install.log`,
and viewable in the graphical Installer's **Show Log** window. For a fully visible
run, install from the terminal:
```sh
sudo installer -verbose -pkg /Volumes/Blamely\ CLI/blamely_*.pkg -target /
```

---

## Linux — `.deb`, `.rpm`, `.AppImage`

No OS gatekeeper. Verify authenticity with GPG before installing (below).

- **Debian/Ubuntu** — `sudo apt install ./blamely_<ver>_linux_<arch>.deb`
- **Fedora/RHEL** — `sudo dnf install ./blamely_<ver>_linux_<arch>.rpm`

  The postinstall runs `blamely install` **as your desktop user** and prints its
  step-by-step report right in the terminal. Removal (`apt remove` / `dnf remove`)
  runs `blamely uninstall`; an upgrade does not.

- **AppImage** — a portable, distro-independent runner (no package manager):
  ```sh
  chmod +x blamely_<ver>_linux_<arch>.AppImage
  ./blamely_<ver>_linux_<arch>.AppImage --version
  ```
  It does **not** auto-wire the daemon/hooks — run `./blamely_*.AppImage install`
  once if you want them.

---

## Verify any download (all platforms)

```sh
# 1. Import our public key and verify the checksum manifest is authentically ours.
gpg --import blamely-gpg-public.asc
gpg --verify SHA256SUMS.asc SHA256SUMS

# 2. Verify the file you downloaded matches the (now-trusted) manifest.
sha256sum -c SHA256SUMS 2>/dev/null | grep -E 'blamely_.*OK'   # Linux/macOS
#   (macOS: `shasum -a 256 -c SHA256SUMS`)
```

Inspect the Windows/macOS code-signing certificate itself:
```sh
openssl x509 -in blamely-codesign.crt -noout -subject -issuer -fingerprint -sha256
```

---

## For your security team

- **Footprint is per-user, never a system daemon as root.** Everything Blamely
  adds lives under the user's home (`~/.blamely`, the user PATH, per-user
  launchd/systemd agent, git `core.hooksPath`, and AI-tool hook config). The
  system installers place only the `blamely` binary (and on Windows `sqlite3`) on
  disk; the root-run postinstall immediately drops to the target user to do the
  per-user wiring.
- **Everything is reversible**: `blamely uninstall` (run automatically on package
  removal) tears the per-user setup back down.
- **No remote-pipe / PowerShell.** The Windows installer runs only its own
  code-signed `blamely.exe` directly — never `powershell -ExecutionPolicy Bypass`,
  `cmd /c`, or an `irm … | iex` remote script. The install report is captured by
  blamely itself to `~/.blamely/last-install.log`, not by shell redirection.
- **The installer scripts are auditable and short** — read them before approving:
  - Windows: [`packaging/windows/blamely.iss`](windows/blamely.iss)
  - macOS: [`packaging/macos/scripts/postinstall`](macos/scripts/postinstall)
  - Linux: [`packaging/linux/scripts/postinstall.sh`](linux/scripts/postinstall.sh)
    and [`preremove.sh`](linux/scripts/preremove.sh)
- **Verify authenticity** with the GPG steps above; **whitelist** by importing
  `blamely-codesign.crt` where your policy allows.

> Once a public-CA / Azure Trusted Signing certificate (Windows) or an Apple
> Developer ID + notarization (macOS) is available, these warnings disappear with
> no change to the install flow — only the signing step in the release pipeline.
