; NSIS installer for the Blamely CLI Windows wizard (blamely_windows_<arch>_setup.exe).
;
; This is the CROSS-PLATFORM counterpart to blamely.iss (Inno Setup, Windows-only):
; NSIS's compiler `makensis` runs natively on macOS/Linux (brew install makensis), so
; the local beta pipeline can build the Windows wizard without Wine. The Inno script
; stays the canonical, signed installer the stable release ships; this one exists so
; `./beta-release.sh` produces a setup.exe on a Mac.
;
; It produces a per-user installer (no admin/UAC) that:
;   - installs blamely.exe to %LOCALAPPDATA%\Programs\Blamely
;   - drops sqlite3.exe into ~/.blamely/bin (the IDE plugins' DB reader)
;   - runs `blamely install`, which copies the binary to ~/.blamely/bin, adds it to
;     the user PATH, registers the per-user daemon, sets git core.hooksPath, and wires
;     each detected AI-tool hook. PATH/daemon/hooks are ALL handled by blamely itself,
;     so this script never edits PATH directly (avoids NSIS's string-length PATH risk).
;   - runs `blamely uninstall` on removal.
;
; Build (defines are supplied by build_windows_installer in beta-release.sh):
;   makensis -DVERSION=1.6.7-beta.1 -DARCH=x64 \
;            -DSRCDIR=/path/to/blamely_windows_amd64 \
;            -DSQLITE=/path/to/sqlite3.exe \
;            -DICON=/path/to/blamely.ico \
;            -DOUTFILE=/path/to/blamely_windows_amd64_setup.exe \
;            packaging/windows/blamely.nsi

Unicode true
ManifestDPIAware true
SetCompressor /SOLID lzma

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef ARCH
  !define ARCH "x64"
!endif
!ifndef SRCDIR
  !error "SRCDIR (dir containing blamely.exe) is required"
!endif
!ifndef OUTFILE
  !define OUTFILE "blamely-setup-${ARCH}.exe"
!endif

!include "MUI2.nsh"

Name "Blamely CLI"
OutFile "${OUTFILE}"
; Per-user install: no admin prompt, matching Blamely's per-user daemon/hook model.
RequestExecutionLevel user
InstallDir "$LOCALAPPDATA\Programs\Blamely"
ShowInstDetails show
ShowUninstDetails show
BrandingText "Blamely ${VERSION}"

!define REGKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\Blamely"

!ifdef ICON
  !define MUI_ICON "${ICON}"
  !define MUI_UNICON "${ICON}"
!endif
!define MUI_ABORTWARNING

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_TEXT "Blamely is installed. Your IDE plugins, git hook, and the background agent were set up automatically.$\r$\n$\r$\nOpen a NEW terminal and run 'blamely doctor' to verify."
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Section "Blamely CLI" SecMain
  SetOutPath "$INSTDIR"
  File "${SRCDIR}/blamely.exe"

  ; sqlite3.exe → ~/.blamely/bin (the IDE plugins look there FIRST, PATH-independent;
  ; a GUI-launched VS Code can't see PATH). Same source the curl installer uses.
!ifdef SQLITE
  SetOutPath "$PROFILE\.blamely\bin"
  File "${SQLITE}"
  SetOutPath "$INSTDIR"
!endif

  ; Let blamely wire everything: copy to ~/.blamely/bin, add to PATH, register the
  ; daemon, set git core.hooksPath, install AI-tool hooks. Runs the signed exe we just
  ; installed directly (no cmd/powershell window) — the EDR/SmartScreen-friendly path.
  DetailPrint "Setting up Blamely - IDE plugins, git hooks, and the background agent..."
  nsExec::ExecToLog '"$INSTDIR\blamely.exe" install'
  Pop $0

  ; Add/Remove Programs entry (per-user hive).
  WriteRegStr   HKCU "${REGKEY}" "DisplayName"     "Blamely CLI"
  WriteRegStr   HKCU "${REGKEY}" "DisplayVersion"  "${VERSION}"
  WriteRegStr   HKCU "${REGKEY}" "Publisher"       "Blamely"
  WriteRegStr   HKCU "${REGKEY}" "URLInfoAbout"    "https://blamely.ai"
  WriteRegStr   HKCU "${REGKEY}" "DisplayIcon"     "$INSTDIR\blamely.exe"
  WriteRegStr   HKCU "${REGKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr   HKCU "${REGKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr   HKCU "${REGKEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKCU "${REGKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${REGKEY}" "NoRepair" 1

  WriteUninstaller "$INSTDIR\uninstall.exe"
SectionEnd

Section "Uninstall"
  ; Reverse the hook/daemon/PATH wiring before files are removed. Best-effort.
  nsExec::ExecToLog '"$INSTDIR\blamely.exe" uninstall'
  Pop $0

  Delete "$INSTDIR\blamely.exe"
  Delete "$INSTDIR\uninstall.exe"
  RMDir  "$INSTDIR"
  DeleteRegKey HKCU "${REGKEY}"
SectionEnd
