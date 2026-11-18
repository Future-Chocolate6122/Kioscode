# Instrucciones para Claude Code: proyecto «Tótem Wi-Fi»

Lee primero `CONTEXTO.md`: describe la red, los dos servidores UniFi, la API y lo que debe ver el
ciudadano. Este fichero dice **cómo** construir la aplicación. Responde y comenta el código **en
castellano**.

> **Estado:** la aplicación está terminada y en producción (versión 1.4.x). Las novedades de cada
> versión están en `CHANGELOG.md` y las decisiones, pruebas en real y desviaciones en
> `CONTEXTO.md` §7–§9. Ante cualquier contradicción, **`CONTEXTO.md` §9 prevalece** sobre este
> fichero. Los cambios nuevos se hacen sobre lo existente: no rehagas lo que ya funciona.

## 1. Objetivo en una frase

Un único ejecutable (`totem-wifi.exe` / `totem-wifi`) que sirve una web de quiosco, pide vouchers del
hotspot a UniFi OS Server por su Integration API y los muestra con un QR Wi-Fi. Sin instalar nada más
en la máquina.

## 2. Stack (decidido)

| Pieza | Elección | Motivo |
|---|---|---|
| Lenguaje | **Go 1.23+** | Un binario estático por plataforma, sin runtime, cross-compila a Windows y Linux desde cualquier sitio |
| HTTP | `net/http` de la biblioteca estándar + `embed` para los estáticos | Cero dependencias en tiempo de ejecución |
| QR | `github.com/skip2/go-qrcode` (genera SVG/PNG en el servidor) | Evita JS externo; el QR se sirve como `<img>` |
| Servicio del SO | `github.com/kardianos/service` | Mismo código para servicio de Windows y systemd |
| Contraseña admin | `golang.org/x/crypto/bcrypt` | |
| Estado persistente | Ficheros JSON en el directorio de datos (`state.json`: contador diario; `config.json`) | No hace falta SQLite para dos contadores |
| Frontend | HTML + CSS + JS vanilla, **sin frameworks ni CDN** | Debe funcionar sin Internet |
| Tests | `testing` estándar + `net/http/httptest` | |

Si crees que hace falta otra dependencia, añádela solo si es Go puro (sin CGO) y explícalo en el
commit. **Nunca** CGO: rompería la compilación cruzada y la promesa de «un solo binario».

## 3. Estructura del repositorio

```
totem-wifi/
├── CLAUDE.md                 ← este fichero
├── CONTEXTO.md
├── README.md                 ← guía de instalación y uso para el administrador (en castellano)
├── CHANGELOG.md              ← novedades de cada versión
├── docs/img/                 ← capturas para el README (modo real con UniFi simulado; sin datos reales)
├── go.mod / go.sum
├── Makefile                  ← build, build-all, test, run, release
├── cmd/totem-wifi/main.go    ← flags, arranque, instalación como servicio
├── internal/
│   ├── config/               ← carga/validación/guardado de config.json; valores por defecto
│   ├── unifi/                ← cliente de la Integration API + cliente mock; anclaje del certificado
│   ├── voucher/              ← servicio: cooldown, límite diario, persistencia del contador, formato del código;
│   │                            invitados conectados, desconexión y caducidad de códigos sin usar (guests.go)
│   ├── firewall/             ← ayudante que ajusta ufw a las IPs permitidas (lo lanza systemd como root)
│   ├── logfile/              ← fichero de log con rotación
│   ├── web/                  ← servidor HTTP: rutas, middlewares (allowlist, rate limit, auth admin), plantillas;
│   │                            admin.go, guests.go (invitados), images.go (logotipo y fondo)
│   │   └── static/           ← index.html, admin.html, invitados.html, nav.html (pestañas), app.css,
│   │                            app.js, admin.js, invitados.js (embebidos con //go:embed)
│   │       └── fonts/        ← Montserrat, Raleway y Open Sans en .woff2 (ver «Estilo visual»)
│   └── i18n/                 ← textos en castellano (es.json) y mecanismo para añadir idiomas
├── deploy/
│   ├── windows/install.ps1   ← crea %ProgramData%\TotemWifi, copia el exe, registra el servicio, abre el firewall
│   ├── windows/uninstall.ps1
│   ├── linux/install.sh      ← /opt/totem-wifi + /etc/totem-wifi + unidad systemd + ayudante de ufw
│   └── linux/uninstall.sh
└── dist/                     ← salida de `make release` (ignorado en git)
```

## 4. Comportamiento que hay que implementar

### Rutas HTTP

| Ruta | Método | Quién | Qué hace |
|---|---|---|---|
| `/` | GET | tótems (allowlist) | Pantalla del quiosco |
| `/api/voucher` | POST | tótems (allowlist) | Crea un voucher. Devuelve `{code, formatted, ssid, qr_url, expires_in_seconds, mock}`. Aplica cooldown por IP y límite diario. Errores: `429` (espera), `503` (UniFi no disponible), siempre con mensaje en castellano apto para mostrar |
| `/api/qr.svg?ssid=…` | GET | tótems | SVG del QR Wi-Fi (solo de los SSID configurados; nunca de un SSID arbitrario) |
| `/healthz` | GET | cualquiera de la allowlist | `{"ok":true,"mock":false,"unifi":"ok|error","daily_used":12}` |
| `/admin` | GET/POST | `admin_allowed_clients` + contraseña | Formulario de configuración; botón «Probar conexión con UniFi»; botón «Obtener huella del certificado»; «Generar voucher de prueba» |
| `/admin/logout` | POST | | |
| `/api/version` | GET | tótems | Huella de lo que muestra la pantalla; el quiosco la consulta cada minuto y se recarga si cambia |
| `/logo`, `/background` | GET | tótems y admin | Logotipo y fondo elegidos en `/admin` (`no-cache`) |
| `/admin/invitados` | GET | admin con sesión | Invitados conectados (pestaña «Invitados conectados») |
| `/admin/api/guests/disconnect` | POST | admin + CSRF | Desconecta los invitados indicados (`UNAUTHORIZE_GUEST_ACCESS`) |
| `/admin/api/guests/disconnect-all` | POST | admin + CSRF + contraseña | Desconecta a todos; opcionalmente invalida los códigos del tótem vigentes |
| `/admin/api/upload`, `/admin/api/image-delete`, `/admin/imagenes/{nombre}` | POST/GET | admin | Biblioteca de imágenes (logotipo ≤ 1 MB, fondo ≤ 5 MB) |
| `/admin/api/check`, `/admin/api/fingerprint`, `/admin/api/test-voucher`, `/admin/api/firewall` | POST/GET | admin | Probar conexión, huella, voucher de prueba, estado de ufw |

### Reglas de negocio

- **Modo mock** (`unifi.mock=true`): genera códigos aleatorios de 10 cifras, nunca llama a UniFi, y
  la web muestra la franja «MODO PRUEBA». Es el estado inicial tras instalar.
- **Cooldown** por IP de cliente (`cooldown_seconds`) y **límite diario** global (`daily_limit`,
  reinicio a medianoche local). El contador se persiste en `state.json` tras cada voucher.
- **Anclaje del certificado**: el cliente HTTPS usa `tls.Config{InsecureSkipVerify: true,
  VerifyPeerCertificate: …}` y en `VerifyPeerCertificate` compara el SHA-256 del certificado hoja con
  `unifi.cert_sha256`. Si no coincide, error claro en el log («huella del certificado distinta: ¿se
  ha regenerado el certificado de UniFi?»). Con `cert_sha256` vacío y `mock=false`, el servicio
  arranca pero `/api/voucher` devuelve 503 y el log explica cómo obtener la huella.
- **`site_id` vacío** → al arrancar (y al pulsar «Probar conexión») se llama a `GET /v1/sites` y se
  usa el que tenga `internalReference == "default"`; se guarda en la configuración.
- La **API key** solo se lee del servidor. No aparece en ninguna respuesta HTTP, ni en `/admin`
  (mostrar «configurada: sí/no» y un campo para sustituirla).
- **Logs** a stdout/stderr (el gestor de servicios los recoge) y, además, a un fichero rotado
  `totem-wifi.log` en el directorio de datos. Una línea por voucher creado (IP del tótem, código
  enmascarado `*****-67890`, SSID, minutos) y por error de UniFi (código HTTP y cuerpo recortado).
- **Primer arranque**: si `config.json` no existe, se crea con los valores por defecto de
  `CONTEXTO.md` §6. Si `admin_password_hash` está vacío, `/admin` muestra un formulario para crear
  la contraseña (solo desde `admin_allowed_clients`).
- **Condiciones de uso** (opcionales): si `kiosk.terms_enabled`, el servidor exige `accepted: true`
  en `/api/voucher` y anota la versión aceptada en el log.
- **Caducidad de códigos sin usar**: los vouchers sin usar no caducan en UniFi; cada minuto se borran
  los **del tótem** (filtro por su nota) con `authorizedGuestCount = 0` y más de
  `voucher.unused_expire_minutes` (15 por defecto). Nunca se tocan los usados ni los creados a mano.
- **Invitados**: la API no permite filtrar clientes por tipo de acceso; se recorren todos y se
  quedan los `GUEST`. Desconectar = `UNAUTHORIZE_GUEST_ACCESS` (uno a uno, en paralelo).
  Comprobado en real: un dispositivo desconectado no puede volver a entrar con el mismo código
  (con «Dispositivos por código» = 1).
- **Cambiar las IPs permitidas o el puerto** en `/admin` exige volver a escribir la contraseña; el
  ayudante `totem-wifi firewall-sync` (systemd `.path`) ajusta ufw tocando solo sus reglas.

### Pantalla del quiosco (`index.html`)

- Una sola página, sin recargas. Pantallas: `inicio` → `pasos` → `inicio` (desde la v1.4).
  - `inicio`: título, red, condiciones de uso (si están activadas) y botón «Empezar».
  - `pasos`: los tres pasos a la vista (horizontal: tres columnas con la central más ancha;
    vertical: apilados): 1. Conéctate a la red (QR), 2. Solicita tu código (botón; el código
    aparece ahí), 3. Escribe el código en tu móvil, ordenador o tablet. El paso 2 pasa por
    `antes` → `cargando` → `codigo`; la espera entre códigos se muestra como cuenta atrás en el
    propio botón y los demás errores, en rojo en ese paso. 2 minutos sin actividad → `inicio`.
- El código solo se genera al pulsar «Solicitar código».
- Tipografía grande (código a ≥ 96 px), contraste alto, funciona en vertical y horizontal.
- Bloquear menú contextual, selección de texto, zoom y arrastre. Recarga automática a las 04:00.
- Cuenta atrás visible. Al terminar o al pulsar «He terminado», el código se elimina del DOM.
- Si hay más de un SSID configurado, selector antes del botón; si hay uno, no se muestra selector.
- Sin peticiones a dominios externos. Comprobarlo con la pestaña Red del navegador.

### Estilo visual

El quiosco debe parecer un **servicio de una administración pública**: sobrio, limpio, de formas
cuadradas, con una paleta azul institucional. La organización concreta no se nombra en el código: su
nombre se lee de `kiosk.organization` y su logotipo de `kiosk.logo_path`.

**Paleta** (define estas variables CSS en `app.css` y usa solo estas):

| Variable | Valor | Uso en el quiosco |
|---|---|---|
| `--azul` | `#079BE2` | **Botón «Obtener código», títulos, cuenta atrás** |
| `--azul-claro` | `#00ACEC` | Estado hover/activo del botón, borde del QR |
| `--azul-oscuro` | `#0085AF` | Botón pulsado, fondo de la franja de pasos |
| `--rojo` | `#CD2027` | Rojo del emblema institucional → **solo** para el logo y para mensajes de error; nunca como color decorativo |
| `--amarillo` | `#EFBE00` | Franja «MODO PRUEBA» |
| `--gris-texto` | `#5A5A5A` | Instrucciones |
| `--gris-nav` | `#5E6A71` | Texto secundario, cabecera, pie |
| `--gris-oscuro` | `#111111` / `#454548` | **Código del voucher** (máximo contraste) |
| `--gris-fondo` | `#F7F8F9` | Fondo de la pantalla y de las tarjetas secundarias |
| `--blanco` | `#FFFFFF` | Cabecera y tarjeta central con el QR y el código |

**Tipografía** (las tres son de Google Fonts con licencia OFL: inclúyelas como ficheros `.woff2`
en `internal/web/static/fonts/`, embebidas en el binario; **ningún `<link>` a fonts.googleapis.com**):

- **Montserrat 700** para el título de la pantalla y el nombre del SSID (en mayúsculas, tamaño medio
  y color `--azul`).
- **Raleway 600–700** en mayúsculas para botones y etiquetas cortas («OBTENER CÓDIGO», «HE TERMINADO»).
- **Open Sans 300/400/700** para el cuerpo e instrucciones. El **código del voucher** en Open Sans 700
  o en una monoespaciada del sistema, ≥ 96 px, con `letter-spacing` amplio y guion central
  (`12345-67890`).
- Si no hubiera forma de embeber las fuentes, degradar a `"Segoe UI", Roboto, Arial, sans-serif`.

**Formas y composición:**

- Cabecera blanca de ~90 px de alto con el **logotipo a la izquierda** (si lo hay) y, a la derecha,
  el nombre del servicio en Raleway mayúsculas gris (`--gris-nav`). Fina sombra debajo
  (`0 0 2px rgba(0,0,0,.5)`).
- Todo **cuadrado o casi**: radios de 0–4 px en tarjetas y botones (0 px en tarjetas, 2 px en
  botones). Nada de pastillas redondeadas ni degradados.
- Tarjeta central blanca sobre fondo `--gris-fondo`, con sombra amplia y suave
  (`0 0 30px rgba(0,0,0,.3)`) que contiene QR + código + pasos.
- Botón principal: fondo `--azul`, texto blanco, Raleway mayúsculas, radio 2 px, grande (≥ 72 px de
  alto para pantalla táctil). Botón secundario «He terminado»: fondo `#EAEAEA`, texto `--gris-oscuro`.
- Títulos de sección en mayúsculas y `--azul`.
- Iconos de línea sencillos, de un solo color, para los tres pasos (Wi-Fi, navegador, teclado).
  Dibújalos como SVG inline; no uses Font Awesome ni ninguna fuente de iconos.
- Mucho aire: márgenes generosos, pocas cosas en pantalla. Fotografía de fondo **opcional** y
  configurable (`kiosk.background_image`), con una capa blanca semitransparente encima
  (`rgba(255,255,255,.56)`) para que el código siga leyéndose.

**Logotipo y nombre:** no incrustes ningún logotipo ni nombre de organización en el código. La app
lee `logo.svg`/`logo.png` del directorio de datos (`kiosk.logo_path`) y el nombre de
`kiosk.organization`. Si ambos están vacíos, la cabecera muestra solo el título del servicio.

**Comprobación:** la pantalla debe leerse como un servicio público serio, sin que nada en ella (ni en
el repositorio) identifique a qué organización pertenece.

### Línea de comandos

```
totem-wifi                      # ejecuta en primer plano (desarrollo)
totem-wifi -config RUTA         # fichero de configuración alternativo
totem-wifi -data DIR            # directorio de datos (state.json, log, imágenes)
totem-wifi install | uninstall | start | stop | restart | status   # servicio del SO (administrador/root)
totem-wifi fingerprint HOST:PUERTO   # imprime la huella SHA-256 del certificado de un servidor
totem-wifi check                # prueba la configuración y la conexión con UniFi; sale con código ≠0 si falla
totem-wifi init                 # crea config.json con los valores por defecto
totem-wifi set -unifi-url URL -totems LISTA -admin-ips LISTA   # UniFi, tótems y administración (lo usa install.sh)
totem-wifi firewall-sync        # ajusta ufw a las IPs permitidas (root; lo lanza systemd)
totem-wifi allowlist            # puerto e IPs permitidas
totem-wifi version
```

## 5. Compilación y entrega

- `make build` → binario para la plataforma actual.
- `make build-all` → `dist/totem-wifi_windows_amd64.exe`, `dist/totem-wifi_linux_amd64`,
  `dist/totem-wifi_linux_arm64`, con `CGO_ENABLED=0 -ldflags="-s -w -X main.version=…"`.
- `make release` → además empaqueta cada binario con sus scripts de `deploy/` y el `README.md` en un
  `.zip` (Windows) o `.tar.gz` (Linux). **Eso es lo que se copia a la máquina destino.**
- Los scripts de instalación no descargan nada de Internet.
- `install.ps1` debe: crear `%ProgramData%\TotemWifi`, copiar el exe a `%ProgramFiles%\TotemWifi`,
  ejecutar `totem-wifi install`, abrir el puerto en el Firewall de Windows **solo para las IPs de
  `allowed_clients`** (regla con `-RemoteAddress`), arrancar el servicio y abrir
  `http://localhost:8080/admin` en el navegador.
- `install.sh` equivalente: usuario de sistema `totem`, `/opt/totem-wifi/totem-wifi`,
  `/etc/totem-wifi/config.json` (**600, `totem:totem`**: `/admin` tiene que poder guardarlo), unidad
  systemd con `ProtectSystem=strict`, `ProtectHome=yes`, `NoNewPrivileges=yes`,
  `ReadWritePaths=/var/lib/totem-wifi /etc/totem-wifi`. Opciones `--unifi-url`, `--totems`,
  `--admin-ips` y `--no-ufw`. Si hay ufw, instala `totem-wifi-firewall.{path,service}`.
- Las releases de GitHub se crean a mano desde la web (esta sesión no puede subir etiquetas ni crear
  releases): `make release VERSION=x.y.z` y se adjuntan los 4 ficheros de `dist/`.
- En cada versión nueva, actualiza los enlaces de descarga de `README.md` §2 (`.../download/vX.Y.Z/...`).

## 6. Orden de trabajo seguido (histórico)

Así se construyó la versión 1.0; se conserva como referencia. Los cambios posteriores están en
`CHANGELOG.md`.

1. `go mod init`, `config` con valores por defecto y tests de validación.
2. Cliente `unifi` real + mock, con tests sobre `httptest` que comprueben cabecera `X-API-KEY`, cuerpo
   del POST y manejo de 401/403/5xx.
3. Servicio `voucher` (cooldown, límite diario, persistencia) con tests de reloj inyectado.
4. Servidor `web` con la pantalla del quiosco en modo mock. Hito: se puede abrir `http://localhost:8080/`
   y obtener un código falso.
5. `/admin` y anclaje del certificado. Hito: `totem-wifi check` pasa contra un UniFi real.
6. Servicio del SO, scripts de `deploy/`, `Makefile`, `README.md`.
7. Pasada final: `go vet`, `staticcheck` si está disponible, `go test ./...`, y una revisión de que
   ninguna respuesta HTTP filtra la API key ni rutas internas.

Haz un commit por paso, con mensaje en castellano que diga qué se ha añadido.

## 7. Cómo trabajar conmigo

- Antes de empezar, confirma en un párrafo cómo lo vas a hacer y pregunta solo lo que bloquee
  (ver «Decisiones pendientes» en `CONTEXTO.md`). Lo que no bloquee, déjalo configurable y sigue.
- Comandos listos para copiar y pegar, con una línea que explique cada uno.
- El `README.md` final es para el administrador que instala el servicio: pasos numerados, qué
  ejecutar en PowerShell/bash, cómo obtener la API key y la huella del certificado, cómo pasar de
  mock a real, cómo ver el log y cómo rotar la clave. En castellano, sin jerga innecesaria.
- No uses frameworks web, ORMs ni librerías JS externas. Si dudas entre «una dependencia más» y
  «cincuenta líneas de código», elige las cincuenta líneas.
- No hagas `InsecureSkipVerify` sin `VerifyPeerCertificate`. No guardes la API key en el log.
- **Anonimato del repositorio (obligatorio).** Ni el código, ni los tests, ni los valores por defecto,
  ni `README.md`, ni los commits contienen: el nombre de la organización, nombres de sedes, nombres
  reales de SSID, IPs o rangos de red de producción, hostnames reales ni nombres de personas. En
  ejemplos y tests usa solo IPs de documentación (`192.0.2.x`, `198.51.100.x`, `203.0.113.x`) o
  `127.0.0.1`, y nombres genéricos («Sede A», `WifiPublica`, «Organización»). Única excepción
  aprobada: la URL del propio repositorio de GitHub en los enlaces de descarga del `README.md`. La
  documentación para el administrador (`README.md`, `CHANGELOG.md`, textos de `/admin` y de los
  instaladores) no habla de sedes: la aplicación se conecta a un UniFi OS Server y basta.
  `CONTEXTO.md` es contexto interno y puede mencionarlas. Antes de cada commit,
  pasa `git grep -n -i -E "192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.|10\.[0-9]+\." -- . ':!CLAUDE.md'`
  y revisa cualquier coincidencia que no sea una IP de documentación.
- Al diagnosticar problemas con UniFi, ve por capas: ¿llega TCP a 11443? → ¿coincide la huella? →
  ¿401/403 (clave/rol)? → ¿404 (`site_id`)? → ¿400 (cuerpo del voucher)?
- Para dudas sobre la Integration API, consulta el OpenAPI oficial de la versión instalada
  (`https://apidoc-cdn.ui.com/network/<versión>/integration.json`) antes que la memoria.
