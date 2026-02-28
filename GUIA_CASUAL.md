# Guía Casual — GUARD_GO para no programadores

> Esta guía explica todo como si nunca hubieras escuchado las palabras "proxy", "DDoS" o "firewall".

---

## ¿Para qué sirve GUARD_GO?

Imaginate que tu servidor de juego es una **disco**. Tiene una puerta de entrada. Cuando hay una fiesta popular, puede llegar muchísima gente al mismo tiempo. Algunos vienen a bailar (jugadores reales), pero algunos vienen a armar quilombo (hackers que quieren tirar el server).

**GUARD_GO es el patovica en la puerta.** Su trabajo:

- ✅ Dejar entrar solo a quienes se portan bien
- ❌ Sacar a patadas a los que intentan hacer algo raro
- 🚪 Si hay demasiada gente haciendo lío, cerrar la puerta un momento para que los que ya están adentro puedan seguir jugando

---

## ¿Qué es un ataque DDoS?

Es cuando alguien manda **miles o millones de pedidos de conexión** a tu servidor al mismo tiempo, con el objetivo de "llenarlo" y que los jugadores reales no puedan entrar, o que el servidor se caiga.

```
Sin GUARD_GO:   [Atacante: 10.000 conexiones/seg] → [Tu servidor VB6 💀]

Con GUARD_GO:   [Atacante: 10.000 conexiones/seg] → [GUARD_GO filtra] → [Tu servidor VB6 ✅]
```

El servidor VB6 nunca ve al atacante. Solo ve conexiones legítimas.

---

## ¿Qué es un "proxy"?

Es un **intermediario**. El jugador se conecta al proxy (GUARD_GO), el proxy revisa si está OK, y si pasa, lo conecta al servidor real. El servidor real nunca ve directamente la IP del atacante.

```
[Jugador] → [GUARD_GO proxy] → [Tu servidor del juego]
```

---

## ¿Por qué usar múltiples proxies?

Si tenés **un solo proxy** y alguien lo ataca específicamente a él (no al servidor del juego), ese proxy puede caerse. Los jugadores quedan sin poder entrar aunque el servidor del juego esté funcionando perfecto.

Con **dos o más proxies**, si uno cae, el otro sigue funcionando. Los jugadores ni se dan cuenta.

```
Es como tener dos puertas de entrada a la disco.
Si bloquean una puerta, entrás por la otra.
```

---

## La arquitectura completa explicada

```
                     ┌─────────────────┐
JUGADORES ──────────▶│   BALANCEADOR   │
(desde internet)     │   (HAProxy)     │
                     │  185.bal.ip     │
                     └────────┬────────┘
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
         ┌─────────┐    ┌─────────┐    ┌─────────┐
         │  VPS 1  │    │  VPS 2  │    │  VPS 3  │
         │ GUARD   │    │ GUARD   │    │ GUARD   │
         │ :7666   │    │ :7666   │    │ :7666   │
         └────┬────┘    └────┬────┘    └────┬────┘
              └──────────────┼──────────────┘
                             ▼
                     ┌───────────────┐
                     │ TU SERVIDOR   │
                     │    VB6        │
                     │ :7668 / :7669 │
                     └───────────────┘
```

**Glosario de esta arquitectura:**

| Parte | ¿Qué es? | Ejemplo |
|-------|----------|---------|
| Jugadores | Los que se conectan al juego | 500 jugadores online |
| Balanceador (HAProxy) | El semáforo que reparte jugadores entre VPS | Si VPS1 cae, manda todos a VPS2 |
| VPS 1, 2, 3... | Cada servidor con GUARD_GO instalado | VPS en Francia, Alemania, USA |
| Tu servidor VB6 | El juego real | El que tenías antes |

---

## ¿Qué es HAProxy?

Es el **semáforo** de la analogía. Recibe a todos los jugadores y los reparte entre los proxies disponibles. Lo más importante: si un proxy se cae, **automáticamente** deja de mandarle jugadores y los manda a los que siguen funcionando. Los jugadores no notan nada.

---

## ¿Cuánto mejora la seguridad?

| Escenario | Sin GUARD_GO | Con GUARD_GO |
|-----------|-------------|--------------|
| Ataque de 10.000 conexiones/seg | Tu servidor VB6 colapsa en segundos | GUARD_GO rechaza todo, servidor intacto |
| IP que intenta conectarse 100 veces | Tu servidor la atiende todas | GUARD_GO la banea automáticamente |
| IP que reincide después del ban | Puede seguir intentando | Cada nuevo ban dura el doble (1x → 2x → 4x → 8x → 16x → hasta 24 horas) |
| VPS que cae bajo ataque | Todos los jugadores sin servicio | Los otros proxies siguen funcionando |

---

## ¿Qué hace el panel de administración?

Es el **tablero de control del patovica jefe**. Desde una página web (solo accesible desde tu PC) podés ver:

- Cuántos jugadores están conectados en **cada VPS** por separado
- Quiénes están baneados y por cuánto tiempo
- Si algún proxy está bajo ataque (aparece "DRAIN" o "CARGA ALTA")
- Un historial de eventos: quién fue baneado, cuándo, por qué
- Desbloquear a alguien de forma manual si fue baneado por error

---

## Glosario completo en palabras simples

| Término técnico | En palabras simples |
|----------------|---------------------|
| **Guard proxy** | El patovica de la puerta |
| **Backend** | El servidor del juego real (VB6) |
| **Rate limiting** | "Solo podés intentar conectarte X veces por segundo" |
| **Tempblock** | Suspensión temporal: "Fuera por 90 segundos" |
| **Backoff exponencial** | Cada vez que reincidís, el ban dura el doble |
| **Modo DRAIN** | "Cerré la puerta temporalmente, los que están adentro que terminen" |
| **AutoBan (Firewall)** | Lista negra en el sistema operativo: el atacante no puede ni tocar el servidor |
| **Balanceador (HAProxy)** | El semáforo que reparte a los jugadores entre los proxies |
| **Failover** | Cambio automático al proxy de backup cuando uno cae |
| **Panel de administración** | El tablero de control del patovica jefe |
| **Token** | Contraseña para que el panel pueda comunicarse con cada proxy |
| **VPS** | Servidor virtual alquilado en la nube (Windows Server) |
| **Nodo** | Cada VPS que tiene un GUARD_GO instalado |

---

## Instalación paso a paso (en cada VPS)

### Lo que necesitás por VPS:
- Windows Server 2019 o 2022 (puede ser 2016 también)
- Los archivos: `guard-login.exe`, `guard-game.exe`, `config.json`
- NSSM (para que arranquen automáticamente con Windows)

### Paso 1: Copiar archivos
Creá la carpeta `C:\guard` y copiá adentro:
- `guard-login.exe`
- `guard-game.exe`
- `config.json` (con la IP de tu servidor VB6 en `backend_addr`)

### Paso 2: Editar config.json
El campo importante es `backend_addr`. Cambialo para que apunte a tu servidor VB6:

```json
{
  "login": {
    "backend_addr": "185.tu.server.ip:7668",
    "admin_listen_addr": "0.0.0.0:7771",
    "admin_allow_ips": ["185.tu.panel.ip"],
    "admin_token": "inventate-una-contraseña-difícil"
  },
  "game": {
    "backend_addr": "185.tu.server.ip:7669",
    "admin_listen_addr": "0.0.0.0:7772",
    "admin_allow_ips": ["185.tu.panel.ip"],
    "admin_token": "inventate-una-contraseña-difícil"
  }
}
```

> ⚠️ **Importante:** `admin_token` tiene que ser la misma contraseña en todas las VPS, y la misma que ponés en `nodes.json`. Es como la llave del tablero de control.

### Paso 3: Instalar como servicio (con NSSM)

Descargá NSSM de https://nssm.cc y copiá `nssm.exe` a `C:\guard`. Luego ejecutá en PowerShell como Administrador:

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

### Paso 4: Abrir puertos en el Firewall de Windows

Ejecutá en PowerShell como Administrador:

```powershell
# Puertos públicos (los jugadores se conectan acá)
New-NetFirewallRule -DisplayName "Guard Login Public"  -Direction Inbound -Protocol TCP -LocalPort 7666 -Action Allow
New-NetFirewallRule -DisplayName "Guard Game Public"   -Direction Inbound -Protocol TCP -LocalPort 7667 -Action Allow

# Puerto del panel admin (solo desde tu IP del panel)
New-NetFirewallRule -DisplayName "Guard Admin Login" -Direction Inbound -Protocol TCP -LocalPort 7771 -RemoteAddress "185.tu.panel.ip" -Action Allow
New-NetFirewallRule -DisplayName "Guard Admin Game"  -Direction Inbound -Protocol TCP -LocalPort 7772 -RemoteAddress "185.tu.panel.ip" -Action Allow

# Bloquear puertos internos desde internet (¡importante!)
New-NetFirewallRule -DisplayName "Block Backend Login" -Direction Inbound -Protocol TCP -LocalPort 7668 -Action Block
New-NetFirewallRule -DisplayName "Block Backend Game"  -Direction Inbound -Protocol TCP -LocalPort 7669 -Action Block
```

### Paso 5: Verificar que funciona
```powershell
Get-Service GuardLogin, GuardGame
# Debe mostrar: Status: Running
```

---

## Instalación del balanceador HAProxy (Linux)

HAProxy va en una VPS Linux pequeña (Ubuntu 22.04). Puede ser la misma VPS del backend si querés.

```bash
sudo apt update && sudo apt install haproxy -y
sudo nano /etc/haproxy/haproxy.cfg
```

Pegá esta configuración (reemplazando las IPs):

```haproxy
global
    log /dev/log local0
    maxconn 10000

defaults
    mode tcp
    option tcplog
    retries 3
    timeout connect 5s
    timeout client 60s
    timeout server 60s

frontend login_front
    bind *:7666
    default_backend login_back

backend login_back
    balance roundrobin
    option tcp-check
    server vps1 185.vps1.ip:7666 check fall 3 rise 2
    server vps2 185.vps2.ip:7666 check fall 3 rise 2

frontend game_front
    bind *:7667
    default_backend game_back

backend game_back
    balance roundrobin
    option tcp-check
    server vps1 185.vps1.ip:7667 check fall 3 rise 2
    server vps2 185.vps2.ip:7667 check fall 3 rise 2
```

```bash
sudo systemctl enable haproxy
sudo systemctl restart haproxy
```

---

## Configurar el panel para ver todos los nodos

En la PC donde corre `guard-panel.exe`, copiá `nodes.json.example` a `nodes.json` y editalo:

```json
{
  "nodes": [
    {
      "id":        "vps1",
      "name":      "VPS1 — Francia",
      "login_url": "http://185.vps1.ip:7771",
      "game_url":  "http://185.vps1.ip:7772",
      "token":     "inventate-una-contraseña-difícil"
    },
    {
      "id":        "vps2",
      "name":      "VPS2 — Alemania",
      "login_url": "http://185.vps2.ip:7771",
      "game_url":  "http://185.vps2.ip:7772",
      "token":     "inventate-una-contraseña-difícil"
    }
  ]
}
```

Arrancar el panel:
```bash
guard-panel.exe -nodes nodes.json
```

Abrir en el navegador: `http://127.0.0.1:7700`

Vas a ver una grilla con todos tus nodos. Verde = online, rojo = offline.

---

## ¿Qué pasa si una VPS cae?

1. HAProxy detecta que no responde (después de 3 checks, ~15 segundos)
2. HAProxy deja de mandarle jugadores
3. Los jugadores nuevos van automáticamente a las VPS que siguen online
4. Los jugadores que ya estaban conectados siguen conectados (si la VPS cayó limpiamente)
5. En el panel vas a ver ese nodo en rojo

Cuando la VPS vuelve a funcionar:
1. HAProxy detecta que responde (después de 2 checks exitosos)
2. Empieza a mandarle jugadores de nuevo

**Todo automático, sin que vos hagas nada.**

---

## Monitoreo con UptimeRobot (gratis)

Para recibir alertas por email/Telegram cuando una VPS cae:

1. Registrarse en https://uptimerobot.com (gratis)
2. Agregar monitor tipo "TCP Port"
3. Configurar: `185.vps1.ip` puerto `7666`
4. Repetir para cada VPS y cada puerto (7666 y 7667)
5. Configurar notificaciones a tu email o Telegram

Cuando una VPS deja de responder, UptimeRobot te avisa en segundos.

---

## Preguntas frecuentes

**¿El token tiene que ser muy difícil?**
Sí, usá algo como `j8Kf#2mP$9xQr` (letras, números y símbolos). Tiene que ser el mismo en todas las VPS y en `nodes.json`.

**¿Puedo tener solo una VPS?**
Sí. Si no usás HAProxy y no tenés `nodes.json`, todo funciona igual que antes, con una sola VPS.

**¿El balanceador (HAProxy) es un punto único de fallo?**
Técnicamente sí. Pero el balanceador es mucho más difícil de tirar que un guard, ya que no procesa ningún protocolo de juego. Para máxima redundancia podés usar DNS con dos IPs (Cloudflare) o un balanceador cloud (AWS ELB, etc.).

**¿Los bans se comparten entre VPS?**
No. Cada VPS tiene su propia lista de baneados. Pero si un atacante está baneado en VPS1 y el balanceador lo manda a VPS2, se banea rápidamente en VPS2 también (en segundos).

**¿Puedo poner hasta 20 VPS?**
Sí. Solo agregás más entradas en `haproxy.cfg` y en `nodes.json`. El panel soporta hasta 20 nodos.
