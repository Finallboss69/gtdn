# GUARD_GO - TCP Guard Proxy (TDN)

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

### Con guard-relay (jugadores detrás de NAT)

```
game.exe → 127.0.0.1:17666 → guard-relay.exe → VPS1:7666 → Servidor de juego (VPS3)
game.exe → 127.0.0.1:17667 → guard-relay.exe → VPS1:7667 → Servidor de juego (VPS3)
```

`guard-relay.exe` corre en la máquina del jugador y crea un proxy local que tuneliza el
tráfico por la VPS con menor latencia. No requiere UPnP ni configuración de router.

## Estructura del Proyecto

```
/cmd/
  /guard-login/    # Ejecutable para login (rate limits agresivos + modo drain)
  /guard-game/     # Ejecutable para game (rate limits suaves + detección carga alta)
  /guard-panel/    # Panel de administración web (proxy reverso hacia ambos guards)
  /guard-relay/    # Cliente relay para jugadores detrás de NAT
  /guard/          # Ejecutable legacy (opcional)
/internal/
  /admin/          # API HTTP de administración (eventos, health, métricas, relay registry)
  /config/         # Manejo de configuración multi-perfil + validación
  /common/         # Funciones compartidas (logging, etc.)
  /firewall/       # Gestión de reglas Windows Firewall
  /limiter/        # Rate limiting, límites por IP, backoff exponencial de bans
  /proxy/          # Proxy TCP transparente con backoff adaptativo
config.json        # Configuración con perfiles "login" y "game"
relay.json.example # Ejemplo de configuración para guard-relay
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

- `admin_listen_addr: "0.0.0.0:7771"` - la API admin escucha en todas las interfaces (no solo localhost)
- `admin_token` - token que el panel usa para autenticarse. **Con token configurado, cualquier IP que presente el Bearer correcto tiene acceso** (el token es la seguridad principal)
- `admin_allow_ips` - lista de IPs permitidas **sin token**. Solo actúa como fallback cuando `admin_token` está vacío. Si hay token, esta lista se ignora para conexiones que presenten el Bearer correcto

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
      "name":      "VPS1 - Francia",
      "login_url": "http://185.vps1.ip:7771",
      "game_url":  "http://185.vps1.ip:7772",
      "token":     "token-secreto-cambia-esto"
    },
    {
      "id":        "vps2",
      "name":      "VPS2 - Alemania",
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
# Compilar todos los ejecutables (recomendado):
.\build.ps1

# O manualmente con optimizaciones:
go build -ldflags "-s -w" -o guard-login.exe ./cmd/guard-login
go build -ldflags "-s -w" -o guard-game.exe  ./cmd/guard-game
go build -ldflags "-s -w" -o guard-panel.exe ./cmd/guard-panel
go build -ldflags "-s -w" -o guard-relay.exe ./cmd/guard-relay
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
| tempblock_seconds | 90 | Duración base del bloqueo temporal (s) - crece exponencialmente |
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
| tempblock_seconds | 60 | Duración base del bloqueo temporal (s) - crece exponencialmente |
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
guard-relay.exe   # Relay cliente para jugadores (requiere relay.json)
```

Detener con `Ctrl+C`; todos hacen **graceful shutdown**.

### Flags disponibles (guard-login / guard-game)

```bash
guard-login.exe -config ruta/config.json -profile login -log-level debug
guard-game.exe  -config ruta/config.json -profile game  -log-level info
```

- `-config`: Ruta al archivo de configuración (default: busca config.json)
- `-profile`: Perfil a usar: login o game (default: detecta del nombre del ejecutable)
- `-log-level`: Override del nivel de log (debug|info|warn|error)

---

## guard-relay - Cliente relay para jugadores detrás de NAT

`guard-relay.exe` es un ejecutable liviano que corre en la PC del jugador y permite conectarse
al servidor de juego a través de la red de VPS, sin necesidad de configurar el router ni UPnP.

### Flujo de tráfico

```
game.exe → 127.0.0.1:17666 → guard-relay.exe → VPS1:7666 → Servidor de juego
game.exe → 127.0.0.1:17667 → guard-relay.exe → VPS1:7667 → Servidor de juego
```

### Comportamiento automático

1. **Selección de nodo**: al iniciar, prueba la latencia TCP de todos los VPS en paralelo y se conecta al más rápido.
2. **Proxy local**: crea dos listeners en `127.0.0.1:17666` (login) y `127.0.0.1:17667` (game). El juego debe apuntar a estos puertos en lugar de los del servidor.
3. **Heartbeat**: cada 30 segundos envía un ping al admin del VPS para que el panel lo cuente como relay activo.
4. **Monitor de salud**: cada 60 segundos re-verifica la latencia. Si el VPS actual falla o supera 500 ms, cambia automáticamente al mejor disponible sin cortar conexiones existentes.
5. **Salida limpia**: al presionar `Ctrl+C` se detiene ordenadamente.

### Configuración: `relay.json`

Copiar `relay.json.example` a `relay.json` (en el mismo directorio que `guard-relay.exe`):

```json
{
  "login_local": "127.0.0.1:17666",
  "game_local":  "127.0.0.1:17667",
  "nodes": [
    {
      "id":         "vps1",
      "name":       "VPS1",
      "login_addr": "38.54.45.154:7666",
      "game_addr":  "38.54.45.154:7667",
      "admin_url":  "http://38.54.45.154:7771",
      "token":      "token-secreto-cambia-esto"
    }
  ]
}
```

| Campo | Descripción |
|-------|-------------|
| `login_local` | Puerto local para login (el juego se conecta aquí) |
| `game_local` | Puerto local para game |
| `login_addr` | IP:Puerto público del guard-login en el VPS |
| `game_addr` | IP:Puerto público del guard-game en el VPS |
| `admin_url` | URL del admin del guard-login en el VPS (para heartbeat) |
| `token` | Token de admin del VPS (mismo que `admin_token` en config.json) |

### Salida de consola

```
[GUARD RELAY] Probando 2 nodo(s)...
[GUARD RELAY] Conectado a VPS1 (38.54.45.154:7666) | latencia: 7ms
[GUARD RELAY] Login local:  127.0.0.1:17666
[GUARD RELAY] Juego  local: 127.0.0.1:17667
[GUARD RELAY] Presiona Ctrl+C para salir
```

### Distribución a jugadores

Entregar al jugador:
- `guard-relay.exe`
- `relay.json` (pre-configurado con los VPS)

El jugador solo tiene que ejecutar `guard-relay.exe` antes de abrir el juego, y configurar
el juego para conectarse a `127.0.0.1` en lugar de la IP del servidor.

### Visibilidad en el panel

Cada VPS muestra un contador **Relays** en su tarjeta del panel cuando hay clientes relay
conectados. Se actualiza automáticamente (los relays aparecen ~30s después de iniciar, y
desaparecen ~90s después de cerrarse).

---

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
- **Contador Relays** en cada tarjeta de nodo cuando hay clientes guard-relay conectados

### Acciones disponibles en el panel

| Acción | Descripción |
|--------|-------------|
| Bloquear IP vía FW | Agrega la IP a las reglas de Windows Firewall |
| Desbloquear (por IP) | Quita el bloqueo temporal del limiter y del FW |
| **Desbloquear todos** | Libera todos los bloqueos temporales del limiter de una vez |
| **Test Conectividad** | Desde el panel, prueba TCP + HTTP a cada nodo. Muestra latencia TCP, HTTP status, y body de respuesta - permite diagnosticar si el problema es firewall (TCP fail), autenticacion (HTTP 401/403) o el servicio (HTTP 5xx) |

## API de Administración

### guard-login / guard-game (por nodo)

Login: `http://<vps>:7771/api/` - Game: `http://<vps>:7772/api/`

Acceso: loopback siempre; IPs externas requieren `Authorization: Bearer <admin_token>`.

| Endpoint | Método | Descripción |
|----------|--------|-------------|
| `/api/status` | GET | Estado del servicio (conns, drain, load_pct, drain_since, relay_count) |
| `/api/ips` | GET | Lista de IPs rastreadas con block_count |
| `/api/blocked` | GET | IPs bloqueadas via Windows Firewall |
| `/api/unblock` | POST | Desbloquear una IP especifica `{"ip":"1.2.3.4"}` |
| `/api/block` | POST | Bloquear una IP via FW `{"ip":"1.2.3.4"}` |
| `/api/unblock-all` | POST | Libera todos los bloqueos temporales |
| `/api/sysinfo` | GET | Goroutines, heap, GC, uptime |
| `/api/metrics` | GET | Historial de muestras (ultimos 6 min, 10s por muestra) |
| `/api/health` | GET | Health check: `{"status":"ok","uptime_seconds":N}` |
| `/api/events` | GET | Log de eventos recientes (ring buffer 200 eventos) |
| `/api/relay/ping` | POST | Heartbeat de guard-relay - abierto a cualquier IP, requiere Bearer. Body: `{"relay_id":"<uuid>"}` |

### guard-panel

Panel: `http://127.0.0.1:7700/api/` (solo localhost - no expuesto publicamente)

| Endpoint | Metodo | Descripcion |
|----------|--------|-------------|
| `/api/nodes` | GET | Lista de nodos configurados (id + name, sin tokens ni URLs internas) |
| `/api/node/{id}/{svc}/{endpoint}` | ANY | Proxy hacia el guard del nodo. `svc` = `login` o `game`. Ejemplo: `/api/node/vps1/login/status` → `http://vps1:7771/api/status` |
| `/api/diag` | GET | Prueba TCP + HTTP a todos los nodos y retorna latencias, status codes y body. Usado por el boton "Test Conectividad" |

### Ejemplo

```bash
# Health check de un nodo desde VPS3
curl -H "Authorization: Bearer token-secreto" http://38.54.45.154:7771/api/health

# Ver estado via el panel (proxy)
curl http://127.0.0.1:7700/api/node/vps1/login/status

# Diagnostico de conectividad desde el panel
curl http://127.0.0.1:7700/api/diag

# Desbloquear todos en un nodo via el panel
curl -X POST http://127.0.0.1:7700/api/node/vps1/login/unblock-all
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

### guard-login y guard-game - sc.exe (Windows Service nativo)

`guard-login.exe` y `guard-game.exe` tienen soporte nativo de Windows Service.
Usar `sc.exe`, **no NSSM** - NSSM los lanza como proceso hijo y el exe falla
al intentar conectarse al SCM ("El proceso del servicio no puede conectar...").

```powershell
# Registrar fuente de Event Log (necesario la primera vez)
New-EventLog -LogName Application -Source "GuardLogin" -ErrorAction SilentlyContinue
New-EventLog -LogName Application -Source "GuardGame"  -ErrorAction SilentlyContinue

# Instalar servicios
sc.exe create GuardLogin binPath= "\"C:\guard\guard-login.exe\" -config \"C:\guard\config.json\" -profile login" start= auto obj= LocalSystem DisplayName= "Guard Login Proxy"
sc.exe create GuardGame  binPath= "\"C:\guard\guard-game.exe\"  -config \"C:\guard\config.json\" -profile game"  start= auto obj= LocalSystem DisplayName= "Guard Game Proxy"

sc.exe start GuardLogin
sc.exe start GuardGame
```

### guard-panel - NSSM

`guard-panel.exe` no tiene soporte nativo de Windows Service (es un servidor HTTP puro).
Usar NSSM para gestionarlo:

```powershell
nssm install GuardPanel "C:\guard\guard-panel.exe"
nssm set GuardPanel AppDirectory  "C:\guard"
nssm set GuardPanel AppParameters "-nodes nodes.json"
nssm set GuardPanel Start         SERVICE_AUTO_START
nssm set GuardPanel AppStdout     "C:\guard\log-panel.txt"
nssm set GuardPanel AppStderr     "C:\guard\log-panel.txt"
nssm start GuardPanel
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
