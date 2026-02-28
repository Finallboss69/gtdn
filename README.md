# GUARD_GO — TCP Guard Proxy (TDN)

Proxy TCP de alto rendimiento en Go 1.22+ para Windows Server 2019 x64 que protege un servidor
de juego (VB6) frente a connection floods, ataques de fuerza bruta y sobrecarga.

## Arquitectura

```
Cliente → 0.0.0.0:7666 (guard-login) → 127.0.0.1:7668 (Game Login interno)
Cliente → 0.0.0.0:7667 (guard-game)  → 127.0.0.1:7669 (Game interno)
              ↓                                  ↓
     Panel admin :7771             Panel admin :7772
```

El guard escucha en interfaces públicas y reenvía solo el tráfico permitido al backend en
localhost. Incluye un panel web de administración en tiempo real.

## Estructura del Proyecto

```
/cmd/
  /guard-login/    # Ejecutable para login (rate limits agresivos + modo drain)
  /guard-game/     # Ejecutable para game (rate limits suaves + detección carga alta)
  /guard-panel/    # Panel de administración web (proxy reverso hacia ambos guards)
  /guard/          # Ejecutable legacy (opcional)
/internal/
  /admin/          # API HTTP de administración (eventos, health, métricas, unblock-all)
  /config/         # Manejo de configuración multi-perfil + validación
  /common/         # Funciones compartidas (logging, etc.)
  /firewall/       # Gestión de reglas Windows Firewall
  /limiter/        # Rate limiting, límites por IP, backoff exponencial de bans
  /proxy/          # Proxy TCP transparente con backoff adaptativo
config.json        # Configuración con perfiles "login" y "game"
CHANGELOG.md       # Historial de cambios
```

## Redundancia con múltiples nodos (1 a 20 VPS)

GUARD_GO soporta múltiples instancias en distintas VPS apuntando al mismo backend. Si una VPS cae, las demás siguen protegiendo el servidor.

```
Jugadores → HAProxy (balanceador) → VPS1: guard-login + guard-game → Backend VB6
                                  → VPS2: guard-login + guard-game ↗
                                  → VPS3: guard-login + guard-game ↗
```

### Paso 1: Configurar cada VPS guard

En el `config.json` de cada VPS guard, apuntar `backend_addr` al servidor real y exponer la API admin:

```json
{
  "login": {
    "backend_addr":   "185.backend.ip:7668",
    "admin_listen_addr": "0.0.0.0:7771",
    "admin_allow_ips": ["185.panel.ip"],
    "admin_token":    "token-secreto-cambia-esto"
  },
  "game": {
    "backend_addr":   "185.backend.ip:7669",
    "admin_listen_addr": "0.0.0.0:7772",
    "admin_allow_ips": ["185.panel.ip"],
    "admin_token":    "token-secreto-cambia-esto"
  }
}
```

- `admin_listen_addr: "0.0.0.0:7771"` — la API admin escucha en todas las interfaces (no solo localhost)
- `admin_allow_ips` — solo esta IP puede conectarse a la API admin remotamente
- `admin_token` — token que el panel usa para autenticarse

### Paso 2: Configurar HAProxy (balanceador)

En una VPS Linux con HAProxy instalado (`apt install haproxy`):

```haproxy
frontend login_front
    bind *:7666
    default_backend login_back

backend login_back
    balance roundrobin
    option tcp-check
    server vps1 185.vps1.ip:7666 check fall 3 rise 2
    server vps2 185.vps2.ip:7666 check fall 3 rise 2
    server vps3 185.vps3.ip:7666 check fall 3 rise 2

frontend game_front
    bind *:7667
    default_backend game_back

backend game_back
    balance roundrobin
    option tcp-check
    server vps1 185.vps1.ip:7667 check fall 3 rise 2
    server vps2 185.vps2.ip:7667 check fall 3 rise 2
    server vps3 185.vps3.ip:7667 check fall 3 rise 2
```

Si una VPS falla 3 checks seguidos, HAProxy la saca del pool automáticamente. Cuando se recupera, la reincorpora sola.

### Paso 3: Configurar nodes.json para el panel

En la máquina donde corre `guard-panel.exe`, copiar `nodes.json.example` a `nodes.json` y editarlo:

```json
{
  "nodes": [
    {
      "id":        "vps1",
      "name":      "VPS1 — Francia",
      "login_url": "http://185.vps1.ip:7771",
      "game_url":  "http://185.vps1.ip:7772",
      "token":     "token-secreto-cambia-esto"
    },
    {
      "id":        "vps2",
      "name":      "VPS2 — Alemania",
      "login_url": "http://185.vps2.ip:7771",
      "game_url":  "http://185.vps2.ip:7772",
      "token":     "token-secreto-cambia-esto"
    }
  ]
}
```

Arrancar el panel:
```bash
guard-panel.exe -nodes nodes.json
```

El panel muestra una grilla con todos los nodos, sus estados, carga, y permite bloquear/desbloquear por nodo.

### Nodo único (configuración por defecto)

Si no existe `nodes.json`, el panel funciona igual que antes apuntando a `127.0.0.1:7771` y `127.0.0.1:7772`. Completamente retrocompatible.

---

## Compilación

```bash
# Compilar los tres ejecutables:
go build -o guard-login.exe ./cmd/guard-login
go build -o guard-game.exe  ./cmd/guard-game
go build -o guard-panel.exe ./cmd/guard-panel

# Con optimizaciones (recomendado para producción):
go build -ldflags "-s -w" -o guard-login.exe ./cmd/guard-login
go build -ldflags "-s -w" -o guard-game.exe  ./cmd/guard-game
go build -ldflags "-s -w" -o guard-panel.exe ./cmd/guard-panel
```

## Configuración

Copia `config.json.example` a `config.json` y ajusta los valores. Si no existe `config.json`,
se usan los valores por defecto.

### Perfil "login" (Rate limits agresivos)

| Parámetro | Default | Descripción |
|-----------|---------|-------------|
| listen_addr | 0.0.0.0:7666 | Dirección:puerto público del proxy |
| backend_addr | 127.0.0.1:7668 | Servidor de juego real (login) |
| max_live_conns_per_ip | 2 | Máximo de conexiones simultáneas por IP |
| attempt_refill_per_sec | 1.0 | Recarga del token bucket (intentos/s) |
| attempt_burst | 4 | Capacidad del token bucket |
| denies_before_tempblock | 10 | Rechazos antes de bloqueo temporal |
| tempblock_seconds | 90 | Duración base del bloqueo temporal (s) — crece exponencialmente |
| max_total_conns | 2000 | Límite global de conexiones |
| idle_timeout_seconds | 15 | Timeout de inactividad (s) |
| stale_after_seconds | 180 | Eliminar IPs sin actividad tras (s) |
| cleanup_every_seconds | 30 | Intervalo de limpieza (s) |
| enable_firewall_autoban | true | Crear regla Windows Firewall en tempblock |
| firewall_block_seconds | 900 | Tiempo que permanece la regla de bloqueo (s) |
| log_level | info | debug \| info \| warn \| error |
| log_file | "" | Archivo de log (vacío = auto-detect) |
| admin_listen_addr | 127.0.0.1:7771 | Dirección del servidor de administración |
| **max_drain_seconds** | **60** | **Tiempo máximo en modo drain antes de forzar salida (0=sin límite)** |
| **backend_dial_timeout_seconds** | **5** | **Timeout para conectar al backend (s)** |

### Perfil "game" (Rate limits suaves)

| Parámetro | Default | Descripción |
|-----------|---------|-------------|
| listen_addr | 0.0.0.0:7667 | Dirección:puerto público del proxy |
| backend_addr | 127.0.0.1:7669 | Servidor de juego real (game) |
| max_live_conns_per_ip | 3 | Máximo de conexiones simultáneas por IP |
| attempt_refill_per_sec | 2.0 | Recarga del token bucket (intentos/s) |
| attempt_burst | 6 | Capacidad del token bucket |
| denies_before_tempblock | 15 | Rechazos antes de bloqueo temporal |
| tempblock_seconds | 60 | Duración base del bloqueo temporal (s) — crece exponencialmente |
| max_total_conns | 4000 | Límite global de conexiones |
| idle_timeout_seconds | 30 | Timeout de inactividad (s) |
| stale_after_seconds | 180 | Eliminar IPs sin actividad tras (s) |
| cleanup_every_seconds | 30 | Intervalo de limpieza (s) |
| enable_firewall_autoban | true | Crear regla Windows Firewall en tempblock |
| firewall_block_seconds | 600 | Tiempo que permanece la regla de bloqueo (s) |
| log_level | info | debug \| info \| warn \| error |
| log_file | "" | Archivo de log (vacío = auto-detect) |
| admin_listen_addr | 127.0.0.1:7772 | Dirección del servidor de administración |
| **max_drain_seconds** | **0** | **Sin límite de drain (game no usa drain)** |
| **backend_dial_timeout_seconds** | **10** | **Timeout para conectar al backend (s)** |

## Ejecución

### Modo Consola

```bash
guard-login.exe
guard-game.exe
guard-panel.exe   # Panel web en http://127.0.0.1:7700
```

Detener con `Ctrl+C` (SIGINT) o enviando SIGTERM; el proxy hace **graceful shutdown**.

### Flags disponibles

```bash
guard-login.exe -config ruta/config.json -profile login -log-level debug
guard-game.exe  -config ruta/config.json -profile game  -log-level info
```

- `-config`: Ruta al archivo de configuración (default: busca config.json)
- `-profile`: Perfil a usar: login o game (default: detecta del nombre del ejecutable)
- `-log-level`: Override del nivel de log (debug|info|warn|error)

## Panel de Administración

El panel web se accede en `http://127.0.0.1:7700` y muestra en tiempo real:

- **Conexiones activas** con porcentaje de carga y barra de progreso con código de color
- **Gráficos de sparkline** de conexiones y rechazos por segundo (últimos 6 minutos)
- **Información del proceso**: goroutines, heap, GC, uptime
- **Tabla de IPs rastreadas** con filtro de búsqueda, contador de bloqueos acumulados
- **Bloqueados vía Firewall** con tiempo restante
- **Log de eventos recientes** (bans, drain on/off, sobrecarga, desbloqueos)
- **Badge DRAIN** con timer `MM:SS` para login
- **Badge CARGA ALTA** para game cuando la carga supera el 80%

### Acciones disponibles en el panel

| Acción | Descripción |
|--------|-------------|
| Bloquear IP vía FW | Agrega la IP a las reglas de Windows Firewall |
| Desbloquear (por IP) | Quita el bloqueo temporal del limiter y del FW |
| **Desbloquear todos** | Libera todos los bloqueos temporales del limiter de una vez |

## API de Administración

Cada guard expone una API HTTP en `localhost` (solo accesible localmente).

### Login: `http://127.0.0.1:7771/api/`
### Game: `http://127.0.0.1:7772/api/`

| Endpoint | Método | Descripción |
|----------|--------|-------------|
| `/api/status` | GET | Estado del servicio (conns, drain, load_pct, drain_since) |
| `/api/ips` | GET | Lista de IPs rastreadas con block_count |
| `/api/blocked` | GET | IPs bloqueadas vía Windows Firewall |
| `/api/unblock` | POST | Desbloquear una IP específica `{"ip":"1.2.3.4"}` |
| `/api/block` | POST | Bloquear una IP vía FW `{"ip":"1.2.3.4"}` |
| `/api/sysinfo` | GET | Goroutines, heap, GC, uptime |
| `/api/metrics` | GET | Historial de muestras (últimos 6 min) |
| **`/api/health`** | GET | Health check: `{"status":"ok","uptime_seconds":N}` |
| **`/api/unblock-all`** | POST | Libera todos los bloqueos temporales |
| **`/api/events`** | GET | Log de eventos recientes (ring buffer 200 eventos) |

### Ejemplo

```bash
# Health check
curl http://127.0.0.1:7771/api/health

# Ver eventos recientes
curl http://127.0.0.1:7771/api/events

# Desbloquear todos
curl -X POST http://127.0.0.1:7771/api/unblock-all
```

## Protecciones implementadas

### Por IP
- **Límite de conexiones vivas**: máx. N conexiones simultáneas por IP
- **Token bucket**: controla la tasa de intentos de conexión
- **Bloqueo temporal con backoff exponencial**: un atacante reincidente recibe bloqueos
  cada vez más largos: 1x → 2x → 4x → 8x → 16x (máx. 24 horas)
- **IPs bloqueadas preservadas**: el cleanup nunca elimina IPs con bloqueo activo

### Global
- **Semáforo de conexiones totales**: límite duro de conexiones simultáneas
- **Modo drain (solo login)**: cierra el listener temporalmente cuando hay sobrecarga crítica
  (90%+), con timeout configurable (`max_drain_seconds`) para evitar que quede cerrado indefinidamente
- **Detección de carga alta (game)**: loggea y notifica cuando la carga supera el 90%

### Firewall
- **AutoBan**: crea reglas en Windows Firewall automáticamente en tempblock
- **Bloqueo asíncrono**: la llamada al firewall es en goroutine separada para no bloquear conexiones

### Logs y métricas
- Logs por nivel (debug, info, warn, error), limitados por IP (máx. 1 log/2s por IP)
- Métricas cada 10s: conexiones activas, IPs en memoria, rechazos/s, % de uso
- **EventLog**: ring buffer de 200 eventos (bans, drain, sobrecarga, desbloqueos)

## Ejecutar como servicio en Windows

### Con NSSM (recomendado)

```powershell
nssm install GuardLogin "C:\ruta\guard-login.exe"
nssm set GuardLogin AppDirectory "C:\ruta"
nssm start GuardLogin

nssm install GuardGame "C:\ruta\guard-game.exe"
nssm set GuardGame AppDirectory "C:\ruta"
nssm start GuardGame
```

### Con sc.exe (servicio nativo)

```powershell
sc create GuardLogin binPath= "C:\ruta\guard-login.exe" start= auto
sc create GuardGame  binPath= "C:\ruta\guard-game.exe"  start= auto
sc start GuardLogin
sc start GuardGame
```

## Firewall de Windows recomendado

Los puertos internos (7668, 7669) **NO** deben ser accesibles desde Internet:

```powershell
# Bloquear backends desde Internet
New-NetFirewallRule -DisplayName "Block Login Backend" -Direction Inbound -Protocol TCP -LocalPort 7668 -RemoteAddress "0.0.0.0/0" -Action Block
New-NetFirewallRule -DisplayName "Block Game Backend"  -Direction Inbound -Protocol TCP -LocalPort 7669 -RemoteAddress "0.0.0.0/0" -Action Block

# Permitir solo desde localhost
New-NetFirewallRule -DisplayName "Allow Login Backend Localhost" -Direction Inbound -Protocol TCP -LocalPort 7668 -RemoteAddress "127.0.0.1" -Action Allow
New-NetFirewallRule -DisplayName "Allow Game Backend Localhost"  -Direction Inbound -Protocol TCP -LocalPort 7669 -RemoteAddress "127.0.0.1" -Action Allow
```

Los puertos públicos (7666 y 7667) deben estar **abiertos** para que los guards reciban
conexiones desde Internet.

## Diferencias entre Login y Game

| Característica | guard-login | guard-game |
|---------------|-------------|------------|
| Puerto público | 7666 | 7667 |
| Puerto backend | 7668 | 7669 |
| Admin API | :7771 | :7772 |
| Rate limits | Agresivos | Suaves |
| Modo drain | ✅ (con timeout) | ❌ |
| Backend dial timeout | 5s | 10s |
| Detección carga alta | por drain | por log/evento |
| FW async | ✅ | ✅ |

## Notas

- El guard **NO** parsea ni modifica el protocolo del juego
- Solo reenvía bytes y aplica límites de conexiones/intentos
- Compatible con cualquier protocolo TCP
- El servidor VB6 debe escuchar en los puertos internos (7668 y 7669)
- La configuración se valida al inicio: si hay valores inválidos, el proceso falla con mensaje claro

Este software es **defensivo**: solo limita y bloquea conexiones; no realiza escaneos ni ataques.
