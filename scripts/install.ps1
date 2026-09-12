#requires -version 5.1
<#
.SYNOPSIS
    Build + install / uninstall blamely on Windows.

.DESCRIPTION
    Builds the blamely binary from this repo with stripped/trim-path flags,
    copies it to %USERPROFILE%\.blamely\bin\blamely.exe, then runs
    `blamely install` (which registers the Scheduled Task daemon and adds the
    AI-tool hooks). On uninstall, reverses everything.

.PARAMETER Action
    install (default) | rebuild | uninstall | repair | doctor

.PARAMETER WithPlugins
    Also install the VS Code-family and JetBrains IDE plugins from the
    marketplace. Off by default for local dev installs (same default as
    scripts/install.sh): the download is slow and, more importantly, it
    overwrites a sideloaded dev build of the plugin under test.

.EXAMPLE
    pwsh -File scripts\install.ps1
    pwsh -File scripts\install.ps1 -WithPlugins
    pwsh -File scripts\install.ps1 uninstall

.NOTES
    Keep this file pure ASCII. Windows PowerShell 5.1 decodes a BOM-less
    script with the system ANSI codepage, so a UTF-8 em dash lands as a
    curly quote - which PowerShell accepts as a string delimiter - and the
    whole file fails to parse. Use plain hyphens, not typographic dashes.
#>

param(
    [ValidateSet('install', 'rebuild', 'uninstall', 'repair', 'doctor')]
    [string]$Action = 'install',

    [switch]$WithPlugins
)

$ErrorActionPreference = 'Stop'

$RepoRoot   = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$StableDir  = Join-Path $env:USERPROFILE '.blamely\bin'
$StableBin  = Join-Path $StableDir 'blamely.exe'

function Die($msg) { Write-Host "error: $msg" -ForegroundColor Red; exit 1 }
function Ok($msg)  { Write-Host ("  [+] {0}" -f $msg) -ForegroundColor Green }
function Info($msg){ Write-Host ("  -> {0}" -f $msg) }

function Require-Go {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        Die "Go is not installed. Get it from https://go.dev/dl/"
    }
}

function Build-Binary {
    Info "Building blamely from source (stripped, trim-path)..."
    if (-not (Test-Path $StableDir)) {
        New-Item -ItemType Directory -Path $StableDir -Force | Out-Null
    }
    Push-Location $RepoRoot
    try {
        # -s strip symbol table, -w strip DWARF, -trimpath remove abs paths,
        # -buildvcs=false don't stamp git metadata.
        & go build -trimpath -buildvcs=false -ldflags="-s -w" -o $StableBin .\cmd\blamely
        if ($LASTEXITCODE -ne 0) { Die "go build failed" }
    } finally {
        Pop-Location
    }
    $size = "{0:N1} MB" -f ((Get-Item $StableBin).Length / 1MB)
    Ok "Binary built: $StableBin ($size)"
}

function Add-ToUserPath {
    # Append %USERPROFILE%\.blamely\bin to the per-user PATH (persistent).
    # No-op if already present. Doesn't affect the current session - caller
    # must open a new shell.
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
    Ok "Added $entry to user PATH (open a new shell for it to take effect)"
}

function Remove-FromUserPath {
    $entry = $StableDir
    $current = [Environment]::GetEnvironmentVariable('PATH', 'User')
    if (-not $current) { return }
    $kept = $current -split ';' | Where-Object { $_ -ne '' -and $_ -ne $entry }
    [Environment]::SetEnvironmentVariable('PATH', ($kept -join ';'), 'User')
    Ok "Removed $entry from user PATH"
}

function Do-Install {
    Require-Go
    Build-Binary

    Info "Running blamely install..."
    if ($WithPlugins) {
        & $StableBin install
    } else {
        & $StableBin install --skip-plugins
    }
    if ($LASTEXITCODE -ne 0) { Die "blamely install failed (exit $LASTEXITCODE)" }
    if (-not $WithPlugins) {
        Info "Skipped IDE/editor plugin install (local dev default) - pass -WithPlugins to include it."
    }
    Ok "Blamely installed."

    # Belt and braces: `blamely install` already writes the HKCU Environment
    # Path entry (install.InstallPathEntry), so this is normally a no-op.
    Add-ToUserPath

    # Best-effort cleanup of legacy hooks (no-op if there are none).
    & $StableBin repair 2>$null | Out-Null

    Write-Host ""
    Write-Host "  Run " -NoNewline; Write-Host "blamely status" -ForegroundColor Cyan -NoNewline
    Write-Host " to verify the daemon is running."
    Write-Host "  Run " -NoNewline; Write-Host "git commit" -ForegroundColor Cyan -NoNewline
    Write-Host " in any repo - you should see the AI/Human bar."
    Write-Host "  Open a NEW PowerShell so the PATH update takes effect."
}

function Do-Rebuild {
    Require-Go
    Build-Binary
    Write-Host ""
    Write-Host "  Rebuilt only - install state was not touched."
}

function Do-Uninstall {
    if (Test-Path $StableBin) {
        Info "Running blamely uninstall..."
        & $StableBin uninstall
        Ok "Blamely configuration removed."
    } elseif (Get-Command blamely -ErrorAction SilentlyContinue) {
        Info "Running blamely uninstall via PATH..."
        & blamely uninstall
        Ok "Blamely configuration removed."
    } else {
        Write-Host "  blamely binary not found - skipping uninstall step."
        Write-Host "  Manually remove $env:USERPROFILE\.blamely and run:"
        Write-Host "    git config --global --unset core.hooksPath"
    }

    if (Test-Path $StableBin) {
        Remove-Item -Force $StableBin
        if ((Get-ChildItem -Path $StableDir -Force | Measure-Object).Count -eq 0) {
            Remove-Item -Force $StableDir
        }
        Ok "Binary removed."
    }
    Remove-FromUserPath

    Write-Host ""
    Write-Host "  Blamely uninstalled. Attribution history is kept at $env:USERPROFILE\.blamely\db.sqlite."
    Write-Host "  Remove it manually if you want to wipe all history:"
    Write-Host "    Remove-Item -Recurse -Force $env:USERPROFILE\.blamely"
}

function Do-Repair {
    if (Test-Path $StableBin) {
        & $StableBin repair
    } elseif (Get-Command blamely -ErrorAction SilentlyContinue) {
        & blamely repair
    } else {
        Die "blamely binary not found. Run: pwsh -File scripts\install.ps1"
    }
}

function Do-Doctor {
    if (Test-Path $StableBin) {
        & $StableBin doctor
    } elseif (Get-Command blamely -ErrorAction SilentlyContinue) {
        & blamely doctor
    } else {
        Die "blamely binary not found. Run: pwsh -File scripts\install.ps1"
    }
}

switch ($Action) {
    'install'   { Do-Install }
    'rebuild'   { Do-Rebuild }
    'uninstall' { Do-Uninstall }
    'repair'    { Do-Repair }
    'doctor'    { Do-Doctor }
}
