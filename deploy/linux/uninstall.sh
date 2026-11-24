#!/usr/bin/env bash
# Desinstala Kioscode.
#
#   sudo ./uninstall.sh           quita el servicio y el ejecutable; conserva configuración y datos
#   sudo ./uninstall.sh --purge   además borra /etc/kioscode, /var/lib/kioscode y el usuario kioscode
set -euo pipefail

APP_DIR=/opt/kioscode
CFG_DIR=/etc/kioscode
DATA_DIR=/var/lib/kioscode
BIN="$APP_DIR/kioscode"
UNIT=Kioscode
PURGE=0
[ "${1:-}" = "--purge" ] && PURGE=1

if [ "$(id -u)" -ne 0 ]; then
  echo "Ejecute este script como root: sudo $0 $*" >&2
  exit 1
fi

if systemctl list-unit-files "$UNIT.service" >/dev/null 2>&1 && [ -f "/etc/systemd/system/$UNIT.service" ]; then
  systemctl stop "$UNIT" 2>/dev/null || true
  systemctl disable "$UNIT" >/dev/null 2>&1 || true
  if [ -x "$BIN" ]; then
    "$BIN" uninstall -config "$CFG_DIR/config.json" -data "$DATA_DIR" || rm -f "/etc/systemd/system/$UNIT.service"
  else
    rm -f "/etc/systemd/system/$UNIT.service"
  fi
  systemctl daemon-reload
  echo "Servicio eliminado."
fi

FW_UNIT=kioscode-firewall
if [ -f "/etc/systemd/system/$FW_UNIT.path" ]; then
  systemctl disable --now "$FW_UNIT.path" >/dev/null 2>&1 || true
  rm -f "/etc/systemd/system/$FW_UNIT.path" "/etc/systemd/system/$FW_UNIT.service"
  systemctl daemon-reload
  echo "Ayudante del cortafuegos eliminado."
fi

# Quita las reglas «Kioscode» de ufw. Se leen de los ficheros de reglas
# (funciona también con ufw desactivado); el comentario va en hexadecimal.
if command -v ufw >/dev/null 2>&1; then
  grep -h '^### tuple ### allow tcp .* comment=4b696f73636f6465$' \
      /etc/ufw/user.rules /etc/ufw/user6.rules 2>/dev/null | awk '{print $9, $6}' |
    while read -r src port; do
      LC_ALL=C ufw delete allow from "$src" to any port "$port" proto tcp >/dev/null && echo "Regla de ufw eliminada: $src → $port/tcp"
    done || true
fi

rm -rf "$APP_DIR"
echo "Ejecutable eliminado ($APP_DIR)."

if [ "$PURGE" -eq 1 ]; then
  rm -rf "$CFG_DIR" "$DATA_DIR"
  id kioscode >/dev/null 2>&1 && userdel kioscode
  echo "Configuración, datos y usuario «kioscode» eliminados."
else
  echo "Se conservan $CFG_DIR y $DATA_DIR (use --purge para borrarlos)."
fi
