; Inno Setup script for the Blamely CLI Windows installer (blamely-setup.exe).
;
; Produces a per-user installer (no admin/UAC) that:
;   - installs blamely.exe to %LOCALAPPDATA%\Programs\Blamely
;   - drops sqlite3.exe into ~/.blamely/bin (the IDE plugins' DB reader)
;   - adds the install directory to the user PATH
;   - runs `blamely install` to copy the binary to ~/.blamely/bin, register the
;     per-user daemon, set git core.hooksPath, and wire each detected AI tool hook
;   - runs `blamely uninstall` on removal
;
; A SIGNED installer is what clears SmartScreen / Defender — an unsigned,
; garble-obfuscated .exe is what trips the Trojan:Win32 false positives and what
; makes the `irm | iex` script get blocked by corporate security. Sign at compile
; time with the SignTool directive (configured by the caller, see build note).
;
; Build (on Windows, with Inno Setup 6):
;   iscc /DAppVersion=1.7.0 /DArch=x64 ^
;        /DSourceDir=path\to\blamely_v1.7.0_windows_amd64 ^
;        /DSqlite=path\to\sqlite3.exe ^
;        packaging\windows\blamely.iss
;   (add /Smysign="signtool ..." + SignTool directive to sign — see README)
;
; Required /D defines: AppVersion, Arch (x64|arm64), SourceDir (holds blamely.exe),
;   Sqlite (path to sqlite3.exe).

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef Arch

  #define Arch "x64"
#endif
#ifndef SourceDir
  #define SourceDir "."
#endif
#ifndef Sqlite
  #define Sqlite ""
#endif

; Inno architecture identifiers: x64 → "x64compatible", arm64 → "arm64".
#if Arch == "arm64"
  #define ArchId "arm64"
#else
  #define ArchId "x64compatible"
#endif

[Setup]
AppId={{A7C3F2E1-9B4D-4E6A-8F12-3D5C7A9E1B02}
AppName=Blamely CLI
AppVersion={#AppVersion}
AppPublisher=Blamely
AppPublisherURL=https://blamely.ai
DefaultDirName={localappdata}\Programs\Blamely
DefaultGroupName=Blamely
DisableProgramGroupPage=yes
DisableDirPage=yes
; Per-user install: no admin prompt, matches Blamely's per-user daemon/hook model.
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
ArchitecturesInstallIn64BitMode={#ArchId}
ArchitecturesAllowed={#ArchId}
ChangesEnvironment=yes
OutputBaseFilename=blamely-setup-{#Arch}
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
; Brand the installer: the setup.exe file icon, the wizard's small header/window
; icon, and the Add/Remove Programs entry. blamely.ico sits next to this script, so
; the path is relative to the script dir (SourcePath). Embedded into blamely.exe too
; via the committed .syso resource, so UninstallDisplayIcon below shows it as well.
SetupIconFile=blamely.ico
UninstallDisplayName=Blamely CLI
UninstallDisplayIcon={app}\blamely.exe

[Files]
Source: "{#SourceDir}\blamely.exe"; DestDir: "{app}"; Flags: ignoreversion
#if Sqlite != ""
; The IDE plugins read the attribution DB via sqlite3, which Windows lacks. They
; look in ~/.blamely/bin FIRST (PATH-independent — a GUI-launched VS Code can't
; see PATH), so place it there directly, matching the curl-installer behavior.
Source: "{#Sqlite}"; DestDir: "{%USERPROFILE}\.blamely\bin"; Flags: ignoreversion
#endif

; NOTE: `blamely install` is intentionally NOT run from [Run]. It's launched from
; [Code]'s CurStepChanged(ssPostInstall) by running the signed blamely.exe DIRECTLY
; (no PowerShell/cmd/script, no console window). blamely writes its own report to
; %USERPROFILE%\.blamely\last-install.log, which the finished "Installation
; details" page displays — the clean, EDR-friendly way to show what was set up.

[UninstallRun]
; Reverse the hook/daemon/PATH wiring before files are removed. Best-effort.
Filename: "{app}\blamely.exe"; Parameters: "uninstall"; \
  RunOnceId: "BlamelyUninstall"; Flags: runhidden

[Code]
const
  EnvKey = 'Environment';

var
  // Path of the captured `blamely install` output; shown on the finished page.
  InstallLog: string;
  // Heading + read-only memo lazily added to the finished page to display it.
  DetailsHeading: TNewStaticText;
  DetailsMemo: TNewMemo;

// Read the current user PATH from HKCU\Environment.
function GetUserPath(): string;
begin
  if not RegQueryStringValue(HKCU, EnvKey, 'Path', Result) then
    Result := '';
end;

// True if Dir is already a ';'-delimited entry in Path (case-insensitive).
function PathHasDir(const Path, Dir: string): Boolean;
var
  Hay, Needle: string;
begin
  Hay := ';' + Lowercase(Path) + ';';
  Needle := ';' + Lowercase(Dir) + ';';
  Result := Pos(Needle, Hay) > 0;
end;

// Append {app} to the user PATH (idempotent). Split out of CurStepChanged so the
// install step below always runs, even when the PATH entry is already present
// (e.g. a repair/reinstall over an existing install).
procedure AddAppToUserPath();
var
  Path, Dir: string;
begin
  Dir := ExpandConstant('{app}');
  Path := GetUserPath();
  if PathHasDir(Path, Dir) then
    Exit;
  if (Path <> '') and (Path[Length(Path)] <> ';') then
    Path := Path + ';';
  RegWriteExpandStringValue(HKCU, EnvKey, 'Path', Path + Dir);
end;

// Run `blamely install` — the signed, bundled blamely.exe, DIRECTLY. No
// PowerShell, no cmd, no script, and no console window (SW_HIDE). Running a
// code-signed child exe is what keeps the installer clean for corporate
// EDR/SmartScreen — the `powershell -ExecutionPolicy Bypass` / `irm … | iex`
// patterns are exactly what those tools flag.
//
// blamely writes its own step-by-step report (detected IDEs, installed plugins,
// git hooks, background agent) to %USERPROFILE%\.blamely\last-install.log (see
// internal/install/ui.go). The finished page reads that file — no shell
// redirection needed to capture the output.
procedure RunBlamelyInstall();
var
  ResultCode: Integer;
begin
  InstallLog := ExpandConstant('{userprofile}\.blamely\last-install.log');
  WizardForm.StatusLabel.Caption :=
    'Setting up Blamely — IDE plugins, git hooks, and the background agent...';
  if not Exec(ExpandConstant('{app}\blamely.exe'), 'install',
     ExpandConstant('{app}'), SW_HIDE, ewWaitUntilTerminated, ResultCode) then
    Log('blamely install failed to launch: ' + IntToStr(ResultCode));
end;

// After files are copied: add {app} to PATH, then run (and show) `blamely install`.
procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep <> ssPostInstall then
    Exit;
  AddAppToUserPath();
  RunBlamelyInstall();
end;

// Present the captured `blamely install` report on the finished page — a clean,
// in-wizard "what was set up" panel (heading + read-only monospaced memo), so the
// user sees the detected IDEs, installed plugins, git hooks and agent without any
// popup dialog or console window.
procedure CurPageChanged(CurPageID: Integer);
begin
  if CurPageID <> wpFinished then
    Exit;
  if DetailsMemo = nil then
  begin
    DetailsHeading := TNewStaticText.Create(WizardForm);
    DetailsHeading.Parent := WizardForm.FinishedPage;
    DetailsHeading.Left := WizardForm.FinishedLabel.Left;
    DetailsHeading.Top := WizardForm.FinishedLabel.Top + ScaleY(44);
    DetailsHeading.Width := WizardForm.FinishedLabel.Width;
    DetailsHeading.Caption := 'What Blamely set up on this machine:';
    DetailsHeading.Font.Style := [fsBold];

    DetailsMemo := TNewMemo.Create(WizardForm);
    DetailsMemo.Parent := WizardForm.FinishedPage;
    DetailsMemo.Left := WizardForm.FinishedLabel.Left;
    DetailsMemo.Top := DetailsHeading.Top + ScaleY(20);
    DetailsMemo.Width := WizardForm.FinishedLabel.Width;
    DetailsMemo.Height := WizardForm.FinishedPage.ClientHeight - DetailsMemo.Top - ScaleY(8);
    DetailsMemo.ReadOnly := True;
    DetailsMemo.ScrollBars := ssVertical;
    DetailsMemo.WordWrap := False;
    DetailsMemo.Font.Name := 'Consolas';
  end;
  if (InstallLog <> '') and FileExists(InstallLog) then
    DetailsMemo.Lines.LoadFromFile(InstallLog)
  else
    DetailsMemo.Lines.Text := 'Blamely was installed successfully.';
end;

// Remove {app} from the user PATH on uninstall.
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Path, Dir, Lower, LowerDir, NewPath: string;
  P: Integer;
begin
  if CurUninstallStep <> usUninstall then
    Exit;
  Dir := ExpandConstant('{app}');
  Path := GetUserPath();
  if Path = '' then
    Exit;
  Lower := Lowercase(Path);
  LowerDir := Lowercase(Dir);
  // Drop ";{app}" or "{app};" or a lone "{app}".
  NewPath := Path;
  P := Pos(';' + LowerDir + ';', ';' + Lower + ';');
  if P > 0 then
  begin
    // Rebuild by splitting is simplest; use StringChangeEx on the delimited form.
    NewPath := ';' + Path + ';';
    StringChangeEx(NewPath, ';' + Dir + ';', ';', True);
    // Trim the sentinel semicolons we added.
    if (Length(NewPath) >= 2) and (NewPath[1] = ';') then
      Delete(NewPath, 1, 1);
    if (Length(NewPath) >= 1) and (NewPath[Length(NewPath)] = ';') then
      Delete(NewPath, Length(NewPath), 1);
    RegWriteExpandStringValue(HKCU, EnvKey, 'Path', NewPath);
  end;
end;
