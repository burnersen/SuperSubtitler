@echo off
REM ============================================================================
REM  build.bat - SuperSubtitler build script
REM ----------------------------------------------------------------------------
REM  Run this from the folder that contains the .go sources (this "src" folder).
REM
REM  Steps:
REM    1) Resolve dependencies (go mod tidy).
REM    2) Build SuperSubtitler.exe (stripped: -ldflags="-s -w") into the parent
REM       folder, next to ffmpeg / Python / whisper, ready to run.
REM ============================================================================
setlocal
cd /d "%~dp0"

set "EXE=..\SuperSubtitler.exe"

echo [1/2] Resolving dependencies (go mod tidy) ...
go mod tidy || ( echo [ERROR] go mod tidy failed. & pause & exit /b 1 )

echo [2/2] Building %EXE% ...
go build -ldflags="-s -w" -o "%EXE%" || ( echo [ERROR] Build failed. & pause & exit /b 1 )

echo.
echo Done: %EXE%
pause
