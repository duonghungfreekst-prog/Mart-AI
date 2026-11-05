@echo off
chcp 65001 >nul

:: Tu dong kiem tra va xin quyen Administrator (UAC)
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo [*] Dang xin quyen Administrator...
    powershell -NoProfile -Command "Start-Process cmd.exe -ArgumentList '/c \"\"\"%~dpnx0\"\"\"' -Verb RunAs"
    exit /b
)

:: ============================================================================
::  MUSEROUTER & XAMPP - TRUNG TAM CAI DAT VA QUAN LY TAT-CA-TRONG-MOT (ALL-IN-ONE)
::  Tu dong nhan dien: XAMPP tich hop san (%~dp0xampp) HOAC XAMPP goc (C:\xampp)
:: ============================================================================

cd /d "%~dp0"
set WORK_DIR=%~dp0

:: Tu dong nhan dien thu muc XAMPP (uu tien goi portable di kem)
set XAMPP_DIR=C:\xampp
if exist "%~dp0xampp\apache\bin\httpd.exe" (
    set XAMPP_DIR=%~dp0xampp
)

set HTDOCS_DIR=%XAMPP_DIR%\htdocs
set SILENT_VBS=%~dp0muse_router_silent.vbs
set BACKUP_DIR=%~dp0data\update_backup

:: Neu co tham so dong lenh goi truc tiep
if "%1"=="1" goto START_ALL
if "%1"=="2" goto STOP_ALL

:MENU
cls
echo ================================================================================
echo      MUSEROUTER ^& XAMPP - TRUNG TAM DIEU KHIEN HE THONG (ALL-IN-ONE)
echo ================================================================================
echo   Thu muc XAMPP su dung: %XAMPP_DIR%
echo ================================================================================

:: Kiem tra trang thai truc tiep
set APACHE_STATUS=[DA TAT]
set MYSQL_STATUS=[DA TAT]
set MUSEROUTER_STATUS=[DA TAT]
set TUNNEL_STATUS=[DA TAT]

netstat -ano | findstr ":80 " | findstr "LISTENING" >nul 2>&1
if %ERRORLEVEL%==0 set APACHE_STATUS=[DANG CHAY - PORT 80]

netstat -ano | findstr ":3306" | findstr "LISTENING" >nul 2>&1
if %ERRORLEVEL%==0 set MYSQL_STATUS=[DANG CHAY - PORT 3306]

netstat -ano | findstr ":8088" | findstr "LISTENING" >nul 2>&1
if %ERRORLEVEL%==0 set MUSEROUTER_STATUS=[DANG CHAY - PORT 8088]

tasklist /fi "imagename eq cloudflared.exe" 2>nul | findstr /i "cloudflared" >nul 2>&1
if %ERRORLEVEL%==0 set TUNNEL_STATUS=[DANG CHAY - DA CAP LINK]

echo.
echo   TRANG THAI DICH VU:
echo   - Apache Web Server (Chay Web Thiet Ke) : %APACHE_STATUS%
echo   - XAMPP MySQL       (Co So Du Lieu)     : %MYSQL_STATUS%
echo   - MuseRouter Engine (Web Studio Chinh)  : %MUSEROUTER_STATUS%
echo   - Cloudflare Tunnel (Duong Ham Khach)   : %TUNNEL_STATUS%
echo.
echo ================================================================================
echo   CHUC NANG CHINH:
echo.
echo   [1] KICH HOAT TAT CA TRONG 1 CLICK  (Bat Apache + MySQL + MuseRouter + DB)
echo   [2] DUNG TAT CA DICH VU             (Tat toan bo he thong)
echo   [3] CAI DAT TU KHOI DONG CUNG WINDOWS (Tu chay ngam moi khi mo may)
echo   [4] GO BO KHOI DONG CUNG WINDOWS
echo.
echo   TIEN ICH THIET KE ^& DU LIEU:
echo.
echo   [5] MO THU MUC THIET KE WEB         (Mo thu muc htdocs de tha file web vao)
echo   [6] MO PHPMYADMIN ^& WEB ROUTER      (Mo trang quan tri MySQL va Web Studio)
echo   [7] MO BANG DIEU KHIEN XAMPP GOC    (Mo xampp-control.exe)
echo   [8] CAP NHAT HE THONG QUA FILE .ZIP (Update code khong mat du lieu)
echo   [9] BAT DUONG HAM CLOUDFLARE        (Lay link HTTPS cho khach truy cap tu xa)
echo.
echo   [0] Thoat
echo ================================================================================
set /p OPT=  Lua chon cua ban (0-9): 

if "%OPT%"=="1" goto START_ALL
if "%OPT%"=="2" goto STOP_ALL
if "%OPT%"=="3" goto AUTO_START_INSTALL
if "%OPT%"=="4" goto AUTO_START_REMOVE
if "%OPT%"=="5" goto OPEN_HTDOCS
if "%OPT%"=="6" goto OPEN_WEB
if "%OPT%"=="7" goto OPEN_XAMPP_CONTROL
if "%OPT%"=="8" goto UPDATE_ZIP
if "%OPT%"=="9" goto START_TUNNEL
if "%OPT%"=="0" goto EXIT_PROG
goto MENU

:: ============================================================
:START_ALL
:: ============================================================
echo.
echo ============================================================================
echo   [1/4] Kiem tra va khoi dong XAMPP MySQL...
echo ============================================================================
net start MySQL >nul 2>&1
netstat -ano | findstr ":3306" | findstr "LISTENING" >nul 2>&1
if errorlevel 1 (
    if exist "%XAMPP_DIR%\mysql\bin\mysqld.exe" (
        start "" "%XAMPP_DIR%\mysql\bin\mysqld.exe" --defaults-file="%XAMPP_DIR%\mysql\bin\my.ini" --standalone
        timeout /t 3 /nobreak >nul
    )
)
echo [OK] MySQL dang hoat dong (cong 3306).

echo.
echo ============================================================================
echo   [2/4] Tu dong khoi tao Database 'muse_router'...
echo ============================================================================
if exist "%XAMPP_DIR%\mysql\bin\mysql.exe" (
    "%XAMPP_DIR%\mysql\bin\mysql.exe" -u root -e "CREATE DATABASE IF NOT EXISTS muse_router CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;" >nul 2>&1
    echo [OK] Database 'muse_router' da san sang.
)

echo.
echo ============================================================================
echo   [3/4] Kiem tra va khoi dong Apache Web Server...
echo ============================================================================
net start Apache2.4 >nul 2>&1
netstat -ano | findstr ":80 " | findstr "LISTENING" >nul 2>&1
if errorlevel 1 (
    if exist "%XAMPP_DIR%\apache\bin\httpd.exe" (
        powershell -NoProfile -Command "Start-Process -FilePath '%XAMPP_DIR%\apache\bin\httpd.exe' -ArgumentList '-d ''%XAMPP_DIR%\apache''' -WorkingDirectory '%XAMPP_DIR%\apache' -WindowStyle Hidden" >nul 2>&1
        timeout /t 2 /nobreak >nul
    )
)
echo [OK] Apache dang hoat dong (cong 80).

echo.
echo ============================================================================
echo   [4/5] Khoi dong MuseRouter Engine (Chay ngam)...
echo ============================================================================
if exist "%SILENT_VBS%" (
    wscript.exe "%SILENT_VBS%"
    timeout /t 3 /nobreak >nul
)
echo [OK] MuseRouter Engine dang hoat dong (cong 8088).

echo.
echo ============================================================================
echo   [5/5] Tu dong khoi dong Cloudflare Tunnel (Tao link HTTPS cho khach)...
echo ============================================================================
if exist "%WORK_DIR%auto_cloudflared_tunnel.ps1" (
    powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "Start-Process powershell.exe -ArgumentList @('-NoProfile','-ExecutionPolicy','Bypass','-WindowStyle','Hidden','-File','%WORK_DIR%auto_cloudflared_tunnel.ps1') -WindowStyle Hidden"
    timeout /t 5 /nobreak >nul
)

echo.
echo ============================================================================
echo   [HOAN TAT] TAT CA DICH VU DA SAN SANG!
echo   - Web MuseRouter Studio : http://127.0.0.1:8088
echo   - Web Thiet Ke (htdocs) : http://localhost/
echo   - Quan Tri phpMyAdmin   : http://localhost/phpmyadmin
if exist "%WORK_DIR%LINK_KHACH_TRUY_CAP.txt" (
    echo.
    type "%WORK_DIR%LINK_KHACH_TRUY_CAP.txt"
)
echo ============================================================================
echo.
set /p OPEN_BROWSER=Ban co muon mo Web ngay bay gio khong? (Y/N): 
if /i "%OPEN_BROWSER%"=="Y" (
    start "" "http://127.0.0.1:8088"
)
if "%1"=="" pause
if "%1"=="" goto MENU
exit /b 0

:: ============================================================
:STOP_ALL
:: ============================================================
echo.
echo [*] Dang dung tat ca dich vu...
taskkill /F /IM muse_router.exe >nul 2>&1
taskkill /F /IM python.exe >nul 2>&1
taskkill /F /IM cloudflared.exe >nul 2>&1
net stop Apache2.4 >nul 2>&1
taskkill /F /IM httpd.exe >nul 2>&1
net stop MySQL >nul 2>&1
taskkill /F /IM mysqld.exe >nul 2>&1
timeout /t 2 /nobreak >nul
echo [OK] Da dung toan bo Apache, MySQL, Cloudflare Tunnel va MuseRouter.
echo.
if "%1"=="" pause
if "%1"=="" goto MENU
exit /b 0

:: ============================================================
:AUTO_START_INSTALL
:: ============================================================
echo.
echo [*] Dang cai dat che do tu khoi dong cung Windows...
sc config Apache2.4 start= auto >nul 2>&1
sc config MySQL start= auto >nul 2>&1
schtasks /create /tn "MuseRouter" /tr "wscript.exe \"%SILENT_VBS%\"" /sc onlogon /rl highest /f >nul 2>&1
echo.
echo [OK] Da cai dat thanh cong! Tu gio moi khi bat may tinh se tu chay ngam.
echo.
pause
goto MENU

:: ============================================================
:AUTO_START_REMOVE
:: ============================================================
echo.
echo [*] Dang go bo che do tu khoi dong...
sc config Apache2.4 start= demand >nul 2>&1
sc config MySQL start= demand >nul 2>&1
schtasks /delete /tn "MuseRouter" /f >nul 2>&1
echo [OK] Da go bo khoi dong cung Windows.
echo.
pause
goto MENU

:: ============================================================
:OPEN_HTDOCS
:: ============================================================
echo.
echo [*] Dang mo thu muc thiet ke: %HTDOCS_DIR% ...
if exist "%HTDOCS_DIR%" (
    explorer "%HTDOCS_DIR%"
    echo [OK] Da mo thu muc htdocs trong File Explorer.
) else (
    echo [LOI] Khong tim thay thu muc: %HTDOCS_DIR%
)
echo.
pause
goto MENU

:: ============================================================
:OPEN_WEB
:: ============================================================
echo.
echo [*] Dang mo trinh duyet...
start "" "http://127.0.0.1:8088"
start "" "http://localhost/phpmyadmin"
pause
goto MENU

:: ============================================================
:OPEN_XAMPP_CONTROL
:: ============================================================
echo.
echo [*] Dang mo Bang dieu khien XAMPP Control Panel...
if exist "%XAMPP_DIR%\xampp-control.exe" (
    start "" "%XAMPP_DIR%\xampp-control.exe"
) else (
    echo [LOI] Khong tim thay %XAMPP_DIR%\xampp-control.exe
)
pause
goto MENU

:: ============================================================
:UPDATE_ZIP
:: ============================================================
cls
echo ================================================================================
echo    CAP NHAT HE THONG QUA FILE NEN (.ZIP)
echo ================================================================================
echo.
set ZIP_PATH=
set /p ZIP_PATH=  Nhap duong dan file .zip (hoac keo tha file vao day): 
set ZIP_PATH=%ZIP_PATH:"=%

if "%ZIP_PATH%"=="" (
    echo [LOI] Chua nhap duong dan file. Huy.
    pause
    goto MENU
)

if not exist "%ZIP_PATH%" (
    echo [LOI] Khong tim thay file: %ZIP_PATH%
    pause
    goto MENU
)

echo.
echo [*] File zip: %ZIP_PATH%
set /p CONFIRM=  Xac nhan cap nhat? (Y/N): 
if /i not "%CONFIRM%"=="Y" (
    echo [HUY] Da huy cap nhat.
    pause
    goto MENU
)

echo.
echo [1/4] Dung he thong cu...
taskkill /F /IM muse_router.exe >nul 2>&1
taskkill /F /IM python.exe >nul 2>&1
timeout /t 2 /nobreak >nul

echo [2/4] Sao luu ban hien tai...
for /f "tokens=1-4 delims=/ " %%a in ("%date%") do set DATESTAMP=%%a%%b%%c%%d
for /f "tokens=1-3 delims=:." %%a in ("%time: =0%") do set TIMESTAMP=%%a%%b%%c
set BACKUP_SLOT=%BACKUP_DIR%\backup_%DATESTAMP%_%TIMESTAMP%
if not exist "%BACKUP_DIR%" mkdir "%BACKUP_DIR%" >nul

powershell -NoProfile -Command ^
    "$dst = '%BACKUP_SLOT%'; New-Item -ItemType Directory -Force -Path $dst | Out-Null; ^
    @('muse_router.exe','muse_router_engine','muse_router_ai','muse_router_core','muse_router_ui') | ForEach-Object { ^
        $f = Join-Path '%WORK_DIR%' $_; if (Test-Path $f) { Copy-Item $f -Destination $dst -Recurse -Force } ^
    }"

echo [3/4] Giai nen file cap nhat...
powershell -NoProfile -Command ^
    "try { ^
        Add-Type -AssemblyName System.IO.Compression.FileSystem; ^
        $zip = [System.IO.Compression.ZipFile]::OpenRead('%ZIP_PATH%'); ^
        foreach ($entry in $zip.Entries) { ^
            if ($entry.FullName -match '\\\\$' -or $entry.FullName -match '/$') { continue }; ^
            $target = Join-Path '%WORK_DIR%' $entry.FullName; ^
            $dir = Split-Path $target -Parent; ^
            if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }; ^
            [System.IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $target, $true); ^
        }; ^
        $zip.Dispose(); ^
        Write-Host '[OK] Giai nen hoan tat.'; ^
    } catch { Write-Host '[LOI] ' + $_.Exception.Message; exit 1 }"

if errorlevel 1 (
    echo [LOI] Giai nen that bai! Dang phuc hoi ban backup...
    powershell -NoProfile -Command "Copy-Item -Path '%BACKUP_SLOT%\*' -Destination '%WORK_DIR%' -Recurse -Force"
    echo [OK] Da phuc hoi ban cu.
)

echo [4/4] Khoi dong lai he thong...
wscript.exe "%SILENT_VBS%"
timeout /t 3 /nobreak >nul
echo.
echo ================================================================================
echo  Cap nhat thanh cong! Web: http://127.0.0.1:8088
echo ================================================================================
echo.
pause
goto MENU

:: ============================================================
:START_TUNNEL
:: ============================================================
echo.
if exist "%WORK_DIR%LINK_KHACH_TRUY_CAP.txt" (
    type "%WORK_DIR%LINK_KHACH_TRUY_CAP.txt"
    echo.
    echo [OK] Link tren da duoc copy vao Clipboard. Ban co the gui ngay cho khach.
) else (
    echo [*] Dang tao duong ham Cloudflare moi...
    if exist "%~dp0BAT_DUONG_HAM_CLOUDFLARE.bat" (
        start "" "%~dp0BAT_DUONG_HAM_CLOUDFLARE.bat"
    )
)
echo.
pause
goto MENU

:: ============================================================
:EXIT_PROG
:: ============================================================
exit /b 0
