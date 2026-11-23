// Paquete web: servidor HTTP del quiosco y de la administración.
//
// Solo este servidor habla con UniFi; el navegador del tótem nunca ve la API key.
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"totem-wifi/internal/config"
	"totem-wifi/internal/i18n"
	"totem-wifi/internal/unifi"
	"totem-wifi/internal/voucher"
)

//go:embed static
var staticFS embed.FS

// Server agrupa las dependencias del servidor web.
type Server struct {
	cfg     *config.Store
	svc     *voucher.Service
	dataDir string
	log     *log.Logger
	version string

	// FetchCert permite sustituir la lectura del certificado en los tests.
	FetchCert func(ctx context.Context, hostport string) (unifi.CertInfo, error)

	tmpl     *template.Template
	static   http.Handler
	qrMu     sync.Mutex
	qrCache  map[string][]byte
	sessions *sessionStore
	logins   *loginLimiter
}

// New crea el servidor.
func New(cfg *config.Store, svc *voucher.Service, dataDir string, logger *log.Logger, version string) *Server {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"join":   strings.Join,
		"hora":   hora,
		"metodo": metodo,
	}).ParseFS(sub, "*.html"))
	return &Server{
		cfg:       cfg,
		svc:       svc,
		dataDir:   dataDir,
		log:       logger,
		version:   version,
		FetchCert: unifi.FetchCertificate,
		tmpl:      tmpl,
		static:    http.StripPrefix("/static/", http.FileServerFS(sub)),
		qrCache:   map[string][]byte{},
		sessions:  newSessionStore(),
		logins:    newLoginLimiter(),
	}
}

// Handler devuelve el enrutador completo con las cabeceras de seguridad.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	kiosk := s.allow(func(c config.Config) []string { return c.AllowedClients })
	admin := s.allow(func(c config.Config) []string { return c.AdminAllowedClients })
	either := s.allow(func(c config.Config) []string {
		return append(append([]string{}, c.AllowedClients...), c.AdminAllowedClients...)
	})

	mux.Handle("GET /{$}", kiosk(http.HandlerFunc(s.handleIndex)))
	mux.Handle("POST /api/voucher", kiosk(http.HandlerFunc(s.handleVoucher)))
	mux.Handle("GET /api/qr.svg", either(http.HandlerFunc(s.handleQR)))
	mux.Handle("GET /healthz", either(http.HandlerFunc(s.handleHealth)))
	mux.Handle("GET /api/version", kiosk(http.HandlerFunc(s.handleVersion)))
	mux.Handle("GET /static/", either(http.HandlerFunc(s.handleStatic)))
	mux.Handle("GET /logo", either(http.HandlerFunc(s.handleLogo)))
	mux.Handle("GET /background", either(http.HandlerFunc(s.handleBackground)))
	mux.Handle("GET /favicon.ico", either(http.HandlerFunc(s.handleFavicon)))
	s.registerAdmin(mux, admin)
	return securityHeaders(mux)
}

// clientIP obtiene la IP del cliente de la conexión TCP. No se confía en
// X-Forwarded-For: no hay proxy delante y sería trivial falsificarla.
func clientIP(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

// allow crea un middleware que solo deja pasar a las IPs de la lista indicada.
func (s *Server) allow(list func(config.Config) []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			al, _ := config.ParseAllowlist(list(s.cfg.Get())) // ya validada al cargar
			ip := clientIP(r)
			if !ip.IsValid() || !al.Allows(ip) {
				s.log.Printf("acceso denegado: %s %s desde %s (no está en la lista de clientes permitidos)", r.Method, r.URL.Path, ip)
				http.Error(w, "Acceso no permitido desde este equipo.", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeaders añade cabeceras comunes. La CSP impide cargar nada de otro
// dominio y ejecutar scripts en línea.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self'; style-src 'self'; script-src 'self'; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin") // con no-referrer, Chrome envía «Origin: null» en los formularios
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func (s *Server) texts(c config.Config) i18n.Texts {
	t, err := i18n.Load(c.Kiosk.Language, s.dataDir)
	if err != nil {
		s.log.Printf("AVISO: fichero de idioma %q mal formado: %v", c.Kiosk.Language, err)
	}
	return t
}

// ---------------------------------------------------------------------------
// Quiosco

type indexData struct {
	Organization  string
	Intro         string // frase opcional bajo el título (vacía = no se muestra)
	Theme         string // tema del quiosco: clase tema-<Theme> del <body>
	TermsEnabled  bool
	TermsText     string
	Lang          string
	Title         string
	Mock          bool
	SSIDs         []string
	MainSSID      string
	HasLogo       bool
	HasBackground bool
	T             i18n.Texts
	ClientConfig  string // JSON para app.js, en un atributo data-
	Version       string
	Rev           string // huella de lo que se ve en pantalla (recarga automática)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	t := s.texts(c)
	var ssids []string
	for _, x := range c.SSIDs() {
		ssids = append(ssids, x.SSID)
	}
	rev := s.kioskRevision(c)
	clientCfg, _ := json.Marshal(map[string]any{
		"display_seconds": c.Kiosk.DisplaySeconds,
		"rev":             rev,
		"textos": map[string]string{
			"cuenta_atras":        t["cuenta_atras"],
			"error_general":       t["error_general"],
			"paso2_titulo":        t["paso2_titulo"],
			"paso2_titulo_codigo": t["paso2_titulo_codigo"],
			"boton_solicitar":     t["boton_solicitar"],
			"boton_espera":        t["boton_espera"],
			"ver_condiciones":     t["ver_condiciones"],
			"ocultar_condiciones": t["ocultar_condiciones"],
		},
	})
	data := indexData{
		Lang:          c.Kiosk.Language,
		Title:         c.Kiosk.Title,
		Organization:  c.Kiosk.Organization,
		Intro:         c.Kiosk.Intro,
		Theme:         c.Kiosk.Theme,
		TermsEnabled:  c.Kiosk.TermsEnabled,
		TermsText:     c.Kiosk.TermsText,
		Mock:          c.UniFi.Mock,
		SSIDs:         ssids,
		MainSSID:      c.Voucher.SSID,
		HasLogo:       s.logoPath(c) != "",
		HasBackground: s.backgroundPath(c) != "",
		T:             t,
		ClientConfig:  string(clientCfg),
		Version:       s.version,
		Rev:           rev,
	}
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "index.html", data); err != nil {
		s.log.Printf("ERROR plantilla index.html: %v", err)
		http.Error(w, "Error interno", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(buf.Bytes()) //nolint:errcheck
}

// kioskRevision resume todo lo que afecta a la pantalla: si cambia, los tótems recargan.
func (s *Server) kioskRevision(c config.Config) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%v|", s.version, c.UniFi.Mock)
	json.NewEncoder(h).Encode(c.Kiosk)       //nolint:errcheck
	json.NewEncoder(h).Encode(c.SSIDs())     //nolint:errcheck
	fmt.Fprintf(h, "%d|", c.Voucher.Minutes) // aparece en «Válido…» junto al código
	for _, p := range []string{s.logoPath(c), s.backgroundPath(c)} {
		if fi, err := os.Stat(p); err == nil {
			fmt.Fprintf(h, "%s|%d|%d|", p, fi.Size(), fi.ModTime().UnixNano())
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// handleVersion devuelve la huella actual para que el quiosco sepa si debe recargar.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"rev": s.kioskRevision(s.cfg.Get())})
}

// useWithin redacta el aviso «úsalo en menos de X» (vacío si los códigos no caducan).
func useWithin(t i18n.Texts, minutes int) string {
	if minutes <= 0 {
		return ""
	}
	return t.T("caducidad_sin_uso", "duracion", t.Duration(minutes))
}

// sameOrigin rechaza peticiones enviadas desde páginas de otro origen
// (por ejemplo, una web externa abierta en un navegador de la LAN).
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true // navegadores antiguos o curl; la allowlist sigue aplicando
	}
	return o == "http://"+r.Host || o == "https://"+r.Host
}

type voucherResponse struct {
	Code             string `json:"code"`
	Formatted        string `json:"formatted"`
	SSID             string `json:"ssid"`
	QRURL            string `json:"qr_url"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
	Mock             bool   `json:"mock"`
	ValidFor         string `json:"valid_for"`
	UseWithin        string `json:"use_within,omitempty"` // aviso de caducidad si no se usa
}

type errorResponse struct {
	Error      string `json:"error"`
	RetryAfter int    `json:"retry_after,omitempty"`
}

func (s *Server) handleVoucher(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	t := s.texts(c)
	if !sameOrigin(r) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeJSON(w, http.StatusForbidden, errorResponse{Error: t["error_general"]})
		return
	}
	var req struct {
		SSID     string `json:"ssid"`
		Accepted bool   `json:"accepted"` // casilla de las condiciones de uso
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	if err == nil && len(bytes.TrimSpace(body)) > 0 {
		err = json.Unmarshal(body, &req)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: t["error_general"]})
		return
	}
	// Las condiciones se comprueban también aquí: la casilla del navegador no basta.
	if c.Kiosk.TermsEnabled && !req.Accepted {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: t["error_condiciones"]})
		return
	}
	res, err := s.svc.Issue(r.Context(), clientIP(r).String(), req.SSID, voucher.Options{TermsVersion: c.Kiosk.TermsVersion()})
	var ce *voucher.CooldownError
	switch {
	case err == nil:
	case errors.As(err, &ce):
		w.Header().Set("Retry-After", strconv.Itoa(ce.Seconds()))
		writeJSON(w, http.StatusTooManyRequests, errorResponse{
			Error: t.T("error_espera", "s", strconv.Itoa(ce.Seconds())), RetryAfter: ce.Seconds(),
		})
		return
	case errors.Is(err, voucher.ErrDailyLimit):
		writeJSON(w, http.StatusTooManyRequests, errorResponse{Error: t["error_limite"]})
		return
	case errors.Is(err, voucher.ErrUnknownSSID):
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: t["error_general"]})
		return
	default:
		// El detalle técnico ya está en el log; al ciudadano, solo el mensaje amable.
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: t["error_general"]})
		return
	}
	writeJSON(w, http.StatusOK, voucherResponse{
		Code:             res.Code,
		Formatted:        res.Formatted,
		SSID:             res.SSID,
		QRURL:            "/api/qr.svg?ssid=" + url.QueryEscape(res.SSID),
		ExpiresInSeconds: c.Kiosk.DisplaySeconds,
		Mock:             res.Mock,
		ValidFor:         t.T("validez", "duracion", t.Duration(res.Minutes)),
		UseWithin:        useWithin(t, c.Voucher.UnusedExpireMinutes),
	})
}

func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	name := r.URL.Query().Get("ssid")
	if name == "" {
		name = c.Voucher.SSID
	}
	ssid, ok := c.FindSSID(name) // nunca un SSID arbitrario
	if !ok {
		http.NotFound(w, r)
		return
	}
	payload := WiFiPayload(ssid.SSID, ssid.Password)
	s.qrMu.Lock()
	svg, ok := s.qrCache[payload]
	s.qrMu.Unlock()
	if !ok {
		var err error
		if svg, err = QRSVG(payload); err != nil {
			s.log.Printf("ERROR generando QR: %v", err)
			http.Error(w, "Error interno", http.StatusInternalServerError)
			return
		}
		s.qrMu.Lock()
		if len(s.qrCache) > 32 {
			s.qrCache = map[string][]byte{}
		}
		s.qrCache[payload] = svg
		s.qrMu.Unlock()
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	// El QR puede llevar la contraseña de la red: que no se guarde en caché.
	w.Header().Set("Cache-Control", "no-store")
	w.Write(svg) //nolint:errcheck
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	ctx, cancel := context.WithTimeout(r.Context(), unifi.Timeout+time.Second)
	defer cancel()
	h := s.svc.Health(ctx, time.Minute)
	status := h.Status
	if status == "mock" {
		status = "ok"
	}
	code := http.StatusOK
	if status != "ok" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{
		"ok":         status == "ok",
		"mock":       c.UniFi.Mock,
		"unifi":      status,
		"daily_used": s.svc.DailyUsed(),
	})
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	// Las plantillas .html y los listados de directorio no se sirven.
	if strings.HasSuffix(r.URL.Path, "/") || path.Ext(r.URL.Path) == ".html" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	s.static.ServeHTTP(w, r)
}

// handleFavicon sirve el icono de las pestañas del navegador (PNG de 32 px).
func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/favicon-32.png")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data) //nolint:errcheck
}

// logoPath devuelve la ruta del logo del directorio de datos, o "" si no hay.
func (s *Server) logoPath(c config.Config) string {
	names := []string{"logo.svg", "logo.png"}
	if c.Kiosk.LogoPath != "" {
		names = []string{c.Kiosk.LogoPath}
	}
	return s.dataFile(names...)
}

func (s *Server) backgroundPath(c config.Config) string {
	if c.Kiosk.BackgroundImage == "" {
		return ""
	}
	return s.dataFile(c.Kiosk.BackgroundImage)
}

// dataFile devuelve el primer fichero existente: primero en la biblioteca de imágenes
// (<datos>/imagenes, lo que se sube desde /admin) y después en la carpeta de datos
// (imágenes copiadas a mano, como en versiones anteriores).
func (s *Server) dataFile(names ...string) string {
	if s.dataDir == "" {
		return ""
	}
	for _, n := range names {
		for _, dir := range []string{s.imagesPath(), s.dataDir} {
			p := filepath.Join(dir, filepath.Base(n))
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				return p
			}
		}
	}
	return ""
}

var imageTypes = map[string]string{
	".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg",
	".jpeg": "image/jpeg", ".webp": "image/webp",
}

func (s *Server) serveImage(w http.ResponseWriter, r *http.Request, p string) {
	ct, ok := imageTypes[strings.ToLower(filepath.Ext(p))]
	if p == "" || !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	// Un SVG abierto directamente no debe poder ejecutar scripts en este origen.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	// no-cache: el navegador vuelve a preguntar y, si no ha cambiado, recibe un 304 sin datos.
	// Así un logotipo o fondo nuevo se ve en cuanto el quiosco recarga.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func (s *Server) handleLogo(w http.ResponseWriter, r *http.Request) {
	s.serveImage(w, r, s.logoPath(s.cfg.Get()))
}

func (s *Server) handleBackground(w http.ResponseWriter, r *http.Request) {
	s.serveImage(w, r, s.backgroundPath(s.cfg.Get()))
}
