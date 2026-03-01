@echo off
echo === GUARD_GO VPS1 - Estado y Reparacion Automatica ===
echo Iniciando con permisos de Administrador...
powershell -Command "Start-Process powershell -Verb RunAs -ArgumentList '-ExecutionPolicy Bypass -NoExit -File ""%~dp0_estado.ps1""'"
