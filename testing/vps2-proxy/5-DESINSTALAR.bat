@echo off
net session >nul 2>&1
if %errorlevel% neq 0 (
    powershell "Start-Process cmd -Verb RunAs -ArgumentList '/c ""%~f0""'"
    exit /b
)
echo ATENCION: Esto va a eliminar los servicios GuardLogin y GuardGame.
pause
powershell -NoExit -Command "Stop-Service GuardLogin -Force -ErrorAction SilentlyContinue; Stop-Service GuardGame -Force -ErrorAction SilentlyContinue; Start-Sleep 1; sc.exe delete GuardLogin; sc.exe delete GuardGame; Remove-NetFirewallRule -DisplayName 'Guard*' -ErrorAction SilentlyContinue; Write-Host 'Servicios eliminados.' -ForegroundColor Yellow"
