# CHANGELOG — GUARD_GO

## [28/02/2026] — Mejoras de Estabilidad, Seguridad y Administración

---

### Estabilidad

#### Bug: Listener TCP cerrado indefinidamente en modo drain
- **Problema anterior:** El listener TCP podía quedar cerrado sin límite de tiempo si la sobrecarga
  no bajaba, dejando el servicio de login completamente inaccesible de forma permanente.
- **Solución:** Se agregó `MaxDrainSeconds` (default 60 para login) que fuerza la salida del
  modo drain si se supera el tiempo máximo, incluso si la carga sigue alta.
- **Archivos modificados:** `internal/config/config.go`, `cmd/guard-login/main.go`

#### Bug: Backoff de proxy demasiado agresivo
- **Problema anterior:** En momentos de muchos rechazos, el proxy esperaba hasta 100ms entre
  conexiones, generando latencia innecesaria para usuarios legítimos.
- **Solución:** El backoff máximo se redujo a 10ms (de 100ms → 10ms, 50ms → 5ms, 10ms → 2ms).
- **Archivos modificados:** `internal/proxy/proxy.go`

---

### Seguridad

#### Bug: Atacantes persistentes nunca baneados permanentemente
- **Problema anterior:** Cada vez que un atacante era bloqueado temporalmente, el contador
  `DenyCount` se reseteaba al expirar el bloqueo, y el siguiente ban tenía la misma duración.
  Un atacante podía repetir el ciclo indefinidamente sin consecuencias acumulativas.
- **Solución:** Se agregó `BlockCount` que persiste aunque expire el bloqueo. La duración del
  bloqueo crece exponencialmente: 1x, 2x, 4x, 8x, 16x del tiempo base (máximo 24 horas).
- **Archivos modificados:** `internal/limiter/limiter.go`

#### Bug: IPs bloqueadas eliminadas por cleanup prematuramente
- **Problema anterior:** El cleanup de memoria podía eliminar registros de IPs bloqueadas si
  no tenían conexiones activas y habían pasado el tiempo de "stale", perdiendo el historial
  de bloqueos y reiniciando el backoff exponencial.
- **Solución:** El cleanup ahora verifica `stillBlocked` antes de eliminar una IP del mapa.
  Las IPs con bloqueo activo nunca son eliminadas hasta que expire el bloqueo.
- **Archivos modificados:** `internal/limiter/limiter.go`

#### Mejora: FW BlockIP asíncrono en guard-game
- **Problema anterior:** La llamada a `fw.BlockIP()` en guard-game era síncrona, pudiendo
  bloquear el handler de rechazo si netsh tardaba.
- **Solución:** La llamada ahora es asíncrona (goroutine), igual que en guard-login.
- **Archivos modificados:** `cmd/guard-game/main.go`

---

### Rendimiento

#### Mejora: Timeout configurable para conexión al backend
- **Problema anterior:** El timeout de conexión al backend era fijo en 10 segundos, sin
  posibilidad de ajuste por perfil.
- **Solución:** Se agregó `BackendDialTimeoutSeconds` (default 5 para login, 10 para game)
  como parámetro en `proxy.Run()`. Si no está configurado, usa el valor por defecto del perfil.
- **Archivos modificados:** `internal/config/config.go`, `internal/proxy/proxy.go`,
  `cmd/guard-login/main.go`, `cmd/guard-game/main.go`

---

### Administración

#### Nuevo: Validación de configuración al inicio
- Los procesos guard-login y guard-game ahora validan la configuración al arrancar usando
  `config.Validate()`. Si hay campos inválidos (max_total_conns ≤ 0, etc.) el proceso falla
  con un mensaje claro en lugar de comportarse incorrectamente.
- **Archivos modificados:** `internal/config/config.go`, `cmd/guard-login/main.go`,
  `cmd/guard-game/main.go`

#### Nuevo: EventLog — log de eventos del sistema
- Se agregó un ring buffer de hasta 200 eventos que registra: bans, desbloqueos, drain on/off,
  inicio/fin de sobrecarga.
- Disponible via `GET /api/{svc}/events`.
- **Archivos modificados:** `internal/admin/admin.go`

#### Nuevo: Endpoint /api/health
- `GET /api/{svc}/health` retorna `{"status":"ok","uptime_seconds":N}`.
- Útil para monitoreo externo y health checks.
- **Archivos modificados:** `internal/admin/admin.go`

#### Nuevo: Endpoint /api/unblock-all
- `POST /api/{svc}/unblock-all` libera todos los bloqueos temporales del limiter y retorna
  `{"cleared":N}`. Registra el evento en el EventLog.
- **Archivos modificados:** `internal/admin/admin.go`, `internal/limiter/limiter.go`

#### Nuevo: Campo drain_since en /api/status
- `/api/{svc}/status` ahora incluye `drain_since` (unix timestamp, 0 si no está en drain),
  `load_pct` (porcentaje de carga actual), y mantiene `drain_mode`.
- **Archivos modificados:** `internal/admin/admin.go`, `cmd/guard-login/main.go`

#### Nuevo: Detección de carga alta en guard-game
- El ticker de 10s ahora calcula el porcentaje de carga y loggea `[WARN] GAME HIGH LOAD`
  cuando supera el 90%. También notifica al EventLog.
- El campo `load_pct` se expone en `/api/game/status`.
- **Archivos modificados:** `cmd/guard-game/main.go`, `internal/admin/admin.go`

---

### Panel de administración

#### Nuevo: Filtro de búsqueda de IPs
- Cada tabla de IPs rastreadas tiene un campo de texto "Filtrar IP..." que oculta filas que
  no coincidan con el texto ingresado (sin recarga).
- **Archivos modificados:** `cmd/guard-panel/panel.html`

#### Nuevo: Botón "Desbloquear todos"
- Botón en la barra de acciones superior que llama a `/api/{svc}/unblock-all` para liberar
  todos los bloqueos temporales del servicio seleccionado (login, game, o ambos).
- **Archivos modificados:** `cmd/guard-panel/panel.html`

#### Nuevo: Drain timer en badge
- Cuando login está en modo drain, el badge ahora muestra `DRAIN MM:SS` con el tiempo
  transcurrido desde que comenzó el drain, calculado con `drain_since`.
- **Archivos modificados:** `cmd/guard-panel/panel.html`

#### Nuevo: Sección de eventos recientes
- Bajo el grid de servicios, una sección muestra los últimos 30 eventos combinados de login
  y game, ordenados por timestamp descendente.
- **Archivos modificados:** `cmd/guard-panel/panel.html`

#### Nuevo: Porcentaje de carga en stats
- La stat "Conexiones" muestra el porcentaje de uso del límite máximo con código de color
  (azul < 50%, naranja 50-80%, rojo > 80%).
- **Archivos modificados:** `cmd/guard-panel/panel.html`

#### Nuevo: Badge "CARGA ALTA" en game
- El card de game muestra un badge naranja "CARGA ALTA" cuando `load_pct >= 80%`.
- **Archivos modificados:** `cmd/guard-panel/panel.html`

#### Mejora: Columna BlockCount en tabla de IPs
- La tabla de IPs rastreadas ahora muestra la columna "Blqs" con el contador de veces
  que la IP fue bloqueada (refleja el backoff exponencial).
- **Archivos modificados:** `internal/admin/admin.go`, `cmd/guard-panel/panel.html`
