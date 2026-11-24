package main

import (
	"strings"
	"testing"
	"text/template"
)

// La plantilla de la unidad systemd debe compilar y llevar el endurecimiento pedido.
func TestUnidadSystemd(t *testing.T) {
	unit := strings.ReplaceAll(systemdUnit, "@RW@", "/var/lib/kioscode /etc/kioscode")
	tf := template.FuncMap{
		"cmd":       func(s string) string { return `"` + s + `"` },
		"cmdEscape": func(s string) string { return s },
	}
	tmpl, err := template.New("").Funcs(tf).Parse(unit)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	err = tmpl.Execute(&b, map[string]any{
		"Description": "Kioscode",
		"Path":        "/opt/kioscode/kioscode",
		"Arguments":   []string{"-config", "/etc/kioscode/config.json", "-data", "/var/lib/kioscode"},
		"UserName":    "kioscode",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		`ExecStart=/opt/kioscode/kioscode "-config" "/etc/kioscode/config.json" "-data" "/var/lib/kioscode"`,
		"User=kioscode", "ProtectSystem=strict", "ProtectHome=yes", "NoNewPrivileges=yes",
		"ReadWritePaths=/var/lib/kioscode /etc/kioscode", "Restart=always", "WantedBy=multi-user.target",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("falta %q en la unidad:\n%s", want, out)
		}
	}
}

func TestResolvePathsConFlags(t *testing.T) {
	cp, dd := resolvePaths("/tmp/x/config.json", "", false)
	if cp != "/tmp/x/config.json" || dd != "/tmp/x" {
		t.Errorf("%s %s", cp, dd)
	}
	cp, dd = resolvePaths("", "", true)
	if cp != "/etc/kioscode/config.json" || dd != "/var/lib/kioscode" {
		t.Errorf("rutas de servicio en Linux: %s %s", cp, dd)
	}
}
