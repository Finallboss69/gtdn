# ============================================================
#  GUARD_GO - Instalador VPS3 / Servidor del juego (45.235.99.117)
#  Ejecutar como Administrador en PowerShell
# ============================================================

if (-NOT ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]"Administrator")) {
    Write-Error "ERROR: Ejecutar PowerShell como Administrador"
    exit 1
}

$dir = "C:\guard"
$src = $PSScriptRoot

# Crear carpeta destino si no existe
Write-Host "Preparando carpeta $dir..." -ForegroundColor Yellow
New-Item -ItemType Directory -Force -Path $dir | Out-Null

# Copiar archivos solo si el origen es distinto al destino
if ($src -ne $dir) {
    Write-Host "Copiando archivos desde $src a $dir..." -ForegroundColor Yellow
    Copy-Item -Path "$src\*" -Destination $dir -Force
} else {
    Write-Host "Archivos ya estan en $dir, sin necesidad de copiar." -ForegroundColor Yellow
}

Write-Host "=== Instalando GUARD_GO Panel en VPS3 ===" -ForegroundColor Cyan

$archivos = @("guard-panel.exe", "nodes.json", "nssm.exe")
foreach ($archivo in $archivos) {
    if (!(Test-Path "$dir\$archivo")) {
        Write-Error "ERROR: Falta $dir\$archivo"
        exit 1
    }
}

# Desinstalar panel viejo si existe
Write-Host "Limpiando instalacion anterior..." -ForegroundColor Yellow
Stop-Service GuardPanel -Force -ErrorAction SilentlyContinue
& "$dir\nssm.exe" remove GuardPanel confirm 2>$null
Start-Sleep 2

# Instalar GuardPanel
Write-Host "Instalando servicio GuardPanel..." -ForegroundColor Green
& "$dir\nssm.exe" install GuardPanel "$dir\guard-panel.exe"
& "$dir\nssm.exe" set GuardPanel AppDirectory  $dir
& "$dir\nssm.exe" set GuardPanel AppParameters "-nodes nodes.json"
& "$dir\nssm.exe" set GuardPanel Start         SERVICE_AUTO_START
& "$dir\nssm.exe" set GuardPanel AppStdout     "$dir\log-panel.txt"
& "$dir\nssm.exe" set GuardPanel AppStderr     "$dir\log-panel.txt"
& "$dir\nssm.exe" set GuardPanel AppRotateFiles 1
& "$dir\nssm.exe" set GuardPanel AppRotateBytes 5242880

# -- Firewall VPS3 ---------------------------------------------
#
# IMPORTANTE: En Windows Firewall, Block SIEMPRE gana sobre Allow,
# sin importar cuan especifica sea la regla Allow.
#
# INCORRECTO (lo que hace fallar la conexion de VPS1/VPS2):
#   Block puerto 7668 desde TODOS  <-- esto bloquea tambien a VPS1 y VPS2
#   Allow puerto 7668 desde VPS1   <-- nunca se ejecuta porque Block gana
#
# CORRECTO: solo reglas Allow. Windows Firewall bloquea todo lo demas
# por defecto (el perfil de inbound por defecto es Block en Windows Server).
#
Write-Host "Configurando firewall del servidor del juego..." -ForegroundColor Green

# Limpiar reglas viejas (incluye las Block incorrectas si existen)
Remove-NetFirewallRule -DisplayName "Block Direct Login Backend"  -ErrorAction SilentlyContinue
Remove-NetFirewallRule -DisplayName "Block Direct Game Backend"   -ErrorAction SilentlyContinue
Remove-NetFirewallRule -DisplayName "Allow VPS1 Login"           -ErrorAction SilentlyContinue
Remove-NetFirewallRule -DisplayName "Allow VPS1 Game"            -ErrorAction SilentlyContinue
Remove-NetFirewallRule -DisplayName "Allow VPS2 Login"           -ErrorAction SilentlyContinue
Remove-NetFirewallRule -DisplayName "Allow VPS2 Game"            -ErrorAction SilentlyContinue
Remove-NetFirewallRule -DisplayName "Allow VPS1"                 -ErrorAction SilentlyContinue
Remove-NetFirewallRule -DisplayName "Allow VPS2"                 -ErrorAction SilentlyContinue
Remove-NetFirewallRule -DisplayName "Allow VPS1 private"         -ErrorAction SilentlyContinue

# Solo Allow desde VPS1 y VPS2. Todo lo demas queda bloqueado por defecto.
# Login (7666) y Game (7669) son los puertos del servidor VB6 en esta maquina.
# IPs publicas de VPS1 y VPS2
New-NetFirewallRule -DisplayName "Allow VPS1 Login" -Direction Inbound -Protocol TCP -LocalPort 7666 -RemoteAddress "38.54.45.154"  -Action Allow | Out-Null
New-NetFirewallRule -DisplayName "Allow VPS2 Login" -Direction Inbound -Protocol TCP -LocalPort 7666 -RemoteAddress "45.235.98.209" -Action Allow | Out-Null
New-NetFirewallRule -DisplayName "Allow VPS1 Game"  -Direction Inbound -Protocol TCP -LocalPort 7669 -RemoteAddress "38.54.45.154"  -Action Allow | Out-Null
New-NetFirewallRule -DisplayName "Allow VPS2 Game"  -Direction Inbound -Protocol TCP -LocalPort 7669 -RemoteAddress "45.235.98.209" -Action Allow | Out-Null
# IPs privadas (misma red del datacenter) - VPS1/VPS2 pueden aparecer con su IP interna
New-NetFirewallRule -DisplayName "Allow VPS1 private" -Direction Inbound -Protocol TCP -LocalPort 7666,7669 -RemoteAddress "192.168.154.20" -Action Allow | Out-Null

# Puerto 7700 (panel web) solo escucha en 127.0.0.1 por configuracion
# del ejecutable, no necesita regla de firewall.

# Iniciar panel
Write-Host "Iniciando servicio GuardPanel..." -ForegroundColor Green
& "$dir\nssm.exe" start GuardPanel
Start-Sleep 2

# ============================================================
#  VERIFICACION COMPLETA DE FIREWALL Y SERVICIOS
# ============================================================
Write-Host ""
Write-Host "=============================================" -ForegroundColor Cyan
Write-Host "  VERIFICACION COMPLETA - VPS3" -ForegroundColor Cyan
Write-Host "=============================================" -ForegroundColor Cyan

$nOK  = 0
$nERR = 0

function OK  { param($m) Write-Host "  [OK]    $m" -ForegroundColor Green;  $script:nOK++  }
function ERR { param($m) Write-Host "  [ERROR] $m" -ForegroundColor Red;    $script:nERR++ }

# -- Servicios ---------------------------------------------
Write-Host ""
Write-Host "-- Servicios --" -ForegroundColor White
$s = Get-Service GuardPanel -ErrorAction SilentlyContinue
if (-not $s)                    { ERR "Servicio GuardPanel no existe" }
elseif ($s.Status -ne "Running"){ ERR "Servicio GuardPanel esta $($s.Status) (esperado: Running)" }
else                            { OK  "Servicio GuardPanel esta Running" }

# -- Puertos escuchando ------------------------------------
Write-Host ""
Write-Host "-- Puertos --" -ForegroundColor White
foreach ($p in @(
    @{Port=7700; Desc="Panel web (solo localhost)"},
    @{Port=7666; Desc="Servidor login VB6 (acepta VPS1 y VPS2)"},
    @{Port=7669; Desc="Servidor game VB6  (acepta VPS1 y VPS2)"}
)) {
    $l = netstat -ano | Select-String (":$($p.Port)\s") | Select-String "LISTENING"
    if ($l) { OK  "Puerto $($p.Port) LISTENING  - $($p.Desc)" }
    else    { ERR "Puerto $($p.Port) NO escucha - $($p.Desc)" }
}

# Verificar que el panel no esta expuesto en 0.0.0.0 (debe ser solo 127.0.0.1)
$panelPublic = netstat -ano | Select-String "0\.0\.0\.0:7700" | Select-String "LISTENING"
if ($panelPublic) {
    ERR "Panel escuchando en 0.0.0.0:7700 - deberia ser solo 127.0.0.1 (riesgo de exposicion)"
} else {
    OK "Panel 7700 NO expuesto publicamente (correcto)"
}

# -- Reglas de firewall ------------------------------------
Write-Host ""
Write-Host "-- Reglas de firewall --" -ForegroundColor White

function Check-AllowRule {
    param($nombre, $puerto)
    $r = Get-NetFirewallRule -DisplayName $nombre -ErrorAction SilentlyContinue
    if (-not $r) {
        ERR "Regla '$nombre' (puerto $puerto) NO existe"
        return
    }
    if ($r.Action -ne "Allow") {
        ERR "Regla '$nombre' existe pero es $($r.Action) en lugar de Allow"
        return
    }
    OK "Regla '$nombre' (puerto $puerto) existe y es Allow"
}

function Check-AllowRemote {
    param($nombre, $ipEsperada)
    $r = Get-NetFirewallRule -DisplayName $nombre -ErrorAction SilentlyContinue
    if (-not $r) { return }
    $addr = ($r | Get-NetFirewallAddressFilter -ErrorAction SilentlyContinue).RemoteAddress
    if ($addr -contains $ipEsperada) {
        OK "Regla '$nombre' restringida solo a $ipEsperada"
    } else {
        ERR "Regla '$nombre' tiene RemoteAddress='$($addr -join ',')' (esperado: $ipEsperada)"
    }
}

# CRITICO: Block + Allow en mismo puerto = Block siempre gana = VPS1/VPS2 no pueden conectar
function Check-NoBlockOnPort {
    param($puerto)
    $conflict = @()
    $allBlock = Get-NetFirewallRule -Direction Inbound -Action Block -Enabled True -ErrorAction SilentlyContinue
    foreach ($r in $allBlock) {
        $pf = $r | Get-NetFirewallPortFilter -ErrorAction SilentlyContinue
        if ($pf -and ($pf.LocalPort -eq "$puerto" -or $pf.LocalPort -eq "Any")) {
            $conflict += $r.DisplayName
        }
    }
    if ($conflict.Count -gt 0) {
        ERR "CRITICO: Puerto $puerto tiene reglas BLOCK activas: '$($conflict -join ', ')' - en Windows Firewall Block anula Allow, VPS1/VPS2 no pueden conectar!"
    } else {
        OK "Puerto $puerto sin reglas BLOCK conflictivas"
    }
}

Check-AllowRule   "Allow VPS1 Login" 7666
Check-AllowRule   "Allow VPS2 Login" 7666
Check-AllowRemote "Allow VPS1 Login" "38.54.45.154"
Check-AllowRemote "Allow VPS2 Login" "45.235.98.209"
Check-AllowRule   "Allow VPS1 Game"  7669
Check-AllowRule   "Allow VPS2 Game"  7669
Check-AllowRemote "Allow VPS1 Game"  "38.54.45.154"
Check-AllowRemote "Allow VPS2 Game"  "45.235.98.209"
Check-AllowRule   "Allow VPS1 private" 7666
Check-AllowRemote "Allow VPS1 private" "192.168.154.20"

# Verificar que las viejas reglas Block fueron eliminadas
foreach ($ruleVieja in @("Block Direct Login Backend","Block Direct Game Backend")) {
    $r = Get-NetFirewallRule -DisplayName $ruleVieja -ErrorAction SilentlyContinue
    if ($r) {
        ERR "Regla '$ruleVieja' todavia existe - eliminarla con: Remove-NetFirewallRule -DisplayName '$ruleVieja'"
    } else {
        OK "Regla Block '$ruleVieja' correctamente eliminada"
    }
}

Check-NoBlockOnPort 7666
Check-NoBlockOnPort 7669

# -- Resumen -----------------------------------------------
Write-Host ""
Write-Host "=============================================" -ForegroundColor Cyan
if ($nERR -eq 0) {
    Write-Host "  RESULTADO: $nOK OK - TODO CORRECTO" -ForegroundColor Green
} else {
    Write-Host "  RESULTADO: $nOK OK  |  $nERR ERROR(S) - REVISAR ITEMS EN ROJO" -ForegroundColor Red
}
Write-Host "=============================================" -ForegroundColor Cyan
Write-Host ""
Write-Host "Panel web:    http://127.0.0.1:7700"
Write-Host "Login 7666:   acepta desde 38.54.45.154 (VPS1), 45.235.98.209 (VPS2), 192.168.154.20 (VPS1 privada)"
Write-Host "Game  7669:   acepta desde 38.54.45.154 (VPS1), 45.235.98.209 (VPS2)"
Write-Host "Logs:         $dir\log-panel.txt"
Write-Host ""
Write-Host "IMPORTANTE: El servidor VB6 tiene que estar corriendo en puerto 7666 (login) y 7669 (game)" -ForegroundColor Yellow
