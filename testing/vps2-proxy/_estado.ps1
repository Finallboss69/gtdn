# ============================================================
#  GUARD_GO VPS2 - Estado y Reparacion Automatica
#  Detecta problemas, los arregla si puede, y reporta resultado.
# ============================================================

if (-NOT ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]"Administrator")) {
    Write-Error "Ejecutar como Administrador para poder reparar"
    exit 1
}

$dir      = "C:\guard"
$fixCount = 0
$errCount = 0

function OK     { param($m) Write-Host "  [OK]     $m" -ForegroundColor Green }
function FIXED  { param($m) Write-Host "  [FIXED]  $m" -ForegroundColor Cyan;    $script:fixCount++ }
function ERR    { param($m) Write-Host "  [ERROR]  $m" -ForegroundColor Red;     $script:errCount++ }
function MANUAL { param($m) Write-Host "  [MANUAL] $m" -ForegroundColor Magenta; $script:errCount++ }
function WARN   { param($m) Write-Host "  [WARN]   $m" -ForegroundColor Yellow }

Write-Host ""
Write-Host "=============================================" -ForegroundColor Cyan
Write-Host "  GUARD_GO VPS2 - Estado y Reparacion Auto" -ForegroundColor Cyan
Write-Host "=============================================" -ForegroundColor Cyan

# -------------------------------------------------------
#  SERVICIOS
# -------------------------------------------------------
Write-Host ""
Write-Host "-- Servicios --" -ForegroundColor White

foreach ($svc in @("GuardLogin","GuardGame")) {
    $s = Get-Service $svc -ErrorAction SilentlyContinue
    if (-not $s) {
        ERR "Servicio $svc NO existe - re-ejecutar 1-INSTALAR.bat"
        continue
    }
    if ($s.Status -eq "Running") {
        OK "Servicio $svc Running"
        continue
    }
    WARN "Servicio $svc esta $($s.Status) - intentando arrancar..."
    Start-Service $svc -ErrorAction SilentlyContinue
    Start-Sleep 3
    $s2 = Get-Service $svc -ErrorAction SilentlyContinue
    if ($s2 -and $s2.Status -eq "Running") {
        FIXED "Servicio $svc iniciado correctamente"
    } else {
        ERR "Servicio $svc sigue $($s2.Status) - ver logs abajo"
        foreach ($logFile in @("$dir\guard-login.log","$dir\guard-game.log")) {
            if (Test-Path $logFile) {
                Write-Host "  -- $(Split-Path $logFile -Leaf) (ultimas 15 lineas) --" -ForegroundColor Yellow
                Get-Content $logFile -Tail 15 | ForEach-Object { Write-Host "    $_" -ForegroundColor Gray }
            } else {
                Write-Host "  $(Split-Path $logFile -Leaf) no existe todavia" -ForegroundColor DarkYellow
            }
        }
        Write-Host "  Probar manualmente para ver el error real:" -ForegroundColor Cyan
        Write-Host "    cd $dir" -ForegroundColor White
        Write-Host "    .\guard-login.exe -config config.json -profile login" -ForegroundColor White
        Write-Host "    .\guard-game.exe  -config config.json -profile game"  -ForegroundColor White
    }
}

# -------------------------------------------------------
#  PUERTOS
# -------------------------------------------------------
Write-Host ""
Write-Host "-- Puertos --" -ForegroundColor White

foreach ($p in @(
    @{Port=7666; Desc="Login proxy (jugadores)"},
    @{Port=7669; Desc="Game proxy (jugadores)"},
    @{Port=7771; Desc="Admin login (panel)"},
    @{Port=7772; Desc="Admin game  (panel)"}
)) {
    $l = netstat -ano | Select-String (":$($p.Port)\s") | Select-String "LISTENING"
    if ($l) { OK  "Puerto $($p.Port) LISTENING - $($p.Desc)" }
    else    { ERR "Puerto $($p.Port) NO escucha - $($p.Desc) (servicio no inicio o config incorrecta)" }
}

# -------------------------------------------------------
#  FIREWALL - Reglas Allow
# -------------------------------------------------------
Write-Host ""
Write-Host "-- Firewall (reglas Allow) --" -ForegroundColor White

$rules = @(
    @{Name="Guard Login Public"; Port=7666; Remote=$null},
    @{Name="Guard Game Public";  Port=7669; Remote=$null},
    @{Name="Guard Admin Login";  Port=7771; Remote=$null},          # abierto a todos: panel + relay heartbeats
    @{Name="Guard Admin Game";   Port=7772; Remote="45.235.99.117"} # solo panel (VPS3)
)

foreach ($rule in $rules) {
    $output = netsh advfirewall firewall show rule name="$($rule.Name)" verbose 2>$null
    
    if ($output -match "No rules match") {
        WARN "Regla '$($rule.Name)' no existe - creando..."
        if ($rule.Remote) {
            netsh advfirewall firewall add rule name="$($rule.Name)" dir=in action=allow protocol=TCP localport=$($rule.Port) remoteip=$($rule.Remote) | Out-Null
        } else {
            netsh advfirewall firewall add rule name="$($rule.Name)" dir=in action=allow protocol=TCP localport=$($rule.Port) | Out-Null
        }
        FIXED "Regla '$($rule.Name)' creada (puerto $($rule.Port))"
    } elseif ($output -notmatch "Action:\s+Allow") {
        WARN "Regla '$($rule.Name)' no es Allow - corrigiendo..."
        netsh advfirewall firewall delete rule name="$($rule.Name)" 2>$null
        if ($rule.Remote) {
            netsh advfirewall firewall add rule name="$($rule.Name)" dir=in action=allow protocol=TCP localport=$($rule.Port) remoteip=$($rule.Remote) | Out-Null
        } else {
            netsh advfirewall firewall add rule name="$($rule.Name)" dir=in action=allow protocol=TCP localport=$($rule.Port) | Out-Null
        }
        FIXED "Regla '$($rule.Name)' corregida a Allow"
    } else {
        OK "Regla '$($rule.Name)' existe y es Allow"
        if ($rule.Remote) {
            if ($output -match "RemoteIP:\s+$($rule.Remote)") {
                OK "  -> Restringida solo a $($rule.Remote)"
            } else {
                ERR "  -> RemoteAddress incorrecto (esperado: $($rule.Remote))"
            }
        }
    }
}

# -------------------------------------------------------
#  FIREWALL - Eliminar reglas BLOCK conflictivas
# -------------------------------------------------------
Write-Host ""
Write-Host "-- Firewall (reglas BLOCK conflictivas) --" -ForegroundColor White

foreach ($port in @(7666,7669,7771,7772)) {
    $allRules = netsh advfirewall firewall show rule name=all | Out-String
    $conflicts = @()
    $lines = $allRules -split "`n"
    $currentRule = ""
    $isBlock = $false
    $hasPort = $false
    $isEnabled = $true
    
    foreach ($line in $lines) {
        if ($line -match "^Rule Name:\s+(.+)") {
            if ($currentRule -and $isBlock -and $hasPort -and $isEnabled) {
                $conflicts += $currentRule
            }
            $currentRule = $matches[1].Trim()
            $isBlock = $false
            $hasPort = $false
            $isEnabled = $true
        }
        if ($line -match "Enabled:\s+No") { $isEnabled = $false }
        if ($line -match "Action:\s+Block") { $isBlock = $true }
        if ($line -match "LocalPort:\s+($port|Any)") { $hasPort = $true }
    }
    # Check last rule
    if ($currentRule -and $isBlock -and $hasPort -and $isEnabled) {
        $conflicts += $currentRule
    }
    
    if ($conflicts.Count -gt 0) {
        WARN "Puerto $port tiene BLOCK: '$($conflicts -join "', '")' - eliminando (Block anula Allow)..."
        foreach ($n in $conflicts) { 
            netsh advfirewall firewall delete rule name="$n" 2>$null
        }
        FIXED "Reglas BLOCK eliminadas del puerto $port"
    } else {
        OK "Puerto $port sin reglas BLOCK conflictivas"
    }
}

# -------------------------------------------------------
#  RESUMEN
# -------------------------------------------------------
Write-Host ""
Write-Host "=============================================" -ForegroundColor Cyan
if ($errCount -eq 0 -and $fixCount -eq 0) {
    Write-Host "  TODO CORRECTO - sin errores ni reparaciones" -ForegroundColor Green
} elseif ($errCount -eq 0) {
    Write-Host "  $fixCount problema(s) REPARADO(S) automaticamente - todo OK ahora" -ForegroundColor Cyan
} else {
    Write-Host "  $fixCount reparado(s)  |  $errCount requieren atencion manual" -ForegroundColor Red
}
Write-Host "=============================================" -ForegroundColor Cyan
Write-Host ""
Write-Host "Login proxy:  0.0.0.0:7666 -> 45.235.99.117:7666"
Write-Host "Game proxy:   0.0.0.0:7669 -> 45.235.99.117:7669"
Write-Host "Logs:         $dir\guard-login.log  |  $dir\guard-game.log"
Write-Host ""
