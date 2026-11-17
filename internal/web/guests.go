package web

// Página /admin/invitados: invitados conectados y desconexión (uno, varios o todos).

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"totem-wifi/internal/unifi"
	"totem-wifi/internal/voucher"
)

const (
	guestsTimeout    = 30 * time.Second // listar puede requerir varias páginas y fichas
	disconnectTimout = 90 * time.Second
	maxDisconnectIDs = 2000
)

// hora formatea una fecha para la tabla: «15:04» si es de hoy, «02/01 15:04» si no.
func hora(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	t = t.Local()
	if y, m, d := time.Now().Date(); t.Year() == y && t.Month() == m && t.Day() == d {
		return t.Format("15:04")
	}
	return t.Format("02/01 15:04")
}

func metodo(m string) string {
	switch m {
	case "VOUCHER":
		return "Código"
	case "API":
		return "API"
	case "":
		return ""
	default:
		return "Otro"
	}
}

func (s *Server) adminGuests(w http.ResponseWriter, r *http.Request) {
	sc := sessionFrom(r)
	c := s.cfg.Get()
	d := adminData{
		View: "panel", Page: "invitados.html", Active: "invitados",
		CSRF: sc.sess.csrf, Cfg: c,
		ShowPending: r.URL.Query().Get("pendientes") == "1",
		Flash:       s.sessions.popFlash(sc.sess),
	}
	ctx, cancel := context.WithTimeout(r.Context(), guestsTimeout)
	defer cancel()
	rows, err := s.svc.Guests(ctx, true)
	if err != nil {
		layer, hint := unifi.Explain(err)
		d.GuestsError = fmt.Sprintf("[%s] %s", layer, hint)
		s.log.Printf("admin: no se pueden listar los invitados: %v", err)
	}
	for _, g := range rows {
		if !g.Authorized {
			d.PendingCount++
			if !d.ShowPending {
				continue
			}
		}
		d.Guests = append(d.Guests, g)
	}
	s.render(w, http.StatusOK, d)
}

// resumen redacta el resultado de una desconexión para el administrador.
func resumen(res voucher.DisconnectResult, all bool) string {
	var b strings.Builder
	switch res.Done {
	case 0:
		b.WriteString("No se ha desconectado a nadie.")
	case 1:
		b.WriteString("1 invitado desconectado.")
	default:
		fmt.Fprintf(&b, "%d invitados desconectados.", res.Done)
	}
	if all && res.Invalidated > 0 {
		fmt.Fprintf(&b, " %d códigos del tótem invalidados: no se pueden volver a usar.", res.Invalidated)
	}
	if len(res.Failed) > 0 {
		fmt.Fprintf(&b, " Fallos (%d): %s.", len(res.Failed), strings.Join(res.Failed, "; "))
	}
	if res.Done > 0 && (!all || res.Invalidated == 0) {
		b.WriteString(" Si su código sigue vigente, pueden volver a entrar escribiéndolo en el portal.")
	}
	return b.String()
}

func (s *Server) apiDisconnect(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	r.ParseForm() //nolint:errcheck // si falla, no habrá ids y se responde con el aviso
	ids := voucher.SanitizeIDs(r.PostForm["ids"])
	if len(ids) == 0 {
		writeJSON(w, http.StatusOK, apiResult{OK: false, Message: "No hay ningún invitado seleccionado."})
		return
	}
	if len(ids) > maxDisconnectIDs {
		ids = ids[:maxDisconnectIDs]
	}
	ctx, cancel := context.WithTimeout(r.Context(), disconnectTimout)
	defer cancel()
	res, err := s.svc.DisconnectGuests(ctx, ids, clientIP(r).String())
	if err != nil {
		layer, hint := unifi.Explain(err)
		writeJSON(w, http.StatusOK, apiResult{OK: false, Layer: layer, Message: hint})
		return
	}
	msg := resumen(res, false)
	s.sessions.setFlash(sessionFrom(r).sess, msg)
	writeJSON(w, http.StatusOK, apiResult{OK: res.Done > 0, Message: msg})
}

func (s *Server) apiDisconnectAll(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r).String()
	if d := s.logins.blocked(ip); d > 0 {
		writeJSON(w, http.StatusOK, apiResult{OK: false, Message: fmt.Sprintf("Demasiados intentos fallidos. Espere %d minutos.", int(d.Minutes())+1)})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(s.cfg.Get().AdminPasswordHash), []byte(r.PostFormValue("password"))) != nil {
		s.logins.fail(ip)
		s.log.Printf("admin: contraseña incorrecta al desconectar a todos los invitados desde %s", ip)
		writeJSON(w, http.StatusOK, apiResult{OK: false, Message: "Contraseña de administración incorrecta."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), disconnectTimout)
	defer cancel()
	res, err := s.svc.DisconnectAll(ctx, r.PostFormValue("invalidar") == "1", ip)
	if err != nil {
		layer, hint := unifi.Explain(err)
		writeJSON(w, http.StatusOK, apiResult{OK: false, Layer: layer, Message: hint})
		return
	}
	msg := resumen(res, true)
	s.sessions.setFlash(sessionFrom(r).sess, msg)
	writeJSON(w, http.StatusOK, apiResult{OK: len(res.Failed) == 0, Message: msg})
}
