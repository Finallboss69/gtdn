@echo off
net session >nul 2>&1
if %errorlevel% neq 0 (
    powershell "Start-Process cmd -Verb RunAs -ArgumentList '/c ""%~f0""'"
    exit /b
)
echo Reiniciando servicios GuardLogin y GuardGame...
powershell -NoExit -Command "Restart-Service GuardLogin, GuardGame -Force; Start-Sleep 2; Get-Service GuardLogin, GuardGame | Format-Table Name, Status"
