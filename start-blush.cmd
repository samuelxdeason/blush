@echo off
rem Starts the blush.xxx server on the LAN so phones on your Wi-Fi can use it.
rem Web UI + API on port 8899; media root comes from your saved config (F:\nsfw).
cd /d "%~dp0"

rem Find this machine's LAN address. DHCP-assigned IPv4 picks out the real network
rem adapter — VPN tunnels (Surfshark, NordLynx) and virtual switches (WSL, VirtualBox)
rem all configure their addresses manually, so they're filtered out.
for /f %%i in ('powershell -NoProfile -Command "(Get-NetIPAddress -AddressFamily IPv4 ^| Where-Object PrefixOrigin -eq 'Dhcp' ^| Select-Object -First 1).IPAddress"') do set LANIP=%%i

if defined LANIP (
  echo blush.xxx starting... open http://%LANIP%:8899 on your phone ^(same Wi-Fi^).
) else (
  echo blush.xxx starting... no DHCP address found - check you're on Wi-Fi/Ethernet.
  echo Run "ipconfig" and open http://YOUR-IP:8899 on your phone.
)
echo.

mediavaultd.exe -addr 0.0.0.0:8899 -ui frontend\dist
pause

