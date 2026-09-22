#requires -Version 5.1
<#
.SYNOPSIS
    Blamely CLI installer for Windows.

.DESCRIPTION
    Intended for:
      powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://blamely.ai/blamely-windows-install.ps1 | iex"

    Downloads the latest release zip, installs to %USERPROFILE%\.blamely\bin,
    adds that directory to the user PATH, and runs `blamely install`.

    Checks for git (required) and offers to install via winget when missing.
    Java is not required for the CLI.
#>

$ErrorActionPreference = 'Stop'

# Decode the CLI's output as UTF-8. `blamely install` prints UTF-8 glyphs (✓ • ✗)
# and "·" separators; this installer captures that output (`& $StableBin install
# 2>&1 | Write-Host`), so PowerShell — not the console — decodes the child
# process's bytes, using [Console]::OutputEncoding. On non-UTF-8 systems (e.g.
# Turkish Windows / CP857) that mangles them into mojibake like "Ô£ô"/"ÔÇó"/"┬À".
# Pinning the console code page and PowerShell's encodings to UTF-8 fixes it.
try {
    & chcp 65001 > $null 2>&1
    [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new()
    [Console]::InputEncoding  = [System.Text.UTF8Encoding]::new()
    $OutputEncoding           = [System.Text.UTF8Encoding]::new()
} catch {
    # Best-effort: a redirected/headless host may not allow setting these.
}

# Older Windows PowerShell builds default to TLS 1.0/1.1, which GitHub and
# blamely.ai reject — that surfaces as a cryptic "underlying connection was
# closed: an unexpected error occurred on a send" from Invoke-WebRequest.
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

# BLAMELY_CHANNEL selects the GitHub release to install from. Default `latest`
# (stable). Set BLAMELY_CHANNEL=beta to install the rolling pre-release build:
#   $env:BLAMELY_CHANNEL='beta'; irm https://blamely.ai/blamely-windows-install.ps1 | iex
# On the beta channel the CLI is installed with --skip-plugins and the beta
# .vsix / IntelliJ .zip are sideloaded directly from the beta release (the public
# marketplaces only ever carry stable builds — see Invoke-BlamelyInstall).
$Channel     = if ($env:BLAMELY_CHANNEL) { $env:BLAMELY_CHANNEL } else { 'latest' }
$ReleaseBase = "https://github.com/blamely-ai/blamely/releases/download/$Channel"
$StableDir   = Join-Path $env:USERPROFILE '.blamely\bin'
$StableBin   = Join-Path $StableDir 'blamely.exe'
$AutoYes     = [bool]$env:BLAMELY_INSTALL_YES

# BLAMELY_SKIP_DEFENDER_EXCLUSION=1 skips the (admin-only) Defender exclusion
# entirely — no elevation, so no UAC / Local-Admin credential prompt appears.
# For locked-down or domain-joined machines where the user has no Local Admin
# and can't satisfy that prompt. The install continues per-user; if Defender
# later quarantines the unsigned binary, the AV-retry path prints the manual
# "add an exclusion yourself" steps.
$SkipDefenderExclusion = [bool]$env:BLAMELY_SKIP_DEFENDER_EXCLUSION

function Info($msg)  { Write-Host ("  -> {0}" -f $msg) }
function Ok($msg)    { Write-Host ("  [+] {0}" -f $msg) -ForegroundColor Green }
function Warn($msg)  { Write-Host ("  [!] {0}" -f $msg) -ForegroundColor Yellow }
function Die($msg)   { Write-Host ("error: {0}" -f $msg) -ForegroundColor Red; exit 1 }

# Blamely's daemon is a PER-USER agent (an ONLOGON scheduled task / Startup
# entry). It installs under the current user's profile, so a normal (non-admin)
# PowerShell is best. Elevation keeps the same user, so we continue — but warn,
# because if you elevated into a DIFFERENT admin account the daemon would be set
# up for that account and your editor (running as you) wouldn't see it.
$IsAdmin = ([Security.Principal.WindowsPrincipal] `
    [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator)
if ($IsAdmin) {
    Warn "Running elevated. Blamely installs per-user (current account: $env:USERNAME). For best results run this in a normal, non-Administrator PowerShell."
}

function Test-Interactive {
    return [Environment]::UserInteractive -and
        ($Host.Name -ne 'ServerRemoteHost') -and
        (-not $AutoYes)
}

function Ask-Yes($prompt) {
    if ($AutoYes) { return $true }
    if (-not (Test-Interactive)) { return $false }
    $ans = Read-Host "$prompt [y/N]"
    return $ans -match '^(y|yes)$'
}

function Get-WindowsArch {
    $arch = $env:PROCESSOR_ARCHITECTURE
    if ($arch -eq 'ARM64') { return 'arm64' }
    if ($arch -eq 'AMD64') { return 'amd64' }
    # WoW64 / older
    if ($env:PROCESSOR_ARCHITEW6432 -eq 'ARM64') { return 'arm64' }
    if ($env:PROCESSOR_ARCHITEW6432 -eq 'AMD64') { return 'amd64' }
    Die "unsupported Windows architecture: $arch"
}

function Ensure-Git {
    if (Get-Command git -ErrorAction SilentlyContinue) {
        $ver = (& git --version 2>$null)
        Ok "git ($ver)"
        return
    }
    Warn 'git is required for Blamely attribution (commits, hooks, git notes).'
    if (-not (Ask-Yes 'Install Git now via winget?')) {
        Die 'Install Git from https://git-scm.com/download/win and re-run.'
    }
    if (-not (Get-Command winget -ErrorAction SilentlyContinue)) {
        Die 'winget not found. Install Git manually: https://git-scm.com/download/win'
    }
    Info 'Installing Git.Git via winget (may take a minute)...'
    $wingetArgs = @('install', '--id', 'Git.Git', '-e', '--source', 'winget',
        '--accept-package-agreements', '--accept-source-agreements')
    # Tee winget's output to the console (so progress is still visible) while
    # also capturing it, so a corrupted-source failure can be detected and
    # repaired automatically with a single retry.
    & winget @wingetArgs 2>&1 | Tee-Object -Variable wingetLines | Out-Host
    $wingetText = $wingetLines | Out-String
    if ($LASTEXITCODE -ne 0 -and $wingetText -match 'opening sources|source reset') {
        Warn 'winget package sources look corrupted on this machine.'
        Info 'Running: winget source reset --force'
        & winget source reset --force | Out-Null
        Info 'Retrying git install via winget...'
        & winget @wingetArgs
    }
    if ($LASTEXITCODE -ne 0) {
        Die "winget install Git failed (exit $LASTEXITCODE). If you saw a 'failed when opening sources' error, run 'winget source reset --force' and re-run this installer, or install Git manually: https://git-scm.com/download/win"
    }
    Refresh-SessionPath
    if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
        Die 'Git was installed but is not on PATH yet. Open a new PowerShell and re-run.'
    }
    Ok 'git installed'
}

function Add-DefenderExclusion {
    # Best-effort: exclude the install folder from Defender scanning so the
    # freshly-downloaded (unsigned) binary isn't quarantined on copy/launch.
    # Runs up front (before the download) so the binary lands in an
    # already-excluded folder. Needs admin (Add-MpPreference); if elevation is
    # declined or unavailable, it logs a note and the install continues.
    if ($SkipDefenderExclusion) {
        Info 'Skipping the Defender exclusion (BLAMELY_SKIP_DEFENDER_EXCLUSION set) — no admin prompt.'
        return
    }
    if (-not (Get-Command Add-MpPreference -ErrorAction SilentlyContinue)) {
        return
    }
    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    $isAdmin = $principal.IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator)
    try {
        if ($isAdmin) {
            Add-MpPreference -ExclusionPath $StableDir -ErrorAction Stop
        } else {
            Info 'Adding a Defender exclusion needs admin rights — you may see a UAC prompt...'
            $cmd = "Add-MpPreference -ExclusionPath '$StableDir'"
            $p = Start-Process -FilePath 'powershell' -Verb RunAs -PassThru -Wait `
                -ArgumentList @('-NoProfile', '-WindowStyle', 'Hidden', '-Command', $cmd)
            if ($p.ExitCode -ne 0) { throw "elevated process exited with code $($p.ExitCode)" }
        }
        Ok "Added a Defender exclusion for $StableDir"
    } catch {
        Warn 'Could not add a Defender exclusion automatically — continuing without one.'
        Info '  To add one yourself: Windows Security > Virus & threat protection >'
        Info "  Manage settings > Exclusions > Add an exclusion > Folder > $StableDir"
    }
}

function Split-WindowsPath($pathValue) {
    if (-not $pathValue) { return @() }
    return $pathValue -split ';' | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne '' }
}

function Test-WindowsPathContains($pathValue, $dir) {
    $want = (Resolve-Path -LiteralPath $dir -ErrorAction SilentlyContinue).Path
    if (-not $want) { $want = $dir }
    foreach ($part in (Split-WindowsPath $pathValue)) {
        $resolved = (Resolve-Path -LiteralPath $part -ErrorAction SilentlyContinue).Path
        if (-not $resolved) { $resolved = $part }
        if ($resolved -ieq $want) { return $true }
    }
    return $false
}

function Refresh-SessionPath {
  # Windows PATH uses ';' — reload Machine + User so this session matches registry.
    $machine = [Environment]::GetEnvironmentVariable('Path', 'Machine')
    $user = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($machine -and $user) {
        $env:Path = "$machine;$user"
    } elseif ($user) {
        $env:Path = $user
    } elseif ($machine) {
        $env:Path = $machine
    }
}

function Add-ToUserPath {
    $entry = $StableDir
    $current = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (Test-WindowsPathContains $current $entry) {
        Info "PATH already contains $entry"
        Refresh-SessionPath
        return
    }
    $parts = Split-WindowsPath $current
    $newPath = ($parts + $entry) -join ';'
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    Ok "Added $entry to user PATH"
    Refresh-SessionPath
    if (-not (Test-WindowsPathContains $env:Path $entry)) {
        # The registry write above is the durable change — new shells will
        # pick it up. Some machines (profile managers, EDR hooks on HKCU)
        # don't reflect a User PATH write back into this same process, so
        # treat that as cosmetic rather than failing the whole install.
        Warn "$entry was added to your PATH but isn't visible in this session yet."
        Info 'Open a new terminal after this finishes to use the `blamely` command directly.'
    }
}

function Invoke-Download($Url, $OutFile) {
    try {
        Invoke-WebRequest -Uri $Url -OutFile $OutFile -UseBasicParsing
        return
    } catch {
        # Invoke-WebRequest rides on .NET's HttpWebRequest. On some machines
        # (outdated cipher-suite support, antivirus/proxy HTTPS inspection)
        # that stack can't complete the TLS handshake even with TLS 1.2
        # forced, surfacing as "underlying connection was closed: an
        # unexpected error occurred on a send". curl.exe (bundled since
        # Windows 10 1803 / Server 2019) goes through WinHTTP/Schannel
        # instead — a fully independent code path — and often succeeds
        # where Invoke-WebRequest can't.
        $curl = Get-Command curl.exe -ErrorAction SilentlyContinue
        if (-not $curl) { throw }
        Warn 'Invoke-WebRequest failed (often a TLS/cipher-suite or proxy issue with the .NET HTTP stack).'
        Info 'Falling back to curl.exe...'
        & $curl.Source -fsSL -o $OutFile $Url
        if ($LASTEXITCODE -ne 0 -or -not (Test-Path $OutFile)) { throw }
    }
}

function Stop-BlamelyDaemon {
    # A previous install's daemon runs `blamely.exe daemon` from $StableBin and
    # holds the file open — Windows then refuses to overwrite the running image.
    # End the scheduled task (if registered) so it can't relaunch the daemon
    # mid-install, then stop any running blamely process. Best-effort throughout;
    # the daemon is restarted later by Install-StartupDaemon / `blamely install`.
    try { & schtasks /End /TN 'Blamely Daemon' 2>$null | Out-Null } catch {}
    Get-Process -Name 'blamely' -ErrorAction SilentlyContinue |
        Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 400  # give the OS a moment to release the handle
}

function Clear-StableBinaryLock {
    # Remove binaries renamed aside by earlier installs (their daemon has since
    # exited, so they're deletable now).
    Get-ChildItem -Path $StableDir -Filter 'blamely.exe.old-*' -File -ErrorAction SilentlyContinue |
        ForEach-Object { Remove-Item -LiteralPath $_.FullName -Force -ErrorAction SilentlyContinue }
    if (-not (Test-Path $StableBin)) { return }
    try {
        # Opening with no sharing succeeds only if nothing holds the file open.
        $fs = [System.IO.File]::Open($StableBin, 'Open', 'ReadWrite', 'None')
        $fs.Close()
    } catch {
        # Still locked (process not fully exited, or relaunched). Windows allows
        # renaming a running image even though it can't be overwritten — move it
        # aside so Copy-Item can write the new binary in its place.
        $old = "$StableBin.old-$([guid]::NewGuid().ToString('n'))"
        Move-Item -LiteralPath $StableBin -Destination $old -Force
    }
}

function Install-BinaryWithAvRetry {
    # Copy the binary into place, handling Windows Defender's false-positive block
    # (HRESULT 0x800704EC, "the file contains a virus or potentially unwanted
    # software") that unsigned, obfuscated Go binaries trip. On a block we add a
    # Defender exclusion for $StableDir (prompts for admin) and retry once; if it
    # still fails, stop with clear, actionable guidance instead of a raw IOException.
    param([Parameter(Mandatory)][string]$Source)
    try {
        Copy-Item -Path $Source -Destination $StableBin -Force -ErrorAction Stop
        return
    } catch {
        if (-not (Test-AvBlocked $_)) { throw }
        Warn 'Windows Defender blocked the Blamely binary as "virus or unwanted software".'
        Info 'This is a known false positive on the unsigned, obfuscated release binary.'
        Info 'Adding a Defender exclusion (you may see a UAC prompt) and retrying...'
        Add-DefenderExclusion
        Start-Sleep -Milliseconds 600
        try {
            Copy-Item -Path $Source -Destination $StableBin -Force -ErrorAction Stop
            Ok 'Exclusion applied — binary installed on retry.'
            return
        } catch {
            Die @"
Windows Defender is quarantining the Blamely binary (a false positive on the
unsigned build) and an exclusion couldn't be applied automatically.

To finish installing:
  1. Open  Windows Security > Virus & threat protection > Manage settings > Exclusions
  2. Add an exclusion > Folder >  $StableDir
  3. Re-run this installer:
       irm https://blamely.ai/blamely-windows-install.ps1 | iex
"@
        }
    }
}

function Download-And-Install {
    $arch = Get-WindowsArch
    $asset = "blamely_windows_${arch}.zip"
    $url = "$ReleaseBase/$asset"
    # Stage the download INSIDE $StableDir — the folder already excluded from
    # Defender (Add-DefenderExclusion runs before this) — instead of %TEMP%.
    # Defender scans files as they're written; an unsigned, obfuscated binary
    # landing in unexcluded %TEMP% gets quarantined there before we can copy it,
    # defeating the exclusion. A Defender folder exclusion covers subfolders, so a
    # staging subdir of $StableDir is never scanned and the binary never touches
    # an unexcluded location.
    if (-not (Test-Path $StableDir)) {
        New-Item -ItemType Directory -Path $StableDir -Force | Out-Null
    }
    $tmpdir = Join-Path $StableDir (".install-" + [guid]::NewGuid().ToString('n'))
    New-Item -ItemType Directory -Path $tmpdir -Force | Out-Null
    try {
        Info "Downloading $url ..."
        $zipPath = Join-Path $tmpdir $asset
        Invoke-Download -Url $url -OutFile $zipPath
        Expand-Archive -Path $zipPath -DestinationPath $tmpdir -Force
        $bin = Get-ChildItem -Path $tmpdir -Recurse -Filter 'blamely.exe' -File -ErrorAction SilentlyContinue |
            Select-Object -First 1
        if (-not $bin) {
            Die "could not find blamely.exe inside $asset"
        }
        # On upgrade the existing daemon locks $StableBin; stop it, then free the
        # path (rename the old image aside if it's still held) so the copy lands.
        Stop-BlamelyDaemon
        Clear-StableBinaryLock
        Install-BinaryWithAvRetry -Source $bin.FullName
        # Strip the Mark-of-the-Web (Zone.Identifier) the download adds, so the
        # unsigned binary launches without a "this file came from the internet"
        # / SmartScreen prompt.
        Unblock-File -LiteralPath $StableBin -ErrorAction SilentlyContinue
        Ok "Binary installed: $StableBin"

        # If the release bundles sqlite3.exe, install it next to blamely.exe. The
        # VS Code / JetBrains plugins read the attribution DB through the sqlite3
        # CLI, and Windows ships none — co-locating it in the on-PATH,
        # Defender-excluded bin dir is exactly where the plugins look first.
        $sqliteSrc = Get-ChildItem -Path $tmpdir -Recurse -Filter 'sqlite3.exe' -File -ErrorAction SilentlyContinue |
            Select-Object -First 1
        if ($sqliteSrc) {
            $sqliteDst = Join-Path $StableDir 'sqlite3.exe'
            Copy-Item -Path $sqliteSrc.FullName -Destination $sqliteDst -Force
            Unblock-File -LiteralPath $sqliteDst -ErrorAction SilentlyContinue
            Ok "sqlite3 installed: $sqliteDst"
        }
    } finally {
        Remove-Item -LiteralPath $tmpdir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Ensure-Sqlite3 {
    # The VS Code & JetBrains plugins read the attribution DB via the sqlite3 CLI;
    # Windows has none. Without it the gutter and status bar stay empty (the CLI's
    # own `blamely report` still works — it uses embedded SQLite, no external
    # binary). If the release didn't bundle one (handled in Download-And-Install)
    # and none is on PATH, install via winget (best-effort), else warn with a hint.
    $binSqlite = Join-Path $StableDir 'sqlite3.exe'
    if (Test-Path $binSqlite) { Ok "sqlite3 ($binSqlite)"; return }

    # Prefer dropping sqlite3.exe INTO the blamely bin dir: the editor plugins look
    # there FIRST, and it's independent of the extension host's PATH. A GUI-launched
    # VS Code can't see PATH-installed tools (winget shims), which surfaces as
    # "spawn sqlite3 ENOENT" — so a PATH install alone is not enough. Download the
    # sqlite3.exe shipped alongside the release into the bin dir.
    try {
        Info 'Installing sqlite3.exe into the blamely bin dir (for the editor plugins)...'
        Invoke-Download -Url "$ReleaseBase/sqlite3.exe" -OutFile $binSqlite
        Unblock-File -LiteralPath $binSqlite -ErrorAction SilentlyContinue
        if (Test-Path $binSqlite) { Ok "sqlite3 installed: $binSqlite"; return }
    } catch {
        Warn 'release did not ship sqlite3.exe — falling back to a PATH install.'
    }

    if (Get-Command sqlite3 -ErrorAction SilentlyContinue) { Ok 'sqlite3 (on PATH)'; return }

    Info 'sqlite3 not found — the editor plugins read the attribution DB through it.'
    if (Get-Command winget -ErrorAction SilentlyContinue) {
        Info 'Installing SQLite.SQLite via winget (for the editor plugins)...'
        & winget install -e --id SQLite.SQLite --source winget `
            --accept-package-agreements --accept-source-agreements 2>&1 | Out-Host
        Refresh-SessionPath
    }
    if (-not (Test-Path $binSqlite) -and -not (Get-Command sqlite3 -ErrorAction SilentlyContinue)) {
        Warn 'sqlite3 is unavailable — the VS Code / JetBrains gutter and status bar will stay empty until it is installed.'
        Info '  Fix: install sqlite3 and put it on PATH, or drop sqlite3.exe into:'
        Info "       $StableDir"
    }
}

function Test-AvBlocked($ErrorRecord) {
    # Windows refuses to launch a binary that Defender (or another AV) has
    # quarantined as malware/PUA — surfaces as ApplicationFailedException /
    # NativeCommandFailed with a "virus or unwanted software" message
    # (HRESULT 0x800704EC). Freshly-built, unsigned Go binaries trigger this
    # as a false positive fairly often.
    $text = $ErrorRecord | Out-String
    return $text -match 'virüslü|unwanted software|0x800704EC|ERROR_VIRUS_INFECTED'
}

function Test-DaemonAccessDenied($text) {
    return $text -match '(?i)erişim engellendi|access is denied|access denied|schtasks /Create'
}

function Install-StartupDaemon {
    # Per-user Startup folder — works without Administrator (unlike schtasks ONLOGON).
    $startup = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\Startup'
    if (-not (Test-Path -LiteralPath $startup)) {
        New-Item -ItemType Directory -Path $startup -Force | Out-Null
    }
    $vbs = Join-Path $startup 'blamely-daemon.vbs'
    @"
Set WshShell = CreateObject("WScript.Shell")
WshShell.Run """$StableBin"" daemon", 0, False
"@ | Set-Content -LiteralPath $vbs -Encoding ASCII
    Start-Process -FilePath $StableBin -ArgumentList 'daemon' -WindowStyle Hidden
    Ok "Daemon registered via Startup folder (no admin required): $vbs"
}

# Resolve-EditorCli locates a VS Code-family editor's command-line launcher.
# It tries $PATH first (the launcher is only there if the user opted into "Add to
# PATH" at install time — often skipped on Windows), then falls back to the
# editor's default install locations. Mirrors the app-bundle fallback the Go
# installer uses on macOS, so a beta sideload still works when `code` isn't on
# PATH. Returns the launcher path, or $null if the editor isn't installed.
function Resolve-EditorCli {
    param([string]$Command, [string[]]$Candidates)
    $cmd = Get-Command $Command -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    foreach ($p in $Candidates) {
        if ($p -and (Test-Path -LiteralPath $p)) { return $p }
    }
    return $null
}

function Sideload-BetaPlugins {
    # The marketplaces only carry stable builds, so on the beta channel we download
    # the beta .vsix / IntelliJ .zip from the beta release and install them directly.
    $tmp = Join-Path $env:TEMP ("blamely-beta-" + [guid]::NewGuid().ToString('n'))
    New-Item -ItemType Directory -Path $tmp -Force | Out-Null
    try {
        Info 'Downloading beta VS Code extension...'
        $vsix = Join-Path $tmp 'blamely.vsix'
        # Download and install are separated so a failure in one isn't reported as
        # the other (the old single catch blamed every editor-install error on the
        # download), and each surfaces the real exception text.
        $vsixOk = $false
        try {
            Invoke-Download -Url "$ReleaseBase/blamely.vsix" -OutFile $vsix
            if ((Test-Path $vsix) -and ((Get-Item $vsix).Length -gt 0)) {
                $vsixOk = $true
            } else {
                Warn "downloaded beta .vsix is empty — skipping VS Code-family install"
            }
        } catch {
            Warn "could not download beta .vsix from $ReleaseBase/blamely.vsix: $($_.Exception.Message) — skipping VS Code-family install"
        }

        if ($vsixOk) {
            # name -> CLI command + default install locations (user and system).
            # Base env vars are checked first: Join-Path throws on a null base, and
            # ProgramFiles(x86) is absent on some Windows editions.
            $lad = $env:LOCALAPPDATA; $pf = $env:ProgramFiles; $pf86 = ${env:ProgramFiles(x86)}
            $codePaths = @()
            if ($lad)  { $codePaths += (Join-Path $lad  'Programs\Microsoft VS Code\bin\code.cmd') }
            if ($pf)   { $codePaths += (Join-Path $pf   'Microsoft VS Code\bin\code.cmd') }
            if ($pf86) { $codePaths += (Join-Path $pf86 'Microsoft VS Code\bin\code.cmd') }
            $cursorPaths = @(); if ($lad) { $cursorPaths += (Join-Path $lad 'Programs\cursor\resources\app\bin\cursor.cmd') }
            $agPaths     = @(); if ($lad) { $agPaths     += (Join-Path $lad 'Programs\Antigravity\resources\app\bin\antigravity.cmd') }
            $editors = @(
                @{ Name='VS Code';         Command='code';        Paths=$codePaths },
                @{ Name='Cursor';          Command='cursor';      Paths=$cursorPaths },
                @{ Name='Antigravity IDE'; Command='antigravity'; Paths=$agPaths }
            )
            $installedAny = $false
            foreach ($e in $editors) {
                $exe = Resolve-EditorCli -Command $e.Command -Candidates $e.Paths
                if (-not $exe) { continue }
                # code.cmd / cursor.cmd routinely write notices to stderr; with
                # $ErrorActionPreference='Stop' the 2>&1 merge would turn that into a
                # terminating error and abort the (otherwise successful) install.
                # Soften it for the native call and judge success by the exit code.
                $prevEAP = $ErrorActionPreference
                $ErrorActionPreference = 'Continue'
                $out = & $exe --install-extension $vsix --force 2>&1
                $code = $LASTEXITCODE
                $ErrorActionPreference = $prevEAP
                if ($code -eq 0) {
                    Ok "$($e.Name) — beta extension installed (reload the window)"
                    $installedAny = $true
                } else {
                    Warn "$($e.Name) — could not install the beta extension (exit $code): $($out -join ' ')"
                }
            }
            if (-not $installedAny) {
                Warn 'No VS Code-family editor found (code/cursor/antigravity not on PATH or in their default install folders) — skipped the beta extension.'
                Warn 'Fix: in VS Code run Command Palette > "Shell Command: Install ''code'' command in PATH", then re-run this installer.'
            }
        }

        Info 'Downloading beta IntelliJ plugin...'
        $ijZip = Join-Path $tmp 'blamely-intellij.zip'
        try {
            Invoke-Download -Url "$ReleaseBase/blamely-intellij.zip" -OutFile $ijZip
            # Let the CLI place it: its IDE discovery finds Toolbox / Program Files
            # installs and not-yet-launched IDEs that a %APPDATA%\JetBrains glob
            # misses (the cause of "not find intellij").
            & $StableBin install-jetbrains-zip $ijZip 2>&1 | Out-Host
        } catch { Warn "could not download/install beta IntelliJ plugin: $($_.Exception.Message) — skipping" }
    } finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Invoke-BlamelyInstall {
    Info 'Running blamely install (hooks, daemon)...'
    # On the beta channel, skip the CLI's marketplace plugin install — beta plugins
    # are sideloaded from the beta release instead (see Sideload-BetaPlugins).
    $installArgs = @('install')
    if ($Channel -ne 'latest') { $installArgs += '--skip-plugins' }
    $installOut = @()
    try {
        $installOut = & $StableBin @installArgs 2>&1
        $installOut | ForEach-Object { Write-Host $_ }
    } catch {
        if (Test-AvBlocked $_) {
            Warn "Windows blocked $StableBin from running — antivirus flagged it as malicious/unwanted software."
            Info 'This is a known false positive on freshly-built, unsigned executables.'
            Info 'To resolve it:'
            Info '  1. Open Windows Security > Virus & threat protection > Protection history,'
            Info "     find the blocked 'blamely.exe' entry, and choose Restore/Allow on device."
            Info "  2. Or add an exclusion for $StableDir under"
            Info '     Virus & threat protection settings > Exclusions, then re-run this installer.'
            Info '  You can also report the false positive to Microsoft: https://www.microsoft.com/wdsi/filesubmission'
            Die 'Installation blocked by antivirus — follow the steps above, then re-run.'
        }
        $installOut = @($_.ToString())
        if (-not (Test-DaemonAccessDenied ($installOut | Out-String))) {
            Die "blamely install failed to launch: $($_.Exception.Message)"
        }
    }
    $installText = $installOut | Out-String
    if ($LASTEXITCODE -ne 0) {
        if (Test-DaemonAccessDenied $installText) {
            Warn 'Scheduled Task registration needs Administrator on this PC — using Startup folder instead.'
            Install-StartupDaemon
            if ($Channel -ne 'latest') { Sideload-BetaPlugins }
            Ok 'Blamely configured (git hooks installed; daemon runs without admin).'
            return
        }
        Die "blamely install failed (exit $LASTEXITCODE)"
    }
    try {
        & $StableBin repair 2>$null | Out-Null
    } catch {
        # best-effort; ignore launch failures here too
    }
    if ($Channel -ne 'latest') { Sideload-BetaPlugins }
    Ok 'Blamely configured.'
}

# ── main ─────────────────────────────────────────────────────────────────────

Write-Host ''
Write-Host 'Blamely CLI installer (Windows)' -ForegroundColor Cyan
if ($Channel -ne 'latest') { Write-Host "  channel: $Channel (pre-release)" -ForegroundColor Yellow }
Write-Host ''

if ($PSVersionTable.PSVersion.Major -lt 5) {
    Die 'PowerShell 5.1 or newer is required.'
}

# The Windows CLI isn't code-signed yet, so Defender can raise a false positive
# on the freshly-built binary. This is a normal, harmless occurrence — rather
# than interrupt with a scary confirmation, proactively add an install-folder
# exclusion (best-effort) so the download isn't quarantined, then continue.
Add-DefenderExclusion
Write-Host ''

Ensure-Git
Download-And-Install
Ensure-Sqlite3
Add-ToUserPath
Invoke-BlamelyInstall

Write-Host ''
Write-Host '  Run ' -NoNewline
Write-Host 'blamely status' -ForegroundColor Cyan -NoNewline
Write-Host ' to verify the daemon.'
Write-Host '  Run ' -NoNewline
Write-Host 'blamely doctor' -ForegroundColor Cyan -NoNewline
Write-Host ' for a full self-check.'
Write-Host '  blamely is on PATH in this session. Fully quit Windows Terminal and reopen if a new window cannot find it.'
Write-Host ''
