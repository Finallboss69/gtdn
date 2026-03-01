# ============================================================
#  GUARD_GO VPS1 - Estado y Reparacion Automatica
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
Write-Host "  GUARD_GO VPS1 - Estado y Reparacion Auto" -ForegroundColor Cyan
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
    $r = Get-NetFirewallRule -DisplayName $rule.Name -ErrorAction SilentlyContinue
    if (-not $r) {
        WARN "Regla '$($rule.Name)' no existe - creando..."
        if ($rule.Remote) {
            New-NetFirewallRule -DisplayName $rule.Name -Direction Inbound -Protocol TCP -LocalPort $rule.Port -RemoteAddress $rule.Remote -Action Allow | Out-Null
        } else {
            New-NetFirewallRule -DisplayName $rule.Name -Direction Inbound -Protocol TCP -LocalPort $rule.Port -Action Allow | Out-Null
        }
        FIXED "Regla '$($rule.Name)' creada (puerto $($rule.Port))"
    } elseif ($r.Action -ne "Allow") {
        WARN "Regla '$($rule.Name)' es $($r.Action) - corrigiendo a Allow..."
        Remove-NetFirewallRule -DisplayName $rule.Name -ErrorAction SilentlyContinue
        if ($rule.Remote) {
            New-NetFirewallRule -DisplayName $rule.Name -Direction Inbound -Protocol TCP -LocalPort $rule.Port -RemoteAddress $rule.Remote -Action Allow | Out-Null
        } else {
            New-NetFirewallRule -DisplayName $rule.Name -Direction Inbound -Protocol TCP -LocalPort $rule.Port -Action Allow | Out-Null
        }
        FIXED "Regla '$($rule.Name)' corregida a Allow"
    } else {
        OK "Regla '$($rule.Name)' existe y es Allow"
        if ($rule.Remote) {
            $addr = ($r | Get-NetFirewallAddressFilter -ErrorAction SilentlyContinue).RemoteAddress
            if ($addr -contains $rule.Remote) {
                OK "  -> Restringida solo a $($rule.Remote)"
            } else {
                ERR "  -> RemoteAddress incorrecto: '$($addr -join ',')' (esperado: $($rule.Remote))"
            }
        }
    }
}

# -------------------------------------------------------
#  FIREWALL - Eliminar reglas BLOCK conflictivas
#  (Block siempre anula Allow en Windows Firewall)
# -------------------------------------------------------
Write-Host ""
Write-Host "-- Firewall (reglas BLOCK conflictivas) --" -ForegroundColor White

foreach ($port in @(7666,7669,7771,7772)) {
    $conflict = @()
    Get-NetFirewallRule -Direction Inbound -Action Block -Enabled True -ErrorAction SilentlyContinue | ForEach-Object {
        $pf = $_ | Get-NetFirewallPortFilter -ErrorAction SilentlyContinue
        if ($pf -and ($pf.LocalPort -eq "$port" -or $pf.LocalPort -eq "Any")) {
            $conflict += $_.DisplayName
        }
    }
    if ($conflict.Count -gt 0) {
        WARN "Puerto $port tiene BLOCK: '$($conflict -join "', '")' - eliminando (Block anula Allow)..."
        foreach ($n in $conflict) { Remove-NetFirewallRule -DisplayName $n -ErrorAction SilentlyContinue }
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
