package unifi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestListGuestsPaginaYFiltra(t *testing.T) {
	// 250 clientes: los pares son invitados (GUEST), los impares internos (DEFAULT).
	_, c := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxy/network/integration/v1/sites/s1/clients" {
			t.Errorf("ruta %s", r.URL.Path)
		}
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		lim, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if lim != 200 {
			t.Errorf("limit=%d", lim)
		}
		var data []string
		for i := off; i < 250 && i < off+lim; i++ {
			acc := `{"type":"DEFAULT"}`
			if i%2 == 0 {
				acc = fmt.Sprintf(`{"type":"GUEST","authorized":%v}`, i%4 == 0)
			}
			data = append(data, fmt.Sprintf(`{"type":"WIRELESS","id":"id-%d","name":"m%d","connectedAt":"2026-11-17T10:00:00Z","ipAddress":"203.0.113.%d","macAddress":"02:00:00:00:00:%02x","access":%s}`, i, i, i%250, i%256, acc))
		}
		fmt.Fprintf(w, `{"offset":%d,"limit":%d,"count":%d,"totalCount":250,"data":[%s]}`, off, lim, len(data), strings.Join(data, ","))
	})
	g, err := c.ListGuests(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(g) != 125 {
		t.Fatalf("invitados: %d, want 125", len(g))
	}
	if g[0].ID != "id-0" || !g[0].Authorized || g[1].Authorized || g[0].ConnectedAt.IsZero() || g[0].MAC == "" {
		t.Errorf("datos del invitado: %+v / %+v", g[0], g[1])
	}
}

func TestGuestDetailsYDesconectar(t *testing.T) {
	var accion map[string]string
	_, c := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/proxy/network/integration/v1/sites/s1/clients/c1":
			io.WriteString(w, `{"type":"WIRELESS","id":"c1","access":{"type":"GUEST","authorized":true,"authorization":{"authorizedAt":"2026-11-17T10:00:00Z","authorizationMethod":"VOUCHER","expiresAt":"2026-11-17T14:00:00Z"}}}`)
		case r.Method == "POST" && r.URL.Path == "/proxy/network/integration/v1/sites/s1/clients/c1/actions":
			json.NewDecoder(r.Body).Decode(&accion)
			io.WriteString(w, `{"action":"UNAUTHORIZE_GUEST_ACCESS"}`)
		default:
			w.WriteHeader(404)
		}
	})
	a, err := c.GuestDetails(context.Background(), "s1", "c1")
	if err != nil || a.Method != "VOUCHER" || !a.ExpiresAt.Equal(time.Date(2026, 11, 17, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("ficha: %+v %v", a, err)
	}
	if err := c.UnauthorizeGuest(context.Background(), "s1", "c1"); err != nil || accion["action"] != "UNAUTHORIZE_GUEST_ACCESS" {
		t.Errorf("desconectar: %v %v", accion, err)
	}
	if err := c.UnauthorizeGuest(context.Background(), "s1", "otro"); err == nil {
		t.Error("un 404 debe devolver error")
	}
}

func TestDeleteVouchersConFiltro(t *testing.T) {
	var filtro string
	_, c := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" || r.URL.Path != "/proxy/network/integration/v1/sites/s1/hotspot/vouchers" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		filtro = r.URL.Query().Get("filter")
		io.WriteString(w, `{"vouchersDeleted":7}`)
	})
	f := "and(name.in(" + FilterString("Tótem d'Ana") + "), expired.eq(false))"
	n, err := c.DeleteVouchers(context.Background(), "s1", f)
	if err != nil || n != 7 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if filtro != "and(name.in('Tótem d''Ana'), expired.eq(false))" {
		t.Errorf("filtro recibido: %q", filtro)
	}
	if _, err := c.DeleteVouchers(context.Background(), "s1", " "); err == nil {
		t.Error("borrar sin filtro debe rechazarse")
	}
}

func TestFilterTime(t *testing.T) {
	if got := FilterTime(time.Date(2026, 11, 17, 11, 30, 0, 0, time.FixedZone("x", 3600))); got != "2026-11-17T10:30:00Z" {
		t.Error(got)
	}
}

func TestMockInvitados(t *testing.T) {
	m := NewMock()
	g, _ := m.ListGuests(context.Background(), "")
	if len(g) != 7 {
		t.Fatalf("invitados de ejemplo: %d", len(g))
	}
	if err := m.UnauthorizeGuest(context.Background(), "", g[0].ID); err != nil {
		t.Fatal(err)
	}
	if g2, _ := m.ListGuests(context.Background(), ""); len(g2) != 6 {
		t.Error("el simulador debe recordar a quién se ha desconectado")
	}
	if err := m.UnauthorizeGuest(context.Background(), "", g[0].ID); err == nil {
		t.Error("desconectar a alguien que ya no está debe dar error")
	}
	var p Provider
	if p.Get(true, Config{}) != p.Get(true, Config{}) {
		t.Error("el Provider debe devolver siempre el mismo simulador")
	}
}
