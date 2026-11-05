@echo off
:: ============================================================
::  MuseRouter - Service Wrapper (XAMPP MySQL localhost)
::  File nay duoc goi boi Windows Service de set env truoc khi
::  chay muse_router.exe
:: ============================================================

cd /d "%~dp0"

:: --- Tu dong khoi dong Cloudflare Tunnel (Tu dong tao link cho khach) ---
if exist "%~dp0auto_cloudflared_tunnel.ps1" (
    start "" /min powershell.exe -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File "%~dp0auto_cloudflared_tunnel.ps1"
)

cd /d "%~dp0muse_router_engine"

:: --- XAMPP MySQL ---
set MUSE_MYSQL_URL=mysql://root@127.0.0.1:3306/muse_router
set DATABASE_URL=mysql://root@127.0.0.1:3306/muse_router

:: --- Engine config ---
set MUSE_ENGINE_PORT=8088
set MUSE_ENGINE_HOST=127.0.0.1

:: Khoi dong XAMPP MySQL neu chua chay
netstat -ano | findstr ":3306" | findstr "LISTENING" >nul 2>&1
if errorlevel 1 (
    start "" "C:\xampp\mysql\bin\mysqld.exe" --defaults-file="C:\xampp\mysql\bin\my.ini"
    timeout /t 4 /nobreak >nul
)

:: Chay Go Engine
muse_router.exe
