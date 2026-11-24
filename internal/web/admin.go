package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"kioscode/internal/config"
	"kioscode/internal/firewall"
	"kioscode/internal/i18n"
	"kioscode/internal/unifi"
	"kioscode/internal/voucher"
)

const (
	cookieName     = "totem_admin"
	sessionTTL     = 30 * time.Minute
	maxSessions    = 50
	minPasswordLen = 10

	loginMaxFails   = 5
	loginLockout    = 5 * time.Minute
	extraSSIDBlanks = 2 // filas vacías para añadir SSID adicionales
)

// bcryptCost es variable para que los tests puedan usar el coste mínimo.
var bcryptCost = 12

// ---------------------------------------------------------------------------
// Sesiones (en memoria: un reinicio obliga a volver a entrar)

type session struct {
	csrf    string
	expires time.Time
	flash   string
}

type sessionStore struct {
	mu sync.Mutex
	m  map[string]*session
}

func newSessionStore() *sessionStore { return &sessionStore{m: map[string]*session{}} }

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (st *sessionStore) create() (string, *session) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	for k, s := range st.m {
		if now.After(s.expires) {
			delete(st.m, k)
		}
	}
	if len(st.m) >= maxSessions { // nunca debería pasar; evita crecer sin límite
		st.m = map[string]*session{}
	}
	tok := randomHex(32)
	s := &session{csrf: randomHex(32), expires: now.Add(sessionTTL)}
	st.m[tok] = s
	return tok, s
}

// get devuelve la sesión y renueva su caducidad.
func (st *sessionStore) get(tok string) *session {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, ok := st.m[tok]
	if !ok {
		return nil
	}
	if time.Now().After(s.expires) {
		delete(st.m, tok)
		return nil
	}
	s.expires = time.Now().Add(sessionTTL)
	return s
}

func (st *sessionStore) delete(tok string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.m, tok)
}

// deleteOthers cierra todas las sesiones salvo keep (tras cambiar la contraseña).
func (st *sessionStore) deleteOthers(keep string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for k := range st.m {
		if k != keep {
			delete(st.m, k)
		}
	}
}

func (st *sessionStore) popFlash(s *session) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	f := s.flash
	s.flash = ""
	return f
}

func (st *sessionStore) setFlash(s *session, msg string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s.flash = msg
}

// ---------------------------------------------------------------------------
// Límite de intentos de acceso por IP

type loginLimiter struct {
	mu sync.Mutex
	m  map[string]*loginState
}

type loginState struct {
	fails int
	until time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{m: map[string]*loginState{}} }

// blocked indica cuánto falta para poder volver a intentarlo (0 = puede).
func (l *loginLimiter) blocked(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s, ok := l.m[ip]; ok && time.Now().Before(s.until) {
		return time.Until(s.until)
	}
	return 0
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s, ok := l.m[ip]
	if !ok || (!s.until.IsZero() && time.Now().After(s.until)) {
		s = &loginState{}
		l.m[ip] = s
	}
	s.fails++
	if s.fails >= loginMaxFails {
		s.until = time.Now().Add(loginLockout)
	}
}

func (l *loginLimiter) ok(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, ip)
}

// ---------------------------------------------------------------------------
// Rutas

func (s *Server) registerAdmin(mux *http.ServeMux, allow func(http.Handler) http.Handler) {
	h := func(f http.HandlerFunc) http.Handler { return allow(noStore(f)) }
	mux.Handle("GET /admin", h(s.adminGet))
	mux.Handle("POST /admin", h(s.withSession(true, s.adminSave)))
	mux.Handle("POST /admin/setup", h(s.adminSetup))
	mux.Handle("POST /admin/login", h(s.adminLogin))
	mux.Handle("POST /admin/logout", h(s.adminLogout))
	mux.Handle("POST /admin/api/check", h(s.withSession(true, s.apiCheck)))
	mux.Handle("POST /admin/api/fingerprint", h(s.withSession(true, s.apiFingerprint)))
	mux.Handle("POST /admin/api/test-voucher", h(s.withSession(true, s.apiTestVoucher)))
	mux.Handle("GET /admin/api/firewall", h(s.withSession(false, s.apiFirewall)))
	// Imágenes: el límite de tamaño se aplica antes de leer nada del cuerpo.
	mux.Handle("POST /admin/api/upload", h(limitBody(MaxFondoBytes+(256<<10), s.withSession(true, s.apiUpload))))
	mux.Handle("POST /admin/api/image-delete", h(s.withSession(true, s.apiImageDelete)))
	// Invitados conectados
	mux.Handle("GET /admin/invitados", h(s.withSession(false, s.adminGuests)))
	mux.Handle("POST /admin/api/guests/disconnect", h(s.withSession(true, s.apiDisconnect)))
	mux.Handle("POST /admin/api/guests/disconnect-all", h(s.withSession(true, s.apiDisconnectAll)))
	mux.Handle("GET /admin/imagenes/{nombre}", h(s.withSession(false, s.adminImage)))
	mux.Handle("GET /admin/", allow(http.RedirectHandler("/admin", http.StatusSeeOther)))
}

func noStore(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

type ctxKey struct{}

type sessionCtx struct {
	token string
	sess  *session
}

func (s *Server) currentSession(r *http.Request) (string, *session) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", nil
	}
	sess := s.sessions.get(c.Value)
	if sess == nil {
		return "", nil
	}
	return c.Value, sess
}

// withSession exige sesión iniciada y, en peticiones POST, el token CSRF
// (campo «csrf» del formulario o cabecera X-CSRF-Token) y el mismo origen.
func (s *Server) withSession(csrf bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, sess := s.currentSession(r)
		isAPI := strings.HasPrefix(r.URL.Path, "/admin/api/")
		if sess == nil {
			if isAPI {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "message": "La sesión ha caducado. Vuelva a entrar."})
				return
			}
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
		if csrf {
			got := r.Header.Get("X-CSRF-Token")
			if got == "" {
				got = r.FormValue("csrf")
			}
			if !sameOrigin(r) || subtle.ConstantTimeCompare([]byte(got), []byte(sess.csrf)) != 1 {
				s.log.Printf("admin: petición rechazada por token CSRF u origen no válidos desde %s", clientIP(r))
				http.Error(w, "Petición no válida. Recargue la página.", http.StatusForbidden)
				return
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sessionCtx{tok, sess})))
	}
}

func sessionFrom(r *http.Request) sessionCtx {
	v, _ := r.Context().Value(ctxKey{}).(sessionCtx)
	return v
}

func (s *Server) startSession(w http.ResponseWriter) {
	tok, _ := s.sessions.create()
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    tok,
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// ---------------------------------------------------------------------------
// Vistas

type adminData struct {
	View     string // "setup", "login" o "panel"
	Page     string // plantilla: "admin.html" (por defecto) o "invitados.html"
	Active   string // pestaña activa: "config" o "invitados"
	Title    string
	Version  string
	CSRF     string
	Flash    string
	Error    string
	HasLogo  bool
	LogoText string

	Cfg        config.Config
	Extras     []config.SSID
	APIKeySet  bool
	Languages  []string
	Health     voucher.Health
	DailyUsed  int
	ConfigPath string
	DataDir    string
	Warnings   []string
	// Textos de la pantalla
	MaxTitleLen, MaxIntroLen, MaxTermsLen int
	TermsVersion                          string
	Themes                                []themeOption // selector de tema del quiosco

	// Biblioteca de imágenes
	Images                  []imageInfo
	LegacyLogo, LegacyFondo string // imagen elegida que no está en la biblioteca (copiada a mano)
	MaxLogoMB, MaxFondoMB   int64

	// Invitados conectados
	Guests       []voucher.GuestRow
	GuestsError  string
	ShowPending  bool
	PendingCount int

	ConfirmAccess bool         // mostrar el campo de confirmación con contraseña
	Firewall      firewallView // estado de ufw (solo Linux con el ayudante)
}

// themeOption es una opción del selector de tema (con su miniatura en /static/).
type themeOption struct{ ID, Name string }

// firewallView resume firewall-status.json para el panel.
type firewallView struct {
	Managed bool   `json:"managed"`
	State   string `json:"state"` // ok, pendiente, inactivo, error
	Text    string `json:"text"`
}

func (s *Server) firewallStatusPath() string {
	return filepath.Join(s.dataDir, firewall.StatusFile)
}

// firewallState compara el último resultado del ayudante con la fecha de config.json.
func (s *Server) firewallState() firewallView {
	st, err := firewall.ReadStatus(s.firewallStatusPath())
	if errors.Is(err, firewall.ErrNotManaged) {
		return firewallView{}
	}
	if err != nil {
		return firewallView{Managed: true, State: "error", Text: "No se puede leer el estado del cortafuegos: " + err.Error()}
	}
	if fi, err := os.Stat(s.cfg.Path()); err == nil && fi.ModTime().After(st.ConfigModTime.Add(time.Second)) {
		return firewallView{Managed: true, State: "pendiente", Text: "Aplicando los cambios…"}
	}
	var srcs []string
	for _, r := range st.Rules {
		srcs = append(srcs, r.Src)
	}
	lista := "solo este equipo"
	if len(srcs) > 0 {
		lista = strings.Join(srcs, ", ")
	}
	switch {
	case !st.OK:
		return firewallView{Managed: true, State: "error", Text: "Error al actualizar ufw (" + st.Time.Local().Format("02/01 15:04") + "): " + st.Error}
	case !st.UFWActive:
		return firewallView{Managed: true, State: "inactivo", Text: "ufw está desactivado: las reglas están preparadas (" + lista + ") pero no tienen efecto hasta activarlo."}
	default:
		port := ""
		if len(st.Rules) > 0 {
			port = fmt.Sprintf(" al puerto %d", st.Rules[0].Port)
		}
		return firewallView{Managed: true, State: "ok", Text: "Actualizado " + st.Time.Local().Format("02/01 15:04") + ": acceso" + port + " para " + lista + "."}
	}
}

func (s *Server) apiFirewall(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.firewallState())
}

func (s *Server) render(w http.ResponseWriter, status int, d adminData) {
	c := s.cfg.Get()
	d.Title = c.Kiosk.Title
	d.MaxTitleLen, d.MaxIntroLen, d.MaxTermsLen = config.MaxTitleLen, config.MaxIntroLen, config.MaxTermsLen
	for _, t := range config.Themes {
		d.Themes = append(d.Themes, themeOption{ID: t.ID, Name: t.Name})
	}
	d.TermsVersion = c.Kiosk.TermsVersion()
	d.Version = s.version
	d.HasLogo = s.logoPath(c) != ""
	d.LogoText = c.Kiosk.Organization
	var buf bytes.Buffer
	page := d.Page
	if page == "" {
		page = "admin.html"
	}
	if err := s.tmpl.ExecuteTemplate(&buf, page, d); err != nil {
		s.log.Printf("ERROR plantilla %s: %v", page, err)
		http.Error(w, "Error interno", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes()) //nolint:errcheck
}

func (s *Server) panelData(sess *session, cfg config.Config) adminData {
	d := adminData{
		View:       "panel",
		Active:     "config",
		CSRF:       sess.csrf,
		Cfg:        cfg,
		APIKeySet:  s.cfg.Get().UniFi.APIKey != "",
		Languages:  i18n.Available(s.dataDir),
		Health:     s.svc.LastHealth(),
		DailyUsed:  s.svc.DailyUsed(),
		ConfigPath: s.cfg.Path(),
		DataDir:    s.dataDir,
	}
	d.Firewall = s.firewallState()
	d.Images = s.listImages(cfg)
	d.MaxLogoMB, d.MaxFondoMB = MaxLogoBytes>>20, MaxFondoBytes>>20
	if n := cfg.Kiosk.LogoPath; n != "" {
		if _, ok := s.findImage(cfg, n); !ok {
			d.LegacyLogo = n
		}
	}
	if n := cfg.Kiosk.BackgroundImage; n != "" {
		if _, ok := s.findImage(cfg, n); !ok {
			d.LegacyFondo = n
		}
	}
	d.Extras = append(append([]config.SSID{}, cfg.Voucher.ExtraSSIDs...), make([]config.SSID, extraSSIDBlanks)...)
	saved := s.cfg.Get()
	if cfg.UniFi.Mock {
		d.Health = voucher.Health{Status: "mock"}
	}
	if !saved.UniFi.Mock {
		if saved.UniFi.CertSHA256 == "" {
			d.Warnings = append(d.Warnings, "Modo real sin huella del certificado: el quiosco no puede dar códigos. Pulse «Obtener huella del certificado» y guarde.")
		}
		if saved.UniFi.APIKey == "" {
			d.Warnings = append(d.Warnings, "Modo real sin API key: el quiosco no puede dar códigos.")
		}
	}
	return d
}

func (s *Server) adminGet(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	if c.AdminPasswordHash == "" {
		s.render(w, http.StatusOK, adminData{View: "setup"})
		return
	}
	_, sess := s.currentSession(r)
	if sess == nil {
		s.render(w, http.StatusOK, adminData{View: "login"})
		return
	}
	d := s.panelData(sess, c)
	d.Flash = s.sessions.popFlash(sess)
	s.render(w, http.StatusOK, d)
}

func (s *Server) adminSetup(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		s.log.Printf("admin: alta de contraseña rechazada por origen %q desde %s", r.Header.Get("Origin"), clientIP(r))
		http.Error(w, "Petición no válida.", http.StatusForbidden)
		return
	}
	if s.cfg.Get().AdminPasswordHash != "" {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	p1, p2 := r.FormValue("password"), r.FormValue("password2")
	if msg := checkNewPassword(p1, p2); msg != "" {
		s.render(w, http.StatusBadRequest, adminData{View: "setup", Error: msg})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(p1), bcryptCost)
	if err != nil {
		http.Error(w, "Error interno", http.StatusInternalServerError)
		return
	}
	err = s.cfg.Update(func(c *config.Config) error {
		if c.AdminPasswordHash != "" {
			return errors.New("la contraseña ya se había creado")
		}
		c.AdminPasswordHash = string(hash)
		return nil
	})
	if err != nil {
		s.render(w, http.StatusConflict, adminData{View: "setup", Error: "No se ha podido guardar: " + err.Error()})
		return
	}
	s.log.Printf("admin: contraseña de administración creada desde %s", clientIP(r))
	s.startSession(w)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func checkNewPassword(p1, p2 string) string {
	switch {
	case len([]rune(p1)) < minPasswordLen:
		return fmt.Sprintf("La contraseña debe tener al menos %d caracteres.", minPasswordLen)
	case len(p1) > 72:
		return "La contraseña no puede superar los 72 caracteres."
	case p1 != p2:
		return "Las dos contraseñas no coinciden."
	}
	return ""
}

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r).String()
	if !sameOrigin(r) {
		s.log.Printf("admin: inicio de sesión rechazado por origen %q desde %s", r.Header.Get("Origin"), ip)
		http.Error(w, "Petición no válida.", http.StatusForbidden)
		return
	}
	if d := s.logins.blocked(ip); d > 0 {
		s.render(w, http.StatusTooManyRequests, adminData{View: "login",
			Error: fmt.Sprintf("Demasiados intentos fallidos. Espere %d minutos.", int(d.Minutes())+1)})
		return
	}
	hash := s.cfg.Get().AdminPasswordHash
	if hash == "" {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(r.FormValue("password"))) != nil {
		s.logins.fail(ip)
		s.log.Printf("admin: contraseña incorrecta desde %s", ip)
		s.render(w, http.StatusUnauthorized, adminData{View: "login", Error: "Contraseña incorrecta."})
		return
	}
	s.logins.ok(ip)
	s.log.Printf("admin: sesión iniciada desde %s", ip)
	s.startSession(w)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	if tok, _ := s.currentSession(r); tok != "" && sameOrigin(r) {
		s.sessions.delete(tok)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/admin", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Guardar la configuración

func lines(s string) []string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' || r == ';' || r == ' ' || r == '\t' })
	if f == nil {
		return []string{}
	}
	return f
}

// formToConfig aplica el formulario sobre base. Devuelve los errores de formato.
func formToConfig(r *http.Request, base config.Config) (config.Config, []string) {
	c := base.Clone()
	var errs []string
	num := func(field, label string, dst *int) {
		v := strings.TrimSpace(r.FormValue(field))
		if v == "" {
			*dst = 0
			return
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, label+": debe ser un número entero")
			return
		}
		*dst = n
	}
	c.Listen = strings.TrimSpace(r.FormValue("listen"))
	c.AllowedClients = lines(r.FormValue("allowed_clients"))
	c.AdminAllowedClients = lines(r.FormValue("admin_allowed_clients"))

	newURL := strings.TrimRight(strings.TrimSpace(r.FormValue("unifi_base_url")), "/")
	c.UniFi.SiteID = strings.TrimSpace(r.FormValue("unifi_site_id"))
	if newURL != base.UniFi.BaseURL && c.UniFi.SiteID == base.UniFi.SiteID {
		c.UniFi.SiteID = "" // otro servidor: el sitio se vuelve a detectar
	}
	c.UniFi.BaseURL = newURL
	if k := strings.TrimSpace(r.FormValue("unifi_api_key")); k != "" {
		c.UniFi.APIKey = k
	}
	if r.FormValue("unifi_api_key_clear") == "1" {
		c.UniFi.APIKey = ""
	}
	c.UniFi.CertSHA256 = r.FormValue("unifi_cert_sha256")
	if newURL != base.UniFi.BaseURL && config.NormalizeFingerprint(c.UniFi.CertSHA256) == base.UniFi.CertSHA256 {
		c.UniFi.CertSHA256 = "" // la huella del servidor anterior no vale para el nuevo
	}
	c.UniFi.Mock = r.FormValue("unifi_mock") == "1"

	c.Voucher.SSID = r.FormValue("voucher_ssid")
	c.Voucher.SSIDPassword = r.FormValue("voucher_ssid_password")
	c.Voucher.ExtraSSIDs = []config.SSID{}
	for i := 0; i < 20; i++ {
		name := r.FormValue("extra_ssid_" + strconv.Itoa(i))
		if strings.TrimSpace(name) == "" {
			continue
		}
		c.Voucher.ExtraSSIDs = append(c.Voucher.ExtraSSIDs, config.SSID{SSID: name, Password: r.FormValue("extra_password_" + strconv.Itoa(i))})
	}
	c.Voucher.Note = r.FormValue("voucher_note")
	num("voucher_minutes", "Duración", &c.Voucher.Minutes)
	num("voucher_guest_limit", "Dispositivos por código", &c.Voucher.GuestLimit)
	num("voucher_data_mb", "Límite de datos", &c.Voucher.DataMB)
	num("voucher_rx_kbps", "Velocidad de bajada", &c.Voucher.RxKbps)
	num("voucher_tx_kbps", "Velocidad de subida", &c.Voucher.TxKbps)
	num("voucher_unused_expire_minutes", "Caducidad de los códigos sin usar", &c.Voucher.UnusedExpireMinutes)

	c.Kiosk.Title = r.FormValue("kiosk_title")
	c.Kiosk.Intro = r.FormValue("kiosk_intro")
	if v := r.FormValue("kiosk_theme"); v != "" { // sin el campo, se conserva el tema actual
		c.Kiosk.Theme = v
	}
	c.Kiosk.TermsEnabled = r.FormValue("kiosk_terms_enabled") == "1"
	c.Kiosk.TermsText = r.FormValue("kiosk_terms_text")
	c.Kiosk.Organization = strings.TrimSpace(r.FormValue("kiosk_organization"))
	num("kiosk_display_seconds", "Tiempo en pantalla", &c.Kiosk.DisplaySeconds)
	num("kiosk_cooldown_seconds", "Espera entre códigos", &c.Kiosk.CooldownSeconds)
	num("kiosk_daily_limit", "Límite diario", &c.Kiosk.DailyLimit)
	c.Kiosk.Language = r.FormValue("kiosk_language")
	c.Kiosk.LogoPath = strings.TrimSpace(r.FormValue("kiosk_logo_path"))
	c.Kiosk.BackgroundImage = strings.TrimSpace(r.FormValue("kiosk_background_image"))
	return c, errs
}

func (s *Server) adminSave(w http.ResponseWriter, r *http.Request) {
	sc := sessionFrom(r)
	// 512 KB: las condiciones de uso (hasta 20 000 caracteres) ocupan bastante más al
	// codificarse en el formulario (cada letra con tilde son 6 bytes).
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	before := s.cfg.Get()
	next, errs := formToConfig(r, before)
	errs = append(errs, s.checkImageChoice(next)...)

	// Cambio de contraseña (opcional): exige la actual.
	var newHash string
	if p1 := r.FormValue("new_password"); p1 != "" {
		if bcrypt.CompareHashAndPassword([]byte(before.AdminPasswordHash), []byte(r.FormValue("current_password"))) != nil {
			errs = append(errs, "Contraseña actual incorrecta: no se ha cambiado la contraseña")
		} else if msg := checkNewPassword(p1, r.FormValue("new_password2")); msg != "" {
			errs = append(errs, msg)
		} else if h, err := bcrypt.GenerateFromPassword([]byte(p1), bcryptCost); err == nil {
			newHash = string(h)
		}
	}

	// Cambiar quién puede conectarse (y, por tanto, el cortafuegos) exige
	// volver a escribir la contraseña de administración.
	accessChanged := !equalStrings(next.AllowedClients, before.AllowedClients) ||
		!equalStrings(next.AdminAllowedClients, before.AdminAllowedClients) || next.Listen != before.Listen
	confirmNeeded := false
	if accessChanged {
		ip := clientIP(r)
		if d := s.logins.blocked(ip.String()); d > 0 {
			errs = append(errs, fmt.Sprintf("Demasiados intentos fallidos. Espere %d minutos.", int(d.Minutes())+1))
			confirmNeeded = true
		} else if bcrypt.CompareHashAndPassword([]byte(before.AdminPasswordHash), []byte(r.FormValue("confirm_password"))) != nil {
			s.logins.fail(ip.String())
			errs = append(errs, "Para cambiar los equipos permitidos o el puerto, escriba su contraseña de administración en «Confirmar con la contraseña de administración»")
			confirmNeeded = true
		}
		if al, err := config.ParseAllowlist(next.AdminAllowedClients); err == nil && !al.Allows(ip) {
			errs = append(errs, fmt.Sprintf("Este equipo (%s) no estaría en «Equipos que pueden abrir /admin» y se quedaría sin acceso: añádalo a la lista", ip))
		}
	}

	if len(errs) == 0 {
		err := s.cfg.Update(func(c *config.Config) error {
			hash := c.AdminPasswordHash
			*c = next
			c.AdminPasswordHash = hash
			if newHash != "" {
				c.AdminPasswordHash = newHash
			}
			return nil
		})
		if err != nil {
			errs = append(errs, strings.TrimPrefix(err.Error(), "configuración no válida:\n  - "))
		}
	}
	if len(errs) > 0 {
		d := s.panelData(sc.sess, next)
		d.ConfirmAccess = confirmNeeded
		d.Error = "No se ha guardado. " + strings.Join(errs, " · ")
		d.Error = strings.ReplaceAll(d.Error, "\n  - ", " · ")
		s.render(w, http.StatusBadRequest, d)
		return
	}

	after := s.cfg.Get()
	s.log.Printf("admin: configuración guardada desde %s%s", clientIP(r), describeChanges(before, after))
	msg := "Configuración guardada."
	if after.Listen != before.Listen {
		msg += " El cambio de dirección/puerto de escucha se aplicará al reiniciar el servicio."
	}
	if accessChanged {
		s.log.Printf("admin: equipos permitidos cambiados desde %s: quiosco=%v admin=%v listen=%s",
			clientIP(r), after.AllowedClients, after.AdminAllowedClients, after.Listen)
		if _, err := firewall.ReadStatus(s.firewallStatusPath()); err == nil {
			msg += " El cortafuegos (ufw) se actualizará en unos segundos."
		} else {
			msg += " Ha cambiado la lista de equipos permitidos: si usa el cortafuegos del sistema, vuelva a ejecutar el instalador para actualizar la regla."
		}
	}
	if after.UniFi.BaseURL != before.UniFi.BaseURL {
		if after.UniFi.CertSHA256 == "" && !after.UniFi.Mock {
			msg += " Ha cambiado el servidor de UniFi: pulse «Obtener huella del certificado» y guarde."
		}
		if after.UniFi.APIKey == before.UniFi.APIKey && after.UniFi.APIKey != "" {
			msg += " Recuerde que cada servidor de UniFi tiene sus propias API keys: pegue la del nuevo servidor."
		}
	}
	if newHash != "" {
		s.sessions.deleteOthers(sc.token)
		msg += " Contraseña cambiada; se han cerrado las demás sesiones."
	}
	s.sessions.setFlash(sc.sess, msg)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// describeChanges resume para el log qué se ha cambiado, sin valores secretos.
func describeChanges(a, b config.Config) string {
	var ch []string
	if a.UniFi.Mock != b.UniFi.Mock {
		ch = append(ch, fmt.Sprintf("mock=%v", b.UniFi.Mock))
	}
	if a.UniFi.BaseURL != b.UniFi.BaseURL {
		ch = append(ch, "base_url="+b.UniFi.BaseURL)
	}
	if a.UniFi.APIKey != b.UniFi.APIKey {
		ch = append(ch, "api_key=(cambiada)")
	}
	if a.UniFi.CertSHA256 != b.UniFi.CertSHA256 {
		ch = append(ch, "cert_sha256="+b.UniFi.CertSHA256)
	}
	if a.AdminPasswordHash != b.AdminPasswordHash {
		ch = append(ch, "contraseña=(cambiada)")
	}
	if len(ch) == 0 {
		return ""
	}
	return " (" + strings.Join(ch, ", ") + ")"
}

// ---------------------------------------------------------------------------
// Acciones (JSON)

type apiResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Layer   string `json:"layer,omitempty"`

	// Huella
	SHA256    string `json:"sha256,omitempty"`
	Formatted string `json:"formatted,omitempty"`
	Matches   bool   `json:"matches,omitempty"`

	// Voucher de prueba
	Code string `json:"code,omitempty"`
}

func (s *Server) apiCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*unifi.Timeout+2*time.Second)
	defer cancel()
	res, err := s.svc.Check(ctx)
	if err != nil {
		layer, hint := unifi.Explain(err)
		s.log.Printf("admin: prueba de conexión con UniFi fallida (%s): %v", layer, err)
		writeJSON(w, http.StatusOK, apiResult{OK: false, Layer: layer, Message: hint + "\n\nDetalle: " + err.Error()})
		return
	}
	if res.Mock {
		writeJSON(w, http.StatusOK, apiResult{OK: true, Message: "Modo simulación activo: no se contacta con UniFi. Desactive «Modo simulación» y guarde para probar el servidor real."})
		return
	}
	writeJSON(w, http.StatusOK, apiResult{OK: true, Message: fmt.Sprintf(
		"Conexión correcta: certificado anclado, API key aceptada y sitio «%s» (%s). Sitios visibles con esta clave: %d.",
		res.SiteName, res.SiteID, res.Sites)})
}

func (s *Server) apiFingerprint(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimSpace(r.FormValue("base_url"))
	if base == "" {
		base = s.cfg.Get().UniFi.BaseURL
	}
	hp, err := unifi.HostPort(base)
	if err != nil || !strings.HasPrefix(base, "https://") {
		writeJSON(w, http.StatusOK, apiResult{OK: false, Message: "La URL de UniFi debe tener la forma https://IP:PUERTO."})
		return
	}
	info, err := s.FetchCert(r.Context(), hp)
	if err != nil {
		writeJSON(w, http.StatusOK, apiResult{OK: false, Layer: "Conexión TCP", Message: err.Error()})
		return
	}
	cur := s.cfg.Get().UniFi.CertSHA256
	msg := fmt.Sprintf("Certificado de %s\nHuella SHA-256: %s\nSujeto: %s\nEmisor: %s\nVálido: %s → %s",
		hp, info.Formatted, info.Subject, info.Issuer,
		info.NotBefore.Format("02/01/2006"), info.NotAfter.Format("02/01/2006"))
	if info.SelfSigned {
		msg += " (autofirmado)"
	}
	if cur == info.SHA256 {
		msg += "\n\nCoincide con la huella guardada."
	} else {
		msg += "\n\nSe ha copiado en el campo «Huella del certificado». Si quiere asegurarse de que es el servidor correcto, compárela con la que muestra el navegador del propio servidor de UniFi; después pulse «Guardar»."
	}
	s.log.Printf("admin: leída la huella de %s: %s", hp, info.SHA256)
	writeJSON(w, http.StatusOK, apiResult{OK: true, Message: msg, SHA256: info.SHA256, Formatted: info.Formatted, Matches: cur == info.SHA256})
}

func (s *Server) apiTestVoucher(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	ctx, cancel := context.WithTimeout(r.Context(), 2*unifi.Timeout+2*time.Second)
	defer cancel()
	res, err := s.svc.Issue(ctx, clientIP(r).String(), "", voucher.Options{SkipCooldown: true, Note: c.Voucher.Note + " (prueba admin)"})
	if err != nil {
		layer, hint := unifi.Explain(errors.Unwrap(err))
		if errors.Is(err, voucher.ErrDailyLimit) {
			layer, hint = "Límite diario", "Se ha alcanzado el límite diario de vouchers."
		}
		writeJSON(w, http.StatusOK, apiResult{OK: false, Layer: layer, Message: hint + "\n\nDetalle: " + err.Error()})
		return
	}
	msg := fmt.Sprintf("Voucher creado: %s (SSID %s, %d minutos).", res.Formatted, res.SSID, res.Minutes)
	if res.Mock {
		msg += " Es un código SIMULADO: no sirve en el portal."
	} else {
		msg += " Es un código REAL: aparece en UniFi como «" + c.Voucher.Note + " (prueba admin)» y cuenta en el límite diario."
	}
	writeJSON(w, http.StatusOK, apiResult{OK: true, Message: msg, Code: res.Formatted})
}
