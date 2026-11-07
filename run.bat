@echo off
chcp 65001 >nul
title Mart AI Studio - He Thong Khoi Dong va Dieu Hanh Server
cd /d "%~dp0"

echo.
echo ================================================================================
echo   MART AI va MUSEROUTER - KHOI DONG SERVER 1 CLICK
echo ================================================================================
echo.

REM 1. Nhan dien thu muc XAMPP
set "XAMPP_DIR=C:\xampp"
if exist "%~dp0xampp\apache\bin\httpd.exe" set "XAMPP_DIR=%~dp0xampp"
if exist "D:\xampp\apache\bin\httpd.exe" set "XAMPP_DIR=D:\xampp"

REM 2. Kiem tra va khoi dong MySQL (Port 3306)
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

REM 3. Dam bao Database 'muse_router' ton tai
echo [*] [2/5] Kiem tra co so du lieu 'muse_router'...
if exist "%XAMPP_DIR%\mysql\bin\mysql.exe" (
    "%XAMPP_DIR%\mysql\bin\mysql.exe" -u root -e "CREATE DATABASE IF NOT EXISTS muse_router CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;" >nul 2>&1
)
echo     [OK] Co so du lieu 'muse_router' san sang.

REM 4. Kiem tra va khoi dong Apache (Port 80)
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

REM 5. Kiem tra va khoi dong Cloudflare Tunnel
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

REM 6. Kiem tra Mart AI Engine (Port 8088)
netstat -ano | findstr ":8088" | findstr "LISTENING" >nul 2>&1
if not errorlevel 1 goto ENGINE_ALREADY_RUNNING

REM 7. Khoi chay Mart AI Engine truc tiep (Giu cua so luon mo)
echo [*] [5/5] Dang khoi chay Mart AI Engine (Port 8088)...
echo.
echo ================================================================================
echo   MART AI STUDIO DANG CHAY LIEN TUC TAI: http://127.0.0.1:8088
echo ================================================================================
echo   - Web Chat Mart AI Studio : http://127.0.0.1:8088
echo   - Trang Cai Dat va Node   : http://127.0.0.1:8088/cai-dat
echo   - Quan Tri phpMyAdmin     : http://localhost/phpmyadmin
echo   - Web Thiet Ke (htdocs)   : http://localhost/
echo   - Tiep Nhan Muse Tu Dong  : Chay file TIEP_NHAN_PHIEN_TU_DONG.bat
echo ================================================================================
echo.
echo [Luu y] CUA SO NAY DANG GIU SERVER HOAT DONG 24/7.
echo         KHONG DONG CUA SO NAY NEU BAN DANG DUNG MART AI.
echo         De dung server, hay nhan to hop phim Ctrl + C.
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
echo   - Tiep Nhan Muse Tu Dong  : Chay file TIEP_NHAN_PHIEN_TU_DONG.bat
echo ================================================================================
echo.
echo [*] Dang mo trinh duyet vao Mart AI Studio...
start "" "http://127.0.0.1:8088"
echo.
echo [THONG BAO] Server da dang chay ngam on dinh tai cong 8088.
echo Nhan phim bat ky de thoat...
pause >nul
exit /b 0
