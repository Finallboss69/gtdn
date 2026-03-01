@echo off
net session >nul 2>&1
if %errorlevel% neq 0 (
    powershell "Start-Process cmd -Verb RunAs -ArgumentList '/c ""%~f0""'"
    exit /b
)
echo Reiniciando servicio GuardPanel...
powershell -NoExit -Command "Restart-Service GuardPanel -Force; Start-Sleep 2; Get-Service GuardPanel | Format-Table Name, Status"
