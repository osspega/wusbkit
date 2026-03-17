@echo off
setlocal enabledelayedexpansion

:: Read version from VERSION file
set /p VERSION=<VERSION

echo Building wusbkit GUI v%VERSION%...

:: Check prerequisites
where wails >nul 2>&1
if %ERRORLEVEL% NEQ 0 (
    echo Error: wails CLI not found. Install with: go install github.com/wailsapp/wails/v2/cmd/wails@latest
    exit /b 1
)

where node >nul 2>&1
if %ERRORLEVEL% NEQ 0 (
    echo Error: node not found. Install Node.js from https://nodejs.org
    exit /b 1
)

:: Build with Wails
cd gui
wails build

if %ERRORLEVEL% EQU 0 (
    echo.
    echo Build successful: gui\build\bin\wusbkit-gui.exe
    echo Version: %VERSION%
) else (
    echo.
    echo Build failed!
    exit /b 1
)

endlocal
