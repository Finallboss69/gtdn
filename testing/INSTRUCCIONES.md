# GUARD_GO - Setup de produccion con 3 VPS

## IPs configuradas en estos archivos

| Rol | IP |
|-----|----|
| VPS1 - Proxy principal | `38.54.45.154` |
| VPS2 - Proxy backup | `45.235.98.209` |
| VPS3 - Servidor del juego + Panel | `45.235.99.117` |

**Token admin:** `G9xmK7pQ2wRs4Tn`
> Si queres cambiarlo, cambiarlo en los 3 `config.json` y en `nodes.json` al mismo tiempo.

---

## Que archivos van en cada VPS

### VPS1 - copiar TODA la carpeta `testing/vps1-proxy/` a `C:\guard\`

| Archivo | Descripcion |
|---------|-------------|
| `guard-login.exe` | Proxy de login con rate limiting |
| `guard-game.exe` | Proxy de juego con rate limiting |
| `config.json` | Configuracion (backend, puertos, token) |
| `nssm.exe` | Ya incluido - gestor de servicios Windows |
| `instalar.ps1` | Script de instalacion |
| `_estado.ps1` | Script de diagnostico y reparacion auto |
| `1-INSTALAR.bat` | Acceso directo: instalar |
| `2-ESTADO.bat` | Acceso directo: diagnostico |
| `3-REINICIAR.bat` | Acceso directo: reiniciar servicios |
| `4-LOGS.bat` | Acceso directo: ver logs en vivo |
| `5-DESINSTALAR.bat` | Acceso directo: desinstalar |

### VPS2 - copiar TODA la carpeta `testing/vps2-proxy/` a `C:\guard\`

Identico a VPS1 pero con `config.json` especifico de VPS2.

### VPS3 - copiar TODA la carpeta `testing/vps3-juego/` a `C:\guard\`

| Archivo | Descripcion |
|---------|-------------|
| `guard-panel.exe` | Panel web de administracion |
| `nodes.json` | Lista de nodos VPS1 y VPS2 |
| `nssm.exe` | Ya incluido - gestor de servicios Windows |
| `instalar.ps1` | Script de instalacion |
| `_estado.ps1` | Script de diagnostico y reparacion auto |
| `1-INSTALAR.bat` | Acceso directo: instalar |
| `2-ESTADO.bat` | Acceso directo: diagnostico |
| `3-REINICIAR.bat` | Acceso directo: reiniciar servicio |
| `4-LOGS.bat` | Acceso directo: ver log en vivo |
| `5-DESINSTALAR.bat` | Acceso directo: desinstalar |

---

## Orden de instalacion

```
1. VPS3 primero  ->  2. VPS1  ->  3. VPS2  ->  4. Verificar panel
```

Empezar por VPS3 porque los proxies van a intentar conectarse al backend
apenas levanten.

---

## Paso a paso (usando los bat)

### 1. Conectate a VPS3 por RDP

1. Copia toda la carpeta `testing/vps3-juego/` a `C:\guard\` en el VPS
2. Hacele doble clic a **`1-INSTALAR.bat`** (pide admin automaticamente)
3. Al final tenes que ver `Status: Running` para `GuardPanel`

### 2. Conectate a VPS1 por RDP

1. Copia toda la carpeta `testing/vps1-proxy/` a `C:\guard\` en el VPS
2. Hacele doble clic a **`1-INSTALAR.bat`** (pide admin automaticamente)
3. Al final tenes que ver `Status: Running` para `GuardLogin` y `GuardGame`

### 3. Conectate a VPS2 por RDP

Exactamente igual que VPS1, pero con la carpeta `testing/vps2-proxy/`.

### 4. Verificar el panel

1. En VPS3, abri el navegador (Edge/Chrome)
2. Entra a: `http://127.0.0.1:7700`
3. Tenes que ver los dos nodos en **verde**
4. Si alguno esta en rojo, usar el boton **"Test Conectividad"** en el panel
   para ver exactamente donde falla (TCP, HTTP, o autenticacion)

---

## Verificacion rapida desde PowerShell

En VPS1 o VPS2:
```powershell
# Ver si los servicios corren
Get-Service GuardLogin, GuardGame

# Ver si los puertos estan abiertos
netstat -ano | findstr "7666"
netstat -ano | findstr "7669"

# Ver los logs en vivo
Get-Content C:\guard\guard-login.log -Wait -Tail 20
Get-Content C:\guard\guard-game.log  -Wait -Tail 20
```

En VPS3:
```powershell
# Ver si el panel corre
Get-Service GuardPanel

# Probar la API del panel
Invoke-WebRequest -Uri "http://127.0.0.1:7700/api/nodes" | Select-Object -ExpandProperty Content

# Ver el log en vivo
Get-Content C:\guard\log-panel.txt -Wait -Tail 20
```

---

## Si algo falla

**Panel muestra nodo en rojo:**
- Hacer clic en "Test Conectividad" en el panel - muestra TCP y HTTP por separado
- Si TCP falla: firewall del VPS1/VPS2 bloqueando el puerto 7771/7772
- Si HTTP falla con 403: problema de token o de IP en admin_allow_ips
- El token tiene que ser exactamente `G9xmK7pQ2wRs4Tn` en todos los archivos

**Los jugadores se conectan pero el juego no responde:**
- Verificar que el servidor VB6 esta corriendo en VPS3 en los puertos 7666 (login) y 7669 (game)
- Ejecutar en VPS3: `netstat -ano | findstr "766"` - tiene que mostrar `LISTENING` para 7666 y 7669
- Verificar firewall de VPS3: solo acepta en 7666 y 7669 desde VPS1 y VPS2

**Servicio no arranca (se ve en 2-ESTADO.bat):**
- Hacer doble clic en **4-LOGS.bat** para ver los logs en vivo
- O ejecutar el exe manualmente en PowerShell:
  ```powershell
  cd C:\guard
  .\guard-login.exe -config config.json -profile login
  ```

**Error de firewall en autoban:**
Los servicios corren como `LocalSystem` (configurado en instalar.ps1).
Si sigue fallando, verificar que el servicio tiene `ObjectName = LocalSystem`:
```powershell
sc.exe qc GuardLogin
```

**Desinstalar para empezar de cero:**
Hacer doble clic en **5-DESINSTALAR.bat** en el VPS correspondiente.
O desde PowerShell:
```powershell
# VPS1 / VPS2
Stop-Service GuardLogin, GuardGame -Force
sc.exe delete GuardLogin
sc.exe delete GuardGame
Remove-NetFirewallRule -DisplayName "Guard*"

# VPS3
Stop-Service GuardPanel -Force
C:\guard\nssm.exe remove GuardPanel confirm
Remove-NetFirewallRule -DisplayName "Guard*"
```

---

## Puertos abiertos al final

| VPS | Puerto | Para que |
|-----|--------|----------|
| VPS1 | 7666 | Jugadores (proxy login) |
| VPS1 | 7669 | Jugadores (proxy game) |
| VPS1 | 7771 | Panel + heartbeats guard-relay (cualquier IP) |
| VPS1 | 7772 | Panel (solo desde 45.235.99.117) |
| VPS2 | 7666 | Jugadores (proxy login) |
| VPS2 | 7669 | Jugadores (proxy game) |
| VPS2 | 7771 | Panel + heartbeats guard-relay (cualquier IP) |
| VPS2 | 7772 | Panel (solo desde 45.235.99.117) |
| VPS3 | 7666 | Login VB6 (solo desde VPS1 y VPS2) |
| VPS3 | 7669 | Game VB6 (solo desde VPS1 y VPS2) |
| VPS3 | 7700 | Panel web (solo localhost - abrir con navegador via RDP) |
