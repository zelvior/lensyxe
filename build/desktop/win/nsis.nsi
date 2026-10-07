; NSIS script for the Lensyxe Windows setup.
;
; Built by goreleaser's `nsis` target, which substitutes {{ .ProjectName }},
; {{ .Version }} and {{ .Arch }} and unpacks the release archive next to this
; script. Run `goreleaser release --snapshot --clean` to produce it locally.
;
; Design decisions, because an installer that gets these wrong is worse than no
; installer:
;
;   - No administrator prompt for the default install. Lensyxe installs under
;     LOCALAPPDATA and uses RequestExecutionLevel user. An installer that always
;     demands elevation is why people run portable binaries instead.
;   - PATH is an opt-in component, not a silent side effect. It is written to the
;     user environment, so it needs no elevation and cannot affect other users.
;   - The uninstaller removes only its own PATH entry, and only if this installer
;     added it. A record in HKCU records that fact.
;   - No `rmdir /r`. A typo in $INSTDIR must not be able to delete a home
;     directory.
;   - The user's .lensyxe.yml is never touched. It holds their decisions.

Unicode true
!include "MUI2.nsh"
!include "FileFunc.nsh"
!include "StrFunc.nsh"

!define APPNAME       "Lensyxe"
!define PUBLISHER     "Lensyxe"
!define UNINSTKEY     "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}"
!define ENVKEY        "Environment"
!define APPKEY        "Software\${APPNAME}"

Name         "${APPNAME}"
OutFile      "LensyxeSetup.exe"
InstallDir   "$LOCALAPPDATA\lensyxe\bin"
InstallDirRegKey HKCU "${APPKEY}" "InstallDir"
RequestExecutionLevel user
ShowInstDetails show
ShowUninstDetails show

VIProductVersion "1.0.0.0"
VIAddVersionKey /LANG=1033 "ProductName"     "${APPNAME}"
VIAddVersionKey /LANG=1033 "CompanyName"    "${PUBLISHER}"
VIAddVersionKey /LANG=1033 "FileVersion"    "{{ .Version }}"
VIAddVersionKey /LANG=1033 "ProductVersion" "{{ .Version }}"
VIAddVersionKey /LANG=1033 "LegalCopyright" "MIT licensed"

!insertmacro MUI_PAGE_LICENSE "..\..\..\LICENSE"
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\lensyxe.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Run lensyxe setup"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Section "lensyxe (required)" SecMain
  SectionIn RO
  ; The binary, its license, and the setup wizard. The wizard is what registers
  ; PATH and offers the background service, so it ships beside the binary rather
  ; than as a separate download that may not match it.
  SetOutPath "$INSTDIR"
  File /r "..\dist\{{ .ProjectName }}_windows_{{ .Arch }}\*"

  WriteUninstaller "$INSTDIR\Uninstall.exe"
  WriteRegStr HKCU "${APPKEY}" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayName"     "${APPNAME}"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayVersion"  "{{ .Version }}"
  WriteRegStr HKCU "${UNINSTKEY}" "Publisher"       "${PUBLISHER}"
  WriteRegStr HKCU "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTKEY}" "UninstallString" "$INSTDIR\Uninstall.exe"
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoRepair" 1

  ; Start Menu entries, not a Desktop shortcut. A desktop shortcut is clutter for
  ; a command-line tool, and the wizard is what a first-time user needs.
  CreateDirectory "$SMPROGRAMS\${APPNAME}"
  CreateShortCut "$SMPROGRAMS\${APPNAME}\Lensyxe Terminal.lnk" "$INSTDIR\lensyxe.exe" "" "" "" SW_SHOWNORMAL "" "Run the lensyxe wizard"
  CreateShortCut "$SMPROGRAMS\${APPNAME}\Setup Wizard.lnk"        "$INSTDIR\lensyxe-gui.exe"
SectionEnd

Section "Add lensyxe to PATH" SecPath
  ; Delegated to `lensyxe installer --yes` rather than done here.
  ;
  ; Two reasons. First, PATH editing in NSIS means hand-written substring search
  ; over a semicolon-separated list, and the failure mode of getting that wrong is
  ; silently corrupting a user's environment. Second, the Go implementation is
  ; covered by tests for idempotency and for leaving a hand-written entry alone,
  ; and duplicating that logic in a second language would mean two of them.
  ;
  ; It writes to the user environment, so no elevation is needed and no other user
  ; is affected.
  DetailPrint "Registering the PATH entry."
  ExecWait '"$INSTDIR\lensyxe.exe" installer --yes --dir "$INSTDIR"' $0

  ; Recorded so the uninstaller knows the entry was ours to remove.
  WriteRegStr HKCU "${APPKEY}" "PathRegistered" "$INSTDIR"
SectionEnd

Section "Start Menu shortcuts" SecMenu
  ; The shortcuts in SecMain are created unconditionally; this component exists
  ; only so the components page is not a single forced choice.
SectionEnd

Function un.onInit
  MessageBox MB_YESNO|MB_ICONQUESTION \
    "Uninstall ${APPNAME}?$\n$\nYour .lensyxe.yml will be left alone." IDYES +2
    Abort
FunctionEnd

Function un.onUninstSuccess
  ; Remove the PATH entry only if this installer added it, and only if the user
  ; still wants it removed. Delegated for the same reason as the install path.
  ReadRegStr $2 HKCU "${APPKEY}" "PathRegistered"
  ${If} $2 != ""
    ExecWait '"$INSTDIR\lensyxe.exe" installer --yes --remove --dir "$INSTDIR"' $0
    DeleteRegValue HKCU "${APPKEY}" "PathRegistered"
  ${EndIf}

  DeleteRegKey HKCU "${UNINSTKEY}"
  DeleteRegKey HKCU "${APPKEY}"
  Delete "$SMPROGRAMS\${APPNAME}\Lensyxe Terminal.lnk"
  Delete "$SMPROGRAMS\${APPNAME}\Setup Wizard.lnk"
  RMDir "$SMPROGRAMS\${APPNAME}"

  ; Our files only, and only our directory. A blanket rmdir /r would turn a typo
  ; in $INSTDIR into data loss.
  Delete "$INSTDIR\lensyxe.exe"
  Delete "$INSTDIR\lensyxe-gui.exe"
  Delete "$INSTDIR\LICENSE"
  Delete "$INSTDIR\README.md"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"
  RMDir "$LOCALAPPDATA\lensyxe"
FunctionEnd

Function .onInit
  ; Refuse to silently reinstall over an existing installation, which would move
  ; files out from under the first uninstaller.
  ReadRegStr $0 HKCU "${UNINSTKEY}" "UninstallString"
  ${If} $0 != ""
    MessageBox MB_YESNOCANCEL|MB_ICONQUESTION \
      "${APPNAME} is already installed at $0.$\n$\nYes reinstalls, No continues, Cancel uninstalls." \
      IDYES 0 IDNO 1
      ClearErrors
      ExecWait '$0 /S _?=$INSTDIR'
      DeleteRegValue HKCU "${UNINSTKEY}" "UninstallString"
      Abort
  ${EndIf}
FunctionEnd