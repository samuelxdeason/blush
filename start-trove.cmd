@echo off
setlocal
rem Starts the trove server on the LAN so phones on your Wi-Fi can use it.
rem Web UI + API share one port; media root comes from your saved config.
set PORT=8008

cd /d "%~dp0"

rem Find this machine's LAN address. A DHCP-assigned IPv4 picks out the real
rem network adapter — VPN tunnels (Surfshark, NordLynx) and virtual switches
rem (WSL, VirtualBox) configure their addresses manually, so they're skipped.
rem If nothing is DHCP-assigned (static LAN IP), fall back to the first
rem Wi-Fi/Ethernet adapter. (Both commands avoid quotes: cmd's for /f mangles
rem nested quoting.)
for /f %%i in ('powershell -NoProfile -Command "(Get-NetIPAddress -AddressFamily IPv4 -PrefixOrigin Dhcp -ErrorAction SilentlyContinue | Select-Object -First 1).IPAddress"') do set LANIP=%%i
if not defined LANIP for /f %%i in ('powershell -NoProfile -Command "(Get-NetIPAddress -AddressFamily IPv4 -InterfaceAlias Wi-Fi*,Ethernet* -ErrorAction SilentlyContinue | Select-Object -First 1).IPAddress"') do set LANIP=%%i

echo.
echo   ============================================================
echo     trove is starting
echo.
echo     On this PC:      http://localhost:%PORT%
if defined LANIP (
  echo     On your phone:   http://%LANIP%:%PORT%
  echo                      ^(phone must be on the same Wi-Fi as this PC^)
) else (
  echo     On your phone:   couldn't detect a LAN address.
  echo                      Run "ipconfig", find your IPv4 address, and open
  echo                      http://THAT-ADDRESS:%PORT%
)
echo   ============================================================
echo.

rem Explicit path: this system has cmd's current-directory exe search disabled,
rem so a bare "troved.exe" is not found even with the cd above.
"%~dp0troved.exe" -addr 0.0.0.0:%PORT% -ui "%~dp0frontend\dist"
pause
