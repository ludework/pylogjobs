@echo off
echo Installing PyLogJobs v2 as Windows Service via NSSM...
echo.
echo Voraussetzung: nssm.exe muss im PATH sein.
echo Download: https://nssm.cc/
echo.
set INSTALL_DIR=%~dp0
nssm install PyLogJobsV2 "%INSTALL_DIR%pylogjobs.exe"
nssm set PyLogJobsV2 AppDirectory "%INSTALL_DIR%"
nssm set PyLogJobsV2 Description "Marquis/Medway Transfer Monitor v2"
nssm set PyLogJobsV2 Start SERVICE_AUTO_START
echo.
echo Service installiert. Starten mit: nssm start PyLogJobsV2
pause
