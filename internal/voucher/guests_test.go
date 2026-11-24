package voucher

import (
	"context"
	"strings"
	"testing"
	"time"

	"kioscode/internal/config"
	"kioscode/internal/unifi"
)

func conInvitados(f *falso, r *reloj) {
	t0 := r.Now()
	f.invitados = []unifi.Guest{
		{ID: "a", Name: "iPhone", IP: "203.0.113.10", MAC: "02:00:00:00:00:0a", ConnectedAt: t0.Add(-30 * time.Minute), Authorized: true},
		{ID: "b", Name: "", IP: "203.0.113.11", MAC: "02:00:00:00:00:0b", ConnectedAt: t0.Add(-5 * time.Minute), Authorized: true},
		{ID: "c", Name: "Pendiente", IP: "203.0.113.12", MAC: "02:00:00:00:00:0c", ConnectedAt: t0.Add(-1 * time.Minute), Authorized: false},
	}
	f.auth = map[string]unifi.GuestAuth{
		"a": {ExpiresAt: t0.Add(3 * time.Hour), Method: "VOUCHER"},
		"b": {ExpiresAt: t0.Add(4 * time.Hour), Method: "VOUCHER"},
	}
}

func TestGuestsAutorizadosYOrden(t *testing.T) {
	s, f, r, _, _ := nuevo(t, nil)
	conInvitados(f, r)
	rows, err := s.Guests(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "b" || rows[1].ID != "a" {
		t.Fatalf("solo autorizados y los más recientes primero: %+v", rows)
	}
	if rows[1].ExpiresAt.IsZero() || rows[1].Method != "VOUCHER" {
		t.Errorf("faltan los datos de la ficha: %+v", rows[1])
	}
	todos, _ := s.Guests(context.Background(), true)
	if len(todos) != 3 {
		t.Errorf("con pendientes: %d", len(todos))
	}
}

func TestDisconnectGuestsParcial(t *testing.T) {
	s, f, r, buf, _ := nuevo(t, nil)
	conInvitados(f, r)
	f.desconectar = map[string]error{"b": &unifi.APIError{Status: 403, Body: "sin permiso"}}
	res, err := s.DisconnectGuests(context.Background(), []string{"a", "b", "zzz", "a"}, "198.51.100.5")
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 1 || len(res.Failed) != 2 {
		t.Fatalf("resultado: %+v", res)
	}
	if len(f.echados) != 1 || f.echados[0] != "a" {
		t.Errorf("desconectados: %v (no debe repetirse «a»)", f.echados)
	}
	if !strings.Contains(strings.Join(res.Failed, " "), "no tiene permiso") || !strings.Contains(strings.Join(res.Failed, " "), "ya no estaba conectado") {
		t.Errorf("fallos poco claros: %v", res.Failed)
	}
	l := buf.String()
	if !strings.Contains(l, "invitado desconectado mac=02:00:00:00:00:0a ip=203.0.113.10 (desde 198.51.100.5)") || strings.Contains(l, "iPhone") {
		t.Errorf("el log debe anotar MAC, IP y administrador, sin el nombre del dispositivo: %s", l)
	}
}

func TestDisconnectAllEInvalidar(t *testing.T) {
	s, f, r, _, _ := nuevo(t, func(c *config.Config) { c.Voucher.Note = "Tótem d'Ana" })
	conInvitados(f, r)
	f.borrados = 9
	res, err := s.DisconnectAll(context.Background(), true, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 2 || res.Invalidated != 9 {
		t.Fatalf("resultado: %+v", res)
	}
	if len(f.filtros) != 1 || f.filtros[0] != "and(name.in('Tótem d''Ana', 'Tótem d''Ana (prueba admin)'), expired.eq(false))" {
		t.Errorf("filtro de invalidación: %v", f.filtros)
	}
	// Sin invalidar no se borra ningún voucher.
	s2, f2, r2, _, _ := nuevo(t, nil)
	conInvitados(f2, r2)
	s2.DisconnectAll(context.Background(), false, "127.0.0.1")
	if len(f2.filtros) != 0 {
		t.Error("sin la opción no deben borrarse vouchers")
	}
}

func TestExpireUnused(t *testing.T) {
	s, f, r, buf, _ := nuevo(t, nil) // caducidad por defecto: 15 min
	f.borrados = 3
	n, err := s.ExpireUnused(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	limite := unifi.FilterTime(r.Now().Add(-15 * time.Minute))
	want := "and(name.in('Kioscode', 'Kioscode (prueba admin)'), authorizedGuestCount.eq(0), createdAt.lt(" + limite + "))"
	if len(f.filtros) != 1 || f.filtros[0] != want {
		t.Errorf("filtro de caducidad:\n got %v\nwant %s", f.filtros, want)
	}
	if !strings.Contains(buf.String(), "borrados 3 códigos del tótem sin usar de más de 15 min") {
		t.Errorf("log: %s", buf.String())
	}
	// Desactivada o en simulación: no se toca UniFi.
	s.cfg.Update(func(c *config.Config) error { c.Voucher.UnusedExpireMinutes = 0; return nil })
	if n, _ := s.ExpireUnused(context.Background()); n != 0 || len(f.filtros) != 1 {
		t.Error("con 0 minutos no debe borrar nada")
	}
	s.cfg.Update(func(c *config.Config) error { c.Voucher.UnusedExpireMinutes = 15; c.UniFi.Mock = true; return nil })
	if n, _ := s.ExpireUnused(context.Background()); n != 0 || len(f.filtros) != 1 {
		t.Error("en simulación no debe borrar nada")
	}
}

func TestSanitizeIDs(t *testing.T) {
	got := SanitizeIDs([]string{"a, b,,", " c ", strings.Repeat("x", 65)})
	if strings.Join(got, "|") != "a|b|c" {
		t.Errorf("%v", got)
	}
}
