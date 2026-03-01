@echo off
echo Abriendo logs en tiempo real (Ctrl+C para salir)...
echo.
echo [LOG-LOGIN] =====================================
start "Log Login" powershell -NoExit -Command "Get-Content C:\guard\guard-login.log -Wait -Tail 30"
echo [LOG-GAME]  =====================================
start "Log Game"  powershell -NoExit -Command "Get-Content C:\guard\guard-game.log  -Wait -Tail 30"
