package unifi

// Invitados conectados y borrado de vouchers por filtro (Integration API, Network 10.x).
//
//   GET    /v1/sites/{siteId}/clients                  lista paginada (máx. 200 por página)
//   GET    /v1/sites/{siteId}/clients/{clientId}       ficha con la autorización del invitado
//   POST   /v1/sites/{siteId}/clients/{clientId}/actions   {"action":"UNAUTHORIZE_GUEST_ACCESS"}
//   DELETE /v1/sites/{siteId}/hotspot/vouchers?filter=… → {"vouchersDeleted": n}
//
// La API no permite filtrar los clientes por tipo de acceso: se recorren todos y se
// quedan los de acceso «GUEST» (los que entran por el portal cautivo).

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Guest es un invitado conectado (dispositivo con acceso de tipo GUEST).
type Guest struct {
	ID          string
	Name        string
	IP          string
	MAC         string
	Wired       bool
	ConnectedAt time.Time
	Authorized  bool
}

// GuestAuth es la autorización vigente de un invitado.
type GuestAuth struct {
	AuthorizedAt time.Time
	ExpiresAt    time.Time
	Method       string // VOUCHER, API u OTHER
}

const (
	clientsPageSize = 200
	maxClientPages  = 25 // hasta 5000 dispositivos
)

type clientItem struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	ConnectedAt string `json:"connectedAt"`
	IPAddress   string `json:"ipAddress"`
	MACAddress  string `json:"macAddress"`
	Access      struct {
		Type          string `json:"type"`
		Authorized    bool   `json:"authorized"`
		Authorization *struct {
			AuthorizedAt        string `json:"authorizedAt"`
			AuthorizationMethod string `json:"authorizationMethod"`
			ExpiresAt           string `json:"expiresAt"`
		} `json:"authorization"`
	} `json:"access"`
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// ListGuests devuelve los invitados conectados al sitio (autorizados o no).
func (c *Real) ListGuests(ctx context.Context, siteID string) ([]Guest, error) {
	if siteID == "" {
		return nil, ErrNoSite
	}
	var out []Guest
	for page, offset := 0, 0; page < maxClientPages; page++ {
		var resp struct {
			Count      int          `json:"count"`
			TotalCount int          `json:"totalCount"`
			Data       []clientItem `json:"data"`
		}
		path := fmt.Sprintf("/sites/%s/clients?offset=%d&limit=%d", url.PathEscape(siteID), offset, clientsPageSize)
		if err := c.do(ctx, http.MethodGet, path, nil, &resp, http.StatusOK); err != nil {
			return nil, err
		}
		for _, it := range resp.Data {
			if it.Access.Type != "GUEST" {
				continue
			}
			out = append(out, Guest{
				ID: it.ID, Name: it.Name, IP: it.IPAddress, MAC: it.MACAddress,
				Wired:       it.Type == "WIRED",
				ConnectedAt: parseTime(it.ConnectedAt),
				Authorized:  it.Access.Authorized,
			})
		}
		offset += len(resp.Data)
		if len(resp.Data) == 0 || offset >= resp.TotalCount {
			break
		}
	}
	return out, nil
}

// GuestDetails devuelve la autorización vigente de un invitado.
func (c *Real) GuestDetails(ctx context.Context, siteID, clientID string) (GuestAuth, error) {
	var it clientItem
	path := "/sites/" + url.PathEscape(siteID) + "/clients/" + url.PathEscape(clientID)
	if err := c.do(ctx, http.MethodGet, path, nil, &it, http.StatusOK); err != nil {
		return GuestAuth{}, err
	}
	a := it.Access.Authorization
	if a == nil {
		return GuestAuth{}, nil
	}
	return GuestAuth{AuthorizedAt: parseTime(a.AuthorizedAt), ExpiresAt: parseTime(a.ExpiresAt), Method: a.AuthorizationMethod}, nil
}

// UnauthorizeGuest retira la autorización de un invitado y lo desconecta.
func (c *Real) UnauthorizeGuest(ctx context.Context, siteID, clientID string) error {
	path := "/sites/" + url.PathEscape(siteID) + "/clients/" + url.PathEscape(clientID) + "/actions"
	body := map[string]string{"action": "UNAUTHORIZE_GUEST_ACCESS"}
	return c.do(ctx, http.MethodPost, path, body, nil, http.StatusOK, http.StatusAccepted, http.StatusNoContent)
}

// DeleteVouchers borra los vouchers que cumplen el filtro y devuelve cuántos.
func (c *Real) DeleteVouchers(ctx context.Context, siteID, filter string) (int, error) {
	if strings.TrimSpace(filter) == "" {
		return 0, fmt.Errorf("se necesita un filtro para borrar vouchers")
	}
	var resp struct {
		VouchersDeleted int `json:"vouchersDeleted"`
	}
	path := "/sites/" + url.PathEscape(siteID) + "/hotspot/vouchers?" + url.Values{"filter": {filter}}.Encode()
	if err := c.do(ctx, http.MethodDelete, path, nil, &resp, http.StatusOK); err != nil {
		return 0, err
	}
	return resp.VouchersDeleted, nil
}

// FilterString escribe un texto para el lenguaje de filtros de la API:
// entre comillas simples, duplicando las comillas que contenga.
func FilterString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// FilterTime escribe una fecha y hora en ISO 8601 (UTC) para los filtros.
func FilterTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// ---------------------------------------------------------------------------
// Simulación

// MockClient genera códigos falsos e invitados de ejemplo, sin contactar con UniFi.
// Recuerda qué invitados se han desconectado y cuántos vouchers ha creado.
type MockClient struct {
	mu       sync.Mutex
	guests   []Guest
	auth     map[string]GuestAuth
	vouchers int
}

// NewMock crea un simulador con algunos invitados de ejemplo.
func NewMock() *MockClient {
	m := &MockClient{auth: map[string]GuestAuth{}}
	now := time.Now()
	ejemplos := []struct {
		name string
		mins int
		auth bool
	}{
		{"iPhone", 95, true}, {"Galaxy-S23", 62, true}, {"", 40, true}, {"Portatil-Ana", 33, true},
		{"Pixel-8", 21, true}, {"iPad", 9, true}, {"", 2, false},
	}
	for i, e := range ejemplos {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		m.guests = append(m.guests, Guest{
			ID: id, Name: e.name, IP: fmt.Sprintf("203.0.113.%d", 20+i),
			MAC:         fmt.Sprintf("02:00:5e:%02x:%02x:%02x", i*17%256, i*31%256, i*47%256),
			ConnectedAt: now.Add(-time.Duration(e.mins) * time.Minute), Authorized: e.auth,
		})
		if e.auth {
			at := now.Add(-time.Duration(e.mins-1) * time.Minute)
			m.auth[id] = GuestAuth{AuthorizedAt: at, ExpiresAt: at.Add(4 * time.Hour), Method: "VOUCHER"}
		}
	}
	return m
}

func (m *MockClient) Mock() bool { return true }

func (m *MockClient) Sites(context.Context) ([]Site, error) {
	return []Site{{ID: "00000000-0000-0000-0000-000000000000", InternalReference: "default", Name: "Simulación"}}, nil
}

func (m *MockClient) CreateVoucher(_ context.Context, _ string, r VoucherRequest) (Voucher, error) {
	v, err := mockVoucher(r)
	if err == nil {
		m.mu.Lock()
		m.vouchers++
		m.mu.Unlock()
	}
	return v, err
}

func (m *MockClient) ListGuests(context.Context, string) ([]Guest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Guest(nil), m.guests...), nil
}

func (m *MockClient) GuestDetails(_ context.Context, _, id string) (GuestAuth, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.auth[id], nil
}

func (m *MockClient) UnauthorizeGuest(_ context.Context, _, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, g := range m.guests {
		if g.ID == id {
			m.guests = append(m.guests[:i], m.guests[i+1:]...)
			delete(m.auth, id)
			return nil
		}
	}
	return &APIError{Status: http.StatusNotFound, Body: "cliente no conectado (simulación)"}
}

// DeleteVouchers en simulación borra todos los vouchers simulados (no interpreta el filtro).
func (m *MockClient) DeleteVouchers(context.Context, string, string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.vouchers
	m.vouchers = 0
	return n, nil
}
