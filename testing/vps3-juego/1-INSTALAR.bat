@echo off
:: Copia los archivos a C:\guard y ejecuta el instalador como Administrador
:: Ejecutar este .bat estando en la misma carpeta que los archivos

echo Elevando permisos e instalando GUARD_GO Panel en VPS3...
powershell -Command "Start-Process powershell -ArgumentList '-NoExit -ExecutionPolicy Bypass -File ""%~dp0instalar.ps1""' -Verb RunAs"
