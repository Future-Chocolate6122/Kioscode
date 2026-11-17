package config

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultEsValida(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("la configuración por defecto debería ser válida: %v", err)
	}
	if !c.UniFi.Mock {
		t.Error("por defecto debe arrancar en modo simulación")
	}
	if c.Voucher.SSID != "WifiPublica" {
		t.Errorf("SSID por defecto inesperado: %q", c.Voucher.SSID)
	}
}

func TestLoadCreaFicheroPorDefecto(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	st, created, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("debería indicar que ha creado el fichero")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("config.json no debería ser legible por otros: %v", fi.Mode().Perm())
	}
	// Segunda carga: lee lo guardado.
	st2, created, err := Load(path)
	if err != nil || created {
		t.Fatalf("segunda carga: created=%v err=%v", created, err)
	}
	if st2.Get().Listen != st.Get().Listen {
		t.Error("la segunda carga no coincide")
	}
}

func TestParseCamposAusentesTomanDefecto(t *testing.T) {
	c, err := Parse([]byte(`{"listen":"127.0.0.1:9000","unifi":{"base_url":"https://192.0.2.10:11443"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:9000" || c.UniFi.BaseURL != "https://192.0.2.10:11443" {
		t.Errorf("no se han leído los campos: %+v", c)
	}
	if c.Voucher.Minutes != 240 || !c.UniFi.Mock {
		t.Errorf("los campos ausentes deberían tener el valor por defecto: %+v", c)
	}
}

func TestParseRechazaCamposDesconocidos(t *testing.T) {
	if _, err := Parse([]byte(`{"lisen":"0.0.0.0:8080"}`)); err == nil {
		t.Error("una errata en un campo debería dar error")
	}
}

func TestValidateErrores(t *testing.T) {
	casos := map[string]func(*Config){
		"listen":            func(c *Config) { c.Listen = "8080" },
		"allowed_clients":   func(c *Config) { c.AllowedClients = []string{"192.0.2.0/40"} },
		"admin_allowed":     func(c *Config) { c.AdminAllowedClients = []string{"no-es-ip"} },
		"base_url http":     func(c *Config) { c.UniFi.BaseURL = "http://1.2.3.4:11443" },
		"base_url con ruta": func(c *Config) { c.UniFi.BaseURL = "https://1.2.3.4:11443/proxy/network" },
		"huella":            func(c *Config) { c.UniFi.CertSHA256 = "abcd" },
		"ssid vacío":        func(c *Config) { c.Voucher.SSID = " " },
		"ssid largo":        func(c *Config) { c.Voucher.SSID = strings.Repeat("x", 33) },
		"nota":              func(c *Config) { c.Voucher.Note = "" },
		"minutos 0":         func(c *Config) { c.Voucher.Minutes = 0 },
		"minutos máx":       func(c *Config) { c.Voucher.Minutes = 1000001 },
		"rx":                func(c *Config) { c.Voucher.RxKbps = 1 },
		"tx":                func(c *Config) { c.Voucher.TxKbps = 100001 },
		"datos":             func(c *Config) { c.Voucher.DataMB = -1 },
		"display":           func(c *Config) { c.Kiosk.DisplaySeconds = 5 },
		"límite diario":     func(c *Config) { c.Kiosk.DailyLimit = 0 },
		"logo con ruta":     func(c *Config) { c.Kiosk.LogoPath = "../secreto.svg" },
		"fondo con ruta":    func(c *Config) { c.Kiosk.BackgroundImage = `C:\fondo.jpg` },
	}
	for nombre, mod := range casos {
		t.Run(nombre, func(t *testing.T) {
			c := Default()
			mod(&c)
			if err := c.Validate(); err == nil {
				t.Error("debería ser inválida")
			}
		})
	}
}

func TestValidateNormaliza(t *testing.T) {
	c := Default()
	c.UniFi.BaseURL = "https://198.51.100.20:11443/"
	c.UniFi.CertSHA256 = strings.ToUpper(strings.Repeat("ab:", 31) + "ab")
	c.Voucher.ExtraSSIDs = []SSID{{SSID: "  "}, {SSID: " Actos "}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.UniFi.BaseURL != "https://198.51.100.20:11443" {
		t.Errorf("base_url sin normalizar: %q", c.UniFi.BaseURL)
	}
	if c.UniFi.CertSHA256 != strings.Repeat("ab", 32) {
		t.Errorf("huella sin normalizar: %q", c.UniFi.CertSHA256)
	}
	if len(c.Voucher.ExtraSSIDs) != 1 || c.Voucher.ExtraSSIDs[0].SSID != "Actos" {
		t.Errorf("extra_ssids sin limpiar: %+v", c.Voucher.ExtraSSIDs)
	}
}

func TestSSIDsSinDuplicados(t *testing.T) {
	c := Default()
	c.Voucher.ExtraSSIDs = []SSID{{SSID: "WifiPublica"}, {SSID: "Actos", Password: "x"}}
	got := c.SSIDs()
	if len(got) != 2 || got[0].SSID != "WifiPublica" || got[1].SSID != "Actos" {
		t.Errorf("SSIDs inesperados: %+v", got)
	}
	if _, ok := c.FindSSID("OtraRed"); ok {
		t.Error("no debería encontrar un SSID no configurado")
	}
}

func TestAllowlist(t *testing.T) {
	l, err := ParseAllowlist([]string{"192.0.2.0/24", "198.51.100.10"})
	if err != nil {
		t.Fatal(err)
	}
	casos := map[string]bool{
		"192.0.2.10":         true,
		"198.51.100.10":      true,
		"198.51.100.11":      false,
		"203.0.113.1":        false,
		"127.0.0.1":          true, // loopback siempre
		"::1":                true,
		"::ffff:192.0.2.3":   true, // IPv4 mapeada en IPv6
		"::ffff:203.0.113.1": false,
	}
	for ip, want := range casos {
		if got := l.Allows(netip.MustParseAddr(ip)); got != want {
			t.Errorf("%s: got %v, want %v", ip, got, want)
		}
	}
	vacia, _ := ParseAllowlist(nil)
	if vacia.Allows(netip.MustParseAddr("192.0.2.2")) || !vacia.Allows(netip.MustParseAddr("127.0.0.1")) {
		t.Error("una lista vacía solo debe permitir localhost")
	}
}

func TestUpdateValidaYGuarda(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	st, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(c *Config) error { c.Voucher.Minutes = 0; return nil }); err == nil {
		t.Fatal("Update debería rechazar una configuración inválida")
	}
	if st.Get().Voucher.Minutes != 240 {
		t.Error("una actualización rechazada no debe cambiar nada")
	}
	if err := st.Update(func(c *Config) error { c.UniFi.SiteID = "abc"; return nil }); err != nil {
		t.Fatal(err)
	}
	var enDisco Config
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &enDisco); err != nil {
		t.Fatal(err)
	}
	if enDisco.UniFi.SiteID != "abc" {
		t.Error("el cambio no se ha guardado en disco")
	}
}

func TestGetDevuelveCopia(t *testing.T) {
	st := NewMemoryStore(Default())
	c := st.Get()
	c.AllowedClients[0] = "1.1.1.1"
	if st.Get().AllowedClients[0] == "1.1.1.1" {
		t.Error("Get debe devolver una copia independiente")
	}
}

func TestTextosYCondiciones(t *testing.T) {
	c := Default()
	if c.Kiosk.TermsEnabled || c.Kiosk.HeaderName() != c.Kiosk.Title || c.Kiosk.TermsVersion() != "" {
		t.Error("por defecto: sin condiciones y la cabecera muestra el título")
	}
	c.Kiosk.HeaderText = "  Servicio de Wi-Fi  "
	c.Kiosk.Intro = "Línea 1  \r\nLínea 2\r\n"
	c.Kiosk.TermsEnabled = true
	c.Kiosk.TermsText = "\r\n1. Uso responsable.\r\n\r\n2. Sin garantías.  \r\n"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Kiosk.HeaderName() != "Servicio de Wi-Fi" {
		t.Errorf("cabecera: %q", c.Kiosk.HeaderName())
	}
	if c.Kiosk.Intro != "Línea 1\nLínea 2" || c.Kiosk.TermsText != "1. Uso responsable.\n\n2. Sin garantías." {
		t.Errorf("textos sin normalizar: %q / %q", c.Kiosk.Intro, c.Kiosk.TermsText)
	}
	v1 := c.Kiosk.TermsVersion()
	if len(v1) != 8 {
		t.Errorf("versión de las condiciones: %q", v1)
	}
	c.Kiosk.TermsText += " Editado."
	if c.Kiosk.TermsVersion() == v1 {
		t.Error("la versión debe cambiar al editar el texto")
	}
}

func TestTextosLimites(t *testing.T) {
	casos := map[string]func(*Config){
		"condiciones vacías": func(c *Config) { c.Kiosk.TermsEnabled = true; c.Kiosk.TermsText = "  " },
		"título largo":       func(c *Config) { c.Kiosk.Title = strings.Repeat("á", MaxTitleLen+1) },
		"cabecera larga":     func(c *Config) { c.Kiosk.HeaderText = strings.Repeat("x", MaxTitleLen+1) },
		"intro larga":        func(c *Config) { c.Kiosk.Intro = strings.Repeat("x", MaxIntroLen+1) },
		"condiciones largas": func(c *Config) { c.Kiosk.TermsText = strings.Repeat("x", MaxTermsLen+1) },
	}
	for nombre, mod := range casos {
		c := Default()
		mod(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: debería ser inválida", nombre)
		}
	}
	c := Default()
	c.Kiosk.Title = strings.Repeat("á", MaxTitleLen) // se cuentan caracteres, no bytes
	if err := c.Validate(); err != nil {
		t.Errorf("un título de %d caracteres con tildes debe valer: %v", MaxTitleLen, err)
	}
}
