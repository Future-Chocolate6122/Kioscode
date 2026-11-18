# Historial de versiones

Formato: las novedades de cada versión, de la más reciente a la más antigua. Antes de actualizar,
haga siempre una copia de `config.json`: una versión anterior no reconoce los campos que añade una
posterior.

## 1.4.1

### Correcciones
- **Aviso tras desconectar invitados**: ahora indica que el dispositivo **no puede volver a entrar
  con el mismo código** (comprobado en una instalación real con «Dispositivos por código» = 1). Si
  ese ajuste es mayor que 1, avisa de que otros dispositivos podrían usar las plazas que queden.

### Novedades
- **Favicon** en las pestañas de `/admin` (símbolo Wi-Fi).

### Documentación
- README reorganizado según el recorrido del administrador, con capturas (también incluidas en los
  paquetes).
- `CHANGELOG.md` (este fichero) y `CLAUDE.md` actualizado.

## 1.4.0

### Novedades
- **Pantalla del quiosco por pasos**: tras aceptar las condiciones y pulsar «Empezar», la persona ve
  los tres pasos a la vez: conectarse a la red (QR), solicitar el código y escribirlo en su
  dispositivo. En horizontal, tres columnas; en vertical, apilados.
- **El código aparece cuando hace falta**: se genera al pulsar «Solicitar código», ya conectado a la
  red y viendo la página de acceso.
- **Espera entre códigos en el propio botón**: «Podrás solicitar tu código en N s», con cuenta atrás;
  se reactiva solo.
- **Errores en el propio paso** (límite diario, UniFi no responde), con posibilidad de reintentar.
- **Vuelta automática al inicio** tras 2 minutos sin actividad.

### Notas
- Sin cambios en el servidor ni en la configuración.
- Si tiene una introducción propia en `/admin` que menciona «Obtener código», actualícela.

## 1.3.0

### Novedades
- **Invitados conectados**: nueva pestaña en `/admin` con los dispositivos conectados por el portal
  (nombre, IP, MAC, hora de conexión y fin del acceso), con buscador, orden por columna y paginación.
- **Desconectar invitados**: uno, los seleccionados o todos. «Desconectar todos» pide la contraseña
  de administración y puede invalidar los códigos del tótem vigentes.
- **Caducidad de códigos sin usar**: los códigos que nadie usa se borran a los 15 minutos de
  generarse (configurable en `/admin`; 0 la desactiva). Se revisa cada minuto.
- **Aviso en el quiosco**: «Tienes 15 minutos para usarlo; si no, caducará».

### Notas
- Nunca se tocan los códigos creados a mano en UniFi ni los que ya se están usando.
- El ajuste anterior en horas (`unused_cleanup_hours`) se convierte solo a minutos al actualizar.

## 1.2.0

### Novedades
- **Logotipo y fondo desde `/admin`**: subir imágenes al servidor, elegir cuál usar como logotipo y
  cuál como fondo, y borrar las que no se usen.
- **Límites por tipo**: logotipo hasta 1 MB (PNG, SVG, JPG o WebP) y fondo hasta 5 MB (JPG, WebP o
  PNG), con las dimensiones recomendadas a la vista.
- **Seguridad**: se comprueba el contenido real de cada imagen y se rechazan los SVG con código.
- **Recarga automática del quiosco**: los cambios hechos en `/admin` se ven en el tótem en menos de un
  minuto.
- **Límite de datos del voucher**: tope de 1 048 576 MB (1 TB), el máximo que admite UniFi.

### Notas
- Las imágenes copiadas a mano en la carpeta de datos (`logo.png`, `logo.svg`) siguen funcionando.
- La primera vez tras actualizar desde la 1.1, recargue el navegador del tótem a mano.

## 1.1.0

### Novedades
- **Textos editables en `/admin`**: título, nombre en la cabecera, nombre de la organización y texto
  de introducción.
- **Condiciones de uso opcionales**: casilla «He leído y acepto las condiciones de uso» con
  desplegable del texto. El servidor también exige la aceptación y el log anota la versión aceptada.

## 1.0.0

Primera versión.

- Quiosco que pide vouchers del hotspot a UniFi OS Server (Integration API) y los muestra con un QR
  Wi-Fi. Un único ejecutable, sin dependencias, que funciona sin Internet.
- Modo simulación hasta configurar la API key.
- `/admin` con contraseña: configuración, prueba de conexión, voucher de prueba y anclaje del
  certificado de UniFi por su huella SHA-256.
- Espera entre códigos por tótem y límite diario, persistente entre reinicios.
- Instaladores para Ubuntu Server (servicio systemd endurecido y ayudante que mantiene ufw al día) y
  para Windows.
