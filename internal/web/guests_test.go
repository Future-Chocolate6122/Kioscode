package web

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"kioscode/internal/voucher"
)

func idsEnPagina(body string) []string {
	var ids []string
	for _, m := range regexp.MustCompile(`data-desconectar="([^"]+)"`).FindAllStringSubmatch(body, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

func TestPaginaInvitados(t *testing.T) {
	e := nuevoEntorno(t, nil) // simulación: 6 invitados autorizados y 1 pendiente de código
	if w := e.peticion("GET", "/admin/invitados", "127.0.0.1", "", nil); w.Code != 303 {
		t.Errorf("sin sesión debe redirigir a /admin: %d", w.Code)
	}
	n := entrar(t, e)
	body := n.hacer("GET", "/admin/invitados", nil, nil).Body.String()
	for _, want := range []string{`class="pestanas"`, `href="/admin/invitados" class="activa"`, "Modo simulación", "invitados conectados", "Mostrar también 1 pendiente de código", "Desconectar todos…", "02:00:5e:"} {
		if !strings.Contains(body, want) {
			t.Errorf("falta %q", want)
		}
	}
	if len(idsEnPagina(body)) != 6 || strings.Contains(body, "Pendiente de código") {
		t.Errorf("por defecto solo los autorizados: %d", len(idsEnPagina(body)))
	}
	conPend := n.hacer("GET", "/admin/invitados?pendientes=1", nil, nil).Body.String()
	if !strings.Contains(conPend, "Pendiente de código") || !strings.Contains(conPend, "Ocultar los pendientes") {
		t.Error("con ?pendientes=1 deben verse los pendientes")
	}
	// La página de configuración también lleva las pestañas.
	if !strings.Contains(n.hacer("GET", "/admin", nil, nil).Body.String(), `href="/admin" class="activa"`) {
		t.Error("faltan las pestañas en /admin")
	}
}

func TestDesconectarInvitados(t *testing.T) {
	e := nuevoEntorno(t, nil)
	n := entrar(t, e)
	ids := idsEnPagina(n.hacer("GET", "/admin/invitados", nil, nil).Body.String())

	// Sin token CSRF no se hace nada.
	if w := n.hacer("POST", "/admin/api/guests/disconnect", url.Values{"ids": {ids[0]}}, nil); w.Code != 403 {
		t.Errorf("sin CSRF: %d", w.Code)
	}
	r := n.api("/admin/api/guests/disconnect", url.Values{"ids": {ids[0] + "," + ids[1]}})
	if !r.OK || !strings.Contains(r.Message, "2 invitados desconectados") || !strings.Contains(r.Message, "No pueden volver a entrar con el mismo código.") {
		t.Fatalf("desconectar dos: %+v", r)
	}
	body := n.hacer("GET", "/admin/invitados", nil, nil).Body.String()
	if len(idsEnPagina(body)) != 4 || !strings.Contains(body, "2 invitados desconectados") {
		t.Errorf("tras desconectar deben quedar 4 y verse el resumen: %d", len(idsEnPagina(body)))
	}
	if r := n.api("/admin/api/guests/disconnect", url.Values{"ids": {""}}); r.OK {
		t.Error("sin selección no debe hacer nada")
	}
	if !strings.Contains(e.logs.String(), "invitado desconectado mac=") {
		t.Error("la desconexión debe quedar en el log")
	}
}

func TestDesconectarTodosPideContraseña(t *testing.T) {
	e := nuevoEntorno(t, nil)
	n := entrar(t, e)
	if r := n.api("/admin/api/guests/disconnect-all", url.Values{"password": {"incorrecta"}, "invalidar": {"1"}}); r.OK || !strings.Contains(r.Message, "incorrecta") {
		t.Fatalf("contraseña incorrecta: %+v", r)
	}
	if len(idsEnPagina(n.hacer("GET", "/admin/invitados", nil, nil).Body.String())) != 6 {
		t.Fatal("con la contraseña incorrecta no debe desconectarse a nadie")
	}
	r := n.api("/admin/api/guests/disconnect-all", url.Values{"password": {"contraseña-larga"}, "invalidar": {"1"}})
	if !r.OK || !strings.Contains(r.Message, "6 invitados desconectados") {
		t.Fatalf("desconectar todos: %+v", r)
	}
	body := n.hacer("GET", "/admin/invitados", nil, nil).Body.String()
	if len(idsEnPagina(body)) != 0 || !strings.Contains(body, "No hay invitados conectados") {
		t.Error("no deben quedar invitados autorizados")
	}
	// Cinco contraseñas incorrectas bloquean también esta acción.
	for i := 0; i < loginMaxFails; i++ {
		n.api("/admin/api/guests/disconnect-all", url.Values{"password": {"mal"}})
	}
	if r := n.api("/admin/api/guests/disconnect-all", url.Values{"password": {"contraseña-larga"}}); r.OK || !strings.Contains(r.Message, "Demasiados intentos") {
		t.Errorf("bloqueo por intentos: %+v", r)
	}
}

func TestResumenSegunDispositivosPorCodigo(t *testing.T) {
	res := voucher.DisconnectResult{Done: 2}
	if m := resumen(res, false, 1); !strings.Contains(m, "No pueden volver a entrar con el mismo código.") || strings.Contains(m, "plazas") {
		t.Errorf("con 1 dispositivo: %q", m)
	}
	if m := resumen(res, false, 3); !strings.Contains(m, "plazas que queden") {
		t.Errorf("con 3 dispositivos: %q", m)
	}
	if m := resumen(voucher.DisconnectResult{Done: 2, Invalidated: 5}, true, 3); strings.Contains(m, "plazas") || !strings.Contains(m, "5 códigos del tótem invalidados") {
		t.Errorf("con invalidación: %q", m)
	}
}
