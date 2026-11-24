package firewall

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"kioscode/internal/config"
)

var hexComment = hex.EncodeToString([]byte(Comment))

const reglasEjemplo = `*filter
:ufw-user-input - [0:0]
### RULES ###

### tuple ### allow tcp 22 0.0.0.0/0 any 0.0.0.0/0 in
-A ufw-user-input -p tcp --dport 22 -j ACCEPT

### tuple ### allow tcp 8080 0.0.0.0/0 any 192.0.2.0/24 in comment=4b696f73636f6465
-A ufw-user-input -p tcp --dport 8080 -s 192.0.2.0/24 -j ACCEPT

### tuple ### allow tcp 8080 0.0.0.0/0 any 203.0.113.9 in comment=4b696f73636f6465
### tuple ### allow tcp 8080 0.0.0.0/0 any 203.0.113.0/24 in
### tuple ### allow tcp 9090 0.0.0.0/0 any 203.0.113.11 in comment=6f74726f
### END RULES ###
`

func TestParseUserRulesSoloLasSuyas(t *testing.T) {
	got := ParseUserRules([]byte(reglasEjemplo))
	if len(got) != 2 || got[0] != (Rule{"192.0.2.0/24", 8080}) || got[1] != (Rule{"203.0.113.9", 8080}) {
		t.Errorf("reglas leídas: %+v", got)
	}
	if hexComment != "4b696f73636f6465" {
		t.Errorf("el comentario en hexadecimal no coincide con el que escribe ufw: %s", hexComment)
	}
}

func TestDesired(t *testing.T) {
	c := config.Default()
	c.AllowedClients = []string{"127.0.0.1", "192.0.2.0/24", "198.51.100.10", "192.0.2.0/24"}
	c.AdminAllowedClients = []string{"::1", "198.51.100.10", "fd00::/64"}
	got, err := Desired(c)
	if err != nil {
		t.Fatal(err)
	}
	want := []Rule{{"192.0.2.0/24", 8080}, {"198.51.100.10", 8080}, {"fd00::/64", 8080}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDesiredRechazaPuertosPrivilegiados(t *testing.T) {
	c := config.Default()
	c.Listen = "0.0.0.0:22" // config.json lo puede escribir el usuario sin privilegios
	if _, err := Desired(c); err == nil {
		t.Error("no debe gestionar puertos por debajo de 1024 (p. ej. SSH)")
	}
}

func TestDesiredLimitaEntradas(t *testing.T) {
	c := config.Default()
	c.AllowedClients = nil
	for i := 0; i < MaxEntries+1; i++ {
		c.AllowedClients = append(c.AllowedClients, "203.0.113."+strconv.Itoa(i))
	}
	if _, err := Desired(c); err == nil {
		t.Error("debería limitar el número de reglas")
	}
}

type ufwFalso struct {
	llamadas [][]string
	falla    string
}

func (u *ufwFalso) run(args ...string) (string, error) {
	u.llamadas = append(u.llamadas, args)
	if u.falla != "" && strings.Contains(strings.Join(args, " "), u.falla) {
		return "ERROR: algo falló", errors.New("exit status 1")
	}
	return "Rules updated", nil
}

func rutas(t *testing.T, reglas, ufwConf, defUFW string) Paths {
	dir := t.TempDir()
	p := Paths{
		UserRules:  []string{filepath.Join(dir, "user.rules"), filepath.Join(dir, "user6.rules")},
		UFWConf:    filepath.Join(dir, "ufw.conf"),
		DefaultUFW: filepath.Join(dir, "default"),
	}
	os.WriteFile(p.UserRules[0], []byte(reglas), 0o644)
	os.WriteFile(p.UFWConf, []byte(ufwConf), 0o644)
	os.WriteFile(p.DefaultUFW, []byte(defUFW), 0o644)
	return p
}

func TestSyncAnadeYQuitaSoloLoNecesario(t *testing.T) {
	c := config.Default()
	c.AllowedClients = []string{"192.0.2.0/24", "198.51.100.0/24"}
	c.AdminAllowedClients = []string{"127.0.0.1"}
	u := &ufwFalso{}
	st := Sync(c, rutas(t, reglasEjemplo, "ENABLED=yes\n", "IPV6=yes\n"), u.run)
	if !st.OK || !st.UFWActive {
		t.Fatalf("estado: %+v", st)
	}
	if len(st.Added) != 1 || st.Added[0].Src != "198.51.100.0/24" {
		t.Errorf("añadidas: %+v", st.Added)
	}
	if len(st.Removed) != 1 || st.Removed[0].Src != "203.0.113.9" {
		t.Errorf("quitadas: %+v", st.Removed)
	}
	if len(u.llamadas) != 2 {
		t.Fatalf("llamadas a ufw: %v", u.llamadas)
	}
	del := strings.Join(u.llamadas[0], " ")
	add := strings.Join(u.llamadas[1], " ")
	if del != "delete allow from 203.0.113.9 to any port 8080 proto tcp" {
		t.Errorf("borrado: %s", del)
	}
	if add != "allow from 198.51.100.0/24 to any port 8080 proto tcp comment Kioscode" {
		t.Errorf("alta: %s", add)
	}
	for _, l := range u.llamadas {
		s := strings.Join(l, " ")
		if strings.Contains(s, " 22 ") || strings.Contains(s, "203.0.113.0/24") || strings.Contains(s, "enable") || strings.Contains(s, "disable") {
			t.Errorf("ha tocado algo que no es suyo: %s", s)
		}
	}
}

func TestSyncSinCambiosNoLlamaAUfw(t *testing.T) {
	c := config.Default()
	c.AllowedClients = []string{"192.0.2.0/24", "203.0.113.9"}
	c.AdminAllowedClients = nil
	u := &ufwFalso{}
	st := Sync(c, rutas(t, reglasEjemplo, "ENABLED=no\n", ""), u.run)
	if !st.OK || st.UFWActive || len(u.llamadas) != 0 {
		t.Errorf("st=%+v llamadas=%v", st, u.llamadas)
	}
}

func TestSyncCambioDePuerto(t *testing.T) {
	c := config.Default()
	c.Listen = "0.0.0.0:8081"
	c.AllowedClients = []string{"192.0.2.0/24"}
	c.AdminAllowedClients = nil
	u := &ufwFalso{}
	st := Sync(c, rutas(t, reglasEjemplo, "ENABLED=yes", ""), u.run)
	if len(st.Removed) != 2 || len(st.Added) != 1 || st.Added[0].Port != 8081 {
		t.Errorf("cambio de puerto: %+v", st)
	}
}

func TestSyncIPv6Desactivado(t *testing.T) {
	c := config.Default()
	c.AllowedClients = []string{"fd00::/64", "192.0.2.0/24"}
	c.AdminAllowedClients = nil
	u := &ufwFalso{}
	st := Sync(c, rutas(t, "", "ENABLED=yes", "IPV6=no\n"), u.run)
	if !st.OK || len(st.Skipped) != 1 || len(st.Added) != 1 {
		t.Errorf("IPv6 desactivado: %+v", st)
	}
}

func TestSyncInformaErrores(t *testing.T) {
	c := config.Default()
	c.AllowedClients = []string{"198.51.100.0/24"}
	c.AdminAllowedClients = nil
	u := &ufwFalso{falla: "198.51.100.0/24"}
	st := Sync(c, rutas(t, "", "ENABLED=yes", ""), u.run)
	if st.OK || !strings.Contains(st.Error, "198.51.100.0/24") {
		t.Errorf("error no informado: %+v", st)
	}
}

func TestStatusIdaYVuelta(t *testing.T) {
	path := filepath.Join(t.TempDir(), StatusFile)
	if _, err := ReadStatus(path); !errors.Is(err, ErrNotManaged) {
		t.Error("sin fichero debe indicar que no se gestiona")
	}
	if err := WriteStatus(path, Status{OK: true, Rules: []Rule{{"1.2.3.4", 8080}}}); err != nil {
		t.Fatal(err)
	}
	st, err := ReadStatus(path)
	if err != nil || !st.OK || len(st.Rules) != 1 {
		t.Errorf("%+v %v", st, err)
	}
}
