@echo off
REM install.bat - Windows CMD entry point.
REM
REM This file does one thing: hand control to install.ps1.
REM
REM It used to carry its own "detect arch -> pick binary -> pass through
REM subcommand" logic, which meant the CMD and PowerShell entries were two
REM implementations that drifted apart. Installation logic now lives in
REM install.ps1 only; this file just invokes it, because CMD cannot run
REM ".\install.ps1" directly and needs ExecutionPolicy Bypass anyway.
REM
REM This file is deliberately ASCII-only. A .bat is parsed by cmd.exe in the
REM OEM code page *before* "chcp 65001" can take effect, so non-ASCII bytes in
REM a REM line or an echo can decode into a backslash (line continuation) or
REM another metacharacter and break the script mid-file. Keeping it ASCII
REM removes that whole class of failure. All user-facing text lives in
REM install.ps1, which is UTF-8 and is parsed correctly.
REM
REM Usage:
REM   install.bat                          interactive install
REM   install.bat -Channel source          build from this source tree, then install
REM   install.bat -Channel npm             npm global install
REM   install.bat doctor                   environment report only
REM   install.bat version                  version + how to get updates
REM   install.bat upgrade                  pass through to llm-gw-installer

setlocal
cd /d "%~dp0"

where powershell >nul 2>nul
if errorlevel 1 (
    echo [install] ERROR: powershell not found in PATH
    exit /b 1
)

powershell -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1" %*
exit /b %ERRORLEVEL%
