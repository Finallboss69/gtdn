# TCP Guard Proxy (TDN)

Proxy TCP de alto rendimiento en Go 1.22+ para Windows Server 2019 x64 que protege un servidor de juego (VB6) frente a connection floods.

## Arquitectura

```
Cliente → 0.0.0.0:7666 (guard-login) → 127.0.0.1:7668 (Game Login interno)
Cliente → 0.0.0.0:7667 (guard-game)  → 127.0.0.1:7669 (Game interno)
```

El guard escucha en interfaces públicas y reenvía solo el tráfico permitido al backend en localhost.

## Estructura del Proyecto

```
/cmd/
  /guard-login/    # Ejecutable para login (rate limits agresivos)
  /guard-game/     # Ejecutable para game (rate limits suaves)
  /guard/          # Ejecutable legacy (opcional)
/internal/
  /config/         # Manejo de configuración multi-perfil
  /common/         # Funciones compartidas (logging, etc.)
  /firewall/       # Gestión de reglas Windows Firewall
  /limiter/        # Rate limiting y límites por IP
  /proxy/          # Proxy TCP transparente
config.json        # Configuración con perfiles "login" y "game"
```

## Compilación

### Compilar ambos ejecutables:

```bash
go build -o guard-login.exe ./cmd/guard-login
go build -o guard-game.exe ./cmd/guard-game
```

### Compilar con optimizaciones (recomendado para producción):

```bash
go build -ldflags "-s -w" -o guard-login.exe ./cmd/guard-login
go build -ldflags "-s -w" -o guard-game.exe ./cmd/guard-game
```

- `-s -w` reduce el tamaño del binario (strip debug info).

### Script de build (PowerShell):

```powershell
.\build.ps1
```

## Configuración

Copia `config.json.example` a `config.json` y ajusta los valores. Si no existe `config.json`, se usan los valores por defecto.

El archivo `config.json` contiene dos perfiles:

### Perfil "login" (Rate limits agresivos)
| Parámetro | Default | Descripción |
|-----------|---------|-------------|
| listen_addr | 0.0.0.0:7666 | Dirección:puerto público del proxy |
| backend_addr | 127.0.0.1:7668 | Servidor de juego real (login) |
| max_live_conns_per_ip | 2 | Máximo de conexiones simultáneas por IP |
| attempt_refill_per_sec | 1.0 | Recarga del token bucket (intentos/s) |
| attempt_burst | 4 | Capacidad del token bucket |
| denies_before_tempblock | 10 | Rechazos antes de bloqueo temporal |
| tempblock_seconds | 90 | Duración del bloqueo temporal (s) |
| max_total_conns | 2000 | Límite global de conexiones |
| idle_timeout_seconds | 15 | Timeout de inactividad (s) |
| stale_after_seconds | 180 | Eliminar IPs sin actividad tras (s) |
| cleanup_every_seconds | 30 | Intervalo de limpieza (s) |
| enable_firewall_autoban | true | Crear regla Windows Firewall en tempblock |
| firewall_block_seconds | 900 | Tiempo que permanece la regla de bloqueo (s) |
| log_level | info | debug \| info \| warn \| error |
| log_file | "" | Archivo de log (vacío = auto-detect) |

### Perfil "game" (Rate limits suaves)
| Parámetro | Default | Descripción |
|-----------|---------|-------------|
| listen_addr | 0.0.0.0:7667 | Dirección:puerto público del proxy |
| backend_addr | 127.0.0.1:7669 | Servidor de juego real (game) |
| max_live_conns_per_ip | 3 | Máximo de conexiones simultáneas por IP |
| attempt_refill_per_sec | 2.0 | Recarga del token bucket (intentos/s) |
| attempt_burst | 6 | Capacidad del token bucket |
| denies_before_tempblock | 15 | Rechazos antes de bloqueo temporal |
| tempblock_seconds | 60 | Duración del bloqueo temporal (s) |
| max_total_conns | 4000 | Límite global de conexiones |
| idle_timeout_seconds | 30 | Timeout de inactividad (s) |
| stale_after_seconds | 180 | Eliminar IPs sin actividad tras (s) |
| cleanup_every_seconds | 30 | Intervalo de limpieza (s) |
| enable_firewall_autoban | true | Crear regla Windows Firewall en tempblock |
| firewall_block_seconds | 600 | Tiempo que permanece la regla de bloqueo (s) |
| log_level | info | debug \| info \| warn \| error |
| log_file | "" | Archivo de log (vacío = auto-detect) |

## Ejecución

### Modo Consola:

```bash
guard-login.exe
guard-game.exe
```

Detener con `Ctrl+C` (SIGINT) o enviando SIGTERM; el proxy hace **graceful shutdown**.

### Flags disponibles:

```bash
guard-login.exe -config ruta/config.json -profile login -log-level debug
guard-game.exe -config ruta/config.json -profile game -log-level info
```

- `-config`: Ruta al archivo de configuración (default: busca config.json)
- `-profile`: Perfil a usar: login o game (default: detecta del nombre del ejecutable)
- `-log-level`: Override del nivel de log (debug|info|warn|error)

## Ejecutar como servicio en Windows

### 1. NSSM (recomendado)

```powershell
# Instalar guard-login
nssm install GuardLogin "C:\ruta\guard-login.exe"
nssm set GuardLogin AppDirectory "C:\ruta"
nssm start GuardLogin

# Instalar guard-game
nssm install GuardGame "C:\ruta\guard-game.exe"
nssm set GuardGame AppDirectory "C:\ruta"
nssm start GuardGame
```

### 2. sc.exe (servicio nativo)

```powershell
# Crear servicios
sc create GuardLogin binPath= "C:\ruta\guard-login.exe" start= auto
sc create GuardGame binPath= "C:\ruta\guard-game.exe" start= auto

# Iniciar servicios
sc start GuardLogin
sc start GuardGame
```

**Nota:** El directorio de trabajo será el del sistema; asegúrate de que `config.json` esté en una ruta accesible o usa rutas absolutas en la configuración.

## Firewall de Windows recomendado

### Bloquear puertos internos desde Internet

Los puertos del juego real (7668 y 7669) **NO** deben ser accesibles desde Internet.

```powershell
# Bloquear 7668 (login interno) desde redes externas
New-NetFirewallRule -DisplayName "Block Game Login Backend External" `
  -Direction Inbound -Protocol TCP -LocalPort 7668 `
  -RemoteAddress "0.0.0.0/0" -Action Block -Profile Public

# Bloquear 7669 (game interno) desde redes externas
New-NetFirewallRule -DisplayName "Block Game Backend External" `
  -Direction Inbound -Protocol TCP -LocalPort 7669 `
  -RemoteAddress "0.0.0.0/0" -Action Block -Profile Public

# Permitir 7668 solo desde localhost (para guard-login)
New-NetFirewallRule -DisplayName "Allow Game Login Backend Localhost" `
  -Direction Inbound -Protocol TCP -LocalPort 7668 `
  -RemoteAddress "127.0.0.1" -Action Allow

# Permitir 7669 solo desde localhost (para guard-game)
New-NetFirewallRule -DisplayName "Allow Game Backend Localhost" `
  -Direction Inbound -Protocol TCP -LocalPort 7669 `
  -RemoteAddress "127.0.0.1" -Action Allow
```

### Puertos públicos (7666 y 7667)

Los puertos 7666 y 7667 deben estar **abiertos** para que los guards reciban conexiones desde Internet. Asegúrate de que el firewall permita conexiones entrantes en estos puertos.

Ajusta perfiles (Public, Private, Domain) según tu red.

## Protecciones implementadas

- **Por IP:** límite de conexiones vivas, token bucket de intentos, bloqueo temporal tras N rechazos.
- **Global:** semáforo de conexiones totales.
- **Opcional:** regla de Windows Firewall para bloquear IP temporalmente (AutoBan).
- **Logs:** por nivel y limitados por IP (máx. 1 log cada 2 s por IP).
- **Métricas:** cada 10 s se imprimen conexiones activas, IPs en memoria, rechazos por intervalo y uso del semáforo.

## Logs

- **guard-login.exe**: Escribe a `guard-login.log` cuando se ejecuta como servicio
- **guard-game.exe**: Escribe a `guard-game.log` cuando se ejecuta como servicio
- En modo consola, los logs van a stderr
- Puedes especificar un archivo de log personalizado en `config.json` con `log_file`

## Diferencias entre Login y Game

### Login (guard-login.exe)
- **Rate limits más agresivos**: Menos conexiones por IP, menos tokens, bloqueo más rápido
- **Diseñado para**: Autenticación y login (target principal de ataques)
- **Puerto público**: 7666
- **Puerto backend**: 7668

### Game (guard-game.exe)
- **Rate limits más suaves**: Más conexiones por IP, más tokens, más permisivo
- **Diseñado para**: Conexiones persistentes del juego
- **Puerto público**: 7667
- **Puerto backend**: 7669

## Notas importantes

- El guard **NO** parsea ni modifica el protocolo del juego
- Solo reenvía bytes y aplica límites de conexiones/intentos
- Compatible con cualquier protocolo TCP
- El servidor VB6 debe escuchar en los puertos internos (7668 y 7669)

Este software es **defensivo**: solo limita y bloquea conexiones; no realiza escaneos ni ataques.
