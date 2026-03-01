@echo off
echo Deteniendo GUARD RELAY...
taskkill /IM guard-relay.exe /F >nul 2>&1
echo Relay detenido.
timeout /t 2 >nul
