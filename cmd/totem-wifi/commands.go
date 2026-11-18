package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"totem-wifi/internal/config"
	"totem-wifi/internal/firewall"
	"totem-wifi/internal/unifi"
)

// runFingerprint imprime la huella del certificado de HOST:PUERTO.
func runFingerprint(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "uso: totem-wifi fingerprint HOST:PUERTO   (por ejemplo 198.51.100.20:11443)")
		return 2
	}
	target := args[0]
	if strings.HasPrefix(target, "https://") {
		hp, err := unifi.HostPort(target)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ERROR:", err)
			return 2
		}
		target = hp
	}
	info, err := unifi.FetchCertificate(context.Background(), target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	fmt.Printf("Servidor:  %s\n", target)
	fmt.Printf("Sujeto:    %s\n", info.Subject)
	fmt.Printf("Emisor:    %s%s\n", info.Issuer, map[bool]string{true: "  (autofirmado)", false: ""}[info.SelfSigned])
	fmt.Printf("Válido:    %s → %s\n", info.NotBefore.Format("2006-01-02"), info.NotAfter.Format("2006-01-02"))
	fmt.Printf("SHA-256:   %s\n\n", info.Formatted)
	fmt.Printf("Valor para unifi.cert_sha256:\n%s\n", info.SHA256)
	return 0
}

// runCheck prueba la configuración y la conexión con UniFi por capas.
// No modifica config.json (el servicio puede estar en marcha).
func runCheck(configPath string) int {
	fail := false
	ok := func(f string, a ...any) { fmt.Printf("[ OK ]  "+f+"\n", a...) }
	warn := func(f string, a ...any) { fmt.Printf("[AVISO] "+f+"\n", a...) }
	bad := func(f string, a ...any) { fmt.Printf("[FALLO] "+f+"\n", a...); fail = true }

	if _, err := os.Stat(configPath); err != nil {
		bad("Configuración: no existe %s (arranque el servicio una vez o ejecute «totem-wifi init»)", configPath)
		return 1
	}
	st, _, err := config.Load(configPath)
	if err != nil {
		bad("Configuración: %v", err)
		return 1
	}
	c := st.Get()
	ok("Configuración válida: %s", configPath)
	if c.AdminPasswordHash == "" {
		warn("No hay contraseña de administración: créela abriendo http://localhost:%s/admin en este equipo", port(c.Listen))
	}
	if c.UniFi.Mock {
		warn("Modo simulación activo: el quiosco entrega códigos FALSOS")
		if c.UniFi.APIKey == "" && c.UniFi.CertSHA256 == "" {
			fmt.Println("        (no hay datos de UniFi que probar todavía)")
			return 0
		}
		fmt.Println("        Se prueba igualmente la conexión con UniFi configurada:")
	}

	// Capa 1: TCP.
	hp, err := unifi.HostPort(c.UniFi.BaseURL)
	if err != nil {
		bad("URL de UniFi: %v", err)
		return 1
	}
	conn, err := net.DialTimeout("tcp", hp, 5*time.Second)
	if err != nil {
		bad("Conexión TCP con %s: %v\n        Compruebe la IP, el puerto, que UniFi OS Server esté en marcha y el cortafuegos.", hp, err)
		return 1
	}
	conn.Close()
	ok("Conexión TCP con %s", hp)

	// Capa 2: certificado.
	info, err := unifi.FetchCertificate(context.Background(), hp)
	if err != nil {
		bad("TLS con %s: %v", hp, err)
		return 1
	}
	switch c.UniFi.CertSHA256 {
	case "":
		bad("Certificado: no hay huella anclada. La del servidor es:\n        %s\n        Guárdela en /admin («Obtener huella del certificado») o en unifi.cert_sha256.", info.SHA256)
		return 1
	case info.SHA256:
		ok("Certificado: la huella coincide (%s…)", info.Formatted[:23])
	default:
		bad("Certificado: huella distinta. ¿Se ha regenerado el certificado de UniFi?\n        Anclada:  %s\n        Servidor: %s", c.UniFi.CertSHA256, info.SHA256)
		return 1
	}

	// Capa 3: clave y rol.
	if c.UniFi.APIKey == "" {
		bad("API key: no configurada. %v", unifi.ErrNoAPIKey)
		return 1
	}
	cl := unifi.NewReal(unifi.Config{BaseURL: c.UniFi.BaseURL, APIKey: c.UniFi.APIKey, CertSHA256: c.UniFi.CertSHA256})
	sites, err := cl.Sites(context.Background())
	if err != nil {
		layer, hint := unifi.Explain(err)
		bad("%s: %s", layer, hint)
		return 1
	}
	ok("API key aceptada (%d sitio(s) visibles)", len(sites))

	// Capa 4: sitio.
	if c.UniFi.SiteID == "" {
		s, err := unifi.DefaultSite(sites)
		if err != nil {
			bad("Sitio: %v", err)
			return 1
		}
		ok("Sitio: site_id vacío; se usará «%s» (%s), que el servicio guardará al arrancar", s.Name, s.ID)
	} else {
		found := false
		for _, s := range sites {
			if s.ID == c.UniFi.SiteID {
				ok("Sitio: «%s» (%s)", s.Name, s.ID)
				found = true
			}
		}
		if !found {
			bad("Sitio: el site_id configurado (%s) no existe en este UniFi; déjelo vacío para detectarlo", c.UniFi.SiteID)
		}
	}
	if fail {
		return 1
	}
	if c.UniFi.Mock {
		fmt.Println("\nTodo correcto. Desactive el modo simulación en /admin para entregar códigos reales.")
	} else {
		fmt.Println("\nTodo correcto: el quiosco puede entregar vouchers reales.")
	}
	return 0
}

func port(listen string) string {
	if _, p, err := net.SplitHostPort(listen); err == nil {
		return p
	}
	return "8080"
}

// runInit crea config.json con los valores por defecto si no existe.
func runInit(configPath string) int {
	st, created, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	if created {
		fmt.Printf("Creada %s con los valores por defecto (modo simulación).\n", st.Path())
	} else {
		fmt.Printf("%s ya existe; no se modifica.\n", st.Path())
	}
	return 0
}

// runAllowlist imprime el puerto y las IPs/redes remotas que deben poder
// conectar (para las reglas del cortafuegos de los instaladores). Formato:
//
//	port 8080
//	addr 192.0.2.0/24
func runAllowlist(configPath string) int {
	st, _, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	c := st.Get()
	fmt.Println("port", port(c.Listen))
	seen := map[string]bool{}
	for _, list := range [][]string{c.AllowedClients, c.AdminAllowedClients} {
		al, _ := config.ParseAllowlist(list)
		for _, p := range al {
			if p.Addr().IsLoopback() || seen[p.String()] {
				continue // el tráfico local no pasa por el cortafuegos
			}
			seen[p.String()] = true
			if p.IsSingleIP() {
				fmt.Println("addr", p.Addr())
			} else {
				fmt.Println("addr", p)
			}
		}
	}
	return 0
}

// setOptions son los ajustes de instalación que admite «totem-wifi set».
type setOptions struct {
	unifiURL, totems, adminIPs string
	given                      []string // opciones indicadas en la línea de comandos
}

func (o setOptions) has(name string) bool {
	for _, g := range o.given {
		if g == name {
			return true
		}
	}
	return false
}

func splitList(s string) []string {
	out := []string{} // nunca nil: una lista vacía se guarda como []
	return append(out, strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' })...)
}

// runSet ajusta UniFi, tótems y administración en config.json (lo crea si no existe).
// La API key no se admite aquí a propósito: quedaría en el historial de la consola.
func runSet(configPath string, o setOptions) int {
	if !o.has("unifi-url") && !o.has("totems") && !o.has("admin-ips") {
		fmt.Fprintln(os.Stderr, "uso: totem-wifi set [-unifi-url URL] [-totems LISTA] [-admin-ips LISTA]")
		return 2
	}
	st, _, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	var changes []string
	err = st.Update(func(c *config.Config) error {
		if o.has("unifi-url") {
			u := strings.TrimRight(strings.TrimSpace(o.unifiURL), "/")
			if u != c.UniFi.BaseURL {
				// Otro servidor: su huella y su sitio son distintos.
				c.UniFi.BaseURL, c.UniFi.CertSHA256, c.UniFi.SiteID = u, "", ""
				changes = append(changes, "UniFi: "+u+" (huella y sitio pendientes de obtener en /admin)")
			}
		}
		if o.has("totems") {
			c.AllowedClients = splitList(o.totems)
			changes = append(changes, "tótems: "+strings.Join(c.AllowedClients, ", "))
		}
		if o.has("admin-ips") {
			c.AdminAllowedClients = splitList(o.adminIPs)
			changes = append(changes, "administración desde: localhost "+strings.Join(c.AdminAllowedClients, " "))
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	if len(changes) == 0 {
		fmt.Println("Sin cambios.")
		return 0
	}
	fmt.Println("Configuración actualizada (" + st.Path() + "):")
	for _, c := range changes {
		fmt.Println("  -", c)
	}
	fmt.Println("Si el servicio está en marcha, reinícielo para aplicar los cambios: totem-wifi restart")
	return 0
}

// runFirewallSync ajusta ufw a la configuración y deja el resultado en el
// directorio de datos para que /admin lo muestre. Lee config.json sin modificarlo.
func runFirewallSync(configPath, dataDir string) int {
	if runtime.GOOS != "linux" {
		fmt.Fprintln(os.Stderr, "firewall-sync solo está disponible en Linux (ufw)")
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "ERROR: firewall-sync debe ejecutarse como root (sudo)")
		return 1
	}
	statusPath := filepath.Join(dataDir, firewall.StatusFile)
	var st firewall.Status
	data, err := os.ReadFile(configPath)
	var fi os.FileInfo
	if err == nil {
		fi, err = os.Stat(configPath)
	}
	if err != nil {
		st = firewall.Status{Time: time.Now(), Error: "no se puede leer la configuración: " + err.Error()}
	} else if cfg, perr := config.Parse(data); perr != nil {
		st = firewall.Status{Time: time.Now(), Error: perr.Error()}
	} else {
		st = firewall.Sync(cfg, firewall.SystemPaths(), nil)
	}
	if fi != nil {
		st.ConfigModTime = fi.ModTime()
	}
	if err := firewall.WriteStatus(statusPath, st); err != nil {
		fmt.Fprintln(os.Stderr, "AVISO: no se puede guardar", statusPath+":", err)
	}
	for _, r := range st.Removed {
		fmt.Println("quitada:  ", r)
	}
	for _, r := range st.Added {
		fmt.Println("añadida:  ", r)
	}
	for _, s := range st.Skipped {
		fmt.Println("omitida:  ", s)
	}
	if !st.OK {
		fmt.Fprintln(os.Stderr, "ERROR:", st.Error)
		return 1
	}
	if !st.UFWActive {
		fmt.Println("AVISO: ufw está desactivado; las reglas quedan preparadas pero no tienen efecto (sudo ufw enable, permitiendo antes SSH).")
	}
	fmt.Printf("Cortafuegos sincronizado: %d regla(s) de Tótem Wi-Fi.\n", len(st.Rules))
	return 0
}
