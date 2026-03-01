# ============================================================
#  GUARD_GO VPS3 - Estado y Reparacion Automatica
#  Detecta problemas, los arregla si puede, y reporta resultado.
#
#  NOTA: El puerto 7666 es del servidor de juego (VB6).
#  Guard NO puede iniciarlo automaticamente.
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
Write-Host "  GUARD_GO VPS3 - Estado y Reparacion Auto" -ForegroundColor Cyan
Write-Host "=============================================" -ForegroundColor Cyan

# -------------------------------------------------------
#  SERVICIO GuardPanel
# -------------------------------------------------------
Write-Host ""
Write-Host "-- Servicios --" -ForegroundColor White

$s = Get-Service GuardPanel -ErrorAction SilentlyContinue
if (-not $s) {
    ERR "Servicio GuardPanel NO existe - re-ejecutar 1-INSTALAR.bat"
} elseif ($s.Status -eq "Running") {
    OK "Servicio GuardPanel Running"
} else {
    WARN "GuardPanel esta $($s.Status) - intentando arrancar..."
    if (Test-Path "$dir\nssm.exe") {
        & "$dir\nssm.exe" start GuardPanel 2>$null
    } else {
        Start-Service GuardPanel -ErrorAction SilentlyContinue
    }
    Start-Sleep 3
    $s2 = Get-Service GuardPanel -ErrorAction SilentlyContinue
    if ($s2 -and $s2.Status -eq "Running") {
        FIXED "GuardPanel iniciado correctamente"
    } else {
        ERR "GuardPanel sigue $($s2.Status) - revisar log abajo"
        if (Test-Path "$dir\log-panel.txt") {
            Write-Host "  -- log-panel.txt (ultimas 15 lineas) --" -ForegroundColor Yellow
            Get-Content "$dir\log-panel.txt" -Tail 15 | ForEach-Object { Write-Host "    $_" -ForegroundColor Gray }
        } else {
            Write-Host "  log-panel.txt no existe todavia" -ForegroundColor DarkYellow
        }
    }
}

# -------------------------------------------------------
#  PUERTOS
# -------------------------------------------------------
Write-Host ""
Write-Host "-- Puertos --" -ForegroundColor White

# Puerto 7700 - panel web (gestionado por GuardPanel)
$l7700 = netstat -ano | Select-String ":7700\s" | Select-String "LISTENING"
if ($l7700) {
    OK "Puerto 7700 LISTENING - Panel web (solo localhost)"
    $pub = netstat -ano | Select-String "0\.0\.0\.0:7700" | Select-String "LISTENING"
    if ($pub) {
        ERR "Panel escuchando en 0.0.0.0:7700 - deberia ser solo 127.0.0.1 (revisar config del exe)"
    } else {
        OK "Panel 7700 NO expuesto publicamente (correcto)"
    }
} else {
    ERR "Puerto 7700 NO escucha - GuardPanel no inicio"
}

# Puerto 7666 - servidor de juego (NO gestionado por Guard)
$l7666 = netstat -ano | Select-String ":7666\s" | Select-String "LISTENING"
if ($l7666) {
    OK "Puerto 7666 LISTENING - Servidor de juego"
} else {
    MANUAL "Puerto 7666 NO escucha - Servidor de juego - iniciarlo manualmente"
}

# -------------------------------------------------------
#  FIREWALL - Eliminar reglas BLOCK viejas (critico)
#  Block siempre anula Allow en Windows Firewall.
#  Las reglas viejas 'Block Direct ...' impiden que VPS1/VPS2 conecten.
# -------------------------------------------------------
Write-Host ""
Write-Host "-- Firewall (reglas BLOCK viejas) --" -ForegroundColor White

foreach ($blockName in @("Block Direct Login Backend","Block Direct Game Backend")) {
    $r = Get-NetFirewallRule -DisplayName $blockName -ErrorAction SilentlyContinue
    if ($r) {
        WARN "Regla BLOCK '$blockName' encontrada - eliminando (bloquea VPS1/VPS2)..."
        Remove-NetFirewallRule -DisplayName $blockName -ErrorAction SilentlyContinue
        FIXED "Regla BLOCK '$blockName' eliminada"
    } else {
        OK "Regla BLOCK '$blockName' no existe (correcto)"
    }
}

# -------------------------------------------------------
#  FIREWALL - Reglas Allow para VPS1 y VPS2
# -------------------------------------------------------
Write-Host ""
Write-Host "-- Firewall (reglas Allow VPS1/VPS2) --" -ForegroundColor White

$rules = @(
    @{Name="Allow VPS1"; Port=7666; Remote="38.54.45.154"},
    @{Name="Allow VPS2"; Port=7666; Remote="45.235.98.209"},
    @{Name="Allow VPS1 private"; Port=7666; Remote="192.168.154.20"}
)

foreach ($rule in $rules) {
    $r = Get-NetFirewallRule -DisplayName $rule.Name -ErrorAction SilentlyContinue
    if (-not $r) {
        WARN "Regla '$($rule.Name)' no existe - creando..."
        New-NetFirewallRule -DisplayName $rule.Name -Direction Inbound -Protocol TCP -LocalPort $rule.Port -RemoteAddress $rule.Remote -Action Allow | Out-Null
        FIXED "Regla '$($rule.Name)' creada (puerto $($rule.Port) desde $($rule.Remote))"
    } elseif ($r.Action -ne "Allow") {
        WARN "Regla '$($rule.Name)' es $($r.Action) - corrigiendo..."
        Remove-NetFirewallRule -DisplayName $rule.Name -ErrorAction SilentlyContinue
        New-NetFirewallRule -DisplayName $rule.Name -Direction Inbound -Protocol TCP -LocalPort $rule.Port -RemoteAddress $rule.Remote -Action Allow | Out-Null
        FIXED "Regla '$($rule.Name)' corregida a Allow"
    } else {
        OK "Regla '$($rule.Name)' existe y es Allow"
        $addr = ($r | Get-NetFirewallAddressFilter -ErrorAction SilentlyContinue).RemoteAddress
        if ($addr -contains $rule.Remote) {
            OK "  -> Restringida a $($rule.Remote)"
        } else {
            ERR "  -> RemoteAddress incorrecto: '$($addr -join ',')' (esperado: $($rule.Remote))"
        }
    }
}

# Verificar que no haya BLOCK en 7666
Write-Host ""
Write-Host "-- Firewall (BLOCK en puerto del juego) --" -ForegroundColor White

foreach ($port in @(7666)) {
    $conflict = @()
    Get-NetFirewallRule -Direction Inbound -Action Block -Enabled True -ErrorAction SilentlyContinue | ForEach-Object {
        $pf = $_ | Get-NetFirewallPortFilter -ErrorAction SilentlyContinue
        if ($pf -and ($pf.LocalPort -eq "$port" -or $pf.LocalPort -eq "Any")) {
            $conflict += $_.DisplayName
        }
    }
    if ($conflict.Count -gt 0) {
        WARN "Puerto $port tiene BLOCK: '$($conflict -join "', '")' - eliminando..."
        foreach ($n in $conflict) { Remove-NetFirewallRule -DisplayName $n -ErrorAction SilentlyContinue }
        FIXED "Reglas BLOCK eliminadas del puerto $port (VPS1/VPS2 ahora pueden conectar)"
    } else {
        OK "Puerto $port sin reglas BLOCK conflictivas"
    }
}

# -------------------------------------------------------
#  API DEL PANEL
# -------------------------------------------------------
Write-Host ""
Write-Host "-- Panel API --" -ForegroundColor White

try {
    $resp = Invoke-WebRequest -Uri "http://127.0.0.1:7700/api/nodes" -UseBasicParsing -TimeoutSec 3 -ErrorAction Stop
    OK "Panel responde en http://127.0.0.1:7700"
    Write-Host "  nodes: $($resp.Content)" -ForegroundColor Gray
} catch {
    ERR "Panel no responde en http://127.0.0.1:7700 - servicio puede estar iniciando todavia"
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
    Write-Host ""
    Write-Host "  NOTA: [MANUAL] = el script no puede repararlo automaticamente" -ForegroundColor Magenta
    Write-Host "  El puerto 7666 es del servidor de juego (VB6)" -ForegroundColor Magenta
    Write-Host "  Guard no puede iniciar ese servidor - hacerlo manualmente" -ForegroundColor Magenta
}
Write-Host "=============================================" -ForegroundColor Cyan
Write-Host ""
Write-Host "Panel web:    http://127.0.0.1:7700"
Write-Host "Juego 7666:   acepta desde 38.54.45.154 (VPS1), 45.235.98.209 (VPS2), 192.168.154.20 (VPS1 red interna)"
Write-Host "Logs:         $dir\log-panel.txt"
Write-Host ""
