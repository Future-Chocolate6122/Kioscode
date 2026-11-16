// Paquete firewall: sincroniza ufw con las IPs permitidas de config.json.
//
// Lo ejecuta como root un servicio systemd aparte (totem-wifi-firewall), que
// systemd lanza cada vez que cambia config.json. El servicio web nunca tiene
// privilegios: solo escribe config.json y lee el resultado en firewall-status.json.
//
// Reglas de seguridad del ayudante:
//   - Solo crea y borra reglas con el comentario «Totem Wi-Fi»; nunca toca las demás
//     (SSH queda a salvo).
//   - Nunca activa ni desactiva ufw.
//   - Solo acepta IPs/redes válidas, un puerto ≥ 1024 y un número limitado de entradas,
//     porque config.json lo puede escribir el usuario sin privilegios del servicio.
package firewall

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"totem-wifi/internal/config"
)

// Comment identifica las reglas que gestiona Tótem Wi-Fi.
const Comment = "Totem Wi-Fi"

// MaxEntries limita las reglas que puede crear el ayudante.
const MaxEntries = 64

// StatusFile es el nombre del fichero de resultado en el directorio de datos.
const StatusFile = "firewall-status.json"

// Rule es una regla «allow from SRC to any port PORT proto tcp».
type Rule struct {
	Src  string `json:"src"`
	Port int    `json:"port"`
}

func (r Rule) String() string { return fmt.Sprintf("%s → %d/tcp", r.Src, r.Port) }

// Desired calcula las reglas que debe haber: allowed_clients ∪ admin_allowed_clients,
// sin loopback (el tráfico local no pasa por el cortafuegos), en el puerto de «listen».
func Desired(c config.Config) ([]Rule, error) {
	_, ps, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen no válido: %w", err)
	}
	port, err := strconv.Atoi(ps)
	if err != nil || port < 1024 || port > 65535 {
		return nil, fmt.Errorf("el puerto %q no se gestiona: solo puertos entre 1024 y 65535", ps)
	}
	seen := map[string]bool{}
	var out []Rule
	for _, list := range [][]string{c.AllowedClients, c.AdminAllowedClients} {
		al, err := config.ParseAllowlist(list)
		if err != nil {
			return nil, err
		}
		for _, p := range al {
			if p.Addr().IsLoopback() {
				continue
			}
			src := p.String()
			if p.IsSingleIP() {
				src = p.Addr().String()
			}
			if !seen[src] {
				seen[src] = true
				out = append(out, Rule{Src: src, Port: port})
			}
		}
	}
	if len(out) > MaxEntries {
		return nil, fmt.Errorf("demasiadas entradas en las listas de equipos permitidos (%d; máximo %d): use redes CIDR", len(out), MaxEntries)
	}
	sortRules(out)
	return out, nil
}

func sortRules(r []Rule) {
	sort.Slice(r, func(i, j int) bool {
		if r[i].Port != r[j].Port {
			return r[i].Port < r[j].Port
		}
		return r[i].Src < r[j].Src
	})
}

// ParseUserRules extrae de /etc/ufw/user.rules (o user6.rules) las reglas con el
// comentario de Tótem Wi-Fi. Formato de ufw:
//
//	### tuple ### allow tcp 8080 0.0.0.0/0 any 192.0.2.0/24 in comment=546f74656d2057692d4669
func ParseUserRules(data []byte) []Rule {
	want := "comment=" + hex.EncodeToString([]byte(Comment))
	var out []Rule
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "### tuple ### ") {
			continue
		}
		f := strings.Fields(strings.TrimPrefix(line, "### tuple ### "))
		// action proto dport dst sport src [dapp sapp] direction comment=…
		if len(f) < 8 || f[len(f)-1] != want {
			continue
		}
		if f[0] != "allow" || f[1] != "tcp" || f[4] != "any" || f[len(f)-2] != "in" {
			continue
		}
		port, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}
		out = append(out, Rule{Src: f[5], Port: port})
	}
	return out
}

// Runner ejecuta ufw con los argumentos dados (inyectable en las pruebas).
type Runner func(args ...string) (string, error)

// UFW ejecuta el ufw del sistema en idioma C (salida predecible) y sin shell.
func UFW(args ...string) (string, error) {
	cmd := exec.Command("ufw", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Paths son los ficheros del sistema que lee el ayudante.
type Paths struct {
	UserRules  []string // user.rules y user6.rules
	UFWConf    string   // /etc/ufw/ufw.conf (ENABLED=yes)
	DefaultUFW string   // /etc/default/ufw (IPV6=yes)
}

// SystemPaths devuelve las rutas habituales en Ubuntu/Debian.
func SystemPaths() Paths {
	return Paths{
		UserRules:  []string{"/etc/ufw/user.rules", "/etc/ufw/user6.rules", "/lib/ufw/user.rules", "/lib/ufw/user6.rules"},
		UFWConf:    "/etc/ufw/ufw.conf",
		DefaultUFW: "/etc/default/ufw",
	}
}

func readKey(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			return strings.ToLower(strings.Trim(v, `"'`))
		}
	}
	return ""
}

// Status es el resultado de la última sincronización (firewall-status.json).
type Status struct {
	Time          time.Time `json:"time"`
	OK            bool      `json:"ok"`
	UFWActive     bool      `json:"ufw_active"`
	Rules         []Rule    `json:"rules"`
	Added         []Rule    `json:"added"`
	Removed       []Rule    `json:"removed"`
	Skipped       []string  `json:"skipped,omitempty"` // p. ej. IPv6 con ufw sin IPv6
	Error         string    `json:"error,omitempty"`
	ConfigModTime time.Time `json:"config_mod_time"`
}

// Sync deja en ufw exactamente las reglas Desired(c), tocando solo las suyas.
func Sync(c config.Config, p Paths, run Runner) Status {
	st := Status{Time: time.Now()}
	if _, err := exec.LookPath("ufw"); err != nil && run == nil {
		st.Error = "ufw no está instalado"
		return st
	}
	if run == nil {
		run = UFW
	}
	st.UFWActive = readKey(p.UFWConf, "ENABLED") == "yes"
	ipv6 := readKey(p.DefaultUFW, "IPV6") != "no"

	want, err := Desired(c)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	if !ipv6 {
		kept := want[:0]
		for _, r := range want {
			if a, err := netip.ParsePrefix(r.Src); (err == nil && a.Addr().Is6()) || strings.Contains(r.Src, ":") {
				st.Skipped = append(st.Skipped, r.Src+" (IPv6 desactivado en ufw)")
				continue
			}
			kept = append(kept, r)
		}
		want = kept
	}

	var have []Rule
	for _, f := range p.UserRules {
		if data, err := os.ReadFile(f); err == nil {
			have = append(have, ParseUserRules(data)...)
		}
	}
	haveSet, wantSet := map[Rule]bool{}, map[Rule]bool{}
	for _, r := range have {
		haveSet[r] = true
	}
	for _, r := range want {
		wantSet[r] = true
	}

	var errs []string
	for r := range haveSet {
		if wantSet[r] {
			continue
		}
		if out, err := run("delete", "allow", "from", r.Src, "to", "any", "port", strconv.Itoa(r.Port), "proto", "tcp"); err != nil {
			errs = append(errs, fmt.Sprintf("no se pudo quitar %s: %s", r, firstLine(out, err)))
		} else {
			st.Removed = append(st.Removed, r)
		}
	}
	for _, r := range want {
		if haveSet[r] {
			continue
		}
		if out, err := run("allow", "from", r.Src, "to", "any", "port", strconv.Itoa(r.Port), "proto", "tcp", "comment", Comment); err != nil {
			errs = append(errs, fmt.Sprintf("no se pudo añadir %s: %s", r, firstLine(out, err)))
		} else {
			st.Added = append(st.Added, r)
		}
	}
	sortRules(st.Added)
	sortRules(st.Removed)
	st.Rules = want
	if len(errs) > 0 {
		st.Error = strings.Join(errs, "; ")
		return st
	}
	st.OK = true
	return st
}

func firstLine(out string, err error) string {
	if out == "" {
		return err.Error()
	}
	if i := strings.IndexByte(out, '\n'); i > 0 {
		return out[:i]
	}
	return out
}

// WriteStatus guarda el resultado de forma atómica, legible por el servicio.
func WriteStatus(path string, st Status) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".firewall-*.tmp")
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
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ReadStatus lee firewall-status.json. ErrNotManaged si no existe (Windows,
// o Linux sin el ayudante instalado).
func ReadStatus(path string) (Status, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, ErrNotManaged
	}
	if err != nil {
		return Status{}, err
	}
	var st Status
	if err := json.Unmarshal(data, &st); err != nil {
		return Status{}, err
	}
	return st, nil
}

// ErrNotManaged: el cortafuegos no lo gestiona Tótem Wi-Fi en este equipo.
var ErrNotManaged = errors.New("cortafuegos no gestionado")
