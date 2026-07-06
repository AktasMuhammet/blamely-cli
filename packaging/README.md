# Native installers (Windows / macOS / Linux)

Native, double-clickable / package-manager installers for all three platforms, so
users can avoid the `irm … | iex` / `curl … | bash` scripts. The
[`.github/workflows/release.yml`](../.github/workflows/release.yml) pipeline builds
them for **x64 + arm64** on every `v*.*.*` tag and publishes them alongside the
`.tar.gz` archives (which are still shipped).

| Platform | Artifact | Built by |
|----------|----------|----------|
| Windows | `blamely_<tag>_windows_<arch>_setup.exe` | Inno Setup — [`windows/blamely.iss`](windows/blamely.iss) |
| macOS | `blamely_<tag>_macos_<arch>.dmg` (contains a `.pkg`) | [`macos/build-macos.sh`](macos/build-macos.sh) |
| Linux | `blamely_<tag>_linux_<arch>.{deb,rpm}` | nfpm — [`linux/nfpm.yaml`](linux/nfpm.yaml) via [`linux/build-linux.sh`](linux/build-linux.sh) |
| Linux | `blamely_<tag>_linux_<arch>.AppImage` | [`linux/appimage/build-appimage.sh`](linux/appimage/build-appimage.sh) |

Every installer follows Blamely's **per-user** model: it places the `blamely`
binary on PATH, then runs `blamely install` (per-user daemon, git `core.hooksPath`,
AI-tool hooks) — showing its step-by-step report of detected IDEs / installed
plugins — and reverses it with `blamely uninstall` on removal. The system
installers (`.pkg`/`.deb`/`.rpm`) run as root but immediately drop to the desktop
user for that per-user wiring.

**Signing / trust:** everything is signed with a **self-signed** cert (Windows,
Authenticode) or **ad-hoc** (macOS), and checksums are GPG-signed — but nothing
chains to a public CA / Apple Developer ID, so Windows SmartScreen and macOS
Gatekeeper still warn. See [`TRUST.md`](TRUST.md) for the "Run anyway" steps, the
verification commands, and the security-team review guide. Real trust hooks
(Azure Trusted Signing, Apple `notarytool`) slot into the build scripts /
`release.yml` later with no change to the install flow.

## Windows installer details

The installer ([`windows/blamely.iss`](windows/blamely.iss)) is per-user (no UAC):
it installs `blamely.exe` to `%LOCALAPPDATA%\Programs\Blamely`, drops `sqlite3.exe`
into `~/.blamely/bin` (the IDE plugins' DB reader), adds the app dir to the user
PATH, then runs the bundled **code-signed `blamely.exe` directly** — no PowerShell,
cmd, or script, and no console window (the EDR/SmartScreen-friendly design).
`blamely install` writes its own report to `%USERPROFILE%\.blamely\last-install.log`,
which the wizard's final **"Installation details"** page displays (detected IDEs +
installed VS Code / JetBrains plugins). It runs `blamely uninstall` on removal.

## macOS / Linux — build locally

```bash
# macOS (.pkg + .dmg) — needs Xcode command line tools (pkgbuild/hdiutil/codesign):
packaging/macos/build-macos.sh \
  --version 1.7.0 --arch arm64 \
  --binary dist/blamely_v1.7.0_darwin_arm64/blamely --out out

# Linux .deb + .rpm — needs nfpm (https://nfpm.goreleaser.com):
packaging/linux/build-linux.sh \
  --version 1.7.0 --arch amd64 \
  --binary dist/blamely_v1.7.0_linux_amd64/blamely --out out

# Linux .AppImage — needs appimagetool on PATH:
packaging/linux/appimage/build-appimage.sh \
  --arch amd64 --tag v1.7.0 \
  --binary dist/blamely_v1.7.0_linux_amd64/blamely --out out
```

macOS ships `/usr/bin/sqlite3`, so the `.pkg` does not bundle sqlite3 (pass
`--sqlite <path>` only if you want to). The macOS binary is ad-hoc signed
(`codesign -s -`) — required on Apple Silicon.

## Signing

CI signs both `blamely.exe` (before packaging) and the produced `setup.exe` with
`signtool`, using two secrets in the private source repo:

| Secret | Value |
|--------|-------|
| `BLAMELY_SIGN_PFX_BASE64` | base64 of the `.pfx` code-signing cert |
| `BLAMELY_SIGN_PASS` | the `.pfx` password (leave empty for a passwordless cert) |

For the cert in `cert/blamely-codesign.pfx`, `cert/encoded.txt` is already its
base64 — paste that as `BLAMELY_SIGN_PFX_BASE64`. Signing is **opt-in**: with no
secret the release still publishes an unsigned installer (with a warning).

> ⚠️ **`cert/blamely-codesign.pfx` is a self-signed cert (CN=Blamely).** It makes
> the installer *signed* (publisher shows "Blamely", helps some AV heuristics), but
> Windows SmartScreen/Defender only *trust* signatures that chain to a public CA.
> So on machines that don't already trust this cert, users still see the
> "unknown publisher" warning. It clears warnings only where the cert is installed
> into **Trusted Root** — e.g. a corporate fleet pushing it via Group Policy.
>
> For public distribution without warnings, use a cert from a real CA (OV/EV) or
> **Azure Trusted Signing** (cheap, immediate SmartScreen trust), and drop its
> base64 into the same `BLAMELY_SIGN_PFX_BASE64` secret — no workflow change needed.

Never commit the `.pfx` / `.key` into a repo — keep it in the secrets only. (The
`cert/` folder lives outside every git repo in this workspace, so it isn't tracked.)

## Build locally (on Windows)

Requires [Inno Setup 6](https://jrsoftware.org/isdl.php) and the Windows SDK
`signtool`.

```bat
iscc /DAppVersion=1.7.0 /DArch=x64 ^
     /DSourceDir=dist\blamely_v1.7.0_windows_amd64 ^
     /DSqlite=dist\sqlite3.exe ^
     /Oout /Fblamely-setup-x64 ^
     packaging\windows\blamely.iss

signtool sign /fd sha256 /tr http://timestamp.digicert.com /td sha256 ^
  /f cert\blamely-codesign.pfx /p <PFX_PASS> out\blamely-setup-x64.exe
```

(`/DArch=arm64` for the ARM build. `/p` is omitted automatically in CI when the
cert is passwordless.)

## CI

[`.github/workflows/release.yml`](../.github/workflows/release.yml) builds the
installer in a Windows-only `installers` job (x64 + arm64) after the archive step,
signs it, and the `publish` job uploads `blamely_<tag>_windows_<arch>_setup.exe`
alongside the archives (and a tag-stripped copy under the rolling `latest`
release), including it in `SHA256SUMS`.
