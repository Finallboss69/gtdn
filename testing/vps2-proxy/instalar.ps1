# ============================================================
#  GUARD_GO - Instalador VPS2 (45.235.98.209)
#  Ejecutar como Administrador en PowerShell
#
#  IMPORTANTE: guard-login.exe y guard-game.exe tienen soporte
#  nativo de Windows Service (svc.Run). NO usar NSSM con ellos.
#  NSSM los lanza como hijo, el exe intenta conectarse al SCM
#  y falla con "El proceso del servicio no puede conectar...".
#  Usar sc.exe (Windows Service Control Manager nativo).
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

Write-Host "=== Instalando GUARD_GO en VPS2 ===" -ForegroundColor Cyan

$archivos = @("guard-login.exe", "guard-game.exe", "config.json")
foreach ($archivo in $archivos) {
    if (!(Test-Path "$dir\$archivo")) {
        Write-Error "ERROR: Falta $dir\$archivo"
        exit 1
    }
}

# -- Limpiar instalacion anterior --------------------------------
Write-Host "Limpiando instalacion anterior..." -ForegroundColor Yellow
Stop-Service GuardLogin -Force -ErrorAction SilentlyContinue
Stop-Service GuardGame  -Force -ErrorAction SilentlyContinue
Start-Sleep 1

# Eliminar servicios viejos (sc.exe o NSSM)
sc.exe delete GuardLogin 2>$null
sc.exe delete GuardGame  2>$null

# Si habia NSSM, limpiar tambien
if (Test-Path "$dir\nssm.exe") {
    & "$dir\nssm.exe" remove GuardLogin confirm 2>$null
    & "$dir\nssm.exe" remove GuardGame  confirm 2>$null
}
Start-Sleep 2

# -- Registrar fuentes de Event Log (requerido por eventlog.Open en el exe) --
Write-Host "Registrando fuentes de Event Log..." -ForegroundColor Yellow
New-EventLog -LogName Application -Source "GuardLogin" -ErrorAction SilentlyContinue
New-EventLog -LogName Application -Source "GuardGame"  -ErrorAction SilentlyContinue

# -- Instalar GuardLogin con sc.exe (Windows Service nativo) -----
Write-Host "Instalando servicio GuardLogin (sc.exe nativo)..." -ForegroundColor Green
sc.exe create GuardLogin `
    binPath= "`"$dir\guard-login.exe`" -config `"$dir\config.json`" -profile login" `
    start= auto `
    obj= LocalSystem `
    DisplayName= "Guard Login Proxy"
sc.exe description GuardLogin "GUARD_GO - Proxy de login con rate limiting y autoban"

# -- Instalar GuardGame con sc.exe (Windows Service nativo) ------
Write-Host "Instalando servicio GuardGame (sc.exe nativo)..." -ForegroundColor Green
sc.exe create GuardGame `
    binPath= "`"$dir\guard-game.exe`" -config `"$dir\config.json`" -profile game" `
    start= auto `
    obj= LocalSystem `
    DisplayName= "Guard Game Proxy"
sc.exe description GuardGame "GUARD_GO - Proxy de game con rate limiting y autoban"

# -- Firewall -------------------------------------------------------
# Solo Allow. Windows Firewall bloquea todo lo demas por defecto.
# NO usar Block + Allow en el mismo puerto: Block siempre gana.
# Usar netsh advfirewall (compatible con Server 2012 R2 / PowerShell 4.0)
Write-Host "Configurando firewall..." -ForegroundColor Green
netsh advfirewall firewall delete rule name="Guard Login Public"  2>$null
netsh advfirewall firewall delete rule name="Guard Game Public"   2>$null
netsh advfirewall firewall delete rule name="Guard Admin Login"   2>$null
netsh advfirewall firewall delete rule name="Guard Admin Game"    2>$null

netsh advfirewall firewall add rule name="Guard Login Public" dir=in action=allow protocol=TCP localport=7666 | Out-Null
netsh advfirewall firewall add rule name="Guard Game Public"  dir=in action=allow protocol=TCP localport=7669 | Out-Null
# Puerto 7771 abierto a todos: VPS3 lo consulta para el panel Y los clientes
# guard-relay envian heartbeats desde IPs de jugadores. El token Bearer en el
# codigo protege los endpoints sensibles; /api/relay/ping es el unico accesible
# desde IPs externas (todos los demas endpoints requieren estar en admin_allow_ips).
netsh advfirewall firewall add rule name="Guard Admin Login"  dir=in action=allow protocol=TCP localport=7771 | Out-Null
# Puerto 7772 solo accesible desde VPS3 (panel). El relay no usa este puerto.
netsh advfirewall firewall add rule name="Guard Admin Game"   dir=in action=allow protocol=TCP localport=7772 remoteip=45.235.99.117 | Out-Null

# -- Iniciar servicios ----------------------------------------------
Write-Host "Iniciando servicios..." -ForegroundColor Green
Start-Service GuardLogin -ErrorAction SilentlyContinue
Start-Sleep 3
Start-Service GuardGame -ErrorAction SilentlyContinue
Start-Sleep 3

# ============================================================
#  VERIFICACION COMPLETA DE FIREWALL Y SERVICIOS
# ============================================================
Write-Host ""
Write-Host "=============================================" -ForegroundColor Cyan
Write-Host "  VERIFICACION COMPLETA - VPS2" -ForegroundColor Cyan
Write-Host "=============================================" -ForegroundColor Cyan

$nOK  = 0
$nERR = 0

function OK  { param($m) Write-Host "  [OK]    $m" -ForegroundColor Green;  $script:nOK++  }
function ERR { param($m) Write-Host "  [ERROR] $m" -ForegroundColor Red;    $script:nERR++ }

# -- Servicios ---------------------------------------------
Write-Host ""
Write-Host "-- Servicios --" -ForegroundColor White
$svcFailed = @()
foreach ($svc in @("GuardLogin","GuardGame")) {
    $s = Get-Service $svc -ErrorAction SilentlyContinue
    if (-not $s) {
        ERR "Servicio $svc no existe"
        $svcFailed += $svc
    } elseif ($s.Status -eq "Running") {
        OK "Servicio $svc esta Running"
    } elseif ($s.Status -eq "Paused") {
        ERR "Servicio $svc esta Paused - el proceso termina al arrancar"
        $svcFailed += $svc
    } else {
        ERR "Servicio $svc esta $($s.Status) (esperado: Running)"
        $svcFailed += $svc
    }
}

# Si hay servicios caidos, mostrar diagnostico y logs
if ($svcFailed.Count -gt 0) {
    Write-Host ""
    Write-Host "  *** DIAGNOSTICO ***" -ForegroundColor Yellow
    Write-Host "  Logs del exe (guard-login.log y guard-game.log en $dir):" -ForegroundColor Yellow
    foreach ($logFile in @("$dir\guard-login.log","$dir\guard-game.log")) {
        if (Test-Path $logFile) {
            Write-Host "  -- $(Split-Path $logFile -Leaf) (ultimas 20 lineas) --" -ForegroundColor Yellow
            Get-Content $logFile -Tail 20 | ForEach-Object { Write-Host "    $_" -ForegroundColor Gray }
            Write-Host ""
        } else {
            Write-Host "  $(Split-Path $logFile -Leaf) no existe todavia" -ForegroundColor DarkYellow
        }
    }
    Write-Host "  Para ver el error en tiempo real, ejecutar manualmente:" -ForegroundColor Cyan
    Write-Host "    cd $dir" -ForegroundColor White
    Write-Host "    .\guard-login.exe -config config.json -profile login" -ForegroundColor White
    Write-Host "    .\guard-game.exe  -config config.json -profile game" -ForegroundColor White
    Write-Host ""
}

# -- Puertos escuchando ------------------------------------
Write-Host ""
Write-Host "-- Puertos --" -ForegroundColor White
foreach ($p in @(
    @{Port=7666; Desc="Login proxy (jugadores)"},
    @{Port=7669; Desc="Game proxy (jugadores)"},
    @{Port=7771; Desc="Admin login (panel -> VPS2)"},
    @{Port=7772; Desc="Admin game  (panel -> VPS2)"}
)) {
    $l = netstat -ano | Select-String (":$($p.Port)\s") | Select-String "LISTENING"
    if ($l) { OK  "Puerto $($p.Port) LISTENING  - $($p.Desc)" }
    else    { ERR "Puerto $($p.Port) NO escucha - $($p.Desc)" }
}

# -- Reglas de firewall ------------------------------------
Write-Host ""
Write-Host "-- Reglas de firewall --" -ForegroundColor White

function Check-AllowRule {
    param($nombre, $puerto)
    $output = netsh advfirewall firewall show rule name="$nombre" 2>$null
    if ($output -match "No rules match") { 
        ERR "Regla '$nombre' (puerto $puerto) NO existe"
        return 
    }
    if ($output -match "Action:\s+Allow") { 
        OK "Regla '$nombre' (puerto $puerto) existe y es Allow"
    } else {
        ERR "Regla '$nombre' no es Allow"
    }
}

function Check-AdminRemote {
    param($nombre, $ipEsperada)
    $output = netsh advfirewall firewall show rule name="$nombre" verbose 2>$null
    if ($output -match "No rules match") { return }
    if ($output -match "RemoteIP:\s+$ipEsperada") { 
        OK "Regla '$nombre' restringida solo a $ipEsperada"
    } elseif ($output -match "RemoteIP:\s+Any") {
        ERR "Regla '$nombre' tiene RemoteIP=Any (esperado: $ipEsperada)"
    }
}

function Check-NoBlockOnPort {
    param($puerto)
    $allRules = netsh advfirewall firewall show rule name=all | Out-String
    $conflicts = @()
    $lines = $allRules -split "`n"
    $currentRule = ""
    $isBlock = $false
    $hasPort = $false
    
    foreach ($line in $lines) {
        if ($line -match "^Rule Name:\s+(.+)") {
            if ($currentRule -and $isBlock -and $hasPort) {
                $conflicts += $currentRule
            }
            $currentRule = $matches[1].Trim()
            $isBlock = $false
            $hasPort = $false
        }
        if ($line -match "Action:\s+Block") { $isBlock = $true }
        if ($line -match "LocalPort:\s+($puerto|Any)") { $hasPort = $true }
    }
    
    if ($conflicts.Count -gt 0) { 
        ERR "Puerto $puerto tiene reglas BLOCK que anulan los Allow: $($conflicts -join ', ')"
    } else { 
        OK "Puerto $puerto sin reglas BLOCK conflictivas"
    }
}

Check-AllowRule   "Guard Login Public" 7666
Check-AllowRule   "Guard Game Public"  7669
Check-AllowRule   "Guard Admin Login"  7771
Check-AllowRule   "Guard Admin Game"   7772
# 7771 es abierto a todos (relay heartbeats); 7772 solo VPS3
Check-AdminRemote "Guard Admin Game"   "45.235.99.117"
Check-NoBlockOnPort 7666
Check-NoBlockOnPort 7669
Check-NoBlockOnPort 7771
Check-NoBlockOnPort 7772

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
Write-Host "Login proxy:  0.0.0.0:7666 -> 45.235.99.117:7666 (abierto a todos)"
Write-Host "Game proxy:   0.0.0.0:7669 -> 45.235.99.117:7669 (abierto a todos)"
Write-Host "Admin login:  0.0.0.0:7771 (abierto a todos - panel + relay heartbeats)"
Write-Host "Admin game:   0.0.0.0:7772 (solo desde 45.235.99.117 - panel)"
Write-Host "Logs:         $dir\guard-login.log  |  $dir\guard-game.log"
Write-Host ""
Write-Host "IMPORTANTE - Puertos a abrir en el panel del proveedor VPS:" -ForegroundColor Yellow
Write-Host "  7666 TCP (cualquier IP) - jugadores conectan al proxy de login" -ForegroundColor Yellow
Write-Host "  7669 TCP (cualquier IP) - jugadores conectan al proxy de juego" -ForegroundColor Yellow
Write-Host "  7771 TCP (cualquier IP) - panel de VPS3 + heartbeats de guard-relay" -ForegroundColor Yellow
Write-Host "  7772 TCP (cualquier IP) - panel de VPS3 (o restringir a 45.235.99.117)" -ForegroundColor Yellow
Write-Host ""
Write-Host "El cliente del juego debe conectarse a: 45.235.98.209:7666 (login) y 45.235.98.209:7669 (game)" -ForegroundColor Cyan
Write-Host "  (o usar guard-relay.exe que auto-selecciona el mejor VPS)" -ForegroundColor Cyan
