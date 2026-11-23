// Paquete config: carga, validación y guardado de config.json.
//
// El fichero se crea con valores por defecto la primera vez que se arranca.
// Todo acceso concurrente pasa por Store, que entrega copias y guarda de forma
// atómica (fichero temporal + renombrado) para no dejar nunca un JSON a medias.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Config es el contenido completo de config.json.
type Config struct {
	Listen              string   `json:"listen"`
	AllowedClients      []string `json:"allowed_clients"`
	AdminPasswordHash   string   `json:"admin_password_hash"`
	AdminAllowedClients []string `json:"admin_allowed_clients"`
	UniFi               UniFi    `json:"unifi"`
	Voucher             Voucher  `json:"voucher"`
	Kiosk               Kiosk    `json:"kiosk"`
}

// UniFi agrupa la conexión con UniFi OS Server.
type UniFi struct {
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key"`
	SiteID     string `json:"site_id"`
	CertSHA256 string `json:"cert_sha256"`
	Mock       bool   `json:"mock"`
}

// SSID adicional que puede elegir el ciudadano en pantalla.
type SSID struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
}

// Voucher son los parámetros con los que se crea cada voucher.
type Voucher struct {
	SSID         string `json:"ssid"`
	SSIDPassword string `json:"ssid_password"`
	// ExtraSSIDs: SSID adicionales. Si hay alguno, la web muestra un selector.
	ExtraSSIDs []SSID `json:"extra_ssids"`
	Note       string `json:"note"`
	Minutes    int    `json:"minutes"`
	GuestLimit int    `json:"guest_limit"`
	DataMB     int    `json:"data_mb"`
	RxKbps     int    `json:"rx_kbps"`
	TxKbps     int    `json:"tx_kbps"`
	// Minutos tras los que caduca (se borra en UniFi) un voucher del tótem que nadie ha
	// usado: los vouchers sin usar no caducan solos. 0 = no caducan.
	UnusedExpireMinutes int `json:"unused_expire_minutes"`
	// Obsoleto (primera v1.3, en horas). Se acepta al leer y se convierte a
	// unused_expire_minutes; no se vuelve a escribir.
	LegacyCleanupHours int `json:"unused_cleanup_hours,omitempty"`
}

// Kiosk agrupa lo que afecta a la pantalla del tótem.
type Kiosk struct {
	Title        string `json:"title"`
	Organization string `json:"organization"`
	// Intro: frase opcional bajo el título de la pantalla de inicio (vacío = no se muestra).
	Intro string `json:"intro"`
	// Theme: aspecto del quiosco (ver Themes). Vacío o desconocido = DefaultTheme.
	Theme string `json:"theme"`
	// Obsoleto (hasta la v1.4): nombre en la cabecera. Se acepta al leer para que las
	// configuraciones anteriores sigan cargando, pero no se usa ni se vuelve a escribir.
	LegacyHeaderText string `json:"header_text,omitempty"`
	// Condiciones de uso: si están activadas, el ciudadano debe aceptarlas antes de obtener el código.
	TermsEnabled    bool   `json:"terms_enabled"`
	TermsText       string `json:"terms_text"`
	DisplaySeconds  int    `json:"display_seconds"`
	CooldownSeconds int    `json:"cooldown_seconds"`
	DailyLimit      int    `json:"daily_limit"`
	Language        string `json:"language"`
	LogoPath        string `json:"logo_path"`
	BackgroundImage string `json:"background_image"`
}

// Default devuelve la configuración inicial descrita en CONTEXTO.md §6.
func Default() Config {
	return Config{
		Listen:              "0.0.0.0:8080",
		AllowedClients:      []string{"127.0.0.1"},
		AdminPasswordHash:   "",
		AdminAllowedClients: []string{"127.0.0.1"},
		UniFi: UniFi{
			BaseURL: "https://192.0.2.10:11443",
			Mock:    true,
		},
		Voucher: Voucher{
			SSID:                "WifiPublica",
			ExtraSSIDs:          []SSID{},
			Note:                "Totem",
			Minutes:             240,
			GuestLimit:          1,
			UnusedExpireMinutes: 15,
		},
		Kiosk: Kiosk{
			Title:           "Wi-Fi gratuita",
			Theme:           DefaultTheme,
			DisplaySeconds:  60,
			CooldownSeconds: 20,
			DailyLimit:      300,
			Language:        "es",
		},
	}
}

// Temas del quiosco que se pueden elegir en /admin. La clave es el valor de kiosk.theme y
// la clase CSS del <body> (tema-<clave>); el valor, el nombre que se ve en /admin.
var Themes = []struct{ ID, Name string }{
	{"claro", "Claro"},
	{"banda", "Banda azul"},
}

// DefaultTheme es el tema de una instalación nueva o de una configuración sin tema válido.
const DefaultTheme = "claro"

// ValidTheme indica si id es uno de los temas disponibles.
func ValidTheme(id string) bool {
	for _, t := range Themes {
		if t.ID == id {
			return true
		}
	}
	return false
}

// Límites de longitud de los textos editables (en caracteres).
const (
	MaxDataMB = 1048576 // máximo de dataUsageLimitMBytes en la Integration API
	// MaxUnusedExpireMinutes: caducidad máxima de los códigos sin usar (7 días).
	MaxUnusedExpireMinutes = 7 * 24 * 60
	MaxTitleLen            = 60
	MaxIntroLen            = 120 // una frase: la pantalla de inicio debe tener poco texto
	MaxTermsLen            = 20000
	termsVerSize           = 8
)

// TermsVersion identifica el texto de las condiciones (8 cifras hexadecimales de su
// SHA-256). Cambia en cuanto se edita el texto y se anota en el log de cada código.
func (k Kiosk) TermsVersion() string {
	if !k.TermsEnabled {
		return ""
	}
	sum := sha256.Sum256([]byte(k.TermsText))
	return hex.EncodeToString(sum[:])[:termsVerSize]
}

// cleanText normaliza saltos de línea y espacios de un texto multilínea.
func cleanText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// SSIDs devuelve la lista de redes que se ofrecen, la principal primero y sin duplicados.
func (c Config) SSIDs() []SSID {
	out := []SSID{{SSID: c.Voucher.SSID, Password: c.Voucher.SSIDPassword}}
	seen := map[string]bool{c.Voucher.SSID: true}
	for _, s := range c.Voucher.ExtraSSIDs {
		if s.SSID == "" || seen[s.SSID] {
			continue
		}
		seen[s.SSID] = true
		out = append(out, s)
	}
	return out
}

// FindSSID busca un SSID configurado por su nombre exacto.
func (c Config) FindSSID(name string) (SSID, bool) {
	for _, s := range c.SSIDs() {
		if s.SSID == name {
			return s, true
		}
	}
	return SSID{}, false
}

// Clone devuelve una copia profunda (las listas no se comparten).
func (c Config) Clone() Config {
	c.AllowedClients = append([]string(nil), c.AllowedClients...)
	c.AdminAllowedClients = append([]string(nil), c.AdminAllowedClients...)
	c.Voucher.ExtraSSIDs = append([]SSID(nil), c.Voucher.ExtraSSIDs...)
	return c
}

// NormalizeFingerprint pasa una huella a 64 caracteres hexadecimales en minúscula,
// aceptando el formato con dos puntos que muestran navegadores y openssl.
func NormalizeFingerprint(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(":", "", " ", "", "-", "").Replace(s)
	return s
}

// Validate comprueba la configuración y devuelve todos los problemas juntos.
// También normaliza algunos campos (huella, espacios, URL sin barra final).
func (c *Config) Validate() error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if _, port, err := net.SplitHostPort(c.Listen); err != nil {
		add("listen: debe tener la forma IP:PUERTO (por ejemplo 0.0.0.0:8080)")
	} else if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		add("listen: puerto no válido")
	}

	c.AllowedClients = trimList(c.AllowedClients)
	if _, err := ParseAllowlist(c.AllowedClients); err != nil {
		add("allowed_clients: %v", err)
	}
	c.AdminAllowedClients = trimList(c.AdminAllowedClients)
	if _, err := ParseAllowlist(c.AdminAllowedClients); err != nil {
		add("admin_allowed_clients: %v", err)
	}

	c.UniFi.BaseURL = strings.TrimRight(strings.TrimSpace(c.UniFi.BaseURL), "/")
	if u, err := url.Parse(c.UniFi.BaseURL); err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") {
		add("unifi.base_url: debe ser https://IP:PUERTO, sin ruta (por ejemplo https://198.51.100.20:11443)")
	}
	c.UniFi.APIKey = strings.TrimSpace(c.UniFi.APIKey)
	c.UniFi.SiteID = strings.TrimSpace(c.UniFi.SiteID)
	c.UniFi.CertSHA256 = NormalizeFingerprint(c.UniFi.CertSHA256)
	if c.UniFi.CertSHA256 != "" {
		if b, err := hex.DecodeString(c.UniFi.CertSHA256); err != nil || len(b) != 32 {
			add("unifi.cert_sha256: debe ser una huella SHA-256 (64 caracteres hexadecimales)")
		}
	}

	c.Voucher.SSID = strings.TrimSpace(c.Voucher.SSID)
	if c.Voucher.SSID == "" {
		add("voucher.ssid: no puede estar vacío")
	}
	if len([]byte(c.Voucher.SSID)) > 32 {
		add("voucher.ssid: un SSID tiene como máximo 32 bytes")
	}
	extras := c.Voucher.ExtraSSIDs[:0:0]
	for _, s := range c.Voucher.ExtraSSIDs {
		s.SSID = strings.TrimSpace(s.SSID)
		if s.SSID == "" {
			continue
		}
		if len([]byte(s.SSID)) > 32 {
			add("voucher.extra_ssids: %q supera los 32 bytes", s.SSID)
		}
		extras = append(extras, s)
	}
	c.Voucher.ExtraSSIDs = extras
	c.Voucher.Note = strings.TrimSpace(c.Voucher.Note)
	if c.Voucher.Note == "" {
		add("voucher.note: es obligatorio (UniFi lo exige como nombre del voucher)")
	}
	if c.Voucher.Minutes < 1 || c.Voucher.Minutes > 1000000 {
		add("voucher.minutes: debe estar entre 1 y 1000000")
	}
	if c.Voucher.GuestLimit < 0 || c.Voucher.GuestLimit > 1000 {
		add("voucher.guest_limit: debe estar entre 0 (sin límite) y 1000")
	}
	if c.Voucher.DataMB < 0 || c.Voucher.DataMB > MaxDataMB {
		add("voucher.data_mb: debe estar entre 0 (sin límite) y %d (1 TB, el máximo que admite UniFi)", MaxDataMB)
	}
	if c.Voucher.UnusedExpireMinutes < 0 || c.Voucher.UnusedExpireMinutes > MaxUnusedExpireMinutes {
		add("voucher.unused_expire_minutes: debe estar entre 0 (no caducan) y %d (7 días)", MaxUnusedExpireMinutes)
	}
	for name, v := range map[string]int{"voucher.rx_kbps": c.Voucher.RxKbps, "voucher.tx_kbps": c.Voucher.TxKbps} {
		if v != 0 && (v < 2 || v > 100000) {
			add("%s: debe ser 0 (sin límite) o estar entre 2 y 100000", name)
		}
	}

	c.Kiosk.Title = strings.TrimSpace(c.Kiosk.Title)
	if c.Kiosk.Title == "" {
		add("kiosk.title: no puede estar vacío")
	}
	c.Kiosk.Intro = cleanText(c.Kiosk.Intro)
	c.Kiosk.TermsText = cleanText(c.Kiosk.TermsText)
	if utf8.RuneCountInString(c.Kiosk.Title) > MaxTitleLen {
		add("kiosk.title: como máximo %d caracteres", MaxTitleLen)
	}
	// Un tema desconocido (o una configuración anterior sin tema) usa el de por defecto.
	c.Kiosk.Theme = strings.ToLower(strings.TrimSpace(c.Kiosk.Theme))
	if !ValidTheme(c.Kiosk.Theme) {
		c.Kiosk.Theme = DefaultTheme
	}
	if utf8.RuneCountInString(c.Kiosk.Intro) > MaxIntroLen {
		add("kiosk.intro: como máximo %d caracteres", MaxIntroLen)
	}
	if utf8.RuneCountInString(c.Kiosk.TermsText) > MaxTermsLen {
		add("kiosk.terms_text: como máximo %d caracteres", MaxTermsLen)
	}
	if c.Kiosk.TermsEnabled && c.Kiosk.TermsText == "" {
		add("kiosk.terms_text: escriba las condiciones de uso o desactive «Mostrar condiciones»")
	}
	if c.Kiosk.DisplaySeconds < 10 || c.Kiosk.DisplaySeconds > 600 {
		add("kiosk.display_seconds: debe estar entre 10 y 600")
	}
	if c.Kiosk.CooldownSeconds < 0 || c.Kiosk.CooldownSeconds > 3600 {
		add("kiosk.cooldown_seconds: debe estar entre 0 y 3600")
	}
	if c.Kiosk.DailyLimit < 1 {
		add("kiosk.daily_limit: debe ser al menos 1")
	}
	c.Kiosk.Language = strings.ToLower(strings.TrimSpace(c.Kiosk.Language))
	if c.Kiosk.Language == "" {
		c.Kiosk.Language = "es"
	}
	for name, p := range map[string]string{"kiosk.logo_path": c.Kiosk.LogoPath, "kiosk.background_image": c.Kiosk.BackgroundImage} {
		if p != "" && !safeDataFile(p) {
			add("%s: debe ser un nombre de fichero del directorio de datos (sin rutas ni «..»)", name)
		}
	}

	if len(errs) > 0 {
		return errors.New("configuración no válida:\n  - " + strings.Join(errs, "\n  - "))
	}
	return nil
}

// safeDataFile acepta solo un nombre de fichero simple (logo.svg, fondo.jpg…).
func safeDataFile(p string) bool {
	return p == filepath.Base(p) && p != "." && p != ".." && !strings.ContainsAny(p, `/\:`)
}

func trimList(in []string) []string {
	out := []string{}
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Allowlist es una lista de IPs/redes permitidas. La red loopback (127.0.0.0/8 y
// ::1) siempre está permitida: el tótem puede ser la propia máquina.
type Allowlist []netip.Prefix

// ParseAllowlist interpreta entradas como "127.0.0.1", "192.0.2.0/24" o "::1".
func ParseAllowlist(entries []string) (Allowlist, error) {
	var out Allowlist
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.Contains(e, "/") {
			p, err := netip.ParsePrefix(e)
			if err != nil {
				return nil, fmt.Errorf("%q no es una red CIDR válida", e)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(e)
		if err != nil {
			return nil, fmt.Errorf("%q no es una IP válida", e)
		}
		out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return out, nil
}

// Allows indica si la IP está permitida.
func (l Allowlist) Allows(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() {
		return true
	}
	for _, p := range l {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Store guarda la configuración en memoria y en disco.
type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

// Load lee la configuración de path. Si el fichero no existe lo crea con los
// valores por defecto (created = true).
func Load(path string) (st *Store, created bool, err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(abs)
	if errors.Is(err, os.ErrNotExist) {
		cfg := Default()
		if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
			return nil, false, fmt.Errorf("no se puede crear el directorio de configuración: %w", err)
		}
		if err := writeAtomic(abs, cfg); err != nil {
			return nil, false, err
		}
		return &Store{path: abs, cfg: cfg}, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("no se puede leer %s: %w", abs, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", abs, err)
	}
	return &Store{path: abs, cfg: cfg}, false, nil
}

// Parse interpreta y valida un config.json. Los campos ausentes toman el valor por defecto.
func Parse(data []byte) (Config, error) {
	cfg := Default()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("JSON no válido: %w", err)
	}
	migrateCleanup(data, &cfg)
	cfg.Kiosk.LegacyHeaderText = ""
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// migrateCleanup convierte voucher.unused_cleanup_hours (primera v1.3, horas, revisión
// horaria) en voucher.unused_expire_minutes. Si el fichero ya trae el campo nuevo, el
// antiguo se ignora. 24 h era el valor por defecto: se pasa al nuevo valor por defecto.
func migrateCleanup(data []byte, cfg *Config) {
	var raw struct {
		Voucher map[string]json.RawMessage `json:"voucher"`
	}
	_ = json.Unmarshal(data, &raw)
	_, hasNew := raw.Voucher["unused_expire_minutes"]
	_, hasOld := raw.Voucher["unused_cleanup_hours"]
	if hasOld && !hasNew {
		switch h := cfg.Voucher.LegacyCleanupHours; {
		case h <= 0:
			cfg.Voucher.UnusedExpireMinutes = 0
		case h == 24:
			cfg.Voucher.UnusedExpireMinutes = Default().Voucher.UnusedExpireMinutes
		default:
			cfg.Voucher.UnusedExpireMinutes = min(h*60, MaxUnusedExpireMinutes)
		}
	}
	cfg.Voucher.LegacyCleanupHours = 0
}

// NewMemoryStore crea un Store sin fichero (para pruebas).
func NewMemoryStore(cfg Config) *Store { return &Store{cfg: cfg} }

// Path devuelve la ruta absoluta de config.json.
func (s *Store) Path() string { return s.path }

// Get devuelve una copia de la configuración actual.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Clone()
}

// Update aplica fn sobre una copia, la valida y, si es correcta, la guarda.
func (s *Store) Update(fn func(*Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg.Clone()
	if err := fn(&next); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if s.path != "" {
		if err := writeAtomic(s.path, next); err != nil {
			return err
		}
	}
	s.cfg = next
	return nil
}

// writeAtomic escribe el JSON en un temporal del mismo directorio y lo renombra.
// Mantiene los permisos del fichero anterior; si no existía, 0600 (contiene la API key).
func writeAtomic(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("no se puede guardar la configuración: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("no se puede guardar la configuración: %w", err)
	}
	return nil
}
