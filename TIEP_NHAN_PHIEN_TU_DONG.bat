@echo off
chcp 65001 >nul
title Mart AI - Tiep Nhan Phien Muse.ai Tu Dong
cd /d "%~dp0"

echo.
echo ================================================================================
echo   MART AI STUDIO - TIEP NHAN PHIEN MUSE.AI TU DONG 100%%
echo ================================================================================
echo.
echo [*] Dang kiem tra Mart AI Engine (Port 8088)...
netstat -ano | findstr ":8088" | findstr "LISTENING" >nul 2>&1
if errorlevel 1 (
    echo [*] Engine chua bat. Dang khoi dong toan bo he thong (XAMPP va Engine)...
    call "%~dp0run.bat"
    ping 127.0.0.1 -n 4 >nul
)

echo [*] Dang kich hoat trinh duyet Chrome/Edge de tiep nhan phien tu dong...
echo [*] Mot cua so trinh duyet se duoc bat len ngay sau day.
echo [*] Ban chi can chon tai khoan Google de dang nhap vao Muse.ai.
echo [*] He thong se TU DONG bat phien, ma hoa chuan va tu dong dong cua so!
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
echo Nhan phim bat ky de ket thuc...
pause >nul
