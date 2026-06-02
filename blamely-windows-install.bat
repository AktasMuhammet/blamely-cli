#requires -Version 5.1
<#
.SYNOPSIS
    Blamely CLI installer for Windows.

.DESCRIPTION
    Intended for:
      powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://blamely.ai/blamely-windows-install.bat | iex"

    Downloads the latest release zip, installs to %USERPROFILE%\.blamely\bin,
    adds that directory to the user PATH, and runs `blamely install`.

    Checks for git (required) and offers to install via winget when missing.
    Java is not required for the CLI.
#>

$ErrorActionPreference = 'Stop'

$ReleaseBase = 'https://github.com/blamely-ai/blamely/releases/download/latest'
$StableDir   = Join-Path $env:USERPROFILE '.blamely\bin'
$StableBin   = Join-Path $StableDir 'blamely.exe'
$AutoYes     = [bool]$env:BLAMELY_INSTALL_YES

function Info($msg)  { Write-Host ("  -> {0}" -f $msg) }
function Ok($msg)    { Write-Host ("  [+] {0}" -f $msg) -ForegroundColor Green }
function Warn($msg)  { Write-Host ("  [!] {0}" -f $msg) -ForegroundColor Yellow }
function Die($msg)   { Write-Host ("error: {0}" -f $msg) -ForegroundColor Red; exit 1 }

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
    & winget install --id Git.Git -e --source winget --accept-package-agreements --accept-source-agreements
    if ($LASTEXITCODE -ne 0) {
        Die "winget install Git failed (exit $LASTEXITCODE)"
    }
    $env:Path = [System.Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' +
        [System.Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
        Die 'Git was installed but is not on PATH yet. Open a new PowerShell and re-run.'
    }
    Ok 'git installed'
}

function Add-ToUserPath {
    $entry = $StableDir
    $current = [Environment]::GetEnvironmentVariable('PATH', 'User')
    if (-not $current) { $current = '' }
    $parts = $current -split ';' | Where-Object { $_ -ne '' }
    if ($parts -contains $entry) {
        Info "PATH already contains $entry"
        return
    }
    $newPath = ($parts + $entry) -join ';'
    [Environment]::SetEnvironmentVariable('PATH', $newPath, 'User')
    $env:Path = "$entry;$env:Path"
    Ok "Added $entry to user PATH"
}

function Download-And-Install {
    $arch = Get-WindowsArch
    $asset = "blamely_windows_${arch}.zip"
    $url = "$ReleaseBase/$asset"
    $tmpdir = Join-Path ([System.IO.Path]::GetTempPath()) ("blamely-install-" + [guid]::NewGuid().ToString('n'))
    New-Item -ItemType Directory -Path $tmpdir -Force | Out-Null
    try {
        Info "Downloading $url ..."
        $zipPath = Join-Path $tmpdir $asset
        Invoke-WebRequest -Uri $url -OutFile $zipPath -UseBasicParsing
        Expand-Archive -Path $zipPath -DestinationPath $tmpdir -Force
        $bin = Get-ChildItem -Path $tmpdir -Recurse -Filter 'blamely.exe' -File -ErrorAction SilentlyContinue |
            Select-Object -First 1
        if (-not $bin) {
            Die "could not find blamely.exe inside $asset"
        }
        if (-not (Test-Path $StableDir)) {
            New-Item -ItemType Directory -Path $StableDir -Force | Out-Null
        }
        Copy-Item -Path $bin.FullName -Destination $StableBin -Force
        Ok "Binary installed: $StableBin"
    } finally {
        Remove-Item -LiteralPath $tmpdir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Invoke-BlamelyInstall {
    Info 'Running blamely install (hooks, daemon)...'
    & $StableBin install
    if ($LASTEXITCODE -ne 0) {
        Die "blamely install failed (exit $LASTEXITCODE)"
    }
    & $StableBin repair 2>$null | Out-Null
    Ok 'Blamely configured.'
}

# ── main ─────────────────────────────────────────────────────────────────────

Write-Host ''
Write-Host 'Blamely CLI installer (Windows)' -ForegroundColor Cyan
Write-Host ''

if ($PSVersionTable.PSVersion.Major -lt 5) {
    Die 'PowerShell 5.1 or newer is required.'
}

Ensure-Git
Download-And-Install
Add-ToUserPath
Invoke-BlamelyInstall

Write-Host ''
Write-Host '  Run ' -NoNewline
Write-Host 'blamely status' -ForegroundColor Cyan -NoNewline
Write-Host ' to verify the daemon.'
Write-Host '  Run ' -NoNewline
Write-Host 'blamely doctor' -ForegroundColor Cyan -NoNewline
Write-Host ' for a full self-check.'
Write-Host '  Open a NEW terminal so PATH includes ~/.blamely/bin.'
Write-Host ''
