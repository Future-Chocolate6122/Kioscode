package web

import (
	"fmt"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// escapeWiFi escapa los caracteres especiales del formato WIFI: (\ ; , : ").
func escapeWiFi(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\;,:"`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// WiFiPayload construye el texto del QR que entienden Android e iOS.
// Sin contraseña: red abierta (con portal cautivo).
func WiFiPayload(ssid, password string) string {
	if password == "" {
		return "WIFI:T:nopass;S:" + escapeWiFi(ssid) + ";;"
	}
	return "WIFI:T:WPA;S:" + escapeWiFi(ssid) + ";P:" + escapeWiFi(password) + ";;"
}

// QRSVG genera el QR como SVG: un único <path> con un cuadrado por módulo,
// con margen blanco incluido y escalable sin perder nitidez.
func QRSVG(content string) ([]byte, error) {
	q, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return nil, err
	}
	bm := q.Bitmap() // incluye la zona de silencio (4 módulos)
	n := len(bm)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, n, n)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#111" d="`, n, n)
	for y, row := range bm {
		for x := 0; x < len(row); x++ {
			if !row[x] {
				continue
			}
			// Agrupa módulos negros consecutivos de la fila en un solo rectángulo.
			w := 1
			for x+w < len(row) && row[x+w] {
				w++
			}
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", x, y, w, w)
			x += w - 1
		}
	}
	b.WriteString(`"/></svg>`)
	return []byte(b.String()), nil
}
