@echo off
:: ============================================================
::  MuseRouter — Cài đặt / Gỡ cài Windows Service
::  Dùng sc.exe (có sẵn trong Windows, không cần phần mềm thêm)
::  Chạy với quyền Administrator!
:: ============================================================

cd /d "%~dp0"
set SERVICE_NAME=MuseRouter
set EXE_PATH=%~dp0muse_router_engine\muse_router.exe
set DISPLAY_NAME=Muse Router Engine
set DESCRIPTION=MuseRouter - Web Tool da tai khoan Muse.ai, cong 8088

echo.
echo ========================================
echo   MUSE ROUTER — WINDOWS SERVICE SETUP
echo ========================================
echo.
echo [1] Cai dat Service (lan dau)
echo [2] Gỡ cai dat Service
echo [3] Khoi dong Service
echo [4] Dung Service
echo [5] Xem trang thai Service
echo [0] Thoat
echo.
set /p CHOICE=Chon (0-5): 

if "%CHOICE%"=="1" goto INSTALL
if "%CHOICE%"=="2" goto UNINSTALL
if "%CHOICE%"=="3" goto START
if "%CHOICE%"=="4" goto STOP
if "%CHOICE%"=="5" goto STATUS
goto END

:INSTALL
echo.
echo [*] Dang cai dat "%SERVICE_NAME%"...
sc stop "%SERVICE_NAME%" >nul 2>&1
sc delete "%SERVICE_NAME%" >nul 2>&1
sc create "%SERVICE_NAME%" ^
    binPath= "\"%EXE_PATH%\"" ^
    DisplayName= "%DISPLAY_NAME%" ^
    start= auto ^
    obj= LocalSystem
if %ERRORLEVEL%==0 (
    sc description "%SERVICE_NAME%" "%DESCRIPTION%"
    sc failure "%SERVICE_NAME%" reset= 60 actions= restart/5000/restart/10000/restart/30000
    sc start "%SERVICE_NAME%"
    echo.
    echo [OK] Da cai dat va khoi dong thanh cong!
    echo      Truy cap: http://127.0.0.1:8088
) else (
    echo [LOI] Cai dat that bai. Chay lai voi quyen Administrator!
)
goto END

:UNINSTALL
echo.
echo [*] Dang go cai dat "%SERVICE_NAME%"...
sc stop "%SERVICE_NAME%" >nul 2>&1
timeout /t 2 >nul
sc delete "%SERVICE_NAME%"
if %ERRORLEVEL%==0 (
    echo [OK] Da go cai dat service thanh cong.
) else (
    echo [LOI] Go cai dat that bai. Co the service chua duoc cai.
)
goto END

:START
echo.
sc start "%SERVICE_NAME%"
echo [OK] Da gui lenh khoi dong service.
goto END

:STOP
echo.
sc stop "%SERVICE_NAME%"
echo [OK] Da gui lenh dung service.
goto END

:STATUS
echo.
sc query "%SERVICE_NAME%"
goto END

:END
echo.
pause
