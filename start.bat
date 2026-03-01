@echo off
powercfg /setactive SCHEME_MIN
set GOGC=200
start "GuardLogin" guard-login.exe
start "GuardGame"  guard-game.exe
start "GuardPanel" guard-panel.exe
