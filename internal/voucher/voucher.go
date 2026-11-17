// Paquete voucher: reglas de negocio para entregar códigos.
//
//   - cooldown por IP de cliente (kiosk.cooldown_seconds);
//   - límite diario global (kiosk.daily_limit) que se reinicia a medianoche local
//     y se guarda en state.json tras cada voucher, para que sobreviva a reinicios;
//   - detección y guardado del site_id;
//   - formato (12345-67890) y enmascarado para el log (*****-67890).
package voucher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"totem-wifi/internal/config"
	"totem-wifi/internal/unifi"
)

// Errores que la web traduce a mensajes para el ciudadano.
var (
	ErrDailyLimit  = errors.New("límite diario de vouchers alcanzado")
	ErrUnknownSSID = errors.New("SSID no configurado")
)

// CooldownError: este tótem tiene que esperar antes de pedir otro código.
type CooldownError struct{ Wait time.Duration }

func (e *CooldownError) Error() string {
	return fmt.Sprintf("hay que esperar %d s antes de pedir otro código", int(e.Wait.Round(time.Second)/time.Second))
}

// Seconds devuelve la espera redondeada hacia arriba (mínimo 1).
func (e *CooldownError) Seconds() int {
	s := int((e.Wait + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	return s
}

// UnavailableError: UniFi no ha podido dar el código (red, huella, clave…).
type UnavailableError struct{ Err error }

func (e *UnavailableError) Error() string { return "UniFi no disponible: " + e.Err.Error() }
func (e *UnavailableError) Unwrap() error { return e.Err }

// Result es un voucher listo para mostrar.
type Result struct {
	Code      string
	Formatted string
	SSID      string
	Minutes   int
	Mock      bool
}

// ClientFunc devuelve el cliente de UniFi adecuado para la configuración actual.
type ClientFunc func(config.Config) unifi.Client

// State es el contenido de state.json.
type State struct {
	Date  string `json:"date"` // AAAA-MM-DD en hora local
	Count int    `json:"count"`
}

// Service entrega vouchers aplicando las reglas.
type Service struct {
	cfg       *config.Store
	client    ClientFunc
	statePath string
	log       *log.Logger
	Now       func() time.Time // reloj inyectable (tests)

	mu       sync.Mutex
	last     map[string]time.Time // último voucher por IP
	inflight map[string]bool      // petición en curso por IP
	state    State
	pending  int // vouchers pedidos a UniFi y aún sin respuesta

	healthMu sync.Mutex
	health   Health
}

// Health es el último estado conocido de la conexión con UniFi.
type Health struct {
	Status string    // "ok", "error" o "mock"
	Error  string    // solo para el log y /admin, nunca para el ciudadano
	At     time.Time // cuándo se comprobó
}

// New crea el servicio y lee state.json (si no existe, empieza en 0).
func New(cfg *config.Store, client ClientFunc, statePath string, logger *log.Logger) *Service {
	if logger == nil {
		logger = log.New(os.Stderr, "", log.LstdFlags)
	}
	s := &Service{
		cfg:       cfg,
		client:    client,
		statePath: statePath,
		log:       logger,
		Now:       time.Now,
		last:      map[string]time.Time{},
		inflight:  map[string]bool{},
	}
	if statePath != "" {
		data, err := os.ReadFile(statePath)
		switch {
		case err == nil:
			if err := json.Unmarshal(data, &s.state); err != nil {
				s.log.Printf("AVISO: %s está dañado (%v); el contador diario empieza en 0", statePath, err)
				s.state = State{}
			}
		case !errors.Is(err, os.ErrNotExist):
			s.log.Printf("AVISO: no se puede leer %s: %v", statePath, err)
		}
	}
	return s
}

func (s *Service) today() string { return s.Now().Format("2006-01-02") }

// rollLocked reinicia el contador si ha cambiado el día. Requiere s.mu.
func (s *Service) rollLocked() {
	if d := s.today(); s.state.Date != d {
		s.state = State{Date: d}
	}
}

// DailyUsed devuelve cuántos vouchers se han entregado hoy.
func (s *Service) DailyUsed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollLocked()
	return s.state.Count
}

// Options modifica una petición concreta.
type Options struct {
	SkipCooldown bool   // voucher de prueba desde /admin
	Note         string // sustituye a voucher.note
	TermsVersion string // versión de las condiciones de uso aceptadas (vacío = no se mostraban)
}

// Issue crea un voucher para el cliente con IP ip y el SSID indicado
// (vacío = el principal).
func (s *Service) Issue(ctx context.Context, ip, ssid string, opt Options) (Result, error) {
	cfg := s.cfg.Get()
	if ssid == "" {
		ssid = cfg.Voucher.SSID
	}
	if _, ok := cfg.FindSSID(ssid); !ok {
		return Result{}, ErrUnknownSSID
	}

	// 1. Reserva: cooldown, petición simultánea y límite diario.
	s.mu.Lock()
	now := s.Now()
	s.rollLocked()
	cooldown := time.Duration(cfg.Kiosk.CooldownSeconds) * time.Second
	for k, t := range s.last { // limpieza para que el mapa no crezca sin fin
		if now.Sub(t) >= cooldown {
			delete(s.last, k)
		}
	}
	if !opt.SkipCooldown {
		if s.inflight[ip] {
			s.mu.Unlock()
			return Result{}, &CooldownError{Wait: time.Second}
		}
		if t, ok := s.last[ip]; ok && now.Sub(t) < cooldown {
			s.mu.Unlock()
			return Result{}, &CooldownError{Wait: cooldown - now.Sub(t)}
		}
	}
	if s.state.Count+s.pending >= cfg.Kiosk.DailyLimit {
		s.mu.Unlock()
		s.log.Printf("límite diario alcanzado (%d); petición de %s rechazada", cfg.Kiosk.DailyLimit, ip)
		return Result{}, ErrDailyLimit
	}
	s.pending++
	s.inflight[ip] = true
	s.mu.Unlock()

	// 2. Petición a UniFi (o al mock), sin el cerrojo.
	v, err := s.create(ctx, cfg, opt)

	// 3. Confirmación.
	s.mu.Lock()
	s.pending--
	delete(s.inflight, ip)
	if err != nil {
		s.mu.Unlock()
		s.setHealth("error", err)
		s.log.Printf("ERROR UniFi al crear voucher para %s: %v", ip, err)
		return Result{}, &UnavailableError{Err: err}
	}
	s.rollLocked()
	s.state.Count++
	if !opt.SkipCooldown {
		s.last[ip] = s.Now()
	}
	st := s.state
	s.mu.Unlock()
	if err := s.saveState(st); err != nil {
		s.log.Printf("AVISO: no se puede guardar el contador diario: %v", err)
	}
	if cfg.UniFi.Mock {
		s.setHealth("mock", nil)
	} else {
		s.setHealth("ok", nil)
	}

	res := Result{
		Code:      v.Code,
		Formatted: Format(v.Code),
		SSID:      ssid,
		Minutes:   cfg.Voucher.Minutes,
		Mock:      cfg.UniFi.Mock,
	}
	modo := ""
	if res.Mock {
		modo = " [SIMULACIÓN]"
	}
	condiciones := ""
	if opt.TermsVersion != "" {
		condiciones = " condiciones_aceptadas=" + opt.TermsVersion
	}
	s.log.Printf("voucher creado%s ip=%s codigo=%s ssid=%q minutos=%d hoy=%d/%d%s",
		modo, ip, Mask(v.Code), ssid, res.Minutes, st.Count, cfg.Kiosk.DailyLimit, condiciones)
	return res, nil
}

func (s *Service) create(ctx context.Context, cfg config.Config, opt Options) (unifi.Voucher, error) {
	c := s.client(cfg)
	siteID := cfg.UniFi.SiteID
	if !c.Mock() && siteID == "" {
		var err error
		if siteID, err = s.ensureSite(ctx, c); err != nil {
			return unifi.Voucher{}, err
		}
	}
	note := cfg.Voucher.Note
	if opt.Note != "" {
		note = opt.Note
	}
	return c.CreateVoucher(ctx, siteID, unifi.VoucherRequest{
		Name:             note,
		TimeLimitMinutes: cfg.Voucher.Minutes,
		GuestLimit:       cfg.Voucher.GuestLimit,
		DataMB:           cfg.Voucher.DataMB,
		RxKbps:           cfg.Voucher.RxKbps,
		TxKbps:           cfg.Voucher.TxKbps,
	})
}

// ensureSite obtiene el sitio «default» y lo guarda en config.json.
func (s *Service) ensureSite(ctx context.Context, c unifi.Client) (string, error) {
	sites, err := c.Sites(ctx)
	if err != nil {
		return "", err
	}
	site, err := unifi.DefaultSite(sites)
	if err != nil {
		return "", err
	}
	if err := s.cfg.Update(func(cf *config.Config) error { cf.UniFi.SiteID = site.ID; return nil }); err != nil {
		s.log.Printf("AVISO: no se puede guardar site_id en la configuración: %v", err)
	} else {
		s.log.Printf("sitio de UniFi detectado: %q (%s); guardado en la configuración", site.Name, site.ID)
	}
	return site.ID, nil
}

// CheckResult es el resultado de probar la conexión con UniFi.
type CheckResult struct {
	Mock     bool
	SiteID   string
	SiteName string
	Sites    int
}

// Check prueba la conexión: lista los sitios y, si site_id está vacío, lo detecta
// y lo guarda. Si site_id está configurado, comprueba que exista.
func (s *Service) Check(ctx context.Context) (CheckResult, error) {
	cfg := s.cfg.Get()
	c := s.client(cfg)
	if c.Mock() {
		s.setHealth("mock", nil)
		return CheckResult{Mock: true}, nil
	}
	sites, err := c.Sites(ctx)
	if err != nil {
		s.setHealth("error", err)
		return CheckResult{}, err
	}
	res := CheckResult{Sites: len(sites)}
	if cfg.UniFi.SiteID == "" {
		site, err := unifi.DefaultSite(sites)
		if err != nil {
			s.setHealth("error", err)
			return res, err
		}
		if err := s.cfg.Update(func(cf *config.Config) error { cf.UniFi.SiteID = site.ID; return nil }); err != nil {
			return res, fmt.Errorf("sitio detectado pero no se puede guardar: %w", err)
		}
		s.log.Printf("sitio de UniFi detectado: %q (%s); guardado en la configuración", site.Name, site.ID)
		res.SiteID, res.SiteName = site.ID, site.Name
	} else {
		for _, st := range sites {
			if st.ID == cfg.UniFi.SiteID {
				res.SiteID, res.SiteName = st.ID, st.Name
			}
		}
		if res.SiteID == "" {
			err := fmt.Errorf("el site_id configurado (%s) no existe en este UniFi; déjelo vacío para detectarlo", cfg.UniFi.SiteID)
			s.setHealth("error", err)
			return res, err
		}
	}
	s.setHealth("ok", nil)
	return res, nil
}

func (s *Service) setHealth(status string, err error) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	s.health = Health{Status: status, At: s.Now()}
	if err != nil {
		s.health.Error = err.Error()
	}
}

// Health devuelve el estado de UniFi. Si el último dato tiene más de maxAge,
// vuelve a comprobarlo (una sola comprobación a la vez).
func (s *Service) Health(ctx context.Context, maxAge time.Duration) Health {
	cfg := s.cfg.Get()
	if cfg.UniFi.Mock {
		return Health{Status: "mock", At: s.Now()}
	}
	s.healthMu.Lock()
	h := s.health
	s.healthMu.Unlock()
	if h.Status != "" && h.Status != "mock" && s.Now().Sub(h.At) < maxAge {
		return h
	}
	s.Check(ctx) //nolint:errcheck // el resultado queda en s.health
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	return s.health
}

// saveState escribe state.json de forma atómica.
func (s *Service) saveState(st State) error {
	if s.statePath == "" {
		return nil
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.statePath), ".state-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.statePath)
}

// Format muestra un código de 10 cifras como 12345-67890.
func Format(code string) string {
	if len(code) == 10 {
		return code[:5] + "-" + code[5:]
	}
	return code
}

// Mask oculta la primera mitad del código para el log: *****-67890.
func Mask(code string) string {
	f := Format(code)
	if i := strings.IndexByte(f, '-'); i > 0 {
		return strings.Repeat("*", i) + f[i:]
	}
	if n := len(code); n > 4 {
		return strings.Repeat("*", n-4) + code[n-4:]
	}
	return "****"
}

// LastHealth devuelve el último estado conocido sin contactar con UniFi.
func (s *Service) LastHealth() Health {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	return s.health
}
