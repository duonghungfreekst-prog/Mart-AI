@echo off
chcp 65001 >nul
title Cloudflare Tunnel - Duong Ham Ra Ngoai Internet

set CLOUDFLARED="C:\Program Files (x86)\cloudflared\cloudflared.exe"

cls
echo ================================================================================
echo               BAT DUONG HAM CLOUDFLARE CHO KHACH TRUY CAP
echo ================================================================================
echo.
echo   Ban muon mo duong ham cho web nao?
echo.
echo   [1] Web MuseRouter Studio   (Cong 8088 - Mac dinh)
echo   [2] Web Thiet Ke XAMPP      (Cong 80)
echo.
set /p PORT_CHOICE=  Chon (1 hoac 2, mac dinh 1): 

set TARGET_PORT=8088
set WEB_NAME=MuseRouter (Cong 8088)
if "%PORT_CHOICE%"=="2" (
    set TARGET_PORT=80
    set WEB_NAME=XAMPP Web Thiet Ke (Cong 80)
)

cls
echo ================================================================================
echo   DANG KHOI TAO DUONG HAM CHO: %WEB_NAME% ...
echo ================================================================================
echo   Vui long doi 3-5 giay de Cloudflare cap link HTTPS...
echo.

powershell -NoProfile -ExecutionPolicy Bypass -Command ^
    "$cf = 'C:\Program Files (x86)\cloudflared\cloudflared.exe'; ^
    $port = '%TARGET_PORT%'; ^
    $pinfo = New-Object System.Diagnostics.ProcessStartInfo; ^
    $pinfo.FileName = $cf; ^
    $pinfo.Arguments = 'tunnel --url http://127.0.0.1:' + $port; ^
    $pinfo.RedirectStandardError = $true; ^
    $pinfo.UseShellExecute = $false; ^
    $proc = New-Object System.Diagnostics.Process; ^
    $proc.StartInfo = $pinfo; ^
    $proc.Start() | Out-Null; ^
    $linkFound = $false; ^
    while (-not $proc.HasExited) { ^
        $line = $proc.StandardError.ReadLine(); ^
        if ($line -match 'https://[a-zA-Z0-9-]+\.trycloudflare\.com') { ^
            $url = $matches[0]; ^
            if (-not $linkFound) { ^
                $linkFound = $true; ^
                Write-Host '================================================================================' -ForegroundColor Green; ^
                Write-Host '  DUONG HAM CLOUDFLARE DA SAN SANG!' -ForegroundColor Green; ^
                Write-Host '  GUI LINK NAY CHO KHACH HANG TRUY CAP:' -ForegroundColor Yellow; ^
                Write-Host ''; ^
                Write-Host ('  👉  ' + $url) -ForegroundColor Cyan; ^
                Write-Host ''; ^
                Write-Host '================================================================================' -ForegroundColor Green; ^
                Write-Host '  [LUU Y QUAN TRONG]:'; ^
                Write-Host '  - Giu nguyen cua so nay de khach luon truy cap duoc.'; ^
                Write-Host '  - Dong cua so nay lai dong nghia voi viec ngat ket noi duong ham.'; ^
                Write-Host '================================================================================'; ^
                try { Set-Clipboard -Value $url; Write-Host '  (Da tu dong Copy link vao Clipboard cua ban)'; } catch {}; ^
                Write-Host ''; ^
            } ^
        } ^
    }"

pause
