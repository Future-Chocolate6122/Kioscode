# Contexto: aplicación «Tótem Wi-Fi» (generador de códigos de acceso)

> Este documento está **anonimizado**: no contiene nombres reales de la organización, de sus sedes
> ni direcciones IP de producción. Las IP que aparecen son de documentación (RFC 5737) y se
> sustituyen en `/admin` en cada instalación.

_Versión 1.1 · 12 de noviembre de 2026 · Sustituye al diseño FastAPI + nginx del documento «despliegue-totem-wifi.md»_

> **v1.1:** la aplicación ya está construida (ver §9). Se han cerrado las decisiones pendientes
> (§8) y se documentan las desviaciones respecto a `CLAUDE.md`, que **prevalecen** sobre él.

## 1. Qué hay que construir

Una aplicación **autocontenida** (un único ejecutable, sin instalar Python, Node, MySQL ni ningún otro
runtime) que:

1. Corre en **una máquina independiente** de la red (el «servidor del tótem»).
2. Sirve por HTTP una **web de quiosco** que se muestra en esa misma máquina o en otros equipos de la
   red (los tótems), en un navegador a pantalla completa.
3. Al pulsar un botón, pide a **UniFi OS Server** un **voucher del hotspot** mediante la Integration
   API oficial y lo muestra en pantalla junto con un **QR Wi-Fi** para unirse al SSID.
4. Permite elegir el **SSID** (configurable; valor de ejemplo `WifiPublica`) y los parámetros
   del voucher (duración, nº de dispositivos, límite de datos, velocidad).
5. Tiene una **página de administración** sencilla, protegida con contraseña, para cambiar la
   configuración sin editar ficheros a mano.

El ciudadano ve el QR y el código, se conecta al SSID, y en el portal cautivo de UniFi escribe el
código. Nada más.

## 2. Entorno real donde va a funcionar

### Red Wi-Fi

- Red UniFi **multisede** de una organización pública. Hay varios SSID; uno de ellos es **público**
  y lleva el **portal cautivo con vouchers**. Es el que usa el tótem.
- Hay **dos UniFi OS Server**, cada uno en una sede y una red distintas:

  | Sede | URL de UniFi OS Server | Observaciones |
  |---|---|---|
  | Sede A | `https://192.0.2.10:11443` | Red 192.0.2.0/24 (ejemplo) |
  | Sede B | `https://198.51.100.20:11443` | Aquí está el portal del SSID público (ejemplo) |

  La app debe poder apuntar a **cualquiera de los dos** (URL configurable). No dar por hecho uno.
  Se instala **una copia por sede**, cada una apuntando a **su** UniFi OS Server y con **sus** tótems
  (ver §7). Los vouchers solo valen en el servidor que gestiona el portal cautivo de esa red.
- Versión de UniFi OS Server: **5.1.42** (Linux x64), aplicación Network 10.x. Certificado HTTPS
  **autofirmado**.
- DNS de la red: solo `8.8.8.8` (el cortafuegos bloquea otros). La app no necesita DNS: trabaja con
  IPs.

### Máquina que ejecutará la app

- **Ubuntu Server x64** (decidido). Un servidor por sede. Se siguen generando también los binarios
  de Windows y Linux arm64 porque no cuesta nada, pero el sistema de referencia es Ubuntu.
- Normalmente sin escritorio: `/admin` se abre por túnel SSH (`ssh -L 8080:localhost:8080 …`) o
  desde las IPs de `admin_allowed_clients`.
- Cortafuegos **ufw**, gestionado por la propia aplicación mediante un ayudante (ver §7).
- Sin acceso a Internet garantizado en tiempo de ejecución: todo lo que necesite la web (fuentes,
  librería QR, CSS) va **embebido** en el ejecutable.
- La máquina puede ser el propio tótem (navegador abriendo `http://localhost:8080/`) o un servidor al
  que se conecten varios tótems por la LAN.

### Tótems

- Equipos con pantalla (táctil o no) y un navegador moderno (Chrome/Edge) en **modo quiosco**:
  `--kiosk http://IP_SERVIDOR:8080/ --incognito --noerrdialogs --disable-pinch --overscroll-history-navigation=0`
- Hardware y SO del tótem todavía sin definir. La web no debe depender de nada del tótem.

## 3. Integration API de UniFi Network (lo que ya está comprobado)

- Base: `https://<HOST_UNIFI>:11443/proxy/network/integration/v1`
- Autenticación: cabecera `X-API-KEY: <clave>`. La clave se crea en la aplicación Network:
  *Settings → Control Plane → Integrations* (en algunas versiones, directamente *Integrations*).
- ⚠️ La clave hereda los permisos del usuario que la crea. Hay que crearla con un **administrador
  local** (no cuenta de ui.com). El rol «Hotspot» **no** da acceso a la Integration API; hace falta
  un rol con gestión de la red.
- Recomendación adoptada: un **usuario local dedicado** por servidor (p. ej. `totem-api`), con acceso
  **solo a Network** y el permiso más bajo que permita gestionar la red (suele llamarse «Full
  Management» o «Admin»; «Hotspot» y «Solo lectura» no sirven). Los nombres exactos de los roles en
  5.1.42 **no están verificados**: si «Probar conexión» da 403, el permiso se queda corto.
  Cada servidor tiene sus propias claves; una clave de un servidor no vale en el otro.
- Listar sitios (sirve para probar la clave y obtener el `siteId`):
  ```
  GET /v1/sites
  → {"data":[{"id":"<uuid>","internalReference":"default","name":"Default"}], ...}
  ```
- Crear vouchers:
  ```
  POST /v1/sites/{siteId}/hotspot/vouchers
  Content-Type: application/json
  {
    "count": 1,                      // opcional, por defecto 1
    "name": "Totem",                 // OBLIGATORIO: nota visible en UniFi
    "timeLimitMinutes": 240,         // OBLIGATORIO: 1..1000000
    "authorizedGuestLimit": 1,       // opcional: dispositivos por voucher
    "dataUsageLimitMBytes": 1024,    // opcional
    "rxRateLimitKbps": 5000,         // opcional, 2..100000
    "txRateLimitKbps": 2000          // opcional, 2..100000
  }
  → 201 {"vouchers":[{"id":"…","code":"1234567890","name":"…","timeLimitMinutes":240,"createdAt":"…","expired":false, …}]}
  ```
  El `code` son 10 cifras. En pantalla se muestra como `12345-67890`.
- Listar / borrar: `GET` y `DELETE /v1/sites/{siteId}/hotspot/vouchers` (con filtro), y
  `GET|DELETE /v1/sites/{siteId}/hotspot/vouchers/{voucherId}`.
- Códigos de error habituales: `401` clave inválida; `403` el usuario de la clave no tiene permiso;
  `404` `siteId` incorrecto.
- Comprobación manual de la clave (desde cualquier máquina de la red):
  ```bash
  curl -sk -H "X-API-KEY: LA_CLAVE" https://198.51.100.20:11443/proxy/network/integration/v1/sites
  ```

## 4. Qué debe mostrar la web del quiosco

Pantalla única, pensada para verse a 2–3 metros en una pantalla vertical u horizontal:

1. **Inicio**: título, nombre del SSID, condiciones de uso (si están activadas) y botón «Empezar».
2. **Pasos** (desde la v1.4), los tres a la vista en una misma pantalla; en horizontal, tres columnas
   (la central, más ancha); en vertical, apilados:
   1. *Conéctate a la red*: QR Wi-Fi y nombre del SSID; «espera a que se abra la página de acceso».
   2. *Solicita tu código*: botón «Solicitar código»; al pulsarlo, el código en grande
      (`12345-67890`), su validez, «Tienes X minutos para usarlo» y la cuenta atrás.
   3. *Escribe el código y conéctate*: «en tu móvil, ordenador o tablet».
   El código solo se crea al pulsar «Solicitar código». Sin actividad en 2 minutos antes de pedirlo,
   vuelta a Inicio.
3. Pasados `display_seconds` (60 s por defecto) o al pulsar «He terminado», vuelve a Inicio y el
   código desaparece de pantalla.
4. Si no se puede dar un código (UniFi no responde, espera entre códigos, límite diario): mensaje
   amable en el propio paso 2 y el botón sigue disponible. Nunca detalles técnicos al ciudadano.
5. Franja amarilla «MODO PRUEBA» cuando la app está en modo simulación.

Formato del QR Wi-Fi (estándar que entienden Android e iOS):
`WIFI:T:nopass;S:<ssid>;;` para red abierta con portal; si el SSID tuviera contraseña,
`WIFI:T:WPA;S:<ssid>;P:<clave>;;`. Hay que escapar `\ ; , : "` en SSID y clave.

## 5. Protección contra abuso (requisitos, no opcionales)

- La API key **nunca** llega al navegador. Solo el servidor habla con UniFi.
- Un voucher como máximo cada `cooldown_seconds` (20 s) por IP de cliente, y `daily_limit` (300)
  vouchers al día en total. El contador diario sobrevive a reinicios (se guarda en disco).
- Lista de IPs/CIDR permitidas para la web del quiosco (`allowed_clients`). Vacía = solo `localhost`.
- La página de administración va en una ruta aparte (`/admin`), con contraseña, y se puede limitar a
  otras IPs distintas (`admin_allowed_clients`).
- El certificado autofirmado de UniFi se **ancla**: se guarda su huella SHA-256 en la configuración y
  el cliente HTTPS solo acepta ese certificado (no `InsecureSkipVerify` en producción). La página de
  administración ofrece un botón «Obtener huella del servidor» para hacerlo sin consola.
- Las peticiones a UniFi tienen timeout (10 s) y no se reintentan en bucle.

## 6. Configuración

Fichero `config.json` junto al ejecutable (o en `%ProgramData%\TotemWifi\` / `/etc/totem-wifi/` cuando
se instala como servicio; en Ubuntu, los datos —contador, log, logo— van en `/var/lib/totem-wifi/`).
Se crea solo con valores por defecto la primera vez. Todo se puede cambiar desde `/admin`. Campos:

```jsonc
{
  "listen": "0.0.0.0:8080",
  "allowed_clients": ["127.0.0.1"],
  "admin_password_hash": "",          // bcrypt; vacío = /admin pide crear contraseña la 1ª vez
  "admin_allowed_clients": ["127.0.0.1"],
  "unifi": {
    "base_url": "https://192.0.2.10:11443",   // ejemplo; se cambia en /admin
    "api_key": "",
    "site_id": "",                    // si vacío, se detecta con GET /v1/sites (el 'default')
    "cert_sha256": "",                // huella anclada; vacío = rechazar conexión salvo mock
    "mock": true                      // true hasta que haya clave: genera códigos falsos
  },
  "voucher": {
    "ssid": "WifiPublica",            // ejemplo; se cambia en /admin
    "ssid_password": "",              // vacío = red abierta con portal
    "extra_ssids": [],                // [{"ssid":"…","password":"…"}]; si hay alguno, la web muestra selector
    "note": "Totem",
    "minutes": 240,
    "guest_limit": 1,
    "data_mb": 0,                     // 0 = sin límite
    "rx_kbps": 0,
    "tx_kbps": 0
  },
  "kiosk": {
    "title": "Wi-Fi gratuita",
    "display_seconds": 60,
    "cooldown_seconds": 20,
    "daily_limit": 300,
    "language": "es",
    "organization": "",               // nombre que se muestra en cabecera si no hay logo; vacío = nada
    "logo_path": "",                  // logo.svg/png en el directorio de datos
    "background_image": ""            // opcional: foto de fondo con capa blanca semitransparente
  }
}
```

La **API key y el hash de la contraseña** van en el mismo fichero; por eso el instalador deja el
fichero con permisos solo para el servicio/administrador.

## 7. Decisiones ya tomadas (no replantear)

- Arquitectura **solo UniFi**: vouchers del hotspot, sin RADIUS ni base de datos externa.
- **Un solo ejecutable** por plataforma, estáticos embebidos, estado en ficheros (JSON/SQLite embebido).
- La app es la **única** que conoce la API key; los tótems solo ven HTTP en la LAN.
- Modo simulación por defecto hasta que el administrador configure la clave.
- Idioma de la interfaz: **castellano**. Textos centralizados para poder añadir otros idiomas.
- Estilo visual sobrio e institucional (paleta azul `#079BE2`, grises,
  formas cuadradas, Montserrat/Raleway/Open Sans), sin copiar su maquetación ni incrustar su logo.
  Los detalles están en `CLAUDE.md`, sección «Estilo visual».
- **Anonimato del repositorio:** ni el código, ni los valores por defecto, ni la documentación
  contienen el nombre de la organización, nombres de sedes, nombres reales de SSID ni IPs de
  producción. Todo eso se introduce en cada instalación desde `/admin`.
- **Servidor: Ubuntu Server**, una instalación por sede. Los datos de cada sede (URL de UniFi, IPs de
  los tótems, IPs de administración, nota del voucher) se indican al instalar
  (`install.sh --unifi-url … --totems … --admin-ips …`) y se cambian después en `/admin`.
- **Todo lo variable es configurable desde `/admin`**: servidor UniFi, API key, huella, IPs de los
  tótems y de administración, SSID, duración y límites del voucher, cooldown y límite diario.
- **Cortafuegos (opción A):** un ayudante `totem-wifi firewall-sync`, lanzado como root por systemd
  (`totem-wifi-firewall.path` vigila `config.json`), mantiene ufw con el puerto abierto **solo** a las
  IPs permitidas. Solo toca reglas con el comentario «Totem Wi-Fi», nunca activa/desactiva ufw,
  rechaza puertos < 1024. El servicio web **sigue sin privilegios**.
- **Cambiar las IPs permitidas o el puerto exige volver a escribir la contraseña de `/admin`.** Se
  descartó pedir credenciales de Ubuntu en la web (viajarían sin cifrar y romperían la separación de
  privilegios).
- **Un usuario local dedicado en UniFi para la API key** (ver §3), con instrucciones plegables en
  `/admin` bajo el campo de la clave.

## 8. Decisiones pendientes

Resueltas en la v1.1:

- [x] SO de la máquina servidora → **Ubuntu Server** (se siguen compilando todos los binarios).
- [x] IPs del servidor y de los tótems → **configurables por sede** en `/admin` (o al instalar);
      admiten IPs sueltas o redes CIDR. El cortafuegos se actualiza solo.
- [x] A qué UniFi OS Server apunta → **configurable por sede** en `/admin`; al cambiarlo se borran la
      huella y el sitio anteriores.
- [x] Duración y límites del voucher → **configurables** en `/admin` (por defecto 4 h, 1 dispositivo).
- [x] Uno o varios SSID → configurable: `extra_ssids`; la web muestra selector solo si hay más de uno.
- [x] Rol mínimo de la API key → usuario local dedicado, solo Network, permiso de gestión (§3).

Siguen abiertas (no bloquean; se rellenan en `/admin` de cada sede):

- [ ] IPs concretas de los tótems de cada sede y si tendrán IP fija o DHCP (si es DHCP, autorizar la
      red completa o hacer reservas).
- [ ] En qué red estará el servidor Ubuntu de cada sede y si llega al puerto 11443 de su UniFi.
- [ ] Nombre exacto del rol mínimo en UniFi OS 5.1.42 (comprobar con «Probar conexión»).

## 9. Estado de la implementación

**Versiones:**

- **1.0.0** — primera versión publicada.
- **1.1.0** — textos de la pantalla editables en `/admin` (título, nombre en la cabecera,
  introducción) y condiciones de uso opcionales: casilla obligatoria con desplegable del texto,
  comprobación también en el servidor y versión de las condiciones anotada en el log de cada código.
- **1.2.0** — biblioteca de imágenes en `/admin`: subir, elegir y borrar logotipo (máx. 1 MB;
  PNG/SVG/JPG/WebP) y fondo (máx. 5 MB; JPG/WebP/PNG), con validación del contenido real y rechazo de
  SVG con código. El quiosco se recarga solo (comprueba cada minuto `/api/version`) cuando cambia
  algo en `/admin`. Tope de 1 048 576 MB en `voucher.data_mb`.

- **1.3.0** — pestaña **Invitados conectados** en `/admin` (`/admin/invitados`): lista de los
  dispositivos con acceso de invitado (portal cautivo), con búsqueda, orden y paginación;
  desconectar uno, los seleccionados o todos (`UNAUTHORIZE_GUEST_ACCESS`, uno a uno y en paralelo).
  «Desconectar todos» pide la contraseña y puede invalidar los códigos del tótem vigentes.
  **Caducidad de los códigos sin usar**: los del tótem que nadie ha usado se borran en UniFi a los
  `voucher.unused_expire_minutes` minutos de generarse (15 por defecto; revisión cada minuto) y el
  quiosco lo avisa junto al código. Sustituye a `unused_cleanup_hours` (horas), que se convierte al
  cargar: 24 → 15 min (valor por defecto), 0 → desactivada, otro valor → a minutos.
  Decidido con el usuario: invitado = entró por el portal; MAC completa; sin listado de vouchers.

- **1.4.0** — nueva pantalla del quiosco: Inicio («Empezar», con las condiciones) y una pantalla con
  los tres pasos (conectarse con el QR, solicitar el código, escribirlo). El código se genera al
  pulsar «Solicitar código» y aparece en el paso 2. La espera entre códigos se muestra en el propio
  botón como cuenta atrás («Podrás solicitar tu código en N s») y se reactiva solo; los demás
  errores, en rojo en ese paso. Vuelta al inicio tras 2 minutos sin actividad. Sin cambios en el
  servidor ni en la configuración.

- **1.4.1** — corregido el aviso tras desconectar invitados (no pueden volver a entrar con el mismo
  código); favicon en `/admin`; README reorganizado con capturas, `CHANGELOG.md` y `CLAUDE.md` al día.

- **1.4.2** — los instaladores guardan `config.json.bak` al actualizar; `install.sh` muestra al
  terminar la dirección de `/admin` (`totem-wifi allowlist` añade líneas `admin`). README con enlaces
  de descarga reales, `--admin-ips` en el ejemplo y tabla de diagnóstico unificada.

- **1.4.3** — licencia MIT (`LICENSE`, titular: el usuario de GitHub) y licencias de terceros en
  `licencias/`, incluidas en cada paquete. Sin cambios en el programa.

**Comportamiento de la API a tener en cuenta:**

- Los vouchers sin usar **no caducan**: `timeLimitMinutes` cuenta desde el primer uso.
- La API no indica el SSID de cada cliente ni qué voucher usó cada invitado.
- No documenta si borrar un voucher activado desconecta a quien lo usa (pendiente de comprobar en
  real; el diseño no depende de ello).
- Comprobado en real: tras `UNAUTHORIZE_GUEST_ACCESS`, el dispositivo no puede volver a entrar con
  el mismo código si «Dispositivos por código» = 1.

**Planificado** (valorado con el OpenAPI de Network 10.0.162 / 10.1.84):

- Descartado por ahora: acceso sin código con `AUTHORIZE_GUEST_ACCESS` (requiere exponer el servidor
  a la red de invitados).

Construida en Go siguiendo `CLAUDE.md`, con el código en un repositorio de GitHub. Probado con `go vet`, `staticcheck`, `go test -race` y Go 1.23;
contra un UniFi **simulado** por HTTPS; e instalación en Ubuntu con systemd simulado y ufw real
inactivo.

**Comprobado en la instalación real** (Ubuntu Server con UniFi OS Server 5.1.42):

- Instalación, huella del certificado, conexión con UniFi y entrega de vouchers.
- Hubo que abrir el puerto 11443 en el ufw del **servidor de UniFi** para la IP del gestor de
  tótems (solo estaba abierto para el equipo de administración).
- Desconectar a un invitado: **no puede volver a entrar con el mismo código** (con «Dispositivos
  por código» = 1).
- systemd lanza el ayudante del cortafuegos al guardar en `/admin` (`totem-wifi-firewall.path`).

**Sin probar todavía:**

- Si borrar en UniFi un voucher ya activado desconecta a quien lo usa (el diseño no depende de ello).
- `install.ps1` / `uninstall.ps1` (no había PowerShell en el entorno de desarrollo). Pendiente de
  probar en un Windows real más adelante.

**Desviaciones respecto a `CLAUDE.md` (prevalecen sobre él):**

| `CLAUDE.md` dice | Se ha hecho | Motivo |
|---|---|---|
| `config.json` 640 `root:totem`; `ReadWritePaths=/var/lib/totem-wifi` | `config.json` 600 `totem:totem`; `ReadWritePaths` incluye también `/etc/totem-wifi` | `/admin` tiene que poder guardar la configuración |
| `install.sh` con «ufw opcional» | Ayudante de ufw instalado por defecto si hay ufw (`--no-ufw` para omitirlo) | Decisión del usuario (opción A, §7) |
| SSID en mayúsculas | El SSID se muestra tal cual | Debe coincidir con la lista de redes del móvil |
| `allowed_clients` vacía = solo localhost | Además, localhost **siempre** está permitido | El tótem puede ser el propio servidor |
| Comandos de `CLAUDE.md` §4 | Añadidos `restart`, `init`, `set`, `firewall-sync`, `allowlist` y la opción `-data` | Instalación por sede y cortafuegos |
| Respuesta de `/api/voucher` | Añade `valid_for` («Válido durante 4 horas») | Se muestra bajo el código |
| `Referrer-Policy` no especificada | `same-origin` | Con `no-referrer`, Chrome envía `Origin: null` y los formularios de `/admin` fallan |

No se pudo consultar el OpenAPI oficial (dominio bloqueado en el entorno de desarrollo): el cliente
sigue §3 y acepta la respuesta del voucher tanto en `vouchers` como en `data`.
