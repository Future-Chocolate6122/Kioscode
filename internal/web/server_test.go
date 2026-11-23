package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"totem-wifi/internal/config"
	"totem-wifi/internal/unifi"
	"totem-wifi/internal/voucher"
)

const claveSecreta = "API-KEY-QUE-NUNCA-DEBE-SALIR"

type entorno struct {
	srv  *Server
	h    http.Handler
	cfg  *config.Store
	logs *bytes.Buffer
	dir  string
}

func nuevoEntorno(t *testing.T, mod func(*config.Config)) *entorno {
	t.Helper()
	dir := t.TempDir()
	c := config.Default()
	c.UniFi.APIKey = claveSecreta
	if mod != nil {
		mod(&c)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	st := config.NewMemoryStore(c)
	var logs bytes.Buffer
	lg := log.New(&logs, "", 0)
	var prov unifi.Provider
	svc := voucher.New(st, func(c config.Config) unifi.Client {
		return prov.Get(c.UniFi.Mock, unifi.Config{BaseURL: c.UniFi.BaseURL, APIKey: c.UniFi.APIKey, CertSHA256: c.UniFi.CertSHA256})
	}, filepath.Join(dir, "state.json"), lg)
	s := New(st, svc, dir, lg, "test")
	return &entorno{srv: s, h: s.Handler(), cfg: st, logs: &logs, dir: dir}
}

func (e *entorno) peticion(method, target, ip string, body string, hdr map[string]string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	r.RemoteAddr = ip + ":40000"
	if strings.Contains(ip, ":") {
		r.RemoteAddr = "[" + ip + "]:40000"
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

var jsonHdr = map[string]string{"Content-Type": "application/json"}

func TestIndexMock(t *testing.T) {
	e := nuevoEntorno(t, nil)
	w := e.peticion("GET", "/", "127.0.0.1", "", nil)
	if w.Code != 200 {
		t.Fatalf("GET / = %d", w.Code)
	}
	b := w.Body.String()
	for _, want := range []string{"MODO PRUEBA", "WifiPublica", "Empezar", "Conéctate a la red", "Solicitar código", "Escribe el código", "pulsa «Conectar»", `id="paso-ssid">WifiPublica<`, "/static/app.css", `class="tema-claro`} {
		if !strings.Contains(b, want) {
			t.Errorf("la página no contiene %q", want)
		}
	}
	if strings.Contains(b, "selector-red\"") || strings.Contains(b, `name="ssid"`) {
		t.Error("con un solo SSID no debe mostrarse el selector")
	}
	if strings.Contains(b, "http://") || strings.Contains(b, "https://") {
		t.Error("la página no debe referenciar dominios externos")
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Error("falta la CSP")
	}
	// Con «no-referrer», Chrome envía «Origin: null» en los formularios de /admin y se rechazarían.
	if w.Header().Get("Referrer-Policy") != "same-origin" {
		t.Errorf("Referrer-Policy = %q", w.Header().Get("Referrer-Policy"))
	}
}

func TestIndexSelectorYModoReal(t *testing.T) {
	e := nuevoEntorno(t, func(c *config.Config) {
		c.UniFi.Mock = false
		c.Voucher.ExtraSSIDs = []config.SSID{{SSID: "Actos"}}
	})
	b := e.peticion("GET", "/", "127.0.0.1", "", nil).Body.String()
	if !strings.Contains(b, `name="ssid"`) || !strings.Contains(b, "Actos") {
		t.Error("con varios SSID debe mostrarse el selector")
	}
	if strings.Contains(b, "MODO PRUEBA") {
		t.Error("en modo real no debe aparecer la franja de prueba")
	}
}

func TestAllowlist(t *testing.T) {
	e := nuevoEntorno(t, func(c *config.Config) { c.AllowedClients = []string{"192.0.2.0/24"} })
	if w := e.peticion("GET", "/", "192.0.2.40", "", nil); w.Code != 200 {
		t.Errorf("IP permitida: %d", w.Code)
	}
	if w := e.peticion("GET", "/", "198.51.100.5", "", nil); w.Code != 403 {
		t.Errorf("IP no permitida: %d", w.Code)
	}
	if w := e.peticion("POST", "/api/voucher", "198.51.100.5", "{}", jsonHdr); w.Code != 403 {
		t.Errorf("API desde IP no permitida: %d", w.Code)
	}
	if w := e.peticion("GET", "/", "::1", "", nil); w.Code != 200 {
		t.Errorf("localhost IPv6: %d", w.Code)
	}
	// X-Forwarded-For no sirve para saltarse la lista.
	if w := e.peticion("GET", "/", "203.0.113.1", "", map[string]string{"X-Forwarded-For": "192.0.2.5"}); w.Code != 403 {
		t.Errorf("X-Forwarded-For no debe tenerse en cuenta: %d", w.Code)
	}
	if !strings.Contains(e.logs.String(), "acceso denegado") {
		t.Error("los accesos denegados deben quedar en el log")
	}
}

func TestVoucherMockYCooldown(t *testing.T) {
	e := nuevoEntorno(t, nil)
	w := e.peticion("POST", "/api/voucher", "127.0.0.1", "{}", jsonHdr)
	if w.Code != 200 {
		t.Fatalf("POST /api/voucher = %d %s", w.Code, w.Body)
	}
	var r voucherResponse
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Code) != 10 || r.Formatted != r.Code[:5]+"-"+r.Code[5:] || !r.Mock || r.SSID != "WifiPublica" ||
		r.ExpiresInSeconds != 60 || !strings.HasPrefix(r.QRURL, "/api/qr.svg?ssid=") {
		t.Errorf("respuesta inesperada: %+v", r)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("la respuesta con el código no debe guardarse en caché")
	}
	w = e.peticion("POST", "/api/voucher", "127.0.0.1", "{}", jsonHdr)
	if w.Code != 429 || !strings.Contains(w.Body.String(), "Espera") || w.Header().Get("Retry-After") == "" {
		t.Errorf("segunda petición: %d %s", w.Code, w.Body)
	}
}

func TestVoucherLimiteDiario(t *testing.T) {
	e := nuevoEntorno(t, func(c *config.Config) { c.Kiosk.DailyLimit = 1 })
	e.peticion("POST", "/api/voucher", "127.0.0.1", "{}", jsonHdr)
	w := e.peticion("POST", "/api/voucher", "127.0.0.2", "{}", jsonHdr)
	if w.Code != 429 || !strings.Contains(w.Body.String(), "mañana") {
		t.Errorf("límite diario: %d %s", w.Code, w.Body)
	}
}

func TestVoucherUniFiNoDisponible(t *testing.T) {
	// Modo real sin huella: 503 con mensaje amable y detalle solo en el log.
	e := nuevoEntorno(t, func(c *config.Config) { c.UniFi.Mock = false })
	w := e.peticion("POST", "/api/voucher", "127.0.0.1", "{}", jsonHdr)
	if w.Code != 503 {
		t.Fatalf("código %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Ahora mismo no podemos darte un código") {
		t.Errorf("mensaje para el ciudadano: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), "huella") || strings.Contains(w.Body.String(), "cert") {
		t.Error("al ciudadano no se le muestran detalles técnicos")
	}
	if !strings.Contains(e.logs.String(), "huella") {
		t.Error("el log debe explicar que falta la huella")
	}
}

func TestVoucherProteccionesDeOrigen(t *testing.T) {
	e := nuevoEntorno(t, nil)
	if w := e.peticion("POST", "/api/voucher", "127.0.0.1", "{}", map[string]string{"Content-Type": "text/plain"}); w.Code != 403 {
		t.Errorf("sin JSON: %d", w.Code)
	}
	if w := e.peticion("POST", "/api/voucher", "127.0.0.1", "{}", map[string]string{"Content-Type": "application/json", "Origin": "http://evil.example"}); w.Code != 403 {
		t.Errorf("otro origen: %d", w.Code)
	}
	if w := e.peticion("POST", "/api/voucher", "127.0.0.1", `{"ssid":"OtraRed"}`, jsonHdr); w.Code != 400 {
		t.Errorf("SSID no configurado: %d", w.Code)
	}
	if w := e.peticion("GET", "/api/voucher", "127.0.0.1", "", nil); w.Code != 405 {
		t.Errorf("GET /api/voucher: %d", w.Code)
	}
}

func TestQR(t *testing.T) {
	e := nuevoEntorno(t, func(c *config.Config) { c.Voucher.ExtraSSIDs = []config.SSID{{SSID: "Actos", Password: "x;y"}} })
	w := e.peticion("GET", "/api/qr.svg?ssid="+url.QueryEscape(config.Default().Voucher.SSID), "127.0.0.1", "", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(w.Body.String(), "<svg") {
		t.Fatalf("QR: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if w := e.peticion("GET", "/api/qr.svg?ssid=Actos", "127.0.0.1", "", nil); w.Code != 200 {
		t.Errorf("QR de SSID adicional: %d", w.Code)
	}
	if w := e.peticion("GET", "/api/qr.svg?ssid=RedOculta", "127.0.0.1", "", nil); w.Code != 404 {
		t.Errorf("QR de SSID arbitrario: %d", w.Code)
	}
}

func TestWiFiPayload(t *testing.T) {
	if got := WiFiPayload("WifiPublica", ""); got != "WIFI:T:nopass;S:WifiPublica;;" {
		t.Error(got)
	}
	if got := WiFiPayload(`Red;"A",B:\`, `cl;ave`); got != `WIFI:T:WPA;S:Red\;\"A\"\,B\:\\;P:cl\;ave;;` {
		t.Error(got)
	}
}

func TestHealthz(t *testing.T) {
	e := nuevoEntorno(t, nil)
	e.peticion("POST", "/api/voucher", "127.0.0.1", "{}", jsonHdr)
	w := e.peticion("GET", "/healthz", "127.0.0.1", "", nil)
	var h map[string]any
	json.Unmarshal(w.Body.Bytes(), &h)
	if w.Code != 200 || h["ok"] != true || h["mock"] != true || h["unifi"] != "ok" || h["daily_used"] != 1.0 {
		t.Errorf("healthz: %d %v", w.Code, h)
	}
	e2 := nuevoEntorno(t, func(c *config.Config) { c.UniFi.Mock = false })
	w = e2.peticion("GET", "/healthz", "127.0.0.1", "", nil)
	json.Unmarshal(w.Body.Bytes(), &h)
	if w.Code != 503 || h["unifi"] != "error" || h["ok"] != false {
		t.Errorf("healthz sin UniFi: %d %v", w.Code, h)
	}
}

func TestEstaticosYLogo(t *testing.T) {
	e := nuevoEntorno(t, nil)
	for path, code := range map[string]int{
		"/static/app.css":                   200,
		"/static/app.js":                    200,
		"/static/fonts/open-sans-700.woff2": 200,
		"/static/index.html":                404,
		"/static/":                          404,
		"/static/fonts/":                    404,
		"/logo":                             404, // aún no hay logo
		"/no-existe":                        404,
		"/favicon.ico":                      200,
		"/static/favicon.svg":               200,
	} {
		if w := e.peticion("GET", path, "127.0.0.1", "", nil); w.Code != code {
			t.Errorf("%s: %d, want %d", path, w.Code, code)
		}
	}
	os.WriteFile(filepath.Join(e.dir, "logo.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o644)
	w := e.peticion("GET", "/logo", "127.0.0.1", "", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("logo: %d %v", w.Code, w.Header())
	}
	if !strings.Contains(e.peticion("GET", "/", "127.0.0.1", "", nil).Body.String(), `src="/logo?v=`) {
		t.Error("con logo.svg en el directorio de datos, la página debe mostrarlo")
	}
}

func TestNingunaRespuestaFiltraLaClave(t *testing.T) {
	e := nuevoEntorno(t, nil)
	for _, p := range []struct{ m, u, b string }{
		{"GET", "/", ""}, {"POST", "/api/voucher", "{}"}, {"GET", "/healthz", ""}, {"GET", "/api/qr.svg", ""},
	} {
		w := e.peticion(p.m, p.u, "127.0.0.1", p.b, jsonHdr)
		if strings.Contains(w.Body.String(), claveSecreta) {
			t.Errorf("%s %s filtra la API key", p.m, p.u)
		}
	}
	if strings.Contains(e.logs.String(), claveSecreta) {
		t.Error("el log contiene la API key")
	}
}

func TestTextosEditablesEnPantalla(t *testing.T) {
	e := nuevoEntorno(t, func(c *config.Config) {
		c.Kiosk.Title = "Internet libre"
		c.Kiosk.Organization = "Organización"
		c.Kiosk.Intro = "Internet gratis en 3 pasos"
	})
	b := e.peticion("GET", "/", "127.0.0.1", "", nil).Body.String()
	for _, want := range []string{`<h1 class="titulo">Internet libre</h1>`, "<title>Internet libre</title>", `<span class="logo-texto">Organización</span>`, `<p class="intro">Internet gratis en 3 pasos</p>`} {
		if !strings.Contains(b, want) {
			t.Errorf("falta el texto configurado %q", want)
		}
	}
	if strings.Contains(b, `id="acepto"`) || strings.Contains(b, "inactivo") {
		t.Error("sin condiciones activadas no debe haber casilla ni botón inactivo")
	}
	// Sin frase propia no se muestra ninguna; ni pie, ni nombre repetido en la cabecera.
	b = nuevoEntorno(t, nil).peticion("GET", "/", "127.0.0.1", "", nil).Body.String()
	for _, no := range []string{`class="intro"`, `class="servicio"`, `class="pie"`, "Servicio público"} {
		if strings.Contains(b, no) {
			t.Errorf("no debería aparecer %q", no)
		}
	}
}

func TestCondicionesDeUso(t *testing.T) {
	e := nuevoEntorno(t, func(c *config.Config) {
		c.Kiosk.TermsEnabled = true
		c.Kiosk.TermsText = "1. Uso responsable.\n\n2. <script>alert(1)</script>"
	})
	b := e.peticion("GET", "/", "127.0.0.1", "", nil).Body.String()
	for _, want := range []string{`id="acepto"`, "He leído y acepto las condiciones de uso", "Ver condiciones", "1. Uso responsable.", `boton-principal inactivo`} {
		if !strings.Contains(b, want) {
			t.Errorf("falta %q en la pantalla", want)
		}
	}
	if strings.Contains(b, "<script>alert(1)</script>") || !strings.Contains(b, "&lt;script&gt;") {
		t.Error("el texto de las condiciones debe mostrarse como texto, sin ejecutar HTML")
	}

	// Sin aceptar: el servidor no entrega código aunque la casilla se salte en el navegador.
	w := e.peticion("POST", "/api/voucher", "127.0.0.1", `{}`, jsonHdr)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "aceptar las condiciones") {
		t.Fatalf("sin aceptar: %d %s", w.Code, w.Body)
	}
	if strings.Contains(e.logs.String(), "voucher creado") {
		t.Error("no debe crearse ningún voucher sin aceptar las condiciones")
	}
	w = e.peticion("POST", "/api/voucher", "127.0.0.1", `{"accepted":true}`, jsonHdr)
	if w.Code != 200 {
		t.Fatalf("aceptadas: %d %s", w.Code, w.Body)
	}
	ver := e.cfg.Get().Kiosk.TermsVersion()
	if !strings.Contains(e.logs.String(), "condiciones_aceptadas="+ver) {
		t.Errorf("el log debe anotar la versión aceptada (%s): %s", ver, e.logs.String())
	}
}

func TestAvisoCaducidadSinUso(t *testing.T) {
	e := nuevoEntorno(t, nil) // 15 minutos por defecto
	var r voucherResponse
	w := e.peticion("POST", "/api/voucher", "127.0.0.1", "{}", jsonHdr)
	json.Unmarshal(w.Body.Bytes(), &r)
	if r.ValidFor != "Válido 4 horas" || r.UseWithin != "úsalo en menos de 15 minutos" {
		t.Errorf("validez y aviso: %q / %q", r.ValidFor, r.UseWithin)
	}
	e2 := nuevoEntorno(t, func(c *config.Config) { c.Voucher.UnusedExpireMinutes = 90 })
	w = e2.peticion("POST", "/api/voucher", "127.0.0.1", "{}", jsonHdr)
	json.Unmarshal(w.Body.Bytes(), &r)
	if r.UseWithin != "úsalo en menos de 1 hora y 30 minutos" {
		t.Errorf("aviso con 90 min: %q", r.UseWithin)
	}
	e3 := nuevoEntorno(t, func(c *config.Config) { c.Voucher.UnusedExpireMinutes = 0 })
	w = e3.peticion("POST", "/api/voucher", "127.0.0.1", "{}", jsonHdr)
	if strings.Contains(w.Body.String(), "use_within") {
		t.Error("sin caducidad no debe haber aviso")
	}
}
