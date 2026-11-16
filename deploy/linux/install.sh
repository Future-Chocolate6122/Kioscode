#!/usr/bin/env bash
# Instala (o actualiza) Tótem Wi-Fi como servicio systemd en Ubuntu Server.
#
#   sudo ./install.sh [opciones]
#
# Opciones para esta sede (opcionales; todo se puede cambiar después en /admin):
#   --unifi-url URL      UniFi OS Server de la sede, p. ej. https://192.0.2.10:11443
#   --totems LISTA       IPs o redes de los tótems, separadas por comas, p. ej. 192.0.2.0/24
#   --admin-ips LISTA    IPs que pueden abrir /admin además de este servidor, p. ej. 192.0.2.50
#   --no-ufw             no instalar el ayudante que mantiene ufw al día
#
# No descarga nada de Internet. Conserva config.json y los datos si ya existen.
set -euo pipefail

APP_DIR=/opt/totem-wifi
CFG_DIR=/etc/totem-wifi
DATA_DIR=/var/lib/totem-wifi
CFG="$CFG_DIR/config.json"
BIN="$APP_DIR/totem-wifi"
USR=totem
UNIT=TotemWifi
FW_UNIT=totem-wifi-firewall
UFW=1
SET_ARGS=()

while [ $# -gt 0 ]; do
  case "$1" in
    --unifi-url) SET_ARGS+=(-unifi-url "${2:?falta la URL}"); shift 2 ;;
    --totems)    SET_ARGS+=(-totems "${2:?falta la lista}"); shift 2 ;;
    --admin-ips) SET_ARGS+=(-admin-ips "${2:?falta la lista}"); shift 2 ;;
    --no-ufw)    UFW=0; shift ;;
    --ufw)       UFW=1; shift ;;   # compatibilidad: ya es el comportamiento por defecto
    -h|--help)   sed -n '2,13p' "$0"; exit 0 ;;
    *) echo "Opción desconocida: $1 (use --help)" >&2; exit 2 ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then
  echo "Ejecute este script como root: sudo $0 $*" >&2
  exit 1
fi
cd "$(dirname "$(readlink -f "$0")")"
SRC=./totem-wifi
[ -f "$SRC" ] || SRC=$(ls ./totem-wifi_linux_* 2>/dev/null | head -n1 || true)
if [ -z "$SRC" ] || [ ! -f "$SRC" ]; then
  echo "No se encuentra el ejecutable «totem-wifi» junto a este script." >&2
  exit 1
fi

echo "1/6 Usuario de sistema «$USR»"
if ! id "$USR" >/dev/null 2>&1; then
  useradd --system --no-create-home --home-dir "$DATA_DIR" --shell /usr/sbin/nologin "$USR"
fi

echo "2/6 Ejecutable en $BIN"
if systemctl is-active --quiet "$UNIT" 2>/dev/null; then
  systemctl stop "$UNIT"
fi
install -d -m 0755 "$APP_DIR"
install -m 0755 "$SRC" "$BIN"

echo "3/6 Configuración en $CFG_DIR y datos en $DATA_DIR"
# config.json contiene la API key y el hash de la contraseña: solo lo leen el
# servicio (usuario totem) y root. El directorio es del servicio porque /admin
# guarda los cambios en él.
install -d -m 0750 -o "$USR" -g "$USR" "$CFG_DIR"
install -d -m 0750 -o "$USR" -g "$USR" "$DATA_DIR"
if [ ! -f "$CFG" ]; then
  runuser -u "$USR" -- "$BIN" -config "$CFG" -data "$DATA_DIR" init
fi
if [ "${#SET_ARGS[@]}" -gt 0 ]; then
  runuser -u "$USR" -- "$BIN" -config "$CFG" -data "$DATA_DIR" set "${SET_ARGS[@]}" | grep -v 'reinícielo'
fi
chown "$USR:$USR" "$CFG"
chmod 0600 "$CFG"

echo "4/6 Servicio systemd «$UNIT»"
if [ -f "/etc/systemd/system/$UNIT.service" ]; then
  "$BIN" uninstall -config "$CFG" -data "$DATA_DIR" >/dev/null   # regenera la unidad
fi
"$BIN" install -config "$CFG" -data "$DATA_DIR"

echo "5/6 Cortafuegos (ufw)"
FW_STATE="no gestionado"
if [ "$UFW" -eq 1 ] && command -v ufw >/dev/null 2>&1; then
  # Ayudante: systemd lo lanza como root cada vez que cambia config.json y ajusta
  # solo las reglas «Totem Wi-Fi» de ufw. El servicio web sigue sin privilegios.
  cat > "/etc/systemd/system/$FW_UNIT.service" <<UNIT
[Unit]
Description=Tótem Wi-Fi: ajusta ufw a los equipos permitidos
After=ufw.service

[Service]
Type=oneshot
ExecStart=$BIN firewall-sync -config $CFG -data $DATA_DIR
UNIT
  cat > "/etc/systemd/system/$FW_UNIT.path" <<UNIT
[Unit]
Description=Tótem Wi-Fi: vigila config.json para actualizar ufw

[Path]
PathChanged=$CFG
Unit=$FW_UNIT.service

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable --now "$FW_UNIT.path" >/dev/null 2>&1
  if "$BIN" firewall-sync -config "$CFG" -data "$DATA_DIR" | sed 's/^/    /'; then
    FW_STATE="gestionado automáticamente"
  else
    FW_STATE="ERROR (revise el mensaje anterior)"
  fi
  chmod 0644 "$DATA_DIR/firewall-status.json" 2>/dev/null || true
elif [ "$UFW" -eq 1 ]; then
  echo "    ufw no está instalado: no se gestiona el cortafuegos."
else
  echo "    omitido (--no-ufw)."
  # Si había un ayudante de una instalación anterior, se retira.
  if [ -f "/etc/systemd/system/$FW_UNIT.path" ]; then
    systemctl disable --now "$FW_UNIT.path" >/dev/null 2>&1 || true
    rm -f "/etc/systemd/system/$FW_UNIT.path" "/etc/systemd/system/$FW_UNIT.service" "$DATA_DIR/firewall-status.json"
  fi
fi

echo "6/6 Arranque"
systemctl daemon-reload
systemctl enable "$UNIT" >/dev/null 2>&1
systemctl restart "$UNIT"
sleep 1
if systemctl is-active --quiet "$UNIT"; then
  echo "    El servicio está en marcha."
else
  echo "    ERROR: el servicio no ha arrancado. Revise: journalctl -u $UNIT -n 50" >&2
  exit 1
fi

PORT=8080
while read -r k v; do
  [ "$k" = port ] && PORT="$v"
done < <("$BIN" -config "$CFG" allowlist)

cat <<FIN

Instalación terminada (modo simulación hasta que configure UniFi en /admin).

  Administración:  http://localhost:$PORT/admin
  Quiosco:         http://IP_DE_ESTE_SERVIDOR:$PORT/
  Cortafuegos:     $FW_STATE

Si este servidor no tiene navegador, abra la administración desde su PC con un
túnel SSH y luego visite http://localhost:$PORT/admin en su navegador:

  ssh -L $PORT:localhost:$PORT usuario@IP_DE_ESTE_SERVIDOR

Log:  journalctl -u $UNIT -f    o    $DATA_DIR/totem-wifi.log
FIN
