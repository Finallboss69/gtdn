@echo off
net session >nul 2>&1
if %errorlevel% neq 0 (
    powershell "Start-Process cmd -Verb RunAs -ArgumentList '/c ""%~f0""'"
    exit /b
)
echo ATENCION: Esto va a eliminar el servicio GuardPanel.
pause
powershell -NoExit -Command "Stop-Service GuardPanel -Force -ErrorAction SilentlyContinue; C:\guard\nssm.exe remove GuardPanel confirm; Remove-NetFirewallRule -DisplayName 'Guard*' -ErrorAction SilentlyContinue; Write-Host 'Servicio eliminado.' -ForegroundColor Yellow"
