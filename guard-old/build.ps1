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

Write-Host "Compilación completada exitosamente!" -ForegroundColor Green
Write-Host "Ejecutables generados:" -ForegroundColor Cyan
Write-Host "  - guard-login.exe" -ForegroundColor Cyan
Write-Host "  - guard-game.exe" -ForegroundColor Cyan
