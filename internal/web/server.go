// Paquete web: servidor HTTP del quiosco y de la administración.
//
// Solo este servidor habla con UniFi; el navegador del tótem nunca ve la API key.
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
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
		"join": strings.Join,
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
	mux.Handle("GET /static/", either(http.HandlerFunc(s.handleStatic)))
	mux.Handle("GET /logo", either(http.HandlerFunc(s.handleLogo)))
	mux.Handle("GET /background", either(http.HandlerFunc(s.handleBackground)))
	mux.Handle("GET /favicon.ico", http.NotFoundHandler())
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
	Lang          string
	Title         string
	Mock          bool
	SSIDs         []string
	MainSSID      string
	HasLogo       bool
	HasBackground bool
	T             i18n.Texts
	Validity      string
	ClientConfig  string // JSON para app.js, en un atributo data-
	Version       string
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	t := s.texts(c)
	var ssids []string
	for _, x := range c.SSIDs() {
		ssids = append(ssids, x.SSID)
	}
	clientCfg, _ := json.Marshal(map[string]any{
		"display_seconds": c.Kiosk.DisplaySeconds,
		"textos": map[string]string{
			"cuenta_atras":  t["cuenta_atras"],
			"error_general": t["error_general"],
			"paso1":         t["paso1"],
		},
	})
	data := indexData{
		Lang:          c.Kiosk.Language,
		Title:         c.Kiosk.Title,
		Organization:  c.Kiosk.Organization,
		Mock:          c.UniFi.Mock,
		SSIDs:         ssids,
		MainSSID:      c.Voucher.SSID,
		HasLogo:       s.logoPath(c) != "",
		HasBackground: s.backgroundPath(c) != "",
		T:             t,
		Validity:      t.T("validez", "duracion", t.Duration(c.Voucher.Minutes)),
		ClientConfig:  string(clientCfg),
		Version:       s.version,
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
		SSID string `json:"ssid"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	if err == nil && len(bytes.TrimSpace(body)) > 0 {
		err = json.Unmarshal(body, &req)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: t["error_general"]})
		return
	}
	res, err := s.svc.Issue(r.Context(), clientIP(r).String(), req.SSID, voucher.Options{})
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

// dataFile devuelve el primer fichero existente del directorio de datos.
func (s *Server) dataFile(names ...string) string {
	if s.dataDir == "" {
		return ""
	}
	for _, n := range names {
		p := filepath.Join(s.dataDir, filepath.Base(n))
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p
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
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func (s *Server) handleLogo(w http.ResponseWriter, r *http.Request) {
	s.serveImage(w, r, s.logoPath(s.cfg.Get()))
}

func (s *Server) handleBackground(w http.ResponseWriter, r *http.Request) {
	s.serveImage(w, r, s.backgroundPath(s.cfg.Get()))
}
