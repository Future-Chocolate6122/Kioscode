// Paquete unifi: cliente de la Integration API de UniFi Network y cliente simulado.
//
// La API key solo vive aquí y en config.json; nunca se registra ni se devuelve.
package unifi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Timeout de cada petición a UniFi (CONTEXTO.md §5).
const Timeout = 10 * time.Second

// Site es un sitio de UniFi Network.
type Site struct {
	ID                string `json:"id"`
	InternalReference string `json:"internalReference"`
	Name              string `json:"name"`
}

// VoucherRequest son los parámetros de creación. Los campos a 0 no se envían.
type VoucherRequest struct {
	Name             string
	TimeLimitMinutes int
	GuestLimit       int
	DataMB           int
	RxKbps           int
	TxKbps           int
}

// Voucher es lo que devuelve UniFi al crear un voucher.
type Voucher struct {
	ID               string `json:"id"`
	Code             string `json:"code"`
	Name             string `json:"name"`
	TimeLimitMinutes int    `json:"timeLimitMinutes"`
	CreatedAt        string `json:"createdAt"`
	Expired          bool   `json:"expired"`
}

// Client es lo que necesita el resto de la aplicación.
type Client interface {
	Sites(ctx context.Context) ([]Site, error)
	CreateVoucher(ctx context.Context, siteID string, req VoucherRequest) (Voucher, error)
	// Invitados conectados (guests.go)
	ListGuests(ctx context.Context, siteID string) ([]Guest, error)
	GuestDetails(ctx context.Context, siteID, clientID string) (GuestAuth, error)
	UnauthorizeGuest(ctx context.Context, siteID, clientID string) error
	DeleteVouchers(ctx context.Context, siteID, filter string) (int, error)
	Mock() bool
}

// Errores reconocibles para poder dar mensajes por capas.
var (
	ErrNoFingerprint = errors.New("falta la huella del certificado de UniFi (unifi.cert_sha256): " +
		"obténgala en /admin con «Obtener huella del certificado» o con «totem-wifi fingerprint HOST:PUERTO»")
	ErrNoAPIKey  = errors.New("falta la API key de UniFi (unifi.api_key): créela en Network → Settings → Control Plane → Integrations")
	ErrNoSite    = errors.New("no se encuentra el sitio «default» en UniFi: indique unifi.site_id a mano")
	ErrBadAnswer = errors.New("respuesta de UniFi no reconocida")
)

// CertMismatchError: el servidor presenta un certificado distinto del anclado.
type CertMismatchError struct{ Got, Want string }

func (e *CertMismatchError) Error() string {
	return fmt.Sprintf("huella del certificado distinta: ¿se ha regenerado el certificado de UniFi? "+
		"(recibida %s, esperada %s; si el cambio es legítimo, actualícela en /admin)", e.Got, e.Want)
}

// ConnError: no se ha podido hablar con UniFi (red, puerto, TLS, timeout).
type ConnError struct {
	URL string
	Err error
}

func (e *ConnError) Error() string {
	return fmt.Sprintf("no se puede conectar con UniFi en %s: %v", e.URL, e.Err)
}
func (e *ConnError) Unwrap() error { return e.Err }

// Explain sitúa un error en la capa en la que falla, en el orden en que conviene
// diagnosticar: configuración → TCP → certificado → clave/rol → sitio → voucher.
func Explain(err error) (layer, hint string) {
	var (
		ce *ConnError
		mm *CertMismatchError
		ae *APIError
	)
	switch {
	case err == nil:
		return "", ""
	case errors.Is(err, ErrNoFingerprint), errors.Is(err, ErrNoAPIKey):
		return "Configuración", err.Error()
	case errors.As(err, &mm):
		return "Certificado", "El servidor presenta un certificado distinto del anclado. Si UniFi se ha reinstalado o ha renovado el certificado, obtenga la huella nueva en /admin y guárdela."
	case errors.As(err, &ce):
		return "Conexión TCP", "No hay conexión con " + ce.URL + ". Compruebe la IP y el puerto (11443), que UniFi OS Server esté en marcha y que ningún cortafuegos lo bloquee."
	case errors.As(err, &ae) && (ae.Status == 401 || ae.Status == 403):
		return "Clave / rol", ae.Error()
	case errors.As(err, &ae) && ae.Status == 404, errors.Is(err, ErrNoSite):
		return "Sitio", err.Error()
	case errors.As(err, &ae) && ae.Status == 400:
		return "Parámetros del voucher", ae.Error()
	default:
		return "UniFi", err.Error()
	}
}

// APIError: UniFi ha respondido con un código HTTP de error.
type APIError struct {
	Status int
	Body   string // recortado, para el log
}

func (e *APIError) Error() string {
	var hint string
	switch e.Status {
	case http.StatusUnauthorized:
		hint = "API key no válida o revocada"
	case http.StatusForbidden:
		hint = "el usuario que creó la API key no tiene permiso (hace falta un administrador local con gestión de la red; el rol «Hotspot» no basta)"
	case http.StatusNotFound:
		hint = "site_id incorrecto o ruta de la API no disponible en esta versión"
	case http.StatusBadRequest:
		hint = "UniFi rechaza los parámetros del voucher"
	default:
		if e.Status >= 500 {
			hint = "error interno de UniFi"
		} else {
			hint = "respuesta inesperada"
		}
	}
	return fmt.Sprintf("UniFi respondió HTTP %d (%s): %s", e.Status, hint, e.Body)
}

// Fingerprint calcula la huella SHA-256 (hex en minúscula) de un certificado DER.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// FormatFingerprint muestra la huella con dos puntos (AB:CD:…), como los navegadores.
func FormatFingerprint(fp string) string {
	fp = strings.ToUpper(fp)
	var parts []string
	for i := 0; i+2 <= len(fp); i += 2 {
		parts = append(parts, fp[i:i+2])
	}
	return strings.Join(parts, ":")
}

// pinnedTLS devuelve una configuración TLS que solo acepta el certificado hoja
// cuya huella SHA-256 coincide con want. La verificación por CA se desactiva
// (el certificado es autofirmado) y se sustituye por la comparación de la huella.
func pinnedTLS(want string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // sustituido por VerifyPeerCertificate (anclaje)
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("el servidor no presenta certificado")
			}
			got := Fingerprint(rawCerts[0])
			if got != want {
				return &CertMismatchError{Got: got, Want: want}
			}
			return nil
		},
	}
}

// Config de un cliente real.
type Config struct {
	BaseURL    string // https://IP:11443
	APIKey     string
	CertSHA256 string // huella anclada (hex)
}

// Real habla con UniFi OS Server.
type Real struct {
	base   string
	apiKey string
	pin    string
	http   *http.Client
}

// NewReal crea el cliente. No conecta todavía.
func NewReal(c Config) *Real {
	tr := &http.Transport{
		Proxy:                 nil, // UniFi está en la LAN: nunca a través de un proxy
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSClientConfig:       pinnedTLS(c.CertSHA256),
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: Timeout,
		MaxIdleConns:          2,
		IdleConnTimeout:       60 * time.Second,
	}
	return &Real{
		base:   strings.TrimRight(c.BaseURL, "/") + "/proxy/network/integration/v1",
		apiKey: c.APIKey,
		pin:    c.CertSHA256,
		http: &http.Client{
			Transport: tr,
			Timeout:   Timeout,
			// La Integration API no redirige; una redirección indicaría un error de URL.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (c *Real) Mock() bool { return false }

func (c *Real) do(ctx context.Context, method, path string, body any, out any, okStatus ...int) error {
	if c.pin == "" {
		return ErrNoFingerprint
	}
	if c.apiKey == "" {
		return ErrNoAPIKey
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-KEY", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var mm *CertMismatchError
		if errors.As(err, &mm) {
			return mm
		}
		return &ConnError{URL: c.base, Err: stripURL(err)}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	ok := false
	for _, s := range okStatus {
		ok = ok || resp.StatusCode == s
	}
	if !ok {
		return &APIError{Status: resp.StatusCode, Body: trimBody(data)}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%w: %v (%s)", ErrBadAnswer, err, trimBody(data))
		}
	}
	return nil
}

// stripURL quita la URL (ya incluida en el mensaje) de un *url.Error.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func trimBody(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// Sites lista los sitios (GET /v1/sites). Sirve también para probar la clave.
func (c *Real) Sites(ctx context.Context) ([]Site, error) {
	var resp struct {
		Data []Site `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/sites?limit=200", nil, &resp, http.StatusOK); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

type createBody struct {
	Count                int    `json:"count"`
	Name                 string `json:"name"`
	TimeLimitMinutes     int    `json:"timeLimitMinutes"`
	AuthorizedGuestLimit int    `json:"authorizedGuestLimit,omitempty"`
	DataUsageLimitMBytes int    `json:"dataUsageLimitMBytes,omitempty"`
	RxRateLimitKbps      int    `json:"rxRateLimitKbps,omitempty"`
	TxRateLimitKbps      int    `json:"txRateLimitKbps,omitempty"`
}

// CreateVoucher crea un voucher (POST /v1/sites/{siteId}/hotspot/vouchers).
func (c *Real) CreateVoucher(ctx context.Context, siteID string, r VoucherRequest) (Voucher, error) {
	if siteID == "" {
		return Voucher{}, ErrNoSite
	}
	body := createBody{
		Count:                1,
		Name:                 r.Name,
		TimeLimitMinutes:     r.TimeLimitMinutes,
		AuthorizedGuestLimit: r.GuestLimit,
		DataUsageLimitMBytes: r.DataMB,
		RxRateLimitKbps:      r.RxKbps,
		TxRateLimitKbps:      r.TxKbps,
	}
	// La respuesta documentada es {"vouchers":[…]}; se acepta también {"data":[…]}
	// por si alguna versión de Network sigue el formato de los listados.
	var resp struct {
		Vouchers []Voucher `json:"vouchers"`
		Data     []Voucher `json:"data"`
	}
	path := "/sites/" + url.PathEscape(siteID) + "/hotspot/vouchers"
	if err := c.do(ctx, http.MethodPost, path, body, &resp, http.StatusCreated, http.StatusOK); err != nil {
		return Voucher{}, err
	}
	list := resp.Vouchers
	if len(list) == 0 {
		list = resp.Data
	}
	if len(list) == 0 || list[0].Code == "" {
		return Voucher{}, fmt.Errorf("%w: no contiene ningún código", ErrBadAnswer)
	}
	return list[0], nil
}

// DefaultSite elige el sitio con internalReference "default"; si solo hay uno, ese.
func DefaultSite(sites []Site) (Site, error) {
	for _, s := range sites {
		if s.InternalReference == "default" {
			return s, nil
		}
	}
	if len(sites) == 1 {
		return sites[0], nil
	}
	return Site{}, ErrNoSite
}

// mockVoucher genera un voucher falso con un código aleatorio de 10 cifras.
func mockVoucher(r VoucherRequest) (Voucher, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1e10))
	if err != nil {
		return Voucher{}, err
	}
	return Voucher{
		ID:               "mock",
		Code:             fmt.Sprintf("%010d", n.Int64()),
		Name:             r.Name,
		TimeLimitMinutes: r.TimeLimitMinutes,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// Provider reutiliza el cliente mientras no cambie su configuración, para no
// abrir un transporte HTTPS nuevo en cada voucher.
type Provider struct {
	mu   sync.Mutex
	key  string
	real *Real
	mock *MockClient // uno solo, para que recuerde a quién se ha desconectado
}

// Get devuelve el cliente adecuado para la configuración dada.
func (p *Provider) Get(mock bool, c Config) Client {
	if mock {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.mock == nil {
			p.mock = NewMock()
		}
		return p.mock
	}
	key := c.BaseURL + "\x00" + c.APIKey + "\x00" + c.CertSHA256
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.real == nil || p.key != key {
		if p.real != nil {
			p.real.http.CloseIdleConnections()
		}
		p.real = NewReal(c)
		p.key = key
	}
	return p.real
}

// CertInfo describe el certificado que presenta un servidor.
type CertInfo struct {
	SHA256     string    `json:"sha256"`
	Formatted  string    `json:"formatted"`
	Subject    string    `json:"subject"`
	Issuer     string    `json:"issuer"`
	NotBefore  time.Time `json:"not_before"`
	NotAfter   time.Time `json:"not_after"`
	SelfSigned bool      `json:"self_signed"`
}

// FetchCertificate conecta por TLS a hostport y devuelve el certificado hoja.
// Solo hace el saludo TLS: no envía ninguna petición ni la API key.
func FetchCertificate(ctx context.Context, hostport string) (CertInfo, error) {
	if _, _, err := net.SplitHostPort(hostport); err != nil {
		return CertInfo{}, fmt.Errorf("indique HOST:PUERTO (por ejemplo 198.51.100.20:11443): %w", err)
	}
	var leaf *x509.Certificate
	cfg := &tls.Config{
		InsecureSkipVerify: true, // solo se lee el certificado para mostrar su huella
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("el servidor no presenta certificado")
			}
			c, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			leaf = c
			return nil
		},
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: cfg}
	conn, err := d.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return CertInfo{}, fmt.Errorf("no se puede conectar por TLS con %s: %w", hostport, err)
	}
	conn.Close()
	if leaf == nil {
		return CertInfo{}, errors.New("no se ha recibido certificado")
	}
	fp := Fingerprint(leaf.Raw)
	return CertInfo{
		SHA256:     fp,
		Formatted:  FormatFingerprint(fp),
		Subject:    leaf.Subject.String(),
		Issuer:     leaf.Issuer.String(),
		NotBefore:  leaf.NotBefore,
		NotAfter:   leaf.NotAfter,
		SelfSigned: leaf.Subject.String() == leaf.Issuer.String(),
	}, nil
}

// HostPort extrae HOST:PUERTO de una base_url (puerto 443 si no se indica).
func HostPort(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("URL no válida: %q", baseURL)
	}
	if u.Port() == "" {
		return net.JoinHostPort(u.Hostname(), "443"), nil
	}
	return u.Host, nil
}
