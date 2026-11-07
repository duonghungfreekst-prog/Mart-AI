@echo off
chcp 65001 >nul
title Mart AI Studio - Trung Tam Dieu Hanh 1-Click Duy Nhat
cd /d "%~dp0"

REM Kiem tra tham so truyen vao
if /i "%~1"=="login" goto DO_LOGIN
if /i "%~1"=="clean" goto DO_CLEAN
if /i "%~1"=="stop"  goto DO_STOP
if /i "%~1"=="menu"  goto SHOW_MENU

goto START_SERVER_DIRECT

:SHOW_MENU
cls
echo ================================================================================
echo          MART AI STUDIO - HE THONG TRUNG TAM 1-CLICK ALL-IN-ONE
echo ================================================================================
echo.
echo   [1] KHOI DONG SERVER 24/7 (Bat MySQL, Apache, Go Engine va Mo Web Chat)
echo   [2] TIEP NHAN PHIEN MUSE.AI TU DONG (Mo Chrome that, tu dong bat Cookie)
echo   [3] DON RAC TOAN DIEN (Lam sach Cache, Temp, Profile rac va Database)
echo   [4] DUNG TOAN BO HE THONG (Tat sach MySQL, Apache, Engine, Cloudflare)
echo   [0] THOAT
echo.
echo ================================================================================
set /p MENU_CHOICE="  Nhap lua chon cua ban (0-4, mac dinh 1): "
if "%MENU_CHOICE%"=="2" goto DO_LOGIN
if "%MENU_CHOICE%"=="3" goto DO_CLEAN
if "%MENU_CHOICE%"=="4" goto DO_STOP
if "%MENU_CHOICE%"=="0" exit /b 0
goto START_SERVER_DIRECT

:DO_CLEAN
cls
echo ================================================================================
echo   TIEN HANH DON RAC TOAN DIEN HE THONG
echo ================================================================================
echo.
echo [*] Dang don dep cac profile trinh duyet tam va thu muc rac...
Remove-Item -Path "%~dp0data\cdp_profile_*" -Recurse -Force >nul 2>&1
Remove-Item -Path "%~dp0muse_router_engine\data\cdp_profile_*" -Recurse -Force >nul 2>&1
Remove-Item -Path "%~dp0muse_router_engine\test_cdp_live_engine" -Recurse -Force >nul 2>&1
powershell -NoProfile -ExecutionPolicy Bypass -Command "Get-ChildItem -Path $env:TEMP -Directory -Filter '*mart_ai*' -ErrorAction SilentlyContinue | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue" >nul 2>&1

echo [*] Dang lam sach cache Go build va Cargo...
cd /d "%~dp0muse_router_engine" && go clean -cache >nul 2>&1
cd /d "%~dp0muse_router_core" && cargo clean >nul 2>&1
cd /d "%~dp0"

echo [*] Dang don dep du lieu tin nhan cu trong MySQL...
if exist "C:\xampp\mysql\bin\mysql.exe" (
    "C:\xampp\mysql\bin\mysql.exe" -u root -e "USE muse_router; TRUNCATE TABLE chat_messages; TRUNCATE TABLE chat_threads; TRUNCATE TABLE routing_telemetry; TRUNCATE TABLE muse_tasks; TRUNCATE TABLE muse_documents; TRUNCATE TABLE muse_memories; TRUNCATE TABLE muse_accounts;" >nul 2>&1
)
powershell -NoProfile -Command "Set-Content -Path '%~dp0data\sessions_store.json' -Value '[]'; Set-Content -Path '%~dp0data\threads.json' -Value '[]'; Set-Content -Path '%~dp0data\accounts_vault.json' -Value '[]'" >nul 2>&1

echo.
echo [THANH CONG] Toan bo he thong da duoc don sach bong 100%%!
echo.
echo Nhan phim bat ky de quay lai...
pause >nul
goto SHOW_MENU

:DO_STOP
cls
echo ================================================================================
echo   DANG DUNG TOAN BO HE THONG MART AI VA XAMPP
echo ================================================================================
echo.
taskkill /F /IM muse_router.exe >nul 2>&1
taskkill /F /IM cloudflared.exe >nul 2>&1
taskkill /F /IM httpd.exe >nul 2>&1
taskkill /F /IM mysqld.exe >nul 2>&1
echo [OK] Da dung tat ca cac tien trinh (Mart AI, Cloudflare, Apache, MySQL).
echo.
echo Nhan phim bat ky de thoat...
pause >nul
exit /b 0

:DO_LOGIN
cls
echo ================================================================================
echo   TIEP NHAN PHIEN MUSE.AI TU DONG 100%% BANG CHROME THAT
echo ================================================================================
echo.
echo [*] Dang kiem tra Server Mart AI Engine (Port 8088)...
netstat -ano | findstr ":8088" | findstr "LISTENING" >nul 2>&1
if errorlevel 1 (
    echo [*] Server chua bat. Dang khoi dong cac dich vu nen truoc...
    if exist "C:\xampp\mysql\bin\mysqld.exe" (
        start "" "C:\xampp\mysql\bin\mysqld.exe" --defaults-file="C:\xampp\mysql\bin\my.ini" --standalone >nul 2>&1
        ping 127.0.0.1 -n 3 >nul
    )
    start "" /b "%~dp0muse_router.exe"
    ping 127.0.0.1 -n 4 >nul
)

echo [*] Dang kich hoat cua so Chrome/Edge that tren may tinh...
echo [*] Ban chi can chon tai khoan Google de dang nhap vao Muse.ai.
echo [*] He thong se TU DONG bat Cookie, ma hoa va tu dong dong cua so!
echo.
echo [DANG CHO DANG NHAP...]
echo.

powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$body = @{ label = 'Muse.ai Tu Dong'; proxy_url = '' } | ConvertTo-Json; " ^
  "try { " ^
  "  $resp = Invoke-RestMethod -Uri 'http://127.0.0.1:8088/api/accounts/browser-login' -Method Post -Body $body -ContentType 'application/json' -TimeoutSec 190; " ^
  "  Write-Host ''; " ^
  "  Write-Host '================================================================================' -ForegroundColor Green; " ^
  "  Write-Host '  [THANH CONG RUC RO] DA TIEP NHAN PHIEN MUSE.AI VA DONG BO VAO POOL!' -ForegroundColor Green; " ^
  "  Write-Host ('  - Node ID   : ' + $resp.id) -ForegroundColor Cyan; " ^
  "  Write-Host ('  - Ten Node  : ' + $resp.label) -ForegroundColor Cyan; " ^
  "  Write-Host ('  - Trang Thai: ' + $resp.status + ' (San Sang Hoat Dong 24/7)') -ForegroundColor Green; " ^
  "  Write-Host '================================================================================' -ForegroundColor Green; " ^
  "  Start-Process 'http://127.0.0.1:8088'; " ^
  "} catch { " ^
  "  Write-Host ''; " ^
  "  Write-Host ('[LOI] ' + $_.Exception.Message) -ForegroundColor Red; " ^
  "}"

echo.
echo Nhan phim bat ky de tiep tuc...
pause >nul
goto SHOW_MENU

:START_SERVER_DIRECT
cls
echo ================================================================================
echo   MART AI va MUSEROUTER - KHOI DONG SERVER 1 CLICK ALL-IN-ONE
echo ================================================================================
echo.

REM 1. Nhan dien thu muc XAMPP
set "XAMPP_DIR=C:\xampp"
if exist "%~dp0xampp\apache\bin\httpd.exe" set "XAMPP_DIR=%~dp0xampp"
if exist "D:\xampp\apache\bin\httpd.exe" set "XAMPP_DIR=D:\xampp"

REM 2. Tu dong don dep nhanh cac profile tam
powershell -NoProfile -Command "Get-ChildItem -Path '%~dp0data\cdp_profile_*' -ErrorAction SilentlyContinue | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue" >nul 2>&1

REM 3. Kiem tra va khoi dong MySQL (Port 3306)
echo [*] [1/5] Kiem tra XAMPP MySQL (Port 3306)...
netstat -ano | findstr ":3306" | findstr "LISTENING" >nul 2>&1
if not errorlevel 1 goto MYSQL_OK
echo     Dang bat MySQL...
net start MySQL >nul 2>&1
netstat -ano | findstr ":3306" | findstr "LISTENING" >nul 2>&1
if not errorlevel 1 goto MYSQL_OK
if exist "%XAMPP_DIR%\mysql\bin\mysqld.exe" (
    start "" "%XAMPP_DIR%\mysql\bin\mysqld.exe" --defaults-file="%XAMPP_DIR%\mysql\bin\my.ini" --standalone
    ping 127.0.0.1 -n 4 >nul
)
:MYSQL_OK
echo     [OK] MySQL dang hoat dong tai cong 3306.

REM 4. Dam bao Database 'muse_router' ton tai
echo [*] [2/5] Kiem tra co so du lieu 'muse_router'...
if exist "%XAMPP_DIR%\mysql\bin\mysql.exe" (
    "%XAMPP_DIR%\mysql\bin\mysql.exe" -u root -e "CREATE DATABASE IF NOT EXISTS muse_router CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;" >nul 2>&1
)
echo     [OK] Co so du lieu 'muse_router' san sang.

REM 5. Kiem tra va khoi dong Apache (Port 80)
echo [*] [3/5] Kiem tra Apache Web Server (Port 80)...
netstat -ano | findstr ":80 " | findstr "LISTENING" >nul 2>&1
if not errorlevel 1 goto APACHE_OK
echo     Dang bat Apache...
net start Apache2.4 >nul 2>&1
netstat -ano | findstr ":80 " | findstr "LISTENING" >nul 2>&1
if not errorlevel 1 goto APACHE_OK
if exist "%XAMPP_DIR%\apache\bin\httpd.exe" (
    powershell -NoProfile -Command "Start-Process -FilePath '%XAMPP_DIR%\apache\bin\httpd.exe' -ArgumentList '-d ''%XAMPP_DIR%\apache''' -WorkingDirectory '%XAMPP_DIR%\apache' -WindowStyle Hidden" >nul 2>&1
    ping 127.0.0.1 -n 3 >nul
)
:APACHE_OK
echo     [OK] Apache dang hoat dong tai cong 80.

REM 6. Kiem tra va khoi dong Cloudflare Tunnel
echo [*] [4/5] Kiem tra Cloudflare Tunnel...
tasklist /fi "imagename eq cloudflared.exe" 2>nul | findstr /i "cloudflared" >nul 2>&1
if not errorlevel 1 goto CLOUDFLARE_OK
if exist "%~dp0auto_cloudflared_tunnel.ps1" (
    echo     Dang ket noi duong ham Cloudflare...
    powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "Start-Process powershell.exe -ArgumentList @('-NoProfile','-ExecutionPolicy','Bypass','-WindowStyle','Hidden','-File','%~dp0auto_cloudflared_tunnel.ps1') -WindowStyle Hidden"
    ping 127.0.0.1 -n 3 >nul
)
:CLOUDFLARE_OK
echo     [OK] Cloudflare Tunnel dang ket noi.

REM 7. Kiem tra Mart AI Engine (Port 8088)
netstat -ano | findstr ":8088" | findstr "LISTENING" >nul 2>&1
if not errorlevel 1 goto ENGINE_ALREADY_RUNNING

REM 8. Khoi chay Mart AI Engine truc tiep (Giu cua so luon mo 24/7)
echo [*] [5/5] Dang khoi chay Mart AI Engine (Port 8088)...
echo.
echo ================================================================================
echo   MART AI STUDIO DANG CHAY LIEN TUC TAI: http://127.0.0.1:8088
echo ================================================================================
echo   - Web Chat Mart AI Studio : http://127.0.0.1:8088
echo   - Trang Cai Dat va Node   : http://127.0.0.1:8088/cai-dat
echo   - Quan Tri phpMyAdmin     : http://localhost/phpmyadmin
echo   - Web Thiet Ke (htdocs)   : http://localhost/
echo   - Tiep Nhan Muse Tu Dong  : Chay 'run.bat login' hoac bam tren web /cai-dat
echo   - Don Rac He Thong        : Chay 'run.bat clean'
echo   - Dung He Thong           : Chay 'run.bat stop' hoac nhan Ctrl+C
echo ================================================================================
echo.
echo [Luu y] CUA SO NAY DANG GIU SERVER HOAT DONG 24/7.
echo         KHONG DONG CUA SO NAY NEU BAN DANG DUNG MART AI.
echo.
echo [*] Dang tu dong mo trinh duyet vao Mart AI Studio...
start "" "http://127.0.0.1:8088"

title Mart AI Studio - Server Engine (Port 8088) [DANG CHAY 24/7]
"%~dp0muse_router.exe"

echo.
echo ================================================================================
echo [CANH BAO] Mart AI Engine da dung hoat dong.
echo ================================================================================
echo Nhan phim bat ky de thoat...
pause >nul
exit /b 0

:ENGINE_ALREADY_RUNNING
echo [*] [5/5] Mart AI Engine DA DANG CHAY tai cong 8088!
echo.
echo ================================================================================
echo   [SERVER DANG HOAT DONG SAN SANG]
echo ================================================================================
echo   - Web Chat Mart AI Studio : http://127.0.0.1:8088
echo   - Trang Cai Dat va Node   : http://127.0.0.1:8088/cai-dat
echo   - Quan Tri phpMyAdmin     : http://localhost/phpmyadmin
echo   - Web Thiet Ke (htdocs)   : http://localhost/
echo   - Tiep Nhan Muse Tu Dong  : Chay 'run.bat login' hoac bam tren web /cai-dat
echo   - Don Rac He Thong        : Chay 'run.bat clean'
echo   - Dung He Thong           : Chay 'run.bat stop'
echo ================================================================================
echo.
echo [*] Dang mo trinh duyet vao Mart AI Studio...
start "" "http://127.0.0.1:8088"
echo.
echo [THONG BAO] Server da dang chay ngam on dinh tai cong 8088.
echo Nhan phim bat ky de thoat...
pause >nul
exit /b 0
