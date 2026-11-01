@echo off
rem Install the wfx client on Windows 10 and later. The same behaviour as install.sh.
rem
rem   curl.exe -fsSLo install.cmd https://raw.githubusercontent.com/muthuishere/wfnexus/main/install.cmd && install.cmd
rem   set WFX_VERSION=v0.1.0 && install.cmd        pin a version
rem   set WFX_BIN=wfx-server && install.cmd        the server (or wfx-runner) instead
rem   set WFX_SKILLS=0 && install.cmd              the CLI without its agent skills (or: install.cmd --no-skills)
rem
rem Only what ships with Windows 10+: curl.exe to download, certutil to hash,
rem reg to set the user PATH. Release assets are bare .exe files, so nothing is
rem unpacked. Per-user only: %LOCALAPPDATA%\wfx\bin, never administrator rights.
rem
rem THE ONE RULE, as in install.sh: the download is verified against the
rem release's checksums.txt and NOTHING is installed on a mismatch or when the
rem checksum cannot be obtained.
setlocal EnableExtensions EnableDelayedExpansion

if "%WFX_REPO%"=="" (set "REPO=muthuishere/wfnexus") else (set "REPO=%WFX_REPO%")
if "%WFX_BASE_URL%"=="" (set "BASE=https://github.com/%REPO%/releases") else (set "BASE=%WFX_BASE_URL%")
if "%WFX_BIN%"=="" (set "BIN=wfx") else (set "BIN=%WFX_BIN%")
if "%WFX_INSTALL_DIR%"=="" (set "DEST=%LOCALAPPDATA%\wfx\bin") else (set "DEST=%WFX_INSTALL_DIR%")
set "SKILLS=1"
if "%WFX_SKILLS%"=="0" set "SKILLS=0"
if /i "%~1"=="--no-skills" set "SKILLS=0"
if not "%~1"=="" if /i not "%~1"=="--no-skills" (
  echo wfx install: unknown argument '%~1' ^(only --no-skills^) 1>&2
  exit /b 1
)

if /i not "%BIN%"=="wfx" if /i not "%BIN%"=="wfx-server" if /i not "%BIN%"=="wfx-runner" (
  echo wfx install: WFX_BIN must be wfx, wfx-server or wfx-runner ^(got '%BIN%'^) 1>&2
  exit /b 1
)

rem ---- what machine is this ------------------------------------------------
rem A 32-bit shell on a 64-bit Windows reports x86 here and the real
rem architecture in PROCESSOR_ARCHITEW6432.
set "PA=%PROCESSOR_ARCHITECTURE%"
if defined PROCESSOR_ARCHITEW6432 set "PA=%PROCESSOR_ARCHITEW6432%"
set "GOARCH="
if /i "%PA%"=="AMD64" set "GOARCH=amd64"
if /i "%PA%"=="ARM64" set "GOARCH=arm64"
if "%GOARCH%"=="" (
  echo wfx install: unsupported architecture '%PA%'. Published platforms: windows/amd64, windows/arm64. 1>&2
  exit /b 1
)

where curl.exe >nul 2>nul || (
  echo wfx install: curl.exe is not on PATH - it ships with Windows 10 1803 and later 1>&2
  exit /b 1
)

if "%WFX_VERSION%"=="" (
  set "VERSION=latest"
  set "URL=%BASE%/latest/download"
) else (
  set "VERSION=%WFX_VERSION%"
  set "URL=%BASE%/download/%WFX_VERSION%"
)

set "ASSET=%BIN%-windows-%GOARCH%.exe"
set "TMPD=%TEMP%\wfx-install-%RANDOM%%RANDOM%"
mkdir "%TMPD%" || (echo wfx install: cannot create a temp dir 1>&2 & exit /b 1)

echo wfx: windows/%GOARCH%, %VERSION%

rem ---- the checksum FIRST: no checksums, no install --------------------------
curl.exe -fsSL -o "%TMPD%\checksums.txt" "%URL%/checksums.txt"
if errorlevel 1 (
  set "ERR=could not download %URL%/checksums.txt - refusing to install unverified"
  goto :fail
)
set "WANT="
for /f "usebackq tokens=1,2" %%a in ("%TMPD%\checksums.txt") do (
  if "%%b"=="%ASSET%" set "WANT=%%a"
  if "%%b"=="*%ASSET%" set "WANT=%%a"
)
if "%WANT%"=="" (
  set "ERR=no checksum published for '%ASSET%' - refusing to install unverified"
  goto :fail
)

curl.exe -fsSL -o "%TMPD%\%ASSET%" "%URL%/%ASSET%"
if errorlevel 1 (
  set "ERR=download failed: %URL%/%ASSET%"
  goto :fail
)
for %%F in ("%TMPD%\%ASSET%") do if %%~zF==0 (
  set "ERR=downloaded file is empty - a partial download, nothing installed"
  goto :fail
)

rem certutil prints a header line, the hash, then a status line. Older
rem Windows separates the hash's bytes with spaces; they are removed.
set "GOT="
for /f "skip=1 delims=" %%h in ('certutil -hashfile "%TMPD%\%ASSET%" SHA256') do (
  if not defined GOT set "GOT=%%h"
)
set "GOT=%GOT: =%"
if /i not "%GOT%"=="%WANT%" (
  del /f /q "%TMPD%\%ASSET%" >nul 2>nul
  set "ERR=CHECKSUM MISMATCH for %ASSET% (published %WANT%, download %GOT%). Nothing was installed and the download has been deleted."
  goto :fail
)
echo verified sha256 %GOT%

rem ---- install ---------------------------------------------------------------
if not exist "%DEST%" mkdir "%DEST%" || (set "ERR=cannot create %DEST%" & goto :fail)
rem One move, so an interrupted install cannot leave a half-written exe on PATH.
move /y "%TMPD%\%ASSET%" "%DEST%\%BIN%.exe" >nul || (set "ERR=cannot write to %DEST%" & goto :fail)
echo installed %DEST%\%BIN%.exe

rem ---- the user PATH ---------------------------------------------------------
rem Read from the registry, not %PATH%: %PATH% is the user AND system path
rem joined, and writing it back with setx would copy the system path into the
rem user's and truncate it at 1024 characters. reg add has no such limit and
rem keeps unexpanded entries (%USERPROFILE%\...) as they were.
set "UPATH="
for /f "skip=2 tokens=2,*" %%a in ('reg query HKCU\Environment /v Path 2^>nul') do set "UPATH=%%b"
echo ;%UPATH%; | find /i ";%DEST%;" >nul
if errorlevel 1 (
  if "%UPATH%"=="" (set "NEWPATH=%DEST%") else (set "NEWPATH=%UPATH%;%DEST%")
  reg add HKCU\Environment /v Path /t REG_EXPAND_SZ /d "!NEWPATH!" /f >nul
  if errorlevel 1 (
    echo.
    echo could not add %DEST% to your PATH - add it yourself in System Properties, Environment Variables.
  ) else (
    rem setx of a throwaway variable broadcasts WM_SETTINGCHANGE, so terminals
    rem opened from now on see the new PATH; the variable is then removed.
    setx WFX_PATH_REFRESH 1 >nul 2>nul
    reg delete HKCU\Environment /v WFX_PATH_REFRESH /f >nul 2>nul
    echo added %DEST% to your user PATH - open a new terminal to use it
  )
)

rem ---- agent skills (the CLI only) -------------------------------------------
rem A failure is a WARNING: the CLI is installed and verified.
if /i "%BIN%"=="wfx" if "%SKILLS%"=="1" (
  "%DEST%\wfx.exe" install --skills > "%TMPD%\skills.log" 2>&1
  if errorlevel 1 (
    echo wfx install: warning - the agent skills were not installed: 1>&2
    type "%TMPD%\skills.log" 1>&2
    echo   the CLI is installed; retry with: wfx install --skills 1>&2
  ) else (
    set "N=the"
    for /f "tokens=1" %%n in ('findstr /r /c:"^[0-9][0-9]* skills: " "%TMPD%\skills.log"') do set "N=%%n"
    echo installed !N! agent skills into %USERPROFILE%\.claude\skills and %USERPROFILE%\.agents\skills ^(opt out: WFX_SKILLS=0^)
  )
)

rmdir /s /q "%TMPD%" >nul 2>nul
echo.
echo try: %BIN% version
endlocal
exit /b 0

:fail
echo wfx install: %ERR% 1>&2
rmdir /s /q "%TMPD%" >nul 2>nul
endlocal
exit /b 1
