package voucher

// Invitados conectados y mantenimiento de los vouchers del tótem.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"totem-wifi/internal/config"
	"totem-wifi/internal/unifi"
)

// paralelo es cuántas peticiones simultáneas se hacen a UniFi en las operaciones por lotes.
const paralelo = 4

// GuestRow es un invitado tal como se muestra en /admin/invitados.
type GuestRow struct {
	unifi.Guest
	ExpiresAt time.Time // fin del acceso (cero si no se conoce)
	Method    string    // VOUCHER, API u OTHER
}

// DisconnectResult resume una desconexión por lotes.
type DisconnectResult struct {
	Done        int
	Failed      []string // descripción de cada fallo
	Invalidated int      // vouchers del tótem invalidados (solo «desconectar todos»)
}

// clientSite devuelve el cliente y el sitio, detectando el sitio si hace falta.
func (s *Service) clientSite(ctx context.Context) (unifi.Client, string, error) {
	cfg := s.cfg.Get()
	c := s.client(cfg)
	site := cfg.UniFi.SiteID
	if !c.Mock() && site == "" {
		var err error
		if site, err = s.ensureSite(ctx, c); err != nil {
			return nil, "", err
		}
	}
	return c, site, nil
}

// Guests lista los invitados conectados, con la hora a la que termina su acceso.
// includePending incluye también a quienes aún no han introducido el código.
func (s *Service) Guests(ctx context.Context, includePending bool) ([]GuestRow, error) {
	c, site, err := s.clientSite(ctx)
	if err != nil {
		return nil, err
	}
	guests, err := c.ListGuests(ctx, site)
	if err != nil {
		return nil, err
	}
	rows := make([]GuestRow, 0, len(guests))
	for _, g := range guests {
		if g.Authorized || includePending {
			rows = append(rows, GuestRow{Guest: g})
		}
	}
	// La fecha de fin solo viene en la ficha de cada invitado: se piden en paralelo.
	// Si alguna falla, la fila se muestra igualmente sin esa fecha.
	var wg sync.WaitGroup
	sem := make(chan struct{}, paralelo)
	for i := range rows {
		if !rows[i].Authorized {
			continue
		}
		wg.Add(1)
		go func(r *GuestRow) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if a, err := c.GuestDetails(ctx, site, r.ID); err == nil {
				r.ExpiresAt, r.Method = a.ExpiresAt, a.Method
			}
		}(&rows[i])
	}
	wg.Wait()
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ConnectedAt.After(rows[j].ConnectedAt) })
	return rows, nil
}

// DisconnectGuests desconecta a los invitados indicados (por su id de UniFi).
func (s *Service) DisconnectGuests(ctx context.Context, ids []string, adminIP string) (DisconnectResult, error) {
	c, site, err := s.clientSite(ctx)
	if err != nil {
		return DisconnectResult{}, err
	}
	guests, err := c.ListGuests(ctx, site)
	if err != nil {
		return DisconnectResult{}, err
	}
	byID := map[string]unifi.Guest{}
	for _, g := range guests {
		byID[g.ID] = g
	}
	var (
		res DisconnectResult
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, paralelo)
	)
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		g, ok := byID[id]
		if !ok {
			mu.Lock() // las desconexiones en curso también escriben en res
			res.Failed = append(res.Failed, "un dispositivo ya no estaba conectado")
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(g unifi.Guest) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			err := c.UnauthorizeGuest(ctx, site, g.ID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				_, hint := unifi.Explain(err)
				res.Failed = append(res.Failed, fmt.Sprintf("%s (%s): %s", guestLabel(g), g.IP, hint))
				s.log.Printf("ERROR al desconectar al invitado mac=%s ip=%s: %v", g.MAC, g.IP, err)
				return
			}
			res.Done++
			s.log.Printf("admin: invitado desconectado mac=%s ip=%s (desde %s)", g.MAC, g.IP, adminIP)
		}(g)
	}
	wg.Wait()
	sort.Strings(res.Failed)
	return res, nil
}

func guestLabel(g unifi.Guest) string {
	if g.Name != "" {
		return g.Name
	}
	return g.MAC
}

// DisconnectAll desconecta a todos los invitados autorizados y, si se pide, invalida
// después los códigos del tótem aún vigentes para que no puedan volver a entrar.
func (s *Service) DisconnectAll(ctx context.Context, invalidate bool, adminIP string) (DisconnectResult, error) {
	rows, err := s.Guests(ctx, false)
	if err != nil {
		return DisconnectResult{}, err
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	s.log.Printf("admin: desconectar a todos los invitados (%d) desde %s; invalidar códigos del tótem: %v", len(ids), adminIP, invalidate)
	res, err := s.DisconnectGuests(ctx, ids, adminIP)
	if err != nil {
		return res, err
	}
	if invalidate {
		n, err := s.InvalidateTotemVouchers(ctx)
		if err != nil {
			_, hint := unifi.Explain(err)
			res.Failed = append(res.Failed, "no se han podido invalidar los códigos del tótem: "+hint)
		}
		res.Invalidated = n
	}
	return res, nil
}

// totemVoucherFilter selecciona los vouchers creados por este tótem: su nota y la de
// los vouchers de prueba de /admin. Los creados a mano en UniFi no coinciden.
func totemVoucherFilter(cfg config.Config) string {
	note := cfg.Voucher.Note
	return fmt.Sprintf("name.in(%s, %s)", unifi.FilterString(note), unifi.FilterString(note+" (prueba admin)"))
}

// InvalidateTotemVouchers borra en UniFi los vouchers del tótem que aún no han caducado.
func (s *Service) InvalidateTotemVouchers(ctx context.Context) (int, error) {
	c, site, err := s.clientSite(ctx)
	if err != nil {
		return 0, err
	}
	filter := fmt.Sprintf("and(%s, expired.eq(false))", totemVoucherFilter(s.cfg.Get()))
	n, err := c.DeleteVouchers(ctx, site, filter)
	if err == nil {
		s.log.Printf("admin: invalidados %d códigos del tótem que seguían vigentes", n)
	}
	return n, err
}

// ExpireUnused borra en UniFi los vouchers del tótem que nadie ha usado y se crearon hace
// más de voucher.unused_expire_minutes minutos: así caducan aunque UniFi no lo haga
// (la duración de un voucher solo empieza a contar desde su primer uso).
// No toca los vouchers usados ni los creados a mano. Devuelve cuántos ha borrado.
func (s *Service) ExpireUnused(ctx context.Context) (int, error) {
	cfg := s.cfg.Get()
	mins := cfg.Voucher.UnusedExpireMinutes
	if mins <= 0 || cfg.UniFi.Mock {
		return 0, nil
	}
	c, site, err := s.clientSite(ctx)
	if err != nil {
		return 0, err
	}
	limit := s.Now().Add(-time.Duration(mins) * time.Minute)
	filter := fmt.Sprintf("and(%s, authorizedGuestCount.eq(0), createdAt.lt(%s))", totemVoucherFilter(cfg), unifi.FilterTime(limit))
	n, err := c.DeleteVouchers(ctx, site, filter)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		s.log.Printf("caducidad: borrados %d códigos del tótem sin usar de más de %d min", n, mins)
	}
	return n, nil
}

// StartCleanup revisa cada minuto los códigos sin usar caducados (ver ExpireUnused).
// Si UniFi falla, lo anota la primera vez y después como mucho una vez por hora,
// para no llenar el log; y anota cuándo vuelve a funcionar.
func (s *Service) StartCleanup(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		var failingSince, lastLog time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			cctx, cancel := context.WithTimeout(ctx, unifi.Timeout+5*time.Second)
			_, err := s.ExpireUnused(cctx)
			cancel()
			now := s.Now()
			switch {
			case err != nil && (failingSince.IsZero() || now.Sub(lastLog) >= time.Hour):
				if failingSince.IsZero() {
					failingSince = now
				}
				lastLog = now
				s.log.Printf("ERROR al caducar los códigos sin usar (se reintenta cada minuto): %v", err)
			case err == nil && !failingSince.IsZero():
				s.log.Printf("caducidad de códigos sin usar: UniFi vuelve a responder tras %s", now.Sub(failingSince).Round(time.Minute))
				failingSince = time.Time{}
			}
		}
	}()
}

// SanitizeIDs limpia la lista de ids recibida del formulario (separados por comas).
func SanitizeIDs(raw []string) []string {
	var out []string
	for _, r := range raw {
		for _, id := range strings.Split(r, ",") {
			if id = strings.TrimSpace(id); id != "" && len(id) <= 64 {
				out = append(out, id)
			}
		}
	}
	return out
}
