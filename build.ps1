# Script de build para compilar ambos ejecutables
# Uso: .\build.ps1

Write-Host "Compilando guard-login.exe..." -ForegroundColor Green
go build -ldflags "-s -w" -o guard-login.exe ./cmd/guard-login
if ($LASTEXITCODE -ne 0) {
    Write-Host "Error compilando guard-login.exe" -ForegroundColor Red
    exit 1
}

Write-Host "Compilando guard-game.exe..." -ForegroundColor Green
go build -ldflags "-s -w" -o guard-game.exe ./cmd/guard-game
if ($LASTEXITCODE -ne 0) {
    Write-Host "Error compilando guard-game.exe" -ForegroundColor Red
    exit 1
}

Write-Host "Compilando guard-panel.exe..." -ForegroundColor Green
go build -ldflags "-s -w" -o guard-panel.exe ./cmd/guard-panel
if ($LASTEXITCODE -ne 0) {
    Write-Host "Error compilando guard-panel.exe" -ForegroundColor Red
    exit 1
}

Write-Host "Compilando guard-relay.exe..." -ForegroundColor Green
go build -ldflags "-s -w" -o guard-relay.exe ./cmd/guard-relay
if ($LASTEXITCODE -ne 0) {
    Write-Host "Error compilando guard-relay.exe" -ForegroundColor Red
    exit 1
}

Write-Host "Compilacion completada exitosamente!" -ForegroundColor Green
Write-Host "Ejecutables generados:" -ForegroundColor Cyan
Write-Host "  - guard-login.exe" -ForegroundColor Cyan
Write-Host "  - guard-game.exe" -ForegroundColor Cyan
Write-Host "  - guard-panel.exe" -ForegroundColor Cyan
Write-Host "  - guard-relay.exe" -ForegroundColor Cyan

# Copiar ejecutables a las carpetas de testing/
Write-Host ""
Write-Host "Copiando ejecutables a testing/..." -ForegroundColor Green
Copy-Item -Path "guard-login.exe" -Destination "testing\vps1-proxy\guard-login.exe" -Force
Copy-Item -Path "guard-game.exe"  -Destination "testing\vps1-proxy\guard-game.exe"  -Force
Copy-Item -Path "guard-login.exe" -Destination "testing\vps2-proxy\guard-login.exe" -Force
Copy-Item -Path "guard-game.exe"  -Destination "testing\vps2-proxy\guard-game.exe"  -Force
Copy-Item -Path "guard-panel.exe" -Destination "testing\vps3-juego\guard-panel.exe" -Force
Write-Host "  testing\vps1-proxy\ -> guard-login.exe, guard-game.exe" -ForegroundColor Cyan
Write-Host "  testing\vps2-proxy\ -> guard-login.exe, guard-game.exe" -ForegroundColor Cyan
Write-Host "  testing\vps3-juego\ -> guard-panel.exe" -ForegroundColor Cyan
Write-Host ""
Write-Host "guard-relay.exe (para distribuir a jugadores) esta en la raiz del proyecto." -ForegroundColor Cyan
