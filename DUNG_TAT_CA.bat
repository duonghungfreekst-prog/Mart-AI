@echo off
chcp 65001 >nul
cd /d "%~dp0"
taskkill /F /IM cloudflared.exe >nul 2>&1
call CAI_DAT_VA_QUAN_LY_ALL_IN_ONE.bat 2
