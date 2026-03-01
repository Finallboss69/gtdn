# Reparaciones — GUARD_GO

Auditoría completa realizada el 2026-02-28.
Todos los bugs listados aquí fueron **corregidos** en el mismo commit.

---

## Bug #1 — CRÍTICO: Doble incremento de `DenyCount` en rechazos por "rate"

**Archivos:** `internal/limiter/limiter.go`, `cmd/guard/main.go`, `cmd/guard-login/main.go`, `cmd/guard-game/main.go`
**Síntoma:** Usuarios legítimos bloqueados temporalmente al doble de velocidad de la esperada.

### Causa

En `TryAccept` (limiter.go), cuando el token bucket estaba agotado, se incrementaba `DenyCount` internamente:

```go
// limiter.go — ANTES (bug)
if state.Tokens < 1 {
    state.DenyCount++   // ← incremento #1 dentro de TryAccept
    if state.DenyCount >= l.deniesToBlock {
        state.BlockUntil = now.Add(...)
    }
    return false, "rate"
}
```

Simultáneamente, todos los handlers de `onReject` llamaban `lim.RecordDeny(ip)` para el caso `"rate"`, que también hace `DenyCount++`:

```go
// guard-login/main.go — ANTES (bug)
case "rate":
    lim.RecordDeny(ip)  // ← incremento #2, externo
```

**Resultado:** Con `denies_before_tempblock: 10`, el bloqueo temporal ocurría tras solo **5 rechazos** en vez de 10.

### Fix aplicado

Se eliminó el incremento interno de `TryAccept`. El único `DenyCount++` ahora ocurre en `RecordDeny`, llamado externamente desde `onReject`.

```go
// limiter.go — DESPUÉS (correcto)
if state.Tokens < 1 {
    // DenyCount lo gestiona RecordDeny externamente
    return false, "rate"
}
```

---

## Bug #2 — CRÍTICO: `RecordDeny` llamado para rechazos por `live_limit`

**Archivos:** `cmd/guard/main.go`, `cmd/guard-login/main.go`, `cmd/guard-game/main.go`
**Síntoma:** Usuarios penalizados y eventualmente bloqueados simplemente por tener demasiadas conexiones simultáneas (comportamiento normal de clientes con reconexiones).

### Causa

En `onReject`, el caso `"live_limit"` llamaba `RecordDeny`, acumulando `DenyCount`:

```go
// ANTES (bug)
case "live_limit":
    lim.RecordDeny(ip)  // penaliza DenyCount por exceso de conexiones simultáneas
    logMsg(2, ip, "reject live_limit client=%s", ip)
```

Con `max_live_conns_per_ip: 2` (login) y `denies_before_tempblock: 10 (efectivo: 5 por bug #1)`, un usuario que intentara una 3ª conexión simultánea (p. ej. reconexión antes de cerrar la anterior) acumulaba bloqueo temporal.

### Fix aplicado

Se quitó `RecordDeny` del caso `"live_limit"`. Exceder el límite de conexiones simultáneas es un evento de tráfico normal, no un ataque, y no debe penalizar el contador de bloqueo.

```go
// DESPUÉS (correcto)
case "live_limit":
    logMsg(2, ip, "reject live_limit client=%s", ip)
```

---

## Bug #3 — IMPORTANTE: `DenyCount` no se resetea al expirar el tempblock

**Archivo:** `internal/limiter/limiter.go`
**Síntoma:** Tras cumplir el tiempo de bloqueo temporal, la siguiente conexión fallida (aunque mínima) podía volver a bloquear al usuario casi inmediatamente.

### Causa

Cuando `BlockUntil` expiraba y el usuario intentaba reconectarse, `DenyCount` seguía en `deniesToBlock` (o superior). Cualquier fallo adicional (tokens aún bajos, live_limit) relanzaba el tempblock sin el período de gracia completo.

`TryAccept` detectaba que `now` ya no era `Before(state.BlockUntil)`, pero no reseteaba el contador:

```go
// ANTES (bug)
if now.Before(state.BlockUntil) {
    return false, "tempblock"
}
// DenyCount sigue alto — próxima penalización es inmediata
```

### Fix aplicado

Al detectar que el tempblock ya expiró, se resetean `DenyCount` y `BlockUntil`:

```go
// DESPUÉS (correcto)
if now.Before(state.BlockUntil) {
    return false, "tempblock"
}
// Tempblock expirado: limpiar penalizaciones acumuladas
if !state.BlockUntil.IsZero() {
    state.DenyCount = 0
    state.BlockUntil = time.Time{}
}
```

---

## Bug #4 — MENOR: Memory leak en `IPLogThrottle`

**Archivos:** `internal/common/common.go`, `cmd/guard/main.go`
**Síntoma:** Bajo DDoS con muchas IPs únicas, el mapa `lastAt` crecía sin límite, consumiendo memoria indefinidamente.

### Causa

El mapa `lastAt` de `IPLogThrottle` nunca eliminaba entradas. IPs que dejaban de conectarse permanecían en el mapa para siempre.

### Fix aplicado

Se agregó limpieza automática en `Allow()` cuando el mapa supera 10.000 entradas: se eliminan todas las entradas cuya última actividad es más antigua que la ventana de throttle.

```go
// DESPUÉS (correcto)
if len(t.lastAt) > 10000 {
    cutoff := now.Add(-t.window)
    for k, v := range t.lastAt {
        if v.Before(cutoff) {
            delete(t.lastAt, k)
        }
    }
}
```

---

## Resumen de impacto por archivo

| Archivo | Bugs corregidos |
|---|---|
| `internal/limiter/limiter.go` | #1 (doble DenyCount), #3 (reset al expirar) |
| `internal/common/common.go` | #4 (memory leak IPLogThrottle) |
| `cmd/guard-login/main.go` | #1 (doble DenyCount vía RecordDeny), #2 (live_limit) |
| `cmd/guard-game/main.go` | #1 (doble DenyCount vía RecordDeny), #2 (live_limit) |
| `cmd/guard/main.go` | #1 (doble DenyCount vía RecordDeny), #2 (live_limit), #4 (memory leak local) |

---

## Configuración recomendada post-fix

Con los bugs corregidos, los valores por defecto originales funcionan correctamente.
Sin embargo, se recomienda ajustar el tiempo de ban de firewall para evitar bloqueos excesivos en usuarios legítimos que disparen el límite accidentalmente:

```json
{
  "login": {
    "denies_before_tempblock": 10,
    "tempblock_seconds": 90,
    "firewall_block_seconds": 300
  },
  "game": {
    "denies_before_tempblock": 15,
    "tempblock_seconds": 60,
    "firewall_block_seconds": 300
  }
}
```

---

## Comportamiento esperado post-fix

| Escenario | Antes | Después |
|---|---|---|
| Usuario con reconexiones rápidas (rate limit) | Bloqueado tras 5 fallos | Bloqueado tras 10 fallos (correcto) |
| Usuario con 3ª conexión simultánea (live_limit) | Acumula DenyCount, posible tempblock | Solo rechazado, sin penalización |
| Retorno tras tempblock expirado | Posible rebloqueo inmediato | DenyCount limpio, período de gracia completo |
| DDoS con miles de IPs únicas | Memoria crece indefinidamente | Mapa limitado a ~10.000 entradas activas |
