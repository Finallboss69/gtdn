# Guía — Setup con 3 VPS (2 proxies + 1 servidor de juego)

## Arquitectura

```
Jugadores → VPS1:7666/7667 (proxy 1) ─┐
                                        ├─→ VPS3:7668/7669 (juego VB6)
Jugadores → VPS2:7666/7667 (proxy 2) ─┘

Panel web en VPS3:7700 (abrís con RDP + navegador)
```

## Lo que necesitás tener a mano antes de empezar

- Las IPs de los 3 VPS (reemplazá `IP_VPS1`, `IP_VPS2`, `IP_VPS3` por las reales)
- Acceso RDP a los 3 VPS
- Los archivos: `guard-login.exe`, `guard-game.exe`, `guard-panel.exe`, `nssm.exe`

---

## PASO 1 — Preparar VPS3 (servidor del juego)

Conectate por RDP a VPS3.

### 1.1 — Verificar que el juego VB6 escucha en los puertos correctos

```powershell
netstat -ano | findstr "7668\|7669"
```
Tiene que aparecer `LISTENING` en ambos puertos. Si no, arrancá el servidor VB6 primero.

### 1.2 — Bloquear acceso directo al juego desde internet

Esto es crítico: el juego solo debe aceptar conexiones de VPS1 y VPS2, nunca de jugadores directamente.

```powershell
# Bloquear para todos
New-NetFirewallRule -DisplayName "Block Direct Login Backend" -Direction Inbound -Protocol TCP -LocalPort 7668 -Action Block
New-NetFirewallRule -DisplayName "Block Direct Game Backend"  -Direction Inbound -Protocol TCP -LocalPort 7669 -Action Block

# Permitir solo desde VPS1
New-NetFirewallRule -DisplayName "Allow VPS1 Login" -Direction Inbound -Protocol TCP -LocalPort 7668 -RemoteAddress "IP_VPS1" -Action Allow
New-NetFirewallRule -DisplayName "Allow VPS1 Game"  -Direction Inbound -Protocol TCP -LocalPort 7669 -RemoteAddress "IP_VPS1" -Action Allow

# Permitir solo desde VPS2
New-NetFirewallRule -DisplayName "Allow VPS2 Login" -Direction Inbound -Protocol TCP -LocalPort 7668 -RemoteAddress "IP_VPS2" -Action Allow
New-NetFirewallRule -DisplayName "Allow VPS2 Game"  -Direction Inbound -Protocol TCP -LocalPort 7669 -RemoteAddress "IP_VPS2" -Action Allow
```

### 1.3 — Instalar el panel en VPS3

Creá la carpeta `C:\guard` y copiá `guard-panel.exe` y `nssm.exe` adentro.

Creá el archivo `C:\guard\nodes.json`:

```json
{
  "nodes": [
    {
      "id":        "vps1",
      "name":      "VPS1 — Proxy Principal",
      "login_url": "http://IP_VPS1:7771",
      "game_url":  "http://IP_VPS1:7772",
      "token":     "ELIGE-UNA-CONTRASEÑA-AQUI"
    },
    {
      "id":        "vps2",
      "name":      "VPS2 — Proxy Backup",
      "login_url": "http://IP_VPS2:7772",
      "game_url":  "http://IP_VPS2:7772",
      "token":     "ELIGE-UNA-CONTRASEÑA-AQUI"
    }
  ]
}
```

> El `token` tiene que ser la **misma contraseña** en VPS1, VPS2 y este archivo. Usá algo como `mJx9#kP2$wQ`.

Instalá el panel como servicio (PowerShell como Administrador):

```powershell
cd C:\guard
.\nssm.exe install GuardPanel "C:\guard\guard-panel.exe"
.\nssm.exe set GuardPanel AppDirectory "C:\guard"
.\nssm.exe set GuardPanel AppParameters "-nodes nodes.json"
.\nssm.exe set GuardPanel Start SERVICE_AUTO_START
.\nssm.exe start GuardPanel
```

---

## PASO 2 — Preparar VPS1 (primer proxy)

Conectate por RDP a VPS1.

### 2.1 — Crear carpeta y copiar archivos

Creá `C:\guard` y copiá: `guard-login.exe`, `guard-game.exe`, `nssm.exe`.

### 2.2 — Crear `C:\guard\config.json`

```json
{
  "login": {
    "listen_addr":             "0.0.0.0:7666",
    "backend_addr":            "IP_VPS3:7668",
    "max_live_conns_per_ip":   2,
    "attempt_refill_per_sec":  1.0,
    "attempt_burst":           4,
    "denies_before_tempblock": 10,
    "tempblock_seconds":       90,
    "max_total_conns":         2000,
    "idle_timeout_seconds":    15,
    "stale_after_seconds":     180,
    "cleanup_every_seconds":   60,
    "enable_firewall_autoban": true,
    "firewall_block_seconds":  900,
    "log_level":               "info",
    "admin_listen_addr":       "0.0.0.0:7771",
    "admin_allow_ips":         ["IP_VPS3"],
    "admin_token":             "ELIGE-UNA-CONTRASEÑA-AQUI"
  },
  "game": {
    "listen_addr":             "0.0.0.0:7667",
    "backend_addr":            "IP_VPS3:7669",
    "max_live_conns_per_ip":   3,
    "attempt_refill_per_sec":  2.0,
    "attempt_burst":           6,
    "denies_before_tempblock": 15,
    "tempblock_seconds":       60,
    "max_total_conns":         4000,
    "idle_timeout_seconds":    30,
    "stale_after_seconds":     180,
    "cleanup_every_seconds":   60,
    "enable_firewall_autoban": true,
    "firewall_block_seconds":  600,
    "log_level":               "info",
    "admin_listen_addr":       "0.0.0.0:7772",
    "admin_allow_ips":         ["IP_VPS3"],
    "admin_token":             "ELIGE-UNA-CONTRASEÑA-AQUI"
  }
}
```

> Reemplazá `IP_VPS3` con la IP real del servidor del juego. El `admin_token` tiene que ser **el mismo** que en `nodes.json` de VPS3.

### 2.3 — Instalar como servicios

```powershell
cd C:\guard

.\nssm.exe install GuardLogin "C:\guard\guard-login.exe"
.\nssm.exe set GuardLogin AppDirectory "C:\guard"
.\nssm.exe set GuardLogin AppParameters "-config config.json -profile login"
.\nssm.exe set GuardLogin Start SERVICE_AUTO_START
.\nssm.exe start GuardLogin

.\nssm.exe install GuardGame "C:\guard\guard-game.exe"
.\nssm.exe set GuardGame AppDirectory "C:\guard"
.\nssm.exe set GuardGame AppParameters "-config config.json -profile game"
.\nssm.exe set GuardGame Start SERVICE_AUTO_START
.\nssm.exe start GuardGame
```

Verificar que están corriendo:

```powershell
Get-Service GuardLogin, GuardGame
# Ambos deben mostrar: Status: Running
```

### 2.4 — Abrir puertos en el Firewall de Windows

```powershell
# Puertos para jugadores
New-NetFirewallRule -DisplayName "Guard Login Public" -Direction Inbound -Protocol TCP -LocalPort 7666 -Action Allow
New-NetFirewallRule -DisplayName "Guard Game Public"  -Direction Inbound -Protocol TCP -LocalPort 7667 -Action Allow

# Puertos admin (solo desde VPS3 donde está el panel)
New-NetFirewallRule -DisplayName "Guard Admin Login" -Direction Inbound -Protocol TCP -LocalPort 7771 -RemoteAddress "IP_VPS3" -Action Allow
New-NetFirewallRule -DisplayName "Guard Admin Game"  -Direction Inbound -Protocol TCP -LocalPort 7772 -RemoteAddress "IP_VPS3" -Action Allow
```

---

## PASO 3 — Preparar VPS2 (segundo proxy)

Exactamente igual que el PASO 2. Misma carpeta, mismo `config.json` (con los mismos `IP_VPS3` y `admin_token`), mismos comandos NSSM, mismas reglas de firewall.

---

## PASO 4 — Verificar que todo funciona

Conectate por RDP a VPS3 y abrí el navegador en:

```
http://127.0.0.1:7700
```

| Lo que ves | Qué significa |
|---|---|
| VPS1 y VPS2 en **verde** | Los guards están corriendo y el panel puede comunicarse |
| VPS1 o VPS2 en **rojo** | El guard no corre, o el firewall bloquea el puerto 7771/7772 |
| Conexiones = 0 | Normal si no hay jugadores conectados todavía |

---

## PASO 5 — Probar con un cliente

Configurá tu cliente del juego para conectarse a:

- Login: `IP_VPS1:7666`
- Game: `IP_VPS1:7667`

Si funciona, probá también con `IP_VPS2:7666` — tiene que funcionar igual.

---

## PASO 6 — Dar a los jugadores las IPs

**Opción A — Una sola IP (más simple)**

Decile a los jugadores que usen `IP_VPS1`. Si VPS1 cae, actualizás manualmente a `IP_VPS2`.

**Opción B — Dos IPs con failover automático (recomendado)**

Registrá un dominio barato (Namecheap, ~$8/año) y configurá dos registros DNS tipo A:

```
mijuego.com  →  IP_VPS1
mijuego.com  →  IP_VPS2
```

Con dos registros A, el DNS hace round-robin. Si un proxy cae, los jugadores que reintentan van al otro automáticamente.

---

## Resumen de puertos por VPS

| VPS | Puerto | Quién se conecta |
|-----|--------|------------------|
| VPS1 | 7666, 7667 | Jugadores (internet abierto) |
| VPS1 | 7771, 7772 | Solo VPS3 (panel admin) |
| VPS2 | 7666, 7667 | Jugadores (internet abierto) |
| VPS2 | 7771, 7772 | Solo VPS3 (panel admin) |
| VPS3 | 7668, 7669 | Solo VPS1 y VPS2 (bloqueado para el resto) |
| VPS3 | 7700 | Solo localhost (abrís con RDP + navegador) |

---

## Solución de problemas comunes

**El panel muestra VPS1/VPS2 en rojo**
1. Verificar que los servicios corren: `Get-Service GuardLogin, GuardGame` en VPS1/VPS2
2. Verificar que el firewall de VPS1/VPS2 permite el puerto 7771/7772 desde `IP_VPS3`
3. Verificar que el `admin_token` en `config.json` de VPS1/VPS2 coincide con el de `nodes.json` en VPS3

**Los jugadores se conectan pero el juego no responde**
1. Verificar que el VB6 está corriendo en VPS3
2. Verificar que el firewall de VPS3 permite 7668/7669 desde `IP_VPS1` e `IP_VPS2`
3. En el panel, revisar si hay muchos rechazos (los jugadores están siendo baneados)

**El AutoBan no funciona (netsh falla)**
Los servicios GuardLogin y GuardGame deben correr con una cuenta que tenga permisos de administrador.
En NSSM, ir a la pestaña "Log on" y seleccionar "Local System account".

```powershell
.\nssm.exe set GuardLogin ObjectName "LocalSystem"
.\nssm.exe set GuardGame  ObjectName "LocalSystem"
.\nssm.exe restart GuardLogin
.\nssm.exe restart GuardGame
```
