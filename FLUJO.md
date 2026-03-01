# GUARD_GO — Flujo completo del sistema

---

## Topología general

```
┌─────────────────────────────────────────────────────────────────┐
│                        INTERNET                                  │
│                                                                  │
│   [Jugador directo]          [Jugador con relay]                 │
│   game.exe                   game.exe                            │
│      │                          │                                │
│      │                    127.0.0.1:17666/17667                  │
│      │                    [guard-relay.exe]                      │
│      │                          │                                │
│      └──────────┬───────────────┘                                │
│                 │  7666 (login)                                  │
│                 │  7669 (game)                                   │
│        ┌────────┴────────┐                                       │
│        │                 │                                       │
│   [VPS1 38.54.45.154]  [VPS2 45.235.98.209]                     │
│   guard-login :7666    guard-login :7666                         │
│   guard-game  :7669    guard-game  :7669                         │
│        │                 │                                       │
│        └────────┬─────────┘                                      │
│                 │  7666 (login backend)                          │
│                 │  7669 (game backend)                           │
│        [VPS3 45.235.99.117]                                      │
│        Servidor VB6 :7666/:7669                                  │
│        guard-panel  :7700 (solo localhost)                       │
└─────────────────────────────────────────────────────────────────┘
```

---

## 1. Conexión directa (sin relay)

```
game.exe ──── TCP ───→ VPS1:7666 ─────────────────→ VB6:7666  (login)
game.exe ──── TCP ───→ VPS1:7669 ─────────────────→ VB6:7669  (game)
```

**Paso a paso:**

```
1. game.exe abre TCP a 38.54.45.154:7666

2. guard-login en VPS1 recibe la conexión:
   ├─ ¿IP bloqueada temporalmente?  → rechaza, cierra
   ├─ ¿Token bucket vacío?          → rechaza, incrementa DenyCount
   ├─ ¿Conexiones vivas > 2?        → rechaza
   ├─ ¿Total conns > 2000?          → rechaza (modo drain si > 90%)
   └─ OK → abre TCP a 45.235.99.117:7666

3. guard-login actúa como tubo transparente:
   game.exe ←──── bytes ────→ VB6:7666
   (sin parsear el protocolo, solo copia bytes en ambas direcciones)

4. Al cerrar:
   ├─ Si fue normal → decrementa contador de conns activas
   └─ Si idle > 15s → guard cierra la conexión por timeout
```

---

## 2. Conexión con guard-relay

```
game.exe → 127.0.0.1:17666 → guard-relay.exe → VPS1:7666 → VB6:7666
game.exe → 127.0.0.1:17667 → guard-relay.exe → VPS1:7669 → VB6:7669
```

**Al iniciar guard-relay.exe:**

```
1. Carga relay.json (lista de VPS)

2. Prueba latencia TCP a todos los nodos en paralelo:
   ├─ VPS1 38.54.45.154:7666 → 7ms  ✓
   └─ VPS2 45.235.98.209:7666 → 23ms ✓

3. Selecciona el más rápido (VPS1)

4. Levanta listeners locales:
   ├─ 127.0.0.1:17666  (login)
   └─ 127.0.0.1:17667  (game)

5. Abre browser: http://127.0.0.1:17770  (UI del relay)

6. Cada 30s → heartbeat POST a http://38.54.45.154:7771/api/relay/ping
   └─ VPS1 lo registra → el panel muestra "Relays: 1"

7. Cada 60s → re-prueba latencia de todos los nodos
   └─ Si VPS1 falla o supera 500ms → cambia a VPS2 automáticamente
```

**UI del relay en el browser:**

```
┌─ GUARD RELAY ────────────────── ● CONECTADO ─┐
│                                               │
│  Proxy activo                                 │
│  ──────────────────────────────               │
│  VPS1 Principal                               │
│  Login    38.54.45.154:7666                   │
│  Juego    38.54.45.154:7669                   │
│  Latencia 7 ms                                │
│  Uptime   4m 32s                              │
│                                               │
│  Actividad                          [Limpiar] │
│  Probando 2 nodo(s)...                        │
│  Conectado a VPS1 | latencia: 7ms             │
│                                               │
│         [ ■ Detener relay ]                   │
└───────────────────────────────────────────────┘
```

---

## 3. Sistema de protección (qué pasa en un ataque)

```
Atacante envía 1000 conexiones/segundo a VPS1:7666
                        │
                        ▼
             guard-login recibe la conexión
                        │
          ┌─────────────┼─────────────────┐
          │             │                 │
    Token bucket   Conn activas    Total conns
    ¿tiene token?   ¿ <= 2?        ¿ <= 2000?
          │             │                 │
         NO            NO               NO
          │             │                 │
          └──────────┬──┘                 │
                     │          modo DRAIN (si > 90%)
                DenyCount++      cierra el listener
                     │           durante X segundos
          ¿DenyCount >= 10?
                     │
                    SI
                     │
            Bloqueo temporal
            (backoff exponencial)
            1er ban:   90s
            2do ban:  180s
            3er ban:  360s   ← BlockCount persiste
            4to ban:  720s      aunque expire el bloqueo
            ...
            máx:      24h
                     │
         enable_firewall_autoban = true
                     │
            netsh (goroutine async)
            crea regla en Windows Firewall
            por 900 segundos
                     │
            IP completamente bloqueada
            a nivel de SO (antes de llegar al guard)
```

---

## 4. Panel de administración (VPS3)

```
Admin abre browser en VPS3 → http://127.0.0.1:7700
                                      │
                              guard-panel.exe
                              lee nodes.json
                                      │
                    ┌─────────────────┴──────────────────┐
                    │                                     │
         GET http://38.54.45.154:7771/api/status   GET http://45.235.98.209:7771/api/status
         GET http://38.54.45.154:7772/api/status   GET http://45.235.98.209:7772/api/status
         GET .../api/metrics                        GET .../api/metrics
         GET .../api/events                         GET .../api/events
                    │                                     │
                    └─────────────────┬──────────────────┘
                                      │
                              panel renderiza:
```

```
┌─ VPS1 Principal ──────────────────────────────────────────────┐
│  LOGIN                              GAME                       │
│  Conns: 42/2000  ██░░░░ 2%          Conns: 187/4000 ████░ 5%  │
│  IPs rastreadas: 156                IPs rastreadas: 203        │
│  Rechazos/s: 0.2                    Rechazos/s: 0.0            │
│  Relays: 3                                                     │
│  [sparkline 6min]                   [sparkline 6min]           │
│                                                                │
│  IPs rastreadas  [Filtrar IP...]                               │
│  IP              Conns  Blqs  Estado                           │
│  1.2.3.4         1      0     ok                               │
│  5.6.7.8         0      3     BLOQUEADA 12:43 restante         │
│                                                                │
│  Eventos recientes                                             │
│  14:23:01  TEMPBLOCK  5.6.7.8  (ban #3, 360s)                 │
│  14:22:45  FW_BLOCK   5.6.7.8                                  │
│  14:20:11  DRAIN_ON   load=92%                                 │
│  14:20:41  DRAIN_OFF  duración=30s                             │
│                                                                │
│  [Bloquear IP]  [Desbloquear]  [Desbloquear todos]             │
│  [Test Conectividad]                                           │
└───────────────────────────────────────────────────────────────┘
```

---

## 5. ¿Qué pasa si VPS1 cae?

```
Jugadores conectados a VPS1 ──→ conexión se corta (inevitable)

guard-relay detecta en 60s que VPS1 no responde
    └─→ cambia a VPS2 automáticamente
         └─→ nuevas conexiones van por VPS2

Jugadores directos sin relay → deben reconectarse a VPS2 manualmente
(o si hay HAProxy delante, el failover es automático también para ellos)
```

---

## Resumen de puertos y quién habla con quién

```
Jugador           →  VPS1/VPS2 :7666  (login proxy, abierto a todos)
Jugador           →  VPS1/VPS2 :7669  (game proxy,  abierto a todos)
guard-relay       →  VPS1/VPS2 :7771  (heartbeat,   abierto a todos con Bearer)
guard-panel(VPS3) →  VPS1/VPS2 :7771  (admin login, requiere Bearer)
guard-panel(VPS3) →  VPS1/VPS2 :7772  (admin game,  solo desde 45.235.99.117)
VPS1/VPS2         →  VPS3      :7666  (backend login VB6, solo desde VPS1/VPS2)
VPS1/VPS2         →  VPS3      :7669  (backend game VB6,  solo desde VPS1/VPS2)
Admin (RDP VPS3)  →  localhost :7700  (panel web,   solo localhost)
```
