package unifi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

const claveTest = "clave-secreta-de-prueba"

// servidor arranca un UniFi falso por HTTPS y devuelve un cliente anclado a su certificado.
func servidor(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Real) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	c := NewReal(Config{
		BaseURL:    srv.URL,
		APIKey:     claveTest,
		CertSHA256: Fingerprint(srv.Certificate().Raw),
	})
	return srv, c
}

func TestSitesEnviaCabeceraYRuta(t *testing.T) {
	_, c := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != claveTest {
			t.Errorf("cabecera X-API-KEY = %q", r.Header.Get("X-API-KEY"))
		}
		if r.Method != http.MethodGet || r.URL.Path != "/proxy/network/integration/v1/sites" {
			t.Errorf("petición inesperada: %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"offset":0,"limit":25,"count":2,"totalCount":2,"data":[
			{"id":"aaa","internalReference":"sede2","name":"Sede 2"},
			{"id":"bbb","internalReference":"default","name":"Default"}]}`)
	})
	sites, err := c.Sites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s, err := DefaultSite(sites)
	if err != nil || s.ID != "bbb" {
		t.Fatalf("DefaultSite = %+v, %v", s, err)
	}
}

func TestCreateVoucherCuerpo(t *testing.T) {
	_, c := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/proxy/network/integration/v1/sites/site-1/hotspot/vouchers" {
			t.Errorf("petición inesperada: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"count": 1.0, "name": "Totem", "timeLimitMinutes": 240.0, "authorizedGuestLimit": 1.0, "rxRateLimitKbps": 5000.0}
		for k, v := range want {
			if body[k] != v {
				t.Errorf("campo %s = %v, want %v", k, body[k], v)
			}
		}
		// Los límites a 0 no se envían (UniFi rechaza 0 en las velocidades).
		for _, k := range []string{"dataUsageLimitMBytes", "txRateLimitKbps"} {
			if _, ok := body[k]; ok {
				t.Errorf("no debería enviarse %s", k)
			}
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"vouchers":[{"id":"v1","code":"1234567890","name":"Totem","timeLimitMinutes":240,"createdAt":"2026-11-12T10:00:00Z","expired":false}]}`)
	})
	v, err := c.CreateVoucher(context.Background(), "site-1", VoucherRequest{Name: "Totem", TimeLimitMinutes: 240, GuestLimit: 1, RxKbps: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if v.Code != "1234567890" || v.ID != "v1" {
		t.Errorf("voucher inesperado: %+v", v)
	}
}

func TestCreateVoucherFormatoData(t *testing.T) {
	_, c := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"data":[{"id":"v2","code":"0987654321"}]}`)
	})
	v, err := c.CreateVoucher(context.Background(), "s", VoucherRequest{Name: "x", TimeLimitMinutes: 1})
	if err != nil || v.Code != "0987654321" {
		t.Fatalf("v=%+v err=%v", v, err)
	}
}

func TestErroresHTTP(t *testing.T) {
	casos := []struct {
		status int
		pista  string
	}{
		{401, "API key no válida"},
		{403, "no tiene permiso"},
		{404, "site_id"},
		{400, "parámetros"},
		{500, "error interno"},
		{502, "error interno"},
	}
	for _, cs := range casos {
		_, c := servidor(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(cs.status)
			io.WriteString(w, `{"statusCode":`+http.StatusText(cs.status)+`,"message":"detalle"}`)
		})
		_, err := c.CreateVoucher(context.Background(), "s", VoucherRequest{Name: "x", TimeLimitMinutes: 1})
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != cs.status {
			t.Errorf("%d: error inesperado %v", cs.status, err)
			continue
		}
		if !strings.Contains(err.Error(), cs.pista) || !strings.Contains(err.Error(), "detalle") {
			t.Errorf("%d: mensaje sin pista o sin cuerpo: %v", cs.status, err)
		}
		if strings.Contains(err.Error(), claveTest) {
			t.Errorf("%d: el error contiene la API key", cs.status)
		}
	}
}

func TestRespuestaSinCodigo(t *testing.T) {
	_, c := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"vouchers":[]}`)
	})
	if _, err := c.CreateVoucher(context.Background(), "s", VoucherRequest{Name: "x", TimeLimitMinutes: 1}); !errors.Is(err, ErrBadAnswer) {
		t.Errorf("err = %v", err)
	}
}

func TestHuellaDistinta(t *testing.T) {
	llamado := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { llamado = true }))
	defer srv.Close()
	c := NewReal(Config{BaseURL: srv.URL, APIKey: claveTest, CertSHA256: strings.Repeat("0", 64)})
	_, err := c.Sites(context.Background())
	var mm *CertMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("se esperaba CertMismatchError, err = %v", err)
	}
	if mm.Got != Fingerprint(srv.Certificate().Raw) {
		t.Error("la huella recibida no es la del servidor")
	}
	if !strings.Contains(err.Error(), "¿se ha regenerado el certificado de UniFi?") {
		t.Errorf("mensaje poco claro: %v", err)
	}
	if llamado {
		t.Error("con la huella distinta no debe llegar ninguna petición (ni la API key) al servidor")
	}
}

func TestSinHuellaNiClave(t *testing.T) {
	c := NewReal(Config{BaseURL: "https://127.0.0.1:1", APIKey: "x"})
	if _, err := c.Sites(context.Background()); !errors.Is(err, ErrNoFingerprint) {
		t.Errorf("sin huella: %v", err)
	}
	c = NewReal(Config{BaseURL: "https://127.0.0.1:1", CertSHA256: strings.Repeat("a", 64)})
	if _, err := c.Sites(context.Background()); !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("sin clave: %v", err)
	}
}

func TestFetchCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	hp, err := HostPort(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	info, err := FetchCertificate(context.Background(), hp)
	if err != nil {
		t.Fatal(err)
	}
	if info.SHA256 != Fingerprint(srv.Certificate().Raw) {
		t.Error("huella distinta de la del servidor")
	}
	if len(info.Formatted) != 95 {
		t.Errorf("formato con dos puntos inesperado: %q", info.Formatted)
	}
}

func TestHostPort(t *testing.T) {
	if hp, _ := HostPort("https://198.51.100.20:11443"); hp != "198.51.100.20:11443" {
		t.Error(hp)
	}
	if hp, _ := HostPort("https://unifi.local"); hp != "unifi.local:443" {
		t.Error(hp)
	}
}

func TestMock(t *testing.T) {
	m := NewMock()
	re := regexp.MustCompile(`^\d{10}$`)
	vistos := map[string]bool{}
	for i := 0; i < 50; i++ {
		v, err := m.CreateVoucher(context.Background(), "", VoucherRequest{Name: "x", TimeLimitMinutes: 5})
		if err != nil || !re.MatchString(v.Code) {
			t.Fatalf("código mock no válido: %q %v", v.Code, err)
		}
		vistos[v.Code] = true
	}
	if len(vistos) < 45 {
		t.Error("los códigos mock deberían ser aleatorios")
	}
}

func TestProviderReutiliza(t *testing.T) {
	var p Provider
	cfg := Config{BaseURL: "https://1.2.3.4:11443", APIKey: "a", CertSHA256: "b"}
	a := p.Get(false, cfg)
	if b := p.Get(false, cfg); a != b {
		t.Error("debería reutilizar el cliente con la misma configuración")
	}
	cfg.APIKey = "otra"
	if c := p.Get(false, cfg); c == a {
		t.Error("debería crear un cliente nuevo al cambiar la clave")
	}
	if !p.Get(true, cfg).Mock() {
		t.Error("mock=true debe devolver el cliente simulado")
	}
}

func TestExplainPorCapas(t *testing.T) {
	c := NewReal(Config{BaseURL: "https://127.0.0.1:1", APIKey: "x", CertSHA256: strings.Repeat("a", 64)})
	_, err := c.Sites(context.Background())
	if l, _ := Explain(err); l != "Conexión TCP" {
		t.Errorf("puerto cerrado: capa %q (%v)", l, err)
	}
	casos := map[string]error{
		"Configuración":          ErrNoFingerprint,
		"Certificado":            &CertMismatchError{Got: "a", Want: "b"},
		"Clave / rol":            &APIError{Status: 403},
		"Sitio":                  &APIError{Status: 404},
		"Parámetros del voucher": &APIError{Status: 400},
		"UniFi":                  &APIError{Status: 500},
	}
	for want, e := range casos {
		if l, _ := Explain(e); l != want {
			t.Errorf("%v: capa %q, want %q", e, l, want)
		}
	}
}
