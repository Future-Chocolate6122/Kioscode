package voucher

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"totem-wifi/internal/config"
	"totem-wifi/internal/unifi"
)

// reloj controlable desde los tests.
type reloj struct {
	mu sync.Mutex
	t  time.Time
}

func (r *reloj) Now() time.Time         { r.mu.Lock(); defer r.mu.Unlock(); return r.t }
func (r *reloj) Avanza(d time.Duration) { r.mu.Lock(); r.t = r.t.Add(d); r.mu.Unlock() }

// falso es un cliente de UniFi que cuenta llamadas y puede fallar.
type falso struct {
	mu       sync.Mutex
	llamadas int
	sitios   int
	err      error
	ultimo   unifi.VoucherRequest
	site     string

	// Invitados
	invitados   []unifi.Guest
	auth        map[string]unifi.GuestAuth
	desconectar map[string]error // error a devolver al desconectar cada id
	echados     []string
	filtros     []string // filtros recibidos en DeleteVouchers
	borrados    int
}

func (f *falso) ListGuests(context.Context, string) ([]unifi.Guest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]unifi.Guest(nil), f.invitados...), nil
}
func (f *falso) GuestDetails(_ context.Context, _, id string) (unifi.GuestAuth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.auth[id], nil
}
func (f *falso) UnauthorizeGuest(_ context.Context, _, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.desconectar[id]; err != nil {
		return err
	}
	f.echados = append(f.echados, id)
	return nil
}
func (f *falso) DeleteVouchers(_ context.Context, _, filter string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.filtros = append(f.filtros, filter)
	return f.borrados, nil
}

func (f *falso) Mock() bool { return false }
func (f *falso) Sites(context.Context) ([]unifi.Site, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sitios++
	return []unifi.Site{{ID: "site-default", InternalReference: "default", Name: "Default"}}, nil
}
func (f *falso) CreateVoucher(_ context.Context, site string, r unifi.VoucherRequest) (unifi.Voucher, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.llamadas++
	f.ultimo, f.site = r, site
	if f.err != nil {
		return unifi.Voucher{}, f.err
	}
	return unifi.Voucher{Code: "1234567890"}, nil
}

func nuevo(t *testing.T, mod func(*config.Config)) (*Service, *falso, *reloj, *bytes.Buffer, string) {
	t.Helper()
	cfg := config.Default()
	cfg.UniFi.Mock = false
	cfg.UniFi.SiteID = "site-1"
	if mod != nil {
		mod(&cfg)
	}
	st := config.NewMemoryStore(cfg)
	f := &falso{}
	r := &reloj{t: time.Date(2026, 11, 12, 10, 0, 0, 0, time.Local)}
	var buf bytes.Buffer
	path := filepath.Join(t.TempDir(), "state.json")
	s := New(st, func(config.Config) unifi.Client { return f }, path, log.New(&buf, "", 0))
	s.Now = r.Now
	return s, f, r, &buf, path
}

func TestIssueBasico(t *testing.T) {
	s, f, _, buf, _ := nuevo(t, nil)
	res, err := s.Issue(context.Background(), "192.0.2.110", "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Formatted != "12345-67890" || res.SSID != "WifiPublica" || res.Minutes != 240 || res.Mock {
		t.Errorf("resultado inesperado: %+v", res)
	}
	if f.site != "site-1" || f.ultimo.Name != "Totem" || f.ultimo.TimeLimitMinutes != 240 || f.ultimo.GuestLimit != 1 {
		t.Errorf("petición a UniFi inesperada: site=%s %+v", f.site, f.ultimo)
	}
	l := buf.String()
	if !strings.Contains(l, "*****-67890") || strings.Contains(l, "1234567890") || !strings.Contains(l, "192.0.2.110") {
		t.Errorf("línea de log incorrecta (debe enmascarar el código): %q", l)
	}
}

func TestCooldownPorIP(t *testing.T) {
	s, _, r, _, _ := nuevo(t, nil) // cooldown 20 s
	ctx := context.Background()
	if _, err := s.Issue(ctx, "192.0.2.110", "", Options{}); err != nil {
		t.Fatal(err)
	}
	r.Avanza(5 * time.Second)
	_, err := s.Issue(ctx, "192.0.2.110", "", Options{})
	var ce *CooldownError
	if !errors.As(err, &ce) || ce.Seconds() != 15 {
		t.Fatalf("se esperaba esperar 15 s, err=%v", err)
	}
	// Otro tótem no se ve afectado.
	if _, err := s.Issue(ctx, "192.0.2.111", "", Options{}); err != nil {
		t.Fatalf("otra IP no debería tener cooldown: %v", err)
	}
	r.Avanza(15 * time.Second)
	if _, err := s.Issue(ctx, "192.0.2.110", "", Options{}); err != nil {
		t.Fatalf("pasado el cooldown debería funcionar: %v", err)
	}
}

func TestCooldownNoSeAplicaSiFalla(t *testing.T) {
	s, f, _, _, _ := nuevo(t, nil)
	f.err = &unifi.APIError{Status: 500, Body: "x"}
	_, err := s.Issue(context.Background(), "192.0.2.110", "", Options{})
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("se esperaba UnavailableError: %v", err)
	}
	if s.DailyUsed() != 0 {
		t.Error("un fallo no debe contar en el límite diario")
	}
	f.err = nil
	if _, err := s.Issue(context.Background(), "192.0.2.110", "", Options{}); err != nil {
		t.Errorf("tras un fallo se puede volver a intentar: %v", err)
	}
}

func TestLimiteDiarioYReinicioAMedianoche(t *testing.T) {
	s, f, r, _, _ := nuevo(t, func(c *config.Config) { c.Kiosk.DailyLimit = 3; c.Kiosk.CooldownSeconds = 0 })
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.Issue(ctx, "192.0.2.110", "", Options{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Issue(ctx, "192.0.2.111", "", Options{}); !errors.Is(err, ErrDailyLimit) {
		t.Fatalf("se esperaba ErrDailyLimit: %v", err)
	}
	if f.llamadas != 3 {
		t.Errorf("con el límite alcanzado no se debe llamar a UniFi (llamadas=%d)", f.llamadas)
	}
	r.Avanza(14 * time.Hour) // 00:00 del día siguiente
	if s.DailyUsed() != 0 {
		t.Error("el contador debe reiniciarse a medianoche")
	}
	if _, err := s.Issue(ctx, "192.0.2.111", "", Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestContadorSobreviveReinicio(t *testing.T) {
	s, f, r, _, path := nuevo(t, func(c *config.Config) { c.Kiosk.CooldownSeconds = 0 })
	for i := 0; i < 2; i++ {
		if _, err := s.Issue(context.Background(), "192.0.2.110", "", Options{}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"count":2`) {
		t.Errorf("state.json = %s", data)
	}
	// «Reinicio»: un servicio nuevo con el mismo fichero.
	s2 := New(config.NewMemoryStore(config.Default()), func(config.Config) unifi.Client { return f }, path, log.New(&bytes.Buffer{}, "", 0))
	s2.Now = r.Now
	if s2.DailyUsed() != 2 {
		t.Errorf("tras reiniciar, daily_used = %d", s2.DailyUsed())
	}
	r.Avanza(24 * time.Hour)
	if s2.DailyUsed() != 0 {
		t.Error("un contador de otro día no cuenta")
	}
}

func TestStateDanado(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(path, []byte("{no json"), 0o600)
	var buf bytes.Buffer
	s := New(config.NewMemoryStore(config.Default()), func(config.Config) unifi.Client { return unifi.NewMock() }, path, log.New(&buf, "", 0))
	if s.DailyUsed() != 0 || !strings.Contains(buf.String(), "dañado") {
		t.Error("un state.json dañado debe avisar y empezar en 0")
	}
}

func TestSSIDNoConfigurado(t *testing.T) {
	s, f, _, _, _ := nuevo(t, func(c *config.Config) { c.Voucher.ExtraSSIDs = []config.SSID{{SSID: "Actos"}} })
	if _, err := s.Issue(context.Background(), "1.1.1.1", "OtraRed", Options{}); !errors.Is(err, ErrUnknownSSID) {
		t.Errorf("err = %v", err)
	}
	res, err := s.Issue(context.Background(), "1.1.1.1", "Actos", Options{})
	if err != nil || res.SSID != "Actos" {
		t.Errorf("SSID adicional: %+v %v", res, err)
	}
	if f.llamadas != 1 {
		t.Error("un SSID no configurado no debe llegar a UniFi")
	}
}

func TestSiteIDSeDetectaYGuarda(t *testing.T) {
	s, f, _, _, _ := nuevo(t, func(c *config.Config) { c.UniFi.SiteID = "" })
	if _, err := s.Issue(context.Background(), "1.1.1.1", "", Options{}); err != nil {
		t.Fatal(err)
	}
	if f.site != "site-default" || s.cfg.Get().UniFi.SiteID != "site-default" {
		t.Errorf("site_id no detectado: usado=%q guardado=%q", f.site, s.cfg.Get().UniFi.SiteID)
	}
	s.Issue(context.Background(), "1.1.1.2", "", Options{})
	if f.sitios != 1 {
		t.Error("una vez guardado, no hace falta volver a pedir los sitios")
	}
}

func TestCheck(t *testing.T) {
	s, _, _, _, _ := nuevo(t, func(c *config.Config) { c.UniFi.SiteID = "" })
	res, err := s.Check(context.Background())
	if err != nil || res.SiteID != "site-default" {
		t.Fatalf("%+v %v", res, err)
	}
	if h := s.Health(context.Background(), time.Minute); h.Status != "ok" {
		t.Errorf("health = %+v", h)
	}
	s.cfg.Update(func(c *config.Config) error { c.UniFi.SiteID = "otro"; return nil })
	if _, err := s.Check(context.Background()); err == nil {
		t.Error("un site_id inexistente debe dar error")
	}
}

func TestPruebaAdminSinCooldown(t *testing.T) {
	s, f, _, _, _ := nuevo(t, nil)
	for i := 0; i < 3; i++ {
		if _, err := s.Issue(context.Background(), "127.0.0.1", "", Options{SkipCooldown: true, Note: "Prueba admin"}); err != nil {
			t.Fatal(err)
		}
	}
	if f.ultimo.Name != "Prueba admin" {
		t.Errorf("nota = %q", f.ultimo.Name)
	}
}

func TestConcurrenciaNoSuperaLimite(t *testing.T) {
	s, _, _, _, _ := nuevo(t, func(c *config.Config) { c.Kiosk.DailyLimit = 5; c.Kiosk.CooldownSeconds = 0 })
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.Issue(context.Background(), "203.0.113."+string(rune('0'+i%10)), "", Options{SkipCooldown: true}); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if ok != 5 || s.DailyUsed() != 5 {
		t.Errorf("entregados %d, contador %d; límite 5", ok, s.DailyUsed())
	}
}

func TestFormatYMask(t *testing.T) {
	if Format("1234567890") != "12345-67890" {
		t.Error(Format("1234567890"))
	}
	if Format("123") != "123" {
		t.Error("los códigos con otra longitud se dejan tal cual")
	}
	if Mask("1234567890") != "*****-67890" {
		t.Error(Mask("1234567890"))
	}
	if Mask("12345678") != "****5678" {
		t.Error(Mask("12345678"))
	}
}
