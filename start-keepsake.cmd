@echo off
rem Starts the Keepsake server on the LAN so phones on your Wi-Fi can use it.
rem Web UI + API on port 8899; media root comes from your saved config (D:\MediaVault).
cd /d "%~dp0"
echo Keepsake starting... open http://192.168.68.62:8899 on your phone (same Wi-Fi).
mediavaultd.exe -addr 0.0.0.0:8899 -ui frontend\dist
pause
