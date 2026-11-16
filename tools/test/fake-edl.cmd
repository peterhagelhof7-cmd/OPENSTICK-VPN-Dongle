@powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0fake-edl.ps1" %*
@exit /b %ERRORLEVEL%
