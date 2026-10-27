@echo off
REM ============================================================================
REM  build.bat - SuperSubtitlerGUI build script
REM ----------------------------------------------------------------------------
REM  Builds SuperSubtitlerGUI.exe into the same folder as SuperSubtitler.exe
REM  (two levels up: <folder>\src\gui -> <folder>), because the window starts
REM  the CLI that sits next to it and reads its settings files from there.
REM
REM  "wails build" works too; this plain "go build" needs no Wails CLI and no
REM  Node: the window is one HTML file plus logic.js, embedded as they are.
REM  The tags are the ones "wails build" sets - without them the exe refuses
REM  to start.
REM ============================================================================
setlocal
cd /d "%~dp0"

set "EXE=..\..\SuperSubtitlerGUI.exe"

echo [1/2] Resolving dependencies (go mod tidy) ...
go mod tidy || ( echo [ERROR] go mod tidy failed. & pause & exit /b 1 )

echo [2/2] Building %EXE% ...
go build -tags desktop,production -ldflags="-s -w -H windowsgui" -o "%EXE%" . || ( echo [ERROR] Build failed. & pause & exit /b 1 )

echo.
echo Done: %EXE%
pause
