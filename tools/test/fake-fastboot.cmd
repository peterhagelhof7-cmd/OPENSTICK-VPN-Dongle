@powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0fake-fastboot.ps1" %*
@exit /b %ERRORLEVEL%
