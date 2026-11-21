# Licencias de terceros

Tótem Wi-Fi se distribuye bajo la licencia MIT (`LICENSE`, en la raíz). El ejecutable incluye
componentes de terceros, cuyas licencias exigen acompañar el programa con sus avisos. Todos aparecen
también, con su texto completo, al final de `LICENSE`:

| Componente | Uso | Licencia | Fichero |
|---|---|---|---|
| Go (biblioteca estándar) | Todo el programa | BSD de 3 cláusulas | `go.txt` |
| `github.com/skip2/go-qrcode` | Generación del QR | MIT | `go-qrcode.txt` |
| `github.com/kardianos/service` | Servicio de Windows y systemd | zlib | `kardianos-service.txt` |
| `golang.org/x/crypto` | Cifrado de la contraseña (bcrypt) | BSD de 3 cláusulas | `x-crypto.txt` |
| `golang.org/x/sys` | Servicio de Windows (solo en el paquete de Windows) | BSD de 3 cláusulas | `x-sys.txt` |
| Montserrat, Raleway y Open Sans | Fuentes del quiosco | SIL Open Font License 1.1 | `OFL-*.txt` |

Si se añade o actualiza una dependencia, copie su licencia aquí y en `LICENSE`. `make release`
comprueba que cada módulo que lleva el ejecutable aparece en `LICENSE` y se detiene si falta alguno.
