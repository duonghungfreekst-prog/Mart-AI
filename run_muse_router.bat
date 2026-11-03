@echo off
chcp 65001 >nul
title MuseRouter — Multi-Account Muse AI Router ^& Web Studio (Real Protocol)
echo ============================================================================
echo   MuseRouter (Antigravity Polyglot v2.4 — Zero Mock / 100%% Real Protocol)
echo   Luong: C# UI -^> Go Engine -^> Python AI Bridge -^> Rust Core Vault
echo ============================================================================

cd /d "%~dp0"

echo [0/3] Don dep tien trinh cu tren cong 8088 / 18089 (neu co)...
taskkill /F /IM muse_router.exe >nul 2>&1
for /f "tokens=5" %%a in ('netstat -aon ^| findstr ":18089" ^| findstr "LISTENING"') do (
    taskkill /F /PID %%a >nul 2>&1
)

if not exist "muse_router_core\muse_router_core.dll" (
    echo [CORE] Dang bien dich muse_router_core.dll...
    pushd muse_router_core
    cargo build --release
    copy /Y target\release\muse_router_core.dll muse_router_core.dll >nul
    popd
)

if not exist "muse_router_engine\muse_router.exe" (
    echo [ENGINE] Dang bien dich muse_router.exe...
    pushd muse_router_engine
    go build -o muse_router.exe .
    popd
)

if not exist "muse_router_ui\bin\Release\MuseRouter.exe" (
    echo [UI] Dang bien dich MuseRouter.exe...
    pushd muse_router_ui
    dotnet build -c Release -o bin\Release
    popd
)

echo [1/3] Khoi dong Python AI Bridge — Primp TLS ^& Playwright Chromium (127.0.0.1:18089)...
start "MuseRouter AI Bridge" /min cmd /c "cd /d "%~dp0muse_router_ai" && python bridge_service.py"

echo [2/3] Khoi dong Go Engine ^& Web Studio (http://127.0.0.1:8088)...
start "MuseRouter Go Engine" /min cmd /c "cd /d "%~dp0muse_router_engine" && muse_router.exe"

timeout /t 2 /nobreak >nul

echo [3/3] Mo trinh duyet Web Tool tai http://127.0.0.1:8088 ...
start "" "http://127.0.0.1:8088"

if "%1"=="--with-desktop" (
    start "" "%~dp0muse_router_ui\bin\Release\MuseRouter.exe"
)

echo.
echo [HOAN TAT] MuseRouter Web Tool dang chay tai: http://127.0.0.1:8088
echo            OpenAI-Compatible Gateway:        http://127.0.0.1:8088/v1/chat/completions
