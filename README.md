# Tótem Wi-Fi

Un programa que muestra en un tótem (una pantalla con navegador) un botón **«Obtener código»**. Al
pulsarlo, pide a UniFi un **voucher del hotspot** y lo muestra en grande junto a un **código QR** para
unirse a la red `WifiPublica`. El ciudadano se conecta, abre el navegador y escribe el código en
el portal cautivo de UniFi.

- Es **un solo ejecutable**: no hay que instalar Python, Node, bases de datos ni nada más.
- Funciona **sin Internet**: fuentes, estilos y QR van dentro del ejecutable.
- La **API key de UniFi solo la conoce este servidor**: los tótems nunca la ven.
- Tras instalarlo arranca en **modo simulación**: da códigos falsos y muestra una franja amarilla
  «MODO PRUEBA» hasta que se configure UniFi.

---

## Índice

1. [Qué hay en el paquete](#1-qué-hay-en-el-paquete)
2. [Instalación en Ubuntu Server](#2-instalación-en-ubuntu-server)
3. [Instalación en Windows (alternativa)](#3-instalación-en-windows-alternativa)
4. [Primer acceso a la administración](#4-primer-acceso-a-la-administración)
5. [Crear la API key en UniFi](#5-crear-la-api-key-en-unifi)
6. [Pasar de simulación a real](#6-pasar-de-simulación-a-real)
7. [Configurar los tótems](#7-configurar-los-tótems)
8. [Ver el log](#8-ver-el-log)
9. [Cambiar (rotar) la API key](#9-cambiar-rotar-la-api-key)
10. [Si UniFi cambia de certificado](#10-si-unifi-cambia-de-certificado)
11. [Diagnóstico de problemas](#11-diagnóstico-de-problemas)
12. [Actualizar a una versión nueva](#12-actualizar-a-una-versión-nueva)
13. [Desinstalar](#13-desinstalar)
14. [Referencia de la configuración](#14-referencia-de-la-configuración)
15. [Línea de comandos](#15-línea-de-comandos)
16. [Para desarrolladores](#16-para-desarrolladores)

---

## 1. Qué hay en el paquete

| Paquete | Para | Contenido |
|---|---|---|
| `totem-wifi_<versión>_windows_amd64.zip` | Windows 10/11 o Windows Server de 64 bits | `totem-wifi.exe`, `install.ps1`, `uninstall.ps1`, este README |
| `totem-wifi_<versión>_linux_amd64.tar.gz` | Ubuntu/Debian en PC o servidor normal | `totem-wifi`, `install.sh`, `uninstall.sh`, este README |
| `totem-wifi_<versión>_linux_arm64.tar.gz` | Ubuntu en ARM (por ejemplo, Raspberry Pi 4/5 de 64 bits) | ídem |

`SHA256SUMS.txt` permite comprobar que el paquete ha llegado íntegro.

Dónde queda cada cosa una vez instalado:

| | Windows | Ubuntu |
|---|---|---|
| Programa | `C:\Program Files\TotemWifi\totem-wifi.exe` | `/opt/totem-wifi/totem-wifi` |
| Configuración (`config.json`) | `C:\ProgramData\TotemWifi\config.json` | `/etc/totem-wifi/config.json` |
| Contador diario (`state.json`), log y logotipo | `C:\ProgramData\TotemWifi\` | `/var/lib/totem-wifi/` |
| Nombre del servicio | `TotemWifi` | `TotemWifi` (systemd) |

> `config.json` contiene la API key y la contraseña de administración (cifrada). Los instaladores lo
> dejan legible solo por el servicio y por los administradores del equipo.

---

## 2. Instalación en Ubuntu Server

Cada sede tiene su propio servidor Ubuntu con su propia instalación, que apunta a **su** UniFi OS Server
y atiende a **sus** tótems. Los pasos son los mismos en todas; solo cambian los datos de la sede.

1. Copie el `.tar.gz` al servidor (por ejemplo, con `scp` desde su PC):

   ```bash
   scp totem-wifi_1.0.0_linux_amd64.tar.gz usuario@IP_DEL_SERVIDOR:
   ```

2. En el servidor, descomprímalo y entre en la carpeta:

   ```bash
   tar xzf totem-wifi_1.0.0_linux_amd64.tar.gz
   ```

   ```bash
   cd totem-wifi_1.0.0_linux_amd64
   ```

3. Instale indicando los datos de la sede. Todos son opcionales y se pueden cambiar después en
   `/admin`:

   | Opción | Qué es | Ejemplo |
   |---|---|---|
   | `--unifi-url` | UniFi OS Server de la sede | `https://192.0.2.10:11443` |
   | `--totems` | IPs o redes de los tótems, separadas por comas | `192.0.2.0/24` |
   | `--admin-ips` | Su PC, para abrir `/admin` sin túnel SSH | `192.0.2.50` |

   Ejemplo para la **Sede A**:

   ```bash
   sudo ./install.sh --unifi-url https://192.0.2.10:11443 --totems 192.0.2.0/24
   ```

   Ejemplo para la **Sede B**:

   ```bash
   sudo ./install.sh --unifi-url https://198.51.100.20:11443 --totems 198.51.100.0/24
   ```

   El instalador crea el usuario de sistema `totem`, copia el programa a `/opt/totem-wifi`, crea la
   configuración, registra el servicio systemd y lo arranca.

4. Siga en [4. Primer acceso a la administración](#4-primer-acceso-a-la-administración).

Comprobar que el servicio está en marcha:

```bash
systemctl status TotemWifi
```

### Cortafuegos (ufw)

Si el servidor tiene ufw, el instalador añade un **ayudante** que mantiene el cortafuegos al día: cada
vez que se guardan en `/admin` cambios en los equipos permitidos, abre el puerto 8080 **solo** a esos
equipos. El resultado se ve en `/admin` → *Estado* → *Cortafuegos (ufw)*.

- Solo crea y borra sus propias reglas, marcadas como «Totem Wi-Fi». Nunca toca las demás (SSH queda
  a salvo) y **nunca activa ni desactiva ufw**.
- Si ufw está desactivado, deja las reglas preparadas y `/admin` lo indica. Para activarlo, permita
  antes SSH para no perder el acceso:

  ```bash
  sudo ufw allow OpenSSH
  ```

  ```bash
  sudo ufw enable
  ```

- El servicio web sigue sin privilegios: el ayudante es un servicio aparte
  (`totem-wifi-firewall`) que systemd lanza como root cuando cambia `config.json`.
- Si no quiere que se gestione ufw, instale con `--no-ufw`.

---

## 3. Instalación en Windows (alternativa)

El sistema elegido es Ubuntu Server; esta sección queda por si alguna sede necesitara Windows 10/11
o Windows Server.

1. Copie el `.zip` al equipo y **descomprímalo** (botón derecho → *Extraer todo…*).
2. Abra **PowerShell como administrador** (menú Inicio → escriba *PowerShell* → botón derecho →
   *Ejecutar como administrador*).
3. Vaya a la carpeta descomprimida (cambie la ruta por la suya):

   ```powershell
   cd "$env:USERPROFILE\Downloads\totem-wifi_1.0.0_windows_amd64"
   ```

4. Ejecute el instalador. Copia el programa, registra el servicio, abre el puerto 8080 en el
   Firewall **solo** para los equipos permitidos, lo arranca y abre la administración en el navegador:

   ```powershell
   powershell -ExecutionPolicy Bypass -File .\install.ps1
   ```

5. Siga en [4. Primer acceso a la administración](#4-primer-acceso-a-la-administración).

Comprobar que el servicio está en marcha:

```powershell
Get-Service TotemWifi
```

> **Firewall.** En Windows la regla «Totem Wi-Fi (HTTP)» no se actualiza sola: se crea con las IPs
> que haya al instalar. Si después cambia las listas en `/admin`, vuelva a ejecutar `install.ps1`
> (conserva toda la configuración).

---

## 4. Primer acceso a la administración

La administración está en `http://localhost:8080/admin` y, por seguridad, **solo se puede abrir desde
el propio servidor** (lista `admin_allowed_clients`, que por defecto es `127.0.0.1`).

- **Ubuntu sin escritorio**: abra un túnel SSH desde su PC y visite `http://localhost:8080/admin` en
  el navegador de su PC (el túnel hace que su PC «sea» el servidor, y además cifra la conexión):

  ```bash
  ssh -L 8080:localhost:8080 usuario@IP_DEL_SERVIDOR
  ```

- **Windows**: el instalador ya la abre en el navegador.

La primera vez pedirá **crear la contraseña** de administración (mínimo 10 caracteres). Después verá
el panel con el estado y todos los ajustes.

Si quiere administrar desde su PC sin túnel, añada su IP en **Acceso → Equipos que pueden abrir
/admin** (o use `--admin-ips` al instalar). Tenga en cuenta que sin túnel la contraseña viaja sin
cifrar por la red local.

**Cambios en «Acceso».** Para cambiar los equipos permitidos o el puerto, `/admin` pide **volver a
escribir la contraseña de administración**, porque esos cambios también modifican el cortafuegos.
Tampoco deja guardar una lista de administración que excluya al equipo desde el que está conectado,
para que no se quede sin acceso.

Ajustes que conviene revisar en el primer acceso:

- **Equipos que pueden usar el quiosco**: las IPs o redes de los tótems (por defecto `127.0.0.1` y
  `192.0.2.0/24`). Un equipo que no esté en la lista recibe «Acceso no permitido».
- **Duración**, **dispositivos por código** y límites del voucher (por defecto 4 h y 1 dispositivo).
- **SSID**: por defecto `WifiPublica`. Si añade SSID adicionales, el quiosco mostrará un
  selector de red.

---

## 5. Crear la API key en UniFi

Hay un UniFi OS Server por sede. Los vouchers solo valen en el servidor que gestiona el portal cautivo
de la red de esa sede (en el ejemplo, el portal del SSID público está en el de la **Sede B**):

| Sede | URL |
|---|---|
| Sede B | `https://198.51.100.20:11443` |
| Sede A | `https://192.0.2.10:11443` |

Cada sede usa la API key de **su** servidor: una clave de un servidor no sirve en el otro.

La clave tiene los mismos permisos que el usuario de UniFi que la crea. Lo recomendable es un usuario
dedicado solo al tótem, porque así se puede revocar sin afectar a nadie y en UniFi queda claro qué hizo
el tótem:

1. En UniFi OS Server, en **Admins & Users**, cree un administrador **local**, por ejemplo
   `totem-api`. No use una cuenta de ui.com. Póngale una contraseña larga.
2. Dele acceso **solo a la aplicación Network**, sin Protect, Access ni otras.
3. En Network, dele el permiso más bajo que permita **administrar** la red (suele llamarse
   «Full Management» o «Admin»). No sirven «Hotspot» ni «Solo lectura» (*View Only*): crear vouchers
   requiere permiso de escritura.
4. Entre en UniFi con ese usuario y abra **Network → Settings → Control Plane → Integrations** (en
   algunas versiones, directamente *Integrations*). Cree una clave con un nombre reconocible, por
   ejemplo «Tótem Wi-Fi», y **cópiela**: UniFi solo la muestra una vez.
5. Péguela en `/admin` (sección *UniFi OS Server*). Si «Probar conexión con UniFi» responde **403**,
   el permiso del usuario se queda corto.

Estas instrucciones también aparecen en `/admin`, bajo el campo de la clave («¿Cómo crear la clave?»).

Comprobación opcional desde cualquier equipo con `curl` (debe devolver la lista de sitios):

```bash
curl -sk -H "X-API-KEY: LA_CLAVE" https://198.51.100.20:11443/proxy/network/integration/v1/sites
```

---

## 6. Pasar de simulación a real

En `/admin`, sección **UniFi OS Server**:

1. **URL del servidor**: elija la de la sede donde está el portal del SSID público.
2. **API key**: pegue la clave del paso anterior.
3. Pulse **«Obtener huella del certificado»**. UniFi usa un certificado autofirmado; el programa
   guarda su huella SHA-256 y a partir de entonces **solo acepta ese certificado** (si alguien
   suplantara al servidor, no le enviaría la clave). La huella se copia sola en su campo.
   Si quiere asegurarse, compárela con la que muestra el navegador del propio servidor de UniFi
   (candado → certificado → huella SHA-256).
4. **Desmarque «Modo simulación»** y pulse **Guardar**.
5. Pulse **«Probar conexión con UniFi»**. Debe decir «Conexión correcta» y el sitio detectado
   (normalmente «Default»; se guarda solo).
6. Pulse **«Generar voucher de prueba»**: crea un voucher **real** con la nota
   «Totem (prueba admin)» que puede comprobar en UniFi (*Hotspot → Vouchers*) y borrar.

Desde la consola se puede hacer la misma comprobación, capa por capa
(Windows, en PowerShell como administrador):

```powershell
& "C:\Program Files\TotemWifi\totem-wifi.exe" check
```

Ubuntu:

```bash
sudo /opt/totem-wifi/totem-wifi check
```

`check` termina con «Todo correcto» o indica en qué capa falla (ver
[11. Diagnóstico](#11-diagnóstico-de-problemas)). No modifica la configuración.

También se puede ver la huella de un servidor sin abrir `/admin`:

```bash
totem-wifi fingerprint 198.51.100.20:11443
```

---

## 7. Configurar los tótems

En cada tótem basta un navegador Chrome o Edge en **modo quiosco** apuntando al servidor. Sustituya
`IP_SERVIDOR` por la IP del equipo donde está instalado Tótem Wi-Fi (o `localhost` si el tótem es el
propio servidor), y asegúrese de que la IP del tótem está en **Equipos que pueden usar el quiosco**.

**Windows** (cree un acceso directo en la carpeta *Inicio* del usuario del tótem con este destino):

```text
"C:\Program Files\Google\Chrome\Application\chrome.exe" --kiosk http://IP_SERVIDOR:8080/ --incognito --noerrdialogs --disable-pinch --overscroll-history-navigation=0
```

Con Edge, cambie la ruta por `"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe"`.

**Linux con escritorio**:

```bash
chromium --kiosk http://IP_SERVIDOR:8080/ --incognito --noerrdialogs --disable-pinch --overscroll-history-navigation=0
```

La pantalla bloquea el menú contextual, la selección, el zoom y el arrastre, borra el código al
terminar la cuenta atrás y **se recarga sola** cuando se guarda un cambio en `/admin` (textos,
logotipo, fondo…; lo comprueba cada minuto y solo en la pantalla de inicio, nunca con un código a la
vista) y, además, cada día a las 04:00.

### Logotipo y fondo

En `/admin` → **Logotipo y fondo**:

1. Pulse **Examinar / Seleccionar archivo** bajo «Logotipo» o «Foto de fondo» y elija la imagen.
2. Pulse **Subir logotipo** o **Subir fondo**. La imagen se guarda en el servidor y queda
   seleccionada en el momento; los tótems la muestran en menos de un minuto.
3. Las imágenes subidas aparecen en **Imágenes en el servidor**, con miniatura, tamaño y
   dimensiones. Para cambiar de logotipo o fondo, elíjalo en el desplegable y pulse **Guardar**.
   Las que no están en uso se pueden **borrar**.

| | Logotipo | Fondo |
|---|---|---|
| Formatos | PNG, SVG, JPG o WebP | JPG, WebP o PNG |
| Tamaño máximo | **1 MB** | **5 MB** |
| Recomendado | PNG con fondo transparente de **128 px de alto** (hasta 640 px de ancho), o SVG | **1920×1080 px** (1080×1920 si el tótem es vertical) |
| Cómo se ve | A 64 px de alto como máximo, arriba a la izquierda | Ocupa toda la pantalla (se recorta si hace falta) con una capa blanca encima para que el texto se lea |

- Se comprueba el contenido real del fichero, no solo la extensión. Los SVG con código (scripts,
  eventos o enlaces `javascript:`) se rechazan.
- Si la imagen es pequeña para su uso, se avisa al subirla (puede verse borrosa o pixelada).
- Caben hasta 30 imágenes; se guardan en `imagenes/` dentro de la carpeta de datos
  (`/var/lib/totem-wifi/imagenes/`).
- Sin logotipo se muestra el **nombre de la organización** (o nada, si está vacío).
- Las imágenes copiadas a mano en la carpeta de datos (`logo.svg` o `logo.png`), como en las
  versiones anteriores, siguen funcionando.

### Textos de la pantalla y condiciones de uso

En `/admin` → **Textos de la pantalla** se cambian sin tocar ficheros:

| Campo | Dónde se ve |
|---|---|
| **Título** | Texto grande de la pantalla de inicio (máx. 60 caracteres). |
| **Nombre en la cabecera** | Arriba a la derecha y en la cabecera de `/admin`. Vacío = el título. |
| **Nombre de la organización** | Arriba a la izquierda, solo si no hay logotipo. |
| **Texto de introducción** | Bajo el título (máx. 400 caracteres). Vacío = el texto por defecto. |
| **Condiciones de uso** | Texto sin formato (máx. 20 000 caracteres); deje una línea en blanco entre párrafos. |

Si marca **«Mostrar condiciones de uso antes de obtener el código»**:

- La pantalla muestra la casilla **«He leído y acepto las condiciones de uso»** y un desplegable
  **«Ver condiciones»** con el texto, que se puede desplazar con el dedo.
- **«Obtener código»** queda atenuado hasta marcar la casilla; si se pulsa antes, aparece un aviso.
- Tras entregar un código, la casilla vuelve a quedar sin marcar para la siguiente persona.
- El servidor también lo comprueba: sin aceptar no entrega código.
- Cada código entregado queda en el log con la **versión** de las condiciones aceptadas
  (`condiciones_aceptadas=84a03f5a`). La versión actual se ve en `/admin` bajo el texto y cambia en
  cuanto se edita, así que se puede saber qué texto estaba vigente en cada momento.

> El portal cautivo de UniFi también puede mostrar condiciones de uso. Si ya las tiene activadas
> allí, valore si hace falta mostrarlas dos veces.

---

## 8. Ver el log

Se escribe una línea por cada voucher (IP del tótem, código **enmascarado** `*****-67890`, SSID y
minutos), por cada error de UniFi y por cada acción de administración. La API key **nunca** se
escribe en el log.

El fichero `totem-wifi.log` está en la carpeta de datos y rota solo (5 ficheros de 5 MB).

Windows, ver las últimas líneas y seguir en directo:

```powershell
Get-Content "C:\ProgramData\TotemWifi\totem-wifi.log" -Tail 50 -Wait
```

Ubuntu:

```bash
journalctl -u TotemWifi -f
```

Estado rápido (sirve para monitorización; responde 503 si UniFi no contesta):

```bash
curl http://localhost:8080/healthz
```

```json
{"ok":true,"mock":false,"unifi":"ok","daily_used":12}
```

---

## 9. Cambiar (rotar) la API key

1. En UniFi (*Network → Settings → Control Plane → Integrations*) cree una clave **nueva**.
2. En `/admin`, pegue la nueva en **API key** y pulse **Guardar**.
3. Pulse **«Probar conexión con UniFi»** y compruebe que dice «Conexión correcta».
4. Vuelva a UniFi y **borre la clave antigua**.

El cambio es inmediato: no hace falta reiniciar el servicio.

---

## 10. Si UniFi cambia de certificado

Si UniFi OS Server se reinstala o renueva su certificado, el quiosco dirá «Ahora mismo no podemos darte
un código…» y el log mostrará:

```text
huella del certificado distinta: ¿se ha regenerado el certificado de UniFi?
```

Si el cambio es legítimo: `/admin` → **«Obtener huella del certificado»** → **Guardar** →
**«Probar conexión con UniFi»**.

---

## 11. Diagnóstico de problemas

Vaya **por capas**, en este orden. `totem-wifi check` y «Probar conexión con UniFi» indican la capa:

| Capa | Síntoma | Qué hacer |
|---|---|---|
| **Conexión TCP** | «No hay conexión con https://…:11443» | ¿Es correcta la IP? ¿Está encendido UniFi OS Server? ¿Algún cortafuegos bloquea el puerto 11443 entre este servidor y UniFi? Pruebe `Test-NetConnection 198.51.100.20 -Port 11443` (Windows) o `nc -vz 198.51.100.20 11443` (Ubuntu). |
| **Certificado** | «huella del certificado distinta» | Ver [10](#10-si-unifi-cambia-de-certificado). |
| **Clave / rol** | HTTP 401 | Clave mal copiada o borrada en UniFi: cree otra. |
| | HTTP 403 | El usuario que creó la clave no tiene permisos suficientes: ver [5](#5-crear-la-api-key-en-unifi) (hace falta permiso de gestión en Network; «Hotspot» y «Solo lectura» no bastan). También pasa si la clave es de **otro** servidor. |
| **Sitio** | HTTP 404 o «no se encuentra el sitio default» | Deje vacío **ID del sitio** y pulse «Probar conexión» para detectarlo. |
| **Parámetros del voucher** | HTTP 400 | Revise duración (1–1 000 000 min) y velocidades (0 o 2–100 000 kbps). |

Otros problemas frecuentes:

- **El tótem muestra «Acceso no permitido desde este equipo»**: su IP no está en *Equipos que pueden
  usar el quiosco*. En el log aparece `acceso denegado … desde IP`.
- **El tótem no carga la página**: en Ubuntu, mire en `/admin` → *Estado* → *Cortafuegos (ufw)* que
  la red del tótem aparece en la lista. Si el estado se queda en «Aplicando…» o da error, consulte el
  ayudante y lance la sincronización a mano (muestra qué reglas añade o quita):

  ```bash
  systemctl status totem-wifi-firewall
  ```

  ```bash
  sudo /opt/totem-wifi/totem-wifi firewall-sync
  ```

  En Windows, compruebe que la regla «Totem Wi-Fi (HTTP)» del Firewall incluye la IP del tótem
  (vuelva a ejecutar `install.ps1`).
- **«Espera N segundos antes de pedir otro código»**: es la protección contra abusos
  (`cooldown_seconds`, 20 s por tótem). **«Hoy ya se han entregado todos los códigos»**: se ha
  alcanzado `daily_limit` (300); el contador vuelve a cero a medianoche.
- **Ha olvidado la contraseña de administración**: pare el servicio, edite `config.json` como
  administrador, deje `"admin_password_hash": ""`, arranque el servicio y abra `/admin` desde el
  propio servidor para crear una nueva.

---

## 12. Actualizar a una versión nueva

Descomprima el paquete nuevo y ejecute **el mismo instalador** que la primera vez. Para el servicio,
sustituye el programa y lo vuelve a arrancar **conservando la configuración y el contador**.

Antes de actualizar, guarde una copia de la configuración. Las versiones nuevas pueden añadir campos
a `config.json` que una versión anterior no reconoce: si tuviera que volver atrás, restaure esa copia.

```bash
sudo cp /etc/totem-wifi/config.json /etc/totem-wifi/config.json.bak
```

Windows (PowerShell como administrador, en la carpeta del paquete nuevo):

```powershell
powershell -ExecutionPolicy Bypass -File .\install.ps1 -NoBrowser
```

Ubuntu (no hace falta repetir las opciones de la sede: la configuración se conserva):

```bash
sudo ./install.sh
```

Los tótems recogen la versión nueva en la recarga de las 04:00 (o al reiniciar el navegador).

---

## 13. Desinstalar

Quita el servicio, el ayudante y las reglas «Totem Wi-Fi» del cortafuegos, y el programa. La configuración y los datos se conservan
salvo que se indique lo contrario.

Windows:

```powershell
powershell -ExecutionPolicy Bypass -File .\uninstall.ps1
```

Windows, borrando también configuración, contador y log:

```powershell
powershell -ExecutionPolicy Bypass -File .\uninstall.ps1 -Purge
```

Ubuntu:

```bash
sudo ./uninstall.sh
```

Ubuntu, borrando también configuración, datos y el usuario `totem`:

```bash
sudo ./uninstall.sh --purge
```

---

## 14. Referencia de la configuración

Todo se puede cambiar desde `/admin`. Si edita `config.json` a mano, pare antes el servicio y
arránquelo después; un campo mal escrito impide arrancar y el error dice cuál es.

| Campo | Por defecto | Significado |
|---|---|---|
| `listen` | `0.0.0.0:8080` | Dirección y puerto de escucha (cambiarlo requiere reiniciar). |
| `allowed_clients` | `127.0.0.1`, `192.0.2.0/24` | IPs o redes CIDR que pueden usar el quiosco. El propio equipo siempre puede. |
| `admin_password_hash` | vacío | Contraseña de `/admin` cifrada con bcrypt. Vacío = se crea en el primer acceso. |
| `admin_allowed_clients` | `127.0.0.1` | IPs o redes que pueden abrir `/admin`. |
| `unifi.base_url` | `https://198.51.100.20:11443` | UniFi OS Server de la sede (IP de ejemplo). |
| `unifi.api_key` | vacío | Clave de la Integration API. Nunca se muestra ni se registra. |
| `unifi.site_id` | vacío | Sitio de UniFi. Vacío = se detecta el «default» y se guarda. |
| `unifi.cert_sha256` | vacío | Huella anclada del certificado. Vacío = no se conecta (salvo simulación). |
| `unifi.mock` | `true` | Modo simulación: códigos falsos y franja «MODO PRUEBA». |
| `voucher.ssid` | `WifiPublica` | Red que se muestra y se codifica en el QR. |
| `voucher.ssid_password` | vacío | Contraseña Wi-Fi para el QR. Vacío = red abierta con portal. |
| `voucher.extra_ssids` | `[]` | Redes adicionales `{"ssid": "...", "password": "..."}`; si hay alguna, el quiosco muestra un selector. |
| `voucher.note` | `Totem` | Nota del voucher visible en UniFi (útil para saber de qué tótem sale). |
| `voucher.minutes` | `240` | Duración del voucher (1–1 000 000). |
| `voucher.guest_limit` | `1` | Dispositivos por código (0 = sin límite). |
| `voucher.data_mb` | `0` | Límite de datos en MB (0 = sin límite; máximo 1 048 576, el que admite UniFi). |
| `voucher.rx_kbps` / `tx_kbps` | `0` | Velocidad de bajada / subida (0 = sin límite; si no, 2–100 000). |
| `kiosk.title` | `Wi-Fi gratuita` | Título de la pantalla. |
| `kiosk.organization` | vacío | Nombre de la organización, en la cabecera cuando no hay logo. |
| `kiosk.header_text` | vacío | Nombre del servicio en la cabecera (vacío = el título). |
| `kiosk.intro` | vacío | Texto de introducción (vacío = el texto por defecto del idioma). |
| `kiosk.terms_enabled` | `false` | Pedir que se acepten las condiciones de uso antes de dar el código. |
| `kiosk.terms_text` | vacío | Texto de las condiciones de uso (obligatorio si `terms_enabled` es `true`). |
| `kiosk.display_seconds` | `60` | Segundos que el código permanece en pantalla. |
| `kiosk.cooldown_seconds` | `20` | Espera mínima entre códigos para un mismo tótem. |
| `kiosk.daily_limit` | `300` | Máximo de códigos al día entre todos los tótems (se reinicia a medianoche). |
| `kiosk.language` | `es` | Idioma de la pantalla. |
| `kiosk.logo_path` | vacío | Imagen del logotipo, elegida en `/admin` (vacío = `logo.svg` o `logo.png` copiado a mano, si existe). |
| `kiosk.background_image` | vacío | Imagen de fondo, elegida en `/admin` (vacío = sin fondo). |

**Otro idioma**: copie `internal/i18n/es.json` del código fuente como, por ejemplo, `en.json` en una
carpeta `i18n` dentro de la carpeta de datos, traduzca los textos y elíjalo en **Quiosco → Idioma**.
Las frases que falten se muestran en castellano.

---

## 15. Línea de comandos

```text
totem-wifi                       ejecuta en primer plano (pruebas)
totem-wifi -config RUTA          usa otro config.json (y su carpeta para los datos)
totem-wifi -data DIR             carpeta de datos (state.json, log, logo)
totem-wifi install | uninstall | start | stop | restart | status
                                 gestiona el servicio (como administrador / root)
totem-wifi fingerprint HOST:PUERTO
                                 muestra la huella SHA-256 del certificado de un servidor
totem-wifi check                 prueba la configuración y UniFi por capas (sale con ≠0 si falla)
totem-wifi init                  crea config.json con los valores por defecto si no existe
totem-wifi set [-unifi-url URL] [-totems LISTA] [-admin-ips LISTA]
                                 ajusta los datos de la sede (lo usa install.sh; reinicie después)
totem-wifi firewall-sync         ajusta ufw a los equipos permitidos (root; lo lanza systemd solo)
totem-wifi allowlist             puerto e IPs permitidas
totem-wifi version               versión
```

Sin `-config`, el programa usa `config.json` junto al ejecutable si existe; si no, el del servicio
instalado (`C:\ProgramData\TotemWifi\` o `/etc/totem-wifi/`).

---

## 16. Para desarrolladores

Requisitos: Go 1.23 o superior y `make` (los paquetes se generan con `zip`, `tar` y `sha256sum`).
Sin CGO: todo compila de forma cruzada desde cualquier sistema.

```bash
make test        # pruebas
make vet         # go vet (y staticcheck si está instalado)
make run         # arranca en modo desarrollo con ./dev/config.json → http://localhost:8080/
make build       # binario para esta plataforma
make build-all   # dist/ para windows/amd64, linux/amd64 y linux/arm64
make release     # paquetes .zip y .tar.gz en dist/, que son lo que se copia al servidor
```

Estructura: `cmd/totem-wifi` (arranque, comandos y servicio del sistema), `internal/config`,
`internal/unifi` (cliente de la Integration API con anclaje del certificado y cliente simulado),
`internal/voucher` (cooldown, límite diario, formato del código), `internal/web` (servidor, quiosco
y administración; los estáticos van embebidos), `internal/i18n` (textos) y `deploy/` (instaladores).

Las fuentes Montserrat, Raleway y Open Sans se distribuyen bajo la SIL Open Font License 1.1
(`internal/web/static/fonts/OFL-*.txt`; también en la carpeta `licencias/` de cada paquete).
