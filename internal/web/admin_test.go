package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"totem-wifi/internal/config"
	"totem-wifi/internal/firewall"
	"totem-wifi/internal/unifi"
)

// navegador simula un navegador de administración (cookie + token CSRF).
type navegador struct {
	t      *testing.T
	e      *entorno
	ip     string
	cookie string
	csrf   string
}

func (n *navegador) hacer(method, target string, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	n.t.Helper()
	var body string
	h := map[string]string{}
	if form != nil {
		body = form.Encode()
		h["Content-Type"] = "application/x-www-form-urlencoded"
	}
	for k, v := range hdr {
		h[k] = v
	}
	if n.cookie != "" {
		h["Cookie"] = cookieName + "=" + n.cookie
	}
	w := n.e.peticion(method, target, n.ip, body, h)
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			n.cookie = c.Value
		}
	}
	if m := regexp.MustCompile(`name="csrf" content="([0-9a-f]+)"`).FindStringSubmatch(w.Body.String()); m != nil {
		n.csrf = m[1]
	}
	return w
}

func (n *navegador) api(path string, form url.Values) apiResult {
	n.t.Helper()
	w := n.hacer("POST", path, form, map[string]string{"X-CSRF-Token": n.csrf})
	var r apiResult
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		n.t.Fatalf("%s: %d %s", path, w.Code, w.Body)
	}
	return r
}

// formularioActual reconstruye el formulario del panel a partir de la configuración.
func formularioActual(c config.Config, csrf string) url.Values {
	v := url.Values{}
	v.Set("csrf", csrf)
	v.Set("listen", c.Listen)
	v.Set("allowed_clients", strings.Join(c.AllowedClients, "\n"))
	v.Set("admin_allowed_clients", strings.Join(c.AdminAllowedClients, "\n"))
	v.Set("unifi_base_url", c.UniFi.BaseURL)
	v.Set("unifi_site_id", c.UniFi.SiteID)
	v.Set("unifi_cert_sha256", c.UniFi.CertSHA256)
	if c.UniFi.Mock {
		v.Set("unifi_mock", "1")
	}
	v.Set("voucher_ssid", c.Voucher.SSID)
	v.Set("voucher_note", c.Voucher.Note)
	v.Set("voucher_minutes", "240")
	v.Set("voucher_guest_limit", "1")
	v.Set("voucher_unused_expire_minutes", strconv.Itoa(c.Voucher.UnusedExpireMinutes))
	v.Set("kiosk_title", c.Kiosk.Title)
	v.Set("kiosk_organization", c.Kiosk.Organization)
	v.Set("kiosk_display_seconds", "60")
	v.Set("kiosk_cooldown_seconds", "20")
	v.Set("kiosk_daily_limit", "300")
	v.Set("kiosk_language", "es")
	return v
}

func entrar(t *testing.T, e *entorno) *navegador {
	t.Helper()
	n := &navegador{t: t, e: e, ip: "127.0.0.1"}
	w := n.hacer("GET", "/admin", nil, nil)
	if !strings.Contains(w.Body.String(), "Primer acceso") {
		t.Fatalf("sin contraseña, /admin debe pedir crearla: %s", w.Body)
	}
	w = n.hacer("POST", "/admin/setup", url.Values{"password": {"contraseña-larga"}, "password2": {"contraseña-larga"}}, nil)
	if w.Code != http.StatusSeeOther || n.cookie == "" {
		t.Fatalf("setup: %d", w.Code)
	}
	w = n.hacer("GET", "/admin", nil, nil)
	if !strings.Contains(w.Body.String(), "Probar conexión con UniFi") || n.csrf == "" {
		t.Fatalf("tras crear la contraseña debe verse el panel: %s", w.Body)
	}
	return n
}

func TestAdminSoloDesdeIPsPermitidas(t *testing.T) {
	e := nuevoEntorno(t, func(c *config.Config) { c.AllowedClients = []string{"192.0.2.0/24"} })
	if w := e.peticion("GET", "/admin", "192.0.2.55", "", nil); w.Code != 403 {
		t.Errorf("un tótem no debe poder abrir /admin: %d", w.Code)
	}
	// Tampoco puede crear la contraseña en el primer arranque.
	if w := e.peticion("POST", "/admin/setup", "192.0.2.55", "password=xxxxxxxxxxxx&password2=xxxxxxxxxxxx",
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"}); w.Code != 403 {
		t.Errorf("setup desde IP no permitida: %d", w.Code)
	}
}

func TestAdminSetupValidaContraseña(t *testing.T) {
	e := nuevoEntorno(t, nil)
	n := &navegador{t: t, e: e, ip: "127.0.0.1"}
	w := n.hacer("POST", "/admin/setup", url.Values{"password": {"corta"}, "password2": {"corta"}}, nil)
	if w.Code != 400 || e.cfg.Get().AdminPasswordHash != "" {
		t.Error("una contraseña corta no debe aceptarse")
	}
	w = n.hacer("POST", "/admin/setup", url.Values{"password": {"contraseña-larga"}, "password2": {"otra-distinta!!"}}, nil)
	if w.Code != 400 {
		t.Error("contraseñas distintas")
	}
}

func TestAdminLoginYBloqueo(t *testing.T) {
	e := nuevoEntorno(t, nil)
	entrar(t, e)
	n := &navegador{t: t, e: e, ip: "127.0.0.1"}
	if w := n.hacer("GET", "/admin", nil, nil); !strings.Contains(w.Body.String(), `action="/admin/login"`) {
		t.Fatal("sin sesión debe pedir la contraseña")
	}
	if w := n.hacer("POST", "/admin/setup", url.Values{"password": {"otra-contraseña"}, "password2": {"otra-contraseña"}}, nil); w.Code != 303 || e.cfg.Get().AdminPasswordHash == "" {
		t.Fatal("setup no debe poder sobrescribir una contraseña existente")
	}
	for i := 0; i < loginMaxFails; i++ {
		n.hacer("POST", "/admin/login", url.Values{"password": {"mal"}}, nil)
	}
	w := n.hacer("POST", "/admin/login", url.Values{"password": {"contraseña-larga"}}, nil)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("tras %d fallos debe bloquearse: %d", loginMaxFails, w.Code)
	}
	n2 := &navegador{t: t, e: e, ip: "::1"}
	if w := n2.hacer("POST", "/admin/login", url.Values{"password": {"contraseña-larga"}}, nil); w.Code != 303 || n2.cookie == "" {
		t.Errorf("login correcto desde otra IP: %d", w.Code)
	}
}

func TestAdminCSRF(t *testing.T) {
	e := nuevoEntorno(t, nil)
	n := entrar(t, e)
	form := formularioActual(e.cfg.Get(), "token-falso")
	if w := n.hacer("POST", "/admin", form, nil); w.Code != 403 {
		t.Errorf("guardar sin token CSRF válido: %d", w.Code)
	}
	if w := n.hacer("POST", "/admin/api/check", nil, nil); w.Code != 403 {
		t.Errorf("API sin token: %d", w.Code)
	}
	form = formularioActual(e.cfg.Get(), n.csrf)
	if w := n.hacer("POST", "/admin", form, map[string]string{"Origin": "http://evil.example"}); w.Code != 403 {
		t.Errorf("guardar desde otro origen: %d", w.Code)
	}
	anon := &navegador{t: t, e: e, ip: "127.0.0.1"}
	if w := anon.hacer("POST", "/admin/api/test-voucher", nil, nil); w.Code != 401 {
		t.Errorf("API sin sesión: %d", w.Code)
	}
}

func TestAdminGuardarYNoMostrarClave(t *testing.T) {
	e := nuevoEntorno(t, nil)
	n := entrar(t, e)
	if strings.Contains(n.hacer("GET", "/admin", nil, nil).Body.String(), claveSecreta) {
		t.Fatal("/admin muestra la API key")
	}
	form := formularioActual(e.cfg.Get(), n.csrf)
	form.Set("voucher_minutes", "90")
	form.Set("extra_ssid_0", "Actos")
	form.Set("allowed_clients", "192.0.2.0/24, 198.51.100.0/24")
	form.Set("confirm_password", "contraseña-larga")
	w := n.hacer("POST", "/admin", form, nil)
	if w.Code != 303 {
		t.Fatalf("guardar: %d %s", w.Code, w.Body)
	}
	c := e.cfg.Get()
	if c.Voucher.Minutes != 90 || len(c.Voucher.ExtraSSIDs) != 1 || len(c.AllowedClients) != 2 {
		t.Errorf("no se ha guardado: %+v", c)
	}
	if c.UniFi.APIKey != claveSecreta {
		t.Error("con el campo de clave vacío debe conservarse la clave actual")
	}
	if c.AdminPasswordHash == "" {
		t.Error("guardar el formulario no debe borrar la contraseña")
	}
	body := n.hacer("GET", "/admin", nil, nil).Body.String()
	if !strings.Contains(body, "Configuración guardada") || !strings.Contains(body, "configurada: sí") {
		t.Error("falta el aviso de guardado o el estado de la clave")
	}

	// Valor no válido: no se guarda y se explica.
	form.Set("voucher_minutes", "0")
	w = n.hacer("POST", "/admin", form, nil)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "voucher.minutes") || e.cfg.Get().Voucher.Minutes != 90 {
		t.Errorf("valor no válido: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), claveSecreta) {
		t.Error("la página de error muestra la API key")
	}

	// Sustituir la clave.
	form.Set("voucher_minutes", "90")
	form.Set("unifi_api_key", "clave-nueva")
	n.hacer("POST", "/admin", form, nil)
	if e.cfg.Get().UniFi.APIKey != "clave-nueva" {
		t.Error("no se ha sustituido la clave")
	}
	if strings.Contains(e.logs.String(), "clave-nueva") || strings.Contains(e.logs.String(), claveSecreta) {
		t.Error("el log contiene la API key")
	}
}

func TestAdminCambioDeContraseña(t *testing.T) {
	e := nuevoEntorno(t, nil)
	n := entrar(t, e)
	hash := e.cfg.Get().AdminPasswordHash
	form := formularioActual(e.cfg.Get(), n.csrf)
	form.Set("current_password", "incorrecta")
	form.Set("new_password", "nueva-contraseña")
	form.Set("new_password2", "nueva-contraseña")
	if w := n.hacer("POST", "/admin", form, nil); w.Code != 400 || e.cfg.Get().AdminPasswordHash != hash {
		t.Error("sin la contraseña actual correcta no debe cambiarse")
	}
	form.Set("current_password", "contraseña-larga")
	if w := n.hacer("POST", "/admin", form, nil); w.Code != 303 || e.cfg.Get().AdminPasswordHash == hash {
		t.Error("no se ha cambiado la contraseña")
	}
}

// unifiFalso arranca un UniFi OS Server de mentira por HTTPS.
func unifiFalso(t *testing.T, clave string) (*httptest.Server, *int32) {
	var vouchers int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != clave {
			w.WriteHeader(401)
			io.WriteString(w, `{"message":"Unauthorized"}`)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/proxy/network/integration/v1/sites":
			io.WriteString(w, `{"data":[{"id":"88f7af54-98f8-306a-a1c7-c9349722b1f6","internalReference":"default","name":"Default"}]}`)
		case r.Method == "POST" && r.URL.Path == "/proxy/network/integration/v1/sites/88f7af54-98f8-306a-a1c7-c9349722b1f6/hotspot/vouchers":
			atomic.AddInt32(&vouchers, 1)
			w.WriteHeader(201)
			io.WriteString(w, `{"vouchers":[{"id":"v","code":"5550001234","name":"x","timeLimitMinutes":240}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &vouchers
}

func TestAdminRecorridoCompletoHastaModoReal(t *testing.T) {
	ufi, creados := unifiFalso(t, "la-clave-buena")
	e := nuevoEntorno(t, func(c *config.Config) { c.UniFi.APIKey = "" })
	n := entrar(t, e)

	// 1. Obtener la huella del servidor (sin guardar todavía la URL).
	r := n.api("/admin/api/fingerprint", url.Values{"base_url": {ufi.URL}})
	if !r.OK || r.SHA256 != unifi.Fingerprint(ufi.Certificate().Raw) {
		t.Fatalf("huella: %+v", r)
	}

	// 2. Guardar URL, huella, clave y desactivar el modo simulación.
	form := formularioActual(e.cfg.Get(), n.csrf)
	form.Set("unifi_base_url", ufi.URL)
	form.Set("unifi_cert_sha256", r.Formatted) // con dos puntos: se normaliza
	form.Set("unifi_api_key", "la-clave-buena")
	form.Del("unifi_mock")
	if w := n.hacer("POST", "/admin", form, nil); w.Code != 303 {
		t.Fatalf("guardar: %d %s", w.Code, w.Body)
	}

	// 3. Probar conexión: detecta y guarda el sitio.
	r = n.api("/admin/api/check", nil)
	if !r.OK || e.cfg.Get().UniFi.SiteID != "88f7af54-98f8-306a-a1c7-c9349722b1f6" {
		t.Fatalf("check: %+v site=%q", r, e.cfg.Get().UniFi.SiteID)
	}

	// 4. Voucher de prueba real.
	r = n.api("/admin/api/test-voucher", nil)
	if !r.OK || r.Code != "55500-01234" || !strings.Contains(r.Message, "REAL") {
		t.Fatalf("voucher de prueba: %+v", r)
	}

	// 5. El quiosco ya entrega códigos reales y sin franja de prueba.
	w := e.peticion("POST", "/api/voucher", "127.0.0.1", "{}", jsonHdr)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"mock":false`) {
		t.Fatalf("quiosco: %d %s", w.Code, w.Body)
	}
	if atomic.LoadInt32(creados) != 2 {
		t.Errorf("vouchers creados en UniFi: %d", *creados)
	}
	if strings.Contains(e.peticion("GET", "/", "127.0.0.1", "", nil).Body.String(), "MODO PRUEBA") {
		t.Error("en modo real no debe aparecer la franja")
	}

	// 6. Clave errónea → capa «Clave / rol».
	form = formularioActual(e.cfg.Get(), n.csrf)
	form.Set("unifi_api_key", "clave-revocada")
	form.Del("unifi_mock")
	n.hacer("POST", "/admin", form, nil)
	r = n.api("/admin/api/check", nil)
	if r.OK || r.Layer != "Clave / rol" {
		t.Errorf("clave errónea: %+v", r)
	}

	// 7. Certificado cambiado → capa «Certificado» y el quiosco da 503.
	form = formularioActual(e.cfg.Get(), n.csrf)
	form.Set("unifi_api_key", "la-clave-buena")
	form.Set("unifi_cert_sha256", strings.Repeat("ab", 32))
	form.Del("unifi_mock")
	n.hacer("POST", "/admin", form, nil)
	r = n.api("/admin/api/check", nil)
	if r.OK || r.Layer != "Certificado" {
		t.Errorf("huella distinta: %+v", r)
	}
	if w := e.peticion("POST", "/api/voucher", "127.0.0.9", "{}", jsonHdr); w.Code != 503 {
		t.Errorf("quiosco con huella distinta: %d", w.Code)
	}
	if !strings.Contains(e.logs.String(), "huella del certificado distinta") {
		t.Error("el log debe explicar que la huella no coincide")
	}
}

func TestAdminCambioDeServidorReiniciaSitio(t *testing.T) {
	e := nuevoEntorno(t, func(c *config.Config) { c.UniFi.SiteID = "sitio-viejo" })
	n := entrar(t, e)
	form := formularioActual(e.cfg.Get(), n.csrf)
	form.Set("unifi_base_url", "https://198.51.100.20:11443") // distinta de la de por defecto
	n.hacer("POST", "/admin", form, nil)
	if e.cfg.Get().UniFi.SiteID != "" {
		t.Error("al cambiar de servidor, el site_id debe volver a detectarse")
	}
}

func TestAdminLogout(t *testing.T) {
	e := nuevoEntorno(t, nil)
	n := entrar(t, e)
	viejo := n.cookie
	n.hacer("POST", "/admin/logout", url.Values{}, nil)
	n.cookie = viejo
	if w := n.hacer("GET", "/admin", nil, nil); !strings.Contains(w.Body.String(), `action="/admin/login"`) {
		t.Error("tras cerrar sesión la cookie antigua no debe servir")
	}
}

func TestAdminCambioDeAccesoPideContraseña(t *testing.T) {
	e := nuevoEntorno(t, nil)
	n := entrar(t, e)
	antes := strings.Join(e.cfg.Get().AllowedClients, ",")
	form := formularioActual(e.cfg.Get(), n.csrf)
	form.Set("allowed_clients", "198.51.100.0/24")
	w := n.hacer("POST", "/admin", form, nil)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "Confirmar con la contraseña") {
		t.Fatalf("sin contraseña: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), `id="confirmar-acceso" hidden`) {
		t.Error("tras el error, el campo de confirmación debe verse")
	}
	if strings.Join(e.cfg.Get().AllowedClients, ",") != antes {
		t.Error("no debe guardarse sin la contraseña")
	}
	form.Set("confirm_password", "incorrecta")
	if w := n.hacer("POST", "/admin", form, nil); w.Code != 400 {
		t.Errorf("contraseña incorrecta: %d", w.Code)
	}
	form.Set("confirm_password", "contraseña-larga")
	if w := n.hacer("POST", "/admin", form, nil); w.Code != 303 {
		t.Fatalf("contraseña correcta: %d", w.Code)
	}
	if c := e.cfg.Get(); len(c.AllowedClients) != 1 || c.AllowedClients[0] != "198.51.100.0/24" {
		t.Errorf("no se ha guardado: %v", c.AllowedClients)
	}
	// Cambiar otra cosa (la duración) no pide contraseña.
	form = formularioActual(e.cfg.Get(), n.csrf)
	form.Set("voucher_minutes", "120")
	if w := n.hacer("POST", "/admin", form, nil); w.Code != 303 {
		t.Errorf("cambio sin tocar el acceso: %d", w.Code)
	}
	if !strings.Contains(e.logs.String(), "equipos permitidos cambiados") {
		t.Error("el cambio de acceso debe quedar en el log")
	}
}

func TestAdminNoPermiteQuedarseFuera(t *testing.T) {
	e := nuevoEntorno(t, func(c *config.Config) { c.AdminAllowedClients = []string{"192.0.2.50"} })
	n := entrar(t, e)
	n.ip = "192.0.2.50"
	form := formularioActual(e.cfg.Get(), n.csrf)
	form.Set("admin_allowed_clients", "192.0.2.99")
	form.Set("confirm_password", "contraseña-larga")
	w := n.hacer("POST", "/admin", form, nil)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "se quedaría sin acceso") {
		t.Errorf("debería impedir dejarse fuera: %d", w.Code)
	}
}

func TestAdminCambioDeServidorBorraHuella(t *testing.T) {
	e := nuevoEntorno(t, func(c *config.Config) { c.UniFi.CertSHA256 = strings.Repeat("ab", 32) })
	n := entrar(t, e)
	form := formularioActual(e.cfg.Get(), n.csrf)
	form.Set("unifi_base_url", "https://198.51.100.20:11443") // distinta de la de por defecto
	n.hacer("POST", "/admin", form, nil)
	if e.cfg.Get().UniFi.CertSHA256 != "" {
		t.Error("la huella del servidor anterior debe borrarse")
	}
	// Si a la vez se indica la huella nueva, se conserva.
	form = formularioActual(e.cfg.Get(), n.csrf)
	form.Set("unifi_base_url", "https://198.51.100.20:11443")
	form.Set("unifi_cert_sha256", strings.Repeat("cd", 32))
	n.hacer("POST", "/admin", form, nil)
	if e.cfg.Get().UniFi.CertSHA256 != strings.Repeat("cd", 32) {
		t.Error("la huella nueva debe guardarse")
	}
}

func TestAdminAyudaClaveYEstadoCortafuegos(t *testing.T) {
	e := nuevoEntorno(t, nil)
	n := entrar(t, e)
	body := n.hacer("GET", "/admin", nil, nil).Body.String()
	if !strings.Contains(body, "¿Cómo crear la clave?") || !strings.Contains(body, "Hotspot") {
		t.Error("faltan las instrucciones para crear la clave")
	}
	if strings.Contains(body, "Cortafuegos (ufw)") {
		t.Error("sin ayudante instalado no debe mostrarse el estado del cortafuegos")
	}
	st := firewall.Status{Time: time.Now(), OK: true, UFWActive: true, Rules: []firewall.Rule{{Src: "192.0.2.0/24", Port: 8080}}}
	if err := firewall.WriteStatus(filepath.Join(e.dir, firewall.StatusFile), st); err != nil {
		t.Fatal(err)
	}
	body = n.hacer("GET", "/admin", nil, nil).Body.String()
	if !strings.Contains(body, "Cortafuegos (ufw)") || !strings.Contains(body, "192.0.2.0/24") {
		t.Error("debe mostrarse el estado del cortafuegos")
	}
	var fv firewallView
	json.Unmarshal(n.hacer("GET", "/admin/api/firewall", nil, nil).Body.Bytes(), &fv)
	if !fv.Managed || fv.State != "ok" {
		t.Errorf("API cortafuegos: %+v", fv)
	}
	st.UFWActive = false
	firewall.WriteStatus(filepath.Join(e.dir, firewall.StatusFile), st)
	json.Unmarshal(n.hacer("GET", "/admin/api/firewall", nil, nil).Body.Bytes(), &fv)
	if fv.State != "inactivo" {
		t.Errorf("ufw desactivado: %+v", fv)
	}
	anon := &navegador{t: t, e: e, ip: "127.0.0.1"}
	if w := anon.hacer("GET", "/admin/api/firewall", nil, nil); w.Code != 401 {
		t.Errorf("sin sesión: %d", w.Code)
	}
}

func TestAdminTextosDeLaPantalla(t *testing.T) {
	e := nuevoEntorno(t, nil)
	n := entrar(t, e)
	body := n.hacer("GET", "/admin", nil, nil).Body.String()
	if !strings.Contains(body, "Textos de la pantalla") || !strings.Contains(body, `name="kiosk_terms_text"`) {
		t.Fatal("falta la sección de textos en /admin")
	}
	form := formularioActual(e.cfg.Get(), n.csrf)
	form.Set("kiosk_header_text", "Servicio de Wi-Fi")
	form.Set("kiosk_intro", "Hola.\r\nSegunda línea.")
	form.Set("kiosk_terms_enabled", "1")
	form.Set("kiosk_terms_text", strings.Repeat("Condición con tildes: áéíóú. ", 600)) // ~18 000 caracteres
	if w := n.hacer("POST", "/admin", form, nil); w.Code != 303 {
		t.Fatalf("guardar textos: %d %s", w.Code, w.Body)
	}
	k := e.cfg.Get().Kiosk
	if k.HeaderText != "Servicio de Wi-Fi" || k.Intro != "Hola.\nSegunda línea." || !k.TermsEnabled || len([]rune(k.TermsText)) < 17000 {
		t.Errorf("no se han guardado los textos: %+v", k.HeaderText)
	}
	body = n.hacer("GET", "/admin", nil, nil).Body.String()
	if !strings.Contains(body, "Servicio de Wi-Fi · Administración") || !strings.Contains(body, k.TermsVersion()) {
		t.Error("la cabecera de /admin debe usar el nombre configurado y mostrarse la versión de las condiciones")
	}
	// Activar las condiciones sin texto no se permite.
	form.Set("kiosk_terms_text", "  ")
	if w := n.hacer("POST", "/admin", form, nil); w.Code != 400 || !strings.Contains(w.Body.String(), "condiciones") {
		t.Errorf("condiciones vacías: %d", w.Code)
	}
}

func TestAdminCaducidadSinUso(t *testing.T) {
	e := nuevoEntorno(t, nil)
	n := entrar(t, e)
	if !strings.Contains(n.hacer("GET", "/admin", nil, nil).Body.String(), "Caducidad de los códigos sin usar (minutos)") {
		t.Fatal("falta la casilla en /admin")
	}
	form := formularioActual(e.cfg.Get(), n.csrf)
	form.Set("voucher_unused_expire_minutes", "10")
	if w := n.hacer("POST", "/admin", form, nil); w.Code != 303 || e.cfg.Get().Voucher.UnusedExpireMinutes != 10 {
		t.Errorf("guardar 10 min: %d %d", w.Code, e.cfg.Get().Voucher.UnusedExpireMinutes)
	}
	form.Set("voucher_unused_expire_minutes", "99999")
	if w := n.hacer("POST", "/admin", form, nil); w.Code != 400 {
		t.Errorf("fuera de rango: %d", w.Code)
	}
}
